package stack

import (
	"reflect"
	"testing"
)

// newTestState builds an in-memory multi-level stack (no git involved):
//
//	main (trunk)
//	└── a
//	    ├── b
//	    │   └── d
//	    └── c
func newTestState() *State {
	s := &State{Trunk: "main", Branches: map[string]*Branch{}}
	s.Track("a", "main", "sha-main")
	s.Track("b", "a", "sha-a")
	s.Track("c", "a", "sha-a")
	s.Track("d", "b", "sha-b")
	return s
}

func branchNames(branches []*Branch) []string {
	names := make([]string, 0, len(branches))
	for _, b := range branches {
		names = append(names, b.Name)
	}
	return names
}

func TestChildren(t *testing.T) {
	s := newTestState()

	if got := branchNames(s.Children("main")); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("Children(main) = %v, want [a]", got)
	}
	// b and c must come back sorted by name.
	if got := branchNames(s.Children("a")); !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Errorf("Children(a) = %v, want [b c]", got)
	}
	if got := branchNames(s.Children("b")); !reflect.DeepEqual(got, []string{"d"}) {
		t.Errorf("Children(b) = %v, want [d]", got)
	}
	if got := s.Children("d"); len(got) != 0 {
		t.Errorf("Children(d) = %v, want empty", branchNames(got))
	}
}

func TestDescendantsTopological(t *testing.T) {
	s := newTestState()

	got := s.Descendants("a")
	want := []string{"b", "d", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Descendants(a) = %v, want %v", got, want)
	}

	// Every parent must appear before each of its children.
	pos := make(map[string]int, len(got))
	for i, name := range got {
		pos[name] = i
	}
	for _, name := range got {
		b, _ := s.Get(name)
		if pIdx, ok := pos[b.Parent]; ok && pIdx > pos[name] {
			t.Errorf("topological violation: parent %q appears after child %q", b.Parent, name)
		}
	}

	if got := s.Descendants("main"); !reflect.DeepEqual(got, []string{"a", "b", "d", "c"}) {
		t.Errorf("Descendants(main) = %v, want [a b d c]", got)
	}
	if got := s.Descendants("d"); len(got) != 0 {
		t.Errorf("Descendants(d) = %v, want empty", got)
	}
}

func TestDescendantsDoesNotReturnRootInCycle(t *testing.T) {
	s := &State{Trunk: "main", Branches: map[string]*Branch{}}
	s.Track("a", "b", "sha-b")
	s.Track("b", "a", "sha-a")

	if got := s.Descendants("a"); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("Descendants(a) in cycle = %v, want [b]", got)
	}
}

func TestAncestors(t *testing.T) {
	s := newTestState()

	if got := s.Ancestors("d"); !reflect.DeepEqual(got, []string{"b", "a", "main"}) {
		t.Errorf("Ancestors(d) = %v, want [b a main]", got)
	}
	if got := s.Ancestors("a"); !reflect.DeepEqual(got, []string{"main"}) {
		t.Errorf("Ancestors(a) = %v, want [main]", got)
	}
	if got := s.Ancestors("unknown"); len(got) != 0 {
		t.Errorf("Ancestors(unknown) = %v, want empty", got)
	}
}

func TestBottomOf(t *testing.T) {
	s := newTestState()

	for _, name := range []string{"a", "b", "c", "d"} {
		if got := s.BottomOf(name); got != "a" {
			t.Errorf("BottomOf(%q) = %q, want a", name, got)
		}
	}
	// Trunk and unknown branches are returned unchanged.
	if got := s.BottomOf("main"); got != "main" {
		t.Errorf("BottomOf(main) = %q, want main", got)
	}
	if got := s.BottomOf("unknown"); got != "unknown" {
		t.Errorf("BottomOf(unknown) = %q, want unknown", got)
	}
}

func TestTrackUpsertAndIsTracked(t *testing.T) {
	s := newTestState()

	if !s.IsTracked("b") {
		t.Fatal("IsTracked(b) = false, want true")
	}
	if s.IsTracked("main") {
		t.Fatal("IsTracked(main) = true, want false (trunk is not tracked)")
	}
	if s.IsTracked("nope") {
		t.Fatal("IsTracked(nope) = true, want false")
	}

	// Track on an existing name must upsert (re-parent), not duplicate.
	before := len(s.Branches)
	s.Track("b", "c", "sha-c")
	if len(s.Branches) != before {
		t.Errorf("Track upsert changed branch count to %d, want %d", len(s.Branches), before)
	}
	b, ok := s.Get("b")
	if !ok {
		t.Fatal("Get(b) missing after upsert")
	}
	if b.Parent != "c" || b.ParentSHA != "sha-c" {
		t.Errorf("after upsert b = {Parent:%q ParentSHA:%q}, want {c sha-c}", b.Parent, b.ParentSHA)
	}

	// Re-parenting b under c moves d's subtree along with it.
	if got := s.Descendants("c"); !reflect.DeepEqual(got, []string{"b", "d"}) {
		t.Errorf("Descendants(c) after re-parent = %v, want [b d]", got)
	}
}

func TestUntrack(t *testing.T) {
	s := newTestState()

	s.Untrack("c")
	if _, ok := s.Get("c"); ok {
		t.Error("Get(c) still present after Untrack")
	}
	if s.IsTracked("c") {
		t.Error("IsTracked(c) = true after Untrack")
	}
	if got := branchNames(s.Children("a")); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("Children(a) after untracking c = %v, want [b]", got)
	}
}

func TestRemoveBranchReparentsChildrenPreservingParentSHA(t *testing.T) {
	s := newTestState()

	former := s.RemoveBranch("a")
	if !reflect.DeepEqual(former, []string{"b", "c"}) {
		t.Fatalf("RemoveBranch(a) = %v, want [b c]", former)
	}
	if s.IsTracked("a") {
		t.Error("a still tracked after RemoveBranch")
	}
	for _, name := range []string{"b", "c"} {
		got, _ := s.Get(name)
		if got.Parent != "main" {
			t.Errorf("%s parent = %q after RemoveBranch, want main", name, got.Parent)
		}
		// The invariant: the child keeps its recorded base so a follow-up
		// restack drops the removed branch's commits.
		if got.ParentSHA != "sha-a" {
			t.Errorf("%s parentSHA = %q after RemoveBranch, want preserved sha-a", name, got.ParentSHA)
		}
	}
	if d, _ := s.Get("d"); d.Parent != "b" || d.ParentSHA != "sha-b" {
		t.Errorf("d = %+v after RemoveBranch(a), want untouched (b, sha-b)", d)
	}

	if got := s.RemoveBranch("not-tracked"); got != nil {
		t.Errorf("RemoveBranch(not-tracked) = %v, want nil", got)
	}
}

// TestDriftAgainstMatchesNeedsRestack asserts the pure tip-map drift
// computation agrees with the live NeedsRestack check, and treats a branch
// missing from the map (a deleted git branch) as not-drifted.
func TestDriftAgainstMatchesNeedsRestack(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "main", "c")
	// Amend a so b drifts.
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	f.amend("rewritten")

	tips, err := f.Tips()
	if err != nil {
		t.Fatal(err)
	}
	drift := s.DriftAgainst(tips)
	for _, name := range sortedBranchNames(s) {
		want, err := s.NeedsRestack(f, name)
		if err != nil {
			t.Fatalf("NeedsRestack(%s): %v", name, err)
		}
		if drift[name] != want {
			t.Errorf("drift[%s] = %v, NeedsRestack = %v", name, drift[name], want)
		}
	}
	if !drift["b"] {
		t.Error("b should drift after its parent was amended")
	}

	// A branch whose parent is missing from the map reports no drift; the
	// missing branch is its consumers' problem to report.
	s.Track("orphan", "gone", "sha-gone")
	drift = s.DriftAgainst(tips)
	if drift["orphan"] {
		t.Error("orphan with missing parent reported drift")
	}
}
