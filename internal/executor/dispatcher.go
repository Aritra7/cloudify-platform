package executor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/migrations"
	"github.com/Aritra7/cloudify-platform/internal/observability"
)

// Worker executes one claimed migration and must honor context cancellation.
type Worker interface {
	Run(context.Context, migrations.Migration) error
}

// Dispatcher claims durable work and maintains ownership while it executes.
type Dispatcher struct {
	Store             migrations.Store
	Worker            Worker
	WorkerID          string
	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration
	CancellationPoll  time.Duration
	PollInterval      time.Duration
	Now               func() time.Time
	Metrics           *observability.Metrics
}

// Run polls until its context is cancelled.
func (d *Dispatcher) Run(ctx context.Context) error {
	if err := d.validate(); err != nil {
		return err
	}

	for {
		processed, err := d.RunOnce(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if processed {
			continue
		}

		timer := time.NewTimer(d.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// RunOnce claims and executes at most one migration.
func (d *Dispatcher) RunOnce(ctx context.Context) (bool, error) {
	if err := d.validate(); err != nil {
		return false, err
	}

	now := d.Now()
	migration, claimed, err := d.Store.ClaimNext(ctx, d.WorkerID, now, now.Add(d.LeaseDuration))
	if err != nil || !claimed {
		return claimed, err
	}
	if d.Metrics != nil {
		d.Metrics.Claimed()
		defer d.Metrics.Released()
	}

	workerContext, cancelWorker := context.WithCancel(ctx)
	defer cancelWorker()
	workerResult := make(chan error, 1)
	go func() {
		workerResult <- d.Worker.Run(workerContext, migration)
	}()

	heartbeat := time.NewTicker(d.HeartbeatInterval)
	defer heartbeat.Stop()
	cancellationPoll := time.NewTicker(d.CancellationPoll)
	defer cancellationPoll.Stop()

	for {
		select {
		case workerErr := <-workerResult:
			current, err := d.Store.Get(ctx, migration.ID)
			if err != nil {
				return true, fmt.Errorf("read migration before completion: %w", err)
			}

			target := migrations.StatusSucceeded
			if current.Status == migrations.StatusCancelling {
				target = migrations.StatusCancelled
			} else if workerErr != nil {
				target = migrations.StatusFailed
			}
			if target == migrations.StatusFailed && d.Metrics != nil {
				d.Metrics.WorkerFailed()
			}
			if _, err := d.Store.Complete(ctx, migration.ID, d.WorkerID, target, d.Now()); err != nil {
				return true, fmt.Errorf("complete migration: %w", err)
			}
			if d.Metrics != nil {
				d.Metrics.Completed(string(target))
			}
			return true, nil

		case <-heartbeat.C:
			heartbeatTime := d.Now()
			if err := d.Store.RenewLease(
				ctx,
				migration.ID,
				d.WorkerID,
				heartbeatTime,
				heartbeatTime.Add(d.LeaseDuration),
			); err != nil {
				cancelWorker()
				if d.Metrics != nil {
					d.Metrics.LeaseRenewFailed()
				}
				return true, fmt.Errorf("renew migration lease: %w", err)
			}

		case <-cancellationPoll.C:
			current, err := d.Store.Get(ctx, migration.ID)
			if err != nil {
				cancelWorker()
				return true, fmt.Errorf("poll migration cancellation: %w", err)
			}
			if current.Status != migrations.StatusCancelling {
				continue
			}

			cancelWorker()
			select {
			case <-workerResult:
			case <-ctx.Done():
				return true, ctx.Err()
			}
			if _, err := d.Store.Complete(
				ctx,
				migration.ID,
				d.WorkerID,
				migrations.StatusCancelled,
				d.Now(),
			); err != nil {
				return true, fmt.Errorf("complete migration cancellation: %w", err)
			}
			if d.Metrics != nil {
				d.Metrics.Completed(string(migrations.StatusCancelled))
			}
			return true, nil

		case <-ctx.Done():
			cancelWorker()
			return true, ctx.Err()
		}
	}
}

func (d *Dispatcher) validate() error {
	if d.Store == nil || d.Worker == nil || d.WorkerID == "" {
		return errors.New("dispatcher requires a store, worker, and worker ID")
	}
	if d.LeaseDuration <= 0 || d.HeartbeatInterval <= 0 || d.CancellationPoll <= 0 || d.PollInterval <= 0 {
		return errors.New("dispatcher durations must be positive")
	}
	if d.HeartbeatInterval >= d.LeaseDuration {
		return errors.New("heartbeat interval must be shorter than the lease duration")
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	return nil
}
