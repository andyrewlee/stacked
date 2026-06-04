package stack

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"stacked/internal/git"
)

// maxUndoEntries bounds the size of the undo journal.
const maxUndoEntries = 20

// UndoEntry is a reversible snapshot taken before a mutating operation: the
// state-file contents and the tip SHAs of the trunk and every tracked branch at
// that moment.
type UndoEntry struct {
	Label string            `json:"label"`
	State json.RawMessage   `json:"state"`
	Refs  map[string]string `json:"refs"`
}

func undoPath() (string, error) {
	dir, err := stackedDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "undo.json"), nil
}

func loadUndo() ([]UndoEntry, error) {
	path, err := undoPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read undo log: %w", err)
	}
	var entries []UndoEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse undo log: %w", err)
	}
	return entries, nil
}

func writeUndo(entries []UndoEntry) error {
	path, err := undoPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// RecordUndo snapshots the current state and the tips of the trunk and all
// tracked branches under label, so the operation about to run can be reverted
// with PopUndo. Branches whose tip cannot be resolved are omitted from the
// snapshot rather than failing the whole record.
func (s *State) RecordUndo(label string) error {
	stateBytes, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state for undo: %w", err)
	}
	refs := map[string]string{}
	if sha, err := git.RevParse("refs/heads/" + s.Trunk); err == nil {
		refs[s.Trunk] = sha
	}
	for name := range s.Branches {
		if sha, err := git.RevParse("refs/heads/" + name); err == nil {
			refs[name] = sha
		}
	}
	entries, err := loadUndo()
	if err != nil {
		return err
	}
	entries = append(entries, UndoEntry{Label: label, State: stateBytes, Refs: refs})
	if len(entries) > maxUndoEntries {
		entries = entries[len(entries)-maxUndoEntries:]
	}
	return writeUndo(entries)
}

// PopUndo removes and returns the most recent undo entry. The boolean is false
// when the journal is empty.
func PopUndo() (*UndoEntry, bool, error) {
	entries, err := loadUndo()
	if err != nil {
		return nil, false, err
	}
	if len(entries) == 0 {
		return nil, false, nil
	}
	last := entries[len(entries)-1]
	if err := writeUndo(entries[:len(entries)-1]); err != nil {
		return nil, false, err
	}
	return &last, true, nil
}

// RestoreState overwrites the on-disk state file with raw bytes (used by undo to
// roll the metadata back to a snapshot).
func RestoreState(raw []byte) error {
	path, err := statePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		raw = append(raw, '\n')
	}
	return os.WriteFile(path, raw, 0o644)
}
