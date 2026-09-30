package iac

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
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
var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// PlanArtifact contains portable output plus the binary plan that will be
// encrypted before persistence.
type PlanArtifact struct {
	MigrationID string
	JSON        []byte
	Text        []byte
	Binary      []byte
	CreatedAt   time.Time
}

// ArtifactMetadata identifies persisted plan evidence.
type ArtifactMetadata struct {
	MigrationID     string    `json:"migration_id"`
	JSONPath        string    `json:"json_path"`
	TextPath        string    `json:"text_path"`
	JSONSHA256      string    `json:"json_sha256"`
	TextSHA256      string    `json:"text_sha256"`
	BinaryObjectKey string    `json:"-"`
	BinarySHA256    string    `json:"binary_sha256"`
	CreatedAt       time.Time `json:"created_at"`
}

// ArtifactStore persists sanitized Terraform plan evidence.
type ArtifactStore interface {
	Save(context.Context, PlanArtifact) (ArtifactMetadata, error)
}

// BinaryArtifactStore retrieves an encrypted binary plan for exact apply.
type BinaryArtifactStore interface {
	LoadBinary(context.Context, ArtifactMetadata) ([]byte, error)
}

// FileArtifactStore is a local implementation suitable for development and
// mounted object-storage filesystems.
type FileArtifactStore struct {
	Root          string
	EncryptionKey []byte
}

func (store FileArtifactStore) Save(ctx context.Context, artifact PlanArtifact) (ArtifactMetadata, error) {
	if err := ctx.Err(); err != nil {
		return ArtifactMetadata{}, err
	}
	if !artifactIDPattern.MatchString(artifact.MigrationID) {
		return ArtifactMetadata{}, errors.New("migration ID is not safe for artifact storage")
	}
	if len(artifact.JSON) == 0 || len(artifact.Text) == 0 || len(artifact.Binary) == 0 {
		return ArtifactMetadata{}, errors.New("plan JSON, text, and binary are required")
	}
	if len(store.EncryptionKey) != 32 {
		return ArtifactMetadata{}, errors.New("artifact encryption key must contain exactly 32 bytes")
	}
	directory := filepath.Join(store.Root, artifact.MigrationID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("create artifact directory: %w", err)
	}
	jsonChecksum := checksum(artifact.JSON)
	textChecksum := checksum(artifact.Text)
	binaryChecksum := checksum(artifact.Binary)
	jsonPath := filepath.Join(directory, "plan-"+jsonChecksum+".json")
	textPath := filepath.Join(directory, "plan-"+textChecksum+".txt")
	binaryObjectKey := filepath.Join(artifact.MigrationID, "plan-"+binaryChecksum+".enc")
	binaryPath := filepath.Join(store.Root, binaryObjectKey)
	if err := writeAtomically(jsonPath, artifact.JSON); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("write JSON plan artifact: %w", err)
	}
	if err := writeAtomically(textPath, artifact.Text); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("write text plan artifact: %w", err)
	}
	encrypted, err := encryptPlan(store.EncryptionKey, artifact.Binary)
	if err != nil {
		return ArtifactMetadata{}, fmt.Errorf("encrypt binary plan artifact: %w", err)
	}
	if err := writeAtomically(binaryPath, encrypted); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("write encrypted binary plan artifact: %w", err)
	}
	return ArtifactMetadata{
		MigrationID:     artifact.MigrationID,
		JSONPath:        jsonPath,
		TextPath:        textPath,
		JSONSHA256:      jsonChecksum,
		TextSHA256:      textChecksum,
		BinaryObjectKey: binaryObjectKey,
		BinarySHA256:    binaryChecksum,
		CreatedAt:       artifact.CreatedAt,
	}, nil
}

// LoadBinary decrypts and verifies the exact binary plan bound to approval.
func (store FileArtifactStore) LoadBinary(ctx context.Context, metadata ArtifactMetadata) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(store.EncryptionKey) != 32 || !artifactIDPattern.MatchString(metadata.MigrationID) {
		return nil, errors.New("invalid encrypted artifact configuration")
	}
	if !sha256Pattern.MatchString(metadata.BinarySHA256) {
		return nil, errors.New("binary plan metadata failed validation")
	}
	expectedKey := filepath.Join(metadata.MigrationID, "plan-"+metadata.BinarySHA256+".enc")
	if metadata.BinaryObjectKey != expectedKey {
		return nil, errors.New("binary plan metadata failed validation")
	}
	encrypted, err := os.ReadFile(filepath.Join(store.Root, expectedKey))
	if err != nil {
		return nil, fmt.Errorf("read encrypted binary plan: %w", err)
	}
	plaintext, err := decryptPlan(store.EncryptionKey, encrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt binary plan: %w", err)
	}
	if checksum(plaintext) != metadata.BinarySHA256 {
		return nil, errors.New("binary plan checksum mismatch")
	}
	return plaintext, nil
}

func encryptPlan(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, nil), nil
}

func decryptPlan(key, encrypted []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(encrypted) < aead.NonceSize() {
		return nil, errors.New("encrypted plan is truncated")
	}
	nonce, ciphertext := encrypted[:aead.NonceSize()], encrypted[aead.NonceSize():]
	return aead.Open(nil, nonce, ciphertext, nil)
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
