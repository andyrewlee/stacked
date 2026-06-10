package stack

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"
)

// TestModelInvariants applies long random sequences of stack operations to the
// engine (over the in-memory fake git) and asserts the core invariants hold
// after every step: the forest is acyclic with valid parents, every branch
// contains its recorded base (parentSHA is an ancestor of its tip), a full
// restack reconciles everything, and restack is idempotent. Every step is also
// an undo oracle: the op's effect is snapshotted, undone, verified equal to
// the snapshot, and re-applied before the run continues — so stack.Undo holds
// over thousands of random sequences, not just the spot checks.
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
		// Build the step as a re-runnable closure so it can be applied, undone,
		// and applied again.
		var label string
		var op func() error
		switch rng.Intn(8) {
		case 0: // create off a random branch
			parent := pick(rng, append([]string{"main"}, tracked...))
			nameSeq++
			name := fmt.Sprintf("b%d", nameSeq)
			label = "create"
			op = func() error {
				mustCheckout(t, f, parent)
				_, err := Create(env, s, name, "subj", true)
				return err
			}
		case 1: // amend a random branch
			if len(tracked) == 0 {
				continue
			}
			target := pick(rng, tracked)
			label = "modify"
			op = func() error {
				mustCheckout(t, f, target)
				_, err := Modify(env, s, "", true, false)
				return err
			}
		case 2: // add a commit to a random branch
			if len(tracked) == 0 {
				continue
			}
			target := pick(rng, tracked)
			label = "modify"
			op = func() error {
				mustCheckout(t, f, target)
				_, err := Modify(env, s, "extra", true, true)
				return err
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
			target := pick(rng, cands)
			label = "fold"
			op = func() error {
				mustCheckout(t, f, target)
				_, err := Fold(env, s)
				return err
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
			target := pick(rng, targets)
			label = "onto"
			op = func() error {
				mustCheckout(t, f, b)
				_, err := Onto(env, s, target)
				return err
			}
		case 5: // delete a random branch
			if len(tracked) == 0 {
				continue
			}
			target := pick(rng, tracked)
			label = "delete"
			op = func() error {
				_, err := Delete(env, s, target, true)
				return err
			}
		case 6: // squash a random branch (a no-op when it has a single commit)
			if len(tracked) == 0 {
				continue
			}
			target := pick(rng, tracked)
			label = "squash"
			op = func() error {
				mustCheckout(t, f, target)
				_, err := Squash(env, s, "squashed")
				return err
			}
		case 7: // rename a random branch to a fresh name
			if len(tracked) == 0 {
				continue
			}
			target := pick(rng, tracked)
			nameSeq++
			newName := fmt.Sprintf("b%d", nameSeq)
			label = "rename"
			op = func() error {
				_, err := Rename(env, s, target, newName)
				return err
			}
		}

		// The undo oracle: snapshot, apply, undo, verify, re-apply.
		entry, err := s.SnapshotUndo(f, label)
		if err != nil {
			t.Fatalf("step %d: snapshot before %s: %v", step, label, err)
		}
		if err := op(); err != nil {
			t.Fatalf("step %d: %s: %v", step, label, err)
		}
		if _, err := Undo(env, s, entry); err != nil {
			t.Fatalf("step %d: undo %s: %v", step, label, err)
		}
		assertUndoRestored(t, f, s, entry)
		if err := op(); err != nil {
			t.Fatalf("step %d: re-apply %s: %v", step, label, err)
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

// TestModelConflictContinueInvariants drives a real conflict-then-Continue (the
// recovery path the random model can't reach) and asserts the invariants hold
// afterward (TEST-3).
func TestModelConflictContinueInvariants(t *testing.T) {
	f := newFakeGit()
	s := &State{Trunk: "main", Branches: map[string]*Branch{}}
	env := Env{Git: f}

	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")

	// b conflicts the next time it restacks; amending a triggers the upstack
	// restack and the conflict on b.
	f.conflictOn("b")
	mustCheckout(t, f, "a")
	if _, err := Modify(env, s, "", true, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict from the upstack restack, got %v", err)
	}
	if inProgress, _ := f.RebaseInProgress(); !inProgress {
		t.Fatal("expected a rebase in progress after the conflict")
	}

	if _, err := Continue(env, s); err != nil {
		t.Fatalf("continue: %v", err)
	}
	// A full restack must now be idempotent and every invariant must hold.
	f.head = "main"
	if res, err := Restack(env, s); err != nil {
		t.Fatalf("restack after continue: %v", err)
	} else if len(res.Restacked) != 0 {
		t.Fatalf("restack after continue not idempotent, rebased %v", res.Restacked)
	}
	checkInvariants(t, f, s, 0)
}

// TestModelSyncInvariants runs a prune-merged sync and asserts the invariants
// hold on the remaining, re-parented stack (TEST-3).
func TestModelSyncInvariants(t *testing.T) {
	f := newFakeGit()
	s := &State{Trunk: "main", Branches: map[string]*Branch{}}
	env := Env{Git: f}

	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "b", "c")

	// Simulate "a" having merged into the trunk: advance main to a's tip.
	aTip, _ := f.RevParse("a")
	if err := f.ForceBranch("main", aTip); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(env, &fakeRemote{exists: false}, s, "origin", false); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if s.IsTracked("a") {
		t.Fatal("a should have been pruned as merged")
	}
	f.head = "main"
	if _, err := Restack(env, s); err != nil {
		t.Fatalf("restack after sync: %v", err)
	}
	checkInvariants(t, f, s, 0)
}

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
