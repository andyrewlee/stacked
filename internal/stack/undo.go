package stack

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// maxUndoEntries bounds the size of the undo journal.
const maxUndoEntries = 20

// UndoEntry is a reversible snapshot taken before a mutating operation: the
// state-file contents and the tip SHAs of the trunk and every tracked branch at
// that moment.
type UndoEntry struct {
	Label           string            `json:"label"`
	State           json.RawMessage   `json:"state"`
	Refs            map[string]string `json:"refs"`
	LocalBranches   []string          `json:"localBranches,omitempty"`
	CreatedBranches []string          `json:"createdBranches,omitempty"`
	CurrentBranch   string            `json:"currentBranch,omitempty"`
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
		// A corrupt or truncated journal is recoverable state, not a fatal
		// error. Every mutating command records an undo entry first, so
		// returning an error here would block all mutations until the file was
		// manually deleted. Discard the unparseable journal and start fresh; the
		// worst case is losing undo history, never the stack itself (state.json
		// is written atomically and separately).
		return nil, nil
	}
	return entries, nil
}

func writeUndo(entries []UndoEntry) error {
	path, err := undoPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, append(data, '\n'))
}

// SnapshotUndo captures, through the Git port, everything needed to revert the
// operation about to run: the encoded state, the tips of the trunk and all
// tracked branches, the local branch list, and the current branch. It reads
// only — nothing is written to the journal. Branches whose tip cannot be
// resolved are omitted from the snapshot rather than failing the capture.
func (s *State) SnapshotUndo(g Git, label string) (*UndoEntry, error) {
	stateBytes, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode state for undo: %w", err)
	}
	refs := map[string]string{}
	if sha, err := g.RevParse("refs/heads/" + s.Trunk); err == nil {
		refs[s.Trunk] = sha
	}
	for name := range s.Branches {
		if sha, err := g.RevParse("refs/heads/" + name); err == nil {
			refs[name] = sha
		}
	}
	localBranches, _ := g.LocalBranches()
	currentBranch, _ := g.CurrentBranch()
	return &UndoEntry{
		Label:         label,
		State:         stateBytes,
		Refs:          refs,
		LocalBranches: localBranches,
		CurrentBranch: currentBranch,
	}, nil
}

// RecordUndo snapshots the current state via SnapshotUndo and appends the
// entry to the undo journal, so the operation about to run can be reverted
// with PopUndo.
func (s *State) RecordUndo(g Git, label string) error {
	entry, err := s.SnapshotUndo(g, label)
	if err != nil {
		return err
	}
	entries, err := loadUndo()
	if err != nil {
		return err
	}
	entries = append(entries, *entry)
	return writeUndo(entries)
}

// TrimUndo bounds the undo log to the most recent entries. Callers run this
// only after a command has produced a real undoable change; failed no-op
// commands can drop their tentative entry first without evicting older history.
func TrimUndo() error {
	entries, err := loadUndo()
	if err != nil {
		return err
	}
	if len(entries) <= maxUndoEntries {
		return nil
	}
	return writeUndo(entries[len(entries)-maxUndoEntries:])
}

// PeekUndo returns the most recent undo entry without removing it. The boolean
// is false when the journal is empty.
func PeekUndo() (*UndoEntry, bool, error) {
	entries, err := loadUndo()
	if err != nil {
		return nil, false, err
	}
	if len(entries) == 0 {
		return nil, false, nil
	}
	last := entries[len(entries)-1]
	return &last, true, nil
}

// DropUndo removes the most recent undo entry.
func DropUndo() error {
	entries, err := loadUndo()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	return writeUndo(entries[:len(entries)-1])
}

// SetLastUndoCreatedBranches records local branches that were created by the
// in-progress operation represented by the latest undo entry.
func SetLastUndoCreatedBranches(names []string) error {
	entries, err := loadUndo()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	entries[len(entries)-1].CreatedBranches = names
	return writeUndo(entries)
}

// PopUndo removes and returns the most recent undo entry. The boolean is false
// when the journal is empty.
func PopUndo() (*UndoEntry, bool, error) {
	last, ok, err := PeekUndo()
	if err != nil || !ok {
		return last, ok, err
	}
	if err := DropUndo(); err != nil {
		return nil, false, err
	}
	return last, true, nil
}

// RestoreState overwrites the on-disk state file with raw bytes (used by undo to
// roll the metadata back to a snapshot).
func RestoreState(raw []byte) error {
	path, err := statePath()
	if err != nil {
		return err
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		raw = append(raw, '\n')
	}
	return atomicWriteFile(path, raw)
}
