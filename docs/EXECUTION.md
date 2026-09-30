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

## Remaining integration

The dispatcher and worker contract are implemented and tested. The next step is
the production Python worker adapter, including isolated repository checkout,
subprocess supervision, structured event capture, and bounded cleanup.
