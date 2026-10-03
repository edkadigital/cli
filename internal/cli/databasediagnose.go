package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/edkadigital/cli/internal/ui"
	"github.com/spf13/cobra"
)

// databaseDiagnosis is what `edka databases diagnose` read about a database
// and concluded.
type databaseDiagnosis struct {
	Healthy  bool           `json:"healthy"`
	Findings []finding      `json:"findings"`
	Database map[string]any `json:"database"`
	Runtime  map[string]any `json:"runtime"`
	Logs     *podLogs       `json:"logs"`
	Unread   []unread       `json:"unread"`
}

// warningFindings is how many provisioning warnings of a database get a finding.
const warningFindings = 5

// databaseDiagnoseCommand reports what is wrong with a database.
func (a *App) databaseDiagnoseCommand() *cobra.Command {
	var tail int
	cmd := &cobra.Command{Use: "diagnose [database]", Short: "Find out what is wrong with a database", Long: "Read a database as Edka records it, its instances, the conditions and warnings\nits operator reports, its backups and the log of an instance that is not ready,\nand report what is wrong with the commands to run next. The command changes\nnothing.\n\nThe findings come first, then what they were read from. A source that can't be\nread is named and the rest is still reported. The command exits unsuccessfully\nwhen it finds a problem. With --json, stdout has `healthy`, `findings` and\neverything that was read.", Args: cobra.MaximumNArgs(1), Example: "  edka databases diagnose orders\n  edka databases diagnose orders --tail 100\n  edka databases diagnose orders --json", RunE: func(cmd *cobra.Command, args []string) error {
		if tail < 1 || tail > 2000 {
			return fmt.Errorf("tail must be between 1 and 2000")
		}
		c, path, err := a.resolveDatabase(cmd.Context(), argument(args))
		if err != nil {
			return err
		}
		d, err := a.diagnoseDatabase(cmd.Context(), c, path, tail)
		if err != nil {
			return err
		}
		return a.finishDiagnosis(c.Name, d, d.Findings, func() error { return a.renderDatabaseDiagnosis(d, c) })
	}}
	cmd.Flags().IntVarP(&tail, "tail", "n", 30, "Number of log lines of the instance that is not ready (at most 2000)")
	return cmd
}

// diagnoseDatabase reads what Edka and the cluster know about the database at
// path. Only the database itself has to be readable: every other source that
// fails is named in Unread.
func (a *App) diagnoseDatabase(ctx context.Context, c *candidate, path string, tail int) (*databaseDiagnosis, error) {
	response, err := a.request(ctx, "GET", path, nil, nil)
	if err != nil {
		return nil, err
	}
	database, err := record(response.Body, "database")
	if err != nil {
		return nil, err
	}
	d := &databaseDiagnosis{Database: database, Findings: []finding{}, Unread: []unread{}}
	var runtimeErr error
	if response, err := a.request(ctx, "GET", path+"/status", nil, nil); err != nil {
		runtimeErr = err
		missed(&d.Unread, "runtime status", err)
	} else if d.Runtime, err = record(response.Body, "status"); err != nil {
		runtimeErr = err
		missed(&d.Unread, "runtime status", err)
	}
	for _, instance := range objectsOf(d.Runtime["instanceDetails"]) {
		if ready, _ := instance["ready"].(bool); ready {
			continue
		}
		if logs, err := a.podLogs(ctx, path+"/logs", instance, tail); err != nil {
			missed(&d.Unread, "logs of "+text(instance, "name"), err)
		} else {
			d.Logs = logs
		}
		break
	}
	d.Findings = databaseFindings(d, first(text(database, "database_name"), c.Name), text(c.Record, "cluster_name"), runtimeErr)
	d.Healthy = problems(d.Findings) == 0
	return d, nil
}

// databaseMessage is what Edka recorded about a database that is not ready.
func databaseMessage(database map[string]any) string {
	metadata, _ := database["metadata"].(map[string]any)
	return first(text(metadata, "last_error"), text(metadata, "message"))
}

// after reports whether the API timestamp a is later than b. A timestamp that
// is missing or can't be read is the earlier one.
func after(a, b any) bool {
	parse := func(v any) time.Time {
		s, _ := v.(string)
		t, _ := time.Parse(time.RFC3339Nano, s)
		return t
	}
	return parse(a).After(parse(b))
}

// failedBackup is the record of a database's last backup when that one failed,
// or nil. A backup that still runs or that is being deleted is not the last one.
func failedBackup(runtime map[string]any) map[string]any {
	var last map[string]any
	for _, row := range objectsOf(runtime["backups"]) {
		if phase := strings.ToLower(text(row, "phase")); phase != "completed" && phase != "failed" {
			continue
		}
		if last == nil || after(row["startedAt"], last["startedAt"]) {
			last = row
		}
	}
	if !strings.EqualFold(text(last, "phase"), "failed") || after(runtime["lastSuccessfulBackup"], last["startedAt"]) {
		return nil
	}
	return last
}

// databaseFindings concludes from what a database diagnosis read. Each rule
// names what was observed, quotes Edka or the database's operator for the
// cause, and lists commands.
func databaseFindings(d *databaseDiagnosis, name, cluster string, runtimeErr error) []finding {
	name = ui.Clean(name)
	found := []finding{}
	add := func(problem bool, summary, detail string, next ...string) {
		found = append(found, finding{Problem: problem, Summary: summary, Detail: ui.Clean(detail), Next: next})
	}
	logs := "edka databases logs " + name

	// While Edka still creates or deletes a database, what the cluster reports
	// about it is a note.
	status := text(d.Database, "status")
	settled := true
	switch status {
	case "failed":
		add(true, "The database failed.", databaseMessage(d.Database), logs)
	case "pending", "provisioning":
		settled = false
		add(false, "Edka is still provisioning the database.", databaseMessage(d.Database))
	case "deleting":
		settled = false
		add(false, "Edka is deleting the database.", "")
	}

	if runtimeErr != nil {
		reason, _, _ := strings.Cut(runtimeErr.Error(), "\n")
		add(true, "Edka could not read the database from its cluster.", reason, "edka clusters diagnose "+ui.Clean(cluster))
	}
	runtime := d.Runtime
	if runtime == nil {
		return found
	}
	phase := strings.TrimSpace(text(runtime, "phase"))
	if reason := text(runtime, "phaseReason"); reason != "" {
		phase = strings.Trim(phase+": "+reason, ": ")
	}

	// The instances that are not ready, or their number when none is listed.
	waiting := 0
	for _, instance := range objectsOf(runtime["instanceDetails"]) {
		if ready, _ := instance["ready"].(bool); ready {
			continue
		}
		waiting++
		instanceName := ui.Clean(text(instance, "name"))
		summary := fmt.Sprintf("Instance %s is not ready.", instanceName)
		if restarts := number(instance["restartCount"]); restarts > 0 {
			summary = fmt.Sprintf("Instance %s is not ready, and restarted %d times.", instanceName, restarts)
		}
		add(settled, summary, phase, "Read what it printed last, under Logs.", logs+" --pod "+instanceName)
	}
	instances, ready := number(runtime["instances"]), number(runtime["readyInstances"])
	available, _ := runtime["available"].(bool)
	switch {
	case waiting > 0:
	case runtime["instances"] != nil && ready < instances:
		add(settled, fmt.Sprintf("%d of %d instances are ready.", ready, instances), phase, logs)
	case !available && settled && phase != "":
		add(true, "Edka reports the database as unavailable.", phase, logs)
	case !available && settled:
		add(false, "Edka reads no runtime status for the database.", "")
	}

	// What the operator reports about archiving and backups.
	backup := false
	for _, condition := range objectsOf(runtime["conditions"]) {
		if text(condition, "status") != "False" {
			continue
		}
		detail := strings.Trim(text(condition, "reason")+": "+text(condition, "message"), ": ")
		switch text(condition, "type") {
		case "ContinuousArchiving":
			add(settled, "The database is not archiving its write-ahead log.", detail, fmt.Sprintf("edka databases backups %s", name))
		case "LastBackupSucceeded":
			backup = true
			add(settled, "The last backup failed.", detail, fmt.Sprintf("edka databases backups %s", name))
		}
	}
	// PostgreSQL's operator names the time of its last failed backup. The other
	// engines report a failure in their backup records only.
	switch failed := runtime["lastFailedBackup"]; {
	case backup:
	case failed != nil:
		if !after(failed, runtime["lastSuccessfulBackup"]) {
			break
		}
		// The newest backup that has an error says why.
		reason, started := "", any(nil)
		for _, row := range objectsOf(runtime["backups"]) {
			if text(row, "error") != "" && (reason == "" || after(row["startedAt"], started)) {
				reason, started = text(row, "error"), row["startedAt"]
			}
		}
		add(settled, "The last backup failed.", reason, fmt.Sprintf("edka databases backups %s", name))
	default:
		if last := failedBackup(runtime); last != nil {
			add(settled, "The last backup failed.", text(last, "error"), fmt.Sprintf("edka databases backups %s", name))
		}
	}

	warnings := objectsOf(runtime["provisioningWarnings"])
	for _, warning := range warnings[:min(len(warnings), warningFindings)] {
		add(settled, strings.TrimRight(ui.Clean(text(warning, "title")), ".")+".", text(warning, "message"))
	}

	if len(found) == 0 {
		healthy := "No problem found."
		if runtime["instances"] != nil {
			healthy = fmt.Sprintf("No problem found. %d of %d instances are ready.", ready, instances)
		}
		add(false, healthy, "")
	}
	return found
}

// renderDatabaseDiagnosis prints the findings, then what they were read from.
func (a *App) renderDatabaseDiagnosis(d *databaseDiagnosis, c *candidate) error {
	if err := a.renderFindings(d.Findings); err != nil {
		return err
	}
	m, runtime := d.Database, d.Runtime
	status := text(m, "status")
	if message := databaseMessage(m); message != "" && status != "ready" {
		status += ": " + message
	}
	phase := text(runtime, "phase")
	if reason := text(runtime, "phaseReason"); reason != "" {
		phase = strings.Trim(phase+": "+reason, ": ")
	}
	ready := ""
	if runtime["instances"] != nil {
		ready = fmt.Sprintf("%d/%d", number(runtime["readyInstances"]), number(runtime["instances"]))
	}
	if err := ui.Fields(a.Out, [][2]string{
		{"Database", first(text(m, "database_name"), c.Name)},
		{"Engine", engineOf(m)},
		{"Cluster", text(c.Record, "cluster_name")},
		{"Namespace", ui.Text(configuration(m)["namespace"])},
		{"Status", status},
		{"Phase", phase},
		{"Ready", ready},
		{"Primary", text(runtime, "currentPrimary")},
		{"Last backup", when(runtime["lastSuccessfulBackup"])},
		{"Last failed backup", first(when(runtime["lastFailedBackup"]), when(failedBackup(runtime)["startedAt"]))},
	}, a.color); err != nil {
		return err
	}
	if instances := objectsOf(runtime["instanceDetails"]); len(instances) > 0 {
		fmt.Fprintln(a.Out)
		if err := ui.Table(a.Out, []ui.Column{ui.Field("INSTANCE", "name"), ui.Field("ROLE", "role"), ui.Field("STATUS", "status"), ui.Field("READY", "ready"), ui.Field("RESTARTS", "restartCount"), ui.Field("NODE", "node")}, instances, a.color); err != nil {
			return err
		}
	}
	if err := a.renderEvidence(nil, nil, d.Logs); err != nil {
		return err
	}
	a.reportUnread(d.Unread)
	return nil
}
