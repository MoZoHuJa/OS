package hostbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MoZoHuJa/OS/scarlihq/internal/workload"
)

func TestDockerExecutorUsesTypedCreateAndVerifiedStop(t *testing.T) {
	healthListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := healthListener.Addr().(*net.TCPAddr).Port
	healthServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = healthServer.Serve(healthListener) }()
	t.Cleanup(func() { _ = healthServer.Close() })

	var mu sync.Mutex
	created := false
	running := false
	containerID := strings.Repeat("a", 64)
	containerName := ""
	var image string
	var labels map[string]string
	var createBody map[string]any

	dockerListener, err := net.Listen("unix", filepath.Join(t.TempDir(), "docker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	dockerServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			if !created {
				_, _ = w.Write([]byte("[]"))
				return
			}
			response, _ := json.Marshal([]map[string]any{{
				"Id":     containerID,
				"State":  map[bool]string{true: "running", false: "exited"}[running],
				"Labels": labels,
			}})
			_, _ = w.Write(response)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/create"):
			if err := json.NewDecoder(r.Body).Decode(&createBody); err != nil {
				http.Error(w, "bad JSON", http.StatusBadRequest)
				return
			}
			image, _ = createBody["Image"].(string)
			configLabels, _ := createBody["Labels"].(map[string]any)
			labels = map[string]string{}
			for key, value := range configLabels {
				if text, ok := value.(string); ok {
					labels[key] = text
				}
			}
			containerName = r.URL.Query().Get("name")
			created = true
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"Id":%q}`, containerID)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/"+containerID+"/start"):
			running = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/"+containerID+"/json"):
			bindings := map[string][]map[string]string{
				fmt.Sprintf("%d/tcp", port): {{"HostIp": "127.0.0.1", "HostPort": fmt.Sprint(port)}},
			}
			response, _ := json.Marshal(map[string]any{
				"Name":       "/" + containerName,
				"Config":     map[string]any{"Image": image, "Labels": labels},
				"State":      map[string]any{"Running": running},
				"HostConfig": map[string]any{"PortBindings": bindings},
			})
			_, _ = w.Write(response)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/"+containerID+"/stop"):
			running = false
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/"+containerID):
			created = false
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = dockerServer.Serve(dockerListener) }()
	t.Cleanup(func() {
		_ = dockerServer.Close()
		_ = dockerListener.Close()
	})

	socketPath := dockerListener.Addr().String()
	cfg := DockerExecutorConfig{
		DockerSocket: socketPath,
		GPUDevices: map[string]string{
			"gpu.nvidia.0": "GPU-0123456789abcdef0123456789abcdef",
		},
		Profiles: []RuntimeProfile{{
			RuntimeID:    "sglang",
			ModelID:      "qwen3-14b-awq",
			Image:        "registry.example.invalid/scarlix/sglang@sha256:" + strings.Repeat("a", 64),
			Command:      []string{"python", "-m", "sglang.launch_server"},
			AllowedGPUs:  []string{"gpu.nvidia.0"},
			AllowedPorts: []int{port},
			HealthPath:   "/health",
			Network:      "scarlix-compute",
			MemoryBytes:  24 * 1024 * 1024 * 1024,
			NanoCPUs:     4_000_000_000,
			PidsLimit:    2048,
		}},
	}
	executor, err := newDockerExecutor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := workload.StartRequest{
		AllocationID: "alloc-test-1",
		RuntimeID:    "sglang",
		ModelID:      "qwen3-14b-awq",
		GPUID:        "gpu.nvidia.0",
		Port:         port,
	}
	started, err := executor.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !started.Ready || !started.Owned || started.Ref != containerID {
		t.Fatalf("unexpected start result: %+v", started)
	}
	hostConfig, ok := createBody["HostConfig"].(map[string]any)
	if !ok || hostConfig["ReadonlyRootfs"] != true || hostConfig["NetworkMode"] != "scarlix-compute" {
		t.Fatalf("unsafe Docker host config: %#v", createBody["HostConfig"])
	}
	bindings, ok := hostConfig["PortBindings"].(map[string]any)
	if !ok {
		t.Fatalf("missing port bindings: %#v", hostConfig["PortBindings"])
	}
	portBindings, _ := bindings[fmt.Sprintf("%d/tcp", port)].([]any)
	if len(portBindings) != 1 {
		t.Fatalf("unexpected port binding: %#v", bindings)
	}
	binding, _ := portBindings[0].(map[string]any)
	if binding["HostIp"] != "127.0.0.1" {
		t.Fatalf("port was not bound to loopback: %#v", binding)
	}

	stopped, err := executor.Stop(context.Background(), workload.StopRequest{
		AllocationID: req.AllocationID,
		RuntimeID:    req.RuntimeID,
		ModelID:      req.ModelID,
		Ref:          started.Ref,
		Owned:        true,
	})
	if err != nil || !stopped.Verified {
		t.Fatalf("Stop: result=%+v err=%v", stopped, err)
	}
	_, found, err := executor.Find(context.Background(), req.AllocationID)
	if err != nil || found {
		t.Fatalf("resource still present after stop: found=%v err=%v", found, err)
	}
}
