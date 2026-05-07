package controlclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Client communicates with the Forge control plane over HTTP.
// It handles registration, heartbeats, command polling, command ack, and event reporting.
//
// Thread safety: Client is safe for concurrent use. The nodeID and token fields
// are protected by a read-write mutex.
type Client struct {
	baseURL    string
	httpClient *http.Client
	nodeID     string
	token      string // per-node token from registration (stored but API uses apiToken)
	apiToken   string // shared API token for authenticated requests
	regReq     RegisterRequest
	logger     *slog.Logger
	mu         sync.RWMutex
}

// NewClient creates a new control plane client.
// The regReq is saved for re-registration on 401/404.
// If apiToken is non-empty, it is used as the Bearer token for all authenticated
// requests. Otherwise, the per-node token returned by Register() is used.
func NewClient(baseURL string, regReq RegisterRequest, logger *slog.Logger, apiToken ...string) *Client {
	c := &Client{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		regReq:     regReq,
		logger:     logger,
	}
	if len(apiToken) > 0 {
		c.apiToken = apiToken[0]
	}
	return c
}

// authToken returns the token to use for authenticated requests.
// Prefers the shared apiToken if set, falls back to the per-node token.
func (c *Client) authToken() string {
	if c.apiToken != "" {
		return c.apiToken
	}
	return c.token
}

// Register sends POST /v1/nodes/register and stores the returned nodeID + token.
// It retries on 5xx with exponential backoff (1s, 2s, 4s, max 8s, up to 5 attempts).
func (c *Client) Register(ctx context.Context) (*RegisterResponse, error) {
	body, err := json.Marshal(c.regReq)
	if err != nil {
		return nil, fmt.Errorf("marshal register request: %w", err)
	}

	var resp *http.Response
	backoff := time.Second
	const maxAttempts = 5

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/nodes/register", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create register request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err = c.httpClient.Do(req)
		if err != nil {
			if attempt == maxAttempts {
				return nil, fmt.Errorf("register request failed after %d attempts: %w", maxAttempts, err)
			}
			c.logger.Warn("register request failed, retrying", "attempt", attempt, "error", err)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > 8*time.Second {
				backoff = 8 * time.Second
			}
			continue
		}

		if resp.StatusCode >= 500 {
			resp.Body.Close()
			if attempt == maxAttempts {
				return nil, fmt.Errorf("register returned %d after %d attempts", resp.StatusCode, maxAttempts)
			}
			c.logger.Warn("register returned server error, retrying", "status", resp.StatusCode, "attempt", attempt)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > 8*time.Second {
				backoff = 8 * time.Second
			}
			continue
		}

		break
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("register returned %d: %s", resp.StatusCode, string(respBody))
	}

	var regResp RegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&regResp); err != nil {
		return nil, fmt.Errorf("decode register response: %w", err)
	}

	c.mu.Lock()
	c.nodeID = regResp.NodeID
	c.token = regResp.AgentToken
	c.mu.Unlock()

	c.logger.Info("registered with control plane", "node_id", regResp.NodeID)
	return &regResp, nil
}

// Heartbeat sends POST /v1/nodes/{id}/heartbeat with current resource usage.
// On 404 (node evicted), it re-registers and retries the heartbeat.
func (c *Client) Heartbeat(ctx context.Context, req HeartbeatRequest) error {
	c.mu.RLock()
	nodeID := c.nodeID
	c.mu.RUnlock()

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal heartbeat: %w", err)
	}

	resp, err := c.doAuthRequest(ctx, http.MethodPost, fmt.Sprintf("/v1/nodes/%s/heartbeat", nodeID), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		c.logger.Warn("node not found during heartbeat, re-registering")
		if _, err := c.Register(ctx); err != nil {
			return fmt.Errorf("re-register after 404: %w", err)
		}
		// Retry heartbeat with new nodeID
		c.mu.RLock()
		newNodeID := c.nodeID
		c.mu.RUnlock()

		resp2, err := c.doAuthRequest(ctx, http.MethodPost, fmt.Sprintf("/v1/nodes/%s/heartbeat", newNodeID), body)
		if err != nil {
			return err
		}
		defer resp2.Body.Close()
		if resp2.StatusCode >= 400 {
			respBody, _ := io.ReadAll(resp2.Body)
			return fmt.Errorf("heartbeat retry returned %d: %s", resp2.StatusCode, string(respBody))
		}
		return nil
	}

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("heartbeat returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// PollCommands sends GET /v1/agents/{id}/commands?wait={waitSeconds} and returns
// the list of commands. The HTTP timeout is set to wait+5 seconds to account for
// network latency. Returns nil (not error) on timeout with no commands.
func (c *Client) PollCommands(ctx context.Context, waitSeconds int) ([]Command, error) {
	c.mu.RLock()
	nodeID := c.nodeID
	c.mu.RUnlock()

	path := fmt.Sprintf("/v1/agents/%s/commands?wait=%d", nodeID, waitSeconds)

	// Create a client with the specific poll timeout
	pollClient := &http.Client{
		Timeout: time.Duration(waitSeconds+5) * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("create poll request: %w", err)
	}

	c.mu.RLock()
	token := c.authToken()
	c.mu.RUnlock()
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := pollClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("poll commands: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		c.logger.Warn("unauthorized during poll, re-registering")
		if _, err := c.Register(ctx); err != nil {
			return nil, fmt.Errorf("re-register after 401: %w", err)
		}
		// Retry poll with new credentials
		c.mu.RLock()
		newNodeID := c.nodeID
		newToken := c.authToken()
		c.mu.RUnlock()

		retryPath := fmt.Sprintf("/v1/agents/%s/commands?wait=%d", newNodeID, waitSeconds)
		retryReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+retryPath, nil)
		if err != nil {
			return nil, fmt.Errorf("create poll retry request: %w", err)
		}
		retryReq.Header.Set("Authorization", "Bearer "+newToken)

		resp2, err := pollClient.Do(retryReq)
		if err != nil {
			return nil, fmt.Errorf("poll commands retry: %w", err)
		}
		defer resp2.Body.Close()

		return c.parseCommands(resp2)
	}

	return c.parseCommands(resp)
}

// parseCommands decodes a CommandsResponse from an HTTP response.
func (c *Client) parseCommands(resp *http.Response) ([]Command, error) {
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("poll commands returned %d: %s", resp.StatusCode, string(respBody))
	}

	var cmdsResp CommandsResponse
	if err := json.NewDecoder(resp.Body).Decode(&cmdsResp); err != nil {
		return nil, fmt.Errorf("decode commands response: %w", err)
	}
	return cmdsResp.Commands, nil
}

// AckCommand sends POST /v1/agents/{id}/commands/{cmdID}/ack.
func (c *Client) AckCommand(ctx context.Context, cmdID string, ack AckRequest) error {
	c.mu.RLock()
	nodeID := c.nodeID
	c.mu.RUnlock()

	body, err := json.Marshal(ack)
	if err != nil {
		return fmt.Errorf("marshal ack: %w", err)
	}

	path := fmt.Sprintf("/v1/agents/%s/commands/%s/ack", nodeID, cmdID)
	resp, err := c.doAuthRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		c.logger.Warn("unauthorized during ack, re-registering")
		if _, err := c.Register(ctx); err != nil {
			return fmt.Errorf("re-register after 401: %w", err)
		}
		c.mu.RLock()
		newNodeID := c.nodeID
		c.mu.RUnlock()
		retryPath := fmt.Sprintf("/v1/agents/%s/commands/%s/ack", newNodeID, cmdID)
		resp2, err := c.doAuthRequest(ctx, http.MethodPost, retryPath, body)
		if err != nil {
			return err
		}
		defer resp2.Body.Close()
		if resp2.StatusCode >= 400 {
			respBody, _ := io.ReadAll(resp2.Body)
			return fmt.Errorf("ack retry returned %d: %s", resp2.StatusCode, string(respBody))
		}
		return nil
	}

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ack returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// ReportEvent sends POST /v1/agents/{id}/events. This is fire-and-forget:
// it retries once on error, then returns.
func (c *Client) ReportEvent(ctx context.Context, event EventReport) error {
	c.mu.RLock()
	nodeID := c.nodeID
	c.mu.RUnlock()

	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	path := fmt.Sprintf("/v1/agents/%s/events", nodeID)
	resp, err := c.doAuthRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		// Retry once
		c.logger.Warn("event report failed, retrying once", "error", err)
		resp, err = c.doAuthRequest(ctx, http.MethodPost, path, body)
		if err != nil {
			return fmt.Errorf("event report failed after retry: %w", err)
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		c.logger.Warn("unauthorized during event report, re-registering")
		if _, err := c.Register(ctx); err != nil {
			return fmt.Errorf("re-register after 401: %w", err)
		}
		c.mu.RLock()
		newNodeID := c.nodeID
		c.mu.RUnlock()
		retryPath := fmt.Sprintf("/v1/agents/%s/events", newNodeID)
		resp2, err := c.doAuthRequest(ctx, http.MethodPost, retryPath, body)
		if err != nil {
			return fmt.Errorf("event report retry after re-register: %w", err)
		}
		defer resp2.Body.Close()
	}

	return nil
}

// NodeID returns the current node ID.
func (c *Client) NodeID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.nodeID
}

// doAuthRequest sends an authenticated HTTP request with the current bearer token.
func (c *Client) doAuthRequest(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	c.mu.RLock()
	token := c.authToken()
	c.mu.RUnlock()

	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.httpClient.Do(req)
}
