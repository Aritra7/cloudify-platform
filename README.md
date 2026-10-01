# Cloudify Platform

Cloudify Platform is a Go control plane for self-service, desired-state cloud
migrations. It is being built on top of the Cloudify Python migration engine
created by Anmol Sahu, Sanath Mahesh Kumar, Aritra Ray, Manav Somani, and
Anubhav Sharma at TartanHacks 2026.

The new control plane will add durable jobs, Terraform-owned infrastructure,
drift reconciliation, a Terraform provider, and a Kubernetes operator without
rewriting Cloudify's migration intelligence. See the
[implementation roadmap](docs/ROADMAP.md) for scope and acceptance criteria.

![Python](https://img.shields.io/badge/python-3.10+-blue.svg)
![Go](https://img.shields.io/badge/go-1.24+-00ADD8.svg)
![License](https://img.shields.io/badge/license-MIT-green.svg)
![Dedalus](https://img.shields.io/badge/powered%20by-Dedalus-purple.svg)

## Current status

- The original Python engine and web console are preserved.
- The Python unit suite has been updated for the current Dedalus agent API.
- The Go control plane exposes health/readiness plus structured create, read,
  cancel, and retry endpoints with strict validation and idempotency.
- Migration state has a transactional Postgres implementation and an explicit
  in-memory development fallback.
- Database-backed work claiming uses row locks, expiring leases, heartbeats,
  and ownership checks so multiple dispatchers can execute safely.
- An opt-in Python worker checks out an immutable source revision in an
  isolated workspace, supervises Cloudify without a shell, streams structured
  output, and terminates the process group on cancellation.
- Redacted worker output is stored as ordered Postgres events and exposed
  through paginated and resumable Server-Sent Events APIs.
- Every worker claim creates durable attempt history; lease recovery and manual
  retry preserve prior outcomes. Prometheus metrics expose dispatcher activity.
- A validated deployment specification now renders a deterministic Cloud Run
  Terraform module. The Go planner uses `terraform-exec`, GCS state locking,
  immutable image digests, Secret Manager references, and checksummed plan
  artifacts.
- Terraform plan requests are durable and idempotent, execute under renewable
  worker leases, pass cost/exposure policy before execution, and require an
  auditable checksum-bound approval before apply.
- Scoped bearer-token roles protect plan, approval, and apply operations. The
  apply dispatcher decrypts and verifies the approval-bound binary plan before
  executing that exact plan under a renewable lease.
- Successful applies are projected idempotently into a durable managed-resource
  registry with desired state, generation, remediation policy, conditions, and
  reconciliation scheduling fields.
- An optional leased reconciler reads live Cloud Run and IAM state with the
  official Go client, classifies configuration or deletion drift, and advances
  policy-gated Terraform remediation with exponential backoff.
- CI runs the Go race detector, `go vet`, and the Python test suite.
- CI exercises the Postgres lifecycle against a real ephemeral database.
- A checked-in Spring Boot/React fixture now produces redacted, reproducible
  dry-run evidence without model credentials or cloud side effects. See the
  [demo and benchmark procedure](docs/DEMO.md).
- A Terraform Plugin Framework provider exposes an asynchronous
  `cloudify_migration` resource and data source with idempotent creation,
  polling, cancellation, import, configurable timeouts, and drift-aware reads.

The [API guide](docs/API.md) contains the current contract and local examples.
The [execution model](docs/EXECUTION.md) documents job ownership and recovery.
The [Terraform planning guide](docs/TERRAFORM.md) documents the IaC trust
boundary, state model, and current apply gate.
The [reconciliation guide](docs/RECONCILIATION.md) documents live-state
observation, drift classification, leases, and remediation.
The [Terraform provider guide](docs/TERRAFORM_PROVIDER.md) documents provider
configuration, lifecycle semantics, import, and local development.

## Original migration engine

## Why

Migrating a Spring Boot + React app from a laptop to a production cloud target
usually takes a day or more of human work — IaC writing, dependency mapping,
Dockerfile authoring, datasource rewiring, secret rotation, and post-deploy
smoke tests. Cloudify decomposes that work into agent-callable skills and runs
them with deterministic guardrails: each agent owns a narrow phase, publishes
its result on an event bus, and Dedalus routes the underlying step to the
model best suited for it (GPT‑4.1 for code reasoning, Claude Opus for code
generation, GPT‑4.1‑mini for fast deploy actions).

## How it works

```
┌─────────────────────────────────────────────────────────────┐
│                   ORCHESTRATOR AGENT                        │
│         (Coordinates all agents via event bus)              │
└────────┬────────────────────────────────────────────────────┘
         │
    ┌────┴─────┬──────────┬──────────────┬──────────────┐
    │          │          │              │              │
┌───▼───┐  ┌──▼───┐  ┌───▼────┐  ┌──────▼─────┐  ┌────▼────┐
│ Code  │  │Infra │  │Database│  │  Backend   │  │Frontend │
│Analyze│─▶│Prov. │─▶│Migrat. │─▶│ Deployment │  │Deploym. │
└───────┘  └──────┘  └────────┘  └────────────┘  └─────────┘
                                         │              │
                                         ▼              ▼
                                   Cloud Run      Firebase
                                                   Hosting
```

- Agent orchestration via the [Dedalus SDK](https://dedaluslabs.ai/)
- OpenAI GPT-4.1 / GPT-4.1-mini and Anthropic Claude Opus 4.6 / Sonnet 4.5
  routed per task by a model-role registry in `agents/base_agent.py`
- Skill registry: code analysis, infrastructure provisioning, database
  migration, backend deployment, frontend deployment
- Event-driven publish/subscribe between agents (loose coupling, parallel
  deploy where safe)
- Deterministic guardrails: dry-run mode, interactive approvals, structured
  Dockerfile + datasource templates rather than free-form generation

### Agents

| Agent | Responsibility |
| --- | --- |
| Code Analyzer | Scans `pom.xml` / `build.gradle` / `application.properties` / React `package.json`. Detects Java + Spring Boot version, build tool, datasource, REST endpoints, CORS config. |
| Infrastructure | Provisions GCP resources via `gcloud`: Cloud Run service, Artifact Registry repo, Firebase project, IAM bindings. |
| Database Migration | Decides between keeping H2 (with warnings) or moving to Cloud SQL (Postgres / MySQL). Rewrites Spring datasource config. |
| Backend Deployment | Generates Dockerfile from template, builds, pushes to Artifact Registry, deploys to Cloud Run with env vars. |
| Frontend Deployment | Detects Vite vs CRA, rewrites the API base URL to the Cloud Run URL, builds, deploys to Firebase Hosting. |

## Supported stack configurations

Cloudify currently targets **Google Cloud Platform** (Cloud Run + Firebase
Hosting + Cloud SQL + Artifact Registry). The agents auto-detect the source
stack — you do not configure it manually. Honestly enumerated permutations:

1. React (Vite) + Spring Boot (Maven) + H2 → Cloud Run + Firebase
2. React (Vite) + Spring Boot (Maven) + Cloud SQL Postgres → Cloud Run + Firebase
3. React (Vite) + Spring Boot (Maven) + Cloud SQL MySQL → Cloud Run + Firebase
4. React (Vite) + Spring Boot (Gradle) + H2 → Cloud Run + Firebase
5. React (Vite) + Spring Boot (Gradle) + Cloud SQL Postgres → Cloud Run + Firebase
6. React (CRA) + Spring Boot (Maven) + H2 → Cloud Run + Firebase
7. React (CRA) + Spring Boot (Maven) + Cloud SQL Postgres → Cloud Run + Firebase
8. React (CRA) + Spring Boot (Gradle) + Cloud SQL MySQL → Cloud Run + Firebase

Java 17 and Java 21 are both detected by the Maven / Gradle scanner.

## Quickstart

```bash
git clone https://github.com/sanathmahesh/cloudify.git
cd cloudify

python -m venv venv
source venv/bin/activate
pip install -r requirements.txt

cp .env.example .env
# Fill in DEDALUS_API_KEY, ANTHROPIC_API_KEY (Claude fallback),
# GCP_PROJECT_ID, GOOGLE_APPLICATION_CREDENTIALS

gcloud auth login
gcloud auth configure-docker us-central1-docker.pkg.dev

python migration_orchestrator.py migrate \
  --source-path /path/to/your/app \
  --gcp-project your-project-id \
  --region us-central1
```

A `migration_config.yaml` template can be generated with
`python migration_orchestrator.py init` if you prefer config-file mode over
CLI flags.

### CLI

```
python migration_orchestrator.py migrate [OPTIONS]

  -s, --source-path PATH      Path to source application directory
  -c, --config PATH           Path to migration configuration file
  -p, --gcp-project TEXT      GCP project ID (overrides config)
  -r, --region TEXT           GCP region (default: us-central1)
  -m, --mode TEXT             Execution mode: interactive or automated
  -d, --dry-run               Preview changes without executing
  -v, --verbose               Enable verbose logging
```

## Demo

Reproduce the side-effect-free evidence pack locally:

```bash
python migration_orchestrator.py migrate \
  --source-path ./examples/demo-app \
  --config ./examples/demo-config.yaml \
  --gcp-project cloudify-controlled-demo \
  --region us-central1 \
  --mode automated \
  --dry-run \
  --evidence-file ./evidence_pack.json
```

The checked-in pack is a verified dry run, not evidence of a live GCP
deployment. The controlled live-run and benchmark protocol is documented in
[`docs/DEMO.md`](docs/DEMO.md).

## Project layout

```
agents/               # Per-phase agents (analyzer, infra, db, backend, frontend)
  base_agent.py       # Event bus, model-role registry, Dedalus integration
  orchestrator.py     # Top-level coordinator
  dedalus_tools.py    # Tool implementations exposed to Dedalus runner
templates/            # Dockerfile + cloudbuild.yaml templates
utils/                # GCP helpers, file ops, logging
tests/                # Unit + integration tests
migration_orchestrator.py   # CLI entry point
migration_config.yaml       # Config template
ARCHITECTURE.md             # Deeper architectural notes
```

## Development

Run unit tests:

```bash
pytest tests/unit -v
```

Run integration tests (require a real GCP project):

```bash
pytest tests/integration -v
```

## Built at TartanHacks 2026

Built by Anmol Sahu, Sanath Mahesh Kumar, Aritra Ray, Manav Somani and Anubhav Sharma

## License

MIT — see [LICENSE](LICENSE).
