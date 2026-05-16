# Go service — MongoDB drivers load-test HTTP API

Implements `spec/http-api.md` v1.0.0 against MongoDB via
[`go.mongodb.org/mongo-driver/v2`](https://pkg.go.dev/go.mongodb.org/mongo-driver/v2).

## Build & run

```sh
docker compose up --build -d
# Health check (poll until 200).
curl -fsS http://localhost:8080/v1/health
# Tear down.
docker compose down -v
```

The service listens on `:8080` and reads `MONGODB_URI` from its environment.

## Validate

From the repo root:

```sh
cd validator
go test ./conformance -url=http://localhost:8080 -count=1 -v
```

## Layout

- `cmd/server/main.go` — process entry, wires the `*mongo.Client`, starts HTTP.
- `internal/api` — `net/http` handlers for the four `/v1` endpoints.
- `internal/ops` — request decoding, validation, and the per-op dispatcher.
- `internal/ejson` — Extended JSON v2 canonical encode/decode helpers.
- `internal/errs` — driver-error → normalized `ErrorCode` mapping.

## Design notes

- One `*mongo.Client` lives for the process lifetime (spec §4).
- The dispatcher executes ops sequentially in a single goroutine; failure of
  op `N` does not short-circuit ops `N+1..end` (spec §6.2). Each per-op error
  surfaces in the per-op result with a normalized `ErrorCode`.
- Every BSON-typed value on the wire is canonical Extended JSON v2; the
  service uses `bson.MarshalExtJSON(v, canonical=true, escapeHTML=false)` for
  output and `bson.UnmarshalExtJSON` for input.
- `bulkWrite` pre-assigns `_id` on `insertOne` sub-ops so the response's
  `inserted_ids` map (decimal-string sub-op index → inserted `_id`) is
  recoverable client-side — the driver's `BulkWriteResult` doesn't expose
  inserted ids directly.
