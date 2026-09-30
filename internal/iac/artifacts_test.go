package iac

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestFileArtifactStoreWritesChecksummedPrivateFiles(t *testing.T) {
	t.Parallel()
	store := FileArtifactStore{Root: t.TempDir()}
	createdAt := time.Unix(100, 0).UTC()
	metadata, err := store.Save(context.Background(), PlanArtifact{
		MigrationID: "migration-123", JSON: []byte("{}\n"), Text: []byte("No changes."), CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("save artifact: %v", err)
	}
	if metadata.CreatedAt != createdAt || metadata.JSONSHA256 == "" || metadata.TextSHA256 == "" {
		t.Fatalf("metadata = %#v", metadata)
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
	store := FileArtifactStore{Root: t.TempDir()}
	if _, err := store.Save(context.Background(), PlanArtifact{
		MigrationID: "../escape", JSON: []byte("{}"), Text: []byte("plan"), CreatedAt: time.Now(),
	}); err == nil {
		t.Fatal("Save returned nil, want unsafe ID error")
	}
}
