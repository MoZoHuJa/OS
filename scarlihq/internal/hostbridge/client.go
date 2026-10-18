package hostbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/MoZoHuJa/OS/scarlihq/internal/workload"
)

const maxResponseBytes = 16 << 10

// Client implements workload.Driver over a Unix-domain socket. It has no TCP
// fallback: an unavailable or inaccessible socket is an execution error.
type Client struct {
	socketPath string
	httpClient *http.Client
}

func NewClient(socketPath string) (*Client, error) {
	if !strings.HasPrefix(socketPath, "/") || strings.ContainsRune(socketPath, '\x00') {
		return nil, errors.New("hostbridge: client socket path must be absolute")
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: true,
	}
	return &Client{
		socketPath: socketPath,
		httpClient: &http.Client{Transport: transport, Timeout: 35 * time.Second},
	}, nil
}

func (c *Client) Start(ctx context.Context, request workload.StartRequest) (workload.StartResult, error) {
	payload := startPayload{
		AllocationID: request.AllocationID,
		RuntimeID:    request.RuntimeID,
		ModelID:      request.ModelID,
		GPUID:        request.GPUID,
		Port:         request.Port,
	}
	var response struct {
		Ref   string `json:"ref"`
		Ready bool   `json:"ready"`
		Owned bool   `json:"owned"`
	}
	if err := c.post(ctx, "/v1/workloads/start", payload, &response); err != nil {
		return workload.StartResult{}, err
	}
	if !response.Ready || !validReference(response.Ref) {
		return workload.StartResult{}, errors.New("hostbridge: start response did not confirm readiness")
	}
	return workload.StartResult{Ref: response.Ref, Ready: response.Ready, Owned: response.Owned}, nil
}

func (c *Client) Stop(ctx context.Context, request workload.StopRequest) (workload.StopResult, error) {
	if !request.Owned {
		return workload.StopResult{}, errors.New("hostbridge: refusing to stop a workload not owned by this allocation")
	}
	payload := stopPayload{
		AllocationID: request.AllocationID,
		RuntimeID: request.RuntimeID,
		ModelID: request.ModelID,
		Ref:          request.Ref,
		Owned:        request.Owned,
	}
	var response struct {
		Verified bool `json:"verified"`
	}
	if err := c.post(ctx, "/v1/workloads/stop", payload, &response); err != nil {
		return workload.StopResult{}, err
	}
	if !response.Verified {
		return workload.StopResult{}, errors.New("hostbridge: stop response did not verify cleanup")
	}
	return workload.StopResult{Verified: true}, nil
}

func (c *Client) post(ctx context.Context, path string, payload any, response any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.New("hostbridge: request encoding failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix"+path, bytes.NewReader(body))
	if err != nil {
		return errors.New("hostbridge: request creation failed")
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.httpClient.Do(req)
	if err != nil {
		return errors.New("hostbridge: host service unavailable")
	}
	defer res.Body.Close()
	reader := io.LimitReader(res.Body, maxResponseBytes+1)
	data, err := io.ReadAll(reader)
	if err != nil || len(data) > maxResponseBytes {
		return errors.New("hostbridge: invalid response body")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var remote struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &remote)
		if remote.Error == "" {
			remote.Error = "request_failed"
		}
		return fmt.Errorf("hostbridge: request rejected (%s)", remote.Error)
	}
	if err := json.Unmarshal(data, response); err != nil {
		return errors.New("hostbridge: invalid response JSON")
	}
	return nil
}

// CloseIdleConnections releases transport resources when the client is retired.
func (c *Client) CloseIdleConnections() {
	c.httpClient.CloseIdleConnections()
}
