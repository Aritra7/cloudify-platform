package iac

import (
	"context"
	"sync"
)

// WorkspaceLocker serializes Terraform mutations for one state workspace.
type WorkspaceLocker interface {
	Acquire(context.Context, string) (release func(), err error)
}

// MemoryWorkspaceLocker serializes work within one control-plane process.
// The GCS backend supplies the cross-process state lock in production.
type MemoryWorkspaceLocker struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

func (locker *MemoryWorkspaceLocker) Acquire(ctx context.Context, workspace string) (func(), error) {
	locker.mu.Lock()
	if locker.locks == nil {
		locker.locks = make(map[string]chan struct{})
	}
	semaphore, exists := locker.locks[workspace]
	if !exists {
		semaphore = make(chan struct{}, 1)
		semaphore <- struct{}{}
		locker.locks[workspace] = semaphore
	}
	locker.mu.Unlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-semaphore:
		var once sync.Once
		return func() { once.Do(func() { semaphore <- struct{}{} }) }, nil
	}
}
