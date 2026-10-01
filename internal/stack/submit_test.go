package stack

import (
	"strings"
	"testing"
)

// submit_test.go — fast fake-git coverage of the submit decision layer moved
// out of cmd (plan 025): push ordering, the corrupt-forest verdict, the
// "nothing to do" reasons, PR hint derivation, and remote-URL conversion.

func TestSubmitPlanCurrentStackOrdersBottomUp(t *testing.T) {
	t.Parallel()
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "b", "c")

	plan, err := SubmitPlan(env, s, false)
	if err != nil {
		t.Fatalf("SubmitPlan: %v", err)
	}
	want := []string{"a", "b", "c"}
	if strings.Join(plan.Branches, ",") != strings.Join(want, ",") {
		t.Fatalf("SubmitPlan.Branches = %v, want %v", plan.Branches, want)
	}
}

func TestSubmitPlanCurrentStackStopsAtCurrent(t *testing.T) {
	t.Parallel()
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	// A descendant off the current branch is NOT part of the path.
	mkBranch(t, env, s, f, "b", "descendant")
	if err := f.Checkout("b"); err != nil {
		t.Fatal(err)
	}

	plan, err := SubmitPlan(env, s, false)
	if err != nil {
		t.Fatalf("SubmitPlan: %v", err)
	}
	want := []string{"a", "b"}
	if strings.Join(plan.Branches, ",") != strings.Join(want, ",") {
		t.Fatalf("SubmitPlan.Branches = %v, want %v", plan.Branches, want)
	}
}

func TestSubmitPlanAtTrunk(t *testing.T) {
	t.Parallel()
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}

	plan, err := SubmitPlan(env, s, false)
	if err != nil {
		t.Fatalf("SubmitPlan: %v", err)
	}
	if len(plan.Branches) != 0 || plan.Reason == "" {
		t.Fatalf("SubmitPlan at trunk = %+v, want empty branches with a reason", plan)
	}
}

func TestSubmitPlanRejectsUntrackedCurrent(t *testing.T) {
	t.Parallel()
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	// An existing-but-untracked current branch is a hard error.
	if err := f.CreateBranch("loose"); err != nil {
		t.Fatal(err)
	}
	if err := f.Checkout("loose"); err != nil {
		t.Fatal(err)
	}

	if _, err := SubmitPlan(env, s, false); err == nil || !strings.Contains(err.Error(), "not tracked") {
		t.Fatalf("SubmitPlan(untracked) error = %v, want not-tracked", err)
	}
}

func TestSubmitPlanAllOrdersWholeForest(t *testing.T) {
	t.Parallel()
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	// A second stack off trunk — the forest order must interleave parents
	// first, matching restack --all's walk.
	mkBranch(t, env, s, f, "main", "x")
	mkBranch(t, env, s, f, "x", "y")

	plan, err := SubmitPlan(env, s, true)
	if err != nil {
		t.Fatalf("SubmitPlan --all: %v", err)
	}
	pos := map[string]int{}
	for i, name := range plan.Branches {
		pos[name] = i
	}
	// Every branch pushes after its recorded parent.
	for name, i := range pos {
		parent := s.Branches[name].Parent
		if parent == s.Trunk {
			continue
		}
		if pi, ok := pos[parent]; !ok || pi >= i {
			t.Fatalf("SubmitPlan --all order %v puts %s before parent %s", plan.Branches, name, parent)
		}
	}
	if len(plan.Branches) != 4 {
		t.Fatalf("SubmitPlan --all = %v, want all 4 tracked branches", plan.Branches)
	}
}

func TestSubmitPlanAllDetectsCorruptForest(t *testing.T) {
	t.Parallel()
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "main", "orphan")
	// Corrupt: orphan's recorded parent no longer exists in state, so the
	// trunk-descendants walk cannot reach it.
	s.Branches["orphan"].Parent = "ghost"

	_, err := SubmitPlan(env, s, true)
	if err == nil || !strings.Contains(err.Error(), "orphan") {
		t.Fatalf("SubmitPlan --all on corrupt forest error = %v, want one naming orphan", err)
	}
}

func TestSubmitPlanAllEmptyForest(t *testing.T) {
	t.Parallel()
	_, s, env := newEnvState()

	plan, err := SubmitPlan(env, s, true)
	if err != nil {
		t.Fatalf("SubmitPlan --all: %v", err)
	}
	if len(plan.Branches) != 0 || plan.Reason == "" {
		t.Fatalf("SubmitPlan --all empty = %+v, want empty branches with a reason", plan)
	}
}

func TestPRHintsForBuildsParentBase(t *testing.T) {
	t.Parallel()
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")

	hints := PRHintsFor(s, []string{"a", "b"}, "https://github.com/o/r", "github.com")
	if len(hints) != 2 {
		t.Fatalf("PRHintsFor = %v, want 2 hints", hints)
	}
	if hints[0].Head != "a" || hints[0].Base != "main" {
		t.Fatalf("hint[0] = %+v, want head=a base=main", hints[0])
	}
	if hints[1].Head != "b" || hints[1].Base != "a" {
		t.Fatalf("hint[1] = %+v, want head=b base=a", hints[1])
	}
	if want := "https://github.com/o/r/compare/main...a"; hints[0].CompareURL != want {
		t.Fatalf("hint[0].CompareURL = %q, want %q", hints[0].CompareURL, want)
	}
	if want := "https://github.com/o/r/compare/a...b"; hints[1].CompareURL != want {
		t.Fatalf("hint[1].CompareURL = %q, want %q", hints[1].CompareURL, want)
	}
}

func TestPRHintsForSkipsUnlistedAndOmitsURLOnEmptyRepo(t *testing.T) {
	t.Parallel()
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")

	hints := PRHintsFor(s, []string{"a", "ghost"}, "", "")
	if len(hints) != 1 || hints[0].Head != "a" {
		t.Fatalf("PRHintsFor = %v, want one hint for a", hints)
	}
	if hints[0].CompareURL != "" {
		t.Fatalf("CompareURL = %q, want empty with no repo URL", hints[0].CompareURL)
	}
}

func TestRemoteToHTTPSConversions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw         string
		wantURL     string
		wantHostHit bool // whether host should classify to a known forge shape
	}{
		{"git@github.com:o/r.git", "https://github.com/o/r", true},
		{"git@gitlab.com:o/r.git", "https://gitlab.com/o/r", true},
		{"https://github.com/o/r.git", "https://github.com/o/r", true},
		{"ssh://git@gitlab.example.com/o/r.git", "https://gitlab.example.com/o/r", true},
		{"https://user:pw@github.com/o/r.git", "https://github.com/o/r", true},
		{"not-a-url", "", false},
	}
	for _, tc := range cases {
		gotURL, host := RemoteToHTTPS(tc.raw)
		if gotURL != tc.wantURL {
			t.Errorf("RemoteToHTTPS(%q) url = %q, want %q", tc.raw, gotURL, tc.wantURL)
		}
		if tc.wantURL == "" {
			continue
		}
		if tc.wantHostHit && forgeKind(host) == forgeUnknown {
			t.Errorf("RemoteToHTTPS(%q) host %q did not classify to a forge", tc.raw, host)
		}
	}
}
