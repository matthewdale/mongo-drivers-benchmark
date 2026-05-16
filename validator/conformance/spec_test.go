package conformance

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSpec_LoadsAndValidatesCanonicalExample loads the real spec/openapi.yaml
// and validates the canonical example response (embedded in the spec) against
// the OpsResponse schema.
func TestSpec_LoadsAndValidatesCanonicalExample(t *testing.T) {
	spec, err := LoadSpec("")
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}

	v3, err := spec.Doc.BuildV3Model()
	if err != nil {
		t.Fatalf("BuildV3Model: %v", err)
	}
	if v3.Model.Components == nil || v3.Model.Components.Examples == nil {
		t.Fatal("spec has no components.examples")
	}
	ex, ok := v3.Model.Components.Examples.Get("CanonicalOpsResponse")
	if !ok {
		t.Fatal("CanonicalOpsResponse example not found in spec")
	}

	var asAny any
	if err := ex.Value.Decode(&asAny); err != nil {
		t.Fatalf("decode example node: %v", err)
	}
	bodyJSON, err := json.Marshal(asAny)
	if err != nil {
		t.Fatalf("marshal example to JSON: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://test.local/v1/ops", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(bodyJSON)),
		Request:    req,
	}

	verrs, verr := spec.ValidateResponse(req, resp)
	if verr != nil {
		t.Fatalf("ValidateResponse error: %v", verr)
	}
	if len(verrs) > 0 {
		t.Fatalf("canonical example failed schema validation:\n  %s", strings.Join(verrs, "\n  "))
	}
}

// TestSpec_DetectsSchemaViolation feeds a deliberately-broken response (an
// InfoResponse missing the required spec_version field) through the spec
// validator and asserts it surfaces an error. This proves the response
// validation plumbing actually catches mistakes — without it, every other
// scenario's assertions would be trusting a no-op validator.
func TestSpec_DetectsSchemaViolation(t *testing.T) {
	spec, err := LoadSpec("")
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Missing required "spec_version".
		_, _ = w.Write([]byte(`{"driver":"x","driver_version":"1","language":"go","language_version":"1"}`))
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/info", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	verrs, verr := spec.ValidateResponse(req, resp)
	if verr != nil {
		t.Fatalf("ValidateResponse: %v", verr)
	}
	if len(verrs) == 0 {
		t.Fatal("expected a schema validation error, got none")
	}
	combined := strings.Join(verrs, " | ")
	if !strings.Contains(combined, "spec_version") {
		t.Logf("note: errors did not mention spec_version explicitly: %s", combined)
	}
}
