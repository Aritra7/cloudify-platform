package resources

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Controller struct {
	Store             Store
	Observer          Observer
	Remediator        Remediator
	WorkerID          string
	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration
	PollInterval      time.Duration
	ReconcileInterval time.Duration
	PendingInterval   time.Duration
	Now               func() time.Time
}

func (controller *Controller) Run(ctx context.Context) error {
	if err := controller.validate(); err != nil {
		return err
	}
	for {
		processed, err := controller.RunOnce(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if processed {
			continue
		}
		timer := time.NewTimer(controller.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (controller *Controller) RunOnce(ctx context.Context) (bool, error) {
	if err := controller.validate(); err != nil {
		return false, err
	}
	now := controller.Now()
	resource, claimed, err := controller.Store.ClaimNext(ctx, controller.WorkerID, now, now.Add(controller.LeaseDuration))
	if err != nil || !claimed {
		return claimed, err
	}
	reconcileContext, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan ReconcileResult, 1)
	go func() { results <- controller.reconcile(reconcileContext, resource) }()
	heartbeat := time.NewTicker(controller.HeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case result := <-results:
			if _, err := controller.Store.Complete(ctx, resource.ID, controller.WorkerID, result, controller.Now()); err != nil {
				return true, fmt.Errorf("complete managed resource reconciliation: %w", err)
			}
			return true, nil
		case <-heartbeat.C:
			heartbeatTime := controller.Now()
			if err := controller.Store.RenewLease(
				ctx, resource.ID, controller.WorkerID, heartbeatTime, heartbeatTime.Add(controller.LeaseDuration),
			); err != nil {
				cancel()
				<-results
				return true, fmt.Errorf("renew managed resource lease: %w", err)
			}
		case <-ctx.Done():
			cancel()
			<-results
			return true, ctx.Err()
		}
	}
}

func (controller *Controller) reconcile(ctx context.Context, resource Resource) ReconcileResult {
	now := controller.Now()
	observed, err := controller.Observer.Observe(ctx, resource)
	if err != nil {
		return controller.observationFailure(resource, err, now)
	}
	encoded, err := json.Marshal(observed)
	if err != nil {
		return controller.observationFailure(resource, err, now)
	}
	state := classify(resource.Desired, observed)
	result := ReconcileResult{
		Observed: encoded, State: state, ObservedGeneration: resource.Generation,
		RetryCount: 0, NextReconcileAt: now.Add(controller.ReconcileInterval), LastReconciledAt: now,
	}
	if state == StateInSync {
		result.Conditions = []Condition{condition("Ready", "True", "InSync", "observed Cloud Run state matches desired state", now)}
		return result
	}
	reason, message := "ConfigurationDrift", "observed Cloud Run configuration differs from desired state"
	if state == StateMissing {
		reason, message = "ResourceMissing", "Cloud Run service does not exist"
	}
	result.Conditions = []Condition{condition("Ready", "False", reason, message, now)}
	if resource.RemediationPolicy != RemediationAutomatic || controller.Remediator == nil {
		return result
	}
	err = controller.Remediator.Remediate(ctx, resource, observed)
	if err == nil || errors.Is(err, ErrRemediationPending) {
		result.NextReconcileAt = now.Add(controller.PendingInterval)
		result.Conditions = append(result.Conditions, condition(
			"Remediation", "False", "TerraformPending", "Terraform remediation is queued or in progress", now,
		))
		return result
	}
	if errors.Is(err, ErrRemediationTerminal) {
		result.NextReconcileAt = permanentRetryTime()
		result.Conditions = append(result.Conditions, condition(
			"Remediation", "False", "TerraformTerminal", "Terraform remediation requires operator intervention", now,
		))
		return result
	}
	result.RetryCount = resource.RetryCount + 1
	result.NextReconcileAt = now.Add(retryDelay(resource.ID, result.RetryCount))
	result.Conditions = append(result.Conditions, condition(
		"Remediation", "False", "TerraformFailed", "Terraform remediation could not be advanced", now,
	))
	return result
}

func (controller *Controller) observationFailure(resource Resource, observationErr error, now time.Time) ReconcileResult {
	retryCount := resource.RetryCount
	next := permanentRetryTime()
	reason := "PermanentObservationFailure"
	message := "Cloud Run observation requires operator intervention; verify credentials and IAM permissions"
	if transient(observationErr) {
		retryCount++
		next = now.Add(retryDelay(resource.ID, retryCount))
		reason = "TransientObservationFailure"
		message = "Cloud Run observation failed transiently; retry is scheduled"
	}
	return ReconcileResult{
		Observed: resource.Observed, State: StateError, ObservedGeneration: resource.ObservedGeneration,
		RetryCount: retryCount, NextReconcileAt: next, LastReconciledAt: now,
		Conditions: []Condition{condition("Ready", "False", reason, message, now)},
	}
}

func permanentRetryTime() time.Time {
	return time.Date(9999, time.December, 31, 0, 0, 0, 0, time.UTC)
}

func (controller *Controller) validate() error {
	if controller.Store == nil || controller.Observer == nil || controller.WorkerID == "" {
		return errors.New("resource controller requires a store, observer, and worker ID")
	}
	if controller.LeaseDuration <= 0 || controller.HeartbeatInterval <= 0 || controller.PollInterval <= 0 ||
		controller.ReconcileInterval <= 0 || controller.PendingInterval <= 0 {
		return errors.New("resource controller durations must be positive")
	}
	if controller.HeartbeatInterval >= controller.LeaseDuration {
		return errors.New("resource controller heartbeat must be shorter than its lease")
	}
	if controller.Now == nil {
		controller.Now = func() time.Time { return time.Now().UTC() }
	}
	return nil
}

func condition(conditionType, conditionStatus, reason, message string, now time.Time) Condition {
	return Condition{Type: conditionType, Status: conditionStatus, Reason: reason, Message: message, LastTransitionTime: now}
}

func transient(err error) bool {
	switch status.Code(err) {
	case codes.Aborted, codes.DeadlineExceeded, codes.Internal, codes.ResourceExhausted, codes.Unavailable:
		return true
	default:
		return errors.Is(err, context.DeadlineExceeded)
	}
}

func retryDelay(resourceID string, retryCount int) time.Duration {
	if retryCount < 1 {
		retryCount = 1
	}
	exponent := retryCount - 1
	if exponent > 8 {
		exponent = 8
	}
	base := 5 * time.Second * time.Duration(1<<exponent)
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", resourceID, retryCount)))
	jitter := time.Duration(binary.BigEndian.Uint16(digest[:2])%2500) * base / 10000
	return base + jitter
}
