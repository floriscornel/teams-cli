package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Stats describes one file or directory tree for `teams cache info`.
type Stats struct {
	Path    string
	Exists  bool
	Files   int
	Bytes   int64
	Newest  time.Time
	Oldest  time.Time
	IsDir   bool
	Mode    fs.FileMode
	Problem string
}

// Age returns how long ago the newest write happened, or 0 when nothing exists.
func (s Stats) Age() time.Duration {
	if !s.Exists || s.Newest.IsZero() {
		return 0
	}
	return time.Since(s.Newest)
}

// Scan measures a path without following symlinks out of the tree. A missing
// path is not an error: it reports Exists=false so `cache info` can list the
// locations that have not been created yet.
func Scan(path string, isDir bool) Stats {
	s := Stats{Path: path, IsDir: isDir}
	fi, err := os.Stat(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.Problem = err.Error()
		}
		return s
	}
	s.Exists = true
	s.Mode = fi.Mode().Perm()
	if !fi.IsDir() {
		s.Files = 1
		s.Bytes = fi.Size()
		s.Newest, s.Oldest = fi.ModTime(), fi.ModTime()
		return s
	}
	walkErr := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			s.Problem = err.Error()
			return nil // keep scanning what we can
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			s.Problem = err.Error()
			return nil
		}
		s.Files++
		s.Bytes += info.Size()
		mt := info.ModTime()
		if s.Newest.IsZero() || mt.After(s.Newest) {
			s.Newest = mt
		}
		if s.Oldest.IsZero() || mt.Before(s.Oldest) {
			s.Oldest = mt
		}
		return nil
	})
	if walkErr != nil && s.Problem == "" {
		s.Problem = walkErr.Error()
	}
	return s
}

// HumanBytes formats a byte count the way `cache info` prints it.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	value := float64(n)
	for _, u := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= unit
		if value < unit {
			return strconv.FormatFloat(value, 'f', 1, 64) + " " + u
		}
	}
	return strconv.FormatFloat(value, 'f', 1, 64) + " PiB"
}
