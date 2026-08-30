package configwriter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileLockHonorsContextAndPersistsSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	first, err := acquireFileLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	lockPath := path + configLockSuffix
	if info, statErr := os.Stat(lockPath); statErr != nil {
		t.Fatalf("lock sidecar was not created: %v", statErr)
	} else if !info.Mode().IsRegular() {
		t.Fatalf("lock sidecar is not a regular file: %v", info.Mode())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	second, err := acquireFileLock(ctx, path)
	if second != nil {
		_ = second.Close()
		t.Fatal("a second lock unexpectedly acquired the held sidecar")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock wait error = %v, want context deadline", err)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := acquireFileLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lock sidecar was removed on release: %v", err)
	}
}
