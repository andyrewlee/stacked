package stack

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// blameID builds an identity-coordinate blame entry — the line sits at the
// same number and path in the owning commit's version as at HEAD, which is
// the only provenance absorb accepts. Tests wanting shifted, renamed, or
// incomplete provenance set the fields explicitly instead.
func blameID(commit string, line int, path string) git.BlameLine {
	return git.BlameLine{Commit: commit, OriginalLine: line, FinalLine: line, Path: path}
}

// The five attribution cases from the absorb design spike, driven through
// the fake git with canned hunks + blame. Refusals must never be errors.
func TestAbsorbPlanAttribution(t *testing.T) {
	t.Run("single target absorbs into the owning branch tip", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
		f.blame = map[string]map[int]git.BlameLine{"f.txt": {2: blameID(tips["a"], 2, "f.txt")}}

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
		f.blame = map[string]map[int]git.BlameLine{"f.txt": {3: blameID(tips["a"], 3, "f.txt"), 4: blameID(tips["b"], 4, "f.txt")}}

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
		f.blame = map[string]map[int]git.BlameLine{"f.txt": {1: blameID(tips["main"], 1, "f.txt")}}

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
		f.blame = map[string]map[int]git.BlameLine{"f.txt": {2: blameID(tips["a"], 2, "f.txt")}}
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
		f.blame = map[string]map[int]git.BlameLine{"f.txt": {}}

		res, err := AbsorbPlan(env, s)
		if err != nil {
			t.Fatalf("AbsorbPlan: %v", err)
		}
		if len(res.Refused) != 1 || !strings.Contains(res.Refused[0].Reason, "cannot attribute") {
			t.Fatalf("result = %+v, want the cannot-attribute refusal", res)
		}
	})

	// A descendant that inserts ABOVE the owned line pushes it down: blame
	// reports the HEAD line (4) but the line sat at 2 in the owning commit.
	// The -U0 patch would land at line 4 of the ancestor's tree — with
	// repeated text, on the wrong occurrence — so the hunk is refused.
	t.Run("line shifted down by an insertion is refused", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 4, OldN: 1, NewStart: 4, NewN: 1}}
		f.blame = map[string]map[int]git.BlameLine{"f.txt": {
			4: {Commit: tips["a"], OriginalLine: 2, FinalLine: 4, Path: "f.txt"},
		}}

		res, err := AbsorbPlan(env, s)
		if err != nil {
			t.Fatalf("AbsorbPlan: %v", err)
		}
		if len(res.Refused) != 1 || !strings.Contains(res.Refused[0].Reason, "shifted") {
			t.Fatalf("result = %+v, want a shifted-coordinates refusal", res)
		}
	})

	// A descendant that deletes ABOVE the owned line pulls it up: original 3
	// now sits at HEAD line 2.
	t.Run("line shifted up by a deletion is refused", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
		f.blame = map[string]map[int]git.BlameLine{"f.txt": {
			2: {Commit: tips["a"], OriginalLine: 3, FinalLine: 2, Path: "f.txt"},
		}}

		res, err := AbsorbPlan(env, s)
		if err != nil {
			t.Fatalf("AbsorbPlan: %v", err)
		}
		if len(res.Refused) != 1 || !strings.Contains(res.Refused[0].Reason, "shifted") {
			t.Fatalf("result = %+v, want a shifted-coordinates refusal", res)
		}
	})

	// A descendant renamed the file: blame reports the historical path even
	// at identity line numbers, and the patch header names the CURRENT path
	// which does not exist in the ancestor's tree.
	t.Run("line whose file was renamed is refused", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		f.stagedHunks = []git.Hunk{{File: "g.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
		f.blame = map[string]map[int]git.BlameLine{"g.txt": {
			2: {Commit: tips["a"], OriginalLine: 2, FinalLine: 2, Path: "f.txt"},
		}}

		res, err := AbsorbPlan(env, s)
		if err != nil {
			t.Fatalf("AbsorbPlan: %v", err)
		}
		if len(res.Refused) != 1 ||
			!strings.Contains(res.Refused[0].Reason, `was "f.txt"`) ||
			!strings.Contains(res.Refused[0].Reason, "renamed") {
			t.Fatalf("result = %+v, want a historical-path refusal naming f.txt", res)
		}
	})

	// Provenance without a decodable path is malformed — fail closed, never
	// pretend identity coordinates.
	t.Run("line with empty provenance path is refused", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
		f.blame = map[string]map[int]git.BlameLine{"f.txt": {
			2: {Commit: tips["a"], OriginalLine: 2, FinalLine: 2, Path: ""},
		}}

		res, err := AbsorbPlan(env, s)
		if err != nil {
			t.Fatalf("AbsorbPlan: %v", err)
		}
		if len(res.Refused) != 1 || !strings.Contains(res.Refused[0].Reason, "missing or malformed") {
			t.Fatalf("result = %+v, want a malformed-provenance refusal", res)
		}
	})

	// An entry keyed by one final line but claiming another is corrupt;
	// refuse rather than trust either number.
	t.Run("entry whose FinalLine disagrees with its key is refused", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
		f.blame = map[string]map[int]git.BlameLine{"f.txt": {
			2: {Commit: tips["a"], OriginalLine: 2, FinalLine: 5, Path: "f.txt"},
		}}

		res, err := AbsorbPlan(env, s)
		if err != nil {
			t.Fatalf("AbsorbPlan: %v", err)
		}
		if len(res.Refused) != 1 || !strings.Contains(res.Refused[0].Reason, "missing or malformed") {
			t.Fatalf("result = %+v, want a malformed-provenance refusal", res)
		}
	})

	// The positive twin: a multi-line hunk whose every line carries identity
	// provenance (contiguous by construction) still absorbs.
	t.Run("multi-line hunk with identity provenance absorbs", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 2, NewStart: 2, NewN: 2}}
		f.blame = map[string]map[int]git.BlameLine{"f.txt": {
			2: blameID(tips["a"], 2, "f.txt"),
			3: blameID(tips["a"], 3, "f.txt"),
		}}

		res, err := AbsorbPlan(env, s)
		if err != nil {
			t.Fatalf("AbsorbPlan: %v", err)
		}
		if len(res.Absorbed) != 1 || len(res.Refused) != 0 {
			t.Fatalf("result = %+v, want the identity two-line hunk absorbed", res)
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
		f.blame = map[string]map[int]git.BlameLine{"f.txt": {5: blameID(oldBTip, 5, "f.txt")}}

		res, err := AbsorbPlan(env, s)
		if err != nil {
			t.Fatalf("AbsorbPlan: %v", err)
		}
		if len(res.Refused) != 1 || !strings.Contains(res.Refused[0].Reason, "not a branch tip") {
			t.Fatalf("result = %+v, want a non-tip refusal", res)
		}
	})
}

// TestAbsorbApply drives the apply slice over the fake git: a single-target
// plan amends the owning tip in place, cascades the descendants, drops the
// staged copy from the current worktree, and returns HEAD to where it started.
func TestAbsorbApply(t *testing.T) {
	stage := func(f *fakeGit, tips map[string]string, owner string) {
		f.staged = true
		f.stagedPatch = []byte("fake patch")
		f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
		f.blame = map[string]map[int]git.BlameLine{"f.txt": {2: blameID(tips[owner], 2, "f.txt")}}
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
		if _, err := Undo(env, s, entry, false); err != nil {
			t.Fatalf("Undo: %v", err)
		}
		assertUndoRestored(t, f, s, entry)
	})

	t.Run("undo names the amended commit the ref-restore orphans", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stage(f, tips, "a")
		entry := mustSnapshot(t, s, f, "absorb")

		res, err := Absorb(env, s)
		if err != nil {
			t.Fatalf("Absorb: %v", err)
		}
		// What cmd/absorb.go writes via SetLastUndoAbsorbed: the amended tip
		// carrying the staged edit.
		entry.AbsorbedCommits = map[string]string{"a": res.Absorbed[0].Commit}
		undo, err := Undo(env, s, entry, false)
		if err != nil {
			t.Fatalf("Undo: %v", err)
		}
		joined := strings.Join(undo.Notes, "\n")
		if !strings.Contains(joined, res.Absorbed[0].Commit) || !strings.Contains(joined, "git cherry-pick") {
			t.Fatalf("undo notes = %v, want the amended tip and a cherry-pick pointer", undo.Notes)
		}
		assertUndoRestored(t, f, s, entry)
	})

	t.Run("absorb apply and dry-run explain undo restores refs not the staged copies", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stage(f, tips, "a")

		plan, err := AbsorbPlan(env, s)
		if err != nil {
			t.Fatalf("AbsorbPlan: %v", err)
		}
		if joined := strings.Join(plan.Notes, "\n"); !strings.Contains(joined, "staged working-tree") {
			t.Fatalf("dry-run notes = %v, want the undo/working-tree caveat", plan.Notes)
		}
		res, err := Absorb(env, s)
		if err != nil {
			t.Fatalf("Absorb: %v", err)
		}
		joined := strings.Join(res.Notes, "\n")
		if !strings.Contains(joined, res.Absorbed[0].Commit) || !strings.Contains(joined, "git cherry-pick") {
			t.Fatalf("apply notes = %v, want the amended tip and a cherry-pick pointer", res.Notes)
		}
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
		f.blame["f.txt"][5] = blameID(tips["b"], 5, "f.txt")
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

	// Plan-017's spawn contract: two targets must share ONE staged-diff
	// capture — the per-target DiffCachedPatchFor loop ran the full-index
	// diff once per target.
	t.Run("multi-target absorb captures the staged diff once", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stageTwoTargets(f, tips)

		before := f.callsSnapshot()
		if _, err := Absorb(env, s); err != nil {
			t.Fatalf("Absorb: %v", err)
		}
		if got := f.calls["DiffCachedPatchesFor"] - before["DiffCachedPatchesFor"]; got != 1 {
			t.Fatalf("DiffCachedPatchesFor calls = %d, want 1 (batched) for two targets", got)
		}
	})

	t.Run("one undo entry reverts a multi-target absorb", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stageTwoTargets(f, tips)
		entry := mustSnapshot(t, s, f, "absorb")

		if _, err := Absorb(env, s); err != nil {
			t.Fatalf("Absorb: %v", err)
		}
		if _, err := Undo(env, s, entry, false); err != nil {
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
// trunk-history walk) and the current branch is read exactly once.
func TestAbsorbPlanSpawnDiet(t *testing.T) {
	f, s, env, tips := absorbEnv(t)
	f.staged = true
	f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
	f.blame = map[string]map[int]git.BlameLine{"f.txt": {2: blameID(tips["a"], 2, "f.txt")}}
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
	if spy.commitRangeCalls != 1 {
		t.Fatalf("commitRangeCalls = %d, want 1 (the bounded range walk)", spy.commitRangeCalls)
	}
	if spy.tipsForCalls != 1 {
		t.Fatalf("tipsForCalls = %d, want 1 (one bulk read for the attribution maps)", spy.tipsForCalls)
	}
}

// TestAbsorbPlanBatchesBlamePerFile pins the blame memoization contract:
// `git blame` spawns once per FILE, never per hunk — a staged diff with many
// hunks in few files must not scale subprocesses with hunk count. Same
// deliberate implementation-strategy ratchet as the spawn-diet test above.
func TestAbsorbPlanBatchesBlamePerFile(t *testing.T) {
	f, s, env, tips := absorbEnv(t)
	f.stagedHunks = []git.Hunk{
		{File: "a.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1},
		{File: "a.txt", OldStart: 7, OldN: 1, NewStart: 7, NewN: 1},
		{File: "b.txt", OldStart: 3, OldN: 1, NewStart: 3, NewN: 1},
		{File: "b.txt", OldStart: 9, OldN: 1, NewStart: 9, NewN: 1},
	}
	f.blame = map[string]map[int]git.BlameLine{
		"a.txt": {2: blameID(tips["a"], 2, "a.txt"), 7: blameID(tips["a"], 7, "a.txt")},
		"b.txt": {3: blameID(tips["a"], 3, "b.txt"), 9: blameID(tips["a"], 9, "b.txt")},
	}
	spy := &tipReadSpyGit{Git: f}
	env.Git = spy

	res, err := AbsorbPlan(env, s)
	if err != nil {
		t.Fatalf("AbsorbPlan: %v", err)
	}
	if len(res.Absorbed) != 4 || len(res.Refused) != 0 {
		t.Fatalf("result = %+v, want all four hunks absorbed", res)
	}
	if spy.blameCalls != 2 {
		t.Fatalf("blameCalls = %d, want 2 — one blame per file, not per hunk", spy.blameCalls)
	}
}

// TestAbsorbPreFlightReadsWorktreesOnce pins the hoisted owner snapshot: the
// dirty-owner pre-flight costs ONE `git worktree list` for all targets (and
// no extra CurrentBranch — cur is already resolved), not one read per
// target. b's dirty worktree returns the plan unapplied BEFORE any amend, so
// the spy counts isolate the pre-flight from the cascade's own worktree
// reads.
func TestAbsorbPreFlightReadsWorktreesOnce(t *testing.T) {
	f, s, env, tips := absorbEnv(t)
	f.staged = true
	f.stagedPatch = []byte("fake patch")
	f.stagedHunks = []git.Hunk{
		{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1},
		{File: "f.txt", OldStart: 5, OldN: 1, NewStart: 5, NewN: 1},
	}
	f.blame = map[string]map[int]git.BlameLine{"f.txt": {2: blameID(tips["a"], 2, "f.txt"), 5: blameID(tips["b"], 5, "f.txt")}}
	f.addWorktree("/wt/b", "b")
	f.dirtyWT = map[string]bool{"b": true}
	spy := &tipReadSpyGit{Git: f}
	env.Git = spy

	res, err := Absorb(env, s)
	if err != nil {
		t.Fatalf("Absorb: %v", err)
	}
	if !res.DryRun || !strings.HasPrefix(res.Summary, "not applied: a target's worktree is dirty") {
		t.Fatalf("result = %+v, want the dirty-owner refusal", res)
	}
	if spy.worktreesCalls != 1 {
		t.Fatalf("worktreesCalls = %d, want 1 — the pre-flight snapshots once for all targets", spy.worktreesCalls)
	}
	if spy.currentBranchCalls != 1 {
		t.Fatalf("currentBranchCalls = %d, want 1 — currentTracked only; the pre-flight threads cur", spy.currentBranchCalls)
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
	f.blame = map[string]map[int]git.BlameLine{"f.txt": {2: blameID(tips["a"], 2, "f.txt")}}
	res, err = AbsorbPlan(env, s)
	if err != nil {
		t.Fatalf("AbsorbPlan with a staged index: %v", err)
	}
	if len(res.Absorbed) != 1 {
		t.Fatalf("result = %+v, want the staged hunk absorbed", res)
	}
}

// TestAbsorbPlanRefusesOffPathSiblingTip is the regression test for the
// dropped-hunks bug: a tracked branch OFF the current stack's path can tip at
// the very commit that owns a staged hunk (here a mid-stack commit a grew
// past), but absorb must never land the hunk there — the staged copy in THIS
// worktree is consumed either way, so off-path attribution silently drops the
// user's work into a different stack. It is refused, naming the off-path
// branch.
func TestAbsorbPlanRefusesOffPathSiblingTip(t *testing.T) {
	f, s, env, tips := absorbEnv(t)
	// Advance a past b's recorded base so its old tip is a mid-stack commit no
	// on-path branch tips, then point a separately tracked side stack at it.
	oldATip := tips["a"]
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	f.commit("a grows past b's base")
	if err := f.Checkout("c"); err != nil {
		t.Fatal(err)
	}
	if err := f.CreateBranchAt("side", oldATip); err != nil {
		t.Fatal(err)
	}
	s.Track("side", "main", tips["main"])
	f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
	f.blame = map[string]map[int]git.BlameLine{"f.txt": {2: blameID(oldATip, 2, "f.txt")}}

	res, err := AbsorbPlan(env, s)
	if err != nil {
		t.Fatalf("AbsorbPlan: %v", err)
	}
	if len(res.Absorbed) != 0 || len(res.Refused) != 1 {
		t.Fatalf("result = %+v, want one refusal and nothing absorbed", res)
	}
	if !strings.Contains(res.Refused[0].Reason, "side") ||
		!strings.Contains(res.Refused[0].Reason, "not on the current stack's path") {
		t.Fatalf("reason = %q, want it to name the off-path tip owner", res.Refused[0].Reason)
	}
}

// TestAbsorbPlanOffPathSharedTipStaysOnPath covers the other half of the same
// bug: when an off-path tracked branch shares the owning commit's SHA with an
// on-path branch, attribution must choose the on-path one — every time, not
// whichever name map iteration happened to write last.
func TestAbsorbPlanOffPathSharedTipStaysOnPath(t *testing.T) {
	f, s, env, tips := absorbEnv(t)
	// side is a second tracked stack rooted at the trunk, pointing at b's tip.
	if err := f.CreateBranchAt("side", tips["b"]); err != nil {
		t.Fatal(err)
	}
	s.Track("side", "main", tips["main"])
	f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
	f.blame = map[string]map[int]git.BlameLine{"f.txt": {2: blameID(tips["b"], 2, "f.txt")}}

	for i := 0; i < 20; i++ {
		res, err := AbsorbPlan(env, s)
		if err != nil {
			t.Fatalf("run %d: AbsorbPlan: %v", i, err)
		}
		if len(res.Absorbed) != 1 || res.Absorbed[0].Branch != "b" || len(res.Refused) != 0 {
			t.Fatalf("run %d: result = %+v, want the hunk on on-path b, never off-path side", i, res)
		}
	}
}

// TestAbsorbPlanSharedTipDeterministic pins the shared-tip tie-break among
// ON-PATH branches: two of them can tip at the same commit (a no-commit child
// is exactly that — `st create` without -m leaves the new branch on its
// parent's tip). The LOWEST sharer wins: its restack covers every deeper
// sharer, so amending it keeps the whole run consistent. The choice must be
// identical on every call — map iteration order never decides.
func TestAbsorbPlanSharedTipDeterministic(t *testing.T) {
	f, s, env, tips := absorbEnv(t)
	// mark is a no-commit child of b sharing b's tip; HEAD lands on it, so
	// both b and mark are on the current path.
	if err := f.Checkout("b"); err != nil {
		t.Fatal(err)
	}
	if err := f.CreateBranch("mark"); err != nil {
		t.Fatal(err)
	}
	s.Track("mark", "b", tips["b"])
	f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
	f.blame = map[string]map[int]git.BlameLine{"f.txt": {2: blameID(tips["b"], 2, "f.txt")}}

	for i := 0; i < 20; i++ {
		res, err := AbsorbPlan(env, s)
		if err != nil {
			t.Fatalf("run %d: AbsorbPlan: %v", i, err)
		}
		if len(res.Absorbed) != 1 || res.Absorbed[0].Branch != "b" || len(res.Refused) != 0 {
			t.Fatalf("run %d: result = %+v, want the hunk on b (the lowest sharer) every run", i, res)
		}
	}
}

// TestAbsorbOwnerWorktreeSyncFails pins the post-amend cleanup arm: once a
// foreign target's temp-index amend lands, syncing that target's OWNING
// worktree can fail (ResetHardIn). The amend already persists — the error must
// name the worktree so the divergence is visible.
func TestAbsorbOwnerWorktreeSyncFails(t *testing.T) {
	f, s, env, tips := absorbEnv(t)
	f.staged = true
	f.stagedPatch = []byte("fake patch")
	f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
	f.blame = map[string]map[int]git.BlameLine{"f.txt": {2: blameID(tips["a"], 2, "f.txt")}}
	f.addWorktree("/wt/a", "a") // a checked out in a clean linked worktree

	resetErr := errors.New("reset exploded")
	f.failErr["ResetHardIn"] = resetErr

	_, err := Absorb(env, s)
	if !errors.Is(err, resetErr) {
		t.Fatalf("Absorb = %v, want the reset sentinel surfaced", err)
	}
	if !strings.Contains(err.Error(), "syncing worktree /wt/a") {
		t.Fatalf("Absorb = %v, want the diverged worktree named", err)
	}
	// The temp-index amend already committed: a's tip moved even though the
	// worktree sync failed — that is the documented no-rollback contract.
	if f.branches["a"] == tips["a"] {
		t.Fatal("a's tip unchanged — the failing arm runs AFTER the amend lands")
	}
	if f.staged {
		// The caller-worktree drop never ran (the owner reset failed first) —
		// the staged copies are still live AND committed at a's tip.
		t.Log("staged copies remain — expected: cleanup failed before the drop")
	}
}

// TestAbsorbCallerResetFails covers the caller-tree arm of the same cleanup:
// the foreign target is unowned (no worktree), so the first and only
// ResetHardIn is the caller's drop-the-copies reset. Its failure leaves the
// edits staged locally AND committed at the target's tip.
func TestAbsorbCallerResetFails(t *testing.T) {
	f, s, env, tips := absorbEnv(t)
	f.staged = true
	f.stagedPatch = []byte("fake patch")
	f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
	f.blame = map[string]map[int]git.BlameLine{"f.txt": {2: blameID(tips["a"], 2, "f.txt")}}

	resetErr := errors.New("reset exploded")
	f.failErr["ResetHardIn"] = resetErr

	_, err := Absorb(env, s)
	if !errors.Is(err, resetErr) {
		t.Fatalf("Absorb = %v, want the reset sentinel surfaced", err)
	}
	if !strings.Contains(err.Error(), "dropping the absorbed staged copies") {
		t.Fatalf("Absorb = %v, want the drop-copies step named", err)
	}
	if f.branches["a"] == tips["a"] {
		t.Fatal("a's tip unchanged — the failing arm runs AFTER the amend lands")
	}
	if !f.staged {
		t.Fatal("the staged copies should still be live — the failed reset was the drop")
	}
	if len(f.resetHardDirs) != 0 {
		t.Fatalf("resetHardDirs = %q, want none recorded (the call failed)", f.resetHardDirs)
	}
}

// TestAbsorbPreAmendProbeFailures pins the pre-amend probe arms: each one must
// surface its error wrapped and leave every tip untouched — nothing has been
// committed yet, so failure is clean.
func TestAbsorbPreAmendProbeFailures(t *testing.T) {
	stage := func(f *fakeGit, tips map[string]string) {
		f.staged = true
		f.stagedPatch = []byte("fake patch")
		f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
		f.blame = map[string]map[int]git.BlameLine{"f.txt": {2: blameID(tips["a"], 2, "f.txt")}}
	}

	t.Run("Worktrees probe fails", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stage(f, tips)
		boom := errors.New("worktree list exploded")
		f.failErr["Worktrees"] = boom

		_, err := Absorb(env, s)
		if !errors.Is(err, boom) {
			t.Fatalf("Absorb = %v, want %v", err, boom)
		}
		if f.branches["a"] != tips["a"] {
			t.Fatal("a's tip moved — a pre-amend probe failure must not mutate")
		}
		if !f.staged {
			t.Fatal("staged copies dropped before the amend even ran")
		}
	})

	t.Run("IsCleanIn probe fails", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stage(f, tips)
		f.addWorktree("/wt/a", "a")
		boom := errors.New("clean probe exploded")
		f.failErr["IsCleanIn"] = boom

		_, err := Absorb(env, s)
		if !errors.Is(err, boom) {
			t.Fatalf("Absorb = %v, want %v", err, boom)
		}
		if !strings.Contains(err.Error(), "checking worktree /wt/a") {
			t.Fatalf("Absorb = %v, want the probed worktree named", err)
		}
		if f.branches["a"] != tips["a"] {
			t.Fatal("a's tip moved — a pre-amend probe failure must not mutate")
		}
	})

	t.Run("DiffCachedPatchesFor fails", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stage(f, tips)
		boom := errors.New("diff exploded")
		f.failErr["DiffCachedPatchesFor"] = boom

		_, err := Absorb(env, s)
		if !errors.Is(err, boom) {
			t.Fatalf("Absorb = %v, want %v", err, boom)
		}
		if !strings.Contains(err.Error(), "assembling staged patches") {
			t.Fatalf("Absorb = %v, want the patch-assembly step named", err)
		}
		if f.branches["a"] != tips["a"] {
			t.Fatal("a's tip moved — a pre-amend probe failure must not mutate")
		}
	})
}

// TestAbsorbPostAmendProbeFailures pins the three post-mutation arms after the
// cascade: re-reading amended tips, the epilogue save, and the HEAD restore.
// In each the amends and the cascade have already committed — the error
// surfaces but cannot roll anything back.
func TestAbsorbPostAmendProbeFailures(t *testing.T) {
	stage := func(f *fakeGit, tips map[string]string) {
		f.staged = true
		f.stagedPatch = []byte("fake patch")
		f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
		f.blame = map[string]map[int]git.BlameLine{"f.txt": {2: blameID(tips["a"], 2, "f.txt")}}
	}

	t.Run("TipsFor re-read fails", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stage(f, tips)
		boom := errors.New("tips exploded")
		// TipsFor is read once before the post-cascade re-read: the plan-time
		// tip snapshot at absorbPlan. (The cascade snapshots via Tips, a
		// different method.) Fail the second TipsFor call.
		f.failErr["TipsFor"] = boom
		f.failAfter["TipsFor"] = 1

		_, err := Absorb(env, s)
		if !errors.Is(err, boom) {
			t.Fatalf("Absorb = %v, want %v", err, boom)
		}
		if !strings.Contains(err.Error(), "re-reading amended tips") {
			t.Fatalf("Absorb = %v, want the tip re-read named", err)
		}
		if f.branches["a"] == tips["a"] {
			t.Fatal("a's tip unchanged — the failure is post-mutation")
		}
		// The cascade ran: b re-pointed at a's amended tip.
		b, _ := s.Get("b")
		if b.ParentSHA != f.branches["a"] {
			t.Fatalf("b.ParentSHA = %q, want a's live tip %q (cascade committed)", b.ParentSHA, f.branches["a"])
		}
	})

	t.Run("epilogue save fails", func(t *testing.T) {
		f, s, _, tips := absorbEnv(t)
		stage(f, tips)
		boom := errors.New("save exploded")
		// The cascade checkpoints once per rebased branch (b, c); absorb's own
		// epilogue save is the third — fail exactly that one.
		env2, saves := envWithSaveErr(f, boom, 2)

		_, err := Absorb(env2, s)
		if !errors.Is(err, boom) {
			t.Fatalf("Absorb = %v, want the save sentinel surfaced", err)
		}
		if *saves != 3 {
			t.Fatalf("saves = %d, want 3 (two cascade checkpoints + epilogue)", *saves)
		}
		if f.branches["a"] == tips["a"] {
			t.Fatal("a's tip unchanged — the failure is post-mutation")
		}
	})

	t.Run("HEAD restore fails", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stage(f, tips)
		// Park HEAD mid-stack so the cascade's last rebase leaves HEAD on c and
		// restoreHEAD must actually check cur back out.
		if err := f.Checkout("b"); err != nil {
			t.Fatal(err)
		}
		boom := errors.New("restore exploded")
		f.checkoutErr["b"] = boom

		_, err := Absorb(env, s)
		if !errors.Is(err, boom) {
			t.Fatalf("Absorb = %v, want the restore sentinel surfaced", err)
		}
		if !strings.Contains(err.Error(), `restore branch "b"`) {
			t.Fatalf("Absorb = %v, want the restore target named", err)
		}
		if f.branches["a"] == tips["a"] {
			t.Fatal("a's tip unchanged — the failure is post-mutation")
		}
	})
}

// failNthAmendGit wraps a Git port so the nth LandAmendedTip call returns
// err — a test-local seam for failing a mid-sequence absorb ref move without
// touching the shared fake's knobs. (A failure on the BUILD half aborts
// before any land; only a land failure leaves earlier amends persisted.)
type failNthAmendGit struct {
	Git
	n     int
	err   error
	calls int
}

func (w *failNthAmendGit) LandAmendedTip(branch, oldTip, newTip string) error {
	w.calls++
	if w.calls == w.n {
		return w.err
	}
	return w.Git.LandAmendedTip(branch, oldTip, newTip)
}

// ckpt records every AbsorbCheckpoint argument in call order (the maps are
// safe to retain — the engine clones before calling) and can fail the call
// numbered failAt (1-based; 0 never fails).
type ckpt struct {
	err    error
	failAt int
	calls  int
	maps   []map[string]string
}

func (c *ckpt) hook() func(map[string]string) error {
	return func(m map[string]string) error {
		c.calls++
		if c.calls == c.failAt {
			return c.err
		}
		c.maps = append(c.maps, m)
		return nil
	}
}

// TestAbsorbRecoveryCheckpoints pins the per-amend and post-cascade
// persistence seams: every staged edit that lands in a commit must be named
// in the durable recovery map before the next destructive step runs, and the
// map handed over is a snapshot later amends cannot rewrite.
func TestAbsorbRecoveryCheckpoints(t *testing.T) {
	// stageTwo stages one hunk owned by a and one owned by b — the two-target
	// fixture whose second target the cascade rewrites.
	stageTwo := func(f *fakeGit, tips map[string]string) {
		f.staged = true
		f.stagedPatch = []byte("fake patch")
		f.stagedHunks = []git.Hunk{
			{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1},
			{File: "f.txt", OldStart: 5, OldN: 1, NewStart: 5, NewN: 1},
		}
		f.blame = map[string]map[int]git.BlameLine{"f.txt": {
			2: blameID(tips["a"], 2, "f.txt"),
			5: blameID(tips["b"], 5, "f.txt"),
		}}
	}
	// stageOne stages a single hunk owned by a.
	stageOne := func(f *fakeGit, tips map[string]string) {
		f.staged = true
		f.stagedPatch = []byte("fake patch")
		f.stagedHunks = []git.Hunk{{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}}
		f.blame = map[string]map[int]git.BlameLine{"f.txt": {2: blameID(tips["a"], 2, "f.txt")}}
	}

	t.Run("two-target success checkpoints cumulative then post-cascade tips", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stageTwo(f, tips)
		c := &ckpt{}
		env.AbsorbCheckpoint = c.hook()

		// The amend-time snapshots see pre-reset state; capture it inside the
		// callback to pin the boundary (checkpoints precede every reset and
		// the cascade).
		var resetsAtCall []int
		var parentSHAAtCall []string
		inner := env.AbsorbCheckpoint
		env.AbsorbCheckpoint = func(m map[string]string) error {
			resetsAtCall = append(resetsAtCall, len(f.resetHardDirs))
			b, _ := s.Get("b")
			parentSHAAtCall = append(parentSHAAtCall, b.ParentSHA)
			return inner(m)
		}

		if _, err := Absorb(env, s); err != nil {
			t.Fatalf("Absorb: %v", err)
		}
		if len(c.maps) != 3 {
			t.Fatalf("checkpoints = %d, want 3 ({a}, {a,b}, final tips)", len(c.maps))
		}
		amendA, amendB := c.maps[0]["a"], c.maps[1]["b"]
		if len(c.maps[0]) != 1 || amendA == "" {
			t.Fatalf("first checkpoint = %v, want {a: amended tip}", c.maps[0])
		}
		if len(c.maps[1]) != 2 || c.maps[1]["a"] != amendA || amendB == "" {
			t.Fatalf("second checkpoint = %v, want cumulative {a, b}", c.maps[1])
		}
		// Retained maps are snapshots: the first call's map must NOT show b.
		if _, ok := c.maps[0]["b"]; ok {
			t.Fatalf("first checkpoint mutated retroactively: %v", c.maps[0])
		}
		// The cascade rewrote b's amend-time commit; the final checkpoint
		// carries live tips, not the stale amend IDs.
		final := c.maps[2]
		if final["a"] != f.branches["a"] || final["b"] != f.branches["b"] {
			t.Fatalf("final checkpoint = %v, want live tips a=%s b=%s", final, f.branches["a"], f.branches["b"])
		}
		if final["b"] == amendB {
			t.Fatal("final checkpoint kept b's amend-time commit; the cascade rewrote it")
		}
		if final["a"] != amendA {
			t.Fatal("a's commit must not change — the lowest target is not re-cascaded")
		}
		// Amend-time checkpoints ran before any reset/cascade bookkeeping.
		for i := 0; i < 2; i++ {
			if resetsAtCall[i] != 0 {
				t.Fatalf("checkpoint %d observed %d resets, want 0 (pre-reset boundary)", i+1, resetsAtCall[i])
			}
			if parentSHAAtCall[i] != tips["a"] {
				t.Fatalf("checkpoint %d observed b.ParentSHA %q, want pre-cascade %q", i+1, parentSHAAtCall[i], tips["a"])
			}
		}
	})

	t.Run("first checkpoint failure stops before further mutation", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stageTwo(f, tips)
		boom := errors.New("journal exploded")
		c := &ckpt{err: boom, failAt: 1}
		env.AbsorbCheckpoint = c.hook()

		_, err := Absorb(env, s)
		if !errors.Is(err, boom) {
			t.Fatalf("Absorb = %v, want errors.Is(%v)", err, boom)
		}
		// The error names the amend that actually landed so the SHA is
		// recoverable even though the journal write was not.
		if !strings.Contains(err.Error(), f.branches["a"]) || !strings.Contains(err.Error(), "cherry-pick") {
			t.Fatalf("error = %q, want the landed commit and recovery pointer", err)
		}
		if f.branches["b"] != tips["b"] {
			t.Fatal("b's tip moved — the second amend must not run after a checkpoint failure")
		}
		if len(f.resetHardDirs) != 0 {
			t.Fatalf("resetHardDirs = %v, want none (refused before resets)", f.resetHardDirs)
		}
		if len(f.rebaseLog) != 0 {
			t.Fatalf("rebaseLog = %v, want empty (refused before the cascade)", f.rebaseLog)
		}
	})

	t.Run("second checkpoint failure names both landed commits", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stageTwo(f, tips)
		boom := errors.New("journal exploded")
		c := &ckpt{err: boom, failAt: 2}
		env.AbsorbCheckpoint = c.hook()

		_, err := Absorb(env, s)
		if !errors.Is(err, boom) {
			t.Fatalf("Absorb = %v, want errors.Is(%v)", err, boom)
		}
		for _, target := range []string{"a", "b"} {
			if !strings.Contains(err.Error(), f.branches[target]) {
				t.Fatalf("error = %q, want %s's landed commit %s", err, target, f.branches[target])
			}
		}
		if len(c.maps) != 1 {
			t.Fatalf("checkpoints = %d, want 1 (the first succeeded)", len(c.maps))
		}
		if len(f.resetHardDirs) != 0 || len(f.rebaseLog) != 0 {
			t.Fatal("a checkpoint failure must stop before resets and the cascade")
		}
	})

	t.Run("second amend failure retains the first target's checkpoint", func(t *testing.T) {
		f, s, env, tips := absorbEnv(t)
		stageTwo(f, tips)
		boom := errors.New("patch exploded")
		env.Git = &failNthAmendGit{Git: f, n: 2, err: boom}
		c := &ckpt{}
		env.AbsorbCheckpoint = c.hook()

		_, err := Absorb(env, s)
		if !errors.Is(err, boom) {
			t.Fatalf("Absorb = %v, want errors.Is(%v)", err, boom)
		}
		if len(c.maps) != 1 {
			t.Fatalf("checkpoints = %d, want 1 (only a's amend landed)", len(c.maps))
		}
		if c.maps[0]["a"] != f.branches["a"] || len(c.maps[0]) != 1 {
			t.Fatalf("checkpoint = %v, want {a: %s}", c.maps[0], f.branches["a"])
		}
		if f.branches["b"] != tips["b"] {
			t.Fatal("b's tip moved — its amend failed")
		}
	})

	// Every post-amend failure arm must leave a nonempty last checkpoint
	// naming the committed edits — the whole point of the seam.
	postAmendArms := []struct {
		name  string
		arm   func(f *fakeGit, env *Env)
		wantN int // checkpoints the arm reaches (2 = amend + final refresh)
	}{
		{"owner worktree reset fails", func(f *fakeGit, env *Env) {
			f.addWorktree("/wt/a", "a")
			f.failErr["ResetHardIn"] = errors.New("reset exploded")
		}, 1},
		{"caller reset fails", func(f *fakeGit, env *Env) {
			f.failErr["ResetHardIn"] = errors.New("reset exploded")
		}, 1},
		{"cascade conflict", func(f *fakeGit, env *Env) {
			f.conflictOn("b")
		}, 1},
		{"cascade hard error", func(f *fakeGit, env *Env) {
			f.rebaseErr["b"] = errors.New("rebase exploded")
		}, 1},
		{"final TipsFor fails", func(f *fakeGit, env *Env) {
			f.failErr["TipsFor"] = errors.New("tips exploded")
			f.failAfter["TipsFor"] = 1
		}, 1},
		{"epilogue save fails", func(f *fakeGit, env *Env) {
			saves := 0
			env.Save = func() error {
				saves++
				if saves > 2 { // two cascade checkpoints + epilogue
					return errors.New("save exploded")
				}
				return nil
			}
		}, 2},
		{"HEAD restore fails", func(f *fakeGit, env *Env) {
			if err := f.Checkout("b"); err != nil {
				t.Fatal(err)
			}
			f.checkoutErr["b"] = errors.New("restore exploded")
		}, 2},
	}
	for _, tc := range postAmendArms {
		t.Run("post-amend arm: "+tc.name, func(t *testing.T) {
			f, s, env, tips := absorbEnv(t)
			stageOne(f, tips)
			c := &ckpt{}
			env.AbsorbCheckpoint = c.hook()
			tc.arm(f, &env)

			if _, err := Absorb(env, s); err == nil {
				t.Fatal("Absorb succeeded; the armed failure did not fire")
			}
			if len(c.maps) != tc.wantN {
				t.Fatalf("checkpoints = %d, want %d", len(c.maps), tc.wantN)
			}
			last := c.maps[len(c.maps)-1]
			if last["a"] == "" || last["a"] != f.branches["a"] {
				t.Fatalf("last checkpoint = %v, want a's live amended tip %s", last, f.branches["a"])
			}
			if f.branches["a"] == tips["a"] {
				t.Fatal("a's tip unchanged — the arm must run after the amend lands")
			}
		})
	}

	// Nothing before the first landed amend may invoke the hook.
	t.Run("pre-amend arms never checkpoint", func(t *testing.T) {
		t.Run("planning", func(t *testing.T) {
			f, s, env, tips := absorbEnv(t)
			stageOne(f, tips)
			c := &ckpt{}
			env.AbsorbCheckpoint = c.hook()
			if _, err := AbsorbPlan(env, s); err != nil {
				t.Fatalf("AbsorbPlan: %v", err)
			}
			if c.calls != 0 {
				t.Fatalf("checkpoint calls = %d during planning, want 0", c.calls)
			}
		})
		t.Run("refused plan", func(t *testing.T) {
			f, s, env, tips := absorbEnv(t)
			stageOne(f, tips)
			f.stagedHunks = append(f.stagedHunks, git.Hunk{File: "f.txt", OldStart: 9, OldN: 0, NewStart: 10, NewN: 1})
			c := &ckpt{}
			env.AbsorbCheckpoint = c.hook()
			res, err := Absorb(env, s)
			if err != nil {
				t.Fatalf("Absorb: %v", err)
			}
			if !res.DryRun || len(res.Refused) == 0 {
				t.Fatalf("result = %+v, want an unapplied refused plan", res)
			}
			if c.calls != 0 {
				t.Fatalf("checkpoint calls = %d on a refused plan, want 0", c.calls)
			}
		})
		t.Run("nothing staged", func(t *testing.T) {
			_, s, env, _ := absorbEnv(t)
			c := &ckpt{}
			env.AbsorbCheckpoint = c.hook()
			if _, err := Absorb(env, s); err != nil {
				t.Fatalf("Absorb: %v", err)
			}
			if c.calls != 0 {
				t.Fatalf("checkpoint calls = %d with nothing staged, want 0", c.calls)
			}
		})
		t.Run("pre-amend probe failure", func(t *testing.T) {
			f, s, env, tips := absorbEnv(t)
			stageOne(f, tips)
			f.failErr["Worktrees"] = errors.New("worktree list exploded")
			f.addWorktree("/wt/a", "a") // force the foreign-owner Worktrees probe
			c := &ckpt{}
			env.AbsorbCheckpoint = c.hook()
			if _, err := Absorb(env, s); err == nil {
				t.Fatal("Absorb succeeded despite the armed probe failure")
			}
			if c.calls != 0 {
				t.Fatalf("checkpoint calls = %d before any amend, want 0", c.calls)
			}
		})
		t.Run("first amend failure", func(t *testing.T) {
			f, s, env, tips := absorbEnv(t)
			stageOne(f, tips)
			boom := errors.New("patch exploded")
			env.Git = &failNthAmendGit{Git: f, n: 1, err: boom}
			c := &ckpt{}
			env.AbsorbCheckpoint = c.hook()
			if _, err := Absorb(env, s); !errors.Is(err, boom) {
				t.Fatalf("Absorb = %v, want errors.Is(%v)", err, boom)
			}
			if c.calls != 0 {
				t.Fatalf("checkpoint calls = %d after a failed amend, want 0", c.calls)
			}
		})
	})
}

// absorbBuildBarrier proves absorb's per-target amended-tip builds overlap:
// each BuildAmendedTip call parks until every build is in flight at once —
// a serial build loop leaves each call parked for the full timeout, so a
// regression fails loudly instead of hanging the suite.
type absorbBuildBarrier struct {
	Git
	need     int64
	arrived  atomic.Int64
	timedOut atomic.Bool // a call gave up waiting — the builds ran serially
	release  chan struct{}
	lands    atomic.Int64
}

func (w *absorbBuildBarrier) BuildAmendedTip(branch string, patch []byte) (string, string, error) {
	if w.arrived.Add(1) == w.need {
		close(w.release)
	}
	select {
	case <-w.release:
	case <-time.After(2 * time.Second):
		w.timedOut.Store(true)
	}
	return w.Git.BuildAmendedTip(branch, patch)
}

func (w *absorbBuildBarrier) LandAmendedTip(branch, oldTip, newTip string) error {
	w.lands.Add(1)
	return w.Git.LandAmendedTip(branch, oldTip, newTip)
}

// failNthBuildGit wraps a Git port so the nth BuildAmendedTip call returns
// err — failing an absorb amend's side-effect-free half, which must abort
// the op before ANY ref moves (unlike failNthAmendGit's land failure, which
// leaves earlier amends persisted).
type failNthBuildGit struct {
	Git
	n     int
	err   error
	calls int
}

func (w *failNthBuildGit) BuildAmendedTip(branch string, patch []byte) (string, string, error) {
	w.calls++
	if w.calls == w.n {
		return "", "", w.err
	}
	return w.Git.BuildAmendedTip(branch, patch)
}

func TestAbsorbFansOutAmendBuilds(t *testing.T) {
	f, s, env, tips := absorbEnv(t)
	f.staged = true
	f.stagedPatch = []byte("fake patch")
	f.stagedHunks = []git.Hunk{
		{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1},
		{File: "f.txt", OldStart: 5, OldN: 1, NewStart: 5, NewN: 1},
	}
	f.blame = map[string]map[int]git.BlameLine{"f.txt": {
		2: blameID(tips["a"], 2, "f.txt"),
		5: blameID(tips["b"], 5, "f.txt"),
	}}
	barrier := &absorbBuildBarrier{Git: f, need: 2, release: make(chan struct{})}
	env.Git = barrier
	c := &ckpt{}
	env.AbsorbCheckpoint = c.hook()

	res, err := Absorb(env, s)
	if err != nil {
		t.Fatalf("Absorb: %v", err)
	}
	if barrier.timedOut.Load() {
		t.Fatalf("only %d of 2 builds were ever in flight — the amend builds regressed to serial", barrier.arrived.Load())
	}
	if got := barrier.lands.Load(); got != 2 {
		t.Fatalf("lands = %d, want 2 (one per target, in landed order)", got)
	}
	// Checkpoint k names only landed amends: map[0] has a alone, map[1] adds b.
	if len(c.maps) < 2 {
		t.Fatalf("checkpoints = %d, want at least the 2 per-target writes", len(c.maps))
	}
	if len(c.maps[0]) != 1 || c.maps[0]["a"] == "" {
		t.Fatalf("checkpoint[0] = %v, want only a's landed amend", c.maps[0])
	}
	if len(c.maps[1]) != 2 || c.maps[1]["b"] == "" {
		t.Fatalf("checkpoint[1] = %v, want a+b landed", c.maps[1])
	}
	if res.DryRun {
		t.Fatal("applied result still marked DryRun")
	}
}

// A failed BUILD aborts the absorb before any ref moves — a stronger
// nothing-mutated boundary than the old serial interleave (where a later
// patch failure left earlier targets amended). The staged edit stays staged.
func TestAbsorbBuildFailureLandsNothing(t *testing.T) {
	f, s, env, tips := absorbEnv(t)
	f.staged = true
	f.stagedPatch = []byte("fake patch")
	f.stagedHunks = []git.Hunk{
		{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1},
		{File: "f.txt", OldStart: 5, OldN: 1, NewStart: 5, NewN: 1},
	}
	f.blame = map[string]map[int]git.BlameLine{"f.txt": {
		2: blameID(tips["a"], 2, "f.txt"),
		5: blameID(tips["b"], 5, "f.txt"),
	}}
	boom := errors.New("patch exploded")
	env.Git = &failNthBuildGit{Git: f, n: 2, err: boom}
	c := &ckpt{}
	env.AbsorbCheckpoint = c.hook()

	_, err := Absorb(env, s)
	if !errors.Is(err, boom) {
		t.Fatalf("Absorb = %v, want errors.Is(%v)", err, boom)
	}
	for _, name := range []string{"a", "b"} {
		if f.branches[name] != tips[name] {
			t.Fatalf("%s's tip moved — a failed build must land nothing", name)
		}
	}
	if !f.staged {
		t.Fatal("a failed build must leave the staged edit staged")
	}
	if c.calls != 0 {
		t.Fatalf("checkpoints = %d, want 0 (nothing landed)", c.calls)
	}
}
