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

type Planner interface {
	Plan(context.Context, string, iac.DeploymentSpec) (iac.PlanResult, error)
}

type Dispatcher struct {
	Store             Store
	Planner           Planner
	WorkerID          string
	WorkRoot          string
	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration
	PollInterval      time.Duration
	Now               func() time.Time
}

func (dispatcher *Dispatcher) Run(ctx context.Context) error {
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

func (dispatcher *Dispatcher) RunOnce(ctx context.Context) (bool, error) {
	if err := dispatcher.validate(); err != nil {
		return false, err
	}
	now := dispatcher.Now()
	plan, claimed, err := dispatcher.Store.ClaimNext(ctx, dispatcher.WorkerID, now, now.Add(dispatcher.LeaseDuration))
	if err != nil || !claimed {
		return claimed, err
	}
	workspace, err := os.MkdirTemp(dispatcher.WorkRoot, "cloudify-plan-"+plan.ID+"-")
	if err != nil {
		_, completeErr := dispatcher.Store.Complete(ctx, plan.ID, dispatcher.WorkerID, StatusFailed, false, nil, "create isolated Terraform workspace", dispatcher.Now())
		return true, errors.Join(err, completeErr)
	}
	defer func() { _ = os.RemoveAll(workspace) }()

	type result struct {
		plan iac.PlanResult
		err  error
	}
	results := make(chan result, 1)
	planContext, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		planned, planErr := dispatcher.Planner.Plan(planContext, workspace, plan.Specification)
		results <- result{plan: planned, err: planErr}
	}()
	heartbeat := time.NewTicker(dispatcher.HeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case completed := <-results:
			status := StatusReady
			var artifact *iac.ArtifactMetadata
			failureMessage := ""
			if completed.err != nil {
				status = StatusFailed
				failureMessage = "Terraform planning failed"
			} else {
				artifact = &completed.plan.Artifact
			}
			if _, err := dispatcher.Store.Complete(
				ctx, plan.ID, dispatcher.WorkerID, status, completed.plan.HasChanges,
				artifact, failureMessage, dispatcher.Now(),
			); err != nil {
				return true, fmt.Errorf("complete Terraform plan: %w", err)
			}
			return true, nil
		case <-heartbeat.C:
			heartbeatTime := dispatcher.Now()
			if err := dispatcher.Store.RenewLease(
				ctx, plan.ID, dispatcher.WorkerID, heartbeatTime, heartbeatTime.Add(dispatcher.LeaseDuration),
			); err != nil {
				cancel()
				return true, fmt.Errorf("renew Terraform plan lease: %w", err)
			}
		case <-ctx.Done():
			cancel()
			return true, ctx.Err()
		}
	}
}

func (dispatcher *Dispatcher) validate() error {
	if dispatcher.Store == nil || dispatcher.Planner == nil || dispatcher.WorkerID == "" {
		return errors.New("plan dispatcher requires a store, planner, and worker ID")
	}
	if dispatcher.WorkRoot == "" || !filepath.IsAbs(dispatcher.WorkRoot) {
		return errors.New("plan dispatcher work root must be an absolute path")
	}
	if dispatcher.LeaseDuration <= 0 || dispatcher.HeartbeatInterval <= 0 || dispatcher.PollInterval <= 0 {
		return errors.New("plan dispatcher durations must be positive")
	}
	if dispatcher.HeartbeatInterval >= dispatcher.LeaseDuration {
		return errors.New("plan dispatcher heartbeat must be shorter than its lease")
	}
	if dispatcher.Now == nil {
		dispatcher.Now = func() time.Time { return time.Now().UTC() }
	}
	return nil
}
