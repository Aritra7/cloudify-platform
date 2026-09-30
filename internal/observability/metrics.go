package observability

import (
	"fmt"
	"io"
	"sync/atomic"
)

// Metrics contains process-local dispatcher metrics exposed in Prometheus format.
type Metrics struct {
	claims             atomic.Uint64
	succeeded          atomic.Uint64
	failed             atomic.Uint64
	cancelled          atomic.Uint64
	workerFailures     atomic.Uint64
	leaseRenewFailures atomic.Uint64
	inflight           atomic.Int64
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
`, m.claims.Load(), m.succeeded.Load(), m.failed.Load(), m.cancelled.Load(),
		m.workerFailures.Load(), m.leaseRenewFailures.Load(), m.inflight.Load())
	return err
}
