package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const (
	// The cluster's first pool has the name `edka clusters create` gives it.
	defaultPool = `{"id":"11111111-1111-4111-8111-111111111111","cluster_id":"c1","name":"default","provider":"hcloud","instance_type":"cx23","location":"fsn1","desired_count":4,"actual_count":4,"autoscaling_enabled":false,"autoscaling_min_instances":1,"autoscaling_max_instances":2,"labels":[],"taints":[],"metal_nodes":[],"vlan_id":4000,"vswitch_subnet":"10.90.1.0/24","lifecycle_status":"active","spec_updated_at":"2026-09-29T08:42:17.913456+02:00","updated_at":"2026-09-30T10:00:00.000Z","created_at":"2026-06-08T19:49:11.394Z"}`
	burstPool   = `{"id":"22222222-2222-4222-8222-222222222222","cluster_id":"c1","name":"burst","provider":"hcloud","instance_type":"cpx32","location":"fsn1","desired_count":1,"actual_count":1,"autoscaling_enabled":true,"autoscaling_min_instances":1,"autoscaling_max_instances":5,"labels":[{"key":"workload","value":"batch"}],"taints":[{"key":"dedicated","value":"batch","effect":"NoSchedule"}],"lifecycle_status":"active","updated_at":"2026-09-30T10:00:00.250Z"}`
	metalPool   = `{"id":"33333333-3333-4333-8333-333333333333","cluster_id":"c2","name":"racks","provider":"hetzner_metal","instance_type":null,"desired_count":2,"actual_count":2,"metal_nodes":[{"public_ip":"203.0.113.10"},{"public_ip":"203.0.113.11"}],"lifecycle_status":"active","updated_at":"2026-09-30T10:00:00.000Z"}`
)

func poolList(active string, deleting ...string) string {
	return fmt.Sprintf(`{"data":[%s],"pending_deletions":[%s]}`, active, strings.Join(deleting, ","))
}

// poolAPI serves the cluster sinaia with a fixed pool and an autoscaling one,
// and staging with a metal pool. Each route in extra replaces its answers.
func poolAPI(t *testing.T, extra map[string][]string) (*stepAPI, string) {
	t.Helper()
	steps := map[string][]string{
		"GET /api/clusters":                                    {`{"data":[{"id":"c1","name":"sinaia","location":"fsn1"},{"id":"c2","name":"staging","location":"hel1"}]}`},
		"GET /api/clusters/c1/nodepools":                       {poolList(defaultPool + "," + burstPool)},
		"GET /api/clusters/c2/nodepools":                       {poolList(metalPool)},
		"GET /api/clusters/c1/nodepools/scheduling-references": {`{"data":{"default":[],"burst":[{"kind":"deployment","id":"d1","name":"api","tolerates_taints":true},{"kind":"github_actions_runner","id":"r1","name":"ci","tolerates_taints":true}]}}`},
		"GET /api/instance-types/fsn1":                         {`[{"name":"cx23","cores":2,"memory":4},{"name":"cx33","cores":4,"memory":8},{"name":"cpx32","cores":4,"memory":8}]`},
		"GET /api/instance-types/hel1":                         {`[{"name":"cx33","cores":4,"memory":8}]`},
		"PUT /api/clusters/c1/nodepools":                       {`{"message":"Node pools updated successfully. Desired-state reconcile queued as needed.","jobs_queued":[{"type":"scaleUp","pool":"default","jobId":"j1","count":1}],"reconcile_batch_id":"b1","scale_update_needed":true}`},
	}
	for route, responses := range extra {
		steps[route] = responses
	}
	server, api := newStepAPI(t, steps)
	return api, server.URL
}

// sentPools is the request the last node pool update carried.
type sentPools struct {
	NodePools []map[string]any `json:"node_pools"`
	Revisions []struct {
		ID        string `json:"id"`
		UpdatedAt string `json:"updated_at"`
	} `json:"pool_revisions"`
}

func (s *stepAPI) sentPools(t *testing.T) sentPools {
	t.Helper()
	var sent sentPools
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := json.Unmarshal([]byte(s.bodies["PUT /api/clusters/c1/nodepools"]), &sent); err != nil {
		t.Fatal(err, s.requests)
	}
	return sent
}

// squeeze joins the words of a table with single spaces, so a test does not
// depend on the width of its columns.
func squeeze(s string) string { return strings.Join(strings.Fields(s), " ") }

func TestNodePoolsListAndGet(t *testing.T) {
	deleting := strings.Replace(strings.Replace(burstPool, `"burst"`, `"old"`, 1), `"lifecycle_status":"active"`, `"lifecycle_status":"delete_failed","deletion_error":"server 42 is locked"`, 1)
	_, base := poolAPI(t, map[string][]string{"GET /api/clusters/c1/nodepools": {poolList(defaultPool+","+burstPool, deleting)}})
	out, _, err := execute(t, base, "nodepools", "list")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i, want := range []string{
		"POOL TYPE NODES LOCATION AUTOSCALING STATUS CLUSTER",
		"default cx23 4/4 fsn1 off active sinaia",
		"burst cpx32 1/1 fsn1 1–5 active sinaia",
		"old cpx32 1/1 fsn1 1–5 delete failed sinaia",
		"racks metal 2/2 — off active staging",
	} {
		if i >= len(lines) || squeeze(lines[i]) != want {
			t.Fatalf("line %d: want %q in\n%s", i, want, out)
		}
	}
	out, _, err = execute(t, base, "nodepools", "list", "--cluster", "staging", "--json")
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err != nil || json.Unmarshal([]byte(out), &body) != nil || len(body.Data) != 1 || body.Data[0]["name"] != "racks" || body.Data[0]["cluster_name"] != "staging" {
		t.Fatal(out, err)
	}

	// A pool named default is selected by its name, like any other.
	out, _, err = execute(t, base, "nodepools", "get", "default")
	if err != nil || !strings.Contains(squeeze(out), "Nodes 4 of 4 Autoscaling off Cluster sinaia") || !strings.Contains(out, "Changed ") || strings.Contains(out, "Pinned") {
		t.Fatal(out, err)
	}
	out, _, err = execute(t, base, "nodepools", "get", "burst", "--cluster", "sinaia")
	for _, want := range []string{"Autoscaling 1–5", "Labels workload=batch", "Taints dedicated=batch:NoSchedule", "Pinned to it deployment api, github actions runner ci"} {
		if err != nil || !strings.Contains(squeeze(out), want) {
			t.Fatalf("want %q in\n%s%v", want, out, err)
		}
	}
	out, _, err = execute(t, base, "nodepools", "get", "old")
	if err != nil || !strings.Contains(squeeze(out), "Status delete failed Message server 42 is locked") {
		t.Fatal(out, err)
	}
	if _, _, err := execute(t, base, "nodepools", "get", "nope", "--cluster", "sinaia"); err == nil || !strings.Contains(err.Error(), `node pool "nope" was not found in cluster sinaia`) {
		t.Fatal(err)
	}
}

func TestNodePoolAddSendsEveryPoolBack(t *testing.T) {
	api, base := poolAPI(t, nil)
	_, _, err := execute(t, base, "nodepools", "add", "workers", "--type", "cx33", "--nodes", "2", "--cluster", "sinaia")
	if err == nil || !strings.Contains(err.Error(), "Add node pool workers to cluster sinaia with 2 × cx33 in fsn1 requires confirmation; rerun with --yes") || api.writes() != 0 {
		t.Fatal(err, api.requests)
	}
	out, errOut, err := execute(t, base, "nodepools", "add", "workers", "--type", "cx33", "--nodes", "2", "--label", "team=core", "--taint", "dedicated=core:NoExecute", "--cluster", "sinaia", "--yes", "--json")
	if err != nil || !strings.Contains(errOut, "✓ Adding node pool workers to cluster sinaia\n  Check progress: edka nodepools list --cluster sinaia") || !strings.Contains(out, `"reconcile_batch_id": "b1"`) {
		t.Fatal(out, errOut, err)
	}
	sent := api.sentPools(t)
	if len(sent.NodePools) != 3 {
		t.Fatalf("%+v", sent)
	}
	// The pools that stay go back as Edka reported them, without the fields
	// of a metal pool, which a cloud pool also carries.
	kept, err := json.Marshal(sent.NodePools[:2])
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"autoscaling_enabled":false,"autoscaling_max_instances":2,"autoscaling_min_instances":1,"desired_count":4,"id":"11111111-1111-4111-8111-111111111111","instance_type":"cx23","labels":[],"location":"fsn1","name":"default","provider":"hcloud","taints":[]},` +
		`{"autoscaling_enabled":true,"autoscaling_max_instances":5,"autoscaling_min_instances":1,"desired_count":1,"id":"22222222-2222-4222-8222-222222222222","instance_type":"cpx32","labels":[{"key":"workload","value":"batch"}],"location":"fsn1","name":"burst","provider":"hcloud","taints":[{"effect":"NoSchedule","key":"dedicated","value":"batch"}]}]`
	if string(kept) != want {
		t.Fatalf("kept pools:\n%s\nwant:\n%s", kept, want)
	}
	added, err := json.Marshal(sent.NodePools[2])
	if err != nil {
		t.Fatal(err)
	}
	if string(added) != `{"autoscaling_enabled":false,"desired_count":2,"instance_type":"cx33","labels":[{"key":"team","value":"core"}],"location":"fsn1","name":"workers","provider":"hcloud","taints":[{"effect":"NoExecute","key":"dedicated","value":"core"}]}` {
		t.Fatalf("%s", added)
	}
	// Edka compares the time a pool last changed in UTC with milliseconds.
	if len(sent.Revisions) != 2 || sent.Revisions[0].UpdatedAt != "2026-09-29T06:42:17.913Z" || sent.Revisions[1].UpdatedAt != "2026-09-30T10:00:00.250Z" || sent.Revisions[1].ID != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("%+v", sent.Revisions)
	}

	if _, _, err := execute(t, base, "nodepools", "add", "elastic", "--type", "cpx32", "--min", "0", "--max", "5", "--location", "fsn1", "--cluster", "sinaia", "--yes"); err != nil {
		t.Fatal(err)
	}
	added, _ = json.Marshal(api.sentPools(t).NodePools[2])
	if string(added) != `{"autoscaling_enabled":true,"autoscaling_max_instances":5,"autoscaling_min_instances":0,"desired_count":0,"instance_type":"cpx32","labels":[],"location":"fsn1","name":"elastic","provider":"hcloud","taints":[]}` {
		t.Fatalf("%s", added)
	}
}

// Edka refuses null where it expects a list of labels or taints.
func TestPoolEntryAlwaysHasLabelAndTaintLists(t *testing.T) {
	entry, err := json.Marshal(poolEntry(map[string]any{"id": "p1", "name": "bare", "instance_type": "cx23", "location": "fsn1", "desired_count": float64(1), "labels": nil}))
	if err != nil || string(entry) != `{"autoscaling_enabled":false,"autoscaling_max_instances":0,"autoscaling_min_instances":0,"desired_count":1,"id":"p1","instance_type":"cx23","labels":[],"location":"fsn1","name":"bare","provider":"hcloud","taints":[]}` {
		t.Fatalf("%s %v", entry, err)
	}
}

func TestNodePoolAddRefusesWhatEdkaWould(t *testing.T) {
	api, base := poolAPI(t, nil)
	for _, test := range []struct {
		line string
		want string
	}{
		{"add Workers --type cx33", "must be 1 to 63 lowercase letters"},
		{"add master --type cx33", "taken by the control plane"},
		{"add default --type cx33", "cluster sinaia already has a node pool named default"},
		{"add workers --type cx99", `unknown server type "cx99" in fsn1; choose one of cx23, cx33, cpx32`},
		{"add workers --type cx33 --nodes 0", "--nodes must be at least 1"},
		{"add workers --type cx33 --min 1", "autoscaling needs both --min and --max"},
		{"add workers --type cx33 --min 3 --max 2", "--max must be at least 1 and at least --min"},
		{"add workers --type cx33 --min 1 --max 3 --nodes 5", "--nodes must be between --min and --max"},
		{"add workers --type cx33 --label team", `a label is key=value: "team"`},
		{"add workers --type cx33 --taint dedicated=core", "a taint is key=value:effect"},
		{"add workers --type cx33 --taint dedicated=core:Never", "a taint is key=value:effect"},
		{"add workers", `required flag(s) "type" not set`},
		{"scale default", "pass --nodes, or --min and --max"},
		{"scale default --nodes 0", "to remove the pool, run `edka nodepools delete default`"},
		{"scale default --nodes 3 --max 5", "not both"},
		{"scale default --max 5", "has a set number of servers; change it with --nodes, or pass both --min and --max"},
		{"scale burst --nodes 3", "autoscales between 1 and 5 servers; change the range with --min and --max"},
		{"scale burst --max 0", "the range would be 1 to 0"},
		{"scale nope --nodes 3", `node pool "nope" was not found in cluster sinaia`},
		{"delete nope", `node pool "nope" was not found in cluster sinaia`},
	} {
		_, _, err := execute(t, base, append(append([]string{"nodepools"}, strings.Split(test.line, " ")...), "--cluster", "sinaia", "--yes")...)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("edka nodepools %s: %v, want %q", test.line, err, test.want)
		}
	}
	// A metal pool carries servers and a vSwitch that a wrong request would change.
	for _, line := range []string{"add workers --type cx33", "scale racks --nodes 3", "delete racks"} {
		_, _, err := execute(t, base, append(append([]string{"nodepools"}, strings.Split(line, " ")...), "--cluster", "staging", "--yes")...)
		if err == nil || !strings.Contains(err.Error(), "cluster staging has the Hetzner Metal node pool racks; change its node pools in the Edka console") {
			t.Errorf("edka nodepools %s: %v", line, err)
		}
	}
	if api.writes() != 0 {
		t.Fatal(api.requests)
	}
}

func TestNodePoolScale(t *testing.T) {
	api, base := poolAPI(t, nil)
	// A larger pool costs money and a smaller one drains servers, so both ask.
	_, _, err := execute(t, base, "nodepools", "scale", "default", "--nodes", "5", "--cluster", "sinaia")
	if err == nil || !strings.Contains(err.Error(), "Scale node pool default in cluster sinaia from 4 servers to 5 requires confirmation") {
		t.Fatal(err)
	}
	_, _, err = execute(t, base, "nodepools", "scale", "default", "--nodes", "3", "--cluster", "sinaia")
	if err == nil || !strings.Contains(err.Error(), "from 4 servers to 3, which drains and deletes 1 server requires confirmation") || api.writes() != 0 {
		t.Fatal(err, api.requests)
	}
	out, errOut, err := execute(t, base, "nodepools", "scale", "default", "--nodes", "4", "--cluster", "sinaia", "--json")
	if err != nil || !strings.Contains(errOut, "Nothing to change: node pool default already has 4 servers.") || !strings.Contains(out, `"changed": false`) || api.writes() != 0 {
		t.Fatal(out, errOut, err, api.requests)
	}

	if _, _, err := execute(t, base, "nodepools", "scale", "default", "--nodes", "6", "--cluster", "sinaia", "--yes"); err != nil {
		t.Fatal(err)
	}
	sent := api.sentPools(t)
	if len(sent.NodePools) != 2 || sent.NodePools[0]["desired_count"] != float64(6) || sent.NodePools[0]["autoscaling_enabled"] != false || sent.NodePools[1]["desired_count"] != float64(1) || len(sent.Revisions) != 2 {
		t.Fatalf("%+v", sent)
	}

	// One end of the range keeps the other.
	_, _, err = execute(t, base, "nodepools", "scale", "burst", "--max", "8", "--cluster", "sinaia")
	if err == nil || !strings.Contains(err.Error(), "Change node pool burst in cluster sinaia to autoscale between 1 and 8 servers, from 1 and 5 requires confirmation") {
		t.Fatal(err)
	}
	if _, _, err := execute(t, base, "nodepools", "scale", "burst", "--max", "8", "--cluster", "sinaia", "--yes"); err != nil {
		t.Fatal(err)
	}
	burst := api.sentPools(t).NodePools[1]
	if burst["autoscaling_min_instances"] != float64(1) || burst["autoscaling_max_instances"] != float64(8) || burst["autoscaling_enabled"] != true || burst["desired_count"] != float64(1) {
		t.Fatalf("%+v", burst)
	}
	if _, errOut, err := execute(t, base, "nodepools", "scale", "burst", "--min", "1", "--max", "5", "--cluster", "sinaia"); err != nil || !strings.Contains(errOut, "already autoscales between 1 and 5 servers") {
		t.Fatal(errOut, err)
	}

	// Autoscaling stays on once it is on, and the question says so.
	_, _, err = execute(t, base, "nodepools", "scale", "default", "--min", "2", "--max", "6", "--cluster", "sinaia")
	if err == nil || !strings.Contains(err.Error(), "Turn autoscaling on for node pool default in cluster sinaia, between 2 and 6 servers, for the life of the pool requires confirmation") {
		t.Fatal(err)
	}
	if _, _, err := execute(t, base, "nodepools", "scale", "default", "--min", "2", "--max", "6", "--cluster", "sinaia", "--yes"); err != nil {
		t.Fatal(err)
	}
	pool := api.sentPools(t).NodePools[0]
	if pool["autoscaling_enabled"] != true || pool["autoscaling_min_instances"] != float64(2) || pool["autoscaling_max_instances"] != float64(6) || pool["desired_count"] != float64(4) {
		t.Fatalf("%+v", pool)
	}
}

func TestNodePoolDelete(t *testing.T) {
	api, base := poolAPI(t, map[string][]string{"PUT /api/clusters/c1/nodepools": {`{"message":"Node pool deletion is pending provider convergence.","deletion_update_needed":true}`}})
	_, _, err := execute(t, base, "nodepools", "delete", "burst", "--cluster", "sinaia")
	if err == nil || !strings.Contains(err.Error(), "Delete node pool burst from cluster sinaia with its 1 server requires confirmation") || api.writes() != 0 {
		t.Fatal(err, api.requests)
	}
	_, errOut, err := execute(t, base, "nodepools", "delete", "burst", "--cluster", "sinaia", "--yes")
	if err != nil || !strings.Contains(errOut, "✓ Deleting node pool burst from cluster sinaia") {
		t.Fatal(errOut, err)
	}
	// The request leaves the pool out and still names the time it last changed.
	sent := api.sentPools(t)
	if len(sent.NodePools) != 1 || sent.NodePools[0]["name"] != "default" || len(sent.Revisions) != 2 {
		t.Fatalf("%+v", sent)
	}

	// Edka names what is pinned to a pool it refuses to delete.
	_, base = poolAPI(t, map[string][]string{"PUT /api/clusters/c1/nodepools": {`409 {"error":"Node pool is still in use","message":"Node pool \"burst\" is still selected by deployment \"api\". Move those workloads to another pool before deleting it."}`}})
	_, _, err = execute(t, base, "nodepools", "delete", "burst", "--cluster", "sinaia", "--yes")
	if err == nil || !strings.Contains(err.Error(), `is still selected by deployment "api"`) {
		t.Fatal(err)
	}

	api, base = poolAPI(t, map[string][]string{"GET /api/clusters/c1/nodepools": {poolList(defaultPool)}})
	_, _, err = execute(t, base, "nodepools", "delete", "default", "--cluster", "sinaia")
	if err == nil || !strings.Contains(err.Error(), "Delete node pool default, the last one of cluster sinaia, with its 4 servers requires confirmation") {
		t.Fatal(err)
	}
	out, _, err := execute(t, base, "nodepools", "delete", "default", "--cluster", "sinaia", "--dry-run")
	if err != nil || api.writes() != 0 || !strings.Contains(out, `"node_pools": []`) || !strings.Contains(out, `"updated_at": "2026-09-29T06:42:17.913Z"`) {
		t.Fatal(out, err, api.requests)
	}
}

func TestNodePoolWaitFollowsTheChange(t *testing.T) {
	grown := strings.Replace(defaultPool, `"desired_count":4`, `"desired_count":5`, 1)
	api, base := poolAPI(t, map[string][]string{
		"GET /api/clusters/c1/nodepools": {poolList(defaultPool), poolList(grown), poolList(strings.Replace(grown, `"actual_count":4`, `"actual_count":5`, 1))},
		"GET /api/clusters/c1":           {`{"data":{"id":"c1","name":"sinaia","status":"updating"}}`, `{"data":{"id":"c1","name":"sinaia","status":"active"}}`},
		"GET /api/clusters/c1/events": {
			`{"data":[{"id":"e1","message":"Cluster scale completed"}]}`,
			`{"data":[{"id":"e2","message":"Workers: Scaling up workers","progress":40},{"id":"e1","message":"Cluster scale completed"}]}`,
		},
	})
	out, errOut, err := execute(t, base, "nodepools", "scale", "default", "--nodes", "5", "--cluster", "sinaia", "--yes", "--wait")
	if err != nil {
		t.Fatal(errOut, err)
	}
	inOrder(t, errOut, "Scaling node pool default in cluster sinaia…\n", " 40% Workers: Scaling up workers\n", "Node pool default has 4 of 5 servers…\n", "✓ Node pool default has 5 of 5 servers\n")
	// The event of an earlier change is not printed again.
	if strings.Contains(errOut, "Cluster scale completed") || !strings.Contains(squeeze(out), "Nodes 5 of 5") {
		t.Fatal(out, errOut)
	}
	if api.count("PUT /api/clusters/c1/nodepools") != 1 {
		t.Fatal(api.requests)
	}

	_, base = poolAPI(t, map[string][]string{"GET /api/clusters/c1": {`{"data":{"id":"c1","name":"sinaia","status":"failed"}}`}, "GET /api/clusters/c1/events": {`{"data":[]}`, `{"data":[{"id":"e3","message":"Hetzner refused the server: resource limit exceeded"}]}`}})
	_, _, err = execute(t, base, "nodepools", "scale", "default", "--nodes", "5", "--cluster", "sinaia", "--yes", "--wait")
	if err == nil || !strings.Contains(err.Error(), "cluster sinaia failed: Hetzner refused the server: resource limit exceeded") {
		t.Fatal(err)
	}
	if _, _, err = execute(t, base, "nodepools", "scale", "default", "--nodes", "5", "--cluster", "sinaia", "--yes", "--wait", "--wait-timeout", "0s"); err == nil || !strings.Contains(err.Error(), "wait-timeout must be positive") {
		t.Fatal(err)
	}
}

func TestNodePoolDeleteWaitsUntilThePoolIsGone(t *testing.T) {
	deleting := strings.Replace(burstPool, `"lifecycle_status":"active"`, `"lifecycle_status":"pending_delete"`, 1)
	steps := map[string][]string{
		"GET /api/clusters/c1":           {`{"data":{"id":"c1","name":"sinaia","status":"active"}}`},
		"GET /api/clusters/c1/events":    {`{"data":[]}`},
		"PUT /api/clusters/c1/nodepools": {`{"message":"Node pool deletion is pending provider convergence.","deletion_update_needed":true}`},
		"GET /api/clusters/c1/nodepools": {poolList(defaultPool + "," + burstPool), poolList(defaultPool, deleting), poolList(defaultPool)},
	}
	_, base := poolAPI(t, steps)
	out, errOut, err := execute(t, base, "nodepools", "delete", "burst", "--cluster", "sinaia", "--yes", "--wait", "--json")
	if err != nil || !strings.Contains(out, `"deleted": true`) {
		t.Fatal(out, errOut, err)
	}
	inOrder(t, errOut, "Deleting node pool burst from cluster sinaia…\n", "Waiting for the servers to be deleted…\n", "✓ Deleted node pool burst from cluster sinaia\n")

	failed := strings.Replace(deleting, `"pending_delete"`, `"delete_failed","deletion_error":"server 42 is locked"`, 1)
	steps["GET /api/clusters/c1/nodepools"] = []string{poolList(defaultPool + "," + burstPool), poolList(defaultPool, deleting), poolList(defaultPool, failed)}
	_, base = poolAPI(t, steps)
	_, _, err = execute(t, base, "nodepools", "delete", "burst", "--cluster", "sinaia", "--yes", "--wait")
	if err == nil || !strings.Contains(err.Error(), "deleting node pool burst failed: server 42 is locked\nRetry or restore the pool in the console's node pool settings") {
		t.Fatal(err)
	}
}

func TestNodePoolCompletion(t *testing.T) {
	_, base := poolAPI(t, nil)
	for _, test := range []struct {
		line string
		want []string
	}{
		{"nodepools scale --cluster sinaia ", []string{"default\t4 × cx23 · sinaia", "burst\t1–5 × cpx32 · sinaia"}},
		{"nodepools delete b", []string{"burst\t1–5 × cpx32 · sinaia"}},
		{"nodepools add workers --cluster sinaia --type cx", []string{"cx23\t2 vCPU, 4 GB", "cx33\t4 vCPU, 8 GB"}},
		{"nodepools add workers --cluster sinaia --location hel1 --type ", []string{"cx33\t4 vCPU, 8 GB"}},
	} {
		got, directive := completions(t, base, strings.Split(test.line, " ")...)
		if strings.Join(got, "\n") != strings.Join(test.want, "\n") || directive != shellNoFiles {
			t.Errorf("edka %s<TAB> offered %q %s, want %q", test.line, got, directive, test.want)
		}
	}
}
