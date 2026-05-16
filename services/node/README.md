# Node.js Load-Test Service

Implements `spec/http-api.md` v1.0.0 using the `mongodb` Node.js driver and
Fastify.

## Run

```sh
docker compose up --build
```

The service listens on port 8080 (override with `HOST_PORT` for host
binding, or `PORT` inside the container).

## Endpoints

- `POST /v1/ops` — execute ordered CRUD ops
- `POST /v1/admin/reset` — drop named databases (validator-only)
- `GET  /v1/info` — service identification
- `GET  /v1/health` — cluster ping

## Conformance

Validated by `validator/conformance`:

```sh
cd ../../validator
go test ./conformance -url=http://localhost:8080 -count=1 -v
```
