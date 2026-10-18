package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/edkadigital/cli/internal/api"
	"github.com/edkadigital/cli/internal/auth"
	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

// stepUpRequired is the reason of a request that Edka refuses until the user
// confirms with a passkey.
const stepUpRequired = "step_up_required"

// verificationWait is how long a passkey check waits for the user. Edka keeps
// a check open for as long.
var verificationWait = 5 * time.Minute

// openBrowser opens an address in the default browser.
var openBrowser = auth.OpenBrowser

// interactive reports whether someone can confirm a passkey check while the
// command waits: stdin and stderr are terminals, and prompts are allowed.
var interactive = func(a *App) bool { return !a.noInput && ui.IsTerminal(a.Err) }

var (
	errVerificationExpired  = errors.New("Verification expired. Run the command again.")
	errVerificationDeclined = errors.New("Verification was declined in the console.")
)

// StepUpError is a request that needs a passkey check, which a command
// without a terminal does not wait for. It wraps Edka's answer.
type StepUpError struct {
	// URL is the console page where the user confirms the check.
	URL string
	// Code is what the Edka app on the user's phone asks for before it
	// approves the check, or 0 when Edka sent none.
	Code int
	api  *api.Error
}

func (e *StepUpError) Error() string {
	where := ui.Clean(e.URL)
	if e.Code > 0 {
		where += fmt.Sprintf(" or in the Edka app on your phone with code %d", e.Code)
	}
	return fmt.Sprintf("This action needs a passkey check. Approve it at %s, then run the command again within 5 minutes. To approve first, run `edka verify`.", where)
}
func (e *StepUpError) Unwrap() error { return e.api }

// verification is a passkey check that Edka keeps open for this CLI login.
type verification struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	URL       string `json:"verify_url"`
	ExpiresAt string `json:"expires_at"`
	// Code is what the Edka app on the user's phone asks for before it
	// approves the check. Edka sends one while the check is pending.
	Code int `json:"code"`
}

func readVerification(body []byte) (*verification, error) {
	var envelope struct {
		Data *verification `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Data == nil {
		return nil, errors.New("Edka answered the passkey check with something this command cannot read")
	}
	return envelope.Data, nil
}

// startVerification asks Edka for a passkey check, for the operation a refused
// request named, if any. The CLI prints the check's code wherever it prints its
// address, so a phone can approve it too.
func (a *App) startVerification(ctx context.Context, operation string) (*verification, error) {
	body := map[string]any{"shows_code": true}
	if operation != "" {
		body["operation"] = operation
	}
	response, err := a.send(ctx, "POST", "/api/cli/step-up", nil, jsonBody(body))
	if err != nil {
		return nil, err
	}
	v, err := readVerification(response.Body)
	if err != nil {
		return nil, err
	}
	if _, err := safeID(v.ID); err != nil {
		return nil, errors.New("Edka answered the passkey check without an ID")
	}
	// The address comes from Edka; only a web address goes to the browser.
	parsed, err := url.Parse(v.URL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, errors.New("Edka answered the passkey check without a web address")
	}
	return v, nil
}

// showVerification prints the page that confirms a check, and its code, and
// opens the page.
func (a *App) showVerification(v *verification, browser bool) {
	a.message("Confirm with your passkey in the Edka console: %s", ui.Clean(v.URL))
	if v.Code > 0 {
		a.message("Code for the Edka app on your phone: %d", v.Code)
	}
	if browser {
		// The address is printed, so a browser that does not open loses nothing.
		_ = openBrowser(v.URL)
	}
}

// awaitVerification reads a check until the user confirms it, and returns the
// confirmed check. It fails when the user declines the check or it expires.
func (a *App) awaitVerification(ctx context.Context, v *verification) (*verification, error) {
	id, err := safeID(v.ID)
	if err != nil {
		return nil, err
	}
	wait, cancel := context.WithTimeout(ctx, verificationWait)
	defer cancel()
	// The request animation would draw over the address while the check waits.
	polling := a.polling
	defer func() { a.polling = polling }()
	p := a.startProgress()
	// ended explains a wait that stopped: the user cancelled it, or the check expired.
	ended := func(err error) error {
		if ctx.Err() != nil {
			return err
		}
		return errVerificationExpired
	}
	for {
		if err := pause(wait, pollInterval); err != nil {
			return nil, ended(err)
		}
		response, err := a.poll(wait, p, "/api/cli/step-up/"+id, nil)
		var apiError *api.Error
		switch {
		case errors.As(err, &apiError) && apiError.Status == http.StatusNotFound:
			// Edka forgets a check when it expires.
			return nil, errVerificationExpired
		case err != nil && wait.Err() != nil:
			return nil, ended(err)
		case err != nil:
			return nil, err
		}
		current, err := readVerification(response.Body)
		if err != nil {
			return nil, err
		}
		switch current.Status {
		case "approved":
			return current, nil
		case "pending":
		case "denied":
			return nil, errVerificationDeclined
		case "expired":
			return nil, errVerificationExpired
		default:
			return nil, fmt.Errorf("Edka answered the passkey check with status %q", ui.Clean(current.Status))
		}
	}
}

// stepUp runs the passkey check that a refused request needs. At a terminal it
// opens the check in the browser and waits for the user to confirm it. Without
// one it starts the check and fails with its address, since nobody may be
// there to confirm it.
func (a *App) stepUp(ctx context.Context, refused *api.Error) error {
	a.verifying = true
	defer func() { a.verifying = false }()
	v, err := a.startVerification(ctx, refused.Operation)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		return refused
	}
	if !interactive(a) {
		return &StepUpError{URL: v.URL, Code: v.Code, api: refused}
	}
	a.showVerification(v, true)
	if _, err := a.awaitVerification(ctx, v); err != nil {
		return err
	}
	a.message("✓ Verified")
	return nil
}

func (a *App) verifyCommand() *cobra.Command {
	var noBrowser bool
	cmd := &cobra.Command{Use: "verify", Short: "Confirm your identity with a passkey", Long: "Confirm your identity with a passkey in the Edka console. An organization can\nask for this check before sensitive actions, such as a kubeconfig download.\nAfter you confirm, sensitive actions from this login work for 5 minutes.\n\nverify opens the console in your browser, prints its address and waits up to 5\nminutes for you to confirm. At a terminal, a command that needs the check runs\nit on its own. A command without a terminal does not wait, so run verify before\na script.", GroupID: "start", Args: cobra.NoArgs, Example: "  edka verify\n  edka verify --no-browser", RunE: func(cmd *cobra.Command, _ []string) error {
		a.verifying = true
		defer func() { a.verifying = false }()
		v, err := a.startVerification(cmd.Context(), "")
		if err != nil {
			return err
		}
		a.showVerification(v, !noBrowser)
		confirmed, err := a.awaitVerification(cmd.Context(), v)
		if err != nil {
			return err
		}
		a.message("✓ Verified. Sensitive actions from this login work for 5 minutes.")
		if a.output != "json" {
			return nil
		}
		result := map[string]any{"verified": true}
		if expires := first(confirmed.ExpiresAt, v.ExpiresAt); expires != "" {
			result["expires_at"] = expires
		}
		return ui.Render(a.Out, jsonBody(result), "json", false)
	}}
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the console address without opening it")
	return cmd
}
