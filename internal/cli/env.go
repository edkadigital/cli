package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

// envName is the rule Edka's console and API apply to variable names.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// maxSecretValue is Kubernetes' size limit for a Secret's data.
const maxSecretValue = 1 << 20

// deploymentEnv holds a deployment's variables and the names of its secrets,
// in the order its stored configuration lists them.
type deploymentEnv struct {
	id, name string
	// generation is the spec generation the lists were read at, or 0 when Edka
	// reports none.
	generation int
	variables  []map[string]any
	secrets    []string
	// read is the deployment as Edka returned it. A plan changes the lists
	// above, so a diff compares with this.
	read []byte
}

// envPlan makes the settings body that changes the lists it is given, and the
// line that reports the change. A nil body means the lists need no change.
type envPlan func(env *deploymentEnv) (body map[string]any, done string, err error)

// envAttempts is how many times a change is sent while Edka refuses it because
// the deployment changed after its lists were read.
const envAttempts = 3

func (e *deploymentEnv) variable(key string) bool {
	return slices.ContainsFunc(e.variables, func(v map[string]any) bool { return text(v, "name") == key })
}

// withoutVariables returns the variables not named in keys. It is never nil:
// Edka rejects a null list.
func (e *deploymentEnv) withoutVariables(keys []string) []map[string]any {
	kept := []map[string]any{}
	for _, v := range e.variables {
		if !slices.Contains(keys, text(v, "name")) {
			kept = append(kept, v)
		}
	}
	return kept
}

// secretItems lists secret names as the settings route takes them; values
// travel separately, in secret_values.
func secretItems(names []string) []map[string]string {
	items := []map[string]string{}
	for _, name := range names {
		items = append(items, map[string]string{"name": name, "value": ""})
	}
	return items
}

func (a *App) addEnv(root *cobra.Command) {
	env := a.envCommand()
	env.GroupID = "work"
	root.AddCommand(env)
}

// envCommand builds `env` for a deployment's variables and secrets. A settings
// request replaces each list it sends, so a change reads both lists and sends
// back in full each list it changes, with the generation it read them at.
func (a *App) envCommand() *cobra.Command {
	list := func(cmd *cobra.Command, _ []string) error {
		c, err := a.resolveDeployment(cmd.Context(), "")
		if err != nil {
			return err
		}
		env, err := a.readEnv(cmd.Context(), c)
		if err != nil {
			return err
		}
		return a.showEnv(env)
	}
	env := &cobra.Command{Use: "env", Short: "List and change a deployment's variables and secrets", Long: "List and change the environment variables and secrets of the linked deployment,\nor of the one --deployment names. Each change starts a rollout; --wait follows it.\n\nSecret values stay hidden. `edka env` lists secrets by name only, and `set --secret`\nreads each value at a hidden prompt, or one value from stdin without its final\nnewline. Values never go on the command line, where shell history keeps them.", Example: "  edka env\n  edka env set PORT=8080 LOG_LEVEL=debug --wait\n  edka env set --secret DATABASE_URL\n  printf %s \"$TOKEN\" | edka env set --secret API_TOKEN\n  edka env unset LOG_LEVEL", Args: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return a.unknownCommand(cmd, args, len(args))
		}
		return nil
	}, RunE: list}
	var secret, wait, diff bool
	var waitTimeout time.Duration
	set := &cobra.Command{Use: "set KEY=value...", Short: "Set variables, or secrets with --secret", Args: cobra.MinimumNArgs(1), Example: "  edka env set PORT=8080 LOG_LEVEL=debug\n  edka env set PORT=9090 --diff\n  edka env set --secret DATABASE_URL SESSION_KEY\n  edka env set --secret API_TOKEN < token.txt", RunE: func(cmd *cobra.Command, args []string) error {
		if wait && waitTimeout <= 0 {
			return fmt.Errorf("wait-timeout must be positive")
		}
		keys, values, err := envAssignments(args, secret)
		if err != nil {
			return err
		}
		c, err := a.resolveDeployment(cmd.Context(), "")
		if err != nil {
			return err
		}
		if secret && diff {
			// A diff names the secrets it would set and shows no value, so it
			// reads none: nothing is prompted for, and stdin is left alone.
			return a.diffEnv(cmd.Context(), c, secretsPlan(keys, values))
		}
		if secret {
			// The values are read first, so the lists are not as old as a
			// prompt takes to answer by the time the change is sent.
			secretValues, err := a.secretValues(cmd.Context(), c.Name, keys)
			if err != nil {
				return err
			}
			_, err = a.applyEnv(cmd.Context(), c, secretsPlan(keys, secretValues), wait, waitTimeout)
			return err
		}
		plan := func(env *deploymentEnv) (map[string]any, string, error) {
			changed := []string{}
			for _, key := range keys {
				if slices.Contains(env.secrets, key) {
					return nil, "", fmt.Errorf("%s is a secret; change it with `edka env set --secret %s`, or unset it first", key, key)
				}
				found, updated := false, false
				for _, v := range env.variables {
					if text(v, "name") == key {
						found = true
						if v["value"] != values[key] {
							v["value"] = values[key]
							updated = true
						}
					}
				}
				if !found {
					env.variables = append(env.variables, map[string]any{"name": key, "value": values[key]})
				}
				if updated || !found {
					changed = append(changed, key)
				}
			}
			if len(changed) == 0 {
				return nil, "", nil
			}
			return map[string]any{"config": map[string]any{"env_variables": env.variables}}, fmt.Sprintf("✓ Set %s on %s.", strings.Join(changed, ", "), ui.Clean(env.name)), nil
		}
		if diff {
			return a.diffEnv(cmd.Context(), c, plan)
		}
		unchanged, err := a.applyEnv(cmd.Context(), c, plan, wait, waitTimeout)
		if err != nil || unchanged == nil {
			return err
		}
		a.message("Nothing to change: %s already set on %s.", strings.Join(keys, ", "), ui.Clean(unchanged.name))
		// No request was sent, so there is no response to print.
		if a.output != "table" {
			return ui.Render(a.Out, jsonBody(map[string]any{"changed": false, "deployment": unchanged.id, "keys": keys}), a.output, false)
		}
		return nil
	}}
	set.Flags().BoolVar(&secret, "secret", false, "Set secrets; values are read at a hidden prompt or from stdin")
	unset := &cobra.Command{Use: "unset KEY...", Short: "Remove variables or secrets", Args: cobra.MinimumNArgs(1), Example: "  edka env unset LOG_LEVEL DEBUG\n  edka env unset LOG_LEVEL --diff", RunE: func(cmd *cobra.Command, args []string) error {
		if wait && waitTimeout <= 0 {
			return fmt.Errorf("wait-timeout must be positive")
		}
		keys := []string{}
		for _, key := range args {
			if !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		}
		c, err := a.resolveDeployment(cmd.Context(), "")
		if err != nil {
			return err
		}
		plan := func(env *deploymentEnv) (map[string]any, string, error) {
			missing := []string{}
			for _, key := range keys {
				if !env.variable(key) && !slices.Contains(env.secrets, key) {
					missing = append(missing, key)
				}
			}
			if len(missing) > 0 {
				return nil, "", fmt.Errorf("%s has no variable or secret named %s", ui.Clean(env.name), strings.Join(missing, ", "))
			}
			config := map[string]any{}
			if slices.ContainsFunc(keys, env.variable) {
				config["env_variables"] = env.withoutVariables(keys)
			}
			if secrets := slices.DeleteFunc(slices.Clone(env.secrets), func(name string) bool { return slices.Contains(keys, name) }); len(secrets) != len(env.secrets) {
				config["secrets"] = secretItems(secrets)
			}
			return map[string]any{"config": config}, fmt.Sprintf("✓ Removed %s from %s.", strings.Join(keys, ", "), ui.Clean(env.name)), nil
		}
		if diff {
			return a.diffEnv(cmd.Context(), c, plan)
		}
		_, err = a.applyEnv(cmd.Context(), c, plan, wait, waitTimeout)
		return err
	}}
	for _, c := range []*cobra.Command{set, unset} {
		c.Flags().BoolVar(&diff, "diff", false, "Show what the change would alter, and send nothing")
		c.Flags().BoolVar(&wait, "wait", false, "Wait until the rollout the change starts is healthy")
		c.Flags().DurationVar(&waitTimeout, "wait-timeout", 5*time.Minute, "Maximum rollout wait")
	}
	env.AddCommand(&cobra.Command{Use: "list", Short: "List variables, and secrets by name", Args: cobra.NoArgs, RunE: list}, set, unset)
	return env
}

// envAssignments parses KEY=value arguments, or secret names when secret is set.
func envAssignments(args []string, secret bool) ([]string, map[string]string, error) {
	keys, values := []string{}, map[string]string{}
	for _, arg := range args {
		key, value, assigned := strings.Cut(arg, "=")
		switch {
		case secret && assigned:
			return nil, nil, fmt.Errorf("secret values go at the prompt or on stdin, not in arguments that shell history keeps; for example: printf %%s \"$VALUE\" | edka env set --secret %s", key)
		case !secret && !assigned:
			return nil, nil, fmt.Errorf("set a variable as KEY=value, or a secret with --secret: %q", arg)
		case !envName.MatchString(key):
			return nil, nil, fmt.Errorf("invalid name %q; use letters, digits and underscores, not starting with a digit", key)
		case slices.Contains(keys, key):
			return nil, nil, fmt.Errorf("%s is given more than once", key)
		}
		keys = append(keys, key)
		values[key] = value
	}
	return keys, values, nil
}

// readEnv reads a deployment's variables and secret names.
func (a *App) readEnv(ctx context.Context, c *candidate) (*deploymentEnv, error) {
	id, err := safeID(c.ID)
	if err != nil {
		return nil, err
	}
	response, err := a.request(ctx, "GET", "/api/deployments/"+id, nil, nil)
	if err != nil {
		return nil, err
	}
	record, err := identityData(response.Body)
	if err != nil {
		return nil, err
	}
	// Without the stored lists, a change would replace them with its own.
	config, ok := record["config"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the API returned no configuration for %s; nothing was changed", ui.Clean(c.Name))
	}
	env := &deploymentEnv{id: id, name: c.Name, generation: number(record["spec_generation"]), read: response.Body}
	variables, _ := config["env_variables"].([]any)
	for _, v := range variables {
		if m, ok := v.(map[string]any); ok {
			env.variables = append(env.variables, m)
		}
	}
	secrets, _ := config["secrets"].([]any)
	for _, s := range secrets {
		if m, ok := s.(map[string]any); ok && text(m, "name") != "" && !slices.Contains(env.secrets, text(m, "name")) {
			env.secrets = append(env.secrets, text(m, "name"))
		}
	}
	return env, nil
}

// showEnv lists variables with their values and secrets by name.
func (a *App) showEnv(env *deploymentEnv) error {
	rows := []map[string]any{}
	for _, v := range env.variables {
		rows = append(rows, map[string]any{"name": text(v, "name"), "value": v["value"], "secret": false})
	}
	for _, name := range env.secrets {
		rows = append(rows, map[string]any{"name": name, "secret": true})
	}
	if a.output != "table" {
		return ui.Render(a.Out, jsonBody(map[string]any{"data": rows}), a.output, a.color)
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintf(a.Out, "No variables or secrets on %s.\n", ui.Clean(env.name))
		return err
	}
	return ui.Table(a.Out, []ui.Column{ui.Field("NAME", "name"), {Header: "VALUE", Value: func(m map[string]any) string {
		if m["secret"] == true {
			return "(secret)"
		}
		return ui.Text(m["value"])
	}}}, rows, a.color)
}

// secretsPlan adds or replaces secrets. A variable of the same name becomes
// the secret, so the value is no longer shown.
func secretsPlan(keys []string, values map[string]string) envPlan {
	return func(env *deploymentEnv) (map[string]any, string, error) {
		moved := []string{}
		for _, key := range keys {
			if !slices.Contains(env.secrets, key) {
				env.secrets = append(env.secrets, key)
			}
			if env.variable(key) {
				moved = append(moved, key)
			}
		}
		config := map[string]any{"secrets": secretItems(env.secrets)}
		kind := "a secret"
		if len(keys) > 1 {
			kind = "secrets"
		}
		done := fmt.Sprintf("✓ Set %s on %s as %s.", strings.Join(keys, ", "), ui.Clean(env.name), kind)
		if len(moved) > 0 {
			done = fmt.Sprintf("Moving %s from variables to secrets.\n%s", strings.Join(moved, ", "), done)
			config["env_variables"] = env.withoutVariables(moved)
		}
		return map[string]any{"config": config, "secret_values": values}, done, nil
	}
}

// secretValues reads each value at a hidden prompt, or one value from stdin
// when stdin is not a terminal, without the newline that ends it.
func (a *App) secretValues(ctx context.Context, name string, keys []string) (map[string]string, error) {
	values := map[string]string{}
	if !ui.InputTerminal(a.In) {
		if len(keys) != 1 {
			return nil, fmt.Errorf("stdin holds one secret value; set one secret at a time")
		}
		value, err := a.stdinSecret(keys[0])
		if err != nil {
			return nil, err
		}
		values[keys[0]] = value
		return values, nil
	}
	if a.noInput {
		return nil, fmt.Errorf("pipe the value on stdin: printf %%s \"$VALUE\" | edka env set --secret %s", keys[0])
	}
	for _, key := range keys {
		value, err := a.promptSecret(ctx, fmt.Sprintf("Value for %s on %s: ", key, ui.Clean(name)), key)
		if err != nil {
			return nil, err
		}
		values[key] = value
	}
	return values, nil
}

// stdinSecret reads the one secret value on a stdin that is not a terminal,
// without the newline that ends it. what names the value in an error.
func (a *App) stdinSecret(what string) (string, error) {
	data, err := io.ReadAll(io.LimitReader(a.In, maxSecretValue+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxSecretValue {
		return "", fmt.Errorf("secret value exceeds 1 MiB")
	}
	value := string(data)
	if strings.HasSuffix(value, "\r\n") {
		value = value[:len(value)-2]
	} else {
		value = strings.TrimSuffix(value, "\n")
	}
	if value == "" {
		return "", fmt.Errorf("no value for %s on stdin", what)
	}
	return value, nil
}

// promptSecret reads one value at a hidden prompt. what names the value in an error.
func (a *App) promptSecret(ctx context.Context, prompt, what string) (string, error) {
	value, err := ui.ReadSecret(ctx, prompt, a.In, a.Err)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("no value entered for %s; nothing was changed", what)
	}
	// A terminal's late answer to the color query at startup arrives as
	// input and would prefix the value; its escape character gives it away.
	if strings.ContainsRune(value, '\x1b') {
		return "", fmt.Errorf("the value for %s contains a terminal escape sequence; nothing was changed. Enter it again, or pipe it on stdin", what)
	}
	return value, nil
}

// diffEnv prints what the settings request plan makes would change in a
// deployment, and sends nothing.
func (a *App) diffEnv(ctx context.Context, c *candidate, plan envPlan) error {
	env, err := a.readEnv(ctx, c)
	if err != nil {
		return err
	}
	body, _, err := plan(env)
	if err != nil {
		return err
	}
	record, err := identityData(env.read)
	if err != nil {
		return err
	}
	// The request as Edka would decode it; a plan that changes nothing sends none.
	request := map[string]any{}
	if body != nil {
		if err := json.Unmarshal(jsonBody(body), &request); err != nil {
			return err
		}
	}
	top, config := a.settingsFields()
	// A change of the lists is planned again from the lists as they are when it
	// is sent, so it has no generation to pin.
	return a.renderSettingsDiff(diffSettings(record, request, top, config), false)
}

// applyEnv reads the deployment's lists, sends the settings request plan makes
// from them, and reports the rollout. The request names the generation the
// lists were read at, and Edka refuses it when another save landed since. Then
// applyEnv reads the lists again and plans from those, so the other save's
// keys stay. It returns the lists when plan finds nothing to change.
func (a *App) applyEnv(ctx context.Context, c *candidate, plan envPlan, wait bool, timeout time.Duration) (*deploymentEnv, error) {
	for attempt := 1; ; attempt++ {
		env, err := a.readEnv(ctx, c)
		if err != nil {
			return nil, err
		}
		body, done, err := plan(env)
		if err != nil {
			return nil, err
		}
		if body == nil {
			return env, nil
		}
		if env.generation > 0 {
			body["expected_generation"] = env.generation
		}
		response, err := a.request(ctx, "PATCH", "/api/deployments/"+env.id+"/settings", nil, jsonBody(body))
		var refused *api.Error
		if errors.As(err, &refused) && refused.Status == 409 && refused.Reason == "Change refused" {
			if attempt < envAttempts {
				a.message("%s changed while this change was being saved; reading it again.", ui.Clean(env.name))
				continue
			}
			return nil, fmt.Errorf("%s changed each of the %d times this change was sent, so nothing was saved; run the command again: %w", ui.Clean(env.name), envAttempts, err)
		}
		if err != nil {
			return nil, err
		}
		return nil, a.reportEnv(ctx, env, response, done, wait, timeout)
	}
}

// reportEnv reports the rollout a settings change started, or with wait
// follows it until it is healthy.
func (a *App) reportEnv(ctx context.Context, env *deploymentEnv, response *api.Response, done string, wait bool, timeout time.Duration) error {
	record, err := identityData(response.Body)
	if err != nil {
		return err
	}
	generation := submittedGeneration(record)
	if !wait {
		if generation > 0 {
			done += fmt.Sprintf(" Generation %d is rolling out.\n  Next: edka deployments status %s", generation, ui.Clean(env.name))
		}
		a.message("%s", done)
		if a.output != "table" {
			return a.render(response)
		}
		return nil
	}
	if generation <= 0 {
		return fmt.Errorf("change saved but no revision generation returned; inspect `edka deployments status %s`", ui.Clean(env.name))
	}
	a.message("%s Waiting for generation %d…", done, generation)
	return a.awaitGeneration(ctx, env.id, env.name, generation, timeout)
}
