package plans

import (
	"context"
	"sync"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/iac"
)

type MemoryStore struct {
	mu            sync.RWMutex
	byID          map[string]Plan
	byIdempotency map[string]string
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
	if plan.Status != StatusPlanning || plan.ClaimedBy != workerID || plan.LeaseExpiresAt == nil || !plan.LeaseExpiresAt.After(now) {
		return ErrLeaseLost
	}
	plan.LeaseExpiresAt = timePointer(leaseUntil)
	plan.UpdatedAt = now
	store.byID[id] = plan
	return nil
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
