# SKUNK-339 — HTTP API spec for driver benchmark services (v1)

## Context

SKUNK-339 proposes replacing per-driver microbenchmarks with HTTP services (one per language: Node, Java, Python, Go, PHP) that each use their MongoDB driver to implement a shared HTTP API. A CLI tool drives the benchmarks against each service and verifies spec conformance. This makes benchmarks consistent across drivers, easier to extend (one spec change vs. five driver changes), and more representative of real applications (driver perf measured inside an HTTP stack, not in isolation).

This plan covers **only the first deliverable**: drafting the HTTP API specification document in this repo. The verifier CLI, language-specific services, and Evergreen integration are downstream tasks.

## Design decisions (confirmed with user)

| Decision | Choice |
|---|---|
| Timing model | CLI times each HTTP round-trip — request = one timed iteration |
| Workload scope (v1) | Mirror existing drivers benchmarking spec **minus** the BSON-only micro-benchmarks |
| Fixture provisioning | CLI POSTs fixture payloads to per-workload `/setup` endpoints |
| Parallel workloads | Spec defines single-op endpoints only; CLI achieves concurrency by firing N concurrent requests |
| Wire format | MongoDB Extended JSON v2 (canonical) for documents; binary octet-stream for GridFS payloads |
| Control plane | Minimal — `GET /health` only; Mongo URI from service env var; setup/teardown is per-workload |
| Spec location | This repo (`mongo-drivers-benchmark`); promote to `mongodb/specifications` later if it stabilizes |

## Source material

Existing drivers benchmarking spec: https://github.com/mongodb/specifications/blob/master/source/benchmarking/benchmarking.md
- Workload definitions, fixture sizes, scoring rules (median wall-clock → MB/s, composites).
- v1 of this HTTP spec inherits fixture filenames and dataset semantics so existing fixture archives can be reused unchanged.

## Files to create

### 1. `spec/http-api.md` (primary deliverable)

Prose specification following RFC 2119 conformance language conventions used in `mongodb/specifications`. Sections in order:

- **Abstract** — one-paragraph summary of what the spec defines and who implements it.
- **Goals & non-goals** — explicitly state: v1 omits BSON micro-benchmarks; v1 does not standardize service deployment; v1 does not define the CLI runner's output format.
- **Terminology** — service, runner, workload, iteration, fixture, ExtJSON.
- **Transport & encoding**
  - HTTP/1.1 over TCP; no TLS required for v1.
  - Request/response bodies: `application/json` containing canonical Extended JSON v2 (per [bson-corpus](https://github.com/mongodb/specifications/blob/master/source/bson-corpus/bson-corpus.md)).
  - GridFS payload endpoints: `application/octet-stream` request/response bodies for the binary; metadata via headers (`X-File-Id`, `X-File-Name`).
  - Standard HTTP status codes; error body shape `{"error": "<code>", "message": "<text>"}`.
- **Service lifecycle**
  - Service MUST read MongoDB connection string from `MONGODB_URI` env var on startup.
  - Service MUST listen on the port specified by `PORT` env var.
  - Service MUST connect lazily or eagerly such that `GET /health` returns 200 once the driver is ready.
- **Control endpoints**
  - `GET /health` → `200 {"status": "ok", "driver": "<name>", "driverVersion": "<semver>"}`.
- **Workload endpoints** — for each workload below, define: HTTP method+path, request body schema, response body schema, what the driver MUST do, what counts as "the timed operation" (i.e. everything between request receipt and response send), and the matching `/setup` endpoint that resets state.

  Single-doc workloads:
  - `POST /single-doc/run-command` — driver issues `{hello: 1}` runCommand on admin db.
  - `POST /single-doc/find-one` — `{"_id": <ExtJSON>}` → returns doc from `perftest.corpus`.
  - `POST /single-doc/insert-one/small` — `{"document": <ExtJSON>}` inserts into `perftest.corpus`.
  - `POST /single-doc/insert-one/large` — same, distinct collection.

  Multi-doc workloads:
  - `POST /multi-doc/find-many` — driver runs `find({})` on a pre-loaded collection, iterates full cursor.
  - `POST /multi-doc/insert-many/small` — `{"documents": [...]}` bulk insert (ordered).
  - `POST /multi-doc/insert-many/large` — same, large docs.
  - `POST /multi-doc/bulk-write` — `{"operations": [{"insertOne": {...}}, {"updateOne": {...}}, ...]}` heterogeneous bulkWrite.
  - `POST /multi-doc/gridfs/upload` — `application/octet-stream` body + `X-File-Name` header → returns `{"_id": <ExtJSON ObjectId>}`.
  - `POST /multi-doc/gridfs/download` — `{"_id": <ExtJSON>}` → `application/octet-stream` body.

  Parallel workloads (per-file primitives; CLI fans out):
  - `POST /parallel/ldjson/import` — body is a single LDJSON file's worth of docs; driver bulk-inserts into `perftest.corpus`. CLI calls this once per file concurrently.
  - `POST /parallel/ldjson/export` — `{"chunk": <int>}` → newline-delimited ExtJSON response stream of that chunk's docs.
  - `POST /parallel/gridfs/upload` — same shape as multi-doc GridFS upload.
  - `POST /parallel/gridfs/download` — same shape as multi-doc GridFS download.

- **Setup endpoints** — one per workload group. Each MUST drop the relevant collection(s) before loading. Examples:
  - `POST /setup/single-doc` — body: `{"corpus": [<ExtJSON>...]}` — drops `perftest.corpus`, inserts seed docs (the tweet/small/large reference docs).
  - `POST /setup/multi-doc/find-many` — body: `{"documents": [...]}` — drops + loads the find-many collection.
  - `POST /setup/gridfs/download` — body: `application/octet-stream` — pre-uploads the file that the download benchmark will fetch; returns the file `_id`.
  - `POST /teardown` — drops the entire `perftest` database; OPTIONAL.
- **Conformance rules** — what makes an implementation conformant; how it MUST handle malformed bodies, missing collections, etc.
- **Open questions / v2 candidates** — BSON-only endpoints, OpenTelemetry hooks, TLS, auth, server-timed batch mode.
- **References** — link to driver benchmarking spec, ExtJSON v2 spec, bson-corpus.

### 2. `spec/openapi.yaml` (machine-readable companion)

OpenAPI 3.1 document covering the same endpoints. Purpose: lets the future verifier CLI auto-generate request validators and gives each service team a typed reference. Keep it tightly synced with `http-api.md` — the prose spec is authoritative when they diverge.

### 3. `README.md` update

Replace the placeholder with: project goal (one paragraph), pointer to `spec/http-api.md`, a "v1 status" line, and the language matrix (Node/Java/Python/Go/PHP — each TBD).

## What's deliberately out of scope

- The verifier/runner CLI tool (separate ticket).
- Any language-specific service implementations.
- Evergreen project config.
- BSON micro-benchmarks (deferred to v2).
- Authentication and TLS (deferred to v2).
- Service-timed batch endpoints (deferred to v2).

## Verification

Spec drafts are reviewed, not executed. Verification steps for this deliverable:

1. **Self-review against source spec**: For each workload in `mongodb/specifications/source/benchmarking/benchmarking.md` (single-doc, multi-doc, parallel sections), confirm there is a corresponding endpoint in `http-api.md` and that the fixture/seed semantics match. BSON micro-benchmarks should be intentionally absent and called out in "Open questions".
2. **OpenAPI lint**: `npx @redocly/cli lint spec/openapi.yaml` (or equivalent) should pass with zero errors.
3. **Cross-doc consistency**: every path in `openapi.yaml` appears in `http-api.md` and vice versa.
4. **User review checkpoint**: share draft with the user before any service implementation work begins.

## Critical files to read before drafting

- `README.md` (current placeholder)
- The source benchmarking spec at https://github.com/mongodb/specifications/blob/master/source/benchmarking/benchmarking.md (already summarized during planning)
- Convention reference: any recent spec in `mongodb/specifications/source/` to mirror tone/structure (e.g., `client-side-encryption/`).
