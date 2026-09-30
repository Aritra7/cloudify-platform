# Cloud Run reconciliation

The optional Go reconciler compares every managed Cloud Run service with the
deployment specification last applied through Terraform. Enable it alongside
the Terraform workers:

```sh
export CLOUDIFY_TERRAFORM_ENABLED=true
export CLOUDIFY_RECONCILER_ENABLED=true
go run ./cmd/control-plane
```

The Cloud Run v2 client uses Application Default Credentials. Its runtime
identity needs permission to read Cloud Run services and their IAM policies;
Terraform execution retains the separate permissions required to plan and
apply the desired configuration.

For each due resource, the controller:

1. claims a PostgreSQL row with `FOR UPDATE SKIP LOCKED` and renews its lease;
2. reads the Cloud Run revision template and service IAM policy;
3. compares the image digest, service account, CPU, memory, revision scaling,
   environment variables, Secret Manager references, and public access;
4. classifies the resource as `in_sync`, `drifted`, `missing`, or `error`;
5. persists sanitized observed state, observed generation, and conditions; and
6. for `automatic` resources, advances an idempotent Terraform remediation
   plan through the existing policy, approval, and guarded apply workflow.

The observer never reads secret values. A deleted service is classified as
`missing`, while transient API failures use exponential backoff with stable
jitter. Permanent failures receive an actionable condition and stop automatic
retry. Multiple controller replicas can run safely because completion and
lease renewal reject stale owners.

Remediation never patches Cloud Run directly. Terraform remains the desired
state owner, and normal approval/apply audit records identify
`cloudify-reconciler` as the actor.
