package migrations

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Service coordinates migration lifecycle operations independently of HTTP.
type Service struct {
	store Store
	now   func() time.Time
	newID func() (string, error)
}

// NewService constructs a migration service.
func NewService(store Store) *Service {
	return &Service{
		store: store,
		now:   func() time.Time { return time.Now().UTC() },
		newID: randomID,
	}
}

// Create creates a queued migration or replays an idempotent request.
func (s *Service) Create(
	ctx context.Context,
	idempotencyKey string,
	request CreateRequest,
) (Migration, bool, error) {
	id, err := s.newID()
	if err != nil {
		return Migration{}, false, fmt.Errorf("generate migration ID: %w", err)
	}

	requestBytes, err := json.Marshal(request)
	if err != nil {
		return Migration{}, false, fmt.Errorf("hash migration request: %w", err)
	}
	hash := sha256.Sum256(requestBytes)
	now := s.now()

	return s.store.CreateOrGet(ctx, Migration{
		ID:             id,
		Status:         StatusQueued,
		Source:         request.Source,
		Destination:    request.Destination,
		CreatedAt:      now,
		UpdatedAt:      now,
		IdempotencyKey: idempotencyKey,
		RequestHash:    hex.EncodeToString(hash[:]),
	})
}

// Get returns a migration by ID.
func (s *Service) Get(ctx context.Context, id string) (Migration, error) {
	return s.store.Get(ctx, id)
}

// Cancel requests cancellation without skipping required worker cleanup.
func (s *Service) Cancel(ctx context.Context, id string) (Migration, error) {
	migration, err := s.store.Get(ctx, id)
	if err != nil {
		return Migration{}, err
	}

	switch migration.Status {
	case StatusQueued:
		return s.store.Transition(ctx, id, StatusCancelled)
	case StatusRunning:
		return s.store.Transition(ctx, id, StatusCancelling)
	default:
		return Migration{}, ErrInvalidTransition
	}
}

// Retry requeues a failed migration while preserving all prior attempts.
func (s *Service) Retry(ctx context.Context, id string) (Migration, error) {
	return s.store.Retry(ctx, id, s.now())
}

// ListAttempts returns execution history in attempt-number order.
func (s *Service) ListAttempts(ctx context.Context, id string) ([]Attempt, error) {
	if _, err := s.store.Get(ctx, id); err != nil {
		return nil, err
	}
	return s.store.ListAttempts(ctx, id)
}

func randomID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}

	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%x-%x-%x-%x-%x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16],
	), nil
}
