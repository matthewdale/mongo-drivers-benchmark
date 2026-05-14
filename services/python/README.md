# mbench Python service

A Python HTTP service that wraps PyMongo and exposes it over the mbench benchmark HTTP API.

## Requirements

- Python 3.9+
- PyMongo 4.3+ (`pip install pymongo`)

## Usage

```bash
MONGODB_URI=mongodb://localhost:27017 PORT=8081 python3 main.py
```

## Verify conformance

```bash
# Start the service (in one terminal)
MONGODB_URI=mongodb://localhost:27017 PORT=8081 python3 main.py

# Run the conformance suite (in another terminal, from the mbench directory)
cd ../../mbench && go run . verify --target http://localhost:8081
```

## Implementation notes

- Uses only stdlib `http.server.ThreadingHTTPServer` (no Flask/FastAPI).
- All request and response bodies use MongoDB Canonical Extended JSON v2 via `bson.json_util`.
- `/clientBulkWrite` returns 501 if the PyMongo version or MongoDB server does not support it.
