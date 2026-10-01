# Kubernetes operator

The Cloudify operator reconciles namespaced `Migration` custom resources into
the Go control-plane API. It uses a generation-scoped idempotency key, records
the remote migration ID and phase in the status subresource, and publishes
standard Kubernetes conditions and Events.

```sh
helm install cloudify ./deploy/helm/cloudify-operator \
  --namespace cloudify-system --create-namespace \
  --set controlPlane.endpoint=https://cloudify.example.com

kubectl apply -f examples/kubernetes/migration.yaml
kubectl get migrations
```

Create the token Secret named by `controlPlane.tokenSecretName` before
installing the chart. The chart never accepts the token as a Helm value.

The controller adds `cloudify.dev/migration-finalizer`. Deleting a custom
resource first cancels any active remote migration, waits for a terminal state,
and only then removes the finalizer. A specification update cancels the prior
generation before submitting a new operation, preventing stale status writes.

The chart defaults to two replicas with lease-based leader election, minimal
RBAC, dropped Linux capabilities, a read-only root filesystem, non-root
execution, seccomp, health probes, resource limits, and ingress/egress network
policy. The operator controls the orchestration object; workload execution
continues to use the control plane's isolated worker and Terraform dispatchers.
