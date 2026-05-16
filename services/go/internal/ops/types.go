// Package ops decodes /v1/ops requests, dispatches each op to the driver,
// and shapes the per-op success/failure responses.
package ops

import (
	"encoding/json"
)

// Request is the top-level /v1/ops request body.
type Request struct {
	Database *string           `json:"database"`
	Ops      []json.RawMessage `json:"ops"`
}

// Op holds the decoded per-op shape. Only fields relevant to the op's name
// are populated.
//
// Each "BSON-typed" field is kept as raw JSON so we can hand it to
// bson.UnmarshalExtJSON without round-tripping through interface{}.
type Op struct {
	Name       string          `json:"name"`
	Collection string          `json:"collection,omitempty"`

	// insertOne / bulk insertOne
	Document json.RawMessage `json:"document,omitempty"`
	// insertMany
	Documents []json.RawMessage `json:"documents,omitempty"`
	Ordered   *bool             `json:"ordered,omitempty"`
	// find
	Filter     json.RawMessage `json:"filter,omitempty"`
	Projection json.RawMessage `json:"projection,omitempty"`
	Sort       json.RawMessage `json:"sort,omitempty"`
	Skip       *int            `json:"skip,omitempty"`
	Limit      *int            `json:"limit,omitempty"`
	// update / replace
	Update       json.RawMessage   `json:"update,omitempty"`
	Replacement  json.RawMessage   `json:"replacement,omitempty"`
	Upsert       *bool             `json:"upsert,omitempty"`
	ArrayFilters []json.RawMessage `json:"array_filters,omitempty"`
	// aggregate
	Pipeline []json.RawMessage `json:"pipeline,omitempty"`
	// bulkWrite
	Operations []Op `json:"operations,omitempty"`
}

// Response is the /v1/ops response body. Each entry is one OpResult; entries
// MUST be in the same order as the request's ops.
type Response struct {
	Results []Result `json:"results"`
}

// Result is one op's outcome. On success Data is populated; on failure Error
// is populated. We rely on omitempty + the marshaler emitting either Data or
// Error but not both.
type Result struct {
	Op    string          `json:"op"`
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *OpError        `json:"error,omitempty"`
}

// OpError is the normalized error envelope. ServerCode is a pointer so
// `omitempty` drops it when the driver didn't expose a code.
type OpError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	ServerCode *int   `json:"server_code,omitempty"`
}
