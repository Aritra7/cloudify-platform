package observability

import (
	"fmt"
	"io"
	"sync/atomic"
)

// Metrics contains process-local dispatcher metrics exposed in Prometheus format.
type Metrics struct {
	claims              atomic.Uint64
	succeeded           atomic.Uint64
	failed              atomic.Uint64
	cancelled           atomic.Uint64
	workerFailures      atomic.Uint64
	leaseRenewFailures  atomic.Uint64
	inflight            atomic.Int64
	reconciledInSync    atomic.Uint64
	reconciledDrifted   atomic.Uint64
	reconciledMissing   atomic.Uint64
	reconciledError     atomic.Uint64
	remediationAttempts atomic.Uint64
	remediationFailures atomic.Uint64
}

func (m *Metrics) Claimed() {
	m.claims.Add(1)
	m.inflight.Add(1)
}

func (m *Metrics) Released() { m.inflight.Add(-1) }

func (m *Metrics) Completed(status string) {
	switch status {
	case "succeeded":
		m.succeeded.Add(1)
	case "failed":
		m.failed.Add(1)
	case "cancelled":
		m.cancelled.Add(1)
	}
}

func (m *Metrics) WorkerFailed()     { m.workerFailures.Add(1) }
func (m *Metrics) LeaseRenewFailed() { m.leaseRenewFailures.Add(1) }

func (m *Metrics) Reconciled(state string, remediationAttempted, remediationFailed bool) {
	switch state {
	case "in_sync":
		m.reconciledInSync.Add(1)
	case "drifted":
		m.reconciledDrifted.Add(1)
	case "missing":
		m.reconciledMissing.Add(1)
	case "error":
		m.reconciledError.Add(1)
	}
	if remediationAttempted {
		m.remediationAttempts.Add(1)
	}
	if remediationFailed {
		m.remediationFailures.Add(1)
	}
}

// WritePrometheus renders a stable, dependency-free Prometheus text endpoint.
func (m *Metrics) WritePrometheus(w io.Writer) error {
	_, err := fmt.Fprintf(w, `# HELP cloudify_dispatcher_claims_total Migrations claimed by this process.
# TYPE cloudify_dispatcher_claims_total counter
cloudify_dispatcher_claims_total %d
# HELP cloudify_migration_completions_total Completed migrations by terminal status.
# TYPE cloudify_migration_completions_total counter
cloudify_migration_completions_total{status="succeeded"} %d
cloudify_migration_completions_total{status="failed"} %d
cloudify_migration_completions_total{status="cancelled"} %d
# HELP cloudify_worker_failures_total Worker executions returning an error.
# TYPE cloudify_worker_failures_total counter
cloudify_worker_failures_total %d
# HELP cloudify_lease_renewal_failures_total Failed lease renewals.
# TYPE cloudify_lease_renewal_failures_total counter
cloudify_lease_renewal_failures_total %d
# HELP cloudify_dispatcher_inflight Current worker executions in this process.
# TYPE cloudify_dispatcher_inflight gauge
cloudify_dispatcher_inflight %d
# HELP cloudify_resource_reconciliations_total Completed managed-resource reconciliations.
# TYPE cloudify_resource_reconciliations_total counter
cloudify_resource_reconciliations_total{state="in_sync"} %d
cloudify_resource_reconciliations_total{state="drifted"} %d
cloudify_resource_reconciliations_total{state="missing"} %d
cloudify_resource_reconciliations_total{state="error"} %d
# HELP cloudify_remediation_attempts_total Terraform remediation attempts.
# TYPE cloudify_remediation_attempts_total counter
cloudify_remediation_attempts_total %d
# HELP cloudify_remediation_failures_total Terraform remediation attempts requiring retry or intervention.
# TYPE cloudify_remediation_failures_total counter
cloudify_remediation_failures_total %d
	`, m.claims.Load(), m.succeeded.Load(), m.failed.Load(), m.cancelled.Load(),
		m.workerFailures.Load(), m.leaseRenewFailures.Load(), m.inflight.Load(),
		m.reconciledInSync.Load(), m.reconciledDrifted.Load(),
		m.reconciledMissing.Load(), m.reconciledError.Load(), m.remediationAttempts.Load(), m.remediationFailures.Load())
	return err
}
