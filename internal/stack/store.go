package stack

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"stacked/internal/git"
)

// stackedDir returns the absolute path of the per-repository stacked metadata
// directory. It uses the common git dir so the stack is shared across all linked
// worktrees of a repository rather than being per-worktree.
func stackedDir() (string, error) {
	gitDir, err := git.GitCommonDir()
	if err != nil {
		return "", fmt.Errorf("locate git dir: %w", err)
	}
	return filepath.Join(gitDir, "stacked"), nil
}

// statePath returns the absolute path of the stacked state file,
// <GitCommonDir>/stacked/state.json.
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
		return nil, fmt.Errorf("parse state file: %w", err)
	}
	if s.Branches == nil {
		s.Branches = make(map[string]*Branch)
	}
	return &s, nil
}

// Save atomically writes the state to disk as pretty-printed JSON with a
// trailing newline, using a temporary file and rename.
func (s *State) Save() error {
	path, err := statePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create stacked dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "state-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp state file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename state file: %w", err)
	}
	return nil
}
