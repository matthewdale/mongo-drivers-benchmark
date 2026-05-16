package com.mongodb.loadtest;

import com.fasterxml.jackson.databind.JsonNode;
import com.mongodb.MongoBulkWriteException;
import com.mongodb.MongoCommandException;
import com.mongodb.MongoExecutionTimeoutException;
import com.mongodb.MongoQueryException;
import com.mongodb.MongoSecurityException;
import com.mongodb.MongoServerException;
import com.mongodb.MongoSocketException;
import com.mongodb.MongoSocketReadTimeoutException;
import com.mongodb.MongoSocketWriteTimeoutException;
import com.mongodb.MongoTimeoutException;
import com.mongodb.MongoWriteException;
import com.mongodb.bulk.BulkWriteInsert;
import com.mongodb.bulk.BulkWriteResult;
import com.mongodb.bulk.BulkWriteUpsert;
import com.mongodb.client.MongoClient;
import com.mongodb.client.MongoCollection;
import com.mongodb.client.MongoDatabase;
import com.mongodb.client.model.BulkWriteOptions;
import com.mongodb.client.model.DeleteManyModel;
import com.mongodb.client.model.DeleteOneModel;
import com.mongodb.client.model.InsertManyOptions;
import com.mongodb.client.model.InsertOneModel;
import com.mongodb.client.model.ReplaceOneModel;
import com.mongodb.client.model.ReplaceOptions;
import com.mongodb.client.model.UpdateManyModel;
import com.mongodb.client.model.UpdateOneModel;
import com.mongodb.client.model.UpdateOptions;
import com.mongodb.client.model.WriteModel;
import com.mongodb.client.result.DeleteResult;
import com.mongodb.client.result.InsertManyResult;
import com.mongodb.client.result.InsertOneResult;
import com.mongodb.client.result.UpdateResult;
import org.bson.BsonArray;
import org.bson.BsonDocument;
import org.bson.BsonObjectId;
import org.bson.BsonString;
import org.bson.BsonValue;
import org.bson.conversions.Bson;
import org.bson.types.ObjectId;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * Executes a single ops request. One Dispatcher instance is shared across all
 * requests (MongoClient is thread-safe). All BSON-typed values are serialized
 * to Extended JSON v2 in RELAXED form (plain JSON numbers for Int32/Int64/Double
 * and {$...} envelopes for non-numeric types) per spec §5.1.
 */
final class Dispatcher {
    private final MongoClient client;

    Dispatcher(MongoClient client) {
        this.client = client;
    }

    /**
     * Run one op against the named database and return its serialized result
     * envelope as a JSON object string (one of OpResultSuccess / OpResultFailure
     * shapes from openapi.yaml).
     */
    String runOne(String dbName, JsonNode op) {
        String name = op.get("name").asText();
        try {
            String data = execute(dbName, op);
            // Build {"op":"<name>","ok":true,"data":<data>}
            StringBuilder sb = new StringBuilder();
            sb.append("{\"op\":").append(Ejson.jsonString(name))
              .append(",\"ok\":true,\"data\":").append(data).append('}');
            return sb.toString();
        } catch (Throwable t) {
            return Errors.toResultJson(name, t);
        }
    }

    private String execute(String dbName, JsonNode op) throws Exception {
        MongoDatabase db = client.getDatabase(dbName);
        String name = op.get("name").asText();
        String collection = op.has("collection") ? op.get("collection").asText() : null;
        MongoCollection<BsonDocument> coll =
                collection == null ? null : db.getCollection(collection, BsonDocument.class);

        return switch (name) {
            case "insertOne" -> insertOne(coll, op);
            case "insertMany" -> insertMany(coll, op);
            case "find" -> find(coll, op);
            case "updateOne" -> updateOne(coll, op);
            case "updateMany" -> updateMany(coll, op);
            case "replaceOne" -> replaceOne(coll, op);
            case "deleteOne" -> deleteOne(coll, op);
            case "deleteMany" -> deleteMany(coll, op);
            case "countDocuments" -> countDocuments(coll, op);
            case "aggregate" -> aggregate(coll, op);
            case "bulkWrite" -> bulkWrite(db, op);
            default -> throw new IllegalArgumentException("unknown op " + name);
        };
    }

    // ---- per-op implementations ----

    private String insertOne(MongoCollection<BsonDocument> coll, JsonNode op) {
        BsonDocument doc = Ejson.parseDoc(op.get("document"));
        InsertOneResult res = coll.insertOne(doc);
        BsonValue id = res.getInsertedId();
        if (id == null) {
            // Driver did not surface an id; fall back to whatever is in the doc.
            id = doc.get("_id");
        }
        return "{\"inserted_id\":" + Ejson.marshalValue(id) + "}";
    }

    private String insertMany(MongoCollection<BsonDocument> coll, JsonNode op) {
        List<BsonDocument> docs = Ejson.parseDocs(op.get("documents"));
        InsertManyOptions opts = new InsertManyOptions();
        if (op.has("ordered")) opts.ordered(op.get("ordered").asBoolean());
        InsertManyResult res = coll.insertMany(docs, opts);
        // Build the inserted_ids array in input order.
        StringBuilder ids = new StringBuilder("[");
        Map<Integer, BsonValue> idMap = res.getInsertedIds();
        for (int i = 0; i < docs.size(); i++) {
            if (i > 0) ids.append(',');
            BsonValue v = idMap.get(i);
            if (v == null) v = docs.get(i).get("_id");
            ids.append(Ejson.marshalValue(v));
        }
        ids.append(']');
        return "{\"inserted_ids\":" + ids + ",\"inserted_count\":" + docs.size() + "}";
    }

    private String find(MongoCollection<BsonDocument> coll, JsonNode op) {
        BsonDocument filter = Ejson.parseDoc(op.get("filter"));
        var iter = coll.find(filter);
        if (op.has("projection")) iter = iter.projection(Ejson.parseDoc(op.get("projection")));
        if (op.has("sort")) iter = iter.sort(Ejson.parseDoc(op.get("sort")));
        if (op.has("skip")) iter = iter.skip(op.get("skip").asInt());
        if (op.has("limit")) iter = iter.limit(op.get("limit").asInt());
        List<BsonDocument> docs = iter.into(new ArrayList<>());
        return docsToFindData(docs);
    }

    private String updateOne(MongoCollection<BsonDocument> coll, JsonNode op) {
        BsonDocument filter = Ejson.parseDoc(op.get("filter"));
        Object update = Ejson.parseUpdate(op.get("update"));
        UpdateOptions opts = new UpdateOptions();
        if (op.has("upsert")) opts.upsert(op.get("upsert").asBoolean());
        if (op.has("array_filters")) opts.arrayFilters(Ejson.parseDocs(op.get("array_filters")));
        UpdateResult res = update instanceof List<?> lst
                ? coll.updateOne(filter, (List<? extends Bson>) lst, opts)
                : coll.updateOne(filter, (Bson) update, opts);
        return updateResultJson(res);
    }

    private String updateMany(MongoCollection<BsonDocument> coll, JsonNode op) {
        BsonDocument filter = Ejson.parseDoc(op.get("filter"));
        Object update = Ejson.parseUpdate(op.get("update"));
        UpdateOptions opts = new UpdateOptions();
        if (op.has("upsert")) opts.upsert(op.get("upsert").asBoolean());
        if (op.has("array_filters")) opts.arrayFilters(Ejson.parseDocs(op.get("array_filters")));
        UpdateResult res = update instanceof List<?> lst
                ? coll.updateMany(filter, (List<? extends Bson>) lst, opts)
                : coll.updateMany(filter, (Bson) update, opts);
        return updateResultJson(res);
    }

    private String replaceOne(MongoCollection<BsonDocument> coll, JsonNode op) {
        BsonDocument filter = Ejson.parseDoc(op.get("filter"));
        BsonDocument replacement = Ejson.parseDoc(op.get("replacement"));
        ReplaceOptions opts = new ReplaceOptions();
        if (op.has("upsert")) opts.upsert(op.get("upsert").asBoolean());
        UpdateResult res = coll.replaceOne(filter, replacement, opts);
        return updateResultJson(res);
    }

    private String deleteOne(MongoCollection<BsonDocument> coll, JsonNode op) {
        BsonDocument filter = Ejson.parseDoc(op.get("filter"));
        DeleteResult res = coll.deleteOne(filter);
        return "{\"deleted_count\":" + res.getDeletedCount() + "}";
    }

    private String deleteMany(MongoCollection<BsonDocument> coll, JsonNode op) {
        BsonDocument filter = Ejson.parseDoc(op.get("filter"));
        DeleteResult res = coll.deleteMany(filter);
        return "{\"deleted_count\":" + res.getDeletedCount() + "}";
    }

    private String countDocuments(MongoCollection<BsonDocument> coll, JsonNode op) {
        BsonDocument filter = Ejson.parseDoc(op.get("filter"));
        long n = coll.countDocuments(filter);
        return "{\"count\":" + n + "}";
    }

    private String aggregate(MongoCollection<BsonDocument> coll, JsonNode op) {
        List<BsonDocument> pipeline = Ejson.parseDocs(op.get("pipeline"));
        var iter = coll.aggregate(pipeline);
        List<BsonDocument> docs = iter.into(new ArrayList<>());
        return docsToFindData(docs);
    }

    private String bulkWrite(MongoDatabase db, JsonNode op) {
        String collection = op.get("collection").asText();
        MongoCollection<BsonDocument> coll = db.getCollection(collection, BsonDocument.class);

        JsonNode sub = op.get("operations");
        List<WriteModel<BsonDocument>> models = new ArrayList<>(sub.size());
        // Pre-assign client-side _id for insertOne sub-ops without _id so we
        // can populate inserted_ids on driver versions where BulkWriteResult
        // does not expose them.
        Map<Integer, BsonValue> preAssignedIds = new HashMap<>();

        for (int i = 0; i < sub.size(); i++) {
            JsonNode s = sub.get(i);
            String name = s.get("name").asText();
            switch (name) {
                case "insertOne" -> {
                    BsonDocument doc = Ejson.parseDoc(s.get("document"));
                    BsonValue id;
                    if (doc.containsKey("_id")) {
                        id = doc.get("_id");
                    } else {
                        id = new BsonObjectId(new ObjectId());
                        // Prepend _id without mutating user-provided documents
                        // that already had _id (we already checked above).
                        BsonDocument withId = new BsonDocument();
                        withId.put("_id", id);
                        for (var entry : doc.entrySet()) withId.put(entry.getKey(), entry.getValue());
                        doc = withId;
                    }
                    preAssignedIds.put(i, id);
                    models.add(new InsertOneModel<>(doc));
                }
                case "updateOne" -> {
                    BsonDocument filter = Ejson.parseDoc(s.get("filter"));
                    Object update = Ejson.parseUpdate(s.get("update"));
                    UpdateOptions o = new UpdateOptions();
                    if (s.has("upsert")) o.upsert(s.get("upsert").asBoolean());
                    if (s.has("array_filters")) o.arrayFilters(Ejson.parseDocs(s.get("array_filters")));
                    if (update instanceof List<?> lst) {
                        models.add(new UpdateOneModel<>(filter, (List<? extends Bson>) lst, o));
                    } else {
                        models.add(new UpdateOneModel<>(filter, (Bson) update, o));
                    }
                }
                case "updateMany" -> {
                    BsonDocument filter = Ejson.parseDoc(s.get("filter"));
                    Object update = Ejson.parseUpdate(s.get("update"));
                    UpdateOptions o = new UpdateOptions();
                    if (s.has("upsert")) o.upsert(s.get("upsert").asBoolean());
                    if (s.has("array_filters")) o.arrayFilters(Ejson.parseDocs(s.get("array_filters")));
                    if (update instanceof List<?> lst) {
                        models.add(new UpdateManyModel<>(filter, (List<? extends Bson>) lst, o));
                    } else {
                        models.add(new UpdateManyModel<>(filter, (Bson) update, o));
                    }
                }
                case "replaceOne" -> {
                    BsonDocument filter = Ejson.parseDoc(s.get("filter"));
                    BsonDocument replacement = Ejson.parseDoc(s.get("replacement"));
                    ReplaceOptions o = new ReplaceOptions();
                    if (s.has("upsert")) o.upsert(s.get("upsert").asBoolean());
                    models.add(new ReplaceOneModel<>(filter, replacement, o));
                }
                case "deleteOne" -> {
                    BsonDocument filter = Ejson.parseDoc(s.get("filter"));
                    models.add(new DeleteOneModel<>(filter));
                }
                case "deleteMany" -> {
                    BsonDocument filter = Ejson.parseDoc(s.get("filter"));
                    models.add(new DeleteManyModel<>(filter));
                }
                default -> throw new IllegalArgumentException("bulkWrite sub-op " + name + " unsupported");
            }
        }
        BulkWriteOptions opts = new BulkWriteOptions();
        if (op.has("ordered")) opts.ordered(op.get("ordered").asBoolean());

        BulkWriteResult res = coll.bulkWrite(models, opts);

        // Build inserted_ids map. Prefer driver-reported (getInserts()), fall
        // back to pre-assigned ids by index.
        Map<Integer, BsonValue> insertedIds = new LinkedHashMap<>();
        if (res.getInserts() != null) {
            for (BulkWriteInsert ins : res.getInserts()) {
                insertedIds.put(ins.getIndex(), ins.getId());
            }
        }
        // Add any inserts the driver didn't report (typically: it reports them
        // all, but we keep the pre-assigned ones as a fallback so the map is
        // never empty for inserts).
        for (var e : preAssignedIds.entrySet()) {
            insertedIds.putIfAbsent(e.getKey(), e.getValue());
        }
        Map<Integer, BsonValue> upsertedIds = new LinkedHashMap<>();
        for (BulkWriteUpsert up : res.getUpserts()) {
            upsertedIds.put(up.getIndex(), up.getId());
        }

        StringBuilder sb = new StringBuilder();
        sb.append("{\"inserted_count\":").append(res.getInsertedCount());
        sb.append(",\"matched_count\":").append(res.getMatchedCount());
        sb.append(",\"modified_count\":").append(res.getModifiedCount());
        sb.append(",\"deleted_count\":").append(res.getDeletedCount());
        sb.append(",\"upserted_count\":").append(res.getUpserts().size());
        sb.append(",\"inserted_ids\":").append(idMapJson(insertedIds));
        sb.append(",\"upserted_ids\":").append(idMapJson(upsertedIds));
        sb.append('}');
        return sb.toString();
    }

    // ---- helpers ----

    private static String idMapJson(Map<Integer, BsonValue> m) {
        StringBuilder sb = new StringBuilder("{");
        boolean first = true;
        for (var e : m.entrySet()) {
            if (!first) sb.append(',');
            first = false;
            sb.append('"').append(e.getKey()).append('"').append(':');
            sb.append(Ejson.marshalValue(e.getValue()));
        }
        sb.append('}');
        return sb.toString();
    }

    private static String updateResultJson(UpdateResult res) {
        StringBuilder sb = new StringBuilder();
        sb.append("{\"matched_count\":").append(res.getMatchedCount());
        sb.append(",\"modified_count\":").append(res.getModifiedCount());
        if (res.getUpsertedId() != null) {
            sb.append(",\"upserted_id\":").append(Ejson.marshalValue(res.getUpsertedId()));
        }
        sb.append('}');
        return sb.toString();
    }

    private static String docsToFindData(List<BsonDocument> docs) {
        StringBuilder sb = new StringBuilder();
        sb.append("{\"documents\":[");
        for (int i = 0; i < docs.size(); i++) {
            if (i > 0) sb.append(',');
            sb.append(Ejson.marshalDoc(docs.get(i)));
        }
        sb.append("],\"count\":").append(docs.size()).append('}');
        return sb.toString();
    }
}
