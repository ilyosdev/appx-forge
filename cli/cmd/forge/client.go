package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// apiError represents an RFC 7807 problem response from the control plane.
type apiError struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

func (e *apiError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s", e.Title, e.Detail)
	}
	return e.Title
}

// apiClient wraps HTTP requests to the control plane API.
type apiClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// newAPIClient creates a client for the given base URL and bearer token.
func newAPIClient(baseURL, token string) *apiClient {
	return &apiClient{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// get sends a GET request and returns the raw response body.
func (c *apiClient) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, c.parseError(body, resp.StatusCode)
	}

	return body, nil
}

// getRaw sends a GET request and returns the raw response body as-is (for text/plain).
func (c *apiClient) getRaw(ctx context.Context, path string, query url.Values) ([]byte, error) {
	return c.get(ctx, path, query)
}

// getNoAuth sends a GET request without the Authorization header.
func (c *apiClient) getNoAuth(ctx context.Context, path string) ([]byte, error) {
	u := c.baseURL + path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	// Intentionally no auth header

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, c.parseError(body, resp.StatusCode)
	}

	return body, nil
}

// post sends a POST request with a JSON body and returns the raw response body.
func (c *apiClient) post(ctx context.Context, path string, payload interface{}) ([]byte, error) {
	var bodyReader io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshaling request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	c.setAuth(req)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, c.parseError(body, resp.StatusCode)
	}

	return body, nil
}

// del sends a DELETE request.
func (c *apiClient) del(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return c.parseError(body, resp.StatusCode)
	}

	return nil
}

// setAuth adds the Bearer token header. Token value is never logged (T-06-08).
func (c *apiClient) setAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// parseError attempts to parse an RFC 7807 problem response.
func (c *apiClient) parseError(body []byte, statusCode int) error {
	var ae apiError
	if err := json.Unmarshal(body, &ae); err == nil && ae.Title != "" {
		ae.Status = statusCode
		return &ae
	}
	return fmt.Errorf("HTTP %d: %s", statusCode, string(body))
}
