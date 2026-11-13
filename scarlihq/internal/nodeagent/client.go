package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/inventory"
)

// Client is the CONTROL-PLANE side of the node agent boundary: the typed
// caller for the agent's six routes (design §3.1). Every call carries
// the control-plane credential; every response is decoded into typed
// values. The client does no scheduling, no reservation writes — it is a
// transport for the already-decided ledger operations.
type Client struct {
	baseURL  string
	cpSecret string
	http     *http.Client
}

// NewClient builds the agent client (baseURL e.g.
// "http://node-a:8420"; the transport may be a Tailscale address — the
// CREDENTIAL is the authorization, roadmap §9.2).
func NewClient(baseURL, cpSecret string, timeout time.Duration) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("nodeagent: the agent base URL is required")
	}
	if strings.TrimSpace(cpSecret) == "" {
		return nil, fmt.Errorf("nodeagent: the control-plane credential is required")
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		cpSecret: cpSecret,
		http:     &http.Client{Timeout: timeout},
	}, nil
}

// AgentError is a typed wire error from the agent.
type AgentError struct {
	HTTPStatus int    `json:"-"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

func (e *AgentError) Error() string {
	return fmt.Sprintf("nodeagent: %s: %s", e.Code, e.Message)
}

// Health fetches the liveness probe.
func (c *Client) Health(ctx context.Context) (HealthReport, error) {
	var out HealthReport
	err := c.get(ctx, "/agent/v1/health", &out)
	return out, err
}

// Inventory polls the node's snapshot.
func (c *Client) Inventory(ctx context.Context) (*inventory.InventorySnapshotV2, error) {
	var out inventory.InventorySnapshotV2
	if err := c.get(ctx, "/agent/v1/inventory", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// NotifyReservation pushes the ledger's reservation notice to the
// agent's mirror.
func (c *Client) NotifyReservation(ctx context.Context, notice ReservationNotice) error {
	return c.post(ctx, "/agent/v1/reservations", notice, nil)
}

// Start asks the agent to start the approved profile for an allocation
// the ledger reserved.
func (c *Client) Start(ctx context.Context, cmd StartCommand) (ExecutionRecord, error) {
	var out ExecutionRecord
	err := c.post(ctx, "/agent/v1/executions/start", cmd, &out)
	return out, err
}

// Stop asks the agent to stop an allocation it owns.
func (c *Client) Stop(ctx context.Context, cmd StopCommand) (ExecutionRecord, error) {
	var out ExecutionRecord
	err := c.post(ctx, "/agent/v1/executions/stop", cmd, &out)
	return out, err
}

// ReconcileReport is the agent's reconciliation answer: its execution
// records + reservation mirror (design §4.2's inputs).
type ReconcileReport struct {
	NodeID            string              `json:"node_id"`
	ReportedAt        time.Time           `json:"reported_at"`
	Executions        []ExecutionRecord   `json:"executions"`
	ReservationMirror []ReservationNotice `json:"reservation_mirror"`
}

// Reconcile fetches the agent's execution records for comparison against
// the ledger.
func (c *Client) Reconcile(ctx context.Context) (ReconcileReport, error) {
	var out ReconcileReport
	err := c.post(ctx, "/agent/v1/executions/reconcile", map[string]string{}, &out)
	return out, err
}

// ---- transport --------------------------------------------------------------

func (c *Client) do(ctx context.Context, method, path string, body interface{}) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("nodeagent: marshal: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("nodeagent: request build: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cpSecret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("nodeagent: transport: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("nodeagent: read body: %w", err)
	}
	return resp.StatusCode, data, nil
}

func (c *Client) get(ctx context.Context, path string, out interface{}) error {
	status, data, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	return decodeAgentResponse(status, data, out)
}

func (c *Client) post(ctx context.Context, path string, body, out interface{}) error {
	status, data, err := c.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	return decodeAgentResponse(status, data, out)
}

// decodeAgentResponse decodes success bodies and raises typed
// AgentErrors for error envelopes.
func decodeAgentResponse(status int, data []byte, out interface{}) error {
	if status < 200 || status >= 300 {
		var env struct {
			Error *AgentError `json:"error"`
		}
		if len(data) > 0 && json.Unmarshal(data, &env) == nil && env.Error != nil && env.Error.Code != "" {
			env.Error.HTTPStatus = status
			return env.Error
		}
		return &AgentError{HTTPStatus: status, Code: "AGENT_UNEXPECTED", Message: fmt.Sprintf("HTTP %d", status)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("nodeagent: decode agent response: %w", err)
	}
	return nil
}
