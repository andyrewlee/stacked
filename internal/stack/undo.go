package stack

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/andyrewlee/stacked/internal/git"
)

// maxUndoEntries bounds the size of the undo journal.
const maxUndoEntries = 20

// zeroSHA is git's all-zeros object id for an absent ref (used in CAS updates).
const zeroSHA = "0000000000000000000000000000000000000000"

// UndoEntry is a reversible snapshot taken before a mutating operation: the
// state-file contents and the tip SHAs of the trunk and every tracked branch at
// that moment.
type UndoEntry struct {
	Label           string            `json:"label"`
	State           json.RawMessage   `json:"state"`
	Refs            map[string]string `json:"refs"`
	PostRefs        map[string]string `json:"postRefs,omitempty"`
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
		return nil, nil
	}
	valid := make([]UndoEntry, 0, len(entries))
	for i := range entries {
		if err := validateUndoEntry(&entries[i]); err == nil {
			valid = append(valid, entries[i])
		}
	}
	return valid, nil
}

func validateUndoEntry(e *UndoEntry) error {
	checkOID := func(kind, name, val string) error {
		if !git.IsHex40(val) || val == zeroSHA {
			return fmt.Errorf("undo entry %q has a malformed %s for %q: %q", e.Label, kind, name, val)
		}
		return nil
	}
	checkName := func(kind, name string) error {
		if name == "" {
			return fmt.Errorf("undo entry %q has a malformed %s: %q", e.Label, kind, name)
		}
		for i := 0; i < len(name); i++ {
			if name[i] <= 0x20 || name[i] == 0x7f {
				return fmt.Errorf("undo entry %q has a malformed %s: %q", e.Label, kind, name)
			}
		}
		return nil
	}
	checkMap := func(kind string, m map[string]string) error {
		names := make([]string, 0, len(m))
		for name := range m {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if err := checkName(kind+" key", name); err != nil {
				return err
			}
			if err := checkOID(kind, name, m[name]); err != nil {
				return err
			}
		}
		return nil
	}
	if err := checkMap("ref", e.Refs); err != nil {
		return err
	}
	if err := checkMap("postRef", e.PostRefs); err != nil {
		return err
	}
	return nil
}

var writeUndo = func(entries []UndoEntry) error {
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

func (s *State) snapshotUndo(g Git, label string) (*UndoEntry, error) {
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

// RecordUndo snapshots the current state and appends the entry to the undo journal.
func (s *State) RecordUndo(g Git, label string) (*UndoEntry, error) {
	entry, err := s.snapshotUndo(g, label)
	if err != nil {
		return nil, err
	}
	entries, err := loadUndo()
	if err != nil {
		return nil, err
	}
	entries = append(entries, *entry)
	if err := writeUndo(entries); err != nil {
		return nil, err
	}
	return entry, nil
}

func trimUndo() error {
	entries, err := loadUndo()
	if err != nil {
		return err
	}
	if len(entries) <= maxUndoEntries {
		return nil
	}
	return writeUndo(entries[len(entries)-maxUndoEntries:])
}

// PeekUndo returns the most recent undo entry without removing it.
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

func mutateLastUndo(missingErr error, fn func(*UndoEntry) error) error {
	entries, err := loadUndo()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return missingErr
	}
	if err := fn(&entries[len(entries)-1]); err != nil {
		return err
	}
	return writeUndo(entries)
}

// SetLastUndoCreatedBranches records local branches created by the latest op.
func SetLastUndoCreatedBranches(names []string) error {
	return mutateLastUndo(nil, func(e *UndoEntry) error {
		e.CreatedBranches = names
		return nil
	})
}

func finalizeEntry(created []string, postRefs map[string]string) error {
	entries, err := loadUndo()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	last := &entries[len(entries)-1]
	if len(created) > 0 {
		last.CreatedBranches = created
	}
	if len(postRefs) > 0 {
		last.PostRefs = postRefs
	}
	if len(entries) > maxUndoEntries {
		entries = entries[len(entries)-maxUndoEntries:]
	}
	return writeUndo(entries)
}

// FinalizeUndo annotates the tentative undo entry with created branches and
// post-op tips, or drops it if the op was a no-op.
func FinalizeUndo(g Git, s *State, entry *UndoEntry) error {
	if entry == nil {
		return trimUndo()
	}
	tips, tipsOK := readUndoTips(g)
	created := createdBranchesSinceTips(entry, tips, tipsOK)
	unchanged, err := sameState(s, entry.State)
	if err != nil {
		return err
	}
	if unchanged && refsUnchangedAgainstTips(entry, tips, tipsOK) {
		return DropUndo()
	}
	post := postRefsFor(entry, tips, tipsOK, created)
	if len(post) > 0 {
		entry.PostRefs = post
	}
	return finalizeEntry(created, post)
}

func postRefsFor(entry *UndoEntry, tips map[string]string, ok bool, created []string) map[string]string {
	if !ok {
		return nil
	}
	post := make(map[string]string, len(entry.Refs)+len(created))
	for name := range entry.Refs {
		if tip, ok := tips[name]; ok {
			post[name] = tip
		}
	}
	for _, name := range created {
		if tip, ok := tips[name]; ok {
			post[name] = tip
		}
	}
	return post
}

// CleanupUndoOnError drops the tentative undo entry when a failed op changed
// nothing, or retains it when partial changes were left behind.
func CleanupUndoOnError(g Git, s *State, opErr error) error {
	entry, _, err := PeekUndo()
	if err != nil {
		return fmt.Errorf("peek undo journal after failed op: %w", err)
	}
	dropped, created, err := dropNoopUndo(g, s, entry, opErr)
	if err != nil {
		return err
	}
	if !dropped && len(created) > 0 {
		if err := SetLastUndoCreatedBranches(created); err != nil {
			return err
		}
	}
	return trimUndo()
}

func dropNoopUndo(g Git, s *State, entry *UndoEntry, opErr error) (dropped bool, created []string, err error) {
	if entry == nil {
		return false, nil, nil
	}
	if errors.Is(opErr, ErrConflict) {
		if inProgress, err := g.RebaseInProgress(); err != nil {
			return false, nil, err
		} else if inProgress {
			tips, tipsOK := readUndoTips(g)
			return false, createdBranchesSinceTips(entry, tips, tipsOK), nil
		}
	}
	unchanged, err := sameState(s, entry.State)
	if err != nil {
		return false, nil, err
	}
	tips, tipsOK := readUndoTips(g)
	created = createdBranchesSinceTips(entry, tips, tipsOK)
	if !unchanged {
		return false, created, nil
	}
	if !refsUnchangedAgainstTips(entry, tips, tipsOK) {
		return false, created, nil
	}
	if len(created) > 0 {
		return false, created, nil
	}
	return true, nil, DropUndo()
}

func readUndoTips(g Git) (map[string]string, bool) {
	tips, err := g.Tips()
	if err != nil {
		return nil, false
	}
	return tips, true
}

func createdBranchesSinceTips(entry *UndoEntry, tips map[string]string, ok bool) []string {
	if entry == nil || entry.LocalBranches == nil || !ok {
		return nil
	}
	existed := map[string]bool{}
	for _, name := range entry.LocalBranches {
		existed[name] = true
	}
	var created []string
	for name := range tips {
		if !existed[name] {
			created = append(created, name)
		}
	}
	sort.Strings(created)
	return created
}

func refsUnchangedAgainstTips(entry *UndoEntry, tips map[string]string, ok bool) bool {
	if entry == nil || !ok {
		return false
	}
	for name, want := range entry.Refs {
		got, exists := tips[name]
		if !exists || got != want {
			return false
		}
	}
	return true
}

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
	if (s.PendingContinuation == nil) != (prev.PendingContinuation == nil) {
		return false, nil
	}
	if s.PendingContinuation != nil && (s.PendingContinuation.Branch != prev.PendingContinuation.Branch ||
		s.PendingContinuation.OrigBranch != prev.PendingContinuation.OrigBranch ||
		!slices.Equal(s.PendingContinuation.Scope, prev.PendingContinuation.Scope)) {
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

// RestoreState overwrites the on-disk state file with raw bytes.
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
