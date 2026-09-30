package plans

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/iac"
)

type Applier interface {
	Apply(context.Context, string, iac.DeploymentSpec, iac.ArtifactMetadata) error
}

type ApplyDispatcher struct {
	Store             Store
	Applier           Applier
	WorkerID          string
	WorkRoot          string
	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration
	PollInterval      time.Duration
	Now               func() time.Time
}

func (dispatcher *ApplyDispatcher) Run(ctx context.Context) error {
	if err := dispatcher.validate(); err != nil {
		return err
	}
	for {
		processed, err := dispatcher.RunOnce(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if processed {
			continue
		}
		timer := time.NewTimer(dispatcher.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (dispatcher *ApplyDispatcher) RunOnce(ctx context.Context) (bool, error) {
	if err := dispatcher.validate(); err != nil {
		return false, err
	}
	now := dispatcher.Now()
	plan, claimed, err := dispatcher.Store.ClaimNextApply(ctx, dispatcher.WorkerID, now, now.Add(dispatcher.LeaseDuration))
	if err != nil || !claimed {
		return claimed, err
	}
	if !validArtifact(plan.Artifact) {
		_, completeErr := dispatcher.Store.CompleteApply(
			ctx, plan.ID, dispatcher.WorkerID, StatusApplyFailed, "Terraform apply failed", dispatcher.Now(),
		)
		return true, completeErr
	}
	workspace, err := os.MkdirTemp(dispatcher.WorkRoot, "cloudify-apply-"+plan.ID+"-")
	if err != nil {
		_, completeErr := dispatcher.Store.CompleteApply(ctx, plan.ID, dispatcher.WorkerID, StatusApplyFailed, "Terraform apply failed", dispatcher.Now())
		return true, errors.Join(err, completeErr)
	}
	defer func() { _ = os.RemoveAll(workspace) }()

	results := make(chan error, 1)
	applyContext, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		results <- dispatcher.Applier.Apply(applyContext, workspace, plan.Specification, *plan.Artifact)
	}()
	heartbeat := time.NewTicker(dispatcher.HeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case applyErr := <-results:
			status := StatusApplied
			failureMessage := ""
			if applyErr != nil {
				status = StatusApplyFailed
				failureMessage = "Terraform apply failed"
			}
			if _, err := dispatcher.Store.CompleteApply(
				ctx, plan.ID, dispatcher.WorkerID, status, failureMessage, dispatcher.Now(),
			); err != nil {
				return true, fmt.Errorf("complete Terraform apply: %w", err)
			}
			return true, nil
		case <-heartbeat.C:
			heartbeatTime := dispatcher.Now()
			if err := dispatcher.Store.RenewLease(
				ctx, plan.ID, dispatcher.WorkerID, heartbeatTime, heartbeatTime.Add(dispatcher.LeaseDuration),
			); err != nil {
				cancel()
				<-results
				return true, fmt.Errorf("renew Terraform apply lease: %w", err)
			}
		case <-ctx.Done():
			cancel()
			<-results
			return true, ctx.Err()
		}
	}
}

func (dispatcher *ApplyDispatcher) validate() error {
	if dispatcher.Store == nil || dispatcher.Applier == nil || dispatcher.WorkerID == "" {
		return errors.New("apply dispatcher requires a store, applier, and worker ID")
	}
	if dispatcher.WorkRoot == "" || !filepath.IsAbs(dispatcher.WorkRoot) {
		return errors.New("apply dispatcher work root must be an absolute path")
	}
	if dispatcher.LeaseDuration <= 0 || dispatcher.HeartbeatInterval <= 0 || dispatcher.PollInterval <= 0 {
		return errors.New("apply dispatcher durations must be positive")
	}
	if dispatcher.HeartbeatInterval >= dispatcher.LeaseDuration {
		return errors.New("apply dispatcher heartbeat must be shorter than its lease")
	}
	if dispatcher.Now == nil {
		dispatcher.Now = func() time.Time { return time.Now().UTC() }
	}
	return nil
}
