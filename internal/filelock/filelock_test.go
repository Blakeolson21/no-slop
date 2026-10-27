package filelock

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireContextCancelsContendedLockAndCanAcquireAfterRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if lock, err := AcquireContext(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		if lock != nil {
			lock.Release()
		}
		t.Fatalf("contended acquisition: %v", err)
	}
	first.Release()
	next, err := AcquireContext(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	next.Release()
}
