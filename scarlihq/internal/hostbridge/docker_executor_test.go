package hostbridge

import (
	"path/filepath"
	"strings"
	"testing"
)

func validDockerConfigForTest() DockerExecutorConfig {
	return DockerExecutorConfig{
		DockerSocket: "/var/run/docker.sock",
		GPUDevices:   map[string]string{"gpu.nvidia.0": "GPU-0123456789abcdef0123456789abcdef"},
		Profiles: []RuntimeProfile{{
			RuntimeID: "sglang", ModelID: "qwen3-14b-awq",
			Image:       "registry.example.invalid/scarlix/sglang@sha256:" + strings.Repeat("a", 64),
			Command:     []string{"python", "-m", "sglang.launch_server", "--model-path", "/models/qwen3-14b-awq", "--host", "0.0.0.0", "--port", "30000"},
			AllowedGPUs: []string{"gpu.nvidia.0"}, AllowedPorts: []int{30000},
			HealthPath: "/health", Network: "scarlix-compute", MemoryBytes: 24 * 1024 * 1024 * 1024, NanoCPUs: 4_000_000_000, PidsLimit: 2048,
		}},
	}
}

func TestDockerConfigRequiresDigestPinnedImagesAndAllowlistedRuntime(t *testing.T) {
	cfg := validDockerConfigForTest()
	if err := validateDockerConfig(cfg); err != nil {
		t.Fatalf("valid operator config rejected: %v", err)
	}
	cfg.Profiles[0].Image = "registry.example.invalid/scarlix/sglang:latest"
	if err := validateDockerConfig(cfg); err == nil {
		t.Fatal("expected mutable image tag to be rejected")
	}
	cfg = validDockerConfigForTest()
	cfg.Profiles[0].RuntimeID = "arbitrary"
	if err := validateDockerConfig(cfg); err == nil {
		t.Fatal("expected unsupported runtime to be rejected")
	}
	cfg = validDockerConfigForTest()
	cfg.Profiles[0].Network = "host"
	if err := validateDockerConfig(cfg); err == nil {
		t.Fatal("expected host network mode to be rejected")
	}
}

func TestDockerConfigRequiresFixedPortAndGPUAllowlist(t *testing.T) {
	cfg := validDockerConfigForTest()
	cfg.Profiles[0].AllowedPorts = nil
	if err := validateDockerConfig(cfg); err == nil {
		t.Fatal("expected empty allowed port list to be rejected")
	}
	cfg = validDockerConfigForTest()
	cfg.Profiles[0].AllowedGPUs = []string{"gpu.nvidia.1"}
	if err := validateDockerConfig(cfg); err == nil {
		t.Fatal("expected GPU without operator device mapping to be rejected")
	}
}

func TestDockerConfigRejectsWritableAndUnnormalizedMounts(t *testing.T) {
	cfg := validDockerConfigForTest()
	cfg.Profiles[0].Mounts = []MountSpec{{Source: filepath.Clean("/var/lib/scarlix/models/qwen"), Target: "/models", ReadOnly: false}}
	if err := validateDockerConfig(cfg); err == nil {
		t.Fatal("expected writable host mount to be rejected")
	}
	cfg.Profiles[0].Mounts = []MountSpec{{Source: "relative-model-path", Target: "/models", ReadOnly: true}}
	if err := validateDockerConfig(cfg); err == nil {
		t.Fatal("expected relative mount source to be rejected")
	}
}

func TestDockerMountsAreAlwaysReadOnly(t *testing.T) {
	mounts := (&DockerExecutor{}).dockerMounts([]MountSpec{{Source: "/var/lib/scarlix/models/qwen", Target: "/models", ReadOnly: true}})
	if len(mounts) != 1 || mounts[0]["ReadOnly"] != true || mounts[0]["Type"] != "bind" {
		t.Fatalf("unexpected Docker mount request: %#v", mounts)
	}
}
