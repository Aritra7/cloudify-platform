package iac

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileArtifactStoreWritesChecksummedPrivateFiles(t *testing.T) {
	t.Parallel()
	store := FileArtifactStore{Root: t.TempDir(), EncryptionKey: []byte("0123456789abcdef0123456789abcdef")}
	createdAt := time.Unix(100, 0).UTC()
	metadata, err := store.Save(context.Background(), PlanArtifact{
		MigrationID: "migration-123", JSON: []byte("{}\n"), Text: []byte("No changes."), Binary: []byte("binary-plan"), CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("save artifact: %v", err)
	}
	if metadata.CreatedAt != createdAt || metadata.JSONSHA256 == "" || metadata.TextSHA256 == "" || metadata.BinarySHA256 == "" {
		t.Fatalf("metadata = %#v", metadata)
	}
	plaintext, err := store.LoadBinary(context.Background(), metadata)
	if err != nil {
		t.Fatalf("load encrypted binary: %v", err)
	}
	if string(plaintext) != "binary-plan" {
		t.Fatalf("binary plan = %q", plaintext)
	}
	encrypted, err := os.ReadFile(filepath.Join(store.Root, metadata.BinaryObjectKey))
	if err != nil {
		t.Fatalf("read ciphertext: %v", err)
	}
	if bytes.Contains(encrypted, []byte("binary-plan")) {
		t.Fatal("encrypted artifact contains plaintext plan")
	}
	secondMetadata, err := store.Save(context.Background(), PlanArtifact{
		MigrationID: "migration-123", JSON: []byte("{\"second\":true}\n"), Text: []byte("Second plan."), Binary: []byte("second-binary-plan"), CreatedAt: createdAt.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("save second artifact: %v", err)
	}
	if secondMetadata.BinaryObjectKey == metadata.BinaryObjectKey {
		t.Fatal("distinct plans for one migration used the same binary object key")
	}
	firstPlan, err := store.LoadBinary(context.Background(), metadata)
	if err != nil || string(firstPlan) != "binary-plan" {
		t.Fatalf("first plan after second save = %q, %v", firstPlan, err)
	}
	encrypted[len(encrypted)-1] ^= 0xff
	if err := os.WriteFile(filepath.Join(store.Root, metadata.BinaryObjectKey), encrypted, 0o600); err != nil {
		t.Fatalf("tamper ciphertext: %v", err)
	}
	if _, err := store.LoadBinary(context.Background(), metadata); err == nil {
		t.Fatal("LoadBinary accepted tampered ciphertext")
	}
	for _, path := range []string{metadata.JSONPath, metadata.TextPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat artifact: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("artifact mode = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestFileArtifactStoreRejectsPathTraversal(t *testing.T) {
	t.Parallel()
	store := FileArtifactStore{Root: t.TempDir(), EncryptionKey: []byte("0123456789abcdef0123456789abcdef")}
	if _, err := store.Save(context.Background(), PlanArtifact{
		MigrationID: "../escape", JSON: []byte("{}"), Text: []byte("plan"), Binary: []byte("binary"), CreatedAt: time.Now(),
	}); err == nil {
		t.Fatal("Save returned nil, want unsafe ID error")
	}
}
