package stack

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andyrewlee/stacked/internal/git"
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
	// PostRefs records where every recorded branch (plus every branch the
	// operation created) actually stood AFTER the operation completed — the
	// compare-and-swap expectation undo verifies before restoring Refs. A
	// recorded branch whose live tip matches neither its Refs value nor its
	// PostRefs value moved outside st; a branch absent from PostRefs was
	// deleted by the op and may only be resurrected while still absent. nil
	// marks an entry written before the field existed (or one retained for a
	// FAILED operation, where abort/continue legitimately moves refs between
	// the failure and the undo): those restore unconditionally, as they
	// always did.
	PostRefs         map[string]string `json:"postRefs,omitempty"`
	LocalBranches    []string          `json:"localBranches,omitempty"`
	CreatedBranches  []string          `json:"createdBranches,omitempty"`
	CreatedWorktrees map[string]string `json:"createdWorktrees,omitempty"`
	CurrentBranch    string            `json:"currentBranch,omitempty"`
	// AbsorbedCommits maps each absorb target branch to the amended tip that
	// carried the caller's staged edits — recorded so undo can name the
	// commits it is about to orphan. Written only for absorb entries.
	AbsorbedCommits map[string]string `json:"absorbedCommits,omitempty"`
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
	// Entries carrying ref values that are not full object ids are dropped
	// like unparseable JSON: the journal is user-writable, and a revision
	// expression (HEAD~2), the all-zeros delete value, or garbage handed to
	// update-ref would resolve to a commit undo never recorded. Dropping the
	// entry (and persisting the filtered list on the next write) is the same
	// self-healing the corrupt-journal path already commits to.
	valid := make([]UndoEntry, 0, len(entries))
	for i := range entries {
		if err := validateUndoEntry(&entries[i]); err == nil {
			valid = append(valid, entries[i])
		}
	}
	return valid, nil
}

// validateUndoEntry verifies every object-id-bearing journal value is a full
// nonzero 40-hex oid and every ref-bearing key is a plausible branch name.
// loadUndo drops failing entries; Undo re-checks the entry it is handed so a
// caller that bypassed the journal cannot smuggle unvalidated values into
// update-ref either.
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
	if err := checkMap("absorbed commit", e.AbsorbedCommits); err != nil {
		return err
	}
	return nil
}

// writeUndo is the journal's single write funnel — a var so tests can count
// the writes a finalize path performs (the coalescing contract is "one
// journal write", which only a funnel-wide count can pin).
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

// snapshotUndo captures, through the Git port, everything needed to revert the
// operation about to run: the encoded state, the tips of the trunk and all
// tracked branches, the local branch list, and the current branch. It reads
// only — nothing is written to the journal. A failure to read branch tips fails
// the whole snapshot: an entry without them would yield an `st undo` that
// silently restored no refs, so the caller (RecordUndo) aborts the mutation
// before anything changes rather than recording an un-revertible entry.
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

// RecordUndo snapshots the current state via snapshotUndo, appends the entry
// to the undo journal so the operation about to run can be reverted by st
// undo, and returns the recorded entry — the caller annotates THAT object in
// FinalizeUndo rather than re-reading the journal (a peek that can only fail
// or race a concurrent append has nothing to add).
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

// trimUndo bounds the undo log to the most recent entries. Callers run this
// only after a command has produced a real undoable change; failed no-op
// commands can drop their tentative entry first without evicting older history.
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

// ListUndo returns every journal entry in record order (oldest first) without
// modifying the journal — the read half of PeekUndo, for `st undo --list`.
func ListUndo() ([]UndoEntry, error) {
	return loadUndo()
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

// mutateLastUndo loads the journal, applies fn to its newest entry, and
// rewrites it. An empty journal is a no-op unless missingErr is non-nil, which
// is returned instead — a caller that promises the entry exists surfaces its
// absence rather than silently no-oping. When fn fails the journal is left
// untouched.
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

// SetLastUndoCreatedBranches records local branches that were created by the
// in-progress operation represented by the latest undo entry.
func SetLastUndoCreatedBranches(names []string) error {
	return mutateLastUndo(nil, func(e *UndoEntry) error {
		e.CreatedBranches = names
		return nil
	})
}

// SetLastUndoCreatedWorktrees records linked worktrees materialized by the
// in-progress operation, keyed by branch name.
func SetLastUndoCreatedWorktrees(paths map[string]string) error {
	return mutateLastUndo(nil, func(e *UndoEntry) error {
		e.CreatedWorktrees = paths
		return nil
	})
}

// SetLastUndoAbsorbed records, on the latest undo entry, the absorb target →
// amended-tip map so a later undo can tell the caller which commits still hold
// the staged edits its ref-restore is about to orphan. It is absorb's
// durable-recovery checkpoint, called while the op's own journal entry is
// active — an absent journal or a latest entry that is not an absorb means
// the pointer would attach to the wrong operation (or nowhere), which must
// surface as an error rather than a silent no-op claiming durability.
func SetLastUndoAbsorbed(commits map[string]string) error {
	return mutateLastUndo(
		fmt.Errorf("cannot record absorb recovery commits: the undo journal is empty"),
		func(e *UndoEntry) error {
			if e.Label != "absorb" {
				return fmt.Errorf("cannot record absorb recovery commits: latest undo entry is %q, not an absorb", e.Label)
			}
			e.AbsorbedCommits = commits
			return nil
		})
}

// absorbedCommitsNote renders the recovery pointer Undo and UndoPreview share
// for absorb entries: which commits hold the staged edits once the recorded
// refs are restored, and how to get them back. Deterministic — sorted by
// branch.
func absorbedCommitsNote(commits map[string]string) string {
	names := make([]string, 0, len(commits))
	for name := range commits {
		names = append(names, name)
	}
	sort.Strings(names)
	pairs := make([]string, 0, len(names))
	shas := make([]string, 0, len(names))
	for _, name := range names {
		pairs = append(pairs, fmt.Sprintf("%s: %s", name, commits[name]))
		shas = append(shas, commits[name])
	}
	return fmt.Sprintf("the staged edits live in commit(s) %s — undo restores the branch refs, not those commits; recover with `git cherry-pick %s`", strings.Join(pairs, ", "), strings.Join(shas, " "))
}

// finalizeEntry applies the post-op annotations — the branches the operation
// created and the post-op tips — to the newest journal entry and trims the
// log, in ONE load-mutate-write. It replaces the three separate journal
// round-trips FinalizeUndo used to pay: those annotations are bookkeeping,
// not crash boundaries (RecordUndo's pre-op write is the real boundary —
// every journal write is atomic, so the merged write can't expose a torn
// intermediate state).
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

// FinalizeUndo completes the undo protocol after a successful mutation: the
// tentative entry is annotated with the branches the operation created and
// the post-operation tips those branches (and the ones the entry recorded)
// now stand at, dropped when the operation turned out to be a no-op (state,
// refs, and branch set all unchanged), and otherwise kept, trimming the
// journal — all in one finalizeEntry write.
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
	// The entry is being retained for a real change: pin the post-operation
	// tips undo will compare-and-swap against. A Tips read failure leaves
	// PostRefs nil — the entry degrades to the legacy unconditional restore
	// rather than failing the mutation's cleanup.
	post := postRefsFor(entry, tips, tipsOK, created)
	if len(post) > 0 {
		entry.PostRefs = post
	}
	return finalizeEntry(created, post)
}

// postRefsFor captures where the entry's recorded branches — and the branches
// the operation created — stand now that the operation has completed.
// Branches the op deleted are simply absent: their absence IS the expectation
// (undo may resurrect them only while they stay absent).
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

// CleanupUndoOnError completes the undo protocol after a failed mutation: the
// tentative entry is dropped when the failure changed nothing (so a failed
// no-op never evicts older history), but kept — annotated with any created
// branches — when the failure left real changes behind, including a conflict
// that left a rebase in progress.
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

// dropNoopUndo drops the tentative entry when the operation changed nothing:
// same state, every recorded ref on its recorded tip, and no branch created.
// A conflict with a rebase still in progress always keeps the entry — the
// mutation is half-applied, exactly what undo protects.
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

// createdBranchesSinceTips returns the local branches that exist now but were
// not in the entry's captured branch list, sorted.
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

// refsUnchangedAgainstTips reports whether every ref the entry recorded still
// resolves to its recorded tip.
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
