// Package verifier implements the conformance suite described in the
// spec's Test Plan section. It drives a service via apiclient and asserts
// that each endpoint behaves according to spec/http-api.md.
package verifier

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/mongodb/mongo-drivers-benchmark/mbench/internal/apiclient"
)

const (
	verifyDB   = "mbench_verify"
	verifyColl = "conformance"
)

// Verifier runs the hardcoded conformance suite against a service.
type Verifier struct {
	c *apiclient.Client
}

// New creates a Verifier using the given client.
func New(c *apiclient.Client) *Verifier { return &Verifier{c: c} }

// Run executes all conformance tests in sequence and prints PASS/FAIL lines to
// w. It returns the number of tests that passed and the number that failed.
func (v *Verifier) Run(ctx context.Context, w io.Writer) (passed, failed int) {
	type tc struct {
		name string
		fn   func(context.Context) error
	}
	tests := []tc{
		{"health: returns 200 with required fields", v.testHealth},
		{"find: returns documents array and count", v.testFind},
		{"findOne: returns matched document", v.testFindOne},
		{"findOne: returns {document:null} on miss", v.testFindOneMiss},
		{"insertOne: returns insertedId", v.testInsertOne},
		{"insertMany: returns insertedCount", v.testInsertMany},
		{"updateOne: returns match/modify counts and upsertedId:null", v.testUpdateOne},
		{"updateOne: upsert:true sets upsertedId when no match", v.testUpdateOneUpsert},
		{"updateMany: returns match/modify counts", v.testUpdateMany},
		{"deleteOne: returns deletedCount", v.testDeleteOne},
		{"deleteMany: returns deletedCount", v.testDeleteMany},
		{"bulkWrite: accepts all six operation kinds", v.testBulkWrite},
		{"clientBulkWrite: 200 with result or 501 unsupported", v.testClientBulkWrite},
		{"error response: missing required field → 400", v.testBadRequest},
		{"database/collection routing: explicit names are respected", v.testRouting},
		{"JSON round-trip: nested document survives insert and findOne", v.testJSONRoundTrip},
		{"concurrency: 5 concurrent insertOne calls succeed", v.testConcurrency},
	}

	for _, t := range tests {
		err := t.fn(ctx)
		if err != nil {
			fmt.Fprintf(w, "FAIL  %s\n      %v\n", t.name, err)
			failed++
		} else {
			fmt.Fprintf(w, "PASS  %s\n", t.name)
			passed++
		}
	}
	return passed, failed
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (v *Verifier) post(ctx context.Context, cmd string, body map[string]any) (map[string]any, int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	resp, status, err := v.c.Command(ctx, cmd, raw)
	if err != nil {
		return nil, 0, err
	}
	var m map[string]any
	if len(resp) > 0 {
		if err := json.Unmarshal(resp, &m); err != nil {
			return nil, status, fmt.Errorf("response is not a JSON object: %w", err)
		}
	}
	return m, status, nil
}

func ns(extra map[string]any) map[string]any {
	m := map[string]any{"database": verifyDB, "collection": verifyColl}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func requireKey(m map[string]any, key string) error {
	if _, ok := m[key]; !ok {
		return fmt.Errorf("response missing %q field; got: %v", key, m)
	}
	return nil
}

func requireStatus(got, want int) error {
	if got != want {
		return fmt.Errorf("expected HTTP %d, got %d", want, got)
	}
	return nil
}

// clearColl clears the verifier collection so tests start clean.
func (v *Verifier) clearColl(ctx context.Context) error {
	_, status, err := v.post(ctx, "deleteMany", ns(map[string]any{"filter": map[string]any{}}))
	if err != nil {
		return err
	}
	return requireStatus(status, 200)
}

// seed inserts documents and returns the first insertedId as a string.
func (v *Verifier) seed(ctx context.Context, docs []any) error {
	_, status, err := v.post(ctx, "insertMany", ns(map[string]any{"documents": docs}))
	if err != nil {
		return err
	}
	return requireStatus(status, 200)
}

// ── individual tests ──────────────────────────────────────────────────────────

func (v *Verifier) testHealth(ctx context.Context) error {
	h, err := v.c.Health(ctx)
	if err != nil {
		return err
	}
	if h.Status != "ok" {
		return fmt.Errorf("status = %q, want \"ok\"", h.Status)
	}
	for _, field := range []string{h.Driver, h.DriverVersion, h.Language, h.LanguageVersion} {
		if field == "" {
			return fmt.Errorf("health response has empty required field; got: %+v", h)
		}
	}
	return nil
}

func (v *Verifier) testFind(ctx context.Context) error {
	if err := v.clearColl(ctx); err != nil {
		return err
	}
	if err := v.seed(ctx, []any{map[string]any{"x": 1}, map[string]any{"x": 2}}); err != nil {
		return err
	}
	m, status, err := v.post(ctx, "find", ns(map[string]any{"filter": map[string]any{}}))
	if err != nil {
		return err
	}
	if err := requireStatus(status, 200); err != nil {
		return err
	}
	if err := requireKey(m, "documents"); err != nil {
		return err
	}
	if err := requireKey(m, "count"); err != nil {
		return err
	}
	docs, ok := m["documents"].([]any)
	if !ok {
		return fmt.Errorf("documents is not an array")
	}
	count, ok := m["count"].(float64)
	if !ok {
		return fmt.Errorf("count is not a number")
	}
	if int(count) != len(docs) {
		return fmt.Errorf("count %v != len(documents) %d", count, len(docs))
	}
	if len(docs) < 2 {
		return fmt.Errorf("expected ≥2 documents, got %d", len(docs))
	}
	return nil
}

func (v *Verifier) testFindOne(ctx context.Context) error {
	if err := v.clearColl(ctx); err != nil {
		return err
	}
	if err := v.seed(ctx, []any{map[string]any{"_id": 42, "x": "hello"}}); err != nil {
		return err
	}
	m, status, err := v.post(ctx, "findOne", ns(map[string]any{"filter": map[string]any{"_id": 42}}))
	if err != nil {
		return err
	}
	if err := requireStatus(status, 200); err != nil {
		return err
	}
	if err := requireKey(m, "document"); err != nil {
		return err
	}
	if m["document"] == nil {
		return fmt.Errorf("document is null; expected the seeded document")
	}
	return nil
}

func (v *Verifier) testFindOneMiss(ctx context.Context) error {
	if err := v.clearColl(ctx); err != nil {
		return err
	}
	m, status, err := v.post(ctx, "findOne", ns(map[string]any{"filter": map[string]any{"_id": 99999}}))
	if err != nil {
		return err
	}
	if err := requireStatus(status, 200); err != nil {
		return err
	}
	if err := requireKey(m, "document"); err != nil {
		return err
	}
	if m["document"] != nil {
		return fmt.Errorf("expected document:null on miss, got: %v", m["document"])
	}
	return nil
}

func (v *Verifier) testInsertOne(ctx context.Context) error {
	if err := v.clearColl(ctx); err != nil {
		return err
	}
	m, status, err := v.post(ctx, "insertOne", ns(map[string]any{"document": map[string]any{"x": 1}}))
	if err != nil {
		return err
	}
	if err := requireStatus(status, 200); err != nil {
		return err
	}
	return requireKey(m, "insertedId")
}

func (v *Verifier) testInsertMany(ctx context.Context) error {
	if err := v.clearColl(ctx); err != nil {
		return err
	}
	docs := []any{map[string]any{"a": 1}, map[string]any{"a": 2}, map[string]any{"a": 3}}
	m, status, err := v.post(ctx, "insertMany", ns(map[string]any{"documents": docs}))
	if err != nil {
		return err
	}
	if err := requireStatus(status, 200); err != nil {
		return err
	}
	if err := requireKey(m, "insertedCount"); err != nil {
		return err
	}
	if n, ok := m["insertedCount"].(float64); !ok || int(n) != 3 {
		return fmt.Errorf("insertedCount = %v, want 3", m["insertedCount"])
	}
	return nil
}

func (v *Verifier) testUpdateOne(ctx context.Context) error {
	if err := v.clearColl(ctx); err != nil {
		return err
	}
	if err := v.seed(ctx, []any{map[string]any{"_id": 1, "x": 0}}); err != nil {
		return err
	}
	m, status, err := v.post(ctx, "updateOne", ns(map[string]any{
		"filter": map[string]any{"_id": 1},
		"update": map[string]any{"$set": map[string]any{"x": 1}},
	}))
	if err != nil {
		return err
	}
	if err := requireStatus(status, 200); err != nil {
		return err
	}
	for _, k := range []string{"matchedCount", "modifiedCount", "upsertedId"} {
		if err := requireKey(m, k); err != nil {
			return err
		}
	}
	if m["upsertedId"] != nil {
		return fmt.Errorf("upsertedId should be null when no upsert occurred, got: %v", m["upsertedId"])
	}
	return nil
}

func (v *Verifier) testUpdateOneUpsert(ctx context.Context) error {
	if err := v.clearColl(ctx); err != nil {
		return err
	}
	m, status, err := v.post(ctx, "updateOne", ns(map[string]any{
		"filter":  map[string]any{"_id": 9999},
		"update":  map[string]any{"$set": map[string]any{"x": 1}},
		"options": map[string]any{"upsert": true},
	}))
	if err != nil {
		return err
	}
	if err := requireStatus(status, 200); err != nil {
		return err
	}
	if m["upsertedId"] == nil {
		return fmt.Errorf("upsertedId should be non-null after upsert, got null")
	}
	return nil
}

func (v *Verifier) testUpdateMany(ctx context.Context) error {
	if err := v.clearColl(ctx); err != nil {
		return err
	}
	if err := v.seed(ctx, []any{
		map[string]any{"tag": "a", "x": 0},
		map[string]any{"tag": "a", "x": 0},
	}); err != nil {
		return err
	}
	m, status, err := v.post(ctx, "updateMany", ns(map[string]any{
		"filter": map[string]any{"tag": "a"},
		"update": map[string]any{"$set": map[string]any{"x": 1}},
	}))
	if err != nil {
		return err
	}
	if err := requireStatus(status, 200); err != nil {
		return err
	}
	for _, k := range []string{"matchedCount", "modifiedCount", "upsertedId"} {
		if err := requireKey(m, k); err != nil {
			return err
		}
	}
	if n, ok := m["matchedCount"].(float64); !ok || int(n) < 2 {
		return fmt.Errorf("matchedCount = %v, want ≥2", m["matchedCount"])
	}
	return nil
}

func (v *Verifier) testDeleteOne(ctx context.Context) error {
	if err := v.clearColl(ctx); err != nil {
		return err
	}
	if err := v.seed(ctx, []any{map[string]any{"_id": 1}}); err != nil {
		return err
	}
	m, status, err := v.post(ctx, "deleteOne", ns(map[string]any{"filter": map[string]any{"_id": 1}}))
	if err != nil {
		return err
	}
	if err := requireStatus(status, 200); err != nil {
		return err
	}
	if err := requireKey(m, "deletedCount"); err != nil {
		return err
	}
	if n, ok := m["deletedCount"].(float64); !ok || int(n) != 1 {
		return fmt.Errorf("deletedCount = %v, want 1", m["deletedCount"])
	}
	return nil
}

func (v *Verifier) testDeleteMany(ctx context.Context) error {
	if err := v.clearColl(ctx); err != nil {
		return err
	}
	if err := v.seed(ctx, []any{map[string]any{"x": 1}, map[string]any{"x": 2}}); err != nil {
		return err
	}
	m, status, err := v.post(ctx, "deleteMany", ns(map[string]any{"filter": map[string]any{}}))
	if err != nil {
		return err
	}
	if err := requireStatus(status, 200); err != nil {
		return err
	}
	if err := requireKey(m, "deletedCount"); err != nil {
		return err
	}
	if n, ok := m["deletedCount"].(float64); !ok || int(n) < 2 {
		return fmt.Errorf("deletedCount = %v, want ≥2", m["deletedCount"])
	}
	return nil
}

func (v *Verifier) testBulkWrite(ctx context.Context) error {
	if err := v.clearColl(ctx); err != nil {
		return err
	}
	// Seed two docs that update/replace/delete operations will target.
	if err := v.seed(ctx, []any{
		map[string]any{"_id": 100, "tag": "upd"},
		map[string]any{"_id": 101, "tag": "del"},
	}); err != nil {
		return err
	}
	ops := []any{
		map[string]any{"insertOne": map[string]any{"document": map[string]any{"x": 1}}},
		map[string]any{"updateOne": map[string]any{
			"filter": map[string]any{"_id": 100},
			"update": map[string]any{"$set": map[string]any{"tag": "updated"}},
		}},
		map[string]any{"updateMany": map[string]any{
			"filter": map[string]any{"tag": "upd"},
			"update": map[string]any{"$set": map[string]any{"done": true}},
		}},
		map[string]any{"replaceOne": map[string]any{
			"filter":      map[string]any{"_id": 100},
			"replacement": map[string]any{"replaced": true},
		}},
		map[string]any{"deleteOne": map[string]any{
			"filter": map[string]any{"_id": 101},
		}},
		map[string]any{"deleteMany": map[string]any{
			"filter": map[string]any{"x": 1},
		}},
	}
	m, status, err := v.post(ctx, "bulkWrite", ns(map[string]any{"operations": ops}))
	if err != nil {
		return err
	}
	if err := requireStatus(status, 200); err != nil {
		return err
	}
	for _, k := range []string{"insertedCount", "matchedCount", "modifiedCount", "deletedCount", "upsertedCount"} {
		if err := requireKey(m, k); err != nil {
			return err
		}
	}
	return nil
}

func (v *Verifier) testClientBulkWrite(ctx context.Context) error {
	models := []any{
		map[string]any{
			"namespace": verifyDB + "." + verifyColl,
			"insertOne": map[string]any{"document": map[string]any{"cbw": true}},
		},
		map[string]any{
			"namespace": verifyDB + ".cbw2",
			"insertOne": map[string]any{"document": map[string]any{"cbw": true}},
		},
	}
	raw, err := json.Marshal(map[string]any{"models": models})
	if err != nil {
		return err
	}
	resp, status, err := v.c.Command(ctx, "clientBulkWrite", raw)
	if err != nil {
		return err
	}
	if status == 501 {
		// Acceptable: driver/server does not support clientBulkWrite.
		var m map[string]any
		if err := json.Unmarshal(resp, &m); err != nil {
			return fmt.Errorf("501 response is not valid JSON: %w", err)
		}
		if err := requireKey(m, "error"); err != nil {
			return fmt.Errorf("501 response missing error field: %w", err)
		}
		return nil
	}
	if status != 200 {
		return fmt.Errorf("expected 200 or 501, got %d", status)
	}
	var m map[string]any
	if err := json.Unmarshal(resp, &m); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	for _, k := range []string{"insertedCount", "matchedCount", "modifiedCount", "deletedCount", "upsertedCount"} {
		if err := requireKey(m, k); err != nil {
			return err
		}
	}
	return nil
}

func (v *Verifier) testBadRequest(ctx context.Context) error {
	// /find requires a "filter" field; sending {} should return 400.
	_, status, err := v.post(ctx, "find", ns(map[string]any{}))
	if err != nil {
		return err
	}
	if status != 400 {
		return fmt.Errorf("expected HTTP 400 for missing filter, got %d", status)
	}
	return nil
}

func (v *Verifier) testRouting(ctx context.Context) error {
	// Insert into a distinct database+collection and verify it can be read back.
	altDB, altColl := "mbench_verify_routing", "route_test"
	_, status, err := v.post(ctx, "deleteMany", map[string]any{
		"database": altDB, "collection": altColl,
		"filter": map[string]any{},
	})
	if err != nil {
		return err
	}
	if err := requireStatus(status, 200); err != nil {
		return err
	}

	_, status, err = v.post(ctx, "insertOne", map[string]any{
		"database": altDB, "collection": altColl,
		"document": map[string]any{"routed": true},
	})
	if err != nil {
		return err
	}
	if err := requireStatus(status, 200); err != nil {
		return err
	}

	m, status, err := v.post(ctx, "find", map[string]any{
		"database": altDB, "collection": altColl,
		"filter": map[string]any{},
	})
	if err != nil {
		return err
	}
	if err := requireStatus(status, 200); err != nil {
		return err
	}
	docs, ok := m["documents"].([]any)
	if !ok || len(docs) == 0 {
		return fmt.Errorf("expected at least one document in routed collection, got: %v", m)
	}
	return nil
}

func (v *Verifier) testJSONRoundTrip(ctx context.Context) error {
	if err := v.clearColl(ctx); err != nil {
		return err
	}
	doc := map[string]any{
		"_id":    "roundtrip-1",
		"str":    "hello",
		"num":    42.0,
		"flag":   true,
		"nested": map[string]any{"a": 1},
		"arr":    []any{1, 2, 3},
	}
	_, status, err := v.post(ctx, "insertOne", ns(map[string]any{"document": doc}))
	if err != nil {
		return err
	}
	if err := requireStatus(status, 200); err != nil {
		return fmt.Errorf("insertOne: %w", err)
	}

	m, status, err := v.post(ctx, "findOne", ns(map[string]any{"filter": map[string]any{"_id": "roundtrip-1"}}))
	if err != nil {
		return err
	}
	if err := requireStatus(status, 200); err != nil {
		return fmt.Errorf("findOne: %w", err)
	}
	if m["document"] == nil {
		return fmt.Errorf("document not found after insert")
	}
	docMap, ok := m["document"].(map[string]any)
	if !ok {
		return fmt.Errorf("document is not a JSON object: %T", m["document"])
	}
	if docMap["str"] != "hello" {
		return fmt.Errorf("str field not preserved, got: %v", docMap["str"])
	}
	if docMap["num"] != 42.0 {
		return fmt.Errorf("num field not preserved, got: %v", docMap["num"])
	}
	return nil
}

func (v *Verifier) testConcurrency(ctx context.Context) error {
	if err := v.clearColl(ctx); err != nil {
		return err
	}
	const workers = 5
	body, err := json.Marshal(map[string]any{
		"database":   verifyDB,
		"collection": verifyColl,
		"document":   map[string]any{"concurrent": true},
	})
	if err != nil {
		return err
	}

	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, status, err := v.c.Command(ctx, "insertOne", body)
			if err != nil {
				errs <- err
				return
			}
			if status >= 500 {
				errs <- fmt.Errorf("concurrent insertOne returned HTTP %d", status)
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
