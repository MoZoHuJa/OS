package hostbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoZoHuJa/OS/scarlihq/internal/workload"
)

type fakeExecutor struct {
	startResult workload.StartResult
	startErr    error
	stopResult  workload.StopResult
	stopErr     error
	startCalls  int
	stopCalls   int
	lastStop    workload.StopRequest
}

func (f *fakeExecutor) Start(_ context.Context, r workload.StartRequest) (workload.StartResult, error) {
	f.startCalls++
	return f.startResult, f.startErr
}

func (f *fakeExecutor) Stop(_ context.Context, r workload.StopRequest) (workload.StopResult, error) {
	f.stopCalls++
	f.lastStop = r
	return f.stopResult, f.stopErr
}

func request(t *testing.T, server *Server, method, path, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	return rec
}

func TestStartAcceptsOnlyTypedAllowlistedRequest(t *testing.T) {
	executor := &fakeExecutor{startResult: workload.StartResult{Ref: "container:abc123", Ready: true, Owned: true}}
	server, err := NewServer(executor)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"allocation_id":"alloc-1","runtime_id":"sglang","model_id":"qwen3-14b-awq","gpu_id":"GPU-abc","port":30000}`
	rec := request(t, server, http.MethodPost, "/v1/workloads/start", "application/json", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if executor.startCalls != 1 {
		t.Fatalf("start calls=%d", executor.startCalls)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["ready"] != true || got["owned"] != true || got["ref"] != "container:abc123" {
		t.Fatalf("unexpected response: %#v", got)
	}
}

func TestStartRejectsUnsupportedRuntimeAndUnknownFields(t *testing.T) {
	for _, body := range []string{
		`{"allocation_id":"alloc-1","runtime_id":"arbitrary","model_id":"model","gpu_id":"","port":30000}`,
		`{"allocation_id":"alloc-1","runtime_id":"sglang","model_id":"model","gpu_id":"","port":30000,"command":"id"}`,
	} {
		executor := &fakeExecutor{startResult: workload.StartResult{Ref: "x", Ready: true, Owned: true}}
		server, err := NewServer(executor)
		if err != nil {
			t.Fatal(err)
		}
		rec := request(t, server, http.MethodPost, "/v1/workloads/start", "application/json", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if executor.startCalls != 0 {
			t.Fatal("executor called for invalid request")
		}
	}
}

func TestStartRejectsWrongMethodAndContentType(t *testing.T) {
	server, err := NewServer(&fakeExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	if rec := request(t, server, http.MethodGet, "/v1/workloads/start", "application/json", "{}"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status=%d", rec.Code)
	}
	if rec := request(t, server, http.MethodPost, "/v1/workloads/start", "text/plain", "{}"); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("content-type status=%d", rec.Code)
	}
}

func TestStopRejectsSharedWorkloadBeforeExecutor(t *testing.T) {
	executor := &fakeExecutor{stopResult: workload.StopResult{Verified: true}}
	server, err := NewServer(executor)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"allocation_id":"alloc-1","runtime_id":"sglang","model_id":"model","ref":"container:shared","owned":false}`
	rec := request(t, server, http.MethodPost, "/v1/workloads/stop", "application/json", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if executor.stopCalls != 0 {
		t.Fatal("shared workload reached executor")
	}
}

func TestStopRequiresVerifiedCleanup(t *testing.T) {
	executor := &fakeExecutor{stopErr: errors.New("private detail")}
	server, err := NewServer(executor)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"allocation_id":"alloc-1","runtime_id":"sglang","model_id":"model","ref":"container:owned","owned":true}`
	rec := request(t, server, http.MethodPost, "/v1/workloads/stop", "application/json", body)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d", rec.Code)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("private detail")) {
		t.Fatal("leaked executor error")
	}
}

func TestStartRejectsExtraJSONValue(t *testing.T) {
	server, err := NewServer(&fakeExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"allocation_id":"alloc-1","runtime_id":"sglang","model_id":"model","gpu_id":"","port":30000} {}`
	rec := request(t, server, http.MethodPost, "/v1/workloads/start", "application/json", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestListenUnixUsesOwnerOnlyPermissionsAndRefusesExistingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.sock")
	listener, err := ListenUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("socket mode=%o, want 0600", info.Mode().Perm())
	}
	if _, err := ListenUnix(path); err == nil {
		t.Fatal("expected existing path to be rejected")
	}
}

func TestListenUnixRequiresAbsolutePath(t *testing.T) {
	if _, err := ListenUnix("relative.sock"); err == nil {
		t.Fatal("expected relative path rejection")
	}
}
