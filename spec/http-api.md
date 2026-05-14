# MongoDB Driver Benchmark HTTP API

- Status: Draft
- Minimum Server Version: 4.4 (8.0 for `clientBulkWrite`)
- Current Version: 0.1.0

______________________________________________________________________

## Abstract

This document specifies an HTTP API that benchmark services implement so a
single, language-agnostic runner can measure MongoDB driver performance
consistently across language ecosystems. Each conforming service wraps one
MongoDB driver (e.g. Node.js, Java, Python, Go, PHP) and exposes a uniform
set of HTTP endpoints — **one per driver CRUD command**. A separate benchmark
runner — the **CLI runner** — issues timed requests, composes workloads from
those primitive commands, and reports results. The runner times each HTTP
round-trip, so reported numbers include HTTP framing in addition to driver
work; this is intentional and reflects a realistic application path.

The keywords "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD",
"SHOULD NOT", "RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be
interpreted as described in RFC 2119.

## Motivation

Benchmark coverage across MongoDB drivers is inconsistent. The existing
[MongoDB Driver Performance Benchmark spec](https://github.com/mongodb/specifications/blob/master/source/benchmarking/benchmarking.md)
requires each driver team to implement its own benchmark harness, with
several drawbacks:

1. Some drivers do not implement every benchmark, or implement them
   inconsistently, producing missing or misleading data.
2. Adding a benchmark means coordinating work across every driver team.
3. The harness runs the driver in isolation, not inside a realistic
   application like a web service.

This spec addresses all three by defining a single HTTP contract that every
language implements. The API surface mirrors the driver's CRUD command API
directly, so the service is a thin shim over the driver and the runner owns
workload composition. Adding or changing a benchmark is a runner-side change
with no service changes required, and because the runner drives real HTTP
requests, results reflect the driver as it is actually used.

## Definitions

**Service**: A long-running HTTP server, written in one language, that wraps
a single MongoDB driver and implements this spec.

**CLI runner** (or "runner"): A separate program that issues HTTP requests
to a service, measures round-trip latency, composes workloads from command
primitives, and aggregates results. The runner is out of scope for this
document.

**Command**: One of the driver CRUD operations exposed by this API
(`find`, `findOne`, `insertOne`, `insertMany`, `updateOne`, `updateMany`,
`deleteOne`, `deleteMany`, `bulkWrite`, `clientBulkWrite`). Each command has
a dedicated endpoint.

**Workload**: A higher-level benchmark scenario (e.g. "10k small-doc inserts
against an empty collection") composed by the runner from one or more
commands. Workloads live in the runner, not the service.

**Iteration**: A single timed unit of work — typically one HTTP request and
response. The runner samples many iterations to compute percentiles.

## Specification

### Transport and encoding

Conforming services MUST accept HTTP/1.1 over TCP. Support for HTTP/2 is
OPTIONAL. TLS is OPTIONAL and out of scope for v1; deployments are expected
to co-locate the runner and the service on a trusted network.

Request and response bodies MUST use `Content-Type: application/json` and
MUST contain valid JSON. Services MUST parse incoming JSON documents into the
driver's native document type and serialize driver results back to JSON in
responses.

Error responses MUST use a non-2xx status code and SHOULD have the body
shape:

```json
{ "error": "<machine-readable code>", "message": "<human-readable description>" }
```

Error codes services SHOULD use:

| HTTP status | `error` value     | Meaning                                   |
|-------------|-------------------|-------------------------------------------|
| 400         | `invalid_request` | Body could not be parsed as JSON, or required field missing. |
| 404         | `not_found`       | Requested resource (e.g. a document) does not exist when the command requires one. |
| 500         | `driver_error`    | Underlying driver raised an unexpected error. The `message` SHOULD include the driver's error string. |
| 503         | `not_ready`       | Service is starting and the driver is not yet connected. |

### Service lifecycle

Services MUST read the MongoDB connection string from the `MONGODB_URI`
environment variable on startup. Services MUST bind to the TCP port given
by the `PORT` environment variable. Services SHOULD default `PORT` to
`8080` if unset.

Services MUST establish a driver connection (eagerly or lazily) such that
`GET /health` returns `200` once the driver is ready. Before then, services
MUST return `503 not_ready` for command endpoints. A connection-pool warmup
ping at startup is RECOMMENDED so the first iteration is not skewed by
connection establishment.

### Common request fields

Every command endpoint accepts the following top-level fields, except where
noted per endpoint:

| Field        | Type   | Required | Description |
|--------------|--------|----------|-------------|
| `database`   | string | NO       | Database name. Defaults to `perftest`. |
| `collection` | string | YES      | Collection name. NOT present on `/clientBulkWrite`. |

Services MUST scope every command to the database and collection given in
the request. Services MUST NOT cache driver `Collection` handles in a way
that prevents per-request database/collection routing.

### Control endpoints

#### `GET /health`

Returns the service's readiness and the driver it wraps.

Response (200):

```json
{
  "status": "ok",
  "driver": "<driver name, e.g. \"mongo-go-driver\">",
  "driverVersion": "<semver, e.g. \"1.15.0\">",
  "language": "<go|node|python|java|php>",
  "languageVersion": "<runtime version>"
}
```

Services MUST return `200` only when the driver has successfully connected
to the deployment named by `MONGODB_URI`. Otherwise they MUST return `503`.

### Command endpoints

For every command endpoint below, the **timed operation** is everything
between the service receiving the request and sending the response — i.e.
parsing the request body, performing the driver call, and serializing the
response. Services MUST NOT perform out-of-band driver work for a command
outside of the corresponding endpoint.

All option fields documented below are OPTIONAL. Services MUST forward
provided options to the driver unchanged; services MUST NOT add their own
defaults to options the caller did not provide.

#### `POST /find`

Driver call: `collection.find(filter, options).toArray()` (or equivalent
cursor-to-array). Services MUST iterate the cursor to exhaustion.

Request body:

```json
{
  "database":   "perftest",
  "collection": "coll0",
  "filter":     { },
  "options": {
    "limit":      0,
    "skip":       0,
    "sort":       { "_id": 1 },
    "projection": { "_id": 1 },
    "batchSize":  100
  }
}
```

Response (200):

```json
{
  "documents": [ { "...": "..." }, ... ],
  "count":     <int>
}
```

`count` MUST equal `documents.length`.

#### `POST /findOne`

Driver call: `collection.findOne(filter, options)` (or
`find(filter).limit(1).next()`).

Request body:

```json
{
  "database":   "perftest",
  "collection": "coll0",
  "filter":     { "_id": "..." },
  "options": {
    "sort":       { "_id": 1 },
    "projection": { "_id": 1 }
  }
}
```

Response (200):

```json
{ "document": { "...": "..." } }
```

If no document matches, services MUST return `200` with `{"document": null}`,
not `404`. `404` is reserved for cases where a command's contract requires
an existing target (e.g. a future drop-by-id endpoint).

#### `POST /insertOne`

Driver call: `collection.insertOne(document, options)`.

Request body:

```json
{
  "database":   "perftest",
  "collection": "coll0",
  "document":   { "...": "..." },
  "options": {
    "bypassDocumentValidation": false
  }
}
```

Response (200):

```json
{ "insertedId": "<id>" }
```

#### `POST /insertMany`

Driver call: `collection.insertMany(documents, options)`.

Request body:

```json
{
  "database":   "perftest",
  "collection": "coll0",
  "documents":  [ { "...": "..." }, ... ],
  "options": {
    "ordered":                 true,
    "bypassDocumentValidation": false
  }
}
```

Response (200):

```json
{ "insertedCount": <int> }
```

Services MAY include `"insertedIds": { "0": <id>, ... }` in the response.
The runner MUST NOT depend on `insertedIds` being present.

#### `POST /updateOne`

Driver call: `collection.updateOne(filter, update, options)`.

Request body:

```json
{
  "database":   "perftest",
  "collection": "coll0",
  "filter":     { "_id": "..." },
  "update":     { "$set": { "x": 1 } },
  "options": {
    "upsert":       false,
    "arrayFilters": [ ]
  }
}
```

Response (200):

```json
{
  "matchedCount":  <int>,
  "modifiedCount": <int>,
  "upsertedId":    null
}
```

`upsertedId` is `null` when no upsert occurred; otherwise it MUST be the
inserted `_id` serialized as JSON.

#### `POST /updateMany`

Identical request shape to `/updateOne`. Driver call:
`collection.updateMany(filter, update, options)`.

Response (200):

```json
{
  "matchedCount":  <int>,
  "modifiedCount": <int>,
  "upsertedId":    null
}
```

#### `POST /deleteOne`

Driver call: `collection.deleteOne(filter, options)`.

Request body:

```json
{
  "database":   "perftest",
  "collection": "coll0",
  "filter":     { "_id": "..." }
}
```

Response (200):

```json
{ "deletedCount": <int> }
```

#### `POST /deleteMany`

Identical request shape to `/deleteOne`. Driver call:
`collection.deleteMany(filter, options)`. Response shape matches
`/deleteOne`.

Runners MAY use `POST /deleteMany` with `filter: {}` to clear a collection
between iterations. See [Workload orchestration](#workload-orchestration).

#### `POST /bulkWrite`

Driver call: `collection.bulkWrite(operations, options)`. All operations in
the request MUST target the same collection (the one named at the top level).

Request body:

```json
{
  "database":   "perftest",
  "collection": "coll0",
  "operations": [
    { "insertOne": { "document": { "...": "..." } } },
    { "updateOne": { "filter":   { "_id": "..." },
                     "update":   { "$set": { "x": 1 } },
                     "upsert":   false } },
    { "updateMany":{ "filter":   { "tag": "a" },
                     "update":   { "$inc": { "n": 1 } } } },
    { "replaceOne":{ "filter":   { "_id": "..." },
                     "replacement": { "...": "..." } } },
    { "deleteOne": { "filter":   { "_id": "..." } } },
    { "deleteMany":{ "filter":   { "tag": "b" } } }
  ],
  "options": {
    "ordered":                 true,
    "bypassDocumentValidation": false
  }
}
```

Services MUST preserve operation order in the driver call. Services MUST
accept the six operation kinds shown above (`insertOne`, `updateOne`,
`updateMany`, `replaceOne`, `deleteOne`, `deleteMany`).

Response (200):

```json
{
  "insertedCount":  <int>,
  "matchedCount":   <int>,
  "modifiedCount":  <int>,
  "deletedCount":   <int>,
  "upsertedCount":  <int>
}
```

#### `POST /clientBulkWrite`

Driver call: `client.bulkWrite(models, options)`. This is the
multi-namespace variant introduced in MongoDB 8.0; each model carries its
own `namespace`.

Top-level `database` and `collection` fields MUST NOT be present on this
endpoint. Services MUST return `400 invalid_request` if either is supplied.

Request body:

```json
{
  "models": [
    { "namespace": "perftest.a",
      "insertOne": { "document": { "...": "..." } } },
    { "namespace": "perftest.b",
      "updateOne": { "filter":   { "_id": "..." },
                     "update":   { "$set": { "x": 1 } } } },
    { "namespace": "perftest.c",
      "deleteMany":{ "filter":   { "tag": "x" } } }
  ],
  "options": {
    "ordered":                  true,
    "bypassDocumentValidation": false,
    "verboseResults":           false
  }
}
```

Each model MUST contain a `namespace` field of the form `"<db>.<coll>"` and
exactly one of `insertOne`, `updateOne`, `updateMany`, `replaceOne`,
`deleteOne`, `deleteMany`.

Response (200):

```json
{
  "insertedCount": <int>,
  "matchedCount":  <int>,
  "modifiedCount": <int>,
  "deletedCount":  <int>,
  "upsertedCount": <int>
}
```

If `options.verboseResults` is `true`, services MUST additionally include a
`"verboseResults"` field whose shape mirrors the driver's
`ClientBulkWriteResult` per-operation maps (`insertResults`, `updateResults`,
`deleteResults`), serialized as JSON.

If the underlying driver does not support `clientBulkWrite` (server <8.0 or
older driver release), the service MUST return `501 unsupported` with a
descriptive message. Runners MUST handle `501` gracefully and skip
`clientBulkWrite` workloads in that case.

### Workload orchestration

Workload composition is the runner's responsibility. There are no
service-side setup endpoints in v1; the runner uses the same command
endpoints to prepare state. Two patterns are RECOMMENDED:

- **Clear a collection**: `POST /deleteMany` with `{ "filter": {} }`.
- **Seed a collection**: `POST /insertMany` with the seed documents.

A typical benchmark iteration sequence:

1. (Once per workload) `POST /deleteMany` with `filter: {}` to clear.
2. (Once per workload) `POST /insertMany` with the seed documents.
3. (N times, timed) `POST /<command>` with the workload's payload.
4. (Between iterations, if state matters) repeat steps 1–2.

This composition belongs to the runner, not the spec. Future spec versions
MAY add admin helpers (`dropCollection`, `createIndex`) if patterns emerge
that cannot be expressed via the CRUD commands. See [Future Work](#future-work).

### Conformance rules

A conforming service:

1. MUST implement every endpoint in [Control endpoints](#control-endpoints)
   and [Command endpoints](#command-endpoints).
2. MUST accept and return valid JSON in request and response bodies.
3. MUST return the status codes and error body shape defined in
   [Transport and encoding](#transport-and-encoding).
4. MUST route each request to the `database`/`collection` it names, with
   `database` defaulting to `perftest`.
5. MUST forward request `options` to the driver unchanged.
6. MUST NOT cap concurrency below what the underlying driver supports.
7. MUST treat `MONGODB_URI` as the sole source of connection configuration.
   Services MUST NOT read connection options from request bodies, headers,
   or other env vars in v1.
8. MUST return `501 unsupported` for `/clientBulkWrite` when the driver or
   server cannot satisfy the command, rather than emulating it from
   `bulkWrite`.

Implementations MAY add endpoints beyond those defined here for debugging
(e.g. `GET /metrics`) but MUST NOT alter the request or response shape of
any endpoint defined in this spec.

## Test Plan

The CLI runner (out of scope for this document) implements a conformance
suite. Conformance tests verify, at a minimum:

1. `GET /health` returns 200 with the required body fields once
   `MONGODB_URI` points to a running deployment, and 503 before the driver
   is connected.
2. Each command endpoint produces the documented response shape for a
   well-formed request.
3. Each command endpoint returns `400 invalid_request` for a malformed
   request body or missing required field, and `400` if `database` or
   `collection` is present on `/clientBulkWrite`.
4. `database` defaults to `perftest` when omitted, and explicit
   `database`/`collection` values route correctly.
5. `/findOne` returns `{"document": null}` (not 404) when no document
   matches.
6. `/updateOne` and `/updateMany` return `upsertedId: null` when no upsert
   occurred and a non-null `_id` when `upsert: true` triggered one.
7. `/bulkWrite` preserves operation order and supports all six operation
   kinds (insertOne, updateOne, updateMany, replaceOne, deleteOne,
   deleteMany).
8. `/clientBulkWrite` accepts cross-namespace `models` and either returns
   200 with the documented body or 501 `unsupported` (never silently
   collapses to a single-collection `bulkWrite`).
9. The service tolerates N concurrent requests to the same endpoint
   without returning 5xx (smoke test for the parallel workload model).

## Design Rationale

### Why command-per-endpoint instead of workload-per-endpoint

An earlier draft of this spec exposed one endpoint per benchmark workload
(`/single-doc/insert-one/small`, `/multi-doc/find-many`, etc.), mirroring
the existing
[Driver Performance Benchmark spec](https://github.com/mongodb/specifications/blob/master/source/benchmarking/benchmarking.md).
This redesign exposes one endpoint per driver CRUD command instead.

Benefits:

- **Adding a workload is a runner-side change.** New doc shapes, batch
  sizes, or sequencing patterns do not require service edits in five
  languages.
- **The API matches what driver teams already implement.** Every
  conforming driver already exposes `find` / `insertOne` / etc. with these
  exact semantics, so service implementations are mechanical.
- **The runner becomes a workload library.** Workload definitions live in
  one place and can evolve independently of any service.

Trade-off: the runner takes on more orchestration. Workloads now require
explicit setup/teardown sequencing via the same CRUD endpoints, rather
than a single "run the small-doc benchmark" call.

### Why time the HTTP round-trip instead of the driver call

The source driver benchmarking spec measures driver work in isolation.
That model surfaces driver implementation differences cleanly but does not
reflect real applications, where the driver runs inside an HTTP server,
ORM, or queue worker. Adopting an HTTP-roundtrip timing model means every
benchmark includes the cost of an HTTP server reading a request, doing
driver work, and writing a response — which is exactly the path a real
service follows. Driver differences are still visible because HTTP framing
cost is roughly constant across languages.

### Why plain JSON instead of Extended JSON

An earlier draft of this spec used MongoDB Canonical Extended JSON v2,
which can represent BSON-specific types (`$oid`, `$date`, `$numberDecimal`,
etc.) as structured JSON objects. Plain JSON is simpler: services pass
documents through the driver's standard JSON serialization without any
additional parsing layer, and workloads authored in the runner need no
knowledge of ExtJSON syntax.

The trade-off is that BSON-typed values (ObjectId, Date, Decimal128, Binary)
are no longer round-trippable in a type-preserving way over this API.
Benchmark workloads that depend on exact BSON type fidelity SHOULD use
driver-generated `_id` values (auto-assigned ObjectIds) rather than
caller-specified ExtJSON ids. For the benchmark scenarios in scope — bulk
inserts, finds, updates — plain JSON is sufficient and keeps each service
implementation simpler.

### Why per-request `database` and `collection`

Putting routing in each request lets a single deployed service participate
in multiple concurrent benchmark suites (e.g. one runner per workload)
without naming collisions or service restarts. It also keeps the service
stateless across requests: no per-workload setup endpoints are needed
because the runner can target any collection it likes.

### Why `findOne` returns `{document: null}` instead of 404

`null` keeps the response shape stable for the runner regardless of
whether the lookup hit. The HTTP timing measurement is the same either
way, and a 404 would require the runner to special-case "miss" responses
when computing latency percentiles. 404 remains reserved for cases where
a command genuinely fails because a required resource is absent.

### Why no GridFS or BSON-only commands in v1

The original draft included GridFS upload/download endpoints and called
out the BSON-only micro-benchmarks as Future Work. With the shift to a
CRUD-command surface, GridFS no longer fits the "one endpoint per CRUD
command" mental model and is deferred to a follow-up alongside any other
non-CRUD admin APIs (see [Future Work](#future-work)). The BSON-only
benchmarks remain a poor fit for HTTP and are still deferred.

## Backwards Compatibility

This is v0.1.0 — there is no published prior version. The spec is Draft
and breaking changes are expected. The previous workload-shaped draft
(also v0.1.0) is superseded by this revision; no service implementations
existed against it. Once v1.0.0 is published, additive endpoint and field
changes are allowed; renames, removals, and semantic changes require a
major version bump and a deprecation window.

## Reference Implementation

None yet. Reference services in Go, Node.js, Python, Java, and PHP are
tracked under SKUNK-339.

## Future Work

Deferred from v1:

- **GridFS upload/download endpoints.** Likely a small dedicated endpoint
  family using `application/octet-stream` payloads, parallel to the CRUD
  commands.
- **Admin helpers**: `POST /dropCollection`, `POST /createIndex`,
  `POST /runCommand`. Useful for benchmark setup patterns that CRUD
  commands cannot express (index creation, secondary read preference
  setup, profiling control).
- **BSON-only micro-benchmarks.** Possibly modeled as a non-HTTP local
  process the runner invokes, or as a `application/bson` endpoint family.
- **TLS and authentication.** v1 assumes a trusted network. A future
  version will define how services advertise TLS support and authenticate
  runner requests.
- **Server-timed batch mode.** A `/batch/{command}` endpoint that runs N
  iterations server-side and returns percentile timings — useful for
  comparing this spec's HTTP-roundtrip results against pure-driver
  numbers from the source spec.
- **OpenTelemetry hooks.** A standard way for services to emit tracing so
  the runner can attribute time spent in the driver vs. the HTTP stack.
- **Connection-pool tuning knobs.** Expose a way for the runner to set
  `maxPoolSize` and similar options without restarting the service.

## Changelog

- 2026-05-12: Wire format changed from MongoDB Canonical Extended JSON v2
  to plain JSON. Services no longer need an ExtJSON parsing layer; BSON
  type fidelity is not guaranteed over this API.
- 2026-05-11: Redesigned to one endpoint per CRUD command (`/find`,
  `/findOne`, `/insertOne`, `/insertMany`, `/updateOne`, `/updateMany`,
  `/deleteOne`, `/deleteMany`, `/bulkWrite`, `/clientBulkWrite`). Removed
  workload-shaped endpoints and GridFS from v1; setup is now composed by
  the runner from CRUD primitives.
- 2026-05-11: Initial draft (v0.1.0).
