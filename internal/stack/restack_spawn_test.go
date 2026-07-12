package stack

import (
	"errors"
	"fmt"
	"testing"
)

// TestRestackBranchRestoresHEADAfterNonConflictFailure is the
// characterization test for the expected-HEAD threading: a non-conflict
// rebase failure must restore HEAD to where the caller started — the value
// restackBranchWith used to read via CurrentBranch per branch and now
// receives threaded from the cascade.
func TestRestackBranchRestoresHEADAfterNonConflictFailure(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	f.commit("advance main") // a now drifts
	f.rebaseErr["a"] = errors.New("pre-rebase hook rejected")

	if _, err := s.RestackBranch(env, "a"); err == nil {
		t.Fatal("expected the injected rebase failure to surface")
	}
	if f.head != "main" {
		t.Fatalf("HEAD = %q after a failed rebase, want it restored to main", f.head)
	}
}

// TestRestackCascadeAvoidsPerBranchCurrentBranch pins PERF-01: the cascade
// reads CurrentBranch a CONSTANT number of times, independent of how many
// branches it rebases (previously one read per drifted branch).
func TestRestackCascadeAvoidsPerBranchCurrentBranch(t *testing.T) {
	countFor := func(n int) int {
		f, s, env := newEnvState()
		parent := "main"
		for i := 1; i <= n; i++ {
			name := fmt.Sprintf("b%d", i)
			mkBranch(t, env, s, f, parent, name)
			parent = name
		}
		if err := f.Checkout("main"); err != nil {
			t.Fatal(err)
		}
		f.commit("advance main") // whole stack drifts
		spy := &tipReadSpyGit{Git: f}
		env.Git = spy
		if _, err := Restack(env, s); err != nil {
			t.Fatalf("restack N=%d: %v", n, err)
		}
		return spy.currentBranchCalls
	}
	small, large := countFor(3), countFor(8)
	if small != large {
		t.Fatalf("CurrentBranch calls scale with stack size: N=3 -> %d, N=8 -> %d", small, large)
	}
}

// TestRestackLeafSkipsPostRebaseRevParse pins PERF-02: only branches with
// children refresh their tip after a rebase; a fan of K leaves costs O(1)
// post-rebase rev-parse, not O(K).
func TestRestackLeafSkipsPostRebaseRevParse(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "anchor")
	for i := 1; i <= 4; i++ {
		mkBranch(t, env, s, f, "anchor", fmt.Sprintf("leaf%d", i))
	}
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	f.commit("advance main") // anchor + all leaves drift

	spy := &tipReadSpyGit{Git: f}
	env.Git = spy
	res, err := Restack(env, s)
	if err != nil {
		t.Fatalf("restack: %v", err)
	}
	if len(res.Restacked) != 5 {
		t.Fatalf("restacked = %v, want anchor + 4 leaves", res.Restacked)
	}
	// Exactly one post-rebase tip refresh: anchor (has children). The leaves'
	// refreshes are skipped, and the cascade's Tips() seed plus the map cover
	// every parent lookup, so no other RevParse fires.
	if spy.revParseCalls != 1 {
		t.Fatalf("revParseCalls = %d, want 1 (anchor only)", spy.revParseCalls)
	}
}
