package reservationops

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
	"github.com/MoZoHuJa/OS/scarlihq/internal/reservation"
)

// ManagedContainer is the minimal projection of a running docker container
// carrying the scarlix managed labels (see hostbridge.DockerExecutor, which
// stamps scarlix.managed / scarlix.owned / scarlix.allocation_id /
// scarlix.runtime_id / scarlix.model_id / scarlix.gpu_id / scarlix.port).
type ManagedContainer struct {
	Labels map[string]string `json:"Labels"`
}

// containerLister lists currently RUNNING containers that carry the
// scarlix.managed=true label. The production implementation shells out to
// `docker ps` (same access model as the inventory collectors); tests inject
// a fake.
type containerLister func(ctx context.Context) ([]ManagedContainer, error)

// dockerListTimeout bounds a docker ps invocation (mirrors inventory.dockerTimeout).
const dockerListTimeout = 5 * time.Second

// DockerCleanupVerifier implements reservation.CleanupVerifier on the host:
// before the reservation engine releases a resource, the verifier checks that
// no RUNNING scarlix-managed workload still occupies it.
//
// Semantics per canonical resource ID:
//
//	port.<n>          dirty iff a running managed container has scarlix.port == n
//	runtime.<id>      dirty iff a running managed container has scarlix.runtime_id == id
//	model.<id>        dirty iff a running managed container has scarlix.model_id == id
//	gpu.nvidia.<uuid> dirty iff a running managed container's scarlix.gpu_id (a display
//	                   index such as "gpu.nvidia.0") maps to that GPU in the current
//	                   inventory. If the UUID cannot be mapped (GPU removed/changed),
//	                   evidence is Verified=false — the reservation converges to
//	                   CONFLICT and blocks, fail-closed, until an operator intervenes.
//	<unknown prefix>  Verified=false (fail-closed; never guess).
//
// Transient failures (docker unreachable) return an error so the reaper
// retries on the next tick — they must NOT release anything.
type DockerCleanupVerifier struct {
	listRunningManaged containerLister
	now                func() time.Time
}

// NewDockerCleanupVerifier builds the production verifier backed by the
// docker CLI. The governor service runs with the docker supplementary group
// exactly like the host workload bridge (see SECURITY_MAP).
func NewDockerCleanupVerifier() *DockerCleanupVerifier {
	return &DockerCleanupVerifier{
		listRunningManaged: listRunningManagedViaDocker,
		now:                func() time.Time { return time.Now().UTC() },
	}
}

// newCleanupVerifierForTests is the injectable constructor used by unit tests.
func newCleanupVerifierForTests(lister containerLister, now func() time.Time) *DockerCleanupVerifier {
	return &DockerCleanupVerifier{listRunningManaged: lister, now: now}
}

// VerifyCleanup implements reservation.CleanupVerifier.
func (v *DockerCleanupVerifier) VerifyCleanup(resourceID string, reconciler reservation.Identity) (reservation.CleanupEvidence, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dockerListTimeout)
	defer cancel()
	containers, err := v.listRunningManaged(ctx)
	if err != nil {
		// Transient: docker unreachable/failed. Error (not evidence) → the
		// caller retries; nothing is released on a maybe.
		return reservation.CleanupEvidence{}, fmt.Errorf("reservationops: docker container listing failed: %w", err)
	}

	dirty := classifyResource(resourceID, containers)

	operationID := fmt.Sprintf("verify-%s-%d", sanitizeLabel(resourceID), v.now().UnixNano())
	return reservation.NewCleanupEvidence(resourceID, operationID, v.now(), !dirty, reconciler.Principal()), nil
}

// classifyResource decides whether any running managed container occupies
// resourceID. Unmappable/unknown resource IDs yield dirty=true — that is the
// deliberate fail-closed CONFLICT path (evidence Verified=false), distinct
// from transient docker failures which surface as errors from VerifyCleanup.
func classifyResource(resourceID string, containers []ManagedContainer) bool {
	switch {
	case strings.HasPrefix(resourceID, "port."):
		portStr := strings.TrimPrefix(resourceID, "port.")
		port, convErr := strconv.Atoi(portStr)
		if convErr != nil || strconv.Itoa(port) != portStr {
			return true // unmappable → fail-closed dirty
		}
		for _, c := range containers {
			if c.Labels["scarlix.port"] == portStr {
				return true
			}
		}
		return false

	case strings.HasPrefix(resourceID, "runtime."):
		id := strings.TrimPrefix(resourceID, "runtime.")
		for _, c := range containers {
			if strings.EqualFold(c.Labels["scarlix.runtime_id"], id) {
				return true
			}
		}
		return false

	case strings.HasPrefix(resourceID, "model."):
		id := strings.TrimPrefix(resourceID, "model.")
		for _, c := range containers {
			if strings.EqualFold(c.Labels["scarlix.model_id"], id) {
				return true
			}
		}
		return false

	case strings.HasPrefix(resourceID, "gpu.nvidia."):
		uuid := strings.TrimPrefix(resourceID, "gpu.nvidia.")
		if uuid == "" {
			return true // malformed → fail-closed dirty
		}
		// Map canonical UUID → display ID via the current inventory.
		gpus := inventory.CollectGPUs()
		display := ""
		found := false
		for _, gpu := range gpus {
			if strings.EqualFold(gpu.UUID, uuid) {
				display, found = gpu.ID, true
				break
			}
		}
		if !found {
			// GPU no longer present: cannot prove the resource is free
			// (a container may still reference its old display index).
			// Fail closed — the reservation converges to CONFLICT.
			return true
		}
		for _, c := range containers {
			if c.Labels["scarlix.gpu_id"] == display {
				return true
			}
		}
		return false

	default:
		// Unknown prefix: never guess — fail closed.
		return true
	}
}

// listRunningManagedViaDocker lists running containers carrying the managed
// label via `docker ps` (docker ps shows only running containers by default —
// exactly the occupancy question we ask; stopped containers hold no GPU/port).
func listRunningManagedViaDocker(ctx context.Context) ([]ManagedContainer, error) {
	cmd := exec.CommandContext(ctx, "docker", "ps", "--filter", "label=scarlix.managed=true", "--format", "{{json .Labels}}")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	containers := make([]ManagedContainer, 0, 8)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var labels map[string]string
		if err := json.Unmarshal([]byte(line), &labels); err != nil {
			// A non-JSON line from docker is a contract break — treat as failure.
			return nil, fmt.Errorf("unparseable docker ps label line: %w", err)
		}
		containers = append(containers, ManagedContainer{Labels: labels})
	}
	return containers, nil
}

// sanitizeLabel makes a resource ID safe for a compact operation ID.
func sanitizeLabel(resourceID string) string {
	replacer := strings.NewReplacer(":", "", "/", "-", ".", "-")
	s := replacer.Replace(resourceID)
	if len(s) > 24 {
		s = s[:24]
	}
	return s
}
