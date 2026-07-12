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
		// Occasionally corrupt the metadata behind the engine's back and Repair
		// it: Repair is otherwise invisible to this model, yet restoring exactly
		// these invariants is its whole job.
		if len(tracked) > 0 && rng.Intn(12) == 0 {
			victim := pick(rng, tracked)
			s.Branches[victim].Parent = "ghost-" + victim // invalid, untracked parent
			if _, err := Repair(env, s); err != nil {
				t.Fatalf("step %d: repair: %v", step, err)
			}
			f.head = "main"
			if _, err := Restack(env, s); err != nil {
				t.Fatalf("step %d: restack after repair: %v", step, err)
			}
			checkInvariants(t, f, s, step)
			continue
		}
		// Occasionally simulate a branch merging into the trunk and prune it.
		// PruneMerged reshapes the forest (delete merged branches, re-parent the
		// survivors) the same way the rest of the model stresses other mutations,
		// but its trunk-ref mutation makes the centralized undo snapshot awkward,
		// so this runs outside the undo oracle: apply, then restack/idempotence/
		// invariants. The fixed TestModelSyncInvariants still covers the Sync
		// fetch/fast-forward wrapper.
		if len(tracked) > 0 && rng.Intn(12) == 0 {
			merged := pick(rng, tracked)
			mustCheckout(t, f, merged) // off main, so ForceBranch(main, ...) is allowed
			tip, err := f.RevParse(merged)
			if err != nil {
				t.Fatalf("step %d: rev-parse %s: %v", step, merged, err)
			}
			if err := f.ForceBranch(s.Trunk, tip); err != nil { // simulate merged into trunk
				t.Fatalf("step %d: force trunk: %v", step, err)
			}
			mustCheckout(t, f, s.Trunk) // Sync checks out trunk before pruning; trunk is never pruned
			if _, err := PruneMerged(env, s); err != nil {
				t.Fatalf("step %d: prune merged: %v", step, err)
			}
			f.head = "main"
			if _, err := Restack(env, s); err != nil {
				t.Fatalf("step %d: restack after prune: %v", step, err)
			}
			f.head = "main"
			if res, err := Restack(env, s); err != nil {
				t.Fatalf("step %d: prune restack idempotence: %v", step, err)
			} else if len(res.Restacked) != 0 {
				t.Fatalf("step %d: restack not idempotent after prune: %v", step, res.Restacked)
			}
			checkInvariants(t, f, s, step)
			continue
		}
		// Occasionally inject a conflict and resolve it with Continue. The random
		// walk otherwise only takes the clean restack path, so ErrConflict /
		// PendingReparent / Continue are never reached; this makes "a conflict
		// resolved at any random point still reconciles invariant-clean" a
		// property. Runs outside the undo oracle (the mutation is half-applied).
		if len(tracked) > 0 && rng.Intn(12) == 0 {
			var cands []string
			for _, n := range tracked {
				if s.Branches[n].Parent != s.Trunk { // amend the parent -> upstack restack hits n
					cands = append(cands, n)
				}
			}
			if len(cands) > 0 {
				victim := pick(rng, cands)
				parent := s.Branches[victim].Parent
				f.conflictOn(victim)
				mustCheckout(t, f, parent)
				if _, err := Modify(env, s, "", true, false); !errors.Is(err, ErrConflict) {
					t.Fatalf("step %d: want ErrConflict injecting on %s, got %v", step, victim, err)
				}
				if inProgress, _ := f.RebaseInProgress(); !inProgress {
					t.Fatalf("step %d: expected a rebase in progress after the injected conflict", step)
				}
				if _, err := Continue(env, s); err != nil {
					t.Fatalf("step %d: continue after injected conflict: %v", step, err)
				}
				f.head = "main"
				if res, err := Restack(env, s); err != nil {
					t.Fatalf("step %d: restack after continue: %v", step, err)
				} else if len(res.Restacked) != 0 {
					t.Fatalf("step %d: restack after continue not idempotent, rebased %v", step, res.Restacked)
				}
				checkInvariants(t, f, s, step)
				continue
			}
		}
		// Build the step as a re-runnable closure so it can be applied, undone,
		// and applied again.
		var label string
		var op func() error
		switch rng.Intn(10) {
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
		case 8: // untrack a random branch (re-parents its children)
			if len(tracked) == 0 {
				continue
			}
			target := pick(rng, tracked)
			label = "untrack"
			op = func() error {
				_, err := UntrackBranch(env, s, target)
				return err
			}
		case 9: // track a freshly-minted untracked branch (exercises inferParent)
			parent := pick(rng, append([]string{"main"}, tracked...))
			nameSeq++
			name := fmt.Sprintf("b%d", nameSeq)
			label = "track"
			op = func() error {
				mustCheckout(t, f, parent)
				if err := f.CreateBranch(name); err != nil {
					return err
				}
				f.commit("subj")
				_, err := TrackBranch(env, s, "")
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

		// On some steps, layer a WORKTREE dimension over the reconcile: materialize
		// worktrees for a random subset of tracked branches (so they are owned
		// elsewhere and the cross-worktree cascade activates) and dirty some of
		// them, then drive the reconcile through the real Restack and assert the
		// worktree-aware invariants. The branches not chosen stay single-tree, so
		// each run mixes both regimes. The helper tears every worktree back down
		// before returning so the next step starts single-tree-coherent (the
		// checkout-based mutation ops can't run against a branch parked elsewhere);
		// the plain single-tree reconcile below then finishes any dirty owner that
		// the cascade had to skip.
		maybeReconcileWithWorktrees(t, rng, f, s, env, step)

		// Reconcile the whole (now single-tree) forest, then assert invariants.
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

// maybeReconcileWithWorktrees, on a random subset of steps, parks some tracked
// branches in their own worktrees (a subset of those dirty), drives the reconcile
// through the real Restack so the owner-driven cross-worktree cascade runs, and
// asserts the worktree-aware invariants. It ALWAYS tears every worktree back down
// before returning (and clears the dirty flags) so the next step — and the
// caller's plain single-tree reconcile, which finishes any skipped dirty owner —
// starts single-tree-coherent. On the single-tree path it does nothing.
func maybeReconcileWithWorktrees(t *testing.T, rng *rand.Rand, f *fakeGit, s *State, env Env, step int) {
	t.Helper()
	tracked := sortedBranchNames(s)
	if len(tracked) == 0 || rng.Intn(3) != 0 {
		return // ~2/3 of steps stay purely single-tree
	}

	// Park a random subset of branches in their own worktrees; dirty some.
	f.head = "main"
	dirty := map[string]bool{}
	owned := map[string]bool{}
	for _, name := range tracked {
		if rng.Intn(2) == 0 {
			continue
		}
		f.addWorktree("/wt/"+name, name)
		owned[name] = true
		if rng.Intn(2) == 0 {
			f.markWorktreeDirty(name)
			dirty[name] = true
		}
	}
	// Always restore single-tree state on the way out, whatever the path taken.
	defer func() {
		f.linkedWorktrees = map[string]string{}
		f.dirtyWT = nil
	}()
	if len(owned) == 0 {
		return // nothing parked: fall back to the single-tree reconcile
	}

	// Occasionally arm ONE clean owned branch to conflict inside its worktree:
	// the cascade must roll it back and surface a NON-ErrConflict error while
	// every other invariant still holds, and a follow-up clean reconcile must
	// converge. This is the conflict × worktree combination the model never
	// exercised before.
	if rng.Intn(4) == 0 {
		victim := ""
		for _, name := range sortedBranchNames(s) {
			if owned[name] && !dirty[name] {
				victim = name
				break
			}
		}
		if victim != "" {
			f.conflictOn(victim)
			_, err := Restack(env, s)
			delete(f.conflictNext, victim)
			// The conflict fires only if the cascade actually tried to rebase
			// the victim (it may have been up to date); either way the state
			// must be coherent and a clean reconcile must converge below.
			if err != nil && errors.Is(err, ErrConflict) {
				t.Fatalf("step %d: cross-worktree conflict on %q surfaced as ErrConflict; want rollback error", step, victim)
			}
			if inProgress, _ := f.RebaseInProgress(); inProgress {
				t.Fatalf("step %d: cross-worktree conflict on %q left a rebase in progress", step, victim)
			}
			checkInvariants(t, f, s, step)
		}
	}

	if _, err := Restack(env, s); err != nil {
		t.Fatalf("step %d: cross-worktree restack-all: %v", step, err)
	}
	checkWorktreeInvariants(t, f, s, step, owned, dirty)
}

// checkWorktreeInvariants asserts the worktree-aware invariants after a reconcile
// that ran the cross-worktree cascade:
//
//	(i)   every existing invariant still holds regardless of worktree ownership
//	      (acyclic, valid parents, contains-base);
//	(ii)  every branch whose owning worktree is CLEAN (or has none) is reconciled
//	      (no longer NeedsRestack);
//	(iii) a branch still needing a restack is exactly one whose owning worktree is
//	      DIRTY, and it is reported in SkippedWorktrees;
//	(iv)  the main worktree's HEAD was never moved by the cascade.
func checkWorktreeInvariants(t *testing.T, f *fakeGit, s *State, step int, owned, dirty map[string]bool) {
	t.Helper()

	// (iv) the cascade rebases in owner worktrees (git -C <path>); the main
	// worktree's HEAD must be untouched.
	if cur, _ := f.CurrentBranch(); cur != "main" {
		t.Fatalf("step %d: main worktree HEAD = %q after cross-worktree restack, want main", step, cur)
	}

	// SkippedWorktrees is drained on read; snapshot it once.
	skipped := map[string]bool{}
	for _, name := range s.SkippedWorktrees() {
		skipped[name] = true
	}

	for _, name := range sortedBranchNames(s) {
		b := s.Branches[name]
		// (i) topology invariants hold regardless of where the branch lives.
		if !f.BranchExists(name) {
			t.Fatalf("step %d: tracked branch %q has no git branch", step, name)
		}
		if b.Parent != s.Trunk && (!s.IsTracked(b.Parent) || !f.BranchExists(b.Parent)) {
			t.Fatalf("step %d: %q has invalid parent %q", step, name, b.Parent)
		}
		if !mustFakeIsAncestor(t, f, b.ParentSHA, name) {
			t.Fatalf("step %d: %q parentSHA is not an ancestor of its tip", step, name)
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

		needs, err := s.NeedsRestack(f, name)
		if err != nil {
			t.Fatalf("step %d: NeedsRestack(%q): %v", step, name, err)
		}
		switch {
		case needs && !dirty[name]:
			// (ii)/(iii) only a DIRTY owner may be left needing a restack.
			t.Fatalf("step %d: %q still needs restack but its worktree is not dirty (owned=%v)", step, name, owned[name])
		case needs && dirty[name]:
			// (iii) and it must be reported as skipped.
			if !skipped[name] {
				t.Fatalf("step %d: dirty owner %q needs a restack but was not reported in SkippedWorktrees", step, name)
			}
		}
	}

	// (iii) the skip set must name only dirty owners (no spurious skips).
	for name := range skipped {
		if !dirty[name] {
			t.Fatalf("step %d: %q reported skipped but its worktree is not dirty", step, name)
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

// TestModelConflictAbortInvariants drives an onto conflict (which records a
// pending reparent) and backs out with Abort (the engine counterpart of
// Continue): the rebase is gone, the pending reparent is cleared, a second abort
// errors, and once the conflict is resolved the stack reconciles invariant-clean.
func TestModelConflictAbortInvariants(t *testing.T) {
	f := newFakeGit()
	s := &State{Trunk: "main", Branches: map[string]*Branch{}}
	env := Env{Git: f}

	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "main", "c")

	// Moving b onto c conflicts and records a pending reparent.
	f.conflictOn("b")
	mustCheckout(t, f, "b")
	if _, err := Onto(env, s, "c"); !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict from onto, got %v", err)
	}
	if inProgress, _ := f.RebaseInProgress(); !inProgress {
		t.Fatal("expected a rebase in progress after the conflict")
	}
	if s.PendingReparent == nil {
		t.Fatal("onto conflict should record a pending reparent")
	}

	if _, err := Abort(env, s); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if inProgress, _ := f.RebaseInProgress(); inProgress {
		t.Fatal("rebase should be gone after abort")
	}
	if s.PendingReparent != nil {
		t.Fatal("abort should clear the pending reparent for the conflicted branch")
	}
	if _, err := Abort(env, s); err == nil {
		t.Fatal("second abort with no rebase in progress should error")
	}

	// Resolve the underlying conflict (as the user would) and reconcile; every
	// invariant must hold on the re-parented stack.
	delete(f.conflictNext, "b")
	f.head = "main"
	if _, err := Restack(env, s); err != nil {
		t.Fatalf("restack after abort: %v", err)
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

	// Cross-check the hand-coded invariant above against the engine's own
	// classifier: a reconciled stack must have no inconsistencies.
	tips, err := f.Tips()
	if err != nil {
		t.Fatalf("step %d: Tips: %v", step, err)
	}
	if ps := s.Inconsistencies(tips); len(ps) != 0 {
		t.Fatalf("step %d: Inconsistencies on a reconciled stack: %+v", step, ps)
	}
}
