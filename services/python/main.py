#!/usr/bin/env python3
"""
PyMongo HTTP benchmark service.

Implements the mbench HTTP API spec (spec/http-api.md) using only the Python
standard library HTTP server plus PyMongo / BSON.

Usage:
    MONGODB_URI=mongodb://localhost:27017 PORT=8081 python3 main.py
"""

import json
import os
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pymongo
from bson import ObjectId
from datetime import datetime


class _Encoder(json.JSONEncoder):
    def default(self, o):
        if isinstance(o, ObjectId):
            return str(o)
        if isinstance(o, datetime):
            return o.isoformat()
        return super().default(o)


def to_json(obj):
    return json.dumps(obj, cls=_Encoder)

# ── global state ──────────────────────────────────────────────────────────────

MONGODB_URI = os.environ.get("MONGODB_URI", "mongodb://localhost:27017")
PORT = int(os.environ.get("PORT", "8081"))

# Eagerly create the MongoClient so /health can respond immediately.
_client = pymongo.MongoClient(MONGODB_URI)
# Force a connection so errors surface at startup, not on the first request.
_client.admin.command("ping")


# ── request handler ───────────────────────────────────────────────────────────

class Handler(BaseHTTPRequestHandler):

    # ── routing ───────────────────────────────────────────────────────────────

    def do_GET(self):
        if self.path == "/health":
            self._handle_health()
        else:
            self._send_error_json(404, "not_found", f"Unknown endpoint: {self.path}")

    _routes = {
        "/find":            "_handle_find",
        "/findOne":         "_handle_find_one",
        "/insertOne":       "_handle_insert_one",
        "/insertMany":      "_handle_insert_many",
        "/updateOne":       "_handle_update_one",
        "/updateMany":      "_handle_update_many",
        "/deleteOne":       "_handle_delete_one",
        "/deleteMany":      "_handle_delete_many",
        "/bulkWrite":       "_handle_bulk_write",
        "/clientBulkWrite": "_handle_client_bulk_write",
    }

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        raw_body = self.rfile.read(length).decode("utf-8")

        path = self.path.rstrip("/")
        method_name = self._routes.get(path)
        handler = getattr(self, method_name) if method_name else None
        if handler is None:
            self._send_error_json(404, "not_found", f"Unknown endpoint: {path}")
            return

        try:
            body = json.loads(raw_body) if raw_body else {}
        except Exception as exc:
            self._send_error_json(400, "invalid_request", f"Invalid JSON: {exc}")
            return

        try:
            handler(body)
        except pymongo.errors.PyMongoError as exc:
            self._send_error_json(500, "driver_error", str(exc))
        except _BadRequest as exc:
            self._send_error_json(400, "invalid_request", str(exc))
        except _NotSupported as exc:
            self._send_error_json(501, "unsupported", str(exc))

    # ── helper: send responses ────────────────────────────────────────────────

    def _send_json(self, status: int, data_str: str):
        encoded = data_str.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def _send_error_json(self, status: int, code: str, message: str):
        self._send_json(status, json.dumps({"error": code, "message": message}))

    # ── helper: extract common fields ─────────────────────────────────────────

    @staticmethod
    def _collection(body: dict, require_collection: bool = True):
        """Return (collection_object, body) or raise _BadRequest."""
        db_name = body.get("database", "perftest")
        coll_name = body.get("collection")
        if require_collection and not coll_name:
            raise _BadRequest("missing required field: collection")
        return _client[db_name][coll_name]

    # ── endpoint handlers ─────────────────────────────────────────────────────

    def _handle_health(self):
        data = {
            "status": "ok",
            "driver": "pymongo",
            "driverVersion": pymongo.version,
            "language": "python",
            "languageVersion": sys.version.split()[0],
        }
        self._send_json(200, json.dumps(data))

    def _handle_find(self, body: dict):
        if "filter" not in body:
            raise _BadRequest("missing required field: filter")
        coll = self._collection(body)
        filter_ = body["filter"]
        opts = body.get("options", {}) or {}

        kwargs = {}
        if "limit" in opts:
            kwargs["limit"] = int(opts["limit"])
        if "skip" in opts:
            kwargs["skip"] = int(opts["skip"])
        if "sort" in opts:
            kwargs["sort"] = opts["sort"]
        if "projection" in opts:
            kwargs["projection"] = opts["projection"]
        if "batchSize" in opts:
            kwargs["batch_size"] = int(opts["batchSize"])

        documents = list(coll.find(filter_, **kwargs))
        self._send_json(200, to_json({"documents": documents, "count": len(documents)}))

    def _handle_find_one(self, body: dict):
        if "filter" not in body:
            raise _BadRequest("missing required field: filter")
        coll = self._collection(body)
        document = coll.find_one(body["filter"])
        self._send_json(200, to_json({"document": document}))

    def _handle_insert_one(self, body: dict):
        if "document" not in body:
            raise _BadRequest("missing required field: document")
        coll = self._collection(body)
        result = coll.insert_one(body["document"])
        self._send_json(200, json.dumps({"insertedId": str(result.inserted_id)}))

    def _handle_insert_many(self, body: dict):
        if "documents" not in body:
            raise _BadRequest("missing required field: documents")
        coll = self._collection(body)
        opts = body.get("options", {}) or {}
        result = coll.insert_many(body["documents"], ordered=opts.get("ordered", True))
        self._send_json(200, json.dumps({"insertedCount": len(result.inserted_ids)}))

    def _handle_update_one(self, body: dict):
        for req in ("filter", "update"):
            if req not in body:
                raise _BadRequest(f"missing required field: {req}")
        coll = self._collection(body)
        opts = body.get("options", {}) or {}
        array_filters = opts.get("arrayFilters")
        kwargs = {"upsert": bool(opts.get("upsert", False))}
        if array_filters is not None:
            kwargs["array_filters"] = array_filters
        result = coll.update_one(body["filter"], body["update"], **kwargs)
        self._send_json(200, json.dumps({
            "matchedCount": result.matched_count,
            "modifiedCount": result.modified_count,
            "upsertedId": str(result.upserted_id) if result.upserted_id is not None else None,
        }))

    def _handle_update_many(self, body: dict):
        for req in ("filter", "update"):
            if req not in body:
                raise _BadRequest(f"missing required field: {req}")
        coll = self._collection(body)
        opts = body.get("options", {}) or {}
        array_filters = opts.get("arrayFilters")
        kwargs = {"upsert": bool(opts.get("upsert", False))}
        if array_filters is not None:
            kwargs["array_filters"] = array_filters
        result = coll.update_many(body["filter"], body["update"], **kwargs)
        self._send_json(200, json.dumps({
            "matchedCount": result.matched_count,
            "modifiedCount": result.modified_count,
            "upsertedId": str(result.upserted_id) if result.upserted_id is not None else None,
        }))

    def _handle_delete_one(self, body: dict):
        if "filter" not in body:
            raise _BadRequest("missing required field: filter")
        coll = self._collection(body)
        result = coll.delete_one(body["filter"])
        self._send_json(200, json.dumps({"deletedCount": result.deleted_count}))

    def _handle_delete_many(self, body: dict):
        if "filter" not in body:
            raise _BadRequest("missing required field: filter")
        coll = self._collection(body)
        result = coll.delete_many(body["filter"])
        self._send_json(200, json.dumps({"deletedCount": result.deleted_count}))

    def _handle_bulk_write(self, body: dict):
        if "operations" not in body:
            raise _BadRequest("missing required field: operations")
        coll = self._collection(body)
        opts = body.get("options", {}) or {}

        write_models = []
        for op in body["operations"]:
            if "insertOne" in op:
                doc = op["insertOne"]
                write_models.append(pymongo.InsertOne(doc["document"]))
            elif "updateOne" in op:
                doc = op["updateOne"]
                kwargs = {"upsert": bool(doc.get("upsert", False))}
                if "arrayFilters" in doc:
                    kwargs["array_filters"] = doc["arrayFilters"]
                write_models.append(pymongo.UpdateOne(
                    doc["filter"], doc["update"], **kwargs
                ))
            elif "updateMany" in op:
                doc = op["updateMany"]
                kwargs = {"upsert": bool(doc.get("upsert", False))}
                if "arrayFilters" in doc:
                    kwargs["array_filters"] = doc["arrayFilters"]
                write_models.append(pymongo.UpdateMany(
                    doc["filter"], doc["update"], **kwargs
                ))
            elif "replaceOne" in op:
                doc = op["replaceOne"]
                write_models.append(pymongo.ReplaceOne(
                    doc["filter"], doc["replacement"],
                    upsert=bool(doc.get("upsert", False))
                ))
            elif "deleteOne" in op:
                doc = op["deleteOne"]
                write_models.append(pymongo.DeleteOne(doc["filter"]))
            elif "deleteMany" in op:
                doc = op["deleteMany"]
                write_models.append(pymongo.DeleteMany(doc["filter"]))
            else:
                raise _BadRequest(f"unknown operation kind: {list(op.keys())}")

        result = coll.bulk_write(write_models, ordered=opts.get("ordered", True))
        self._send_json(200, json.dumps({
            "insertedCount": result.inserted_count,
            "matchedCount": result.matched_count,
            "modifiedCount": result.modified_count,
            "deletedCount": result.deleted_count,
            "upsertedCount": result.upserted_count,
        }))

    def _handle_client_bulk_write(self, body: dict):
        if "models" not in body:
            raise _BadRequest("missing required field: models")

        # Attempt to build and execute the client-level bulk write.
        # If the pymongo version or server doesn't support it, return 501.
        try:
            result_str = _build_and_run_client_bulk_write(body)
            self._send_json(200, result_str)
        except _NotSupported as exc:
            self._send_error_json(501, "unsupported", str(exc))

    # ── suppress default request logging ─────────────────────────────────────

    def log_message(self, *args):
        pass




# ── clientBulkWrite helper (isolated to catch import/attribute errors) ────────

def _build_and_run_client_bulk_write(body: dict) -> str:
    """Build and run client.bulk_write; returns a JSON string result.

    Raises _NotSupported if the feature is unavailable.
    """
    # Check that client.bulk_write exists (PyMongo >= 4.4).
    if not hasattr(_client, "bulk_write"):
        raise _NotSupported("client.bulk_write not available in this PyMongo version")

    try:
        from pymongo.operations import (
            InsertOne, UpdateOne, UpdateMany, ReplaceOne, DeleteOne, DeleteMany,
        )
    except ImportError as exc:
        raise _NotSupported(f"required pymongo classes unavailable: {exc}")

    write_models = []
    for model in body["models"]:
        ns = model.get("namespace")
        if not ns or "." not in ns:
            raise _BadRequest(f"invalid namespace: {ns!r}")
        full_ns = ns

        if "insertOne" in model:
            doc = model["insertOne"]
            try:
                write_models.append(InsertOne(doc["document"], namespace=full_ns))
            except TypeError:
                raise _NotSupported("client-level bulk_write requires PyMongo >= 4.4 with namespace support")
        elif "updateOne" in model:
            doc = model["updateOne"]
            kwargs = {"upsert": bool(doc.get("upsert", False)), "namespace": full_ns}
            if "arrayFilters" in doc:
                kwargs["array_filters"] = doc["arrayFilters"]
            try:
                write_models.append(UpdateOne(doc["filter"], doc["update"], **kwargs))
            except TypeError:
                raise _NotSupported("client-level bulk_write requires PyMongo >= 4.4")
        elif "updateMany" in model:
            doc = model["updateMany"]
            kwargs = {"upsert": bool(doc.get("upsert", False)), "namespace": full_ns}
            try:
                write_models.append(UpdateMany(doc["filter"], doc["update"], **kwargs))
            except TypeError:
                raise _NotSupported("client-level bulk_write requires PyMongo >= 4.4")
        elif "replaceOne" in model:
            doc = model["replaceOne"]
            try:
                write_models.append(ReplaceOne(
                    doc["filter"], doc["replacement"],
                    upsert=bool(doc.get("upsert", False)), namespace=full_ns,
                ))
            except TypeError:
                raise _NotSupported("client-level bulk_write requires PyMongo >= 4.4")
        elif "deleteOne" in model:
            doc = model["deleteOne"]
            try:
                write_models.append(DeleteOne(doc["filter"], namespace=full_ns))
            except TypeError:
                raise _NotSupported("client-level bulk_write requires PyMongo >= 4.4")
        elif "deleteMany" in model:
            doc = model["deleteMany"]
            try:
                write_models.append(DeleteMany(doc["filter"], namespace=full_ns))
            except TypeError:
                raise _NotSupported("client-level bulk_write requires PyMongo >= 4.4")
        else:
            raise _BadRequest(f"unknown operation kind in clientBulkWrite model: {list(model.keys())}")

    try:
        opts = body.get("options", {}) or {}
        result = _client.bulk_write(write_models, ordered=opts.get("ordered", True))
    except pymongo.errors.PyMongoError as exc:
        # Could be a server-level "operation not supported" error.
        if "not supported" in str(exc).lower() or "unsupported" in str(exc).lower():
            raise _NotSupported(str(exc))
        raise

    return json.dumps({
        "insertedCount": result.inserted_count,
        "matchedCount": result.matched_count,
        "modifiedCount": result.modified_count,
        "deletedCount": result.deleted_count,
        "upsertedCount": result.upserted_count,
    })


# ── internal exception types ──────────────────────────────────────────────────

class _BadRequest(Exception):
    pass


class _NotSupported(Exception):
    pass


# ── entry point ───────────────────────────────────────────────────────────────

if __name__ == "__main__":
    server = ThreadingHTTPServer(("", PORT), Handler)
    print(f"Listening on port {PORT} (MongoDB: {MONGODB_URI})", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
