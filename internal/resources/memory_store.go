package resources

import (
	"context"
	"sort"
	"sync"
)

type MemoryStore struct {
	mu   sync.RWMutex
	byID map[string]Resource
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
	existing.Desired = candidate.Desired
	existing.Generation++
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
