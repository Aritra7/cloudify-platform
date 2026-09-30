package resources

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"time"
)

type MemoryStore struct {
	mu   sync.RWMutex
	byID map[string]Resource
}

func (store *MemoryStore) ClaimNext(_ context.Context, workerID string, now, leaseUntil time.Time) (Resource, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var selected Resource
	found := false
	for _, resource := range store.byID {
		claimable := !resource.NextReconcileAt.After(now) &&
			(resource.ClaimedBy == "" || resource.LeaseExpiresAt == nil || !resource.LeaseExpiresAt.After(now))
		if claimable && (!found || resource.NextReconcileAt.Before(selected.NextReconcileAt)) {
			selected, found = resource, true
		}
	}
	if !found {
		return Resource{}, false, nil
	}
	selected.ClaimedBy = workerID
	selected.LeaseExpiresAt = timePointer(leaseUntil)
	selected.UpdatedAt = now
	store.byID[selected.ID] = selected
	return selected, true, nil
}

func (store *MemoryStore) RenewLease(_ context.Context, id, workerID string, now, leaseUntil time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	resource, exists := store.byID[id]
	if !exists {
		return ErrNotFound
	}
	if resource.ClaimedBy != workerID || resource.LeaseExpiresAt == nil || !resource.LeaseExpiresAt.After(now) {
		return ErrLeaseLost
	}
	resource.LeaseExpiresAt = timePointer(leaseUntil)
	resource.UpdatedAt = now
	store.byID[id] = resource
	return nil
}

func (store *MemoryStore) Complete(_ context.Context, id, workerID string, result ReconcileResult, now time.Time) (Resource, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	resource, exists := store.byID[id]
	if !exists {
		return Resource{}, ErrNotFound
	}
	if resource.ClaimedBy != workerID {
		return Resource{}, ErrLeaseLost
	}
	resource.Observed = append(resource.Observed[:0], result.Observed...)
	resource.State = result.State
	resource.ObservedGeneration = result.ObservedGeneration
	resource.Conditions = append([]Condition(nil), result.Conditions...)
	resource.RetryCount = result.RetryCount
	resource.NextReconcileAt = result.NextReconcileAt
	resource.LastReconciledAt = timePointer(result.LastReconciledAt)
	resource.ClaimedBy = ""
	resource.LeaseExpiresAt = nil
	resource.UpdatedAt = now
	store.byID[id] = resource
	return resource, nil
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{byID: make(map[string]Resource)} }

func (store *MemoryStore) UpsertApplied(_ context.Context, candidate Resource) (Resource, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	existing, exists := store.byID[candidate.ID]
	if !exists {
		store.byID[candidate.ID] = candidate
		return candidate, true, nil
	}
	if existing.SourcePlanID == candidate.SourcePlanID {
		return existing, false, nil
	}
	existing.MigrationID = candidate.MigrationID
	existing.SourcePlanID = candidate.SourcePlanID
	desiredChanged := !reflect.DeepEqual(existing.Desired, candidate.Desired)
	existing.Desired = candidate.Desired
	if desiredChanged {
		existing.Generation++
	}
	existing.State = StateUnknown
	existing.ObservedGeneration = 0
	existing.Conditions = []Condition{}
	existing.RetryCount = 0
	existing.NextReconcileAt = candidate.NextReconcileAt
	existing.UpdatedAt = candidate.UpdatedAt
	store.byID[candidate.ID] = existing
	return existing, true, nil
}

func (store *MemoryStore) Get(_ context.Context, id string) (Resource, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	resource, exists := store.byID[id]
	if !exists {
		return Resource{}, ErrNotFound
	}
	return resource, nil
}

func (store *MemoryStore) List(_ context.Context, limit int) ([]Resource, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]Resource, 0, len(store.byID))
	for _, resource := range store.byID {
		result = append(result, resource)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func timePointer(value time.Time) *time.Time { return &value }
