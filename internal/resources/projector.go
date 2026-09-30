package resources

import (
	"context"
	"errors"
	"time"
)

// Projector materializes applied Terraform plans as managed resources. Its
// cursor is only an optimization: a restart safely replays idempotent upserts.
type Projector struct {
	Plans        AppliedPlanSource
	Resources    *Service
	PollInterval time.Duration
	BatchSize    int
	afterTime    time.Time
	afterID      string
}

func (projector *Projector) Run(ctx context.Context) error {
	if err := projector.validate(); err != nil {
		return err
	}
	for {
		processed, err := projector.RunOnce(ctx)
		if err != nil {
			return err
		}
		if processed {
			continue
		}
		timer := time.NewTimer(projector.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (projector *Projector) RunOnce(ctx context.Context) (bool, error) {
	if err := projector.validate(); err != nil {
		return false, err
	}
	applied, err := projector.Plans.ListApplied(ctx, projector.afterTime, projector.afterID, projector.BatchSize)
	if err != nil {
		return false, err
	}
	for _, plan := range applied {
		if _, _, err := projector.Resources.ProjectApplied(ctx, plan); err != nil {
			return false, err
		}
		projector.afterTime, projector.afterID = plan.UpdatedAt, plan.ID
	}
	return len(applied) > 0, nil
}

func (projector *Projector) validate() error {
	if projector.Plans == nil || projector.Resources == nil {
		return errors.New("resource projector requires plan source and resource service")
	}
	if projector.PollInterval <= 0 || projector.BatchSize < 1 || projector.BatchSize > 1000 {
		return errors.New("resource projector interval and batch size are invalid")
	}
	return nil
}
