# Python load-test service

Implements the MongoDB drivers load-test HTTP API (`spec/http-api.md`,
`spec/openapi.yaml`) using [pymongo](https://pymongo.readthedocs.io/) in
synchronous mode, served by Flask under gunicorn.

## Layout

- `app.py` — Flask app exposing the four `/v1` endpoints; single shared
  `MongoClient`; per-op dispatcher; spec §7 error classifier.
- `requirements.txt` — pymongo (latest), flask, gunicorn.
- `Dockerfile` — python:3.13-slim; runs `gunicorn --workers 2 --threads 8`.
- `compose.yaml` — `mongo:8` + service.

## Running

```sh
docker compose up --build
curl localhost:8080/v1/health
```

## Notes

- Extended JSON v2 follows the v1.0 amendment (spec §5.1): numeric BSON types
  emit as plain JSON numbers, non-numeric types use the canonical `{$...}`
  envelope. Achieved via `bson.json_util` with `JSONOptions(json_mode=RELAXED)`.
- The MongoClient is thread-safe and reused across requests; gunicorn provides
  worker processes + threads for concurrency.
- For `bulkWrite`, `insertOne` sub-ops without an explicit `_id` get a fresh
  `ObjectId` pre-assigned client-side so the `inserted_ids` map can be
  populated (pymongo's `BulkWriteResult` does not expose driver-generated
  insert IDs).
