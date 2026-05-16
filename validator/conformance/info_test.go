package conformance

import (
	"slices"
	"testing"
)

func TestConformance_Info_ShapeAndVersion(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	info, _, err := c.Info(Ctx(t))
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Driver == "" {
		t.Error("info.driver is empty")
	}
	if info.DriverVersion == "" {
		t.Error("info.driver_version is empty")
	}
	if info.LanguageVersion == "" {
		t.Error("info.language_version is empty")
	}
	allowed := []string{"go", "python", "node", "java", "php"}
	if !slices.Contains(allowed, info.Language) {
		t.Errorf("info.language %q not in %v", info.Language, allowed)
	}
	if info.SpecVersion != "1.0.0" {
		t.Errorf("info.spec_version = %q, want %q", info.SpecVersion, "1.0.0")
	}
}

func TestConformance_Health_OK(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	health, resp, err := c.Health(Ctx(t))
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("health status = %d, want 200 (cluster must be reachable for the validator to run)", resp.StatusCode)
	}
	if !health.OK {
		t.Errorf("health.ok = false: %s", health.Detail)
	}
}

func TestConformance_Reset_DropsDatabases(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	db := Setup(t, c)

	// Insert a doc, count it, reset the DB, count again.
	insert, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops: []Op{
			InsertOne("things", JSON(`{"x": 1}`)),
			CountDocuments("things", JSON(`{}`)),
		},
	})
	if err != nil {
		t.Fatalf("seed ops: %v", err)
	}
	if len(insert.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(insert.Results))
	}
	ExpectOK(t, insert.Results[0], "insertOne")
	ExpectOK(t, insert.Results[1], "countDocuments")
	pre, err := DecodeData[CountData](insert.Results[1])
	if err != nil {
		t.Fatalf("decode pre-count: %v", err)
	}
	if pre.Count != 1 {
		t.Fatalf("pre-reset count = %d, want 1", pre.Count)
	}

	resp, _, err := c.Reset(Ctx(t), []string{db})
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if !slices.Contains(resp.Dropped, db) {
		t.Errorf("reset response missing %q: %v", db, resp.Dropped)
	}

	post, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops:      []Op{CountDocuments("things", JSON(`{}`))},
	})
	if err != nil {
		t.Fatalf("post-reset count: %v", err)
	}
	ExpectOK(t, post.Results[0], "countDocuments")
	postCount, err := DecodeData[CountData](post.Results[0])
	if err != nil {
		t.Fatalf("decode post-count: %v", err)
	}
	if postCount.Count != 0 {
		t.Errorf("post-reset count = %d, want 0", postCount.Count)
	}
}

func TestConformance_Reset_RejectsEmpty(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	resp, _, err := c.RawPost(Ctx(t), "/v1/admin/reset", []byte(`{"databases": []}`))
	if err != nil {
		t.Fatalf("RawPost: %v", err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400 for empty databases array", resp.StatusCode)
	}
}
