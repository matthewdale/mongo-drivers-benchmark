package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/mongodb-labs/mongo-drivers-benchmark/services/go/internal/ejson"
	"github.com/mongodb-labs/mongo-drivers-benchmark/services/go/internal/errs"
)

// Dispatcher executes one OpsRequest worth of ops sequentially against a
// single database. Per spec §6.2 it MUST NOT short-circuit on per-op failure.
type Dispatcher struct {
	Client *mongo.Client
}

// Run executes the ops in order. Returns one Result per op, in the same order.
// An error is returned only for service-internal failures (e.g. ResponseWriter
// problems) — never for driver-level failures, which become per-op Results.
func (d *Dispatcher) Run(ctx context.Context, dbName string, ops []Op) []Result {
	db := d.Client.Database(dbName)
	out := make([]Result, len(ops))
	for i, op := range ops {
		out[i] = d.runOne(ctx, db, op)
	}
	return out
}

func (d *Dispatcher) runOne(ctx context.Context, db *mongo.Database, op Op) Result {
	res := Result{Op: op.Name}
	data, err := d.execute(ctx, db, op)
	if err != nil {
		cl := errs.Classify(err)
		res.OK = false
		res.Error = &OpError{Code: cl.Code, Message: cl.Message, ServerCode: cl.ServerCode}
		return res
	}
	res.OK = true
	res.Data = data
	return res
}

// execute dispatches op to the right driver call and returns the per-op
// success "data" as canonical-JSON bytes ready to embed in the response.
func (d *Dispatcher) execute(ctx context.Context, db *mongo.Database, op Op) (json.RawMessage, error) {
	switch op.Name {
	case "insertOne":
		return d.insertOne(ctx, db, op)
	case "insertMany":
		return d.insertMany(ctx, db, op)
	case "find":
		return d.find(ctx, db, op)
	case "updateOne":
		return d.updateOne(ctx, db, op)
	case "updateMany":
		return d.updateMany(ctx, db, op)
	case "replaceOne":
		return d.replaceOne(ctx, db, op)
	case "deleteOne":
		return d.deleteOne(ctx, db, op)
	case "deleteMany":
		return d.deleteMany(ctx, db, op)
	case "countDocuments":
		return d.countDocuments(ctx, db, op)
	case "aggregate":
		return d.aggregate(ctx, db, op)
	case "bulkWrite":
		return d.bulkWrite(ctx, db, op)
	default:
		// Should never happen: validateOp rejects unknown ops upstream.
		return nil, fmt.Errorf("unknown op %q", op.Name)
	}
}

// ---- per-op implementations ----

func (d *Dispatcher) insertOne(ctx context.Context, db *mongo.Database, op Op) (json.RawMessage, error) {
	doc, err := ejson.DecodeDoc(op.Document)
	if err != nil {
		return nil, err
	}
	res, err := db.Collection(op.Collection).InsertOne(ctx, doc)
	if err != nil {
		return nil, err
	}
	insertedID, err := ejson.MarshalCanonical(res.InsertedID)
	if err != nil {
		return nil, err
	}
	return buildJSON(kv{"inserted_id", insertedID})
}

func (d *Dispatcher) insertMany(ctx context.Context, db *mongo.Database, op Op) (json.RawMessage, error) {
	docs, err := ejson.DecodeDocs(op.Documents)
	if err != nil {
		return nil, err
	}
	// Convert []bson.D to []any to satisfy the driver signature.
	asAny := make([]any, len(docs))
	for i, d := range docs {
		asAny[i] = d
	}
	opts := options.InsertMany()
	if op.Ordered != nil {
		opts = opts.SetOrdered(*op.Ordered)
	}
	res, err := db.Collection(op.Collection).InsertMany(ctx, asAny, opts)
	if err != nil {
		return nil, err
	}
	ids := make([]json.RawMessage, len(res.InsertedIDs))
	for i, id := range res.InsertedIDs {
		raw, mErr := ejson.MarshalCanonical(id)
		if mErr != nil {
			return nil, mErr
		}
		ids[i] = raw
	}
	idsJSON, _ := json.Marshal(ids)
	return buildJSON(
		kv{"inserted_ids", json.RawMessage(idsJSON)},
		kv{"inserted_count", json.RawMessage(strconv.Itoa(len(ids)))},
	)
}

func (d *Dispatcher) find(ctx context.Context, db *mongo.Database, op Op) (json.RawMessage, error) {
	filter, err := ejson.DecodeDoc(op.Filter)
	if err != nil {
		return nil, err
	}
	opts := options.Find()
	if len(op.Projection) > 0 {
		proj, perr := ejson.DecodeDoc(op.Projection)
		if perr != nil {
			return nil, perr
		}
		opts = opts.SetProjection(proj)
	}
	if len(op.Sort) > 0 {
		sort, serr := ejson.DecodeDoc(op.Sort)
		if serr != nil {
			return nil, serr
		}
		opts = opts.SetSort(sort)
	}
	if op.Skip != nil {
		opts = opts.SetSkip(int64(*op.Skip))
	}
	if op.Limit != nil {
		opts = opts.SetLimit(int64(*op.Limit))
	}

	cur, err := db.Collection(op.Collection).Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	return cursorToFindData(ctx, cur)
}

func (d *Dispatcher) updateOne(ctx context.Context, db *mongo.Database, op Op) (json.RawMessage, error) {
	filter, err := ejson.DecodeDoc(op.Filter)
	if err != nil {
		return nil, err
	}
	update, err := decodeUpdate(op.Update)
	if err != nil {
		return nil, err
	}
	opts := options.UpdateOne()
	if op.Upsert != nil {
		opts = opts.SetUpsert(*op.Upsert)
	}
	if len(op.ArrayFilters) > 0 {
		afs, ferr := decodeArrayFilters(op.ArrayFilters)
		if ferr != nil {
			return nil, ferr
		}
		opts = opts.SetArrayFilters(afs)
	}
	res, err := db.Collection(op.Collection).UpdateOne(ctx, filter, update, opts)
	if err != nil {
		return nil, err
	}
	return updateResultToJSON(res)
}

func (d *Dispatcher) updateMany(ctx context.Context, db *mongo.Database, op Op) (json.RawMessage, error) {
	filter, err := ejson.DecodeDoc(op.Filter)
	if err != nil {
		return nil, err
	}
	update, err := decodeUpdate(op.Update)
	if err != nil {
		return nil, err
	}
	opts := options.UpdateMany()
	if op.Upsert != nil {
		opts = opts.SetUpsert(*op.Upsert)
	}
	if len(op.ArrayFilters) > 0 {
		afs, ferr := decodeArrayFilters(op.ArrayFilters)
		if ferr != nil {
			return nil, ferr
		}
		opts = opts.SetArrayFilters(afs)
	}
	res, err := db.Collection(op.Collection).UpdateMany(ctx, filter, update, opts)
	if err != nil {
		return nil, err
	}
	return updateResultToJSON(res)
}

func (d *Dispatcher) replaceOne(ctx context.Context, db *mongo.Database, op Op) (json.RawMessage, error) {
	filter, err := ejson.DecodeDoc(op.Filter)
	if err != nil {
		return nil, err
	}
	rep, err := ejson.DecodeDoc(op.Replacement)
	if err != nil {
		return nil, err
	}
	opts := options.Replace()
	if op.Upsert != nil {
		opts = opts.SetUpsert(*op.Upsert)
	}
	res, err := db.Collection(op.Collection).ReplaceOne(ctx, filter, rep, opts)
	if err != nil {
		return nil, err
	}
	return updateResultToJSON(res)
}

func (d *Dispatcher) deleteOne(ctx context.Context, db *mongo.Database, op Op) (json.RawMessage, error) {
	filter, err := ejson.DecodeDoc(op.Filter)
	if err != nil {
		return nil, err
	}
	res, err := db.Collection(op.Collection).DeleteOne(ctx, filter)
	if err != nil {
		return nil, err
	}
	return buildJSON(kv{"deleted_count", json.RawMessage(strconv.FormatInt(res.DeletedCount, 10))})
}

func (d *Dispatcher) deleteMany(ctx context.Context, db *mongo.Database, op Op) (json.RawMessage, error) {
	filter, err := ejson.DecodeDoc(op.Filter)
	if err != nil {
		return nil, err
	}
	res, err := db.Collection(op.Collection).DeleteMany(ctx, filter)
	if err != nil {
		return nil, err
	}
	return buildJSON(kv{"deleted_count", json.RawMessage(strconv.FormatInt(res.DeletedCount, 10))})
}

func (d *Dispatcher) countDocuments(ctx context.Context, db *mongo.Database, op Op) (json.RawMessage, error) {
	filter, err := ejson.DecodeDoc(op.Filter)
	if err != nil {
		return nil, err
	}
	n, err := db.Collection(op.Collection).CountDocuments(ctx, filter)
	if err != nil {
		return nil, err
	}
	return buildJSON(kv{"count", json.RawMessage(strconv.FormatInt(n, 10))})
}

func (d *Dispatcher) aggregate(ctx context.Context, db *mongo.Database, op Op) (json.RawMessage, error) {
	pipeline, err := ejson.DecodeDocs(op.Pipeline)
	if err != nil {
		return nil, err
	}
	// Convert []bson.D to mongo.Pipeline ([]bson.D-style).
	mp := make(mongo.Pipeline, len(pipeline))
	for i, p := range pipeline {
		mp[i] = p
	}
	cur, err := db.Collection(op.Collection).Aggregate(ctx, mp)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	return cursorToFindData(ctx, cur)
}

func (d *Dispatcher) bulkWrite(ctx context.Context, db *mongo.Database, op Op) (json.RawMessage, error) {
	models := make([]mongo.WriteModel, 0, len(op.Operations))
	// For inserts inside bulkWrite, the driver's BulkWriteResult does NOT
	// expose inserted IDs. To satisfy the spec we pre-assign _id when the
	// document doesn't already have one. We track the IDs alongside the
	// sub-op index so we can emit the inserted_ids map.
	insertedIDs := map[int]any{}

	for i, sub := range op.Operations {
		switch sub.Name {
		case "insertOne":
			doc, err := ejson.DecodeDoc(sub.Document)
			if err != nil {
				return nil, err
			}
			doc, id := ejson.EnsureID(doc)
			insertedIDs[i] = id
			models = append(models, mongo.NewInsertOneModel().SetDocument(doc))
		case "updateOne":
			f, err := ejson.DecodeDoc(sub.Filter)
			if err != nil {
				return nil, err
			}
			u, err := decodeUpdate(sub.Update)
			if err != nil {
				return nil, err
			}
			m := mongo.NewUpdateOneModel().SetFilter(f).SetUpdate(u)
			if sub.Upsert != nil {
				m = m.SetUpsert(*sub.Upsert)
			}
			if len(sub.ArrayFilters) > 0 {
				afs, ferr := decodeArrayFilters(sub.ArrayFilters)
				if ferr != nil {
					return nil, ferr
				}
				m = m.SetArrayFilters(afs)
			}
			models = append(models, m)
		case "updateMany":
			f, err := ejson.DecodeDoc(sub.Filter)
			if err != nil {
				return nil, err
			}
			u, err := decodeUpdate(sub.Update)
			if err != nil {
				return nil, err
			}
			m := mongo.NewUpdateManyModel().SetFilter(f).SetUpdate(u)
			if sub.Upsert != nil {
				m = m.SetUpsert(*sub.Upsert)
			}
			if len(sub.ArrayFilters) > 0 {
				afs, ferr := decodeArrayFilters(sub.ArrayFilters)
				if ferr != nil {
					return nil, ferr
				}
				m = m.SetArrayFilters(afs)
			}
			models = append(models, m)
		case "replaceOne":
			f, err := ejson.DecodeDoc(sub.Filter)
			if err != nil {
				return nil, err
			}
			r, err := ejson.DecodeDoc(sub.Replacement)
			if err != nil {
				return nil, err
			}
			m := mongo.NewReplaceOneModel().SetFilter(f).SetReplacement(r)
			if sub.Upsert != nil {
				m = m.SetUpsert(*sub.Upsert)
			}
			models = append(models, m)
		case "deleteOne":
			f, err := ejson.DecodeDoc(sub.Filter)
			if err != nil {
				return nil, err
			}
			models = append(models, mongo.NewDeleteOneModel().SetFilter(f))
		case "deleteMany":
			f, err := ejson.DecodeDoc(sub.Filter)
			if err != nil {
				return nil, err
			}
			models = append(models, mongo.NewDeleteManyModel().SetFilter(f))
		default:
			return nil, fmt.Errorf("bulkWrite sub-op %q unsupported", sub.Name)
		}
	}

	opts := options.BulkWrite()
	if op.Ordered != nil {
		opts = opts.SetOrdered(*op.Ordered)
	}
	res, err := db.Collection(op.Collection).BulkWrite(ctx, models, opts)
	if err != nil {
		return nil, err
	}

	// Build inserted_ids and upserted_ids maps keyed by decimal-string index.
	insertedMap := map[string]json.RawMessage{}
	for idx, id := range insertedIDs {
		raw, mErr := ejson.MarshalCanonical(id)
		if mErr != nil {
			return nil, mErr
		}
		insertedMap[strconv.Itoa(idx)] = raw
	}
	upsertedMap := map[string]json.RawMessage{}
	for idx, id := range res.UpsertedIDs {
		raw, mErr := ejson.MarshalCanonical(id)
		if mErr != nil {
			return nil, mErr
		}
		upsertedMap[strconv.FormatInt(idx, 10)] = raw
	}

	insertedB, _ := json.Marshal(insertedMap)
	upsertedB, _ := json.Marshal(upsertedMap)

	return buildJSON(
		kv{"inserted_count", json.RawMessage(strconv.FormatInt(res.InsertedCount, 10))},
		kv{"matched_count", json.RawMessage(strconv.FormatInt(res.MatchedCount, 10))},
		kv{"modified_count", json.RawMessage(strconv.FormatInt(res.ModifiedCount, 10))},
		kv{"deleted_count", json.RawMessage(strconv.FormatInt(res.DeletedCount, 10))},
		kv{"upserted_count", json.RawMessage(strconv.FormatInt(res.UpsertedCount, 10))},
		kv{"inserted_ids", json.RawMessage(insertedB)},
		kv{"upserted_ids", json.RawMessage(upsertedB)},
	)
}

// ---- helpers ----

// decodeUpdate accepts either a document (bson.D) or an array of pipeline
// stages (bson.A) for aggregation-pipeline updates. We try doc first; on a
// "decoded array" error fall back to array decoding.
func decodeUpdate(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return bson.D{}, nil
	}
	// Try array first by peeking at the first non-whitespace byte.
	for _, b := range raw {
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			continue
		}
		if b == '[' {
			var a bson.A
			if err := bson.UnmarshalExtJSON(raw, false, &a); err != nil {
				return nil, fmt.Errorf("decode update pipeline: %w", err)
			}
			return a, nil
		}
		break
	}
	var d bson.D
	if err := bson.UnmarshalExtJSON(raw, false, &d); err != nil {
		return nil, fmt.Errorf("decode update doc: %w", err)
	}
	return d, nil
}

func decodeArrayFilters(raws []json.RawMessage) ([]any, error) {
	out := make([]any, 0, len(raws))
	for i, r := range raws {
		d, err := ejson.DecodeDoc(r)
		if err != nil {
			return nil, fmt.Errorf("array_filters[%d]: %w", i, err)
		}
		out = append(out, d)
	}
	return out, nil
}

func updateResultToJSON(res *mongo.UpdateResult) (json.RawMessage, error) {
	kvs := []kv{
		{"matched_count", json.RawMessage(strconv.FormatInt(res.MatchedCount, 10))},
		{"modified_count", json.RawMessage(strconv.FormatInt(res.ModifiedCount, 10))},
	}
	if res.UpsertedCount > 0 && res.UpsertedID != nil {
		idRaw, err := ejson.MarshalCanonical(res.UpsertedID)
		if err != nil {
			return nil, err
		}
		kvs = append(kvs, kv{"upserted_id", idRaw})
	}
	return buildJSON(kvs...)
}

func cursorToFindData(ctx context.Context, cur *mongo.Cursor) (json.RawMessage, error) {
	docs := []json.RawMessage{}
	for cur.Next(ctx) {
		// Re-marshal cursor.Current (a bson.Raw) as Extended JSON v2.
		// We use relaxed form (canonical=false) so plain integers and
		// doubles round-trip as JSON numbers; ObjectIDs and other BSON-
		// typed values remain wrapped (e.g. {"$oid": ...}).
		raw, err := bson.MarshalExtJSON(cur.Current, false, false)
		if err != nil {
			return nil, fmt.Errorf("marshal document: %w", err)
		}
		docs = append(docs, json.RawMessage(raw))
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	docsJSON, _ := json.Marshal(docs)
	return buildJSON(
		kv{"documents", json.RawMessage(docsJSON)},
		kv{"count", json.RawMessage(strconv.Itoa(len(docs)))},
	)
}

// kv is one key/value pair in an order-preserving JSON object builder.
type kv struct {
	K string
	V json.RawMessage
}

// buildJSON builds a JSON object from kv entries in the order provided. We
// emit objects manually so the field order matches the spec's canonical
// scenarios (matters for byte-equal cross-implementation comparison).
func buildJSON(entries ...kv) (json.RawMessage, error) {
	var out []byte
	out = append(out, '{')
	for i, e := range entries {
		if i > 0 {
			out = append(out, ',')
		}
		kbytes, err := json.Marshal(e.K)
		if err != nil {
			return nil, err
		}
		out = append(out, kbytes...)
		out = append(out, ':')
		out = append(out, e.V...)
	}
	out = append(out, '}')
	return out, nil
}
