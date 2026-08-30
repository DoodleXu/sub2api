package configwriter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ErrConfigLock is returned when a configuration lock cannot be acquired.
// Callers should normally retry after the returned context deadline rather
// than treating it as a malformed configuration.
var ErrConfigLock = errors.New("configuration file lock failed")

const configLockSuffix = ".sub2api.lock"

// fileLock is an advisory, cross-process lock. The lock file is deliberately
// kept after release: deleting it would let another process replace the path
// with a symlink and bypass the lock between two operations.
type fileLock struct {
	file *os.File
}

// AcquireProcessLock exposes the same advisory, cross-platform lock used by
// configuration transactions to other desktop subsystems that share state
// across GUI and helper processes. The sidecar is retained after release so a
// symlink cannot be inserted between two lock attempts.
func AcquireProcessLock(ctx context.Context, target string) (func() error, error) {
	lock, err := acquireFileLock(ctx, target)
	if err != nil {
		return nil, err
	}
	return lock.Close, nil
}

func acquireFileLock(ctx context.Context, target string) (*fileLock, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	lockPath := filepath.Clean(target) + configLockSuffix
	if err := ensureSafeParent(lockPath, 0o700); err != nil {
		return nil, fmt.Errorf("%w: prepare lock parent: %v", ErrConfigLock, err)
	}
	file, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("%w: open %s: %v", ErrConfigLock, lockPath, err)
	}
	// Chmod is best effort on Windows, but on Unix it repairs permissions when
	// a lock file was created by an older build with a broader umask.
	if chmodErr := file.Chmod(0o600); chmodErr != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: secure %s: %v", ErrConfigLock, lockPath, chmodErr)
	}
	for {
		err = tryLockFile(file)
		if err == nil {
			return &fileLock{file: file}, nil
		}
		if !isLockBusy(err) {
			_ = file.Close()
			return nil, fmt.Errorf("%w: lock %s: %v", ErrConfigLock, lockPath, err)
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *fileLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unlockFile(l.file)
	closeErr := l.file.Close()
	if unlockErr != nil && closeErr != nil {
		return errors.Join(unlockErr, closeErr)
	}
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

// acquireFileLocks takes locks in lexical order. Every multi-file operation
// uses this helper so two processes touching Codex's config.toml/auth.json
// cannot deadlock each other by acquiring the files in opposite order.
func acquireFileLocks(ctx context.Context, targets []string) ([]*fileLock, error) {
	ordered := append([]string(nil), targets...)
	sort.Strings(ordered)
	locks := make([]*fileLock, 0, len(ordered))
	for _, target := range ordered {
		lock, err := acquireFileLock(ctx, target)
		if err != nil {
			for i := len(locks) - 1; i >= 0; i-- {
				_ = locks[i].Close()
			}
			return nil, err
		}
		locks = append(locks, lock)
	}
	return locks, nil
}

func releaseFileLocks(locks []*fileLock) error {
	var joined error
	for i := len(locks) - 1; i >= 0; i-- {
		joined = errors.Join(joined, locks[i].Close())
	}
	return joined
}
