package stack

import (
	"fmt"
	"strings"
	"testing"

	"github.com/andyrewlee/stacked/internal/git"
)

// absorbEnv builds main -> a -> b -> c with HEAD on c and returns the pieces
// plus each branch's tip, mirroring the absorb design spike's 3-branch stack.
func absorbEnv(t *testing.T) (*fakeGit, *State, Env, map[string]string) {
	t.Helper()
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "b", "c")
	tips := map[string]string{}
	for _, name := range []string{"main", "a", "b", "c"} {
		tip, err := f.RevParse(name)
		if err != nil {
			t.Fatalf("tip %s: %v", name, err)
		}
		tips[name] = tip
	}
	return f, s, env, tips
}

// The five v1 attribution cases from the absorb design spike, driven through
// the fake git with canned hunks + blame. Refusals must never be errors.
func TestAbsorbPlanAttribution(t *testing.T) {
	t.Run("single target absorbs into the owning branch tip", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
		f.blame = map[string]map[int]string{"f.txt": {2: tips["a"]}}

		res, err := AbsorbPlan(env, s)
		if err != nil {
			t.Fatalf("AbsorbPlan: %v", err)
		}
		if len(res.Absorbed) != 1 || len(res.Refused) != 0 {
			t.Fatalf("result = %+v, want one absorbed, none refused", res)
		}
		got := res.Absorbed[0]
		if got.Branch != "a" || got.Commit != tips["a"] || got.File != "f.txt" || got.Lines != "2" {
			t.Fatalf("absorbed = %+v, want branch a at its tip, f.txt:2", got)
		}
		if !res.DryRun {
			t.Fatal("AbsorbPlan result must be DryRun")
		}
	})

	t.Run("hunk spanning two stack commits is refused", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 3, OldN: 2, NewStart: 3, NewN: 2}}
		f.blame = map[string]map[int]string{"f.txt": {3: tips["a"], 4: tips["b"]}}

		res, err := AbsorbPlan(env, s)
		if err != nil {
			t.Fatalf("AbsorbPlan: %v", err)
		}
		if len(res.Refused) != 1 || len(res.Absorbed) != 0 {
			t.Fatalf("result = %+v, want one refusal", res)
		}
		if !strings.Contains(res.Refused[0].Reason, "spans") {
			t.Fatalf("reason = %q, want a spans refusal", res.Refused[0].Reason)
		}
		if res.Refused[0].Lines != "3-4" {
			t.Fatalf("lines = %q, want 3-4", res.Refused[0].Lines)
		}
	})

	t.Run("line owned by trunk is refused", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 1, OldN: 1, NewStart: 1, NewN: 1}}
		f.blame = map[string]map[int]string{"f.txt": {1: tips["main"]}}

		res, err := AbsorbPlan(env, s)
		if err != nil {
			t.Fatalf("AbsorbPlan: %v", err)
		}
		if len(res.Refused) != 1 || !strings.Contains(res.Refused[0].Reason, "trunk") {
			t.Fatalf("result = %+v, want a trunk refusal", res)
		}
	})

	t.Run("pure addition is refused", func(t *testing.T) {
		f, s, env, _ := absorbEnv(t)
		f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 7, OldN: 0, NewStart: 8, NewN: 1}}

		res, err := AbsorbPlan(env, s)
		if err != nil {
			t.Fatalf("AbsorbPlan: %v", err)
		}
		if len(res.Refused) != 1 || !strings.Contains(res.Refused[0].Reason, "pure addition") {
			t.Fatalf("result = %+v, want a pure-addition refusal", res)
		}
	})

	t.Run("stack commit that is not a branch tip is refused", func(t *testing.T) {
		f, s, env, _ := absorbEnv(t)
		// Advance b past its recorded tip so the OLD tip is a stack commit that
		// is no longer any branch's tip.
		if err := f.Checkout("b"); err != nil {
			t.Fatal(err)
		}
		oldBTip, _ := f.RevParse("b")
		f.commit("advance b")
		if err := f.Checkout("c"); err != nil {
			t.Fatal(err)
		}
		f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 5, OldN: 1, NewStart: 5, NewN: 1}}
		f.blame = map[string]map[int]string{"f.txt": {5: oldBTip}}

		res, err := AbsorbPlan(env, s)
		if err != nil {
			t.Fatalf("AbsorbPlan: %v", err)
		}
		if len(res.Refused) != 1 || !strings.Contains(res.Refused[0].Reason, "not a branch tip") {
			t.Fatalf("result = %+v, want a non-tip refusal", res)
		}
	})
}

// TestAbsorbApply drives the v1 apply slice over the fake git: a single-target
// plan amends the owning tip in place, cascades the descendants, drops the
// staged copy from the current worktree, and returns HEAD to where it started.
func TestAbsorbApply(t *testing.T) {
	stage := func(f *fakeGit, tips map[string]string, owner string) {
		f.staged = true
		f.stagedPatch = []byte("fake patch")
		f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
		f.blame = map[string]map[int]string{"f.txt": {2: tips[owner]}}
	}

	t.Run("single target amends the tip and cascades", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stage(f, tips, "a")

		res, err := Absorb(env, s)
		if err != nil {
			t.Fatalf("Absorb: %v", err)
		}
		if res.DryRun {
			t.Fatal("applied result still marked DryRun")
		}
		newATip := f.branches["a"]
		if newATip == tips["a"] {
			t.Fatal("a's tip unchanged; the amend did not land")
		}
		a, _ := s.Get("a")
		if a.ParentSHA != tips["main"] {
			t.Fatalf("a.ParentSHA = %q, want unchanged %q (amend in place, not re-parent)", a.ParentSHA, tips["main"])
		}
		if len(res.Absorbed) != 1 || res.Absorbed[0].Branch != "a" || res.Absorbed[0].Commit != newATip {
			t.Fatalf("Absorbed = %+v, want one entry on a's NEW tip %s", res.Absorbed, newATip)
		}
		if len(res.Restacked) != 2 || res.Restacked[0] != "b" || res.Restacked[1] != "c" {
			t.Fatalf("Restacked = %v, want [b c]", res.Restacked)
		}
		b, _ := s.Get("b")
		c, _ := s.Get("c")
		if b.ParentSHA != newATip || c.ParentSHA != f.branches["b"] {
			t.Fatalf("cascade bookkeeping: b.ParentSHA=%q c.ParentSHA=%q, want %q and %q", b.ParentSHA, c.ParentSHA, newATip, f.branches["b"])
		}
		// The staged copy was dropped from the CURRENT worktree only, and HEAD
		// is back on c.
		if len(f.resetHardDirs) != 1 || f.resetHardDirs[0] != "" {
			t.Fatalf("resetHardDirs = %q, want one reset of the current worktree", f.resetHardDirs)
		}
		if f.head != "c" {
			t.Fatalf("HEAD = %q after absorb, want c", f.head)
		}
	})

	t.Run("one undo entry reverts the amend and the cascade", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stage(f, tips, "a")
		entry := mustSnapshot(t, s, f, "absorb")

		if _, err := Absorb(env, s); err != nil {
			t.Fatalf("Absorb: %v", err)
		}
		if _, err := Undo(env, s, entry); err != nil {
			t.Fatalf("Undo: %v", err)
		}
		assertUndoRestored(t, f, s, entry)
	})

	t.Run("absorbing into the current branch needs no reset", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stage(f, tips, "c")

		res, err := Absorb(env, s)
		if err != nil {
			t.Fatalf("Absorb: %v", err)
		}
		if f.branches["c"] == tips["c"] {
			t.Fatal("c's tip unchanged; the amend did not land")
		}
		if len(res.Restacked) != 0 {
			t.Fatalf("Restacked = %v, want none (c is the top)", res.Restacked)
		}
		if len(f.resetHardDirs) != 0 {
			t.Fatalf("resetHardDirs = %q; amending the current tip must not reset (the index self-resolves)", f.resetHardDirs)
		}
	})

	t.Run("multi-target plan is returned as data, unapplied", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stage(f, tips, "a")
		f.stagedHunks = append(f.stagedHunks, git.Hunk{File: "f.txt", OldStart: 5, OldN: 1, NewStart: 5, NewN: 1})
		f.blame["f.txt"][5] = tips["b"]

		res, err := Absorb(env, s)
		if err != nil {
			t.Fatalf("Absorb: %v", err)
		}
		if !res.DryRun || !strings.HasPrefix(res.Summary, "not applied: absorb v1 handles a single target") {
			t.Fatalf("result = %+v, want the unapplied plan", res)
		}
		if f.branches["a"] != tips["a"] || f.branches["b"] != tips["b"] {
			t.Fatal("a multi-target plan mutated refs")
		}
	})

	t.Run("any refusal blocks the apply", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stage(f, tips, "a")
		f.stagedHunks = append(f.stagedHunks, git.Hunk{File: "f.txt", OldStart: 9, OldN: 0, NewStart: 10, NewN: 1})

		res, err := Absorb(env, s)
		if err != nil {
			t.Fatalf("Absorb: %v", err)
		}
		if !res.DryRun || len(res.Refused) != 1 || f.branches["a"] != tips["a"] {
			t.Fatalf("result = %+v (a=%s), want unapplied with the refusal intact", res, f.branches["a"])
		}
	})

	t.Run("dirty owner worktree skips with a note", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stage(f, tips, "a")
		f.addWorktree("/wt/a", "a")
		f.dirtyWT = map[string]bool{"a": true}

		res, err := Absorb(env, s)
		if err != nil {
			t.Fatalf("Absorb: %v", err)
		}
		if !res.DryRun || f.branches["a"] != tips["a"] {
			t.Fatalf("result = %+v, want unapplied (dirty owner)", res)
		}
		if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "/wt/a") {
			t.Fatalf("Notes = %v, want the dirty-worktree note", res.Notes)
		}
	})

	t.Run("a non-applying patch is an error with nothing mutated", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stage(f, tips, "a")
		f.applyErr = fmt.Errorf("patch does not apply")

		if _, err := Absorb(env, s); err == nil || !strings.Contains(err.Error(), "does not apply") {
			t.Fatalf("Absorb = %v, want the apply failure surfaced", err)
		}
		if f.branches["a"] != tips["a"] || f.staged != true {
			t.Fatal("failed apply must leave refs and the staged edit untouched")
		}
	})
}

// TestAbsorbPlanGuards pins the absorb-local preconditions: nothing staged is
// a clean no-op, and UNSTAGED changes refuse (while a staged index — dirty by
// requireClean's standard — is precisely absorb's input and allowed).
func TestAbsorbPlanGuards(t *testing.T) {
	f, s, env, tips := absorbEnv(t)
	res, err := AbsorbPlan(env, s)
	if err != nil {
		t.Fatalf("AbsorbPlan with nothing staged: %v", err)
	}
	if res.Summary != "nothing to absorb" || len(res.Absorbed) != 0 || len(res.Refused) != 0 {
		t.Fatalf("result = %+v, want the nothing-to-absorb no-op", res)
	}

	f.clean = false // dirty with nothing staged = unstaged changes in the fake
	if _, err := AbsorbPlan(env, s); err == nil || !strings.Contains(err.Error(), "unstaged") {
		t.Fatalf("AbsorbPlan with unstaged changes = %v, want the unstaged refusal", err)
	}
	f.clean = true

	// A staged index alone must NOT refuse (requireClean would; absorb's guard
	// is unstaged-only). staged=true makes IsClean false but absorb proceeds.
	f.staged = true
	f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
	f.blame = map[string]map[int]string{"f.txt": {2: tips["a"]}}
	res, err = AbsorbPlan(env, s)
	if err != nil {
		t.Fatalf("AbsorbPlan with a staged index: %v", err)
	}
	if len(res.Absorbed) != 1 {
		t.Fatalf("result = %+v, want the staged hunk absorbed", res)
	}
}
