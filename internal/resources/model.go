package resources

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/iac"
)

type State string

const (
	StateUnknown State = "unknown"
	StateInSync  State = "in_sync"
	StateDrifted State = "drifted"
	StateMissing State = "missing"
	StateError   State = "error"
)

type RemediationPolicy string

const (
	RemediationReport    RemediationPolicy = "report"
	RemediationAutomatic RemediationPolicy = "automatic"
)

var ErrNotFound = errors.New("managed resource not found")

type Condition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason"`
	Message            string    `json:"message"`
	LastTransitionTime time.Time `json:"last_transition_time"`
}

// Resource is the durable desired/observed-state record reconciled by the
// control plane. Desired never contains secret values, only references.
type Resource struct {
	ID                 string             `json:"id"`
	Kind               string             `json:"kind"`
	MigrationID        string             `json:"migration_id"`
	SourcePlanID       string             `json:"source_plan_id"`
	ProjectID          string             `json:"project_id"`
	Region             string             `json:"region"`
	Name               string             `json:"name"`
	Desired            iac.DeploymentSpec `json:"desired"`
	Observed           json.RawMessage    `json:"observed,omitempty"`
	State              State              `json:"state"`
	RemediationPolicy  RemediationPolicy  `json:"remediation_policy"`
	Generation         int64              `json:"generation"`
	ObservedGeneration int64              `json:"observed_generation"`
	Conditions         []Condition        `json:"conditions"`
	RetryCount         int                `json:"retry_count"`
	NextReconcileAt    time.Time          `json:"next_reconcile_at"`
	LastReconciledAt   *time.Time         `json:"last_reconciled_at,omitempty"`
	CreatedAt          time.Time          `json:"created_at"`
	UpdatedAt          time.Time          `json:"updated_at"`
	ClaimedBy          string             `json:"-"`
	LeaseExpiresAt     *time.Time         `json:"-"`
}

func appliedResourceID(spec iac.DeploymentSpec) string {
	sum := sha256.Sum256([]byte("cloud_run_service\x00" + spec.ProjectID + "\x00" + spec.Region + "\x00" + spec.ServiceName))
	encoded := hex.EncodeToString(sum[:16])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
