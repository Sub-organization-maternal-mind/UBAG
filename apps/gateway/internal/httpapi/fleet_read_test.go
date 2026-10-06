package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
)

type fakeFleet struct {
	list  []nodes.FleetNode
	sum   nodes.FleetSummary
	hosts map[string]bool
	err   error
}

func (f fakeFleet) Nodes(context.Context) ([]nodes.FleetNode, error)     { return f.list, f.err }
func (f fakeFleet) Summary(context.Context) (nodes.FleetSummary, error)  { return f.sum, f.err }
func (f fakeFleet) HelperHosts(context.Context) (map[string]bool, error) { return f.hosts, f.err }

func fleetNode(id string) nodes.FleetNode {
	return nodes.FleetNode{
		NodeID: id, Label: id, Region: "eu-west", State: nodes.FleetEligible,
		Grant:     nodes.FleetGrant{ValidUntil: time.Now().Add(time.Hour).UTC(), State: "active", ReservationState: "known"},
		Readiness: []nodes.FleetReadiness{},
	}
}

func TestFleetNodesAndSummaryServeTheFleetToOperators(t *testing.T) {
	src := fakeFleet{
		list: []nodes.FleetNode{fleetNode("n1"), fleetNode("n2"), fleetNode("n3")},
		sum: nodes.FleetSummary{
			NodesTotal: 3, NodesByState: map[string]int{"eligible": 3, "ineligible": 0, "draining": 0, "lost": 0, "unknown_reservation": 0},
			HeldByReason: map[string]int{"identity_busy": 4},
		},
	}
	for _, role := range []string{"operator", "admin"} {
		server := NewServer(Config{AppSecret: "dev-secret", ActorRole: role, Fleet: src}).Handler()

		resp := doJSON(server, http.MethodGet, "/v1/fleet/nodes?limit=2", "", authHeaders(""))
		if resp.Code != http.StatusOK {
			t.Fatalf("%s nodes = %d; body=%s", role, resp.Code, resp.Body.String())
		}
		var nodesBody struct {
			Kind  string           `json:"kind"`
			Total int              `json:"total"`
			Data  []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &nodesBody); err != nil {
			t.Fatal(err)
		}
		if nodesBody.Kind != "fleet_nodes" || nodesBody.Total != 3 || len(nodesBody.Data) != 2 {
			t.Fatalf("nodes body = %+v", nodesBody)
		}
		for _, banned := range []string{"endpoint", "address", "hostname", "nat_ip", "remote_endpoint"} {
			if strings.Contains(resp.Body.String(), banned) {
				t.Fatalf("fleet nodes response carries %q: %s", banned, resp.Body.String())
			}
		}

		resp = doJSON(server, http.MethodGet, "/v1/fleet/summary", "", authHeaders(""))
		if resp.Code != http.StatusOK {
			t.Fatalf("%s summary = %d; body=%s", role, resp.Code, resp.Body.String())
		}
		var sumBody struct {
			Kind         string         `json:"kind"`
			NodesTotal   int            `json:"nodes_total"`
			HeldByReason map[string]int `json:"held_by_reason"`
			Pressure     *int           `json:"nodes_pressure_reduced"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &sumBody); err != nil {
			t.Fatal(err)
		}
		if sumBody.Kind != "fleet_summary" || sumBody.NodesTotal != 3 || sumBody.HeldByReason["identity_busy"] != 4 || sumBody.Pressure == nil {
			t.Fatalf("summary body = %+v", sumBody)
		}
	}
}

func TestFleetNodesRejectsABadLimit(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "operator", Fleet: fakeFleet{}}).Handler()
	for _, limit := range []string{"0", "257", "abc", "-1"} {
		resp := doJSON(server, http.MethodGet, "/v1/fleet/nodes?limit="+limit, "", authHeaders(""))
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("limit=%s = %d, want 400; body=%s", limit, resp.Code, resp.Body.String())
		}
	}
}

// A role without fleet:read never reaches the source, even when one is wired.
func TestFleetRoutesWithASourceStillDenyOtherRoles(t *testing.T) {
	for _, role := range []string{"viewer", "developer", "service"} {
		server := NewServer(Config{AppSecret: "dev-secret", ActorRole: role, Fleet: fakeFleet{list: []nodes.FleetNode{fleetNode("n1")}}}).Handler()
		for _, path := range fleetPaths {
			if resp := doJSON(server, http.MethodGet, path, "", authHeaders("")); resp.Code != http.StatusForbidden {
				t.Fatalf("%s GET %s = %d, want 403; body=%s", role, path, resp.Code, resp.Body.String())
			}
		}
	}
}

func TestFleetSourceErrorIsAnOpaque500(t *testing.T) {
	server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "operator", Fleet: fakeFleet{err: errors.New("pq: password=hunter2 host=10.0.0.5")}}).Handler()
	for _, path := range fleetPaths {
		resp := doJSON(server, http.MethodGet, path, "", authHeaders(""))
		if resp.Code != http.StatusInternalServerError {
			t.Fatalf("GET %s = %d, want 500", path, resp.Code)
		}
		if strings.Contains(resp.Body.String(), "hunter2") || strings.Contains(resp.Body.String(), "10.0.0.5") {
			t.Fatalf("a store error leaked: %s", resp.Body.String())
		}
	}
}

func queuedJob(t *testing.T, store jobstore.Store, tenant, app string, n int) jobstore.Job {
	t.Helper()
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion: DefaultAPIVersion, TenantID: tenant, AppID: app,
		IdempotencyKey: "idem_qr_" + tenant + "_" + app + "_" + string(rune('a'+n)),
		Target:         "mock_target", CommandType: "echo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != jobstore.StatusQueued {
		if job, _, err = store.UpdateStatus(context.Background(), job.ID, jobstore.StatusQueued); err != nil {
			t.Fatal(err)
		}
	}
	return job
}

func TestQueueReasonIsCoarseOnJobReads(t *testing.T) {
	store := jobstore.NewMemoryStore()
	held := queuedJob(t, store, defaultTenantID, defaultAppID, 0)
	free := queuedJob(t, store, defaultTenantID, defaultAppID, 1)
	running := queuedJob(t, store, defaultTenantID, defaultAppID, 2)
	if _, _, err := store.UpdateStatus(context.Background(), running.ID, jobstore.StatusRunning); err != nil {
		t.Fatal(err)
	}
	board := executor.NewHoldBoard()
	board.Note(held.ID, defaultTenantID, defaultAppID, "no_eligible_node")
	board.Note(running.ID, defaultTenantID, defaultAppID, "no_capacity") // stale: not queued, so never shown
	server := NewServer(Config{AppSecret: "dev-secret", Jobs: store, QueueHolds: board}).Handler()

	get := func(id string) map[string]any {
		resp := doJSON(server, http.MethodGet, "/v1/jobs/"+id, "", authHeaders(""))
		if resp.Code != http.StatusOK {
			t.Fatalf("GET job = %d; body=%s", resp.Code, resp.Body.String())
		}
		var m map[string]any
		if err := json.Unmarshal(resp.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if got := get(held.ID); got["queue_reason"] != "waiting_for_node" || got["queue_reason_since"] == nil {
		t.Fatalf("held job: queue_reason=%v since=%v", got["queue_reason"], got["queue_reason_since"])
	}
	if got := get(free.ID); got["queue_reason"] != "waiting_for_worker" {
		t.Fatalf("an unheld queued job waits for a worker, got %v", got["queue_reason"])
	}
	if got := get(running.ID); got["queue_reason"] != nil || got["queue_reason_since"] != nil {
		t.Fatalf("only queued jobs carry a reason: %v", got)
	}

	// The fine reason is operator detail and never appears on a job read.
	resp := doJSON(server, http.MethodGet, "/v1/jobs/"+held.ID, "", authHeaders(""))
	if strings.Contains(resp.Body.String(), "no_eligible_node") {
		t.Fatalf("fine reason leaked to the tenant: %s", resp.Body.String())
	}

	// The list carries the same coarse reasons.
	resp = doJSON(server, http.MethodGet, "/v1/jobs?filter[status]=queued", "", authHeaders(""))
	var list struct {
		Jobs []struct {
			JobID       string `json:"job_id"`
			QueueReason string `json:"queue_reason"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &list); err != nil || len(list.Jobs) != 2 {
		t.Fatalf("list: %v %s", err, resp.Body.String())
	}
	for _, j := range list.Jobs {
		if want := map[string]string{held.ID: "waiting_for_node", free.ID: "waiting_for_worker"}[j.JobID]; j.QueueReason != want {
			t.Fatalf("list job %s queue_reason=%q, want %q", j.JobID, j.QueueReason, want)
		}
	}
}

func TestQueueReasonIsAbsentWithoutAHoldSource(t *testing.T) {
	store := jobstore.NewMemoryStore()
	job := queuedJob(t, store, defaultTenantID, defaultAppID, 0)
	server := NewServer(Config{AppSecret: "dev-secret", Jobs: store}).Handler()
	resp := doJSON(server, http.MethodGet, "/v1/jobs/"+job.ID, "", authHeaders(""))
	if strings.Contains(resp.Body.String(), "queue_reason") {
		t.Fatalf("queue_reason must be omitted while the gateway computes none: %s", resp.Body.String())
	}
}

func TestJobsSummaryQueuedByReasonIsCoarseAndTenantScoped(t *testing.T) {
	store := jobstore.NewMemoryStore()
	board := executor.NewHoldBoard()
	for i := 0; i < 5; i++ {
		job := queuedJob(t, store, defaultTenantID, defaultAppID, i)
		switch {
		case i < 2:
			board.Note(job.ID, defaultTenantID, defaultAppID, "identity_busy")
		case i == 2:
			board.Note(job.ID, defaultTenantID, defaultAppID, "ledger_unavailable")
		}
	}
	other := queuedJob(t, store, "other-tenant", defaultAppID, 0)
	board.Note(other.ID, "other-tenant", defaultAppID, "no_capacity") // must not appear for the default tenant
	server := NewServer(Config{AppSecret: "dev-secret", Jobs: store, QueueHolds: board}).Handler()

	resp := doJSON(server, http.MethodGet, "/v1/jobs/summary", "", authHeaders(""))
	var body struct {
		QueuedByReason map[string]int `json:"queued_by_reason"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"waiting_for_identity": 2, "temporarily_unavailable": 1, "waiting_for_worker": 2}
	if len(body.QueuedByReason) != len(want) {
		t.Fatalf("queued_by_reason = %v, want %v", body.QueuedByReason, want)
	}
	for k, v := range want {
		if body.QueuedByReason[k] != v {
			t.Fatalf("queued_by_reason = %v, want %v", body.QueuedByReason, want)
		}
	}
	if strings.Contains(resp.Body.String(), "identity_busy") || strings.Contains(resp.Body.String(), "no_capacity") {
		t.Fatalf("fine reasons leaked: %s", resp.Body.String())
	}
}

// A browser instance reached through a helper node never reports its endpoint,
// even with UBAG_REDACT_REMOTE_ENDPOINT off; a local one is unchanged.
func TestTopologyRedactsHelperHostedEndpoints(t *testing.T) {
	t.Setenv("UBAG_REDACT_REMOTE_ENDPOINT", "")
	store := topology.NewMemoryStore()
	mk := func(id, endpoint string) topology.BrowserInstance {
		return topology.BrowserInstance{InstanceID: id, WorkerID: "w1", TenantID: defaultTenantID, Engine: "chromium", State: "ready", RemoteEndpoint: endpoint, CreatedAt: time.Now()}
	}
	store.AddInstance(mk("on-helper", "http://Helper.Internal:9223"))
	store.AddInstance(mk("bare-host", "10.8.0.2:9223"))
	store.AddInstance(mk("local", "http://172.28.0.10:9223"))

	read := func(fleet FleetSource) map[string]map[string]any {
		server := NewServer(Config{AppSecret: "dev-secret", ActorRole: "developer", Topology: store, Fleet: fleet}).Handler()
		resp := doJSON(server, http.MethodGet, "/v1/browser/instances", "", authHeaders(""))
		if resp.Code != http.StatusOK {
			t.Fatalf("instances = %d; body=%s", resp.Code, resp.Body.String())
		}
		var payload struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		out := map[string]map[string]any{}
		for _, d := range payload.Data {
			out[d["instance_id"].(string)] = d
		}
		return out
	}

	got := read(fakeFleet{hosts: map[string]bool{"helper.internal": true, "10.8.0.2": true}})
	for _, id := range []string{"on-helper", "bare-host"} {
		if _, present := got[id]["remote_endpoint"]; present {
			t.Fatalf("%s: a helper-hosted endpoint must be withheld", id)
		}
	}
	if got["local"]["remote_endpoint"] != "http://172.28.0.10:9223" {
		t.Fatalf("a local instance keeps the legacy endpoint, got %v", got["local"]["remote_endpoint"])
	}

	// Fail closed: a fleet that cannot be read redacts every endpoint.
	for id, d := range read(fakeFleet{err: errors.New("down")}) {
		if _, present := d["remote_endpoint"]; present {
			t.Fatalf("%s: endpoint present while the fleet is unreadable", id)
		}
	}
	// No fleet at all is the unchanged legacy shape.
	if got := read(nil); got["on-helper"]["remote_endpoint"] == nil {
		t.Fatal("with no fleet source the legacy shape must be unchanged")
	}
}
