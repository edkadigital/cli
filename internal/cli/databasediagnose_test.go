package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// databaseDiagnoseFixtures is a database that runs, with the routes its
// diagnosis reads. A test replaces the routes of its failure.
func databaseDiagnoseFixtures(extra map[string]string) map[string]string {
	routes := map[string]string{
		"GET /api/clusters":                         `{"data":[{"id":"c1","name":"sinaia"}]}`,
		"GET /api/clusters/c1/databases":            `{"success":true,"databases":[{"id":"db1","cluster_id":"c1","engine":"postgresql","database_name":"orders","version":"17","status":"ready"}]}`,
		"GET /api/clusters/c1/databases/db1":        `{"success":true,"database":{"id":"db1","engine":"postgresql","database_name":"orders","version":"17","status":"ready","configuration":{"instances_count":"3","namespace":"postgres","backup_enabled":true}}}`,
		"GET /api/clusters/c1/databases/db1/status": `{"success":true,"status":{"available":true,"phase":"Cluster in healthy state","instances":3,"readyInstances":3,"currentPrimary":"orders-1","lastSuccessfulBackup":"2026-10-02T02:00:00Z","conditions":[{"type":"Ready","status":"True"},{"type":"ContinuousArchiving","status":"True"}],"instanceDetails":[{"name":"orders-1","role":"primary","status":"OK","ready":true,"restartCount":0},{"name":"orders-2","role":"replica","status":"OK","ready":true,"restartCount":0},{"name":"orders-3","role":"replica","status":"OK","ready":true,"restartCount":0}]}}`,
	}
	for route, body := range extra {
		routes[route] = body
	}
	return routes
}

func TestDatabaseDiagnoseFindsNothingInADatabaseThatRuns(t *testing.T) {
	server, requests := fakeAPI(t, databaseDiagnoseFixtures(nil))
	out, errOut, err := execute(t, server.URL, "databases", "diagnose", "orders")
	if err != nil || errOut != "" {
		t.Fatal(out, errOut, err)
	}
	inOrder(t, out, "• No problem found. 3 of 3 instances are ready.\n", "Database", "orders", "Engine", "postgresql 17", "Cluster", "sinaia", "Status", "ready", "Phase", "Cluster in healthy state", "Ready", "3/3", "INSTANCE", "orders-1", "primary")
	// The log of an instance that works explains nothing.
	for _, request := range *requests {
		if !strings.HasPrefix(request, "GET ") || strings.Contains(request, "/logs") {
			t.Fatalf("a diagnosis of a database that runs sent %s", request)
		}
	}
	out, _, err = execute(t, server.URL, "db", "diagnose", "orders", "--json")
	var report struct {
		Healthy  bool
		Findings []finding
		Database map[string]any
		Runtime  map[string]any
		Logs     *podLogs
		Unread   []unread
	}
	if err != nil || json.Unmarshal([]byte(out), &report) != nil || !report.Healthy || len(report.Findings) != 1 || report.Database["database_name"] != "orders" || report.Runtime == nil || report.Logs != nil || len(report.Unread) != 0 {
		t.Fatal(out, err)
	}
}

func TestDatabaseDiagnoseExplainsAnInstanceThatIsNotReady(t *testing.T) {
	server, requests := fakeAPI(t, databaseDiagnoseFixtures(map[string]string{
		"GET /api/clusters/c1/databases/db1/status": `{"success":true,"status":{"available":true,"phase":"Waiting for the instances to become active","phaseReason":"Some instances are not yet active. Please wait.","instances":3,"readyInstances":2,"currentPrimary":"orders-1","instanceDetails":[{"name":"orders-1","role":"primary","status":"OK","ready":true,"restartCount":0},{"name":"orders-2","role":"replica","status":"Not ready","ready":false,"restartCount":6},{"name":"orders-3","role":"replica","status":"OK","ready":true,"restartCount":0}]}}`,
		"GET /api/clusters/c1/databases/db1/logs":   `{"logs":"FATAL: could not write to file: No space left on device\n","podName":"orders-2","parameters":{"previous":true}}`,
	}))
	out, _, err := execute(t, server.URL, "databases", "diagnose", "orders", "--tail", "40")
	if err == nil || err.Error() != "orders has a problem; see the finding above" {
		t.Fatal(out, err)
	}
	inOrder(t, out,
		"✗ Instance orders-2 is not ready, and restarted 6 times.\n",
		"  Waiting for the instances to become active: Some instances are not yet active. Please wait.\n",
		"  Next: Read what it printed last, under Logs.\n",
		"  Next: edka databases logs orders --pod orders-2\n",
		"Ready", "2/3",
		"orders-2", "replica", "Not ready",
		"Logs of orders-2, from the container before its last restart\n",
		"FATAL: could not write to file: No space left on device\n")
	// The instance explains the count, which is no finding of its own.
	if strings.Contains(out, "2 of 3 instances") {
		t.Fatalf("the count of ready instances is a second finding:\n%s", out)
	}
	if !strings.Contains(strings.Join(*requests, "\n"), "GET /api/clusters/c1/databases/db1/logs?podName=orders-2&previous=true&tailLines=40") {
		t.Fatalf("the log of the instance was not read: %v", *requests)
	}
}

func TestDatabaseDiagnoseReportsArchivingBackupsAndWarnings(t *testing.T) {
	server, _ := fakeAPI(t, databaseDiagnoseFixtures(map[string]string{
		"GET /api/clusters/c1/databases/db1/status": `{"success":true,"status":{"available":true,"phase":"Cluster in healthy state","instances":3,"readyInstances":3,"lastSuccessfulBackup":"2026-09-30T02:00:00Z","lastFailedBackup":"2026-10-02T02:00:00Z","conditions":[{"type":"Ready","status":"True"},{"type":"ContinuousArchiving","status":"False","reason":"ContinuousArchivingFailing","message":"exit status 4: bucket not found"}],"backups":[{"name":"orders-20260930","phase":"completed","startedAt":"2026-09-30T02:00:00Z"},{"name":"orders-20261001","phase":"failed","startedAt":"2026-10-01T02:00:00Z","error":"an older error"},{"name":"orders-20261002","phase":"failed","startedAt":"2026-10-02T02:00:00Z","error":"can't upload the backup: access denied"}],"provisioningWarnings":[{"title":"Volume is almost full","message":"orders-1 uses 96% of its volume","source":"PersistentVolumeClaim"}]}}`,
	}))
	out, _, err := execute(t, server.URL, "databases", "diagnose", "orders")
	if err == nil || err.Error() != "orders has 3 problems; see the findings above" {
		t.Fatal(out, err)
	}
	inOrder(t, out,
		"✗ The database is not archiving its write-ahead log.\n", "  ContinuousArchivingFailing: exit status 4: bucket not found\n", "  Next: edka databases backups orders\n",
		"✗ The last backup failed.\n", "  can't upload the backup: access denied\n", "  Next: edka databases backups orders\n",
		"✗ Volume is almost full.\n", "  orders-1 uses 96% of its volume\n",
		"Last failed backup")

	// A backup that failed before the last one that succeeded is no finding.
	server, _ = fakeAPI(t, databaseDiagnoseFixtures(map[string]string{
		"GET /api/clusters/c1/databases/db1/status": `{"success":true,"status":{"available":true,"instances":1,"readyInstances":1,"lastSuccessfulBackup":"2026-10-02T02:00:00Z","lastFailedBackup":"2026-10-01T02:00:00Z"}}`,
	}))
	out, _, err = execute(t, server.URL, "databases", "diagnose", "orders")
	if err != nil || !strings.Contains(out, "• No problem found. 1 of 1 instances are ready.\n") {
		t.Fatal(out, err)
	}
}

// Valkey, MySQL and ClickHouse name no time of a failed backup: their backup
// records hold the failure.
func TestDatabaseDiagnoseReportsAFailedBackupFromItsRecords(t *testing.T) {
	status := func(rest string) map[string]string {
		return databaseDiagnoseFixtures(map[string]string{
			"GET /api/clusters/c1/databases/db1/status": `{"success":true,"status":{"available":true,"instances":1,"readyInstances":1,` + rest + `}}`,
		})
	}
	// Edka lists the backups newest first, and a backup that still runs is not the last one.
	server, _ := fakeAPI(t, status(`"lastSuccessfulBackup":"2026-09-30T02:00:00Z","backups":[{"name":"b3","phase":"running","startedAt":"2026-10-02T09:00:00Z"},{"name":"b2","phase":"failed","startedAt":"2026-10-02T02:00:00Z","error":"S3 upload failed: access denied"},{"name":"b1","phase":"completed","startedAt":"2026-09-30T02:00:00Z"}]`))
	out, _, err := execute(t, server.URL, "databases", "diagnose", "orders")
	if err == nil || err.Error() != "orders has a problem; see the finding above" {
		t.Fatal(out, err)
	}
	inOrder(t, out, "✗ The last backup failed.\n", "  S3 upload failed: access denied\n", "  Next: edka databases backups orders\n", "Last failed backup", "2026-10-02")
	out, _, _ = execute(t, server.URL, "databases", "diagnose", "orders", "--json")
	var report struct{ Healthy bool }
	if err := json.Unmarshal([]byte(out), &report); err != nil || report.Healthy {
		t.Fatal(out, err)
	}

	for name, rest := range map[string]string{
		"a later backup succeeded":   `"lastSuccessfulBackup":"2026-10-02T02:00:00Z","backups":[{"name":"b2","phase":"completed","startedAt":"2026-10-02T02:00:00Z"},{"name":"b1","phase":"failed","startedAt":"2026-10-01T02:00:00Z","error":"S3 upload failed: access denied"}]`,
		"a backup has a warning":     `"backups":[{"name":"b1","phase":"completed","startedAt":"2026-10-02T02:00:00Z","error":"skipped an empty table"}]`,
		"a failed backup is deleted": `"backups":[{"name":"b2","phase":"deleting","startedAt":"2026-10-02T02:00:00Z","error":"S3 upload failed"},{"name":"b1","phase":"completed","startedAt":"2026-10-01T02:00:00Z"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			server, _ := fakeAPI(t, status(rest))
			out, _, err := execute(t, server.URL, "databases", "diagnose", "orders")
			if err != nil || !strings.Contains(out, "• No problem found. 1 of 1 instances are ready.\n") {
				t.Fatal(out, err)
			}
		})
	}
}

// While Edka still provisions a database, what the cluster reports is a note.
func TestDatabaseDiagnoseNotesADatabaseThatIsStillProvisioned(t *testing.T) {
	record := func(status, metadata string) string {
		return `{"success":true,"database":{"id":"db1","engine":"postgresql","database_name":"orders","version":"17","status":"` + status + `","metadata":` + metadata + `}}`
	}
	server, _ := fakeAPI(t, databaseDiagnoseFixtures(map[string]string{
		"GET /api/clusters/c1/databases/db1":        record("provisioning", `{"message":"Waiting for the first instance"}`),
		"GET /api/clusters/c1/databases/db1/status": `{"success":true,"status":{"available":false,"provisioningWarnings":[{"title":"Volume is not bound","message":"waiting for a volume to be created"}]}}`,
	}))
	out, _, err := execute(t, server.URL, "databases", "diagnose", "orders")
	if err != nil {
		t.Fatal(out, err)
	}
	inOrder(t, out, "• Edka is still provisioning the database.\n", "  Waiting for the first instance\n", "• Volume is not bound.\n", "  waiting for a volume to be created\n", "Status", "provisioning: Waiting for the first instance")
	if strings.Contains(out, "✗") {
		t.Fatalf("a database that is being provisioned is reported as a problem:\n%s", out)
	}

	server, _ = fakeAPI(t, databaseDiagnoseFixtures(map[string]string{
		"GET /api/clusters/c1/databases/db1":        record("failed", `{"last_error":"helm install failed: timed out waiting for the condition"}`),
		"GET /api/clusters/c1/databases/db1/status": `{"success":true,"status":{"available":false}}`,
	}))
	out, _, err = execute(t, server.URL, "databases", "diagnose", "orders")
	if err == nil {
		t.Fatal("a failed database exits successfully", out)
	}
	inOrder(t, out, "✗ The database failed.\n", "  helm install failed: timed out waiting for the condition\n", "  Next: edka databases logs orders\n", "• Edka reads no runtime status for the database.\n")
}

func TestDatabaseDiagnoseReportsWhatItCouldNotRead(t *testing.T) {
	server, _ := fakeAPI(t, databaseDiagnoseFixtures(map[string]string{
		"GET /api/clusters/c1/databases/db1/status": `503 {"error":"Cluster unreachable","message":"The cluster did not answer"}`,
	}))
	out, errOut, err := execute(t, server.URL, "databases", "diagnose", "orders")
	if err == nil {
		t.Fatal("an unreachable cluster exits successfully", out)
	}
	inOrder(t, out, "✗ Edka could not read the database from its cluster.\n", "  The cluster did not answer (HTTP 503)\n", "  Next: edka clusters diagnose sinaia\n", "Database", "orders")
	if !strings.Contains(errOut, "Could not read the runtime status: ") {
		t.Fatal(errOut)
	}

	// Edka answers, and says that it reads nothing from the cluster.
	server, _ = fakeAPI(t, databaseDiagnoseFixtures(map[string]string{
		"GET /api/clusters/c1/databases/db1/status": `{"success":true,"status":{"available":false,"phase":"Runtime status unavailable","provisioningWarnings":[{"title":"Runtime status unavailable","message":"connect ETIMEDOUT 10.0.0.1:6443"}]}}`,
	}))
	out, _, err = execute(t, server.URL, "databases", "diagnose", "orders")
	if err == nil || err.Error() != "orders has 2 problems; see the findings above" {
		t.Fatal(out, err)
	}
	inOrder(t, out, "✗ Edka reports the database as unavailable.\n", "  Runtime status unavailable\n", "✗ Runtime status unavailable.\n", "  connect ETIMEDOUT 10.0.0.1:6443\n")
}
