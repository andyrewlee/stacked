package stack

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
// only — nothing is written to the journal. A failure to read branch tips fails
// the whole snapshot: an entry without them would yield an `st undo` that
// silently restored no refs, so the caller (RecordUndo) aborts the mutation
// before anything changes rather than recording an un-revertible entry.
func (s *State) SnapshotUndo(g Git, label string) (*UndoEntry, error) {
	stateBytes, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode state for undo: %w", err)
	}
	tips, err := g.Tips()
	if err != nil {
		return nil, fmt.Errorf("snapshot branch tips for undo: %w", err)
	}
	refs := map[string]string{}
	if sha, ok := tips[s.Trunk]; ok {
		refs[s.Trunk] = sha
	}
	for name := range s.Branches {
		if sha, ok := tips[name]; ok {
			refs[name] = sha
		}
	}
	localBranches := make([]string, 0, len(tips))
	for name := range tips {
		localBranches = append(localBranches, name)
	}
	sort.Strings(localBranches)
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

// FinalizeUndo completes the undo protocol after a successful mutation: the
// tentative entry is annotated with the branches the operation created,
// dropped when the operation turned out to be a no-op (state, refs, and
// branch set all unchanged), and otherwise kept, trimming the journal.
func FinalizeUndo(g Git, s *State, entry *UndoEntry) error {
	if entry == nil {
		return TrimUndo()
	}
	created := createdBranchesSince(g, entry)
	if len(created) > 0 {
		if err := SetLastUndoCreatedBranches(created); err != nil {
			return err
		}
	}
	unchanged, err := sameState(s, entry.State)
	if err != nil {
		return err
	}
	if !unchanged {
		return TrimUndo()
	}
	if refsUnchanged(g, entry) {
		return DropUndo()
	}
	return TrimUndo()
}

// CleanupUndoOnError completes the undo protocol after a failed mutation: the
// tentative entry is dropped when the failure changed nothing (so a failed
// no-op never evicts older history), but kept — annotated with any created
// branches — when the failure left real changes behind, including a conflict
// that left a rebase in progress.
func CleanupUndoOnError(g Git, s *State, opErr error) error {
	entry, _, _ := PeekUndo()
	dropped, err := dropNoopUndo(g, s, entry, opErr)
	if err != nil {
		return err
	}
	if !dropped {
		created := createdBranchesSince(g, entry)
		if len(created) > 0 {
			if err := SetLastUndoCreatedBranches(created); err != nil {
				return err
			}
		}
	}
	return TrimUndo()
}

// dropNoopUndo drops the tentative entry when the operation changed nothing:
// same state, every recorded ref on its recorded tip, and no branch created.
// A conflict with a rebase still in progress always keeps the entry — the
// mutation is half-applied, exactly what undo protects.
func dropNoopUndo(g Git, s *State, entry *UndoEntry, opErr error) (bool, error) {
	if entry == nil {
		return false, nil
	}
	if errors.Is(opErr, ErrConflict) {
		if inProgress, err := g.RebaseInProgress(); err != nil {
			return false, err
		} else if inProgress {
			return false, nil
		}
	}
	unchanged, err := sameState(s, entry.State)
	if err != nil || !unchanged {
		return false, err
	}
	if !refsUnchanged(g, entry) {
		return false, nil
	}
	if len(createdBranchesSince(g, entry)) > 0 {
		return false, nil
	}
	return true, DropUndo()
}

// createdBranchesSince returns the local branches that exist now but were not
// in the entry's captured branch list, sorted.
func createdBranchesSince(g Git, entry *UndoEntry) []string {
	if entry == nil || entry.LocalBranches == nil {
		return nil
	}
	existed := map[string]bool{}
	for _, name := range entry.LocalBranches {
		existed[name] = true
	}
	branches, err := g.LocalBranches()
	if err != nil {
		return nil
	}
	var created []string
	for _, name := range branches {
		if !existed[name] {
			created = append(created, name)
		}
	}
	sort.Strings(created)
	return created
}

// refsUnchanged reports whether every ref the entry recorded still resolves to
// its recorded tip.
func refsUnchanged(g Git, entry *UndoEntry) bool {
	tips, err := g.Tips()
	if err != nil {
		return false
	}
	for name, want := range entry.Refs {
		if tips[name] != want {
			return false
		}
	}
	return true
}

// sameState reports whether s is semantically equal to the snapshot raw.
func sameState(s *State, raw []byte) (bool, error) {
	var prev State
	if err := json.Unmarshal(raw, &prev); err != nil {
		return false, err
	}
	if s.Trunk != prev.Trunk {
		return false, nil
	}
	if (s.PendingReparent == nil) != (prev.PendingReparent == nil) {
		return false, nil
	}
	if s.PendingReparent != nil && *s.PendingReparent != *prev.PendingReparent {
		return false, nil
	}
	if len(s.Branches) != len(prev.Branches) {
		return false, nil
	}
	for name, got := range s.Branches {
		want, ok := prev.Branches[name]
		if !ok || got == nil || want == nil || *got != *want {
			return false, nil
		}
	}
	return true, nil
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
