package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// fakeExecutor is the typed executor fake: scripted outcomes.
type fakeExecutor struct {
	startCalls int
	stopCalls  int
	startErr   error
	startOut   StartOutcome
	stopErr    error
	stopOut    StopOutcome
	lastStart  StartCommand
}

func (f *fakeExecutor) Start(ctx context.Context, cmd StartCommand) (StartOutcome, error) {
	f.startCalls++
	f.lastStart = cmd
	return f.startOut, f.startErr
}

func (f *fakeExecutor) Stop(ctx context.Context, allocationID, ref string) (StopOutcome, error) {
	f.stopCalls++
	return f.stopOut, f.stopErr
}

func newTestService(t *testing.T) (*AgentService, *fakeExecutor, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 11, 13, 13, 0, 0, 0, time.UTC)}
	exec := &fakeExecutor{startOut: StartOutcome{Ref: "workload-1", Ready: true, Owned: true}, stopOut: StopOutcome{Verified: true}}
	svc, err := NewAgentService(clock, exec, nil)
	if err != nil {
		t.Fatalf("NewAgentService: %v", err)
	}
	return svc, exec, clock
}

func notice(resID, allocID string, released bool) ReservationNotice {
	return ReservationNotice{ReservationID: resID, AllocationID: allocID, NodeID: "node-x", Released: released}
}

func startCmd(alloc, res string) StartCommand {
	return StartCommand{RequestID: "req-1", AllocationID: alloc, ReservationID: res, RuntimeID: "sglang", ModelID: "qwen3-14b-awq", GPUID: "GPU-1", Port: 30000}
}

func TestStartHappyPathRecordsRunning(t *testing.T) {
	svc, exec, clock := newTestService(t)
	if err := svc.NotifyReservation(notice("res-1", "alloc-1", false)); err != nil {
		t.Fatalf("NotifyReservation: %v", err)
	}
	rec, err := svc.Start(context.Background(), startCmd("alloc-1", "res-1"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if rec.State != AgentExecRunning {
		t.Fatalf("state = %q, want running", rec.State)
	}
	if !rec.StartedAt.Equal(clock.Now()) {
		t.Fatalf("started_at = %v", rec.StartedAt)
	}
	if exec.startCalls != 1 {
		t.Fatalf("executor calls = %d", exec.startCalls)
	}
	// Idempotent replay of the SAME start → the same record, no extra
	// executor call.
	rec2, err := svc.Start(context.Background(), startCmd("alloc-1", "res-1"))
	if err != nil {
		t.Fatalf("replayed Start: %v", err)
	}
	if rec2.State != AgentExecRunning || exec.startCalls != 1 {
		t.Fatalf("replay must be idempotent: state=%q calls=%d", rec2.State, exec.startCalls)
	}
}

func TestStartRogueReservationRefused(t *testing.T) {
	svc, exec, _ := newTestService(t)
	// No mirror entry at all — the ledger never told this node.
	if _, err := svc.Start(context.Background(), startCmd("alloc-r", "res-ghost")); !errors.Is(err, ErrReservationUnknown) {
		t.Fatalf("want ErrReservationUnknown, got %v", err)
	}
	// A mirror entry for a DIFFERENT allocation.
	if err := svc.NotifyReservation(notice("res-2", "alloc-OTHER", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(context.Background(), startCmd("alloc-r", "res-2")); !errors.Is(err, ErrReservationUnknown) {
		t.Fatalf("reservation/allocation mismatch must be refused, got %v", err)
	}
	// A RELEASED reservation.
	if err := svc.NotifyReservation(notice("res-3", "alloc-r", true)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(context.Background(), startCmd("alloc-r", "res-3")); !errors.Is(err, ErrReservationReleased) {
		t.Fatalf("released reservation must refuse start, got %v", err)
	}
	if exec.startCalls != 0 {
		t.Fatalf("no executor call may happen on refused starts: %d", exec.startCalls)
	}
}

func TestStartRuntimeAllowlistEnforced(t *testing.T) {
	svc, exec, _ := newTestService(t)
	if err := svc.NotifyReservation(notice("res-1", "alloc-1", false)); err != nil {
		t.Fatal(err)
	}
	cmd := startCmd("alloc-1", "res-1")
	cmd.RuntimeID = "bash" // the arbitrary-runtime ask
	if _, err := svc.Start(context.Background(), cmd); !errors.Is(err, ErrRuntimeNotAllowed) {
		t.Fatalf("want ErrRuntimeNotAllowed, got %v", err)
	}
	if exec.startCalls != 0 {
		t.Fatal("the allowlist gate must refuse BEFORE any executor call")
	}
	// A real allowlisted runtime passes the gate.
	cmd.RuntimeID = "sglang"
	if _, err := svc.Start(context.Background(), cmd); err != nil {
		t.Fatalf("allowlisted runtime must pass: %v", err)
	}
}

func TestStartAmbiguousOutcomeFailsClosed(t *testing.T) {
	svc, exec, _ := newTestService(t)
	if err := svc.NotifyReservation(notice("res-1", "alloc-1", false)); err != nil {
		t.Fatal(err)
	}
	exec.startErr = errors.New("executor exploded mid-start")
	rec, err := svc.Start(context.Background(), startCmd("alloc-1", "res-1"))
	if err == nil {
		t.Fatal("an ambiguous start must surface the executor error")
	}
	if rec.State != AgentExecStartUncertain {
		t.Fatalf("state = %q, want start-uncertain (fail-closed)", rec.State)
	}
	// A RETRY of an uncertain start is refused — reconciliation decides.
	if _, err := svc.Start(context.Background(), startCmd("alloc-1", "res-1")); !errors.Is(err, ErrStartConflict) {
		t.Fatalf("blind retry of uncertain start must be refused, got %v", err)
	}
	if exec.startCalls != 1 {
		t.Fatalf("the uncertain start must not have re-called the executor: %d", exec.startCalls)
	}
}

func TestStartSharedWorkloadNeverStarted(t *testing.T) {
	svc, exec, _ := newTestService(t)
	if err := svc.NotifyReservation(notice("res-1", "alloc-1", false)); err != nil {
		t.Fatal(err)
	}
	exec.startOut = StartOutcome{Ref: "shared", Ready: true, Owned: false}
	rec, err := svc.Start(context.Background(), startCmd("alloc-1", "res-1"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if rec.State != AgentExecNeverStarted {
		t.Fatalf("shared workload state = %q, want never-started", rec.State)
	}
	// And stopping it is refused as not-owned.
	if _, err := svc.Stop(context.Background(), StopCommand{RequestID: "r", AllocationID: "alloc-1"}); !errors.Is(err, ErrAllocationNotOwned) {
		t.Fatalf("stopping a shared workload must be refused, got %v", err)
	}
	if exec.stopCalls != 0 {
		t.Fatal("the stop gate must refuse BEFORE any executor call")
	}
}

func TestStopOwnershipAndOutcomes(t *testing.T) {
	svc, exec, _ := newTestService(t)
	if err := svc.NotifyReservation(notice("res-1", "alloc-1", false)); err != nil {
		t.Fatal(err)
	}
	// Unknown allocation → named refusal.
	if _, err := svc.Stop(context.Background(), StopCommand{RequestID: "r", AllocationID: "ghost"}); !errors.Is(err, ErrAllocationUnknown) {
		t.Fatalf("want ErrAllocationUnknown, got %v", err)
	}
	if _, err := svc.Start(context.Background(), startCmd("alloc-1", "res-1")); err != nil {
		t.Fatal(err)
	}
	// Owned running → verified stop → stopped.
	rec, err := svc.Stop(context.Background(), StopCommand{RequestID: "r", AllocationID: "alloc-1"})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if rec.State != AgentExecStopped {
		t.Fatalf("state = %q, want stopped", rec.State)
	}
	// Idempotent re-stop reports the state, no executor call.
	if _, err := svc.Stop(context.Background(), StopCommand{RequestID: "r", AllocationID: "alloc-1"}); err != nil {
		t.Fatalf("re-stop: %v", err)
	}
	if exec.stopCalls != 1 {
		t.Fatalf("re-stop must not re-call the executor: %d", exec.stopCalls)
	}
	// Ambiguous stop → stop-uncertain (the reservation is PRESERVED
	// until reconciliation proves cleanup — §9.5).
	exec.stopErr = errors.New("cannot verify")
	if _, err := svc.Start(context.Background(), startCmd("alloc-1", "res-1")); err != nil { // re-start after stopped
		t.Fatal(err)
	}
	rec2, err := svc.Stop(context.Background(), StopCommand{RequestID: "r2", AllocationID: "alloc-1"})
	if err == nil {
		t.Fatal("an ambiguous stop must surface the executor error")
	}
	if rec2.State != AgentExecStopUncertain {
		t.Fatalf("state = %q, want stop-uncertain", rec2.State)
	}
	// A blind re-stop of an uncertain record is refused.
	if _, err := svc.Stop(context.Background(), StopCommand{RequestID: "r3", AllocationID: "alloc-1"}); !errors.Is(err, ErrStartConflict) {
		t.Fatalf("blind re-stop of uncertain must be refused, got %v", err)
	}
}

func TestRecordsAndMirrorDeterministicOrder(t *testing.T) {
	svc, _, _ := newTestService(t)
	for _, n := range []struct{ res, alloc string }{
		{"res-c", "alloc-3"}, {"res-a", "alloc-1"}, {"res-b", "alloc-2"},
	} {
		if err := svc.NotifyReservation(notice(n.res, n.alloc, false)); err != nil {
			t.Fatal(err)
		}
	}
	mirror := svc.Mirror()
	if len(mirror) != 3 || mirror[0].ReservationID != "res-a" || mirror[2].ReservationID != "res-c" {
		t.Fatalf("mirror order = %v", mirror)
	}
	records := svc.Records()
	if len(records) != 0 {
		t.Fatalf("records = %v", records)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{now: time.Date(2026, 11, 13, 13, 0, 0, 0, time.UTC)}
	exec := &fakeExecutor{startOut: StartOutcome{Ref: "w1", Ready: true, Owned: true}, stopOut: StopOutcome{Verified: true}}
	svc, err := NewAgentService(clock, exec, NewJSONLRecordStore(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.NotifyReservation(notice("res-1", "alloc-1", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(context.Background(), startCmd("alloc-1", "res-1")); err != nil {
		t.Fatal(err)
	}

	// The agent restarts: a fresh service over the SAME store.
	clock.now = clock.now.Add(time.Minute)
	svc2, err := NewAgentService(clock, exec, NewJSONLRecordStore(dir))
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	records := svc2.Records()
	if len(records) != 1 || records[0].State != AgentExecRunning || records[0].AllocationID != "alloc-1" {
		t.Fatalf("execution records must survive the restart: %+v", records)
	}
	if mirror := svc2.Mirror(); len(mirror) != 1 || mirror[0].ReservationID != "res-1" {
		t.Fatalf("the mirror must survive the restart: %+v", mirror)
	}
	// The replayed start after restart is still idempotent (running).
	if _, err := svc2.Start(context.Background(), startCmd("alloc-1", "res-1")); err != nil {
		t.Fatalf("re-start after restart must be idempotent: %v", err)
	}
}

// ---- the HTTP boundary --------------------------------------------------------

type fakeSnapshots struct {
	snap *inventory.InventorySnapshotV2
	err  error
}

func (f fakeSnapshots) Current() (*inventory.InventorySnapshotV2, error) {
	return f.snap, f.err
}

func newTestServer(t *testing.T) (*Server, *AgentService, *fakeExecutor, *httptest.Server) {
	t.Helper()
	svc, exec, clock := newTestService(t)
	srv, err := NewServer("node-x", "cp-secret-1", svc, fakeSnapshots{snap: &inventory.InventorySnapshotV2{
		SchemaVersion: inventory.SchemaVersionV2,
		SnapshotID:    "snap-agent-1",
		ObservedAt:    clock.Now(),
	}}, clock)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, svc, exec, ts
}

func httpJSON(t *testing.T, method, url, token string, body interface{}) (int, []byte) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, data
}

func TestHTTPAuthBoundary(t *testing.T) {
	_, svc, _, ts := newTestServer(t)

	// No credential → 401 on every data route.
	if code, _ := httpJSON(t, "GET", ts.URL+"/agent/v1/inventory", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no credential: %d", code)
	}
	// Wrong credential → 401.
	if code, _ := httpJSON(t, "GET", ts.URL+"/agent/v1/inventory", "wrong", nil); code != http.StatusUnauthorized {
		t.Fatalf("wrong credential: %d", code)
	}
	// Tailscale-style reachability without the credential authorizes
	// NOTHING (roadmap §9.2 verbatim): the health probe (unauthenticated
	// liveness) works, but every data route demands the credential.
	if code, _ := httpJSON(t, "GET", ts.URL+"/agent/v1/health", "", nil); code != http.StatusOK {
		t.Fatalf("health: %d", code)
	}
	// The rogue start over HTTP: named wire code.
	svc.NotifyReservation(notice("res-1", "alloc-OTHER", false))
	body := startCmd("alloc-1", "res-1")
	code, data := httpJSON(t, "POST", ts.URL+"/agent/v1/executions/start", "cp-secret-1", body)
	if code != http.StatusForbidden {
		t.Fatalf("rogue start: %d", code)
	}
	if !strings.Contains(string(data), "RESERVATION_MIRROR_REFUSED") {
		t.Fatalf("the wire code must be stable: %s", data)
	}
}

func TestHTTPInventoryAndReconcileTyped(t *testing.T) {
	_, svc, _, ts := newTestServer(t)
	code, data := httpJSON(t, "GET", ts.URL+"/agent/v1/inventory", "cp-secret-1", nil)
	if code != http.StatusOK {
		t.Fatalf("inventory: %d", code)
	}
	var snap inventory.InventorySnapshotV2
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("inventory decode: %v", err)
	}
	if snap.SnapshotID != "snap-agent-1" {
		t.Fatalf("snapshot id = %q", snap.SnapshotID)
	}
	svc.NotifyReservation(notice("res-1", "alloc-1", false))
	svc.Start(context.Background(), startCmd("alloc-1", "res-1"))
	code, data = httpJSON(t, "POST", ts.URL+"/agent/v1/executions/reconcile", "cp-secret-1", map[string]string{})
	if code != http.StatusOK {
		t.Fatalf("reconcile: %d", code)
	}
	var report ReconcileReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("reconcile decode: %v", err)
	}
	if report.NodeID != "node-x" || len(report.Executions) != 1 || report.Executions[0].State != AgentExecRunning {
		t.Fatalf("report = %+v", report)
	}
	if len(report.ReservationMirror) != 1 {
		t.Fatalf("mirror in report = %+v", report.ReservationMirror)
	}
}

func TestClientServerRoundTrip(t *testing.T) {
	_, _, exec, ts := newTestServer(t)
	client, err := NewClient(ts.URL, "cp-secret-1", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if h, err := client.Health(ctx); err != nil || h.Status != "ok" || h.NodeID != "node-x" {
		t.Fatalf("Health: %v %+v", err, h)
	}
	snap, err := client.Inventory(ctx)
	if err != nil || snap.SnapshotID != "snap-agent-1" {
		t.Fatalf("Inventory: %v %+v", err, snap)
	}
	if err := client.NotifyReservation(ctx, notice("res-1", "alloc-1", false)); err != nil {
		t.Fatalf("NotifyReservation: %v", err)
	}
	rec, err := client.Start(ctx, startCmd("alloc-1", "res-1"))
	if err != nil || rec.State != AgentExecRunning {
		t.Fatalf("Start: %v %+v", err, rec)
	}
	stopRec, err := client.Stop(ctx, StopCommand{RequestID: "r2", AllocationID: "alloc-1"})
	if err != nil || stopRec.State != AgentExecStopped {
		t.Fatalf("Stop: %v %+v", err, stopRec)
	}
	if exec.startCalls != 1 || exec.stopCalls != 1 {
		t.Fatalf("executor calls: start=%d stop=%d", exec.startCalls, exec.stopCalls)
	}
	report, err := client.Reconcile(ctx)
	if err != nil || len(report.Executions) != 1 {
		t.Fatalf("Reconcile: %v %+v", err, report)
	}

	// A bad-credential client is refused with the typed error.
	badClient, err := NewClient(ts.URL, "wrong", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badClient.Inventory(ctx); err == nil {
		t.Fatal("a bad credential must be refused")
	} else {
		var ae *AgentError
		if !errors.As(err, &ae) || ae.Code != CodeUnauthenticated {
			t.Fatalf("the typed agent error must carry the code, got %#v", err)
		}
	}
}
