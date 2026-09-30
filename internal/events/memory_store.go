package events

import (
	"context"
	"sync"
)

// MemoryStore is a concurrency-safe development event store.
type MemoryStore struct {
	mu          sync.RWMutex
	next        int64
	byMigration map[string][]Event
}

// NewMemoryStore creates an empty event store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{next: 1, byMigration: make(map[string][]Event)}
}

// Append assigns a globally increasing sequence number.
func (store *MemoryStore) Append(_ context.Context, event Event) (Event, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	event.Sequence = store.next
	store.next++
	store.byMigration[event.MigrationID] = append(store.byMigration[event.MigrationID], event)
	return event, nil
}

// ListAfter returns up to limit events ordered by sequence.
func (store *MemoryStore) ListAfter(_ context.Context, migrationID string, after int64, limit int) ([]Event, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]Event, 0, limit)
	for _, event := range store.byMigration[migrationID] {
		if event.Sequence <= after {
			continue
		}
		result = append(result, event)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}
