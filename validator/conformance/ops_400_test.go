package conformance

import (
	"testing"
)

func TestConformance_Ops_RejectsMissingDatabase(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	resp, _, err := c.RawPost(Ctx(t), "/v1/ops", []byte(`{"ops":[{"name":"insertOne","collection":"x","document":{}}]}`))
	if err != nil {
		t.Fatalf("RawPost: %v", err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400 (missing required `database`)", resp.StatusCode)
	}
}

func TestConformance_Ops_RejectsEmptyOps(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	resp, _, err := c.RawPost(Ctx(t), "/v1/ops", []byte(`{"database":"x","ops":[]}`))
	if err != nil {
		t.Fatalf("RawPost: %v", err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400 (empty ops)", resp.StatusCode)
	}
}

func TestConformance_Ops_RejectsUnknownOpName(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	resp, _, err := c.RawPost(Ctx(t), "/v1/ops",
		[]byte(`{"database":"x","ops":[{"name":"frobnicate","collection":"x"}]}`))
	if err != nil {
		t.Fatalf("RawPost: %v", err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400 (unknown op name)", resp.StatusCode)
	}
}

func TestConformance_Ops_RejectsSchemaViolation(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	// insertOne missing required `document` field.
	resp, _, err := c.RawPost(Ctx(t), "/v1/ops",
		[]byte(`{"database":"x","ops":[{"name":"insertOne","collection":"x"}]}`))
	if err != nil {
		t.Fatalf("RawPost: %v", err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400 (insertOne missing document)", resp.StatusCode)
	}
}
