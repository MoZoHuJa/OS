package hostbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"strconv"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/workload"
)

const dockerAPIVersion = "v1.45"

type MountSpec struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

type RuntimeProfile struct {
	RuntimeID    string            `json:"runtime_id"`
	ModelID      string            `json:"model_id"`
	Image        string            `json:"image"`
	Command      []string          `json:"command"`
	Environment  []string          `json:"environment,omitempty"`
	Mounts       []MountSpec       `json:"mounts,omitempty"`
	AllowedGPUs  []string          `json:"allowed_gpus,omitempty"`
	AllowedPorts []int             `json:"allowed_ports"`
	HealthPath   string            `json:"health_path"`
	Network      string            `json:"network"`
}

type DockerExecutorConfig struct {
	DockerSocket string            `json:"docker_socket"`
	GPUDevices   map[string]string `json:"gpu_devices,omitempty"`
	Profiles     []RuntimeProfile  `json:"profiles"`
}

type DockerExecutor struct {
	client   *http.Client
	profiles map[string]RuntimeProfile
	gpuDevices map[string]string
}

var digestImagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:+-]*@sha256:[a-f0-9]{64}$`)
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var networkPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

// LoadDockerExecutorConfig reads an operator-owned JSON file. It refuses symlinks
// and group/other-writable files. Runtime commands and environment are fixed in
// this file; none are accepted from ScarliHQ requests.
func LoadDockerExecutorConfig(path string) (DockerExecutorConfig, error) {
	var cfg DockerExecutorConfig
	if !filepath.IsAbs(path) {
		return cfg, errors.New("hostbridge: config path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
		return cfg, errors.New("hostbridge: config must be a regular non-writable-by-group/other file")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 1<<20 {
		return cfg, errors.New("hostbridge: cannot read bounded runtime config")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, errors.New("hostbridge: invalid runtime config")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return cfg, errors.New("hostbridge: trailing runtime config data")
	}
	if err := validateDockerConfig(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func validateDockerConfig(cfg DockerExecutorConfig) error {
	if !filepath.IsAbs(cfg.DockerSocket) || strings.ContainsRune(cfg.DockerSocket, '\x00') {
		return errors.New("hostbridge: Docker socket path must be absolute")
	}
	if len(cfg.Profiles) == 0 {
		return errors.New("hostbridge: at least one explicitly configured runtime profile is required")
	}
	seen := map[string]bool{}
	for _, p := range cfg.Profiles {
		key := profileKey(p.RuntimeID, p.ModelID)
		if !idPattern.MatchString(p.RuntimeID) || !resourceRuntimeAllowed(p.RuntimeID) ||
			!modelPattern.MatchString(p.ModelID) || seen[key] {
			return errors.New("hostbridge: invalid or duplicate runtime/model profile")
		}
		seen[key] = true
		if !digestImagePattern.MatchString(p.Image) {
			return fmt.Errorf("hostbridge: profile %s/%s image must be pinned by sha256 digest", p.RuntimeID, p.ModelID)
		}
		if len(p.Command) == 0 {
			return errors.New("hostbridge: profile command must be explicitly configured")
		}
		for _, arg := range p.Command {
			if arg == "" || strings.ContainsRune(arg, '\x00') {
				return errors.New("hostbridge: invalid static command argument")
			}
		}
		for _, env := range p.Environment {
			parts := strings.SplitN(env, "=", 2)
			if len(parts) != 2 || !envNamePattern.MatchString(parts[0]) || strings.ContainsRune(parts[1], '\x00') {
				return errors.New("hostbridge: invalid static environment entry")
			}
		}
		for _, mount := range p.Mounts {
			if !mount.ReadOnly || !filepath.IsAbs(mount.Source) || !filepath.IsAbs(mount.Target) ||
				filepath.Clean(mount.Target) != mount.Target || strings.Contains(mount.Target, "/../") ||
				strings.ContainsRune(mount.Source, '\x00') || strings.ContainsRune(mount.Target, '\x00') {
				return errors.New("hostbridge: only absolute, read-only, normalized bind mounts are permitted")
			}
			sourceInfo, sourceErr := os.Lstat(mount.Source)
			if sourceErr != nil || sourceInfo.Mode()&os.ModeSymlink != 0 || (!sourceInfo.IsDir() && !sourceInfo.Mode().IsRegular()) {
				return errors.New("hostbridge: bind mount source must exist and must not be a symlink")
			}
		}
		if len(p.AllowedPorts) == 0 || p.HealthPath == "" || !strings.HasPrefix(p.HealthPath, "/") ||
			strings.Contains(p.HealthPath, "?") || strings.Contains(p.HealthPath, "#") ||
			!networkPattern.MatchString(p.Network) {
			return errors.New("hostbridge: profile requires allowed ports, fixed health path and network")
		}
		portSeen := map[int]bool{}
		for _, port := range p.AllowedPorts {
			if port < 1 || port > 65535 || portSeen[port] {
				return errors.New("hostbridge: invalid or duplicate allowed port")
			}
			portSeen[port] = true
		}
		gpuSeen := map[string]bool{}
		for _, gpu := range p.AllowedGPUs {
			if !validGPU(gpu) || gpu == "" || gpuSeen[gpu] {
				return errors.New("hostbridge: invalid or duplicate allowed GPU id")
			}
			gpuSeen[gpu] = true
			if _, ok := cfg.GPUDevices[gpu]; !ok {
				return errors.New("hostbridge: allowed GPU has no operator-defined Docker device mapping")
			}
		}
	}
	for logical, device := range cfg.GPUDevices {
		if !validGPU(logical) || !strings.HasPrefix(device, "GPU-") || !validGPU(device) {
			return errors.New("hostbridge: GPU device map must map logical IDs to NVIDIA GPU UUIDs")
		}
	}
	return nil
}

func resourceRuntimeAllowed(id string) bool {
	return id == "sglang" || id == "vllm" || id == "beellama" || id == "ollama" || id == "litellm"
}

func NewDockerExecutor(cfg DockerExecutorConfig) (*DockerExecutor, error) {
	if err := validateDockerConfig(cfg); err != nil {
		return nil, err
	}
	socketInfo, err := os.Lstat(cfg.DockerSocket)
	if err != nil || socketInfo.Mode()&os.ModeSocket == 0 || socketInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("hostbridge: Docker endpoint must be an existing Unix socket")
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", cfg.DockerSocket)
		},
		DisableKeepAlives: true,
	}
	executor := &DockerExecutor{
		client: &http.Client{Transport: transport, Timeout: 70 * time.Second},
		profiles: map[string]RuntimeProfile{},
		gpuDevices: cfg.GPUDevices,
	}
	for _, p := range cfg.Profiles {
		executor.profiles[profileKey(p.RuntimeID, p.ModelID)] = p
	}
	return executor, nil
}

func profileKey(runtimeID, modelID string) string { return runtimeID + "\x00" + modelID }

func (e *DockerExecutor) Start(ctx context.Context, req workload.StartRequest) (workload.StartResult, error) {
	profile, ok := e.profiles[profileKey(req.RuntimeID, req.ModelID)]
	if !ok {
		return workload.StartResult{}, errors.New("hostbridge: runtime/model pair is not configured")
	}
	if !containsInt(profile.AllowedPorts, req.Port) {
		return workload.StartResult{}, errors.New("hostbridge: requested port is not allowed by runtime profile")
	}
	if len(profile.AllowedGPUs) > 0 && !containsString(profile.AllowedGPUs, req.GPUID) {
		return workload.StartResult{}, errors.New("hostbridge: requested GPU is not allowed by runtime profile")
	}
	if len(profile.AllowedGPUs) == 0 && req.GPUID != "" {
		return workload.StartResult{}, errors.New("hostbridge: profile does not permit GPU assignment")
	}
	if existing, found, err := e.Find(ctx, req.AllocationID); err != nil || found {
		if err != nil { return workload.StartResult{}, err }
		if existing.Ready { return existing, nil }
		return workload.StartResult{}, errors.New("hostbridge: allocation resource exists but is not ready")
	}
	sum := sha256.Sum256([]byte(req.AllocationID))
	containerName := "scarlix-wl-" + hex.EncodeToString(sum[:12])
	portKey := fmt.Sprintf("%d/tcp", req.Port)
	createBody := map[string]any{
		"Image": profile.Image,
		"Cmd": profile.Command,
		"Env": profile.Environment,
		"Labels": map[string]string{
			"scarlix.managed": "true",
			"scarlix.allocation_id": req.AllocationID,
			"scarlix.runtime_id": req.RuntimeID,
			"scarlix.model_id": req.ModelID,
			"scarlix.owned": "true",
		},
		"ExposedPorts": map[string]any{portKey: map[string]any{}},
		"HostConfig": map[string]any{
			"AutoRemove": false,
			"ReadonlyRootfs": true,
			"CapDrop": []string{"ALL"},
			"SecurityOpt": []string{"no-new-privileges:true"},
			"NetworkMode": profile.Network,
			"PortBindings": map[string]any{portKey: []map[string]string{{"HostIp":"127.0.0.1", "HostPort":fmt.Sprint(req.Port)}}},
			"Tmpfs": map[string]string{"/tmp":"rw,noexec,nosuid,size=256m"},
			"DeviceRequests": e.deviceRequests(profile, req.GPUID),
			"Mounts": e.dockerMounts(profile.Mounts),
		},
	}
	raw, _ := json.Marshal(createBody)
	query := url.Values{}
	query.Set("name", containerName)
	resp, data, err := e.do(ctx, http.MethodPost, "/containers/create?"+query.Encode(), raw)
	if err != nil {
		return workload.StartResult{}, err
	}
	var created struct { ID string `json:"Id"` }
	if resp.StatusCode == http.StatusConflict {
		// Name conflicts are reconciled by the allocation label, never by deleting
		// a container with a conflicting name.
		found, ok, findErr := e.Find(ctx, req.AllocationID)
		if findErr == nil && ok { return found, nil }
		return workload.StartResult{}, errors.New("hostbridge: Docker container name conflict requires reconciliation")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || json.Unmarshal(data, &created) != nil || !containerIDPattern.MatchString(created.ID) {
		return workload.StartResult{}, errors.New("hostbridge: Docker create failed")
	}
	resp, _, err = e.do(ctx, http.MethodPost, "/containers/"+created.ID+"/start", nil)
	if err != nil { return workload.StartResult{Ref:created.ID, Owned:true}, err }
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.Status304NotModified {
		return workload.StartResult{Ref:created.ID, Owned:true}, errors.New("hostbridge: Docker start failed")
	}
	result := workload.StartResult{Ref:created.ID, Owned:true}
	if err := e.waitReady(ctx, req.Port, profile.HealthPath); err != nil {
		return result, err
	}
	result.Ready = true
	return result, nil
}

func (e *DockerExecutor) Find(ctx context.Context, allocationID string) (workload.StartResult, bool, error) {
	filters, _ := json.Marshal(map[string][]string{"label":{"scarlix.allocation_id="+allocationID,"scarlix.managed=true"}})
	query := url.Values{}
	query.Set("all","1")
	query.Set("filters",string(filters))
	resp, data, err := e.do(ctx,http.MethodGet,"/containers/json?"+query.Encode(),nil)
	if err != nil { return workload.StartResult{},false,err }
	if resp.StatusCode < 200 || resp.StatusCode >= 300 { return workload.StartResult{},false,errors.New("hostbridge: Docker lookup failed") }
	var containers []struct {
		ID string `json:"Id"`
		State string `json:"State"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.Unmarshal(data,&containers); err != nil { return workload.StartResult{},false,errors.New("hostbridge: invalid Docker lookup response") }
	var found *workload.StartResult
	for _, c := range containers {
		if c.Labels["scarlix.allocation_id"] != allocationID || c.Labels["scarlix.managed"] != "true" { continue }
		if !containerIDPattern.MatchString(c.ID) || c.Labels["scarlix.owned"] != "true" { return workload.StartResult{},false,errors.New("hostbridge: Docker resource ownership metadata invalid") }
		if found != nil { return workload.StartResult{},false,errors.New("hostbridge: multiple Docker resources exist for allocation; manual reconciliation required") }
		result := workload.StartResult{Ref:c.ID,Ready:false,Owned:true}
		found = &result
	}
	if found == nil { return workload.StartResult{},false,nil }
	// Determine the readiness path/port from the resource labels and configured profile.
	runtimeID, modelID := "", ""
	for _, p := range e.profiles {
		// Labels are returned by Docker; recover the matching profile after inspecting
		// the exact container in the next request.
		_ = p
	}
	inspectResp, inspectData, err := e.do(ctx,http.MethodGet,"/containers/"+found.Ref+"/json",nil)
	if err != nil || inspectResp.StatusCode < 200 || inspectResp.StatusCode >= 300 {
		return workload.StartResult{},false,errors.New("hostbridge: Docker inspect failed")
	}
	var inspected struct {
		Config struct { Labels map[string]string `json:"Labels"` } `json:"Config"`
		State struct { Running bool `json:"Running"` } `json:"State"`
		HostConfig struct { PortBindings map[string][]struct { HostPort string `json:"HostPort"` } `json:"PortBindings"` } `json:"HostConfig"`
	}
	if err := json.Unmarshal(inspectData,&inspected); err != nil { return workload.StartResult{},false,errors.New("hostbridge: invalid Docker inspect response") }
	runtimeID = inspected.Config.Labels["scarlix.runtime_id"]
	modelID = inspected.Config.Labels["scarlix.model_id"]
	profile, ok := e.profiles[profileKey(runtimeID,modelID)]
	if !ok || inspected.Config.Labels["scarlix.allocation_id"] != allocationID {
		return workload.StartResult{},false,errors.New("hostbridge: Docker labels do not match configured profile")
	}
	port := 0
	for _, candidate := range profile.AllowedPorts {
		bindings := inspected.HostConfig.PortBindings[fmt.Sprintf("%d/tcp",candidate)]
		if len(bindings) == 0 { continue }
		for _, binding := range bindings {
			if binding.HostPort == "" { continue }
			if p, convErr := strconv.Atoi(binding.HostPort); convErr == nil && containsInt(profile.AllowedPorts,p) {
				port = p
				break
			}
		}
		if port > 0 { break }
	}
	found.Ready = inspected.State.Running && port > 0 && e.healthCheck(ctx,port,profile.HealthPath)
	return *found,true,nil
}

func (e *DockerExecutor) Stop(ctx context.Context, req workload.StopRequest) (workload.StopResult,error) {
	if !req.Owned || !containerIDPattern.MatchString(req.Ref) { return workload.StopResult{},errors.New("hostbridge: invalid owned container reference") }
	observed, found, err := e.Find(ctx,req.AllocationID)
	if err != nil { return workload.StopResult{},err }
	if !found { return workload.StopResult{Verified:true},nil }
	if observed.Ref != req.Ref || !observed.Owned { return workload.StopResult{},errors.New("hostbridge: container reference does not match allocation") }
	stopResp,_,err := e.do(ctx,http.MethodPost,"/containers/"+req.Ref+"/stop?t=10",nil)
	if err != nil { return workload.StopResult{},err }
	if stopResp.StatusCode != http.StatusNoContent && stopResp.StatusCode != http.StatusNotModified && stopResp.StatusCode != http.StatusNotFound {
		return workload.StopResult{},errors.New("hostbridge: Docker stop failed")
	}
	deleteResp,_,err := e.do(ctx,http.MethodDelete,"/containers/"+req.Ref+"?force=0&v=0",nil)
	if err != nil { return workload.StopResult{},err }
	if deleteResp.StatusCode != http.StatusNoContent && deleteResp.StatusCode != http.StatusNotFound {
		return workload.StopResult{},errors.New("hostbridge: Docker remove failed")
	}
	_,stillFound,err := e.Find(ctx,req.AllocationID)
	if err != nil || stillFound { return workload.StopResult{},errors.New("hostbridge: Docker cleanup could not be verified") }
	return workload.StopResult{Verified:true},nil
}

func (e *DockerExecutor) dockerMounts(mounts []MountSpec) []map[string]any {
	result := make([]map[string]any, 0, len(mounts))
	for _, mount := range mounts {
		result = append(result, map[string]any{"Type":"bind", "Source":mount.Source, "Target":mount.Target, "ReadOnly":true})
	}
	return result
}

func (e *DockerExecutor) deviceRequests(profile RuntimeProfile, gpuID string) []map[string]any {
	if gpuID == "" { return []map[string]any{} }
	deviceID := e.gpuDevices[gpuID]
	if deviceID == "" { return []map[string]any{} }
	return []map[string]any{{"Driver":"nvidia","Count":0,"DeviceIDs":[]string{deviceID},"Capabilities":[][]string{{"gpu"}}}}
}

func (e *DockerExecutor) waitReady(ctx context.Context, port int, path string) error {
	deadline, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	ticker := time.NewTicker(500*time.Millisecond)
	defer ticker.Stop()
	for {
		if e.healthCheck(deadline,port,path) { return nil }
		select {
		case <-deadline.Done(): return errors.New("hostbridge: workload readiness timeout")
		case <-ticker.C:
		}
	}
}

func (e *DockerExecutor) healthCheck(ctx context.Context, port int, path string) bool {
	req,err := http.NewRequestWithContext(ctx,http.MethodGet,fmt.Sprintf("http://127.0.0.1:%d%s",port,path),nil)
	if err != nil { return false }
	resp,err := (&http.Client{Timeout:1500*time.Millisecond}).Do(req)
	if err != nil { return false }
	defer resp.Body.Close()
	_,_ = io.Copy(io.Discard,io.LimitReader(resp.Body,4096))
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func (e *DockerExecutor) do(ctx context.Context, method, path string, body []byte) (*http.Response,[]byte,error) {
	req,err := http.NewRequestWithContext(ctx,method,"http://docker/"+dockerAPIVersion+path,bytes.NewReader(body))
	if err != nil { return nil,nil,errors.New("hostbridge: invalid Docker API request") }
	if body != nil { req.Header.Set("Content-Type","application/json") }
	resp,err := e.client.Do(req)
	if err != nil { return nil,nil,errors.New("hostbridge: Docker API unavailable") }
	defer resp.Body.Close()
	data,err := io.ReadAll(io.LimitReader(resp.Body,1<<20+1))
	if err != nil || len(data)>1<<20 { return resp,nil,errors.New("hostbridge: invalid Docker API response body") }
	return resp,data,nil
}

func containsInt(values []int, value int) bool { for _,v := range values { if v==value { return true } }; return false }
func containsString(values []string, value string) bool { for _,v := range values { if v==value { return true } }; return false }

var containerIDPattern = regexp.MustCompile(`^[a-f0-9]{12,64}$`)
