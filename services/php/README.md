# PHP load-test service

PHP implementation of the MongoDB drivers load-test HTTP API (`spec/http-api.md`).

## Stack

- `php:8.3-fpm-bookworm` + nginx (one container, both managed by the CMD).
- PECL `mongodb` extension (latest) + `mongodb/mongodb` userland library.
- php-fpm with `pm = static` and `pm.max_children = 32` so the validator's
  parallel scenarios are served concurrently.

## Run

```sh
docker compose up --build -d
curl -s http://localhost:8080/v1/health
docker compose down -v
```

## Layout

```
Dockerfile
compose.yaml
composer.json
nginx.conf
public/index.php          # HTTP front controller; routes /v1/*
src/Errors.php            # driver-exception → ErrorCode mapping
src/Ejson.php             # Extended JSON v2 encode/decode helpers
src/Ops.php               # /v1/ops dispatcher (one method per op)
src/Validator.php         # request schema validation (RequestError 400)
src/ValidationError.php
```

## Notes

- Extended JSON: responses use **relaxed** Extended JSON v2 per the v1.0 spec
  amendment — numerics emit as plain JSON numbers, non-numeric BSON types use
  the `{$...}` envelope. We produce response bodies by calling
  `MongoDB\BSON\Document::toRelaxedExtendedJSON()` on driver-returned values.
- Inputs accept canonical or relaxed Extended JSON v2 via
  `MongoDB\BSON\Document::fromJSON()->toPHP()`.
- For `bulkWrite` we pre-assign a fresh `ObjectId` to any `insertOne` sub-op
  whose document lacks `_id`, so the response `inserted_ids` map is populated
  even when the driver's `BulkWriteResult::getInsertedIds()` is empty.
