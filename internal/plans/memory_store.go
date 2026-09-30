package plans

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/iac"
)

type MemoryStore struct {
	mu            sync.RWMutex
	byID          map[string]Plan
	byIdempotency map[string]string
}

func (store *MemoryStore) ListApplied(_ context.Context, after time.Time, afterID string, limit int) ([]Plan, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]Plan, 0)
	for _, plan := range store.byID {
		if plan.Status != StatusApplied || plan.UpdatedAt.Before(after) || (plan.UpdatedAt.Equal(after) && plan.ID <= afterID) {
			continue
		}
		result = append(result, plan)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].UpdatedAt.Before(result[j].UpdatedAt)
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byID: make(map[string]Plan), byIdempotency: make(map[string]string)}
}

func (store *MemoryStore) CreateOrGet(_ context.Context, candidate Plan) (Plan, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := candidate.MigrationID + "\x00" + candidate.IdempotencyKey
	if id, exists := store.byIdempotency[key]; exists {
		existing := store.byID[id]
		if existing.RequestHash != candidate.RequestHash {
			return Plan{}, false, ErrIdempotency
		}
		return existing, false, nil
	}
	store.byID[candidate.ID] = candidate
	store.byIdempotency[key] = candidate.ID
	return candidate, true, nil
}

func (store *MemoryStore) Get(_ context.Context, id string) (Plan, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	plan, exists := store.byID[id]
	if !exists {
		return Plan{}, ErrNotFound
	}
	return plan, nil
}

func (store *MemoryStore) ClaimNext(_ context.Context, workerID string, now, leaseUntil time.Time) (Plan, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var selected Plan
	found := false
	for _, plan := range store.byID {
		eligible := plan.Status == StatusQueued ||
			(plan.Status == StatusPlanning && plan.LeaseExpiresAt != nil && !plan.LeaseExpiresAt.After(now))
		if eligible && (!found || plan.CreatedAt.Before(selected.CreatedAt)) {
			selected = plan
			found = true
		}
	}
	if !found {
		return Plan{}, false, nil
	}
	selected.Status = StatusPlanning
	selected.ClaimedBy = workerID
	selected.LeaseExpiresAt = timePointer(leaseUntil)
	selected.AttemptCount++
	selected.UpdatedAt = now
	store.byID[selected.ID] = selected
	return selected, true, nil
}

func (store *MemoryStore) RenewLease(_ context.Context, id, workerID string, now, leaseUntil time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	plan, exists := store.byID[id]
	if !exists {
		return ErrNotFound
	}
	if (plan.Status != StatusPlanning && plan.Status != StatusApplying) || plan.ClaimedBy != workerID || plan.LeaseExpiresAt == nil || !plan.LeaseExpiresAt.After(now) {
		return ErrLeaseLost
	}
	plan.LeaseExpiresAt = timePointer(leaseUntil)
	plan.UpdatedAt = now
	store.byID[id] = plan
	return nil
}

func (store *MemoryStore) QueueApply(_ context.Context, id, actor string, now time.Time) (Plan, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	plan, exists := store.byID[id]
	if !exists {
		return Plan{}, ErrNotFound
	}
	if plan.Status != StatusApproved || !validArtifact(plan.Artifact) {
		return Plan{}, ErrInvalidTransition
	}
	plan.Status = StatusApplyQueued
	plan.ApplyRequestedBy = actor
	plan.ApplyRequestedAt = timePointer(now)
	plan.UpdatedAt = now
	store.byID[id] = plan
	return plan, nil
}

func (store *MemoryStore) ClaimNextApply(_ context.Context, workerID string, now, leaseUntil time.Time) (Plan, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var selected Plan
	found := false
	for _, plan := range store.byID {
		eligible := plan.Status == StatusApplyQueued ||
			(plan.Status == StatusApplying && plan.LeaseExpiresAt != nil && !plan.LeaseExpiresAt.After(now))
		if eligible && (!found || plan.CreatedAt.Before(selected.CreatedAt)) {
			selected, found = plan, true
		}
	}
	if !found {
		return Plan{}, false, nil
	}
	selected.Status = StatusApplying
	selected.ClaimedBy = workerID
	selected.LeaseExpiresAt = timePointer(leaseUntil)
	selected.ApplyAttemptCount++
	selected.UpdatedAt = now
	store.byID[selected.ID] = selected
	return selected, true, nil
}

func (store *MemoryStore) CompleteApply(
	_ context.Context, id, workerID string, status Status, failureMessage string, now time.Time,
) (Plan, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	plan, exists := store.byID[id]
	if !exists {
		return Plan{}, ErrNotFound
	}
	if plan.Status != StatusApplying || plan.ClaimedBy != workerID {
		return Plan{}, ErrLeaseLost
	}
	if status != StatusApplied && status != StatusApplyFailed {
		return Plan{}, ErrInvalidTransition
	}
	plan.Status = status
	plan.FailureMessage = failureMessage
	plan.ClaimedBy = ""
	plan.LeaseExpiresAt = nil
	plan.UpdatedAt = now
	store.byID[id] = plan
	return plan, nil
}

func (store *MemoryStore) Complete(
	_ context.Context,
	id, workerID string,
	status Status,
	hasChanges bool,
	artifact *iac.ArtifactMetadata,
	failureMessage string,
	now time.Time,
) (Plan, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	plan, exists := store.byID[id]
	if !exists {
		return Plan{}, ErrNotFound
	}
	if plan.Status != StatusPlanning || plan.ClaimedBy != workerID {
		return Plan{}, ErrLeaseLost
	}
	if status != StatusReady && status != StatusFailed {
		return Plan{}, ErrInvalidTransition
	}
	if status == StatusReady && !validArtifact(artifact) {
		return Plan{}, ErrInvalidTransition
	}
	plan.Status = status
	plan.ClaimedBy = ""
	plan.LeaseExpiresAt = nil
	plan.UpdatedAt = now
	plan.FailureMessage = failureMessage
	if status == StatusReady {
		plan.HasChanges = boolPointer(hasChanges)
		plan.Artifact = artifact
	}
	store.byID[id] = plan
	return plan, nil
}

func (store *MemoryStore) Approve(_ context.Context, id, actor string, now time.Time) (Plan, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	plan, exists := store.byID[id]
	if !exists {
		return Plan{}, ErrNotFound
	}
	if plan.Status != StatusReady || !plan.Policy.Allowed || !validArtifact(plan.Artifact) {
		return Plan{}, ErrInvalidTransition
	}
	plan.Status = StatusApproved
	plan.ApprovedBy = actor
	plan.ApprovedAt = timePointer(now)
	plan.UpdatedAt = now
	store.byID[id] = plan
	return plan, nil
}

func timePointer(value time.Time) *time.Time { return &value }
func boolPointer(value bool) *bool           { return &value }
