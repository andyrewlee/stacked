package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

// Tests for the two failure arms of the mutation pipeline (mutate.go:
// lock -> load -> RecordUndo -> op -> Save -> FinalizeUndo) where a silent
// bug loses undo data:
//
//   - A post-op Save failure must surface the error AND keep the tentative
//     journal entry — the operation happened, so that entry is the only
//     record of the pre-op state.
//   - A RecordUndo failure must abort before the op runs — a mutation with
//     no journal entry could never be undone.
//
// Both faults are injected at the filesystem: a directory where the atomic
// writer expects a file fails deterministically (os.ReadFile on a directory
// errors; os.Rename refuses to replace a directory with a file), with no
// test seam in production code.

// stackedFilePath returns <common git dir>/stacked/<name>, the on-disk
// location of state.json / undo.json / the lock file.
func stackedFilePath(t *testing.T, name string) string {
	t.Helper()
	gitDir, err := git.GitCommonDir()
	if err != nil {
		t.Fatalf("git common dir: %v", err)
	}
	return filepath.Join(gitDir, "stacked", name)
}

func TestMutateSaveFailureKeepsUndoEntry(t *testing.T) {
	newRepo(t)
	mustInit(t)
	statePath := stackedFilePath(t, "state.json")

	// The swap must happen inside the op — after lockAndLoad has already read
	// state.json — so the test drives mutate with the real create op plus
	// fault injection. stack.Create checkpoints via env.Save() mid-op, so the
	// injected directory defeats only the protocol's final s.Save().
	err := mutate("create", false, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		res, err := stack.Create(env, s, "feat-x", "", false)
		if err != nil {
			return nil, err
		}
		if err := os.Remove(statePath); err != nil {
			return nil, err
		}
		if err := os.Mkdir(statePath, 0o755); err != nil {
			return nil, err
		}
		return res, nil
	})
	if err == nil || !strings.Contains(err.Error(), "saving stack state") {
		t.Fatalf("mutate with state.json a directory = %v, want a saving stack state error", err)
	}

	// The op ran and its effects are real: the branch exists, and the state
	// file really was not persisted by the post-op save.
	if !git.BranchExists("feat-x") {
		t.Fatal("feat-x missing: the op did not run")
	}
	if _, err := stack.Load(); err == nil {
		t.Fatal("state.json unexpectedly loadable: the post-op save did not fail")
	}

	// The tentative undo entry must survive: the operation happened, so the
	// journal is the only record of the pre-op state.
	entry, ok, err := stack.PeekUndo()
	if err != nil || !ok {
		t.Fatalf("peek undo after save failure = ok %v err %v, want a preserved entry", ok, err)
	}
	if entry.Label != "create" {
		t.Fatalf("undo entry label = %q, want %q", entry.Label, "create")
	}
	if _, tracked := entry.Refs["main"]; !tracked {
		t.Fatal("undo entry has no ref snapshot for trunk: the entry cannot revert anything")
	}
}

func TestMutateUndoJournalFailureRunsNothing(t *testing.T) {
	newRepo(t)
	mustInit(t)

	// undo.json does not exist yet (no mutation has run), so a plain Mkdir
	// puts a directory where RecordUndo's read/write expects a file.
	if err := os.Mkdir(stackedFilePath(t, "undo.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := runCreate([]string{"feat-y"})
	if err == nil || !strings.Contains(err.Error(), "undo") {
		t.Fatalf("create with undo.json a directory = %v, want a journal error", err)
	}

	// Fail-before-mutate ordering: the op must not have run at all — no
	// branch, no tracked state.
	if git.BranchExists("feat-y") {
		t.Fatal("feat-y exists: the op ran despite the RecordUndo failure")
	}
	if s := stateT(t); s.IsTracked("feat-y") || len(s.Branches) != 0 {
		t.Fatalf("state changed despite the RecordUndo failure: %+v", s.Branches)
	}
}

// TestMutateOpFailureSurfacesUnreadableUndoJournal pins plan-008: when the op
// fails AND the cleanup-time peek of the journal fails, both errors must
// surface — a swallowed peek error would keep the tentative entry silently
// unannotated.
func TestMutateOpFailureSurfacesUnreadableUndoJournal(t *testing.T) {
	newRepo(t)
	mustInit(t)
	undoPath := stackedFilePath(t, "undo.json")

	err := mutate("create", false, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		// Between RecordUndo's append (already on disk) and the cleanup peek:
		// swap the journal file for a directory so PeekUndo fails on read.
		if err := os.Remove(undoPath); err != nil {
			return nil, err
		}
		if err := os.Mkdir(undoPath, 0o755); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("op exploded")
	})
	if err == nil {
		t.Fatal("mutate with failing op + unreadable journal returned nil error")
	}
	if !strings.Contains(err.Error(), "op exploded") {
		t.Fatalf("mutate error = %v, want the op error surfaced", err)
	}
	if !strings.Contains(err.Error(), "clean up undo entry") {
		t.Fatalf("mutate error = %v, want the journal-peek failure surfaced alongside", err)
	}
}
