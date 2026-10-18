// Package hostbridge exposes a narrow, typed workload-control API intended to
// run behind a local Unix-domain socket on the host. It deliberately contains
// no shell, Docker socket, arbitrary command, image, environment, or path input.
package hostbridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	resourcecontract "github.com/MoZoHuJa/OS/scarlihq/internal/contract"
	"github.com/MoZoHuJa/OS/scarlihq/internal/workload"
)

const (
	maxRequestBytes = 16 << 10
	maxReferenceLen = 256
)

// Executor is implemented by a separately reviewed runtime adapter. The host
// bridge never invokes a shell or chooses an executable itself.
type Executor interface {
	Start(context.Context, workload.StartRequest) (workload.StartResult, error)
	Stop(context.Context, workload.StopRequest) (workload.StopResult, error)
}

// Server accepts only the two typed operations needed by the workload lifecycle.
type Server struct {
	executor Executor
	mux      *http.ServeMux
}

func NewServer(executor Executor) (*Server, error) {
	if executor == nil {
		return nil, errors.New("hostbridge: executor is required")
	}
	s := &Server{executor: executor, mux: http.NewServeMux()}
	s.mux.HandleFunc("/v1/workloads/start", s.start)
	s.mux.HandleFunc("/v1/workloads/stop", s.stop)
	s.mux.HandleFunc("/", s.notFound)
	return s, nil
}

// Handler returns a hardened HTTP handler. It must be served only over a
// Unix-domain socket; this package intentionally does not provide a TCP listener.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// ListenUnix starts a local-only listener. The socket path must be absolute,
// its parent directory must already exist and be administrator-controlled, and
// the socket is restricted to owner read/write. It never removes a pre-existing
// path because doing so could unlink an unrelated file or attacker-controlled
// socket. Caller owns the returned listener and must close it.
func ListenUnix(path string) (net.Listener, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
		return nil, errors.New("hostbridge: socket path must be absolute")
	}
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, errors.New("hostbridge: socket parent directory is unavailable")
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
		return nil, errors.New("hostbridge: socket parent must be a real directory not writable by group/other")
	}
	if _, err := os.Lstat(path); err == nil {
		return nil, errors.New("hostbridge: socket path already exists; inspect and remove it manually")
	} else if !os.IsNotExist(err) {
		return nil, errors.New("hostbridge: cannot inspect socket path")
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, errors.New("hostbridge: cannot create Unix socket")
	}
	if err := os.Chmod(path, 0600); err != nil {
		// Close our listener, but do not unlink the pathname here: another
		// privileged process could have replaced it after Listen returned.
		// Leave any residual path for an administrator to inspect safely.
		_ = listener.Close()
		return nil, errors.New("hostbridge: cannot secure Unix socket permissions")
	}
	return listener, nil
}

func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	if !methodAndContentType(w, r) {
		return
	}
	var payload startPayload
	if !decodeStrict(w, r, &payload) {
		return
	}
	request := workload.StartRequest{
		AllocationID: payload.AllocationID,
		RuntimeID:    payload.RuntimeID,
		ModelID:      payload.ModelID,
		GPUID:        payload.GPUID,
		Port:         payload.Port,
	}
	if !validCommon(request.AllocationID, request.RuntimeID, request.ModelID) ||
		request.Port < 1 || request.Port > 65535 || !validGPU(request.GPUID) {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := s.executor.Start(r.Context(), request)
	if err != nil || !result.Ready || !validReference(result.Ref) {
		writeError(w, http.StatusBadGateway, "execution_failed")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Ref   string `json:"ref"`
		Ready bool   `json:"ready"`
		Owned bool   `json:"owned"`
	}{Ref: result.Ref, Ready: true, Owned: result.Owned})
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	if !methodAndContentType(w, r) {
		return
	}
	var payload stopPayload
	if !decodeStrict(w, r, &payload) {
		return
	}
	request := workload.StopRequest{
		AllocationID: payload.AllocationID,
		RuntimeID:    payload.RuntimeID,
		ModelID:      payload.ModelID,
		Ref:          payload.Ref,
		Owned:        payload.Owned,
	}
	if !validCommon(request.AllocationID, request.RuntimeID, request.ModelID) ||
		!validReference(request.Ref) || !request.Owned {
		// Shared/pre-existing workloads are never eligible for host-side stop.
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := s.executor.Stop(r.Context(), request)
	if err != nil || !result.Verified {
		writeError(w, http.StatusBadGateway, "cleanup_unverified")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Verified bool `json:"verified"`
	}{Verified: true})
}

type startPayload struct {
	AllocationID string `json:"allocation_id"`
	RuntimeID    string `json:"runtime_id"`
	ModelID      string `json:"model_id"`
	GPUID        string `json:"gpu_id"`
	Port         int    `json:"port"`
}

type stopPayload struct {
	AllocationID string `json:"allocation_id"`
	RuntimeID    string `json:"runtime_id"`
	ModelID      string `json:"model_id"`
	Ref          string `json:"ref"`
	Owned        bool   `json:"owned"`
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not_found")
}

func methodAndContentType(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "application_json_required")
		return false
	}
	return true
}

func decodeStrict(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(w, http.StatusBadRequest, "multiple_json_values")
		return false
	}
	return true
}

var (
	idPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,127}$`)
	gpuPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	referencePat = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)
)

func validCommon(allocationID, runtimeID, modelID string) bool {
	return idPattern.MatchString(allocationID) &&
		resourcecontract.IsSupportedRuntime(runtimeID) &&
		modelPattern.MatchString(modelID)
}

func validGPU(id string) bool {
	return id == "" || gpuPattern.MatchString(id)
}

func validReference(ref string) bool {
	return len(ref) <= maxReferenceLen && referencePat.MatchString(ref)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, struct {
		Error string `json:"error"`
	}{Error: code})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// NewHTTPServer applies bounded timeouts for use with the Unix listener.
func NewHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      6 * time.Minute,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}
