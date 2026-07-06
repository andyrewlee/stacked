package stack

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"stacked/internal/git"
)

var stackedDirCache sync.Map // cwd -> dir

// stackedDir returns the absolute path of the per-repository stacked metadata
// directory. It uses the common git dir so the stack is shared across all linked
// worktrees of a repository rather than being per-worktree.
func stackedDir() (string, error) {
	cwd, err := os.Getwd()
	if err == nil {
		if dir, ok := stackedDirCache.Load(cwd); ok {
			return dir.(string), nil
		}
	}

	gitDir, gerr := git.GitCommonDir()
	if gerr != nil {
		return "", fmt.Errorf("locate git dir: %w", gerr)
	}
	dir := filepath.Join(gitDir, "stacked")
	// Key by cwd, not sync.Once: cmd integration tests chdir between repos in
	// one process, and each repo must resolve its own common git dir.
	if err == nil {
		stackedDirCache.Store(cwd, dir)
	}
	return dir, nil
}

// statePath returns the absolute path of the stacked state file,
// <common git dir>/stacked/state.json.
func statePath() (string, error) {
	dir, err := stackedDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

// Init creates a new stacked state file for the given trunk branch. It returns
// an error if the state file already exists.
func Init(trunk string) (*State, error) {
	path, err := statePath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("stacked is already initialized (%s)", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat state file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create stacked dir: %w", err)
	}
	s := &State{Trunk: trunk, Branches: make(map[string]*Branch)}
	if err := s.Save(); err != nil {
		return nil, err
	}
	return s, nil
}

// Load reads and parses the stacked state file. It returns ErrNotInitialized if
// the file does not exist.
func Load() (*State, error) {
	path, err := statePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotInitialized
		}
		return nil, fmt.Errorf("read state file: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse state file %s (fix or delete it and re-run st init): %w", path, err)
	}
	if s.Branches == nil {
		s.Branches = make(map[string]*Branch)
	}
	return &s, nil
}

// Save atomically writes the state to disk as pretty-printed JSON with a
// trailing newline.
func (s *State) Save() error {
	path, err := statePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	return atomicWriteFile(path, append(data, '\n'))
}

// atomicWriteFile writes data to path so a reader never observes a half-written
// file and the result survives an OS/power crash: it writes a temporary file in
// the same directory, fsyncs it, renames it over path (rename is atomic within a
// filesystem), and best-effort fsyncs the parent directory so the rename entry
// itself is durable. The parent directory is created if necessary. A crash or
// full disk mid-write leaves either the old file or the new one intact, never a
// truncated mix. This is the single writer used for all on-disk stacked metadata
// (state.json and undo.json).
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create stacked dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	// Flush the file's data before the rename: without it, most filesystems can
	// make the rename durable while the new file's bytes are still in the page
	// cache, so a power loss could resurrect a zero-length or partial file —
	// exactly the truncated mix the rename is meant to rule out.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	syncDir(dir)
	return nil
}

// syncDir flushes a directory entry to disk so a rename within it is durable
// across an OS/power crash, not merely visible to readers in the running
// kernel. Best-effort: directory fsync is not supported on every platform (for
// example Windows), and a failure here does not make the freshly written file
// wrong — only less crash-durable.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
