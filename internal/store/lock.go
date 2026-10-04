package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"time"
)

// StaleLockAfter is how old a lock file may be before we assume its owner died.
const StaleLockAfter = 10 * time.Minute

type lockInfo struct {
	PID  int       `json:"pid"`
	Time time.Time `json:"time"`
}

// Lock is an advisory, cross-process lock on a file path, implemented with an
// O_EXCL lock file so it works on every OS without cgo. It guards the
// per-profile state files (aliases, AI sessions) and mirrors, rather than
// replaces, the lock MSAL's cache extension takes around the token store.
type Lock struct {
	path string
}

// Acquire takes the lock, waiting up to timeout. A zero timeout still tries
// once. A lock file older than StaleLockAfter is treated as abandoned.
func Acquire(ctx context.Context, path string, timeout time.Duration) (*Lock, error) {
	if err := EnsureParent(path); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		err := tryLock(path)
		if err == nil {
			return &Lock{path: path}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		if stale, staleErr := staleLock(path); staleErr == nil && stale {
			// Best effort: another process may win the race, which is fine.
			_ = os.Remove(path)
			continue
		}
		if timeout <= 0 || time.Now().After(deadline) {
			return nil, fmt.Errorf("%s is locked by another teams process (delete the file if nothing is running)", path)
		}
		// Jittered poll keeps several waiting processes from waking together.
		// Jittered polling only spreads the wake-ups; it is not a secret.
		delay := 20*time.Millisecond + time.Duration(rand.Int63n(int64(20*time.Millisecond))) //nolint:gosec // jitter, not a secret
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}

func tryLock(path string) error {
	// path is one of our own state paths, never user input.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, FileMode) //nolint:gosec // our own state path
	if err != nil {
		return err
	}
	data, _ := json.Marshal(lockInfo{PID: os.Getpid(), Time: time.Now().UTC()})
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write lock file %s: %w", path, werr)
	}
	if cerr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write lock file %s: %w", path, cerr)
	}
	return nil
}

// staleLock reports whether the lock file belongs to a process that is gone.
// We only trust the clock, because probing another pid is not portable and a
// wrong answer here would break the mutual exclusion we are after.
func staleLock(path string) (bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // our own state path
	if err != nil {
		return false, err
	}
	var info lockInfo
	err = json.Unmarshal(data, &info)
	at := info.Time
	if err != nil || at.IsZero() {
		fi, statErr := os.Stat(path)
		if statErr != nil {
			return false, statErr
		}
		at = fi.ModTime()
	}
	return time.Since(at) > StaleLockAfter, nil
}

// Release removes the lock file. It is safe to call twice.
func (l *Lock) Release() error {
	if l == nil || l.path == "" {
		return nil
	}
	err := os.Remove(l.path)
	l.path = ""
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Path returns the lock file location.
func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// WithLock runs fn while holding the lock at path.
func WithLock(ctx context.Context, path string, timeout time.Duration, fn func() error) error {
	l, err := Acquire(ctx, path, timeout)
	if err != nil {
		return err
	}
	defer func() { _ = l.Release() }()
	return fn()
}

// PidFromLockFile reports the pid recorded in a lock file (used by tests).
func PidFromLockFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var info lockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return 0, err
	}
	return info.PID, nil
}
