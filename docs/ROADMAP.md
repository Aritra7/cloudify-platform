# Cloudify Platform implementation roadmap

## Objective

Build an auditable, self-service Go control plane around the existing Cloudify
Python migration engine. Terraform owns the desired GCP infrastructure; the
control plane owns jobs, policy, lifecycle, reconciliation, and evidence.

The project is intentionally designed around the technical capabilities in
Apple role 200661492. It demonstrates those capabilities, but it does not
substitute for the posting's required years of professional experience.

## Architecture boundary

```text
Clients / Terraform provider / Kubernetes CRD
                    |
                    v
              Go control plane
       API | jobs | policy | reconciler
          |              |          |
          v              v          v
  Python Cloudify    Terraform     GCP APIs
      worker        desired state  observed state
                    |
                    v
             GCS state backend

              Postgres
       job metadata | leases | audit
```

Terraform state belongs in GCS. Postgres stores platform metadata, desired
specifications, status, leases, and audit records. Cloudify remains responsible
for application analysis and migration-specific transformations.

## Engineering principles

- Make operations idempotent and asynchronous.
- Keep desired and observed state explicit.
- Prefer deterministic templates and schemas over free-form generation.
- Apply least privilege and never persist secret values in logs or job records.
- Design every destructive action for retry, cancellation, and auditability.
- Measure claims with repeatable tests; do not present estimates as benchmarks.

## Phase 0 — trustworthy baseline

Status: in progress

- [x] Start from the latest upstream Cloudify commit.
- [x] Repair tests broken by the `claude_api_key` to `dedalus_api_key` refactor.
- [x] Update analyzer tests to exercise the current deterministic tool boundary.
- [x] Add Go and Python CI jobs.
- [x] Add a minimal Go server with health, readiness, timeouts, and graceful
  shutdown.
- [x] Record a reproducible, side-effect-free dry-run fixture and generated
  evidence pack.
- [ ] Record a successful controlled GCP migration and replace the dry-run pack
  with verified live-run evidence.
- [x] Document benchmark start/end conditions before publishing any time-saved
  metric.

Exit criteria:

- Python and Go tests pass from a clean clone.
- CI is green on `main`.
- One migration can be reproduced from documented inputs.

## Phase 1 — durable Go control plane

Status: in progress

- [x] `POST /v1/migrations` with structured input and an idempotency key.
- [x] Read and cancel endpoints.
- [x] Retry and event-stream endpoints.
- [x] `GET` resource collection and detail endpoints.
- [ ] Asynchronous `DELETE` resource endpoint.
- [x] Transactional Postgres repository for migrations.
- [x] Postgres repositories for attempts, events, and leases.
- [x] Postgres repository for managed resources.
- [x] Explicit migration state transitions enforced by the domain layer.
- [x] Postgres work claiming with `SKIP LOCKED`, expiring leases, renewal, and
  stale-worker protection.
- [x] Dispatcher and cancellable worker interface.
- [x] Isolated Python worker adapter with safe Git checkout, argument-based
  subprocesses, bounded output, process-group cancellation, and cleanup.
- [x] Persist ordered worker events and expose paginated and resumable SSE APIs.
- [x] Context propagation, request limits, and Prometheus dispatcher metrics.
- [ ] Authentication hooks, structured request logs, and OpenTelemetry traces.
- [x] A versioned worker interface and local subprocess adapter for the Python
  engine. Introduce gRPC only when the worker must run remotely.

Exit criteria:

- Restarting the API does not lose a job.
- Duplicate requests return the original operation.
- Cancellation terminates the worker and records the terminal state.
- Handler, repository, and state-machine tests cover success and failure paths.

## Phase 2 — Terraform-owned GCP infrastructure

Status: in progress

- [x] Versioned Cloud Run v2 module.
- [x] Validated, deterministic deployment-spec renderer.
- [x] `terraform-exec` plan runner with GCS backend configuration and bounded
  command output.
- [x] Checksummed JSON and human-readable plan artifacts plus encrypted binary
  plans for exact apply; plaintext workspace copies are removed.
- [x] CI formatting and validation for Terraform modules.
- [x] Durable plan metadata, policy evaluation, leased execution, and auditable
  approval.
- [x] Role-authenticated, audited, leased apply of the exact approved plan.
- [ ] Workload-identity/OIDC authentication and cloud object artifact storage.

Implement versioned modules for:

- Artifact Registry
- [x] Cloud Run
- Cloud SQL
- IAM and workload identities
- Secret Manager references
- Cloud DNS
- GCS remote state

Cloudify analysis produces a validated deployment specification. A deterministic
renderer turns that specification into module inputs. Existing resources use
generated Terraform import blocks; a successful adoption finishes with an empty
plan.

Exit criteria:

- Plans are stored as artifacts and linked to the initiating job.
- Applies use per-workspace locks and bounded execution time.
- Secret values never enter generated files, state metadata, or logs.
- Create, update, import, no-op, partial failure, and destroy are integration
  tested.

## Phase 3 — reconciliation and self-healing

Status: in progress

- [x] Project applied plans into a durable, idempotent managed-resource
  registry.
- [x] Record desired state, generation, observed generation, conditions, retry
  count, remediation policy, and reconciliation scheduling fields.
- [x] Observe Cloud Run and IAM state and classify configuration or deletion
  drift under a renewable lease.
- [x] Trigger policy-gated Terraform remediation with exponential backoff and
  the existing approval/apply audit trail.
- [x] Add immutable per-reconciliation observation events and drift/remediation
  metrics.
- [ ] Add fault-injection coverage for database outages.

Each resource records desired state, observed state, generation, observed
generation, conditions, retry count, and remediation policy.

The controller:

1. acquires a lease;
2. reads actual GCP state with the Go SDK;
3. classifies configuration, application, or deletion drift;
4. emits a plan and policy decision;
5. remediates approved drift through Terraform or the worker;
6. records status and an immutable audit event;
7. retries transient failures with exponential backoff and jitter.

Exit criteria:

- Concurrent controllers cannot mutate the same resource.
- Out-of-band deletion is detected and handled according to policy.
- Permanent failures stop retrying and expose an actionable condition.
- Fault-injection tests cover rate limits, lock contention, worker crashes, and
  database outages.

## Phase 4 — Terraform provider

Build `terraform-provider-cloudify` with the Terraform Plugin Framework.

Initial resources and data sources:

- `cloudify_migration`
- `cloudify_application`
- `cloudify_resource`
- `cloudify_migration` data source

Required behaviors include asynchronous create/read/update/delete, import,
timeouts, cancellation, drift-aware refresh, sensitive attributes, actionable
diagnostics, and acceptance tests.

Exit criteria:

- A clean Terraform configuration can create, observe, update, import, and
  delete a migration through the Go API.
- Provider acceptance tests run against an ephemeral control-plane environment.

## Phase 5 — Kubernetes operator

Add a `Migration` custom resource using `controller-runtime`.

- Reconcile CRD specifications into control-plane operations.
- Use finalizers for deletion, status conditions for progress, and generation
  checks to prevent stale writes.
- Run Cloudify workers as Kubernetes Jobs where appropriate.
- Add leader election, Kubernetes Events, minimal RBAC, NetworkPolicy, pod
  security settings, and a Helm chart.

Exit criteria:

- `kind` end-to-end tests exercise create, update, cancellation, retry, and
  deletion.
- Two controller replicas demonstrate leader election and safe failover.

## Phase 6 — security and cost controls

- Workload Identity and least-privilege service accounts.
- Project/tenant authorization and immutable destructive-action audit records.
- Secret Manager references and log redaction tests.
- Resource ownership, environment, and cost-center labels.
- Policy limits for instance counts, database tiers, regions, and public access.
- Pre-apply monthly cost estimates, budgets, and alert thresholds.
- Threat model and operational incident runbook.

Exit criteria:

- Security tests reject cross-project access and secret disclosure.
- Every proposed plan carries policy results and a cost estimate.
- Destructive operations identify actor, request, target, and result.

## Qualification evidence matrix

| Capability | Repository evidence |
| --- | --- |
| Go backend engineering | Control plane, reconciler, provider, operator, tests |
| Python/TypeScript | Existing migration engine and web console |
| Terraform/IaC | GCP modules, imports, remote state, provider |
| Enterprise resource lifecycle | Durable jobs, leases, policies, audit, reconciliation |
| Reliable software | CI, race tests, integration tests, fault injection, runbooks |
| HTTP/RPC/database/OS | REST, worker protocol, Postgres, process and signal control |
| Self-service/self-healing | REST, Terraform provider, CRD, drift remediation |
| Cloud security/cost | Identity, secrets, authorization, policy, budgets, estimates |

## Explicit non-goals for the first release

- Claiming multi-cloud or hybrid-cloud support before a second provider works.
- Adding Spinnaker solely as a keyword.
- Re-running the full migration engine for every infrastructure drift event.
- Storing Terraform state in the application database.
- Publishing performance or time-saved claims without a reproducible benchmark.
