package stack

import (
	"errors"
	"testing"
)

// TestRestackPersistsEachBranchBeforeMidwayConflict pins the engine's per-branch
// save checkpoint: when a multi-branch restack stalls on a conflict partway up
// the stack, the branches that restacked BEFORE the conflict must already be
// persisted, so `st continue` (a fresh process loading from disk) resumes from
// the progress made instead of losing it.
//
// The existing Save-seam tests cover the onto reparent
// (TestOntoPersistsReparentBeforeRestackingDescendants) and the per-prune save
// (TestSyncPersistsEachSuccessfulPrune); this covers the restack cascade, the
// path CLAUDE.md calls out ("checkpoint at safe points ... so a later conflict
// cannot lose progress").
func TestRestackPersistsEachBranchBeforeMidwayConflict(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "b", "c")

	// Advance the trunk so the whole stack (a, b, c) is out of date.
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	f.commit("new-main")
	newMain, _ := f.RevParse("main")

	// The cascade restacks a, then b, then stalls on c.
	f.conflictOn("c")

	// Record the state at every checkpoint the engine takes. Set AFTER the
	// mkBranch setup so only the restack's saves are captured.
	var saved []*State
	env.Save = func() error {
		saved = append(saved, cloneState(s))
		return nil
	}

	if _, err := Restack(env, s); !errors.Is(err, ErrConflict) {
		t.Fatalf("Restack error = %v, want ErrConflict", err)
	}
	if len(saved) == 0 {
		t.Fatal("restack persisted nothing before the conflict; progress would be lost")
	}

	// The most recent checkpoint must show a and b already rebased onto their
	// parents' new tips — proving the per-branch save ran before c conflicted.
	last := saved[len(saved)-1]
	if a, ok := last.Get("a"); !ok || a.ParentSHA != newMain {
		t.Fatalf("persisted a.ParentSHA = %v (ok=%v), want %s", branchSHA(a), ok, newMain)
	}
	aTip, _ := f.RevParse("a")
	if b, ok := last.Get("b"); !ok || b.ParentSHA != aTip {
		t.Fatalf("persisted b.ParentSHA = %v (ok=%v), want %s", branchSHA(b), ok, aTip)
	}
}

func branchSHA(b *Branch) string {
	if b == nil {
		return "<nil>"
	}
	return b.ParentSHA
}
