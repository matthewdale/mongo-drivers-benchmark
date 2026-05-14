package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/version"
)

var (
	client    *mongo.Client
	connected bool
)

func main() {
	uri := os.Getenv("MONGODB_URI")
	if uri == "" {
		uri = "mongodb://localhost:27017"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	var err error
	client, err = mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		log.Fatalf("failed to connect: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Ping(ctx, nil); err != nil {
		log.Fatalf("failed to ping MongoDB: %v", err)
	}
	connected = true
	log.Printf("Connected to MongoDB at %s", uri)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("POST /find", handleFind)
	mux.HandleFunc("POST /findOne", handleFindOne)
	mux.HandleFunc("POST /insertOne", handleInsertOne)
	mux.HandleFunc("POST /insertMany", handleInsertMany)
	mux.HandleFunc("POST /updateOne", handleUpdateOne)
	mux.HandleFunc("POST /updateMany", handleUpdateMany)
	mux.HandleFunc("POST /deleteOne", handleDeleteOne)
	mux.HandleFunc("POST /deleteMany", handleDeleteMany)
	mux.HandleFunc("POST /bulkWrite", handleBulkWrite)
	mux.HandleFunc("POST /clientBulkWrite", handleClientBulkWrite)

	log.Printf("Listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("error encoding response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}

// parseRequest reads the body and json-decodes it into dst.
func parseRequest(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}

// getCollection resolves the database and collection from a request map.
func getCollection(req map[string]json.RawMessage) (*mongo.Collection, error) {
	db := "perftest"
	coll := ""
	if v, ok := req["database"]; ok {
		if err := json.Unmarshal(v, &db); err != nil {
			return nil, fmt.Errorf("invalid database field: %w", err)
		}
	}
	if v, ok := req["collection"]; ok {
		if err := json.Unmarshal(v, &coll); err != nil {
			return nil, fmt.Errorf("invalid collection field: %w", err)
		}
	}
	if coll == "" {
		return nil, fmt.Errorf("collection is required")
	}
	return client.Database(db).Collection(coll), nil
}

// unmarshalDoc decodes plain JSON bytes into a map[string]any.
func unmarshalDoc(data json.RawMessage) (map[string]any, error) {
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// serializeID converts an inserted/upserted ID value to a JSON-serializable
// form. ObjectIDs are represented as their hex string; other types pass through
// as-is.
func serializeID(val any) any {
	if oid, ok := val.(bson.ObjectID); ok {
		return oid.Hex()
	}
	return val
}

// ── handlers ──────────────────────────────────────────────────────────────────

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if !connected {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "driver not yet connected")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":          "ok",
		"driver":          "mongo-go-driver",
		"driverVersion":   version.Driver,
		"language":        "go",
		"languageVersion": runtime.Version(),
	})
}

func handleFind(w http.ResponseWriter, r *http.Request) {
	var req map[string]json.RawMessage
	if err := parseRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	coll, err := getCollection(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	filterRaw, ok := req["filter"]
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "filter is required")
		return
	}
	filter, err := unmarshalDoc(filterRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid filter: "+err.Error())
		return
	}

	opts := options.Find()
	if optsRaw, ok := req["options"]; ok {
		var findOpts struct {
			Limit      *int64         `json:"limit"`
			Skip       *int64         `json:"skip"`
			BatchSize  *int32         `json:"batchSize"`
			Sort       map[string]any `json:"sort"`
			Projection map[string]any `json:"projection"`
		}
		if err := json.Unmarshal(optsRaw, &findOpts); err == nil {
			if findOpts.Limit != nil && *findOpts.Limit > 0 {
				opts.SetLimit(*findOpts.Limit)
			}
			if findOpts.Skip != nil && *findOpts.Skip > 0 {
				opts.SetSkip(*findOpts.Skip)
			}
			if findOpts.BatchSize != nil {
				opts.SetBatchSize(*findOpts.BatchSize)
			}
			if len(findOpts.Sort) > 0 {
				opts.SetSort(findOpts.Sort)
			}
			if len(findOpts.Projection) > 0 {
				opts.SetProjection(findOpts.Projection)
			}
		}
	}

	cursor, err := coll.Find(r.Context(), filter, opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "driver_error", err.Error())
		return
	}
	defer cursor.Close(r.Context())

	var results []map[string]any
	if err := cursor.All(r.Context(), &results); err != nil {
		writeError(w, http.StatusInternalServerError, "driver_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"documents": results,
		"count":     len(results),
	})
}

func handleFindOne(w http.ResponseWriter, r *http.Request) {
	var req map[string]json.RawMessage
	if err := parseRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	coll, err := getCollection(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	filterRaw, ok := req["filter"]
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "filter is required")
		return
	}
	filter, err := unmarshalDoc(filterRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid filter: "+err.Error())
		return
	}

	opts := options.FindOne()
	if optsRaw, ok := req["options"]; ok {
		var findOneOpts struct {
			Sort       map[string]any `json:"sort"`
			Projection map[string]any `json:"projection"`
		}
		if err := json.Unmarshal(optsRaw, &findOneOpts); err == nil {
			if len(findOneOpts.Sort) > 0 {
				opts.SetSort(findOneOpts.Sort)
			}
			if len(findOneOpts.Projection) > 0 {
				opts.SetProjection(findOneOpts.Projection)
			}
		}
	}

	var result map[string]any
	err = coll.FindOne(r.Context(), filter, opts).Decode(&result)
	if err == mongo.ErrNoDocuments {
		writeJSON(w, http.StatusOK, map[string]any{"document": nil})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "driver_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"document": result})
}

func handleInsertOne(w http.ResponseWriter, r *http.Request) {
	var req map[string]json.RawMessage
	if err := parseRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	coll, err := getCollection(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	docRaw, ok := req["document"]
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "document is required")
		return
	}
	doc, err := unmarshalDoc(docRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid document: "+err.Error())
		return
	}

	opts := options.InsertOne()
	if optsRaw, ok := req["options"]; ok {
		var insOpts struct {
			BypassDocumentValidation *bool `json:"bypassDocumentValidation"`
		}
		if err := json.Unmarshal(optsRaw, &insOpts); err == nil {
			if insOpts.BypassDocumentValidation != nil {
				opts.SetBypassDocumentValidation(*insOpts.BypassDocumentValidation)
			}
		}
	}

	result, err := coll.InsertOne(r.Context(), doc, opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "driver_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"insertedId": serializeID(result.InsertedID),
	})
}

func handleInsertMany(w http.ResponseWriter, r *http.Request) {
	var req map[string]json.RawMessage
	if err := parseRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	coll, err := getCollection(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	docsRaw, ok := req["documents"]
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "documents is required")
		return
	}

	var docs []any
	if err := json.Unmarshal(docsRaw, &docs); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid documents: "+err.Error())
		return
	}

	opts := options.InsertMany()
	if optsRaw, ok := req["options"]; ok {
		var insOpts struct {
			Ordered                  *bool `json:"ordered"`
			BypassDocumentValidation *bool `json:"bypassDocumentValidation"`
		}
		if err := json.Unmarshal(optsRaw, &insOpts); err == nil {
			if insOpts.Ordered != nil {
				opts.SetOrdered(*insOpts.Ordered)
			}
			if insOpts.BypassDocumentValidation != nil {
				opts.SetBypassDocumentValidation(*insOpts.BypassDocumentValidation)
			}
		}
	}

	result, err := coll.InsertMany(r.Context(), docs, opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "driver_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"insertedCount": len(result.InsertedIDs),
	})
}

func handleUpdateOne(w http.ResponseWriter, r *http.Request) {
	handleUpdate(w, r, false)
}

func handleUpdateMany(w http.ResponseWriter, r *http.Request) {
	handleUpdate(w, r, true)
}

func handleUpdate(w http.ResponseWriter, r *http.Request, many bool) {
	var req map[string]json.RawMessage
	if err := parseRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	coll, err := getCollection(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	filterRaw, ok := req["filter"]
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "filter is required")
		return
	}
	filter, err := unmarshalDoc(filterRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid filter: "+err.Error())
		return
	}

	updateRaw, ok := req["update"]
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "update is required")
		return
	}
	update, err := unmarshalDoc(updateRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid update: "+err.Error())
		return
	}

	var upsert *bool
	if optsRaw, ok := req["options"]; ok {
		var updOpts struct {
			Upsert *bool `json:"upsert"`
		}
		if err := json.Unmarshal(optsRaw, &updOpts); err == nil {
			upsert = updOpts.Upsert
		}
	}

	var (
		matchedCount  int64
		modifiedCount int64
		upsertedID    any
	)

	if many {
		opts := options.UpdateMany()
		if upsert != nil {
			opts.SetUpsert(*upsert)
		}
		result, err := coll.UpdateMany(r.Context(), filter, update, opts)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "driver_error", err.Error())
			return
		}
		matchedCount = result.MatchedCount
		modifiedCount = result.ModifiedCount
		upsertedID = result.UpsertedID
	} else {
		opts := options.UpdateOne()
		if upsert != nil {
			opts.SetUpsert(*upsert)
		}
		result, err := coll.UpdateOne(r.Context(), filter, update, opts)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "driver_error", err.Error())
			return
		}
		matchedCount = result.MatchedCount
		modifiedCount = result.ModifiedCount
		upsertedID = result.UpsertedID
	}

	resp := map[string]any{
		"matchedCount":  matchedCount,
		"modifiedCount": modifiedCount,
		"upsertedId":    nil,
	}
	if upsertedID != nil {
		resp["upsertedId"] = serializeID(upsertedID)
	}
	writeJSON(w, http.StatusOK, resp)
}

func handleDeleteOne(w http.ResponseWriter, r *http.Request) {
	handleDelete(w, r, false)
}

func handleDeleteMany(w http.ResponseWriter, r *http.Request) {
	handleDelete(w, r, true)
}

func handleDelete(w http.ResponseWriter, r *http.Request, many bool) {
	var req map[string]json.RawMessage
	if err := parseRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	coll, err := getCollection(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	filterRaw, ok := req["filter"]
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "filter is required")
		return
	}
	filter, err := unmarshalDoc(filterRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid filter: "+err.Error())
		return
	}

	var deletedCount int64
	if many {
		result, err := coll.DeleteMany(r.Context(), filter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "driver_error", err.Error())
			return
		}
		deletedCount = result.DeletedCount
	} else {
		result, err := coll.DeleteOne(r.Context(), filter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "driver_error", err.Error())
			return
		}
		deletedCount = result.DeletedCount
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"deletedCount": deletedCount,
	})
}

func handleBulkWrite(w http.ResponseWriter, r *http.Request) {
	var req map[string]json.RawMessage
	if err := parseRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	coll, err := getCollection(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	opsRaw, ok := req["operations"]
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "operations is required")
		return
	}

	var rawOps []map[string]json.RawMessage
	if err := json.Unmarshal(opsRaw, &rawOps); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid operations: "+err.Error())
		return
	}

	models := make([]mongo.WriteModel, 0, len(rawOps))
	for i, op := range rawOps {
		model, err := parseBulkWriteModel(op)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("invalid operation[%d]: %v", i, err))
			return
		}
		models = append(models, model)
	}

	opts := options.BulkWrite()
	if optsRaw, ok := req["options"]; ok {
		var bwOpts struct {
			Ordered                  *bool `json:"ordered"`
			BypassDocumentValidation *bool `json:"bypassDocumentValidation"`
		}
		if err := json.Unmarshal(optsRaw, &bwOpts); err == nil {
			if bwOpts.Ordered != nil {
				opts.SetOrdered(*bwOpts.Ordered)
			}
			if bwOpts.BypassDocumentValidation != nil {
				opts.SetBypassDocumentValidation(*bwOpts.BypassDocumentValidation)
			}
		}
	}

	result, err := coll.BulkWrite(r.Context(), models, opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "driver_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"insertedCount": result.InsertedCount,
		"matchedCount":  result.MatchedCount,
		"modifiedCount": result.ModifiedCount,
		"deletedCount":  result.DeletedCount,
		"upsertedCount": result.UpsertedCount,
	})
}

func parseBulkWriteModel(op map[string]json.RawMessage) (mongo.WriteModel, error) {
	if raw, ok := op["insertOne"]; ok {
		var args struct {
			Document json.RawMessage `json:"document"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, err
		}
		doc, err := unmarshalDoc(args.Document)
		if err != nil {
			return nil, err
		}
		return mongo.NewInsertOneModel().SetDocument(doc), nil
	}

	if raw, ok := op["updateOne"]; ok {
		var args struct {
			Filter json.RawMessage `json:"filter"`
			Update json.RawMessage `json:"update"`
			Upsert *bool           `json:"upsert"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, err
		}
		filter, err := unmarshalDoc(args.Filter)
		if err != nil {
			return nil, err
		}
		update, err := unmarshalDoc(args.Update)
		if err != nil {
			return nil, err
		}
		m := mongo.NewUpdateOneModel().SetFilter(filter).SetUpdate(update)
		if args.Upsert != nil {
			m.SetUpsert(*args.Upsert)
		}
		return m, nil
	}

	if raw, ok := op["updateMany"]; ok {
		var args struct {
			Filter json.RawMessage `json:"filter"`
			Update json.RawMessage `json:"update"`
			Upsert *bool           `json:"upsert"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, err
		}
		filter, err := unmarshalDoc(args.Filter)
		if err != nil {
			return nil, err
		}
		update, err := unmarshalDoc(args.Update)
		if err != nil {
			return nil, err
		}
		m := mongo.NewUpdateManyModel().SetFilter(filter).SetUpdate(update)
		if args.Upsert != nil {
			m.SetUpsert(*args.Upsert)
		}
		return m, nil
	}

	if raw, ok := op["replaceOne"]; ok {
		var args struct {
			Filter      json.RawMessage `json:"filter"`
			Replacement json.RawMessage `json:"replacement"`
			Upsert      *bool           `json:"upsert"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, err
		}
		filter, err := unmarshalDoc(args.Filter)
		if err != nil {
			return nil, err
		}
		replacement, err := unmarshalDoc(args.Replacement)
		if err != nil {
			return nil, err
		}
		m := mongo.NewReplaceOneModel().SetFilter(filter).SetReplacement(replacement)
		if args.Upsert != nil {
			m.SetUpsert(*args.Upsert)
		}
		return m, nil
	}

	if raw, ok := op["deleteOne"]; ok {
		var args struct {
			Filter json.RawMessage `json:"filter"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, err
		}
		filter, err := unmarshalDoc(args.Filter)
		if err != nil {
			return nil, err
		}
		return mongo.NewDeleteOneModel().SetFilter(filter), nil
	}

	if raw, ok := op["deleteMany"]; ok {
		var args struct {
			Filter json.RawMessage `json:"filter"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, err
		}
		filter, err := unmarshalDoc(args.Filter)
		if err != nil {
			return nil, err
		}
		return mongo.NewDeleteManyModel().SetFilter(filter), nil
	}

	return nil, fmt.Errorf("unknown operation kind")
}

func handleClientBulkWrite(w http.ResponseWriter, r *http.Request) {
	var req map[string]json.RawMessage
	if err := parseRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	// Spec: top-level database/collection MUST NOT be present on this endpoint.
	if _, ok := req["database"]; ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "database must not be set on /clientBulkWrite")
		return
	}
	if _, ok := req["collection"]; ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "collection must not be set on /clientBulkWrite")
		return
	}

	modelsRaw, ok := req["models"]
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "models is required")
		return
	}

	var rawModels []map[string]json.RawMessage
	if err := json.Unmarshal(modelsRaw, &rawModels); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid models: "+err.Error())
		return
	}

	models := make([]mongo.ClientBulkWrite, 0, len(rawModels))
	for i, rm := range rawModels {
		m, err := parseClientWriteModel(rm)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("invalid model[%d]: %v", i, err))
			return
		}
		models = append(models, m)
	}

	opts := options.ClientBulkWrite()
	if optsRaw, ok := req["options"]; ok {
		var cbwOpts struct {
			Ordered                  *bool `json:"ordered"`
			BypassDocumentValidation *bool `json:"bypassDocumentValidation"`
			VerboseResults           *bool `json:"verboseResults"`
		}
		if err := json.Unmarshal(optsRaw, &cbwOpts); err == nil {
			if cbwOpts.Ordered != nil {
				opts.SetOrdered(*cbwOpts.Ordered)
			}
			if cbwOpts.BypassDocumentValidation != nil {
				opts.SetBypassDocumentValidation(*cbwOpts.BypassDocumentValidation)
			}
			if cbwOpts.VerboseResults != nil {
				opts.SetVerboseResults(*cbwOpts.VerboseResults)
			}
		}
	}

	result, err := client.BulkWrite(r.Context(), models, opts)
	if err != nil {
		// Return 501 when the server does not support clientBulkWrite (pre-8.0).
		var cmdErr mongo.CommandError
		if (errors.As(err, &cmdErr) && cmdErr.HasErrorCode(40324)) || strings.Contains(err.Error(), "Unrecognized field") {
			writeError(w, http.StatusNotImplemented, "unsupported", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "driver_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"insertedCount": result.InsertedCount,
		"matchedCount":  result.MatchedCount,
		"modifiedCount": result.ModifiedCount,
		"deletedCount":  result.DeletedCount,
		"upsertedCount": result.UpsertedCount,
	})
}

func parseClientWriteModel(rm map[string]json.RawMessage) (mongo.ClientBulkWrite, error) {
	zero := mongo.ClientBulkWrite{}

	nsRaw, ok := rm["namespace"]
	if !ok {
		return zero, fmt.Errorf("namespace is required")
	}
	var nsStr string
	if err := json.Unmarshal(nsRaw, &nsStr); err != nil {
		return zero, fmt.Errorf("invalid namespace: %w", err)
	}
	dot := strings.IndexByte(nsStr, '.')
	if dot < 0 {
		return zero, fmt.Errorf("namespace must be in 'db.coll' format, got %q", nsStr)
	}
	db, coll := nsStr[:dot], nsStr[dot+1:]

	if raw, ok := rm["insertOne"]; ok {
		var args struct {
			Document json.RawMessage `json:"document"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return zero, err
		}
		doc, err := unmarshalDoc(args.Document)
		if err != nil {
			return zero, err
		}
		return mongo.ClientBulkWrite{Database: db, Collection: coll, Model: mongo.NewClientInsertOneModel().SetDocument(doc)}, nil
	}

	if raw, ok := rm["updateOne"]; ok {
		var args struct {
			Filter json.RawMessage `json:"filter"`
			Update json.RawMessage `json:"update"`
			Upsert *bool           `json:"upsert"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return zero, err
		}
		filter, err := unmarshalDoc(args.Filter)
		if err != nil {
			return zero, err
		}
		update, err := unmarshalDoc(args.Update)
		if err != nil {
			return zero, err
		}
		m := mongo.NewClientUpdateOneModel().SetFilter(filter).SetUpdate(update)
		if args.Upsert != nil {
			m.SetUpsert(*args.Upsert)
		}
		return mongo.ClientBulkWrite{Database: db, Collection: coll, Model: m}, nil
	}

	if raw, ok := rm["updateMany"]; ok {
		var args struct {
			Filter json.RawMessage `json:"filter"`
			Update json.RawMessage `json:"update"`
			Upsert *bool           `json:"upsert"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return zero, err
		}
		filter, err := unmarshalDoc(args.Filter)
		if err != nil {
			return zero, err
		}
		update, err := unmarshalDoc(args.Update)
		if err != nil {
			return zero, err
		}
		m := mongo.NewClientUpdateManyModel().SetFilter(filter).SetUpdate(update)
		if args.Upsert != nil {
			m.SetUpsert(*args.Upsert)
		}
		return mongo.ClientBulkWrite{Database: db, Collection: coll, Model: m}, nil
	}

	if raw, ok := rm["replaceOne"]; ok {
		var args struct {
			Filter      json.RawMessage `json:"filter"`
			Replacement json.RawMessage `json:"replacement"`
			Upsert      *bool           `json:"upsert"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return zero, err
		}
		filter, err := unmarshalDoc(args.Filter)
		if err != nil {
			return zero, err
		}
		replacement, err := unmarshalDoc(args.Replacement)
		if err != nil {
			return zero, err
		}
		m := mongo.NewClientReplaceOneModel().SetFilter(filter).SetReplacement(replacement)
		if args.Upsert != nil {
			m.SetUpsert(*args.Upsert)
		}
		return mongo.ClientBulkWrite{Database: db, Collection: coll, Model: m}, nil
	}

	if raw, ok := rm["deleteOne"]; ok {
		var args struct {
			Filter json.RawMessage `json:"filter"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return zero, err
		}
		filter, err := unmarshalDoc(args.Filter)
		if err != nil {
			return zero, err
		}
		return mongo.ClientBulkWrite{Database: db, Collection: coll, Model: mongo.NewClientDeleteOneModel().SetFilter(filter)}, nil
	}

	if raw, ok := rm["deleteMany"]; ok {
		var args struct {
			Filter json.RawMessage `json:"filter"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return zero, err
		}
		filter, err := unmarshalDoc(args.Filter)
		if err != nil {
			return zero, err
		}
		return mongo.ClientBulkWrite{Database: db, Collection: coll, Model: mongo.NewClientDeleteManyModel().SetFilter(filter)}, nil
	}

	return zero, fmt.Errorf("unknown operation kind in client bulk write model")
}
