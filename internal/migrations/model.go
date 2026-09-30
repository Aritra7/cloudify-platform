package migrations

import (
	"errors"
	"time"
)

// Status is the lifecycle state of a migration operation.
type Status string

const (
	StatusQueued     Status = "queued"
	StatusRunning    Status = "running"
	StatusCancelling Status = "cancelling"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
	StatusCancelled  Status = "cancelled"
)

var (
	ErrNotFound            = errors.New("migration not found")
	ErrInvalidTransition   = errors.New("invalid migration state transition")
	ErrIdempotencyConflict = errors.New("idempotency key was already used for a different request")
)

// Source identifies the immutable application revision to migrate.
type Source struct {
	RepositoryURL string `json:"repository_url"`
	Revision      string `json:"revision"`
}

// Destination describes the requested cloud deployment target.
type Destination struct {
	Provider  string `json:"provider"`
	ProjectID string `json:"project_id"`
	Region    string `json:"region"`
	Runtime   string `json:"runtime"`
	Database  string `json:"database,omitempty"`
}

// CreateRequest is the desired state accepted by the control plane.
type CreateRequest struct {
	Source      Source      `json:"source"`
	Destination Destination `json:"destination"`
}

// Migration is the durable representation of an asynchronous migration.
type Migration struct {
	ID             string      `json:"id"`
	Status         Status      `json:"status"`
	Source         Source      `json:"source"`
	Destination    Destination `json:"destination"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
	IdempotencyKey string      `json:"-"`
	RequestHash    string      `json:"-"`
}

// CanTransition reports whether a state change is permitted by the lifecycle.
func CanTransition(from, to Status) bool {
	allowed := map[Status]map[Status]bool{
		StatusQueued: {
			StatusRunning:   true,
			StatusCancelled: true,
		},
		StatusRunning: {
			StatusCancelling: true,
			StatusSucceeded:  true,
			StatusFailed:     true,
		},
		StatusCancelling: {
			StatusCancelled: true,
			StatusFailed:    true,
		},
	}

	return allowed[from][to]
}
