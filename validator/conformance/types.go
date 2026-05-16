package conformance

import "encoding/json"

// Doc is an Extended-JSON-bearing JSON object. We use json.RawMessage for
// fields whose values are arbitrary BSON (filters, updates, documents,
// inserted_id, etc.) so canonical Extended JSON survives serialization.
type Doc = json.RawMessage

// ---- /v1/ops request ----

type OpsRequest struct {
	Database string `json:"database"`
	Ops      []Op   `json:"ops"`
}

// Op is one CRUD operation. Only the fields relevant to Name are populated;
// JSON marshaling omits empties so the wire representation is clean.
type Op struct {
	Name       string `json:"name"`
	Collection string `json:"collection,omitempty"`

	// insertOne
	Document Doc `json:"document,omitempty"`
	// insertMany
	Documents []Doc `json:"documents,omitempty"`
	Ordered   *bool `json:"ordered,omitempty"`
	// find
	Filter     Doc   `json:"filter,omitempty"`
	Projection Doc   `json:"projection,omitempty"`
	Sort       Doc   `json:"sort,omitempty"`
	Skip       *int  `json:"skip,omitempty"`
	Limit      *int  `json:"limit,omitempty"`
	// update / replace
	Update       Doc   `json:"update,omitempty"`
	Replacement  Doc   `json:"replacement,omitempty"`
	Upsert       *bool `json:"upsert,omitempty"`
	ArrayFilters []Doc `json:"array_filters,omitempty"`
	// aggregate
	Pipeline []Doc `json:"pipeline,omitempty"`
	// bulkWrite
	Operations []Op `json:"operations,omitempty"`
}

// ---- Op constructors. Keep scenarios terse. ----

func InsertOne(coll string, document Doc) Op {
	return Op{Name: "insertOne", Collection: coll, Document: document}
}

func InsertMany(coll string, documents []Doc) Op {
	return Op{Name: "insertMany", Collection: coll, Documents: documents}
}

func Find(coll string, filter Doc) Op {
	return Op{Name: "find", Collection: coll, Filter: filter}
}

func UpdateOne(coll string, filter, update Doc) Op {
	return Op{Name: "updateOne", Collection: coll, Filter: filter, Update: update}
}

func UpdateMany(coll string, filter, update Doc) Op {
	return Op{Name: "updateMany", Collection: coll, Filter: filter, Update: update}
}

func ReplaceOne(coll string, filter, replacement Doc) Op {
	return Op{Name: "replaceOne", Collection: coll, Filter: filter, Replacement: replacement}
}

func DeleteOne(coll string, filter Doc) Op {
	return Op{Name: "deleteOne", Collection: coll, Filter: filter}
}

func DeleteMany(coll string, filter Doc) Op {
	return Op{Name: "deleteMany", Collection: coll, Filter: filter}
}

func CountDocuments(coll string, filter Doc) Op {
	return Op{Name: "countDocuments", Collection: coll, Filter: filter}
}

func Aggregate(coll string, pipeline []Doc) Op {
	return Op{Name: "aggregate", Collection: coll, Pipeline: pipeline}
}

func BulkWrite(coll string, operations []Op) Op {
	return Op{Name: "bulkWrite", Collection: coll, Operations: operations}
}

// Bool / Int return pointers, for the optional-with-default fields.
func Bool(b bool) *bool { return &b }
func Int(i int) *int    { return &i }

// WithSort, WithLimit, WithSkip, WithProjection return copies of op with the
// optional fields set.
func (o Op) WithSort(sort Doc) Op             { o.Sort = sort; return o }
func (o Op) WithLimit(limit int) Op           { o.Limit = Int(limit); return o }
func (o Op) WithSkip(skip int) Op             { o.Skip = Int(skip); return o }
func (o Op) WithProjection(projection Doc) Op { o.Projection = projection; return o }
func (o Op) WithUpsert(upsert bool) Op        { o.Upsert = Bool(upsert); return o }
func (o Op) WithOrdered(ordered bool) Op      { o.Ordered = Bool(ordered); return o }

// ---- /v1/ops response ----

type OpsResponse struct {
	Results []OpResult `json:"results"`
}

// OpResult covers both success and failure shapes via OK; on success Data is
// populated, on failure Error is populated. The Go type is permissive (no
// discriminated decoding); scenarios assert on OK / OpName + Data fields.
type OpResult struct {
	Op    string          `json:"op"`
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *OpError        `json:"error,omitempty"`
}

type OpError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	ServerCode *int   `json:"server_code,omitempty"`
}

// Per-op data shapes. Decoders are scenario-local: scenarios that care about
// a specific shape call DecodeData[Shape] on the result.

type InsertOneData struct {
	InsertedID Doc `json:"inserted_id"`
}

type InsertManyData struct {
	InsertedIDs   []Doc `json:"inserted_ids"`
	InsertedCount int   `json:"inserted_count"`
}

type FindData struct {
	Documents []Doc `json:"documents"`
	Count     int   `json:"count"`
}

type UpdateData struct {
	MatchedCount  int `json:"matched_count"`
	ModifiedCount int `json:"modified_count"`
	UpsertedID    Doc `json:"upserted_id,omitempty"`
}

type DeleteData struct {
	DeletedCount int `json:"deleted_count"`
}

type CountData struct {
	Count int `json:"count"`
}

type BulkWriteData struct {
	InsertedCount int            `json:"inserted_count"`
	MatchedCount  int            `json:"matched_count"`
	ModifiedCount int            `json:"modified_count"`
	DeletedCount  int            `json:"deleted_count"`
	UpsertedCount int            `json:"upserted_count"`
	InsertedIDs   map[string]Doc `json:"inserted_ids"`
	UpsertedIDs   map[string]Doc `json:"upserted_ids"`
}

// DecodeData unmarshals r.Data into v.
func DecodeData[T any](r OpResult) (T, error) {
	var v T
	if err := json.Unmarshal(r.Data, &v); err != nil {
		return v, err
	}
	return v, nil
}

// ---- /v1/admin/reset ----

type ResetRequest struct {
	Databases []string `json:"databases"`
}

type ResetResponse struct {
	Dropped []string `json:"dropped"`
}

// ---- /v1/info ----

type InfoResponse struct {
	Driver          string `json:"driver"`
	DriverVersion   string `json:"driver_version"`
	Language        string `json:"language"`
	LanguageVersion string `json:"language_version"`
	SpecVersion     string `json:"spec_version"`
}

// ---- /v1/health ----

type HealthResponse struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// ---- 400 body ----

type RequestError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
