package migrations

import (
	"context"
	"sync"
	"time"
)

// MemoryStore is a concurrency-safe development store. Production deployments
// will replace it with the Postgres implementation behind the same interface.
type MemoryStore struct {
	mu             sync.RWMutex
	byID           map[string]Migration
	byIdempotency  map[string]string
	transitionTime func() time.Time
}

// NewMemoryStore creates an empty in-memory migration store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		byID:           make(map[string]Migration),
		byIdempotency:  make(map[string]string),
		transitionTime: func() time.Time { return time.Now().UTC() },
	}
}

// CreateOrGet atomically creates a migration or returns the request associated
// with the idempotency key.
func (s *MemoryStore) CreateOrGet(_ context.Context, candidate Migration) (Migration, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id, exists := s.byIdempotency[candidate.IdempotencyKey]; exists {
		existing := s.byID[id]
		if existing.RequestHash != candidate.RequestHash {
			return Migration{}, false, ErrIdempotencyConflict
		}
		return existing, false, nil
	}

	s.byID[candidate.ID] = candidate
	s.byIdempotency[candidate.IdempotencyKey] = candidate.ID
	return candidate, true, nil
}

// Get returns a migration by ID.
func (s *MemoryStore) Get(_ context.Context, id string) (Migration, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	migration, exists := s.byID[id]
	if !exists {
		return Migration{}, ErrNotFound
	}
	return migration, nil
}

// Transition atomically moves a migration to an allowed lifecycle state.
func (s *MemoryStore) Transition(_ context.Context, id string, to Status) (Migration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	migration, exists := s.byID[id]
	if !exists {
		return Migration{}, ErrNotFound
	}
	if !CanTransition(migration.Status, to) {
		return Migration{}, ErrInvalidTransition
	}

	migration.Status = to
	migration.UpdatedAt = s.transitionTime()
	s.byID[id] = migration
	return migration, nil
}

// ClaimNext assigns the oldest eligible migration to one worker.
func (s *MemoryStore) ClaimNext(
	_ context.Context,
	workerID string,
	now time.Time,
	leaseUntil time.Time,
) (Migration, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var selected Migration
	found := false
	for _, migration := range s.byID {
		eligible := migration.Status == StatusQueued ||
			(migration.Status == StatusRunning && migration.LeaseExpiresAt != nil && !migration.LeaseExpiresAt.After(now))
		if eligible && (!found || migration.CreatedAt.Before(selected.CreatedAt)) {
			selected = migration
			found = true
		}
	}
	if !found {
		return Migration{}, false, nil
	}

	selected.Status = StatusRunning
	selected.ClaimedBy = workerID
	selected.LeaseExpiresAt = timePointer(leaseUntil)
	selected.UpdatedAt = now
	s.byID[selected.ID] = selected
	return selected, true, nil
}

// RenewLease extends a lease only while the same worker still owns it.
func (s *MemoryStore) RenewLease(
	_ context.Context,
	id string,
	workerID string,
	now time.Time,
	leaseUntil time.Time,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	migration, exists := s.byID[id]
	if !exists {
		return ErrNotFound
	}
	if migration.ClaimedBy != workerID || migration.LeaseExpiresAt == nil || !migration.LeaseExpiresAt.After(now) {
		return ErrLeaseLost
	}
	if migration.Status != StatusRunning && migration.Status != StatusCancelling {
		return ErrLeaseLost
	}

	migration.LeaseExpiresAt = timePointer(leaseUntil)
	migration.UpdatedAt = now
	s.byID[id] = migration
	return nil
}

// Complete records a worker-owned terminal state and releases its lease.
func (s *MemoryStore) Complete(
	_ context.Context,
	id string,
	workerID string,
	to Status,
	now time.Time,
) (Migration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	migration, exists := s.byID[id]
	if !exists {
		return Migration{}, ErrNotFound
	}
	if migration.ClaimedBy != workerID {
		return Migration{}, ErrLeaseLost
	}
	if !CanTransition(migration.Status, to) {
		return Migration{}, ErrInvalidTransition
	}

	migration.Status = to
	migration.ClaimedBy = ""
	migration.LeaseExpiresAt = nil
	migration.UpdatedAt = now
	s.byID[id] = migration
	return migration, nil
}

func timePointer(value time.Time) *time.Time {
	return &value
}
