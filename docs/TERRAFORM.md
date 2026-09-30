# Terraform planning layer

Cloudify's Python engine discovers and transforms an application. The Go IaC
layer accepts the resulting `cloudify.dev/v1alpha1` deployment specification
and turns it into a reproducible Terraform plan. It does not accept arbitrary
HCL from an agent.

## Safety boundary

The specification requires:

- an immutable container image addressed by a SHA-256 digest;
- bounded Cloud Run scaling;
- a dedicated GCP service account;
- plain environment variables separated from Secret Manager references; and
- unique environment-variable names across both sources.

Names that look secret-bearing, including token, password, API-key, and private
key variables, are rejected from the plain environment map. Secret references
contain only a Secret Manager resource name and version. Secret values are
never rendered into configuration or plan metadata.

## Deterministic rendering

`internal/iac.Renderer` validates the specification, copies the versioned Cloud
Run module into an isolated workspace, and writes a `main.tf.json` root module.
Go's JSON encoder provides stable map-key ordering and secret references are
explicitly sorted, so equivalent specifications produce byte-identical root
configuration. The Google provider is pinned to an exact reviewed version;
upgrades are explicit pull-request changes rather than implicit plan drift.

The root module uses a partial GCS backend. The planner supplies the bucket and
a per-migration prefix during `terraform init`; credentials come from workload
identity rather than generated files.

## Plan execution

`internal/iac.Planner` uses HashiCorp's `terraform-exec` Go SDK rather than
shell commands. It:

1. acquires a per-migration process lock;
2. renders validated configuration;
3. initializes the partial GCS backend;
4. creates a binary plan with Terraform's state-lock timeout;
5. exports structured JSON and human-readable plan evidence;
6. encrypts the binary plan with AES-256-GCM;
7. stores immutable, checksum-addressed artifacts with private file modes; and
8. removes the plaintext workspace copy.

The GCS backend provides the cross-process state lock. The in-process lock
prevents duplicate execution inside one dispatcher replica.

## Durable lifecycle and approval

`POST /v1/migrations/{id}/plans` records an idempotent plan request in
Postgres. A separate dispatcher claims queued requests with `SKIP LOCKED`,
maintains an expiring lease while Terraform runs, and rejects stale-worker
completion. Failed planning can be reclaimed after lease expiry without
running two active owners.

Policy is evaluated before work becomes claimable. The default policy limits
regions and maximum instances; API middleware separately authenticates the
caller and enforces role authorization. Rejected requests remain visible for
audit but never invoke Terraform.

A ready plan can be approved only when its policy passed and all plan
artifacts exist. Approval atomically changes the status and appends an audit row
containing the authenticated actor, timestamp, and all artifact checksums.

Only an authenticated `operator` may queue apply. A separate leased dispatcher
decrypts the binary, verifies its approval-bound SHA-256 checksum, initializes
the same GCS state prefix, and passes that exact plan to `terraform apply`.
Plaintext binary plans exist only inside a private, short-lived workspace.
Apply requests are recorded in their own append-only audit table.

Enable the planner with:

```sh
export CLOUDIFY_TERRAFORM_ENABLED=true
export CLOUDIFY_TERRAFORM_STATE_BUCKET=cloudify-terraform-state
export CLOUDIFY_TERRAFORM_ARTIFACT_ROOT=/durable/cloudify-plan-artifacts
export CLOUDIFY_TERRAFORM_ARTIFACT_KEY="$(openssl rand -base64 32)"
export CLOUDIFY_AUTH_TOKENS_JSON='[{"actor":"platform@example.com","token_sha256":"...","roles":["planner","approver","operator"]}]'
go run ./cmd/control-plane
```

Optional settings include `CLOUDIFY_TERRAFORM_BINARY` and
`CLOUDIFY_TERRAFORM_WORK_ROOT`. The file artifact implementation expects its
root to be durable shared storage in multi-replica deployments. The encryption
key must come from Secret Manager or an equivalent runtime secret source and
must remain stable for the lifetime of stored plans.

## Verification

Go tests cover validation, deterministic rendering, path traversal, private
artifact permissions, lock cancellation, bounded command output, and planner
orchestration, authenticated role checks, encrypted artifact tampering, and
exact-plan apply. Store and API tests cover idempotency, policy rejection,
leasing, approval/apply preconditions, and the Postgres lifecycle. CI also runs
`terraform fmt`, initializes the module without a backend, and runs `terraform
validate`.
