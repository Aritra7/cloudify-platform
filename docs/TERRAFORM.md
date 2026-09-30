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
6. stores those artifacts with SHA-256 checksums and private file modes; and
7. removes the binary plan, which can contain cleartext sensitive values.

The GCS backend provides the cross-process state lock. The in-process lock
prevents duplicate execution inside one dispatcher replica. Apply remains
intentionally disabled until policy approval and durable plan records are wired
into the migration lifecycle.

## Verification

Go tests cover validation, deterministic rendering, path traversal, private
artifact permissions, lock cancellation, bounded command output, and planner
orchestration. CI also runs `terraform fmt`, initializes the module without a
backend, and runs `terraform validate`.
