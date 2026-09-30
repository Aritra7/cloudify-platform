# Control-plane API

The API is under active development. All migration creation requests require an
idempotency key. Reusing a key with the same request returns the original
migration; reusing it with a different request returns `409 Conflict`.

## Start locally with in-memory state

```sh
go run ./cmd/control-plane
```

The server warns that state is not durable when `CLOUDIFY_DATABASE_URL` is not
set. This mode is intended only for development.

## Start with Postgres

Apply `db/migrations/001_create_migrations.sql`, then run:

```sh
export CLOUDIFY_DATABASE_URL='postgres://cloudify:cloudify@localhost:5432/cloudify?sslmode=disable'
go run ./cmd/control-plane
```

The process verifies database connectivity before accepting requests.

## Create a migration

```sh
curl --fail-with-body \
  -X POST http://localhost:8080/v1/migrations \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: example-application-main' \
  -d '{
    "source": {
      "repository_url": "https://github.com/example/application",
      "revision": "main"
    },
    "destination": {
      "provider": "gcp",
      "project_id": "example-project",
      "region": "us-central1",
      "runtime": "cloud-run",
      "database": "cloud-sql-postgres"
    }
  }'
```

A new operation returns `202 Accepted`, a `Location` header, and a migration in
the `queued` state. An exact replay returns `200 OK` with
`Idempotent-Replayed: true`.

## Read a migration

```sh
curl --fail-with-body http://localhost:8080/v1/migrations/MIGRATION_ID
```

## Cancel a migration

```sh
curl --fail-with-body \
  -X POST http://localhost:8080/v1/migrations/MIGRATION_ID/cancel
```

A queued migration moves directly to `cancelled`. A running migration moves to
`cancelling`, allowing the worker to perform cleanup before it records the
terminal `cancelled` state.

## Read migration events

```sh
curl --fail-with-body \
  'http://localhost:8080/v1/migrations/MIGRATION_ID/events?after=0'
```

Events have globally increasing sequence IDs and are returned in order. Pass
the last processed sequence as `after` to resume without replaying it.

## Stream migration events

```sh
curl --no-buffer --fail-with-body \
  'http://localhost:8080/v1/migrations/MIGRATION_ID/events/stream?after=0'
```

The endpoint uses Server-Sent Events. Clients can resume through the `after`
query parameter or the standard `Last-Event-ID` header. The server sends
keepalive comments while an active migration is quiet and closes the stream
after all events for a terminal migration have been delivered.

Worker output is redacted for common password, token, secret, API-key, and
Bearer-authorization forms before persistence. Redaction is defense in depth;
workers must still avoid emitting credentials.

## Error shape

```json
{
  "error": {
    "code": "validation_failed",
    "message": "destination.provider must be gcp"
  }
}
```

Request bodies are limited to 1 MiB, unknown JSON fields are rejected, and only
one JSON value is accepted.
