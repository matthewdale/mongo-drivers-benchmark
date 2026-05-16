# MongoDB Drivers Load-Test HTTP API

**Spec version:** `1.0.0`

This document specifies an HTTP API implemented by one small service per MongoDB driver language. Every service exposes the same endpoints, accepts the same request shapes, and returns semantically equivalent responses, so a single load driver can exercise every driver uniformly through a realistic application boundary.

The schemas for every endpoint, op variant, and response variant live in [`openapi.yaml`](./openapi.yaml). This document is authoritative for **semantics**, conformance language, the error-code mapping table, determinism rules, and the worked canonical scenario; `openapi.yaml` is authoritative for **shape**.

## 1. Conformance language

The key words MUST, MUST NOT, REQUIRED, SHALL, SHALL NOT, SHOULD, SHOULD NOT, RECOMMENDED, MAY, and OPTIONAL in this document are to be interpreted as described in [RFC 2119](https://www.rfc-editor.org/rfc/rfc2119).

A "service" is a process that implements this spec. A "client" is anything that calls a service. The "validator" is a special-purpose client that asserts conformance to this spec.

## 2. Scope

### In scope (v1.0)

- Core CRUD against a single MongoDB cluster: `insertOne`, `insertMany`, `find`, `updateOne`, `updateMany`, `replaceOne`, `deleteOne`, `deleteMany`, `countDocuments`, `aggregate`, `bulkWrite`.
- Many CRUD ops per HTTP request, executed sequentially.
- Normalized cross-driver error categorization.
- Validator-only state reset.

### Out of scope (v1.0)

- Transactions, change streams, GridFS, index admin, server-side JavaScript.
- Per-request read concern, write concern, read preference, or session.
- Per-op database override (one `database` per request).
- HTTP-client authentication or TLS termination at the service.
- Cursor pagination across HTTP responses.

These MAY appear in a later spec version.

## 3. Endpoint surface

| Method | Path                | Purpose                                                          |
|--------|---------------------|------------------------------------------------------------------|
| POST   | `/v1/ops`           | Execute an ordered list of CRUD ops. Hot path.                   |
| POST   | `/v1/admin/reset`   | Drop the named databases. Validator-only.                        |
| GET    | `/v1/info`          | Service identification.                                          |
| GET    | `/v1/health`        | Liveness with a real cluster ping.                               |

A service MUST expose exactly these four endpoints under the `/v1` prefix and MUST NOT expose load-bearing endpoints outside this prefix in v1. A service MAY expose additional debug endpoints (e.g. `/debug/pprof`); clients MUST NOT depend on them.

## 4. Connection model

A service MUST be configurable with a MongoDB connection string via the environment variable `MONGODB_URI` and MUST connect to that single cluster at startup. It MUST maintain one driver client for the lifetime of the process and reuse it for every request. There is no per-request URI in v1.

A service SHOULD verify cluster reachability at startup (e.g. a `ping` against the admin database) and MAY refuse to start if the initial ping fails. Once running, transient failures MUST surface per-op as described in §7, not as service startup failures.

## 5. Transport and encoding

- All requests and responses MUST use `Content-Type: application/json; charset=utf-8`.
- All JSON MUST be valid per RFC 8259 and MUST be UTF-8 encoded.
- A service SHOULD reject requests larger than 16 MiB with `413 Payload Too Large`. Clients SHOULD NOT rely on requests larger than this being accepted.

### 5.1 Extended JSON

All BSON-typed values in requests and responses MUST be representable in [MongoDB Extended JSON v2](https://www.mongodb.com/docs/manual/reference/mongodb-extended-json/). BSON types that have no lossless plain-JSON equivalent (ObjectId, Decimal128, Binary, DateTime, Timestamp, Regex, MinKey, MaxKey) MUST use the canonical `{$...}` envelope; numeric BSON types that have a lossless plain-JSON representation (Int32, Int64, Double) MUST be emitted as plain JSON numbers (this is the **relaxed** form for numerics, and it is required so generic JSON consumers can decode response bodies without an Extended-JSON-aware library).

Concretely, the required output form is:

- ObjectId: `{"$oid": "<24-hex>"}`
- 32-bit int: plain JSON number (e.g. `42`)
- 64-bit int: plain JSON number when it fits losslessly in IEEE-754 (i.e. `|n| <= 2^53`); otherwise `{"$numberLong": "<int>"}`
- Double: plain JSON number; the special values `Infinity`, `-Infinity`, `NaN` MUST use `{"$numberDouble": "Infinity" | "-Infinity" | "NaN"}`
- Decimal128: `{"$numberDecimal": "<dec>"}`
- Binary: `{"$binary": {"base64": "<b64>", "subType": "<2-hex>"}}`
- DateTime (ms since epoch): `{"$date": {"$numberLong": "<int>"}}`
- Timestamp: `{"$timestamp": {"t": <uint32>, "i": <uint32>}}`
- Regex: `{"$regularExpression": {"pattern": "<str>", "options": "<str>"}}`
- MinKey / MaxKey: `{"$minKey": 1}` / `{"$maxKey": 1}`

Strings, booleans, and `null` appear as their plain JSON forms. On input, services MUST accept both canonical and relaxed forms for every numeric BSON type; e.g. both `42` and `{"$numberInt": "42"}` MUST be treated identically.

> **Note:** Earlier drafts of this spec required fully canonical Extended JSON for numerics. This was changed in v1.0 because (a) the canonical envelope adds no information beyond the request's existing knowledge of expected BSON shapes and (b) it breaks downstream JSON consumers (including the validator's `documents[]` decoders) that decode numeric fields as native JSON numbers. The non-numeric BSON types are unaffected; they still require the canonical envelope.

## 6. `POST /v1/ops`

### 6.1 Request

The full request schema is `OpsRequest` in [`openapi.yaml`](./openapi.yaml). In prose:

- `database` (string, required): the database name applied to every op in this request.
- `ops` (array, required, non-empty): ordered list of CRUD ops.

Each op is an object with a `name` field (the discriminator) and a `collection` field, plus op-specific arguments. The full per-op argument set is enumerated in `openapi.yaml`; a high-level summary:

| Op                | Required args                       | Optional args                            |
|-------------------|-------------------------------------|------------------------------------------|
| `insertOne`       | `document`                          | —                                        |
| `insertMany`      | `documents[]`                       | `ordered` (default `true`)               |
| `find`            | `filter`                            | `projection`, `sort`, `skip`, `limit`    |
| `updateOne`       | `filter`, `update`                  | `upsert`, `array_filters`                |
| `updateMany`      | `filter`, `update`                  | `upsert`, `array_filters`                |
| `replaceOne`      | `filter`, `replacement`             | `upsert`                                 |
| `deleteOne`       | `filter`                            | —                                        |
| `deleteMany`      | `filter`                            | —                                        |
| `countDocuments`  | `filter`                            | —                                        |
| `aggregate`       | `pipeline[]`                        | —                                        |
| `bulkWrite`       | `operations[]`                      | `ordered` (default `true`)               |

`bulkWrite.operations[]` contains nested ops with the same arg shapes as the top-level variants but no `collection` field (the bulk write is single-collection by construction).

### 6.2 Sequencing

A service MUST execute the ops in `ops` in order. Ops MUST run within a single worker (thread/goroutine/event-loop turn) and MUST NOT share implicit state across ops (no implicit session, no implicit cursor reuse).

A service MUST NOT short-circuit on op failure. If op N fails, ops N+1..end MUST still be attempted. Each op produces an independent entry in the response `results` array, in input order.

A service MUST NOT execute the ops concurrently in v1.

### 6.3 Read exhaustion

For `find` and `aggregate`, a service MUST exhaust the cursor and inline all matched documents into the response `data.documents`. There is no cursor pagination across HTTP responses in v1. Clients SHOULD set `limit` (or use selective `filter`s, or use `aggregate` with a `$limit` stage) to bound response size.

A service SHOULD reject reads that produce a response body larger than 16 MiB with a per-op `INTERNAL` error. (A future spec version MAY introduce explicit cursor handling.)

### 6.4 Response

The full response schema is `OpsResponse` in `openapi.yaml`. The HTTP status MUST be:

- `200 OK` whenever the request was successfully decoded and every op was dispatched to the driver, regardless of per-op outcomes;
- `400 Bad Request` if the request body fails schema validation, contains an unknown op name, or has an empty `ops` array (see `RequestError`);
- `5xx` only for internal failures (panic, client-pool collapse, etc.).

The response body is `{ "results": [<OpResult>, ...] }`. The length of `results` MUST equal the length of the request's `ops`, and entries MUST be in the same order. Each `OpResult` is one of:

- Success: `{ "op": "<name>", "ok": true, "data": <per-op data> }`
- Failure: `{ "op": "<name>", "ok": false, "error": { "code": "<ErrorCode>", "message": "<str>", "server_code": <int>? } }`

Per-op success `data` shapes are summarized below; full schemas live in `openapi.yaml`.

| Op                                | `data` fields                                                                                        |
|-----------------------------------|------------------------------------------------------------------------------------------------------|
| `insertOne`                       | `inserted_id`                                                                                        |
| `insertMany`                      | `inserted_ids[]`, `inserted_count`                                                                   |
| `find`, `aggregate`               | `documents[]`, `count`                                                                               |
| `updateOne`, `updateMany`, `replaceOne` | `matched_count`, `modified_count`, `upserted_id?`                                              |
| `deleteOne`, `deleteMany`         | `deleted_count`                                                                                      |
| `countDocuments`                  | `count`                                                                                              |
| `bulkWrite`                       | `inserted_count`, `matched_count`, `modified_count`, `deleted_count`, `upserted_count`, `inserted_ids{index → id}`, `upserted_ids{index → id}` |

`inserted_id`, `inserted_ids[]`, `upserted_id`, and the `inserted_ids` / `upserted_ids` maps under `bulkWrite` MUST be emitted as Extended JSON v2 values of the actual `_id` BSON type per §5.1 (canonical envelope for non-numeric types, plain JSON for numeric types). For the `bulkWrite` maps, keys MUST be decimal-string sub-op indices (e.g. `"0"`, `"1"`).

For `bulkWrite`, every sub-op of name `insertOne` MUST produce an entry in `inserted_ids` keyed by the sub-op's index in `operations[]`. Because some drivers do not expose driver-generated `_id`s through their native `BulkWriteResult`, services MAY pre-assign a client-side `_id` (typically a fresh ObjectId) on any `insertOne` sub-op whose document does not already supply one. The pre-assigned value MUST then appear in the response `inserted_ids` map. Services MUST NOT mutate sub-ops whose `document._id` is already set.

For `bulkWrite`, every sub-op that performs an upserting insert (an `updateOne` / `updateMany` / `replaceOne` with `upsert: true` that inserted a new document) MUST produce an entry in `upserted_ids` keyed by the sub-op's index, with the value taken from the driver's bulk-write result.

## 7. Error mapping

A service MUST classify every driver exception arising from a dispatched op into exactly one of the eight `ErrorCode` values below. The mapping rules are:

| `code`               | Triggered by                                                                                          | Typical MongoDB `server_code` |
|----------------------|-------------------------------------------------------------------------------------------------------|-------------------------------|
| `DUPLICATE_KEY`      | Unique-index violation on insert / upsert / update.                                                   | 11000, 11001                  |
| `WRITE_CONFLICT`     | Concurrent-write conflict (typically WiredTiger).                                                     | 112                           |
| `TIMEOUT`            | Operation timeout, socket read/write timeout, server `MaxTimeMSExpired`.                              | 50, 89, 262                   |
| `NETWORK`            | Connection failures, topology errors, no-primary-found, broken sockets — driver-recognized as transport-level. | (none / driver-internal)      |
| `AUTH`               | Authentication or authorization failure.                                                              | 13, 18, 8000 (variants)       |
| `NOT_FOUND`          | Namespace not found, index not found when required by the op.                                         | 26, 27                        |
| `INVALID_ARGUMENT`   | Driver rejected the op before sending it (bad BSON, illegal update doc, malformed filter) **or** server returned a command-level argument error. | 2, 9, 14, 40, 51, 72, 73 |
| `INTERNAL`           | Anything not covered above. The driver error text MUST appear verbatim in `message` and `server_code` MUST be included if the driver exposes one. | various |

Rules:

1. The mapping MUST be deterministic: given the same driver exception, a service MUST always produce the same `code`. Implementations SHOULD use the MongoDB server error code first; if absent, the driver's exception class hierarchy second; if neither classifies, `INTERNAL`.
2. `message` MUST be the driver's error text exactly as the driver produced it. Services MUST NOT translate, prefix, or otherwise rewrite it.
3. `server_code` MUST be included whenever the driver exposes one; it MUST be omitted otherwise (not set to `0` or `null`).
4. A service MUST NOT throw an HTTP-level error for a driver-level failure. Driver failures are always per-op results with `ok: false`.

## 8. `POST /v1/admin/reset`

Request:

```json
{ "databases": ["loadtest", "loadtest_2"] }
```

A service MUST drop each named database in input order and MUST return:

```json
{ "dropped": ["loadtest", "loadtest_2"] }
```

`databases` MUST be a non-empty array of non-empty strings; otherwise the service MUST return `400` with a `RequestError` body. A service SHOULD refuse to drop the `admin`, `local`, and `config` databases and SHOULD return `400` if asked to.

This endpoint exists for the validator. Production load drivers SHOULD NOT call it during a load test.

## 9. `GET /v1/info`

```json
{
  "driver": "mongo-go-driver",
  "driver_version": "2.6.0",
  "language": "go",
  "language_version": "1.22.0",
  "spec_version": "1.0.0"
}
```

`language` MUST be one of `go`, `python`, `node`, `java`, `php` in v1. `spec_version` MUST match the version this document declares (`1.0.0`).

## 10. `GET /v1/health`

`200` with `{"ok": true}` when a successful `ping` was performed within the last 5 seconds (services SHOULD ping on demand and cache for up to 5 seconds). `503` with `{"ok": false, "detail": "<str>"}` when the most recent ping failed.

## 11. Determinism rules

The validator compares responses across implementations to assert semantic equivalence. To make that possible, the following rules apply.

### 11.1 What the validator normalizes (and therefore what MAY vary across implementations)

The validator MUST strip or relax the following before comparison:

1. **Server-generated `_id` values.** When an `insertOne` / `insertMany` / `bulkWrite` did not supply an `_id`, the driver-generated ObjectId is non-deterministic. The validator MUST replace such values with a stable placeholder before comparison and MUST verify the replaced values appear consistently in any subsequent `find` results in the same scenario.
2. **Error `message` text.** Driver error messages vary in wording. The validator MUST compare only `error.code` (and, if present in both responses, `error.server_code`) for cross-implementation equivalence. `message` is informational.
3. **`bulkWrite.data.upserted_ids` / `inserted_ids` ordering.** These are maps keyed by sub-op index; the JSON object key order MUST NOT be relied upon by the validator.

### 11.2 What MUST be deterministic

After the normalizations in §11.1, two services running the same scenario against equivalent cluster state MUST produce byte-equal canonical-JSON-encoded responses. In particular:

1. `count`, `matched_count`, `modified_count`, `deleted_count`, `inserted_count`, `upserted_count` MUST match exactly.
2. For `find` and `aggregate`, `documents[]` MUST match exactly when the request specified a total order (i.e. a `sort` that resolves ties or guarantees uniqueness, such as `sort: {_id: 1}`). The validator's reference scenarios MUST always include such a `sort`.
3. `error.code` MUST match for any op that fails on both implementations.
4. Canonical Extended JSON encoding of every BSON value MUST be byte-identical for the same BSON value.

### 11.3 What clients SHOULD do for cross-implementation comparability

- Always supply a `sort` on `find` / `aggregate` whose results will be compared.
- Always supply client-side `_id`s for inserts whose result documents will be compared. (`{"$oid": "..."}` is the most portable.)
- Avoid relying on insertion order for `find` without `sort`; drivers don't guarantee it identically.

## 12. Worked canonical scenario

This scenario MUST validate against `openapi.yaml` (it is embedded there as `examples.CanonicalOpsRequest` and `examples.CanonicalOpsResponse`).

**Setup.** Validator first calls `POST /v1/admin/reset` with `{"databases": ["loadtest"]}`.

**Request.** `POST /v1/ops`:

```json
{
  "database": "loadtest",
  "ops": [
    {
      "name": "insertOne",
      "collection": "users",
      "document": {
        "_id": {"$oid": "64b1f2a4d3e2c1b0a9876543"},
        "name": "Alice",
        "n": 1
      }
    },
    {
      "name": "find",
      "collection": "users",
      "filter": {"n": {"$gte": 0}},
      "sort": {"_id": 1},
      "limit": 100
    },
    {
      "name": "updateMany",
      "collection": "users",
      "filter": {"n": 1},
      "update": {"$inc": {"n": 1}}
    }
  ]
}
```

**Response.** `200 OK`:

```json
{
  "results": [
    {
      "op": "insertOne",
      "ok": true,
      "data": {"inserted_id": {"$oid": "64b1f2a4d3e2c1b0a9876543"}}
    },
    {
      "op": "find",
      "ok": true,
      "data": {
        "documents": [
          {
            "_id": {"$oid": "64b1f2a4d3e2c1b0a9876543"},
            "name": "Alice",
            "n": 1
          }
        ],
        "count": 1
      }
    },
    {
      "op": "updateMany",
      "ok": true,
      "data": {"matched_count": 1, "modified_count": 1}
    }
  ]
}
```

After applying the §11.1 normalizations (none are needed here because the test supplied a client-side `_id`), every conforming service MUST produce this exact response.

## 13. Versioning

The spec is versioned with semver: `MAJOR.MINOR.PATCH`.

- **MAJOR** bumps for any change that could cause a previously-conforming service to be rejected by a new validator, or a previously-valid client request to be rejected by a new service. Examples: removing or renaming an endpoint, adding a required field, changing an error code's mapping, narrowing a determinism guarantee.
- **MINOR** bumps for additions that preserve backward compatibility: new optional request fields, new ops, new endpoints, additional error codes that only apply to new ops.
- **PATCH** bumps for clarifications and editorial fixes that do not change conforming behavior.

A service's `/v1/info.spec_version` MUST be the version of this document the service targets. Clients SHOULD verify it matches the version they were built against.

The `/v1` URL prefix MUST be bumped to `/v2` only on a MAJOR change that cannot be expressed as additions under `/v1`.

## 14. Self-review checklist

Before declaring v1.0 ready:

- [ ] Every op in §6.1 has a request schema in `openapi.yaml`.
- [ ] Every op in §6.4 has a success-result schema in `openapi.yaml`.
- [ ] Every `ErrorCode` in §7 appears in `openapi.yaml`'s `ErrorCode` enum.
- [ ] Every endpoint in §3 appears as a path in `openapi.yaml`.
- [ ] The §12 canonical scenario validates against `openapi.yaml`.
- [ ] No `TBD`, `TODO`, or unresolved bracketed placeholders remain.
- [ ] §11 lists every field the validator must normalize and every guarantee it can rely on.
