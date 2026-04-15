// Package routing provides Caddy Admin API client and route update batching.
package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Route represents a Caddy reverse proxy route for a sandbox.
type Route struct {
	AppName   string // e.g. "my-app" -> routes my-app.myappx.live
	SandboxID string // passed as X-Sandbox-ID header
	Upstream  string // tailscale_ip:host_port
}

// CaddyClient communicates with the Caddy Admin API.
type CaddyClient struct {
	baseURL string
	http    *http.Client
}

// NewCaddyClient creates a CaddyClient targeting the given Caddy Admin API base URL.
func NewCaddyClient(baseURL string) *CaddyClient {
	return &CaddyClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// AddRoute adds a reverse proxy route to Caddy for the given sandbox.
func (c *CaddyClient) AddRoute(ctx context.Context, r Route) error {
	body := buildRouteJSON(r)

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal route: %w", err)
	}

	url := c.baseURL + "/config/apps/http/servers/srv0/routes"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("caddy POST: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("caddy AddRoute: status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// RemoveRoute removes a sandbox route from Caddy. Returns nil on 404 (idempotent).
func (c *CaddyClient) RemoveRoute(ctx context.Context, appName string) error {
	url := c.baseURL + "/id/route-" + appName
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("caddy DELETE: %w", err)
	}
	defer resp.Body.Close()

	// 404 is expected when route already removed -- idempotent.
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		return nil
	}

	respBody, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("caddy RemoveRoute: status %d: %s", resp.StatusCode, string(respBody))
}

// ListRoutes fetches all routes from Caddy and parses them into Route structs.
func (c *CaddyClient) ListRoutes(ctx context.Context) ([]Route, error) {
	url := c.baseURL + "/config/apps/http/servers/srv0/routes"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("caddy GET: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("caddy ListRoutes: status %d: %s", resp.StatusCode, string(respBody))
	}

	var raw []caddyRoutePayload
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode routes: %w", err)
	}

	routes := make([]Route, 0, len(raw))
	for _, r := range raw {
		route := parseRoute(r)
		if route.AppName != "" {
			routes = append(routes, route)
		}
	}
	return routes, nil
}

// Apply implements Flusher by calling AddRoute for each add and RemoveRoute for
// each remove. Errors are collected; partial failures do not stop remaining ops.
func (c *CaddyClient) Apply(ctx context.Context, adds []Route, removes []string) error {
	var errs []error

	for _, r := range adds {
		if err := c.AddRoute(ctx, r); err != nil {
			errs = append(errs, fmt.Errorf("add %s: %w", r.AppName, err))
		}
	}
	for _, appName := range removes {
		if err := c.RemoveRoute(ctx, appName); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", appName, err))
		}
	}

	return errors.Join(errs...)
}

// ── JSON Helpers ────────────────────────────────────────────────────

// buildRouteJSON constructs the Caddy route JSON payload per the proxy-routing contract.
func buildRouteJSON(r Route) map[string]any {
	return map[string]any{
		"@id": "route-" + r.AppName,
		"match": []map[string]any{
			{"host": []string{r.AppName + ".myappx.live"}},
		},
		"handle": []map[string]any{
			{
				"handler":   "reverse_proxy",
				"upstreams": []map[string]string{{"dial": r.Upstream}},
				"transport":  map[string]string{"protocol": "http"},
				"headers": map[string]any{
					"request": map[string]any{
						"set": map[string][]string{
							"X-Forwarded-Host": {"{http.request.host}"},
							"X-Sandbox-ID":     {r.SandboxID},
						},
					},
				},
			},
		},
	}
}

// caddyRoutePayload represents a route as returned by Caddy's Admin API.
type caddyRoutePayload struct {
	ID     string `json:"@id"`
	Match  []struct {
		Host []string `json:"host"`
	} `json:"match"`
	Handle []struct {
		Handler   string `json:"handler"`
		Upstreams []struct {
			Dial string `json:"dial"`
		} `json:"upstreams"`
	} `json:"handle"`
}

// parseRoute extracts a Route from a Caddy route payload.
func parseRoute(p caddyRoutePayload) Route {
	var r Route

	// Extract AppName from @id: "route-{app_name}" -> "{app_name}"
	if strings.HasPrefix(p.ID, "route-") {
		r.AppName = strings.TrimPrefix(p.ID, "route-")
	}

	// Extract Upstream from first handler's first upstream
	if len(p.Handle) > 0 && len(p.Handle[0].Upstreams) > 0 {
		r.Upstream = p.Handle[0].Upstreams[0].Dial
	}

	return r
}
