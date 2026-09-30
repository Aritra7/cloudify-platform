package observability

import (
	"bytes"
	"strings"
	"testing"
)

func TestMetricsRenderPrometheusCountersAndGauge(t *testing.T) {
	t.Parallel()
	metrics := &Metrics{}
	metrics.Claimed()
	metrics.WorkerFailed()
	metrics.LeaseRenewFailed()
	metrics.Completed("failed")

	var output bytes.Buffer
	if err := metrics.WritePrometheus(&output); err != nil {
		t.Fatalf("render metrics: %v", err)
	}
	for _, expected := range []string{
		"cloudify_dispatcher_claims_total 1",
		`cloudify_migration_completions_total{status="failed"} 1`,
		"cloudify_worker_failures_total 1",
		"cloudify_lease_renewal_failures_total 1",
		"cloudify_dispatcher_inflight 1",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("metrics missing %q:\n%s", expected, output.String())
		}
	}

	metrics.Released()
	output.Reset()
	if err := metrics.WritePrometheus(&output); err != nil {
		t.Fatalf("render released metrics: %v", err)
	}
	if !strings.Contains(output.String(), "cloudify_dispatcher_inflight 0") {
		t.Fatalf("released metrics missing zero gauge:\n%s", output.String())
	}
}
