package conformance

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// UniqueDB returns a stable, conflict-free database name for t. Same test
// name → same database, so re-runs are deterministic; different test names
// (and parallel scenarios) never collide.
func UniqueDB(t *testing.T) string {
	t.Helper()
	h := sha256.Sum256([]byte(t.Name()))
	return "mdbv_" + hex.EncodeToString(h[:6])
}

// NewObjectID returns a fresh ObjectId in Extended JSON canonical form
// (`{"$oid": "..."}`). Encodes the 12 bytes as a 24-char hex string.
//
// Bytes 0..3: seconds-since-epoch big-endian; 4..8: random; 9..11: random.
// Matches the BSON ObjectId on-wire format closely enough that drivers will
// accept it.
func NewObjectID() Doc {
	var b [12]byte
	t := uint32(time.Now().Unix())
	b[0] = byte(t >> 24)
	b[1] = byte(t >> 16)
	b[2] = byte(t >> 8)
	b[3] = byte(t)
	if _, err := rand.Read(b[4:]); err != nil {
		// rand should not fail; if it does, fall back to a deterministic
		// suffix so callers don't crash.
		copy(b[4:], []byte("deadbeefcafe"))
	}
	raw, _ := json.Marshal(struct {
		OID string `json:"$oid"`
	}{OID: hex.EncodeToString(b[:])})
	return Doc(raw)
}

// JSON wraps a Go map-literal as a Doc. Scenarios use it to build filters
// and updates compactly: JSON(`{"n": {"$gte": 0}}`).
func JSON(s string) Doc {
	// Validate it's parsable, fail-fast at construction time so the
	// scenario error points at the right line rather than the HTTP call.
	var any any
	if err := json.Unmarshal([]byte(s), &any); err != nil {
		panic(fmt.Sprintf("JSON fixture is not valid JSON: %v\n  %s", err, s))
	}
	return Doc(s)
}

// Setup resets the per-test database before the scenario runs and again on
// cleanup. Returns the database name to use in OpsRequest.Database.
func Setup(t *testing.T, c *Client) string {
	t.Helper()
	db := UniqueDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, _, err := c.Reset(ctx, []string{db}); err != nil {
		t.Fatalf("setup reset: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, _, err := c.Reset(ctx, []string{db}); err != nil {
			t.Logf("cleanup reset failed (continuing): %v", err)
		}
	})
	return db
}

// Ctx returns a context with a sensible per-scenario timeout, cancelled on
// test cleanup.
func Ctx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// MustJSON returns json.Marshal(v) or t.Fatal's.
func MustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return b
}

// ExpectOK fails the test if r is not a successful op result of the given
// op name.
func ExpectOK(t *testing.T, r OpResult, opName string) {
	t.Helper()
	if r.Op != opName {
		t.Errorf("expected op=%q, got %q", opName, r.Op)
	}
	if !r.OK {
		var emsg, ecode string
		if r.Error != nil {
			emsg = r.Error.Message
			ecode = r.Error.Code
		}
		t.Errorf("op %s failed: code=%q message=%q", opName, ecode, emsg)
	}
}

// ExpectError fails the test if r is not a failed op with the given code.
func ExpectError(t *testing.T, r OpResult, opName, code string) {
	t.Helper()
	if r.Op != opName {
		t.Errorf("expected op=%q, got %q", opName, r.Op)
	}
	if r.OK {
		t.Errorf("expected op %s to fail with code %q, but it succeeded", opName, code)
		return
	}
	if r.Error == nil {
		t.Errorf("op %s failed but error field is nil", opName)
		return
	}
	if r.Error.Code != code {
		t.Errorf("op %s: expected error code %q, got %q (message=%q)", opName, code, r.Error.Code, r.Error.Message)
	}
}

// ExtJSONOID extracts the lowercase hex string of an ObjectId-encoded Doc.
// Returns empty string if the Doc isn't an `{"$oid": ...}` envelope.
func ExtJSONOID(d Doc) string {
	var w struct {
		OID string `json:"$oid"`
	}
	if err := json.Unmarshal(d, &w); err != nil {
		return ""
	}
	return strings.ToLower(w.OID)
}
