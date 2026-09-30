package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Aritra7/cloudify-platform/internal/plans"
)

var ErrRemediationPending = errors.New("Terraform remediation is pending")
var ErrRemediationTerminal = errors.New("Terraform remediation reached a terminal failure")

type Remediator interface {
	Remediate(context.Context, Resource, Observation) error
}

// PlanRemediator advances drift through the same policy, approval, audit, and
// exact-plan apply workflow used by interactive requests.
type PlanRemediator struct {
	Plans *plans.Service
	Actor string
}

func (remediator *PlanRemediator) Remediate(ctx context.Context, resource Resource, observed Observation) error {
	if remediator.Plans == nil || remediator.Actor == "" {
		return errors.New("plan remediator requires a plan service and actor")
	}
	fingerprint, err := observationFingerprint(observed)
	if err != nil {
		return err
	}
	idempotencyKey := fmt.Sprintf("reconcile-%s-%d-%s", resource.ID, resource.Generation, fingerprint)
	plan, _, err := remediator.Plans.Create(ctx, resource.MigrationID, idempotencyKey, resource.Desired)
	if err != nil {
		return fmt.Errorf("create remediation plan: %w", err)
	}
	switch plan.Status {
	case plans.StatusReady:
		plan, err = remediator.Plans.Approve(ctx, plan.ID, remediator.Actor)
		if err != nil {
			return fmt.Errorf("approve remediation plan: %w", err)
		}
		fallthrough
	case plans.StatusApproved:
		if _, err := remediator.Plans.QueueApply(ctx, plan.ID, remediator.Actor); err != nil {
			return fmt.Errorf("queue remediation apply: %w", err)
		}
		return ErrRemediationPending
	case plans.StatusQueued, plans.StatusPlanning, plans.StatusApplyQueued, plans.StatusApplying:
		return ErrRemediationPending
	case plans.StatusApplied:
		return nil
	case plans.StatusRejected, plans.StatusFailed, plans.StatusApplyFailed:
		return fmt.Errorf("%w: status %q", ErrRemediationTerminal, plan.Status)
	default:
		return fmt.Errorf("remediation plan has unsupported status %q", plan.Status)
	}
}

func observationFingerprint(observed Observation) (string, error) {
	encoded, err := json.Marshal(observed)
	if err != nil {
		return "", fmt.Errorf("encode observed state fingerprint: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:6]), nil
}
