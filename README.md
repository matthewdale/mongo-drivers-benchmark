# mongo-drivers-benchmark

A consistent, cross-language benchmarking harness for MongoDB drivers (SKUNK-339).

Each supported MongoDB driver is wrapped by a small HTTP service that exposes
the driver's CRUD command API over a uniform set of endpoints. A single
language-agnostic CLI runner composes benchmark workloads from those command
primitives and times each HTTP round-trip, so reported numbers reflect the
driver as it is used inside a real application stack — not in isolation.
Adding or changing a benchmark is a runner-side change; the service surface
stays small and stable across languages.

## Status

**v0.1.0 — Draft.** Only the HTTP API specification is checked in. The CLI
runner, language-specific services, and Evergreen project are not yet
implemented.

## Spec

- [`spec/http-api.md`](spec/http-api.md) — authoritative prose specification.
- [`spec/openapi.yaml`](spec/openapi.yaml) — machine-readable OpenAPI 3.1
  companion. The prose spec wins when they diverge.

The API surface is one endpoint per driver CRUD command:
`find`, `findOne`, `insertOne`, `insertMany`, `updateOne`, `updateMany`,
`deleteOne`, `deleteMany`, `bulkWrite`, `clientBulkWrite` — plus a single
`GET /health` control endpoint.

## Service implementation matrix

| Language | Driver              | Status |
|----------|---------------------|--------|
| Go       | mongo-go-driver     | TBD    |
| Node.js  | node-mongodb-native | TBD    |
| Python   | pymongo             | TBD    |
| Java     | mongo-java-driver   | TBD    |
| PHP      | mongo-php-library   | TBD    |

## Background

Replaces the per-driver implementation model of the existing
[MongoDB Driver Performance Benchmark spec](https://github.com/mongodb/specifications/blob/master/source/benchmarking/benchmarking.md).
See [`spec/http-api.md`](spec/http-api.md) for the motivation in full.
