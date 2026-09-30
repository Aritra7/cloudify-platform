package plans

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/iac"
)

type Service struct {
	store  Store
	policy Policy
	now    func() time.Time
	newID  func() (string, error)
}

func NewService(store Store, policy Policy) *Service {
	return &Service{store: store, policy: policy, now: func() time.Time { return time.Now().UTC() }, newID: randomID}
}

func (service *Service) Create(ctx context.Context, migrationID, idempotencyKey string, spec iac.DeploymentSpec) (Plan, bool, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		return Plan{}, false, fmt.Errorf("idempotency key is required and must not exceed 128 characters")
	}
	spec.MigrationID = migrationID
	if err := spec.Validate(); err != nil {
		return Plan{}, false, err
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		return Plan{}, false, fmt.Errorf("encode deployment specification: %w", err)
	}
	hash := sha256.Sum256(encoded)
	id, err := service.newID()
	if err != nil {
		return Plan{}, false, fmt.Errorf("generate plan ID: %w", err)
	}
	now := service.now()
	candidate := Plan{
		ID: id, MigrationID: migrationID, Status: StatusQueued, Specification: spec,
		CreatedAt: now, UpdatedAt: now, IdempotencyKey: idempotencyKey, RequestHash: hex.EncodeToString(hash[:]),
	}
	candidate.Policy = service.policy.Evaluate(candidate)
	if !candidate.Policy.Allowed {
		candidate.Status = StatusRejected
	}
	return service.store.CreateOrGet(ctx, candidate)
}

func (service *Service) Get(ctx context.Context, id string) (Plan, error) {
	return service.store.Get(ctx, id)
}

func (service *Service) Approve(ctx context.Context, id, actor string) (Plan, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" || len(actor) > 200 {
		return Plan{}, fmt.Errorf("approval actor is required and must not exceed 200 characters")
	}
	return service.store.Approve(ctx, id, actor, service.now())
}

func (service *Service) QueueApply(ctx context.Context, id, actor string) (Plan, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" || len(actor) > 200 {
		return Plan{}, fmt.Errorf("apply actor is required and must not exceed 200 characters")
	}
	return service.store.QueueApply(ctx, id, actor, service.now())
}

func randomID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
