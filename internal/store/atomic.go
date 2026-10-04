package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// File modes required by PLAN.md: files 0600, directories 0700.
const (
	FileMode fs.FileMode = 0o600
	DirMode  fs.FileMode = 0o700
)

// EnsureDir creates a directory tree with 0700 permissions.
func EnsureDir(path string) error {
	if err := os.MkdirAll(path, DirMode); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	return nil
}

// EnsureParent creates the parent directory of a file path.
func EnsureParent(path string) error { return EnsureDir(filepath.Dir(path)) }

// ReadFile reads a file, returning (nil, nil) when it does not exist so callers
// can treat "not written yet" and "empty" the same way.
func ReadFile(path string) ([]byte, error) {
	// path is always one of our own layout paths (see Package store).
	// path is always one of our own layout paths (see Package store).
	data, err := os.ReadFile(path) //nolint:gosec // path is ours, not user input //nolint:gosec // path is ours, not user input
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

// WriteFile atomically writes a 0600 file: the data goes to a temporary file in
// the same directory which is then renamed over the target, so a crash never
// leaves a half-written cache or alias file behind.
func WriteFile(path string, data []byte) error {
	return WriteFileMode(path, data, FileMode)
}

// WriteFileMode is WriteFile with an explicit permission bit set.
func WriteFileMode(path string, data []byte, perm fs.FileMode) error {
	if err := EnsureParent(path); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer func() {
		// Both calls are cleanup: closing twice and removing an
		// already-renamed temporary file are expected to fail.
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	if err := tmp.Chmod(perm); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// RemoveIfExists deletes a file, treating a missing file as success.
func RemoveIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// RemoveTree deletes a directory tree, treating a missing directory as success.
func RemoveTree(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}
