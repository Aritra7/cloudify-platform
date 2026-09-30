# Durable execution model

The control plane separates accepting a migration from executing it. API
replicas only persist desired work; dispatcher replicas compete for eligible
jobs through the shared store.

## Claiming

Postgres selects the oldest eligible migration with `FOR UPDATE SKIP LOCKED` and
atomically changes it from `queued` to `running`. The same operation records a
worker identity and lease deadline. This allows multiple dispatcher replicas to
claim work without a process-local queue or a global lock.

A `running` migration becomes claimable again after its lease expires. The new
worker identity prevents the previous worker from recording a late result.

## Heartbeats

While work is active, the dispatcher renews the lease before it expires. A
renewal succeeds only when:

- the worker still owns the migration;
- the previous lease has not expired; and
- the migration is `running` or `cancelling`.

Losing a lease cancels the worker context and prevents a stale completion.
Workers must honor context cancellation.

## Completion and cancellation

Only the lease owner may record a terminal result. A successful worker changes
`running` to `succeeded`; a worker error changes it to `failed`.

Cancellation is two-stage for active work:

```text
running -> cancelling -> cancelled
```

This gives the worker an opportunity to stop external processes and perform
cleanup. Queued work can move directly to `cancelled` because it has not started.

## Python worker adapter

Worker execution is disabled by default. Enable it only in an environment that
has the Cloudify Python dependencies, Docker, `gcloud`, Firebase tooling, and
the intended workload identity:

```sh
export CLOUDIFY_WORKER_ENABLED=true
export CLOUDIFY_ENGINE_ROOT=/path/to/cloudify-platform
export CLOUDIFY_DATABASE_URL='postgres://...'
go run ./cmd/control-plane
```

Optional settings are `CLOUDIFY_WORK_ROOT`, `CLOUDIFY_PYTHON_BINARY`, and
`CLOUDIFY_GIT_BINARY`.

For each claimed migration, the adapter:

1. validates that the repository uses HTTPS and has no embedded credentials;
2. rejects revisions that could be interpreted as command options;
3. creates a uniquely named temporary workspace;
4. fetches only the requested revision and checks out `FETCH_HEAD` detached;
5. invokes `migration_orchestrator.py migrate` with an argument array rather
   than a shell command;
6. streams bounded stdout and stderr lines to a structured event sink; and
7. removes the workspace after success, failure, or cancellation.

Every child is started in a separate process group. Cancellation sends
`SIGTERM` to the group, allows a bounded grace period, and then uses `SIGKILL`
if descendants do not exit. Dispatcher status polling connects an API
cancellation request to that process context.

The current sink writes structured logs. Durable event persistence and the
event-stream API are the next integration step.
