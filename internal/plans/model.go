package plans

import (
	"errors"
	"regexp"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/iac"
)

type Status string

const (
	StatusQueued   Status = "queued"
	StatusPlanning Status = "planning"
	StatusReady    Status = "ready"
	StatusRejected Status = "rejected"
	StatusApproved Status = "approved"
	StatusFailed   Status = "failed"
)

var (
	ErrNotFound          = errors.New("Terraform plan not found")
	ErrInvalidTransition = errors.New("invalid Terraform plan state transition")
	ErrIdempotency       = errors.New("idempotency key was already used for a different plan")
	ErrLeaseLost         = errors.New("Terraform plan worker lease was lost")
)

var checksumPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Violation struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type PolicyDecision struct {
	Allowed    bool        `json:"allowed"`
	Violations []Violation `json:"violations"`
}

type Plan struct {
	ID             string                `json:"id"`
	MigrationID    string                `json:"migration_id"`
	Status         Status                `json:"status"`
	Specification  iac.DeploymentSpec    `json:"specification"`
	Policy         PolicyDecision        `json:"policy"`
	HasChanges     *bool                 `json:"has_changes,omitempty"`
	Artifact       *iac.ArtifactMetadata `json:"artifact,omitempty"`
	FailureMessage string                `json:"failure_message,omitempty"`
	AttemptCount   int                   `json:"attempt_count"`
	ApprovedBy     string                `json:"approved_by,omitempty"`
	ApprovedAt     *time.Time            `json:"approved_at,omitempty"`
	CreatedAt      time.Time             `json:"created_at"`
	UpdatedAt      time.Time             `json:"updated_at"`
	IdempotencyKey string                `json:"-"`
	RequestHash    string                `json:"-"`
	ClaimedBy      string                `json:"-"`
	LeaseExpiresAt *time.Time            `json:"-"`
}

func validArtifact(artifact *iac.ArtifactMetadata) bool {
	return artifact != nil && artifact.JSONPath != "" && artifact.TextPath != "" &&
		checksumPattern.MatchString(artifact.JSONSHA256) && checksumPattern.MatchString(artifact.TextSHA256)
}
