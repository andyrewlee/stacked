package stack

import (
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
