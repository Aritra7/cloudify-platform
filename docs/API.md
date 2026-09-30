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

Apply every SQL file in `db/migrations` in numeric order, then run:

```sh
export CLOUDIFY_DATABASE_URL='postgres://cloudify:cloudify@localhost:5432/cloudify?sslmode=disable'
go run ./cmd/control-plane
```

The process verifies database connectivity before accepting requests.

## Authentication

Set `CLOUDIFY_AUTH_TOKENS_JSON` to enable bearer authentication for every
endpoint except `/healthz` and `/readyz`. Configuration contains only SHA-256
digests of high-entropy service tokens:

```sh
export CLOUDIFY_AUTH_TOKENS_JSON='[
  {
    "actor": "platform-operator@example.com",
    "token_sha256": "64_LOWERCASE_HEX_CHARACTERS",
    "roles": ["planner", "approver", "operator"]
  }
]'
```

Requests pass the plaintext token in `Authorization: Bearer TOKEN`. The
control plane hashes it and compares digests in constant time. The `planner`
role can submit plans, `approver` can approve them, and only `operator` can
queue an apply. This service-token implementation provides a tested local and
machine-to-machine boundary; workload-identity/OIDC validation remains the
production integration target.

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

## Retry a failed migration

```sh
curl --fail-with-body \
  -X POST http://localhost:8080/v1/migrations/MIGRATION_ID/retry
```

Only a migration in the `failed` state can be retried. The endpoint returns
`202 Accepted` and moves it back to `queued`. Prior execution attempts remain
available for diagnosis and the next claim receives a new attempt number.

## Read execution attempts

```sh
curl --fail-with-body \
  http://localhost:8080/v1/migrations/MIGRATION_ID/attempts
```

Each attempt includes its worker, terminal status, start time, last heartbeat,
and finish time. A lease-expired execution is closed as failed before the next
worker opens a new attempt.

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

## Metrics

```sh
curl --fail-with-body http://localhost:8080/metrics
```

The Prometheus text endpoint reports claims, terminal completions, worker
failures, lease-renewal failures, and current in-process executions. These are
process metrics; durable migration and attempt state remains in Postgres.

## Create a Terraform plan

Terraform planning is asynchronous and requires a migration in the `succeeded`
state. Submit a validated deployment specification with a new idempotency key:

```sh
curl --fail-with-body \
  -X POST http://localhost:8080/v1/migrations/MIGRATION_ID/plans \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: plan-example-api-v1' \
  --data @examples/deployment-spec.json
```

A policy-compliant request returns `202 Accepted` in the `queued` state. A
request that violates a region, scaling, or public-access policy is durably
recorded as `rejected` and returns `201 Created`; Terraform is never invoked.

Read its status and checksummed artifacts with:

```sh
curl --fail-with-body http://localhost:8080/v1/plans/PLAN_ID
```

Plan states are `queued`, `planning`, `ready`, `rejected`, `failed`,
`approved`, `apply_queued`, `applying`, `applied`, and `apply_failed`.
Planning and apply use the same lease and stale-owner principles as migration
execution.

## Approve a Terraform plan

```sh
curl --fail-with-body \
  -X POST http://localhost:8080/v1/plans/PLAN_ID/approve \
  -H 'Authorization: Bearer APPROVER_TOKEN'
```

Only a policy-compliant `ready` plan with persisted artifacts can be approved.
Postgres records the actor, timestamp, and exact artifact checksums in an
append-only approval row. The actor comes from the authenticated principal,
not from a caller-controlled audit header.

## Apply an approved Terraform plan

```sh
curl --fail-with-body \
  -X POST http://localhost:8080/v1/plans/PLAN_ID/apply \
  -H 'Authorization: Bearer OPERATOR_TOKEN'
```

The endpoint requires the `operator` role and returns `202 Accepted`. It moves
an `approved` plan through `apply_queued`, `applying`, and either `applied` or
`apply_failed`. The apply worker decrypts, checksum-verifies, and executes the
exact binary plan covered by approval; it never silently creates a replacement
plan.

## List managed resources

Successful Terraform applies are projected into the managed-resource registry.
Projection is asynchronous and idempotent, so API restarts safely replay any
applied plans that have not yet been materialized.

```sh
curl --fail-with-body \
  -H 'Authorization: Bearer OPERATOR_TOKEN' \
  'http://localhost:8080/v1/resources?limit=100'

curl --fail-with-body \
  -H 'Authorization: Bearer OPERATOR_TOKEN' \
  http://localhost:8080/v1/resources/RESOURCE_ID

curl --fail-with-body \
  -H 'Authorization: Bearer OPERATOR_TOKEN' \
  'http://localhost:8080/v1/resources/RESOURCE_ID/events?after=0&limit=100'
```

Each record exposes its desired deployment specification, source plan,
generation, observed generation, drift state, conditions, retry count,
remediation policy, and next reconciliation time. Newly projected resources
start in `unknown` until the GCP observer completes a reconciliation.
When `CLOUDIFY_RECONCILER_ENABLED=true`, observed state and conditions update
as the leased controller classifies live Cloud Run configuration. See
[Cloud Run reconciliation](RECONCILIATION.md).
The events endpoint returns the append-only observation history in sequence
order, so operators can distinguish recurring drift from the latest snapshot.

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
