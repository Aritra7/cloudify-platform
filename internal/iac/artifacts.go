package iac

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var artifactIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// PlanArtifact contains portable plan output. The binary plan is deliberately
// excluded because it may contain cleartext sensitive values.
type PlanArtifact struct {
	MigrationID string
	JSON        []byte
	Text        []byte
	CreatedAt   time.Time
}

// ArtifactMetadata identifies persisted plan evidence.
type ArtifactMetadata struct {
	MigrationID string    `json:"migration_id"`
	JSONPath    string    `json:"json_path"`
	TextPath    string    `json:"text_path"`
	JSONSHA256  string    `json:"json_sha256"`
	TextSHA256  string    `json:"text_sha256"`
	CreatedAt   time.Time `json:"created_at"`
}

// ArtifactStore persists sanitized Terraform plan evidence.
type ArtifactStore interface {
	Save(context.Context, PlanArtifact) (ArtifactMetadata, error)
}

// FileArtifactStore is a local implementation suitable for development and
// mounted object-storage filesystems.
type FileArtifactStore struct{ Root string }

func (store FileArtifactStore) Save(ctx context.Context, artifact PlanArtifact) (ArtifactMetadata, error) {
	if err := ctx.Err(); err != nil {
		return ArtifactMetadata{}, err
	}
	if !artifactIDPattern.MatchString(artifact.MigrationID) {
		return ArtifactMetadata{}, errors.New("migration ID is not safe for artifact storage")
	}
	if len(artifact.JSON) == 0 || len(artifact.Text) == 0 {
		return ArtifactMetadata{}, errors.New("plan JSON and text are required")
	}
	directory := filepath.Join(store.Root, artifact.MigrationID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("create artifact directory: %w", err)
	}
	jsonPath := filepath.Join(directory, "plan.json")
	textPath := filepath.Join(directory, "plan.txt")
	if err := writeAtomically(jsonPath, artifact.JSON); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("write JSON plan artifact: %w", err)
	}
	if err := writeAtomically(textPath, artifact.Text); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("write text plan artifact: %w", err)
	}
	return ArtifactMetadata{
		MigrationID: artifact.MigrationID,
		JSONPath:    jsonPath,
		TextPath:    textPath,
		JSONSHA256:  checksum(artifact.JSON),
		TextSHA256:  checksum(artifact.Text),
		CreatedAt:   artifact.CreatedAt,
	}, nil
}

func writeAtomically(path string, contents []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".plan-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func checksum(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}
