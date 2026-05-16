"""MongoDB drivers load-test HTTP service — Python (pymongo) implementation.

Implements the four /v1 endpoints from spec/http-api.md. Uses pymongo in
synchronous mode with a single shared MongoClient (thread-safe). The HTTP
layer is Flask; production deployments run under gunicorn with threads.
"""

from __future__ import annotations

import os
import platform
import sys
import threading
import time
from typing import Any

import bson
from bson import json_util
from bson.binary import Binary
from bson.codec_options import TypeRegistry
from bson.json_util import JSONMode, JSONOptions, DEFAULT_JSON_OPTIONS
from bson.objectid import ObjectId
from flask import Flask, Response, request
from pymongo import MongoClient
from pymongo import (
    DeleteMany,
    DeleteOne,
    InsertOne as BulkInsertOne,
    ReplaceOne,
    UpdateMany,
    UpdateOne,
)
from pymongo.errors import (
    AutoReconnect,
    BulkWriteError,
    ConnectionFailure,
    DuplicateKeyError,
    ExecutionTimeout,
    InvalidDocument,
    InvalidOperation,
    NetworkTimeout,
    OperationFailure,
    PyMongoError,
    ServerSelectionTimeoutError,
    WriteError,
    WTimeoutError,
)


# -----------------------------------------------------------------------------
# Extended JSON encoding/decoding
# -----------------------------------------------------------------------------

# RELAXED Extended JSON v2: numeric BSON types (Int32/Int64/Double) emit as
# plain JSON numbers; non-numeric BSON types (ObjectId, Decimal128, Binary,
# DateTime, Timestamp, Regex, Min/MaxKey) use the canonical {$...} envelope.
# This matches the spec's §5.1 amendment exactly.
EJSON_OPTIONS = JSONOptions(json_mode=JSONMode.RELAXED)


def ejson_dumps(obj: Any) -> str:
    """Serialize obj (dict/list of Python primitives + BSON types) to JSON."""
    return json_util.dumps(obj, json_options=EJSON_OPTIONS, ensure_ascii=False)


def ejson_loads(s: str | bytes) -> Any:
    """Parse JSON, accepting both canonical and relaxed Extended JSON v2."""
    return json_util.loads(s, json_options=EJSON_OPTIONS)


# -----------------------------------------------------------------------------
# Error classification (spec §7)
# -----------------------------------------------------------------------------

# Server code -> normalized ErrorCode. Mirrors services/go/internal/errs.
_SERVER_CODE_MAP: dict[int, str] = {
    11000: "DUPLICATE_KEY",
    11001: "DUPLICATE_KEY",
    12582: "DUPLICATE_KEY",
    112: "WRITE_CONFLICT",
    50: "TIMEOUT",
    89: "TIMEOUT",
    262: "TIMEOUT",
    13: "AUTH",
    18: "AUTH",
    8000: "AUTH",
    26: "NOT_FOUND",
    27: "NOT_FOUND",
    2: "INVALID_ARGUMENT",
    9: "INVALID_ARGUMENT",
    14: "INVALID_ARGUMENT",
    40: "INVALID_ARGUMENT",
    51: "INVALID_ARGUMENT",
    72: "INVALID_ARGUMENT",
    73: "INVALID_ARGUMENT",
    4: "INVALID_ARGUMENT",
    16: "INVALID_ARGUMENT",
    17: "INVALID_ARGUMENT",
    30: "INVALID_ARGUMENT",
    31: "INVALID_ARGUMENT",
    52: "INVALID_ARGUMENT",
    66: "INVALID_ARGUMENT",
}


def _code_from_server(code: int) -> str:
    return _SERVER_CODE_MAP.get(code, "INTERNAL")


def classify_error(exc: BaseException) -> dict[str, Any]:
    """Map a pymongo exception to {code, message, server_code?}.

    Precedence per spec §7 rule 1: server error code first; driver exception
    class second; INTERNAL otherwise.
    """
    msg = str(exc)
    err: dict[str, Any] = {"message": msg}

    # BulkWriteError: details['writeErrors'] is a list of per-sub-op errors.
    if isinstance(exc, BulkWriteError):
        details = getattr(exc, "details", None) or {}
        write_errors = details.get("writeErrors") or []
        if write_errors:
            first = write_errors[0]
            sc = first.get("code")
            if isinstance(sc, int):
                err["code"] = _code_from_server(sc)
                err["server_code"] = sc
                return err
        wce = details.get("writeConcernError")
        if wce and isinstance(wce.get("code"), int):
            sc = wce["code"]
            err["code"] = _code_from_server(sc)
            err["server_code"] = sc
            return err

    # DuplicateKeyError carries .code.
    if isinstance(exc, DuplicateKeyError):
        sc = getattr(exc, "code", None)
        err["code"] = "DUPLICATE_KEY"
        if isinstance(sc, int):
            err["server_code"] = sc
        return err

    # OperationFailure (and subclasses like ExecutionTimeout, WriteError) carry .code.
    if isinstance(exc, OperationFailure):
        sc = getattr(exc, "code", None)
        if isinstance(sc, int):
            err["code"] = _code_from_server(sc)
            err["server_code"] = sc
            return err
        # No code: classify by class.
        if isinstance(exc, (ExecutionTimeout, WTimeoutError)):
            err["code"] = "TIMEOUT"
            return err
        err["code"] = "INTERNAL"
        return err

    # Network / topology errors.
    if isinstance(exc, (NetworkTimeout,)):
        err["code"] = "TIMEOUT"
        return err
    if isinstance(exc, (ServerSelectionTimeoutError, AutoReconnect, ConnectionFailure)):
        err["code"] = "NETWORK"
        return err

    # Driver-level argument errors (bad BSON, invalid op).
    if isinstance(exc, (InvalidDocument, InvalidOperation, TypeError, ValueError)):
        err["code"] = "INVALID_ARGUMENT"
        return err

    err["code"] = "INTERNAL"
    return err


# -----------------------------------------------------------------------------
# Op dispatcher
# -----------------------------------------------------------------------------

KNOWN_OPS = frozenset(
    [
        "insertOne",
        "insertMany",
        "find",
        "updateOne",
        "updateMany",
        "replaceOne",
        "deleteOne",
        "deleteMany",
        "countDocuments",
        "aggregate",
        "bulkWrite",
    ]
)

# Ops valid inside bulkWrite.operations[].
BULK_SUB_OPS = frozenset(
    ["insertOne", "updateOne", "updateMany", "replaceOne", "deleteOne", "deleteMany"]
)


class ValidationError(Exception):
    def __init__(self, code: str, message: str) -> None:
        super().__init__(message)
        self.code = code
        self.message = message


def _missing(field: str) -> ValidationError:
    return ValidationError("MISSING_FIELD", f"missing required field {field!r}")


def _schema(msg: str) -> ValidationError:
    return ValidationError("SCHEMA_VIOLATION", msg)


def _unknown_op(name: str) -> ValidationError:
    return ValidationError("UNKNOWN_OP", f"unknown op name {name!r}")


def _empty_ops() -> ValidationError:
    return ValidationError("EMPTY_OPS", "ops must be a non-empty array")


def _validate_op(op: Any, in_bulk: bool) -> None:
    if not isinstance(op, dict):
        raise _schema("op must be an object")
    name = op.get("name")
    if not name or not isinstance(name, str):
        raise _missing("name")
    if not in_bulk:
        coll = op.get("collection")
        if not coll or not isinstance(coll, str):
            raise _schema("collection must be non-empty")
    if name == "bulkWrite":
        if in_bulk:
            raise _unknown_op("bulkWrite (nested)")
        operations = op.get("operations")
        if not isinstance(operations, list) or len(operations) == 0:
            raise _missing("operations")
        for i, sub in enumerate(operations):
            try:
                _validate_op(sub, True)
            except ValidationError as e:
                raise ValidationError(e.code, f"operations[{i}]: {e.message}") from None
        if not in_bulk:
            pass
        return
    if name not in KNOWN_OPS:
        raise _unknown_op(name)
    if in_bulk and name not in BULK_SUB_OPS:
        raise _unknown_op(name)
    if name == "insertOne":
        if "document" not in op:
            raise _missing("document")
    elif name == "insertMany":
        docs = op.get("documents")
        if not isinstance(docs, list) or len(docs) == 0:
            raise _missing("documents")
    elif name == "find":
        if "filter" not in op:
            raise _missing("filter")
    elif name in ("updateOne", "updateMany"):
        if "filter" not in op:
            raise _missing("filter")
        if "update" not in op:
            raise _missing("update")
    elif name == "replaceOne":
        if "filter" not in op:
            raise _missing("filter")
        if "replacement" not in op:
            raise _missing("replacement")
    elif name in ("deleteOne", "deleteMany"):
        if "filter" not in op:
            raise _missing("filter")
    elif name == "countDocuments":
        if "filter" not in op:
            raise _missing("filter")
    elif name == "aggregate":
        if "pipeline" not in op or not isinstance(op["pipeline"], list):
            raise _missing("pipeline")


def _decode_doc(v: Any) -> dict:
    """Pass through a dict (already-decoded). json_util.loads has already
    converted ext-JSON envelopes to BSON types, so v should be a dict (or empty)."""
    if v is None:
        return {}
    if not isinstance(v, dict):
        raise ValueError(f"expected document, got {type(v).__name__}")
    return v


def _decode_update(v: Any):
    """Update can be a doc or an aggregation pipeline (list of docs)."""
    if isinstance(v, list):
        return v
    if isinstance(v, dict):
        return v
    if v is None:
        return {}
    raise ValueError("update must be a document or an array of pipeline stages")


def _bulk_model(sub: dict):
    """Convert a bulkWrite sub-op dict to a pymongo write model.

    Returns (model, inserted_id_or_None). For insertOne we pre-assign a fresh
    ObjectId when the document lacks _id, and return that id so the caller
    can populate the inserted_ids map.
    """
    name = sub["name"]
    if name == "insertOne":
        doc = _decode_doc(sub.get("document"))
        # Per spec §6.4: pre-assign _id when missing so we can populate inserted_ids.
        if "_id" not in doc:
            new_id = ObjectId()
            doc = dict(doc)
            doc["_id"] = new_id
            return BulkInsertOne(doc), new_id
        return BulkInsertOne(doc), doc["_id"]
    if name == "updateOne":
        f = _decode_doc(sub.get("filter"))
        u = _decode_update(sub.get("update"))
        kwargs: dict[str, Any] = {}
        if sub.get("upsert") is not None:
            kwargs["upsert"] = bool(sub["upsert"])
        if sub.get("array_filters"):
            kwargs["array_filters"] = [_decode_doc(d) for d in sub["array_filters"]]
        return UpdateOne(f, u, **kwargs), None
    if name == "updateMany":
        f = _decode_doc(sub.get("filter"))
        u = _decode_update(sub.get("update"))
        kwargs = {}
        if sub.get("upsert") is not None:
            kwargs["upsert"] = bool(sub["upsert"])
        if sub.get("array_filters"):
            kwargs["array_filters"] = [_decode_doc(d) for d in sub["array_filters"]]
        return UpdateMany(f, u, **kwargs), None
    if name == "replaceOne":
        f = _decode_doc(sub.get("filter"))
        r = _decode_doc(sub.get("replacement"))
        kwargs = {}
        if sub.get("upsert") is not None:
            kwargs["upsert"] = bool(sub["upsert"])
        return ReplaceOne(f, r, **kwargs), None
    if name == "deleteOne":
        f = _decode_doc(sub.get("filter"))
        return DeleteOne(f), None
    if name == "deleteMany":
        f = _decode_doc(sub.get("filter"))
        return DeleteMany(f), None
    raise ValueError(f"unsupported bulkWrite sub-op {name!r}")


def execute_op(db, op: dict) -> dict[str, Any]:
    """Execute one op against db. Returns the success `data` payload as a
    Python object tree (will be serialized later via ejson_dumps). Raises
    pymongo errors on failure."""
    name = op["name"]
    coll = db[op["collection"]]

    if name == "insertOne":
        doc = _decode_doc(op.get("document"))
        res = coll.insert_one(doc)
        return {"inserted_id": res.inserted_id}

    if name == "insertMany":
        docs = [_decode_doc(d) for d in op["documents"]]
        ordered = op.get("ordered", True)
        res = coll.insert_many(docs, ordered=bool(ordered))
        return {
            "inserted_ids": list(res.inserted_ids),
            "inserted_count": len(res.inserted_ids),
        }

    if name == "find":
        f = _decode_doc(op.get("filter"))
        kwargs: dict[str, Any] = {}
        if "projection" in op and op["projection"] is not None:
            kwargs["projection"] = _decode_doc(op["projection"])
        cur = coll.find(f, **kwargs)
        if "sort" in op and op["sort"] is not None:
            sort_doc = _decode_doc(op["sort"])
            # Preserve order: dict iteration order is insertion order in Py3.7+.
            sort_list = [(k, int(v)) for k, v in sort_doc.items()]
            cur = cur.sort(sort_list)
        if "skip" in op and op["skip"] is not None:
            cur = cur.skip(int(op["skip"]))
        if "limit" in op and op["limit"] is not None:
            cur = cur.limit(int(op["limit"]))
        docs = list(cur)
        return {"documents": docs, "count": len(docs)}

    if name == "updateOne":
        f = _decode_doc(op.get("filter"))
        u = _decode_update(op.get("update"))
        kwargs = {}
        if op.get("upsert") is not None:
            kwargs["upsert"] = bool(op["upsert"])
        if op.get("array_filters"):
            kwargs["array_filters"] = [_decode_doc(d) for d in op["array_filters"]]
        res = coll.update_one(f, u, **kwargs)
        data: dict[str, Any] = {
            "matched_count": res.matched_count,
            "modified_count": res.modified_count,
        }
        if res.upserted_id is not None:
            data["upserted_id"] = res.upserted_id
        return data

    if name == "updateMany":
        f = _decode_doc(op.get("filter"))
        u = _decode_update(op.get("update"))
        kwargs = {}
        if op.get("upsert") is not None:
            kwargs["upsert"] = bool(op["upsert"])
        if op.get("array_filters"):
            kwargs["array_filters"] = [_decode_doc(d) for d in op["array_filters"]]
        res = coll.update_many(f, u, **kwargs)
        data = {
            "matched_count": res.matched_count,
            "modified_count": res.modified_count,
        }
        if res.upserted_id is not None:
            data["upserted_id"] = res.upserted_id
        return data

    if name == "replaceOne":
        f = _decode_doc(op.get("filter"))
        r = _decode_doc(op.get("replacement"))
        kwargs = {}
        if op.get("upsert") is not None:
            kwargs["upsert"] = bool(op["upsert"])
        res = coll.replace_one(f, r, **kwargs)
        data = {
            "matched_count": res.matched_count,
            "modified_count": res.modified_count,
        }
        if res.upserted_id is not None:
            data["upserted_id"] = res.upserted_id
        return data

    if name == "deleteOne":
        f = _decode_doc(op.get("filter"))
        res = coll.delete_one(f)
        return {"deleted_count": res.deleted_count}

    if name == "deleteMany":
        f = _decode_doc(op.get("filter"))
        res = coll.delete_many(f)
        return {"deleted_count": res.deleted_count}

    if name == "countDocuments":
        f = _decode_doc(op.get("filter"))
        n = coll.count_documents(f)
        return {"count": n}

    if name == "aggregate":
        pipeline = [_decode_doc(s) for s in op["pipeline"]]
        cur = coll.aggregate(pipeline)
        docs = list(cur)
        return {"documents": docs, "count": len(docs)}

    if name == "bulkWrite":
        models: list = []
        inserted_ids: dict[str, Any] = {}
        for i, sub in enumerate(op["operations"]):
            model, inserted_id = _bulk_model(sub)
            models.append(model)
            if inserted_id is not None:
                inserted_ids[str(i)] = inserted_id
        ordered = op.get("ordered", True)
        res = coll.bulk_write(models, ordered=bool(ordered))
        upserted_raw = res.upserted_ids or {}
        upserted_ids = {str(k): v for k, v in upserted_raw.items()}
        return {
            "inserted_count": res.inserted_count,
            "matched_count": res.matched_count,
            "modified_count": res.modified_count,
            "deleted_count": res.deleted_count,
            "upserted_count": res.upserted_count,
            "inserted_ids": inserted_ids,
            "upserted_ids": upserted_ids,
        }

    raise ValueError(f"unknown op {name!r}")


# -----------------------------------------------------------------------------
# Flask app
# -----------------------------------------------------------------------------


def _json_response(body: str, status: int = 200) -> Response:
    return Response(body, status=status, mimetype="application/json; charset=utf-8")


def _json_error(status: int, code: str, message: str) -> Response:
    return _json_response(
        ejson_dumps({"error": {"code": code, "message": message}}), status=status
    )


def create_app() -> Flask:
    uri = os.environ.get("MONGODB_URI")
    if not uri:
        sys.stderr.write("MONGODB_URI is required\n")
        sys.exit(1)

    client: MongoClient = MongoClient(uri)
    # Best-effort startup ping (don't fail startup on transient).
    try:
        client.admin.command("ping")
    except Exception as e:  # noqa: BLE001
        sys.stderr.write(f"startup ping failed (continuing): {e}\n")

    # Cached health: ping at most every 5 seconds.
    health_lock = threading.Lock()
    health_state = {"ts": 0.0, "ok": False, "detail": ""}

    app = Flask(__name__)

    @app.after_request
    def _set_ct(resp: Response) -> Response:
        # Ensure every response advertises charset=utf-8.
        ct = resp.headers.get("Content-Type", "")
        if ct.startswith("application/json") and "charset" not in ct:
            resp.headers["Content-Type"] = "application/json; charset=utf-8"
        return resp

    # ---- /v1/ops ----
    @app.route("/v1/ops", methods=["POST"])
    def handle_ops() -> Response:
        # Limit body to 16 MiB.
        raw = request.get_data(cache=False)
        if len(raw) > 16 * 1024 * 1024:
            return _json_error(413, "BAD_REQUEST", "request body too large")
        try:
            req_obj = ejson_loads(raw)
        except Exception as e:  # noqa: BLE001
            return _json_error(400, "SCHEMA_VIOLATION", f"invalid JSON: {e}")
        if not isinstance(req_obj, dict):
            return _json_error(400, "SCHEMA_VIOLATION", "request body must be an object")
        database = req_obj.get("database")
        if database is None:
            return _json_error(400, "MISSING_FIELD", "missing required field 'database'")
        if not isinstance(database, str) or database == "":
            return _json_error(400, "SCHEMA_VIOLATION", "`database` must be a non-empty string")
        ops = req_obj.get("ops")
        if ops is None:
            return _json_error(400, "MISSING_FIELD", "missing required field 'ops'")
        if not isinstance(ops, list):
            return _json_error(400, "SCHEMA_VIOLATION", "`ops` must be an array")
        if len(ops) == 0:
            return _json_error(400, "EMPTY_OPS", "ops must be a non-empty array")
        for i, op in enumerate(ops):
            try:
                _validate_op(op, False)
            except ValidationError as e:
                return _json_error(400, e.code, f"ops[{i}]: {e.message}")

        db = client[database]
        results: list[dict[str, Any]] = []
        for op in ops:
            name = op["name"]
            try:
                data = execute_op(db, op)
                results.append({"op": name, "ok": True, "data": data})
            except PyMongoError as exc:
                err = classify_error(exc)
                results.append({"op": name, "ok": False, "error": err})
            except (ValueError, TypeError) as exc:
                # Driver-input errors caught before sending (e.g. bad BSON).
                err = classify_error(exc)
                results.append({"op": name, "ok": False, "error": err})

        body = ejson_dumps({"results": results})
        return _json_response(body, status=200)

    # ---- /v1/admin/reset ----
    @app.route("/v1/admin/reset", methods=["POST"])
    def handle_reset() -> Response:
        raw = request.get_data(cache=False)
        try:
            req_obj = ejson_loads(raw)
        except Exception as e:  # noqa: BLE001
            return _json_error(400, "SCHEMA_VIOLATION", f"invalid JSON: {e}")
        if not isinstance(req_obj, dict):
            return _json_error(400, "SCHEMA_VIOLATION", "request body must be an object")
        databases = req_obj.get("databases")
        if not isinstance(databases, list) or len(databases) == 0:
            return _json_error(400, "EMPTY_OPS", "databases must be a non-empty array")
        for name in databases:
            if not isinstance(name, str) or name == "":
                return _json_error(400, "SCHEMA_VIOLATION", "database name must be a non-empty string")
            if name in ("admin", "local", "config"):
                return _json_error(400, "BAD_REQUEST", f"refusing to drop {name!r}")
        dropped: list[str] = []
        for name in databases:
            try:
                client.drop_database(name)
            except PyMongoError as exc:
                return _json_error(500, "BAD_REQUEST", f"drop {name}: {exc}")
            dropped.append(name)
        return _json_response(ejson_dumps({"dropped": dropped}), status=200)

    # ---- /v1/info ----
    @app.route("/v1/info", methods=["GET"])
    def handle_info() -> Response:
        info = {
            "driver": "pymongo",
            "driver_version": pymongo_version(),
            "language": "python",
            "language_version": platform.python_version(),
            "spec_version": "1.0.0",
        }
        return _json_response(ejson_dumps(info), status=200)

    # ---- /v1/health ----
    @app.route("/v1/health", methods=["GET"])
    def handle_health() -> Response:
        now = time.time()
        with health_lock:
            stale = (now - health_state["ts"]) > 5.0
        if stale:
            try:
                client.admin.command("ping")
                with health_lock:
                    health_state["ts"] = now
                    health_state["ok"] = True
                    health_state["detail"] = ""
            except Exception as exc:  # noqa: BLE001
                with health_lock:
                    health_state["ts"] = now
                    health_state["ok"] = False
                    health_state["detail"] = str(exc)
        with health_lock:
            ok = health_state["ok"]
            detail = health_state["detail"]
        if ok:
            return _json_response(ejson_dumps({"ok": True}), status=200)
        return _json_response(
            ejson_dumps({"ok": False, "detail": detail}), status=503
        )

    return app


def pymongo_version() -> str:
    try:
        import pymongo

        return pymongo.__version__
    except Exception:  # noqa: BLE001
        return "unknown"


app = create_app()


if __name__ == "__main__":
    port = int(os.environ.get("PORT", "8080"))
    app.run(host="0.0.0.0", port=port, threaded=True)
