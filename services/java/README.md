# Java load-test service

Implements `spec/http-api.md` on top of the synchronous `mongodb-driver-sync`
Java driver and Javalin.

## Build / run

```sh
docker compose up --build
# wait for /v1/health to be 200
curl http://localhost:8080/v1/health
```

Or natively (requires JDK 21 + Maven):

```sh
mvn -B -DskipTests package
MONGODB_URI=mongodb://localhost:27017 java -jar target/java-service-1.0.0-shaded.jar
```

## Validator

```sh
cd ../../validator
go test ./conformance -url=http://localhost:8080 -count=1 -v
```

## Layout

- `App.java` — entry point, MongoClient bootstrap, /v1/info.
- `Routes.java` — Javalin handler wiring and 400 RequestError validation.
- `Dispatcher.java` — per-op execution and result-shape building.
- `Ejson.java` — Extended JSON v2 relaxed bridge.
- `Errors.java` — driver-exception to spec-ErrorCode mapping (§7).
