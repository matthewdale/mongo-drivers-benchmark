// Package ejson handles the boundary between HTTP JSON (with embedded
// Extended JSON v2 canonical objects) and the driver's BSON types.
//
// Decoding strategy: for any field whose value is a "BSON document" (filter,
// update, document, replacement, projection, sort, pipeline element, etc.) we
// keep the raw JSON bytes and feed them to bson.UnmarshalExtJSON. This gives
// us a bson.D / bson.A that the driver consumes directly.
//
// Encoding strategy: for response values that are driver-typed BSON (inserted
// _ids, found documents, etc.) we call bson.MarshalExtJSON(canonical=true,
// escapeHTML=false) and embed the resulting bytes as a json.RawMessage. The
// top-level envelope is built with encoding/json so the wire format stays
// valid RFC 8259.
package ejson

import (
	"encoding/json"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// MarshalCanonical wraps bson.MarshalExtJSON. It supports both whole BSON
// documents and scalar BSON values (ObjectID, int32, int64, Decimal128,
// DateTime, etc.) — the latter are marshaled by wrapping in a single-field
// document and slicing the wrapper out.
//
// Output uses RELAXED Extended JSON v2 (canonical=false). The on-wire shape
// for `_id`-like BSON values (ObjectId, Decimal128, Binary, DateTime, ...)
// is still `{"$oid": ...}` / `{"$numberDecimal": ...}` / etc., which is what
// the spec and validator require for `inserted_id`-style fields; plain ints
// and doubles are emitted as JSON numbers, which is what the validator's
// `struct{N int}` decoding expects for round-tripped `find` documents.
func MarshalCanonical(v any) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage("null"), nil
	}
	// Try to marshal as a document directly. If that fails (because v is a
	// scalar BSON type, not a doc), wrap it.
	if b, err := bson.MarshalExtJSON(v, false, false); err == nil {
		return json.RawMessage(b), nil
	}
	wrapped := bson.D{{Key: "v", Value: v}}
	b, err := bson.MarshalExtJSON(wrapped, false, false)
	if err != nil {
		return nil, fmt.Errorf("MarshalExtJSON: %w", err)
	}
	// b is `{"v": <value>}` — slice out the value bytes by parsing JSON.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err != nil {
		return nil, fmt.Errorf("MarshalExtJSON unwrap: %w", err)
	}
	val, ok := obj["v"]
	if !ok {
		return nil, fmt.Errorf("MarshalExtJSON unwrap: missing v")
	}
	return val, nil
}

// MustMarshal panics on encoding failure. Reserved for values the service
// itself constructs (so a failure is a service bug).
func MustMarshal(v any) json.RawMessage {
	r, err := MarshalCanonical(v)
	if err != nil {
		panic(err)
	}
	return r
}

// DecodeDoc unmarshals a JSON value (possibly canonical Extended JSON) into a
// bson.D. Used for filters, updates, documents, projections, sorts, etc. The
// input bytes MUST already be valid JSON; an empty/nil input is treated as an
// empty document.
func DecodeDoc(raw json.RawMessage) (bson.D, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return bson.D{}, nil
	}
	var d bson.D
	if err := bson.UnmarshalExtJSON(raw, false, &d); err != nil {
		return nil, fmt.Errorf("decode bson doc: %w", err)
	}
	return d, nil
}

// DecodeDocs decodes a slice of JSON documents.
func DecodeDocs(raws []json.RawMessage) ([]bson.D, error) {
	out := make([]bson.D, 0, len(raws))
	for i, r := range raws {
		d, err := DecodeDoc(r)
		if err != nil {
			return nil, fmt.Errorf("doc[%d]: %w", i, err)
		}
		out = append(out, d)
	}
	return out, nil
}

// DocHasID reports whether a bson.D contains a top-level "_id" element.
func DocHasID(d bson.D) bool {
	for _, e := range d {
		if e.Key == "_id" {
			return true
		}
	}
	return false
}

// EnsureID returns d with an "_id" element guaranteed to exist. If d already
// has one, it is returned unchanged along with the existing id. Otherwise a
// new bson.ObjectID is prepended and returned as the id. The returned id is
// the actual BSON value (bson.ObjectID or whatever the user supplied).
func EnsureID(d bson.D) (bson.D, any) {
	for _, e := range d {
		if e.Key == "_id" {
			return d, e.Value
		}
	}
	id := bson.NewObjectID()
	out := make(bson.D, 0, len(d)+1)
	out = append(out, bson.E{Key: "_id", Value: id})
	out = append(out, d...)
	return out, id
}

// RawDocToBytes serializes any value that the driver returned as a "document"
// (bson.Raw, bson.D, map, etc.) to canonical EJSON bytes.
func RawDocToBytes(v any) (json.RawMessage, error) {
	return MarshalCanonical(v)
}
