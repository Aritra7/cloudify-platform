# Reproducible migration evidence

The checked-in demo uses a minimal Spring Boot 3.5/Java 21 backend, React/Vite
frontend, and H2 database under `examples/demo-app`. Dry run is a strict local
safety boundary: it performs deterministic source analysis and records proposed
mutations, but it does not load model credentials, invoke `gcloud`, contact GCP,
build containers, or change the source tree.

## Reproduce the checked-in dry run

From the repository root:

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

The command requires no cloud or model credentials. `evidence_pack.json`
contains the effective redacted configuration, repository revision, timing,
detected application properties, proposed cloud actions, outcome, and ordered
events. Workspace paths are normalized before publication.

## Controlled live run

Use a new disposable GCP project with billing alerts and no unrelated
resources. Replace `PROJECT_ID` below only after verifying the active account,
project ownership, quota, and expected charges:

```bash
gcloud auth list --filter=status:ACTIVE
gcloud projects describe PROJECT_ID

python migration_orchestrator.py migrate \
  --source-path ./examples/demo-app \
  --config ./examples/demo-config.yaml \
  --gcp-project PROJECT_ID \
  --region us-central1 \
  --mode automated \
  --evidence-file ./evidence_pack.json
```

Do not call the live run verified until the Cloud Run health endpoint and
frontend URL both respond, the evidence status is `success`, and GCP inventory
matches the evidence. The current checked-in pack is explicitly a dry run.

Use a disposable project so teardown is complete and auditable: export the
final inventory and billing view, then delete the project through the normal
GCP project lifecycle after the demonstration.

## Benchmark definition

Record two separate measurements:

- **Migration wall time:** starts immediately before the migration command and
  ends after health checks pass and the evidence file is flushed.
- **Active operator time:** time spent entering commands, approving actions,
  diagnosing failures, and validating the result.

Dependency installation, initial account creation, billing activation, and
unrelated source-development time are excluded and must be stated alongside
the result. Compare Cloudify with a documented manual run of the same fixture,
target project shape, region, validation steps, and teardown procedure. Run
each path at least three times and publish the median plus individual samples.
Do not publish a percentage reduction until both datasets exist.
