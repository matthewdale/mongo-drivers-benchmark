# validator

Conformance validator for the MongoDB drivers load-test HTTP API
([`spec/http-api.md`](../spec/http-api.md), [`spec/openapi.yaml`](../spec/openapi.yaml)).

The validator is implemented as a Go test binary. Each conformance check is
a `Test*` function; running it against a live service is `go test` with a
flag pointing at the target.

## Prerequisites

- Go 1.22+ (developed against 1.25).
- A running implementation of the spec, reachable over HTTP.
- A MongoDB cluster reachable by that service (the validator never connects
  to MongoDB directly; everything goes through the service's HTTP API).

## Running

From this directory:

```sh
go test ./conformance -url=http://localhost:8080 -v
```

Useful flags:

| Flag                | Effect                                                                |
|---------------------|-----------------------------------------------------------------------|
| `-url=`             | Target service base URL. Required. Falls back to `$MDBV_URL`.         |
| `-spec=`            | Path to `openapi.yaml`. Default: looks at `../spec/openapi.yaml`.     |
| `-run=Pattern`      | Standard Go test filter. e.g. `-run=Reset` runs reset scenarios only. |
| `-v`                | Verbose: prints every scenario name as it runs.                       |
| `-count=1`          | Disable Go's test cache (recommended when iterating).                 |
| `-parallel=N`       | Cap concurrent scenarios. All scenarios opt in to `t.Parallel()`.     |

For CI with structured output:

```sh
go test ./conformance -url=http://localhost:8080 -json | gotestsum --raw-command -- cat
```

(or pipe the `-json` stream to any junit/teamcity converter).

## What the v1 validator checks

1. **OpenAPI schema** — every typed response (and every `400` from
   `RawPost`) is validated against `spec/openapi.yaml` automatically; no
   per-scenario assertions are needed.
2. **CRUD self-consistency** — insert/find/update/delete/replace/count/
   aggregate/bulkWrite round-trip correctly against a real cluster.
3. **Sequencing** — when one op fails mid-request, subsequent ops still run
   (no short-circuit), per §6.2 of the prose spec.
4. **`/v1/admin/reset`** — drops named databases; rejects empty input with
   `400`.
5. **`/v1/info`** — returns required fields, `spec_version == "1.0.0"`, and
   `language` in the v1 enum.
6. **`/v1/health`** — returns 200 with `ok: true` when the cluster is
   reachable.
7. **`400`s for malformed `/v1/ops`** — missing `database`, empty `ops`,
   unknown op name, schema-violating op body.

## What's NOT in v1

- Error-code mapping verification (DUPLICATE_KEY/etc.) — v1.1.
- Canonical Extended JSON byte-level enforcement on response bodies — v1.2.
- Fault-injection-only categories (WRITE_CONFLICT, NETWORK, AUTH, TIMEOUT)
  — v2 (needs a TCP proxy in front of MongoDB).
- Cross-implementation equivalence — out of scope; this is a single-service
  black-box validator.

## Running the unit tests (no service required)

```sh
go test ./conformance -run=TestSpec_ -v
```

These two tests do not require `-url=` — they exercise the spec loader and
response-validation plumbing against an in-process `httptest` server.

## Smoke-running against an arbitrary service

For early integration, point `-url=` at any HTTP server that responds on
the right paths. Scenarios will report exactly which schema rule or
behavior was violated, which is usually enough to drive a service
implementation forward one scenario at a time:

```sh
go test ./conformance -url=http://localhost:8080 -run=TestConformance_Info_ -v
go test ./conformance -url=http://localhost:8080 -run=TestConformance_Ops_  -v
```
