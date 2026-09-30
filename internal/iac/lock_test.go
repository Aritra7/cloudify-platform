package iac

import (
	"context"
	"errors"
	"testing"
)

func TestWorkspaceLockerHonorsCancellation(t *testing.T) {
	t.Parallel()
	locker := &MemoryWorkspaceLocker{}
	release, err := locker.Acquire(context.Background(), "workspace")
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := locker.Acquire(ctx, "workspace"); !errors.Is(err, context.Canceled) {
		t.Fatalf("second acquire error = %v, want context.Canceled", err)
	}
}
