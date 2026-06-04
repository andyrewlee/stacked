package stack

import (
	"fmt"
	"math/rand"
	"testing"
)

// TestModelInvariants applies long random sequences of stack operations to the
// engine (over the in-memory fake git) and asserts the core invariants hold
// after every step: the forest is acyclic with valid parents, every branch
// contains its recorded base (parentSHA is an ancestor of its tip), a full
// restack reconciles everything, and restack is idempotent.
func TestModelInvariants(t *testing.T) {
	for seed := int64(1); seed <= 6; seed++ {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			runModel(t, seed, 250)
		})
	}
}

func runModel(t *testing.T, seed int64, steps int) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	f := newFakeGit()
	s := &State{Trunk: "main", Branches: map[string]*Branch{}}
	env := Env{Git: f}
	nameSeq := 0

	for step := 0; step < steps; step++ {
		tracked := sortedBranchNames(s)
		switch rng.Intn(6) {
		case 0: // create off a random branch
			parent := pick(rng, append([]string{"main"}, tracked...))
			mustCheckout(t, f, parent)
			nameSeq++
			if _, err := Create(env, s, fmt.Sprintf("b%d", nameSeq), "subj", true); err != nil {
				t.Fatalf("create: %v", err)
			}
		case 1: // amend a random branch
			if len(tracked) == 0 {
				continue
			}
			mustCheckout(t, f, pick(rng, tracked))
			if _, err := Modify(env, s, "", true, false); err != nil {
				t.Fatalf("modify amend: %v", err)
			}
		case 2: // add a commit to a random branch
			if len(tracked) == 0 {
				continue
			}
			mustCheckout(t, f, pick(rng, tracked))
			if _, err := Modify(env, s, "extra", true, true); err != nil {
				t.Fatalf("modify commit: %v", err)
			}
		case 3: // fold a branch whose parent is not the trunk
			var cands []string
			for _, n := range tracked {
				if s.Branches[n].Parent != s.Trunk {
					cands = append(cands, n)
				}
			}
			if len(cands) == 0 {
				continue
			}
			mustCheckout(t, f, pick(rng, cands))
			if _, err := Fold(env, s); err != nil {
				t.Fatalf("fold: %v", err)
			}
		case 4: // move a branch onto a valid target
			if len(tracked) == 0 {
				continue
			}
			b := pick(rng, tracked)
			blocked := map[string]bool{b: true, s.Branches[b].Parent: true}
			for _, d := range s.Descendants(b) {
				blocked[d] = true
			}
			var targets []string
			for _, c := range append([]string{"main"}, tracked...) {
				if !blocked[c] {
					targets = append(targets, c)
				}
			}
			if len(targets) == 0 {
				continue
			}
			mustCheckout(t, f, b)
			if _, err := Onto(env, s, pick(rng, targets)); err != nil {
				t.Fatalf("onto: %v", err)
			}
		case 5: // delete a random branch
			if len(tracked) == 0 {
				continue
			}
			if err := func() error { _, err := Delete(env, s, pick(rng, tracked), true); return err }(); err != nil {
				t.Fatalf("delete: %v", err)
			}
		}

		// Reconcile the whole forest, then assert invariants.
		f.head = "main"
		if _, err := Restack(env, s); err != nil {
			t.Fatalf("step %d: restack-all: %v", step, err)
		}
		checkInvariants(t, f, s, step)

		// A second restack must rebase nothing (idempotence).
		f.head = "main"
		res, err := Restack(env, s)
		if err != nil {
			t.Fatalf("step %d: restack idempotence: %v", step, err)
		}
		if len(res.Restacked) != 0 {
			t.Fatalf("step %d: restack not idempotent, rebased %v", step, res.Restacked)
		}
	}
}

func pick(rng *rand.Rand, xs []string) string { return xs[rng.Intn(len(xs))] }

func mustCheckout(t *testing.T, f *fakeGit, name string) {
	t.Helper()
	if err := f.Checkout(name); err != nil {
		t.Fatalf("checkout %s: %v", name, err)
	}
}

func checkInvariants(t *testing.T, f *fakeGit, s *State, step int) {
	t.Helper()
	for _, name := range sortedBranchNames(s) {
		b := s.Branches[name]
		if !f.BranchExists(name) {
			t.Fatalf("step %d: tracked branch %q has no git branch", step, name)
		}
		if b.Parent != s.Trunk && (!s.IsTracked(b.Parent) || !f.BranchExists(b.Parent)) {
			t.Fatalf("step %d: %q has invalid parent %q", step, name, b.Parent)
		}
		if !mustFakeIsAncestor(t, f, b.ParentSHA, name) {
			t.Fatalf("step %d: %q parentSHA is not an ancestor of its tip", step, name)
		}
		needs, err := s.NeedsRestack(f, name)
		if err != nil {
			t.Fatalf("step %d: NeedsRestack(%q): %v", step, name, err)
		}
		if needs {
			t.Fatalf("step %d: %q still needs restack after restack-all", step, name)
		}
		seen := map[string]bool{name: true}
		for cur := b.Parent; cur != s.Trunk; {
			if seen[cur] {
				t.Fatalf("step %d: cycle through %q", step, name)
			}
			seen[cur] = true
			nb, ok := s.Get(cur)
			if !ok {
				t.Fatalf("step %d: %q parent chain hit untracked %q", step, name, cur)
			}
			cur = nb.Parent
		}
	}
}
