package conformance

import (
	"fmt"
	"testing"
)

// TestConformance_FailureDoesNotShortCircuit verifies §6.2 of the spec: when
// an op fails mid-request, subsequent ops MUST still execute. We trigger a
// DUPLICATE_KEY by inserting the same _id twice, then put a find after it.
// The find must run and return the original doc.
func TestConformance_FailureDoesNotShortCircuit(t *testing.T) {
	t.Parallel()
	c := NewClient(t)
	db := Setup(t, c)
	id := NewObjectID()
	doc := Doc(fmt.Sprintf(`{"_id": %s, "v": 1}`, string(id)))

	// Seed: insert once.
	if _, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops:      []Op{InsertOne("things", doc)},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, _, err := c.Ops(Ctx(t), OpsRequest{
		Database: db,
		Ops: []Op{
			InsertOne("things", doc), // duplicate _id -> DUPLICATE_KEY
			Find("things", JSON(`{}`)).WithSort(JSON(`{"_id": 1}`)),
		},
	})
	if err != nil {
		t.Fatalf("Ops: %v", err)
	}
	if got := len(resp.Results); got != 2 {
		t.Fatalf("results=%d, want 2 (the failing op MUST NOT short-circuit subsequent ops)", got)
	}
	// The first op should have failed. The validator's v1 doesn't require
	// the specific error code (that comes in v1.1) — just that the op
	// reported failure rather than being skipped or short-circuiting.
	if resp.Results[0].OK {
		t.Errorf("first op (duplicate insert) reported ok=true; expected ok=false")
	}
	// The second op MUST have executed.
	ExpectOK(t, resp.Results[1], "find")
	find, err := DecodeData[FindData](resp.Results[1])
	if err != nil {
		t.Fatalf("decode find: %v", err)
	}
	if find.Count != 1 {
		t.Errorf("find count = %d, want 1 (seed doc should still be present)", find.Count)
	}
}
