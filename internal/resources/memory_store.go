package resources

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"sync"
	"time"
)

type MemoryStore struct {
	mu       sync.RWMutex
	byID     map[string]Resource
	events   map[string][]Event
	sequence int64
}

func (store *MemoryStore) ClaimNext(_ context.Context, workerID string, now, leaseUntil time.Time) (Resource, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var selected Resource
	found := false
	for _, resource := range store.byID {
		claimable := resource.Lifecycle != LifecycleDeleted && !resource.NextReconcileAt.After(now) &&
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

func (store *MemoryStore) RequestDeletion(_ context.Context, id, actor string, now time.Time) (Resource, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	resource, exists := store.byID[id]
	if !exists {
		return Resource{}, ErrNotFound
	}
	if resource.Lifecycle == LifecycleDeleted || resource.Lifecycle == LifecycleDeletionRequested {
		return resource, nil
	}
	resource.Lifecycle = LifecycleDeletionRequested
	resource.DeletionRequestedBy = actor
	resource.DeletionRequestedAt = timePointer(now)
	resource.DeletedAt = nil
	resource.DeleteFailure = ""
	resource.NextReconcileAt = now
	resource.UpdatedAt = now
	store.byID[id] = resource
	return resource, nil
}

func (store *MemoryStore) CompleteDeletion(
	_ context.Context, id, workerID, failure string, next, now time.Time,
) (Resource, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	resource, exists := store.byID[id]
	if !exists {
		return Resource{}, ErrNotFound
	}
	if resource.ClaimedBy != workerID {
		return Resource{}, ErrLeaseLost
	}
	resource.ClaimedBy = ""
	resource.LeaseExpiresAt = nil
	resource.LastReconciledAt = timePointer(now)
	resource.UpdatedAt = now
	conditionReason := "TerraformDestroyApplied"
	conditionMessage := "managed infrastructure was destroyed"
	resource.State = StateMissing
	if failure == "" {
		resource.Lifecycle = LifecycleDeleted
		resource.DeletedAt = timePointer(now)
		resource.DeleteFailure = ""
		resource.NextReconcileAt = permanentRetryTime()
		resource.RetryCount = 0
	} else {
		resource.Lifecycle = LifecycleDeleteFailed
		resource.DeleteFailure = failure
		resource.NextReconcileAt = next
		resource.RetryCount++
		resource.State = StateError
		conditionReason = "TerraformDestroyFailed"
		conditionMessage = "Terraform destroy failed; retry is scheduled"
	}
	resource.Conditions = []Condition{condition("Deleted", boolStatus(failure == ""), conditionReason, conditionMessage, now)}
	store.byID[id] = resource
	store.sequence++
	store.events[id] = append(store.events[id], Event{
		Sequence: store.sequence, ResourceID: id, Generation: resource.Generation,
		ObservedGeneration: resource.ObservedGeneration, Observed: append(json.RawMessage(nil), resource.Observed...),
		State: resource.State, Conditions: append([]Condition(nil), resource.Conditions...),
		RetryCount: resource.RetryCount, CreatedAt: now,
	})
	return resource, nil
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
	store.sequence++
	store.events[id] = append(store.events[id], Event{
		Sequence: store.sequence, ResourceID: id, Generation: resource.Generation,
		ObservedGeneration: result.ObservedGeneration, Observed: append(json.RawMessage(nil), result.Observed...),
		State: result.State, Conditions: append([]Condition(nil), result.Conditions...),
		RetryCount: result.RetryCount, CreatedAt: result.LastReconciledAt,
	})
	return resource, nil
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byID: make(map[string]Resource), events: make(map[string][]Event)}
}

func (store *MemoryStore) ListEvents(_ context.Context, id string, after int64, limit int) ([]Event, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	if _, exists := store.byID[id]; !exists {
		return nil, ErrNotFound
	}
	result := make([]Event, 0, limit)
	for _, event := range store.events[id] {
		if event.Sequence > after {
			result = append(result, event)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func (store *MemoryStore) UpsertApplied(_ context.Context, candidate Resource) (Resource, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	existing, exists := store.byID[candidate.ID]
	if !exists {
		if candidate.Lifecycle == "" {
			candidate.Lifecycle = LifecycleActive
		}
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
	existing.Lifecycle = LifecycleActive
	existing.DeletionRequestedBy = ""
	existing.DeletionRequestedAt = nil
	existing.DeletedAt = nil
	existing.DeleteFailure = ""
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

func boolStatus(value bool) string {
	if value {
		return "True"
	}
	return "False"
}
