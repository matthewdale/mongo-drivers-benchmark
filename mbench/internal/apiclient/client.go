package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client wraps an HTTP client and routes typed requests to a benchmark service.
type Client struct {
	baseURL string
	http    *http.Client
}

// New creates a Client targeting baseURL (trailing slash is trimmed).
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http: &http.Client{
			Transport: &http.Transport{
				// Allow enough idle connections to cover high-concurrency workloads
				// without re-establishing TCP connections between iterations.
				MaxIdleConns:        512,
				MaxIdleConnsPerHost: 512,
			},
		},
	}
}

// HealthResponse is the response body from GET /health.
type HealthResponse struct {
	Status          string `json:"status"`
	Driver          string `json:"driver"`
	DriverVersion   string `json:"driverVersion"`
	Language        string `json:"language"`
	LanguageVersion string `json:"languageVersion"`
}

// Health calls GET /health and returns the parsed response.
func (c *Client) Health(ctx context.Context) (*HealthResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET /health: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /health: HTTP %d", resp.StatusCode)
	}
	var h HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return nil, fmt.Errorf("decoding /health response: %w", err)
	}
	return &h, nil
}

// Command POSTs body to /<name> and returns the raw response body and HTTP
// status code. A network error returns a non-nil error; an HTTP error status
// does not — callers inspect the status code directly.
func (c *Client) Command(ctx context.Context, name string, body json.RawMessage) (json.RawMessage, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/"+name, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("POST /%s: %w", name, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading POST /%s response: %w", name, err)
	}
	return json.RawMessage(data), resp.StatusCode, nil
}

// Exec POSTs body to /<name> and returns only the HTTP status code. The
// response body is drained into io.Discard to keep the connection alive but
// is not buffered, saving an allocation on every hot-path iteration.
func (c *Client) Exec(ctx context.Context, name string, body json.RawMessage) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/"+name, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("POST /%s: %w", name, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}
