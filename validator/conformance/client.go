package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Client is an HTTP client for the load-test service. Each typed method
// auto-validates the response body against the OpenAPI schema and reports
// failures via the bound *testing.T.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	Spec    *Spec
	t       *testing.T
}

// NewClient returns a Client bound to t. If t is nil schema validation
// failures become returned errors instead of t.Errorf.
func NewClient(t *testing.T) *Client {
	t.Helper()
	spec, err := SharedSpec()
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	url := BaseURL()
	if url == "" {
		t.Skip("no target service URL (set -url= or MDBV_URL)")
	}
	return &Client{
		BaseURL: url,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		Spec:    spec,
		t:       t,
	}
}

// NewClientFor returns a Client targeting an arbitrary base URL. Used by unit
// tests that point at an httptest.Server.
func NewClientFor(t *testing.T, baseURL string, spec *Spec) *Client {
	return &Client{
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		Spec:    spec,
		t:       t,
	}
}

// ---- Typed calls. Each validates response shape automatically. ----

func (c *Client) Info(ctx context.Context) (InfoResponse, *http.Response, error) {
	var out InfoResponse
	resp, err := c.do(ctx, http.MethodGet, "/v1/info", nil, &out)
	return out, resp, err
}

func (c *Client) Health(ctx context.Context) (HealthResponse, *http.Response, error) {
	var out HealthResponse
	resp, err := c.do(ctx, http.MethodGet, "/v1/health", nil, &out)
	return out, resp, err
}

func (c *Client) Reset(ctx context.Context, databases []string) (ResetResponse, *http.Response, error) {
	var out ResetResponse
	resp, err := c.do(ctx, http.MethodPost, "/v1/admin/reset", ResetRequest{Databases: databases}, &out)
	return out, resp, err
}

func (c *Client) Ops(ctx context.Context, req OpsRequest) (OpsResponse, *http.Response, error) {
	var out OpsResponse
	resp, err := c.do(ctx, http.MethodPost, "/v1/ops", req, &out)
	return out, resp, err
}

// RawPost POSTs body verbatim and returns the raw response body plus the
// *http.Response with its body already drained. Useful for malformed-request
// scenarios that must bypass typed marshaling. Does NOT run schema
// validation (a 400 RequestError shape is validated, anything else is left
// to the caller).
func (c *Client) RawPost(ctx context.Context, path string, body []byte) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	respBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(respBody))

	// If 400, validate against RequestError schema via the spec validator.
	if resp.StatusCode == http.StatusBadRequest {
		if errs := c.validate(req, resp); len(errs) > 0 {
			c.t.Errorf("[%s %s] 400 response failed schema validation:\n  %s",
				req.Method, req.URL.Path, joinErrs(errs))
		}
	}
	return resp, respBody, nil
}

// ---- core ----

func (c *Client) do(ctx context.Context, method, path string, body, out any) (*http.Response, error) {
	c.t.Helper()
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reqBody)
	if err != nil {
		return nil, err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}

	if errs := c.validate(req, resp); len(errs) > 0 {
		c.t.Errorf("[%s %s] response failed schema validation:\n  %s",
			method, path, joinErrs(errs))
	}

	// Body was rewound by validate; read it for unmarshaling.
	respBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return resp, fmt.Errorf("read body: %w", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(respBody))

	if resp.StatusCode/100 != 2 {
		return resp, fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, string(respBody))
	}

	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return resp, fmt.Errorf("decode body: %w (body=%s)", err, string(respBody))
		}
	}
	return resp, nil
}

func (c *Client) validate(req *http.Request, resp *http.Response) []string {
	errs, err := c.Spec.ValidateResponse(req, resp)
	if err != nil {
		return []string{fmt.Sprintf("validator internal error: %v", err)}
	}
	return errs
}

func joinErrs(errs []string) string {
	return strings.Join(errs, "\n  ")
}
