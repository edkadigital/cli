package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A list across clusters reads clusterReads of them at a time and keeps the
// rows in the order of the clusters.
func TestClusterListsAreReadTogether(t *testing.T) {
	const clusters = clusterReads + 3
	var counting sync.Mutex
	open, most, arrived := 0, 0, 0
	full := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/clusters" {
			list := []string{}
			for i := range clusters {
				list = append(list, fmt.Sprintf(`{"id":"c%d","name":"cluster-%d"}`, i, i))
			}
			fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(list, ","))
			return
		}
		counting.Lock()
		open++
		arrived++
		most = max(most, open)
		if arrived == clusterReads {
			close(full)
		}
		counting.Unlock()
		// Reads made one after another never have clusterReads open.
		select {
		case <-full:
		case <-time.After(2 * time.Second):
			t.Error("the lists were read one after another")
		}
		// A read beyond the limit would arrive while these are still open.
		time.Sleep(20 * time.Millisecond)
		counting.Lock()
		open--
		counting.Unlock()
		fmt.Fprintf(w, `{"data":[{"id":"j","name":"job-%s"}]}`, strings.Split(r.URL.Path, "/")[3])
	}))
	t.Cleanup(server.Close)
	out, _, err := execute(t, server.URL, "cronjobs", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil || len(body.Data) != clusters {
		t.Fatal(out, err)
	}
	for i, row := range body.Data {
		if row["name"] != fmt.Sprintf("job-c%d", i) || row["cluster_name"] != fmt.Sprintf("cluster-%d", i) || row["cluster_id"] != fmt.Sprintf("c%d", i) {
			t.Fatalf("row %d: %v", i, row)
		}
	}
	if most != clusterReads {
		t.Fatalf("%d lists were open at once, want %d", most, clusterReads)
	}
}

func TestClusterListFailsWhenOneClusterFails(t *testing.T) {
	fixtures := withFixtures(map[string]string{"GET /api/clusters/c2/apps/instances": `503 {"error":"cluster staging is unreachable"}`})
	server, _ := fakeAPI(t, fixtures)
	out, _, err := execute(t, server.URL, "apps", "list")
	if err == nil || !strings.Contains(err.Error(), "cluster staging is unreachable") || out != "" {
		t.Fatal(out, err)
	}
	if _, _, err := execute(t, server.URL, "apps", "list", "--cluster", "sinaia"); err != nil {
		t.Fatal(err)
	}
}
