package stack

import (
	"errors"
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

	t.Run("unsupported staged record is refused and blocks the apply", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		f.staged = true
		f.stagedPatch = []byte("fake patch")
		f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
		f.blame = map[string]map[int]string{"f.txt": {2: tips["a"]}}
		f.stagedUnsupported = []git.UnsupportedRecord{{File: "bin.dat", Reason: "binary file"}}

		res, err := AbsorbPlan(env, s)
		if err != nil {
			t.Fatalf("AbsorbPlan: %v", err)
		}
		if len(res.Absorbed) != 1 || len(res.Refused) != 1 {
			t.Fatalf("result = %+v, want one absorbed and one refusal", res)
		}
		if !strings.Contains(res.Refused[0].Reason, "binary file") || res.Refused[0].File != "bin.dat" {
			t.Fatalf("refusal = %+v, want the binary record surfaced", res.Refused[0])
		}

		applied, err := Absorb(env, s)
		if err != nil {
			t.Fatalf("Absorb: %v", err)
		}
		if !applied.DryRun || !strings.HasPrefix(applied.Summary, "not applied:") {
			t.Fatalf("result = %+v, want the plan returned unapplied", applied)
		}
		if f.branches["a"] != tips["a"] {
			t.Fatal("an unsupported staged record must block the apply, but a's tip moved")
		}
	})

	t.Run("line missing from blame is refused as unattributable", func(t *testing.T) {
		f, s, env, _ := absorbEnv(t)
		f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
		f.blame = map[string]map[int]string{"f.txt": {}}

		res, err := AbsorbPlan(env, s)
		if err != nil {
			t.Fatalf("AbsorbPlan: %v", err)
		}
		if len(res.Refused) != 1 || !strings.Contains(res.Refused[0].Reason, "cannot attribute") {
			t.Fatalf("result = %+v, want the cannot-attribute refusal", res)
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

	t.Run("cascade conflict after the amend leaves the rebase paused for continue", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stage(f, tips, "a")
		f.conflictOn("b")

		res, err := Absorb(env, s)
		if res != nil || !errors.Is(err, ErrConflict) {
			t.Fatalf("Absorb = %+v, %v; want nil result and ErrConflict", res, err)
		}
		// The amend landed before the cascade paused: the edit is safe in a.
		amendedATip := f.branches["a"]
		if amendedATip == tips["a"] {
			t.Fatal("a's tip unchanged; the amend should persist through the conflict")
		}
		if inProg, _ := f.RebaseInProgress(); !inProg {
			t.Fatal("no rebase in progress after the cascade conflict")
		}
		if name, _ := f.RebaseHeadName(); name != "b" {
			t.Fatalf("paused rebase head = %q, want b", name)
		}

		// st continue finishes the paused rebase and restacks the rest.
		if _, err := Continue(env, s); err != nil {
			t.Fatalf("Continue: %v", err)
		}
		if f.branches["b"] == tips["b"] || f.branches["c"] == tips["c"] {
			t.Fatalf("b/c tips = %s/%s, want both moved by the resumed cascade", f.branches["b"], f.branches["c"])
		}
		b, _ := s.Get("b")
		if b.ParentSHA != amendedATip {
			t.Fatalf("b.ParentSHA = %q, want the amended a tip %q", b.ParentSHA, amendedATip)
		}
	})

	// stageTwoTargets stages two hunks owned by a and b respectively.
	stageTwoTargets := func(f *fakeGit, tips map[string]string) {
		stage(f, tips, "a")
		f.stagedHunks = append(f.stagedHunks, git.Hunk{File: "f.txt", OldStart: 5, OldN: 1, NewStart: 5, NewN: 1})
		f.blame["f.txt"][5] = tips["b"]
	}

	t.Run("multi-target plan amends every target and cascades once", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stageTwoTargets(f, tips)

		res, err := Absorb(env, s)
		if err != nil {
			t.Fatalf("Absorb: %v", err)
		}
		if res.DryRun {
			t.Fatal("applied result still marked DryRun")
		}
		newATip, newBTip := f.branches["a"], f.branches["b"]
		if newATip == tips["a"] || newBTip == tips["b"] {
			t.Fatalf("tips a=%s b=%s, want BOTH amended", newATip, newBTip)
		}
		if len(res.Absorbed) != 2 || res.Absorbed[0].Commit != newATip || res.Absorbed[1].Commit != newBTip {
			t.Fatalf("Absorbed = %+v, want commits on the two NEW tips", res.Absorbed)
		}
		// One cascade from the lowest target (a): b re-based onto amended a,
		// then c. b appears in Restacked because its recorded parent moved.
		if len(res.Restacked) != 2 || res.Restacked[0] != "b" || res.Restacked[1] != "c" {
			t.Fatalf("Restacked = %v, want [b c] from the single cascade", res.Restacked)
		}
		b, _ := s.Get("b")
		if b.ParentSHA != f.branches["a"] {
			t.Fatalf("b.ParentSHA = %q, want the amended a tip", b.ParentSHA)
		}
		if f.head != "c" {
			t.Fatalf("HEAD = %q, want restored to c", f.head)
		}
		if !strings.Contains(res.Summary, "into a, b") {
			t.Fatalf("summary = %q, want both targets named", res.Summary)
		}
	})

	t.Run("one undo entry reverts a multi-target absorb", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stageTwoTargets(f, tips)
		entry := mustSnapshot(t, s, f, "absorb")

		if _, err := Absorb(env, s); err != nil {
			t.Fatalf("Absorb: %v", err)
		}
		if _, err := Undo(env, s, entry); err != nil {
			t.Fatalf("Undo: %v", err)
		}
		assertUndoRestored(t, f, s, entry)
	})

	t.Run("one dirty owner among two targets blocks the whole plan", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stageTwoTargets(f, tips)
		f.addWorktree("/wt/b", "b")
		f.dirtyWT = map[string]bool{"b": true}

		res, err := Absorb(env, s)
		if err != nil {
			t.Fatalf("Absorb: %v", err)
		}
		if !res.DryRun || !strings.HasPrefix(res.Summary, "not applied: a target's worktree is dirty") {
			t.Fatalf("result = %+v, want the whole plan unapplied", res)
		}
		if f.branches["a"] != tips["a"] || f.branches["b"] != tips["b"] {
			t.Fatal("all-or-nothing violated: a ref moved despite the dirty owner")
		}
		if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "/wt/b") {
			t.Fatalf("Notes = %v, want the dirty-worktree path named", res.Notes)
		}
	})

	t.Run("cascade conflict after multi-target amends leaves both amends standing", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stageTwoTargets(f, tips)
		f.conflictOn("c")

		res, err := Absorb(env, s)
		if res != nil || !errors.Is(err, ErrConflict) {
			t.Fatalf("Absorb = %+v, %v; want ErrConflict", res, err)
		}
		if f.branches["a"] == tips["a"] || f.branches["b"] == tips["b"] {
			t.Fatal("both amends must persist through the conflict (undo-covered)")
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

	t.Run("non-conflict cascade failure names the recovery path", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stage(f, tips, "a")
		f.rebaseErr["b"] = fmt.Errorf("boom")

		_, err := Absorb(env, s)
		if err == nil || errors.Is(err, ErrConflict) {
			t.Fatalf("Absorb = %v, want a hard non-conflict error", err)
		}
		if !strings.Contains(err.Error(), "safely committed in a") || !strings.Contains(err.Error(), "st restack") {
			t.Fatalf("error = %q, want the recovery hint naming the target and st restack", err)
		}
		// The hint is truthful: the amend persisted in a.
		if f.branches["a"] == tips["a"] {
			t.Fatal("a's tip unchanged; the hint would be a lie")
		}
		// The target==cur arm carries no hint by design (nothing was reset);
		// it is structurally unreachable here — when cur IS the target, cur
		// itself is never cascaded, so a failing rebase of cur cannot occur.
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

// TestAbsorbPlanSpawnDiet is a deliberate perf ratchet in the style of
// restack_spawn_test.go: it pins the spawn STRATEGY, not behavior. The stack
// set comes from one bounded CommitRange (no per-tip RevParse, no unbounded
// AncestorSet trunk walk) and the current branch is read exactly once.
func TestAbsorbPlanSpawnDiet(t *testing.T) {
	f, s, env, tips := absorbEnv(t)
	f.staged = true
	f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
	f.blame = map[string]map[int]string{"f.txt": {2: tips["a"]}}
	spy := &tipReadSpyGit{Git: f}
	env.Git = spy

	if _, err := AbsorbPlan(env, s); err != nil {
		t.Fatalf("AbsorbPlan: %v", err)
	}
	if spy.revParseCalls != 0 {
		t.Fatalf("revParseCalls = %d, want 0 (tips resolve inside the one CommitRange)", spy.revParseCalls)
	}
	if spy.currentBranchCalls != 1 {
		t.Fatalf("currentBranchCalls = %d, want exactly the single currentTracked read", spy.currentBranchCalls)
	}
	if spy.ancestorSetCalls != 0 || spy.commitRangeCalls != 1 {
		t.Fatalf("ancestorSet/commitRange = %d/%d, want 0/1 (the bounded range walk)", spy.ancestorSetCalls, spy.commitRangeCalls)
	}
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
