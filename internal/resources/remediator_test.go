package resources

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/iac"
	"github.com/Aritra7/cloudify-platform/internal/plans"
)

func TestPlanRemediatorAdvancesGuardedTerraformWorkflow(t *testing.T) {
	t.Parallel()
	planStore := plans.NewMemoryStore()
	planService := plans.NewService(planStore, plans.DefaultPolicy())
	remediator := &PlanRemediator{Plans: planService, Actor: "cloudify-reconciler"}
	_, resource := projectedResource(t)
	observed := matchingObservation(resource)
	observed.Image = "us-docker.pkg.dev/example-project/apps/api@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	if err := remediator.Remediate(context.Background(), resource, observed); !errors.Is(err, ErrRemediationPending) {
		t.Fatalf("create remediation error = %v", err)
	}
	now := time.Unix(300, 0).UTC()
	plan, claimed, err := planStore.ClaimNext(context.Background(), "planner", now, now.Add(time.Minute))
	if err != nil || !claimed {
		t.Fatalf("claim remediation plan = (%v, %v)", claimed, err)
	}
	artifact := &iac.ArtifactMetadata{
		MigrationID: resource.MigrationID, JSONPath: "/plan.json", TextPath: "/plan.txt",
		JSONSHA256:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		TextSHA256:      "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		BinaryObjectKey: resource.MigrationID + "/plan.enc",
		BinarySHA256:    "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		CreatedAt:       now,
	}
	if _, err := planStore.Complete(context.Background(), plan.ID, "planner", plans.StatusReady, true, artifact, "", now); err != nil {
		t.Fatalf("complete remediation plan: %v", err)
	}
	if err := remediator.Remediate(context.Background(), resource, observed); !errors.Is(err, ErrRemediationPending) {
		t.Fatalf("approve and queue remediation error = %v", err)
	}
	applying, claimed, err := planStore.ClaimNextApply(context.Background(), "applier", now, now.Add(time.Minute))
	if err != nil || !claimed || applying.ApprovedBy != "cloudify-reconciler" || applying.ApplyRequestedBy != "cloudify-reconciler" {
		t.Fatalf("claim remediation apply = (%#v, %v, %v)", applying, claimed, err)
	}
	if _, err := planStore.CompleteApply(context.Background(), plan.ID, "applier", plans.StatusApplied, "", now); err != nil {
		t.Fatalf("complete remediation apply: %v", err)
	}
	if err := remediator.Remediate(context.Background(), resource, observed); err != nil {
		t.Fatalf("completed remediation error = %v", err)
	}
}
