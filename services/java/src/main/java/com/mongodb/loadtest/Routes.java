package com.mongodb.loadtest;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import com.mongodb.client.MongoClient;
import io.javalin.Javalin;
import io.javalin.http.Context;
import io.javalin.http.HttpStatus;
import org.bson.BsonDocument;
import org.bson.BsonInt32;

import java.util.ArrayList;
import java.util.Iterator;
import java.util.List;
import java.util.Set;

/** Wires the four /v1 endpoints onto a MongoClient. */
final class Routes {
    private static final ObjectMapper M = new ObjectMapper();
    private static final Set<String> PROTECTED_DBS = Set.of("admin", "local", "config");

    private Routes() {}

    static Javalin create(MongoClient client, App.Info info) {
        Dispatcher dispatcher = new Dispatcher(client);

        Javalin app = Javalin.create(cfg -> {
            cfg.showJavalinBanner = false;
            cfg.http.defaultContentType = "application/json; charset=utf-8";
            // Default max body is 1MB; raise so we don't 413 on bulk requests.
            cfg.http.maxRequestSize = 16L * 1024 * 1024;
        });

        app.before(ctx -> ctx.contentType("application/json; charset=utf-8"));

        app.post("/v1/ops", ctx -> handleOps(ctx, client, dispatcher));
        app.post("/v1/admin/reset", ctx -> handleReset(ctx, client));
        app.get("/v1/info", ctx -> handleInfo(ctx, info));
        app.get("/v1/health", ctx -> handleHealth(ctx, client));

        app.exception(Exception.class, (e, ctx) -> {
            ctx.status(HttpStatus.INTERNAL_SERVER_ERROR);
            writeRequestError(ctx, "INTERNAL", e.getMessage() == null ? e.getClass().getSimpleName() : e.getMessage());
        });

        return app;
    }

    // ---- /v1/ops ----

    private static void handleOps(Context ctx, MongoClient client, Dispatcher dispatcher) {
        JsonNode root;
        try {
            root = M.readTree(ctx.body());
        } catch (Exception e) {
            writeRequestError(ctx, "SCHEMA_VIOLATION", "invalid JSON: " + e.getMessage());
            return;
        }
        if (root == null || !root.isObject()) {
            writeRequestError(ctx, "SCHEMA_VIOLATION", "request body must be a JSON object");
            return;
        }
        if (!root.has("database")) {
            writeRequestError(ctx, "MISSING_FIELD", "missing required field \"database\"");
            return;
        }
        JsonNode dbNode = root.get("database");
        if (!dbNode.isTextual() || dbNode.asText().isEmpty()) {
            writeRequestError(ctx, "SCHEMA_VIOLATION", "`database` must be a non-empty string");
            return;
        }
        if (!root.has("ops")) {
            writeRequestError(ctx, "MISSING_FIELD", "missing required field \"ops\"");
            return;
        }
        JsonNode opsNode = root.get("ops");
        if (!opsNode.isArray()) {
            writeRequestError(ctx, "SCHEMA_VIOLATION", "`ops` must be an array");
            return;
        }
        if (opsNode.isEmpty()) {
            writeRequestError(ctx, "EMPTY_OPS", "ops must be a non-empty array");
            return;
        }
        // Validate each op shape.
        for (int i = 0; i < opsNode.size(); i++) {
            JsonNode op = opsNode.get(i);
            String err = validateOp(op, false);
            if (err != null) {
                String code = err.startsWith("UNKNOWN:") ? "UNKNOWN_OP"
                        : err.startsWith("MISSING:") ? "MISSING_FIELD"
                        : "SCHEMA_VIOLATION";
                String msg = err.substring(err.indexOf(':') + 1);
                writeRequestError(ctx, code, "ops[" + i + "]: " + msg);
                return;
            }
        }

        String database = dbNode.asText();
        StringBuilder out = new StringBuilder(256);
        out.append("{\"results\":[");
        for (int i = 0; i < opsNode.size(); i++) {
            if (i > 0) out.append(',');
            String res = dispatcher.runOne(database, opsNode.get(i));
            out.append(res);
        }
        out.append("]}");
        ctx.status(HttpStatus.OK);
        ctx.result(out.toString());
    }

    /** Returns null if op shape is valid; otherwise "CODE_KEY:message". */
    private static String validateOp(JsonNode op, boolean inBulk) {
        if (op == null || !op.isObject()) {
            return "SCHEMA:op must be an object";
        }
        JsonNode name = op.get("name");
        if (name == null || !name.isTextual() || name.asText().isEmpty()) {
            return "MISSING:missing required field \"name\"";
        }
        String n = name.asText();
        if (!inBulk) {
            JsonNode coll = op.get("collection");
            if (coll == null || !coll.isTextual() || coll.asText().isEmpty()) {
                return "SCHEMA:collection must be non-empty";
            }
        }
        return switch (n) {
            case "insertOne" -> requireField(op, "document");
            case "insertMany" -> requireArray(op, "documents");
            case "find", "countDocuments", "deleteOne", "deleteMany" -> requireField(op, "filter");
            case "updateOne", "updateMany" -> {
                String f = requireField(op, "filter");
                if (f != null) yield f;
                yield requireField(op, "update");
            }
            case "replaceOne" -> {
                String f = requireField(op, "filter");
                if (f != null) yield f;
                yield requireField(op, "replacement");
            }
            case "aggregate" -> {
                JsonNode p = op.get("pipeline");
                if (p == null || !p.isArray()) yield "MISSING:missing required field \"pipeline\"";
                yield null;
            }
            case "bulkWrite" -> {
                if (inBulk) yield "UNKNOWN:unknown op name \"bulkWrite\" (nested)";
                JsonNode ops = op.get("operations");
                if (ops == null || !ops.isArray() || ops.isEmpty()) {
                    yield "MISSING:missing required field \"operations\"";
                }
                for (int i = 0; i < ops.size(); i++) {
                    String e = validateOp(ops.get(i), true);
                    if (e != null) {
                        int idx = e.indexOf(':');
                        yield e.substring(0, idx + 1) + "operations[" + i + "]: " + e.substring(idx + 1);
                    }
                }
                yield null;
            }
            default -> "UNKNOWN:unknown op name \"" + n + "\"";
        };
    }

    private static String requireField(JsonNode op, String field) {
        JsonNode v = op.get(field);
        if (v == null) return "MISSING:missing required field \"" + field + "\"";
        return null;
    }

    private static String requireArray(JsonNode op, String field) {
        JsonNode v = op.get(field);
        if (v == null || !v.isArray()) return "MISSING:missing required field \"" + field + "\"";
        return null;
    }

    // ---- /v1/admin/reset ----

    private static void handleReset(Context ctx, MongoClient client) {
        JsonNode root;
        try {
            root = M.readTree(ctx.body());
        } catch (Exception e) {
            writeRequestError(ctx, "SCHEMA_VIOLATION", "invalid JSON: " + e.getMessage());
            return;
        }
        if (root == null || !root.isObject()) {
            writeRequestError(ctx, "SCHEMA_VIOLATION", "request body must be a JSON object");
            return;
        }
        JsonNode dbs = root.get("databases");
        if (dbs == null || !dbs.isArray()) {
            writeRequestError(ctx, "SCHEMA_VIOLATION", "`databases` must be an array");
            return;
        }
        if (dbs.isEmpty()) {
            writeRequestError(ctx, "EMPTY_OPS", "databases must be a non-empty array");
            return;
        }
        List<String> names = new ArrayList<>(dbs.size());
        for (int i = 0; i < dbs.size(); i++) {
            JsonNode n = dbs.get(i);
            if (!n.isTextual() || n.asText().isEmpty()) {
                writeRequestError(ctx, "SCHEMA_VIOLATION", "database name must be a non-empty string");
                return;
            }
            String name = n.asText();
            if (PROTECTED_DBS.contains(name)) {
                writeRequestError(ctx, "BAD_REQUEST", "refusing to drop \"" + name + "\"");
                return;
            }
            names.add(name);
        }
        ObjectNode resp = M.createObjectNode();
        var arr = resp.putArray("dropped");
        for (String name : names) {
            client.getDatabase(name).drop();
            arr.add(name);
        }
        ctx.status(HttpStatus.OK);
        ctx.result(resp.toString());
    }

    // ---- /v1/info ----

    private static void handleInfo(Context ctx, App.Info info) {
        ObjectNode obj = M.createObjectNode();
        obj.put("driver", info.driver());
        obj.put("driver_version", info.driverVersion());
        obj.put("language", info.language());
        obj.put("language_version", info.languageVersion());
        obj.put("spec_version", info.specVersion());
        ctx.status(HttpStatus.OK);
        ctx.result(obj.toString());
    }

    // ---- /v1/health ----

    private static void handleHealth(Context ctx, MongoClient client) {
        try {
            client.getDatabase("admin").runCommand(new BsonDocument().append("ping", new BsonInt32(1)));
            ctx.status(HttpStatus.OK);
            ctx.result("{\"ok\":true}");
        } catch (Exception e) {
            ctx.status(HttpStatus.SERVICE_UNAVAILABLE);
            ObjectNode body = M.createObjectNode();
            body.put("ok", false);
            body.put("detail", e.getMessage() == null ? e.getClass().getSimpleName() : e.getMessage());
            ctx.result(body.toString());
        }
    }

    // ---- helpers ----

    private static void writeRequestError(Context ctx, String code, String message) {
        ObjectNode body = M.createObjectNode();
        ObjectNode err = body.putObject("error");
        err.put("code", code);
        err.put("message", message);
        ctx.status(HttpStatus.BAD_REQUEST);
        ctx.result(body.toString());
    }
}
