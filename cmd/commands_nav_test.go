package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/andyrewlee/stacked/internal/git"
)

// Navigation commands over real git: up/down/top/bottom edge cases and the
// checkout listing.
// --- navigation edge cases -------------------------------------------------

func TestUpAmbiguousAndTop(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	// Two children of feat-a => a branch point.
	mustCheckout(t, "feat-a")
	mustCreate(t, "child-1", "c1.txt", "c1\n", "c1")
	mustCheckout(t, "feat-a")
	mustCreate(t, "child-2", "c2.txt", "c2\n", "c2")

	mustCheckout(t, "feat-a")
	out := captureStdout(t, func() {
		if err := runUp(nil); err != nil {
			t.Fatalf("up at fork: %v", err)
		}
	})
	if !strings.Contains(out, "multiple children") {
		t.Fatalf("up at a fork should list multiple children, got:\n%s", out)
	}
	// up must not move past the fork.
	if got := curBranch(t); got != "feat-a" {
		t.Fatalf("up moved past the fork to %q", got)
	}

	// top from the branch point reports the branch-point error.
	if err := runTop(nil); err == nil {
		t.Fatalf("expected top to error at a branch point")
	}
}

func TestUpAlreadyAtTop(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCheckout(t, "feat-a")

	out := captureStdout(t, func() {
		if err := runUp(nil); err != nil {
			t.Fatalf("up at leaf: %v", err)
		}
	})
	if !strings.Contains(out, "already at the top") {
		t.Fatalf("up at leaf should say already at the top, got:\n%s", out)
	}
}

// `st up` landing on a branch owned by another worktree refuses clearly,
// naming the owning worktree, instead of teleporting or leaking raw git errors.
func TestUpToLeafInOtherWorktreeRefusesClearly(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")

	// In-place arm: `up 2` overshoots the leaf, checks feat-b out locally, and
	// reports the top-of-stack switch.
	mustCheckout(t, "feat-a")
	out := captureStdout(t, func() {
		if err := runUp([]string{"2"}); err != nil {
			t.Fatalf("up 2 (in place): %v", err)
		}
	})
	if !strings.Contains(out, "switched to feat-b (top of stack)") {
		t.Fatalf("up 2 in place = %q, want the top-of-stack summary", out)
	}
	if got := curBranch(t); got != "feat-b" {
		t.Fatalf("up 2 in place left HEAD = %q, want feat-b", got)
	}

	mustCheckout(t, "feat-a")
	wtPath := filepath.Join(t.TempDir(), "wt-feat-b")
	mustRun(t, "git", "worktree", "add", "-q", wtPath, "feat-b")
	resetProcCaches()

	err := runUp([]string{"2"})
	if err == nil || !strings.Contains(err.Error(), "another worktree") {
		t.Fatalf("up 2 into foreign worktree = %v, want foreign-worktree refusal", err)
	}
	if got := curBranch(t); got != "feat-a" {
		t.Fatalf("refused up moved HEAD to %q, want feat-a", got)
	}
}

func TestDownClampAtTrunk(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCheckout(t, "feat-b")

	// down 5 should clamp at the trunk, stopping there rather than erroring.
	if err := runDown([]string{"5"}); err != nil {
		t.Fatalf("down 5: %v", err)
	}
	if got := curBranch(t); got != "main" {
		t.Fatalf("down 5 want main (clamped at trunk), got %q", got)
	}

	// From the trunk, down is a no-op notice.
	mustCheckout(t, "main")
	out := captureStdout(t, func() {
		if err := runDown(nil); err != nil {
			t.Fatalf("down at trunk: %v", err)
		}
	})
	if !strings.Contains(out, "already at trunk") {
		t.Fatalf("down at trunk should print notice, got:\n%s", out)
	}
}

func TestUpDownInvalidCounts(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	if err := runUp([]string{"zero"}); err == nil {
		t.Fatalf("up with non-integer should error")
	}
	if err := runUp([]string{"0"}); err == nil {
		t.Fatalf("up with 0 should error")
	}
	if err := runDown([]string{"nope"}); err == nil {
		t.Fatalf("down with non-integer should error")
	}
	if err := runDown([]string{"-1"}); err == nil {
		t.Fatalf("down with negative should error")
	}
	if err := runUp([]string{"1", "extra"}); err == nil {
		t.Fatalf("up with extra count should error")
	}
	if err := runDown([]string{"1", "extra"}); err == nil {
		t.Fatalf("down with extra count should error")
	}
}

func TestBottomNotices(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	// At the bottom branch already.
	mustCheckout(t, "feat-a")
	out := captureStdout(t, func() {
		if err := runBottom(nil); err != nil {
			t.Fatalf("bottom: %v", err)
		}
	})
	if !strings.Contains(out, "already at bottom") {
		t.Fatalf("bottom at the bottom should say so, got:\n%s", out)
	}

	// At the trunk.
	mustCheckout(t, "main")
	out = captureStdout(t, func() {
		if err := runBottom(nil); err != nil {
			t.Fatalf("bottom at trunk: %v", err)
		}
	})
	if !strings.Contains(out, "at trunk") {
		t.Fatalf("bottom at trunk should say so, got:\n%s", out)
	}
}

func TestNavigationRejectsUntrackedCurrentBranch(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustRun(t, "git", "checkout", "-q", "-b", "loose")
	for _, tt := range []struct {
		name string
		run  func([]string) error
	}{
		{"up", runUp},
		{"top", runTop},
		{"bottom", runBottom},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(nil); err == nil {
				t.Fatalf("%s succeeded on an untracked branch", tt.name)
			}
		})
	}
}

// --- checkout listing ------------------------------------------------------

func TestCheckoutListsBranches(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCheckout(t, "feat-a")

	out := captureStdout(t, func() {
		if err := runCheckout(nil); err != nil {
			t.Fatalf("checkout (list): %v", err)
		}
	})
	for _, want := range []string{"main", "feat-a", "feat-b"} {
		if !strings.Contains(out, want) {
			t.Fatalf("checkout listing missing %q:\n%s", want, out)
		}
	}
	// The current branch is marked with a leading "*".
	if !strings.Contains(out, "* feat-a") {
		t.Fatalf("checkout listing should mark current branch:\n%s", out)
	}
}

func TestCheckoutUntrackedError(t *testing.T) {
	newRepo(t)
	mustInit(t)
	if err := runCheckout([]string{"ghost"}); err == nil {
		t.Fatalf("expected error checking out an untracked branch")
	}
}

// TestTopRefusesParentCycle pins the corrupted-state guard: a state file
// recording a single-child cycle (a<->b) must error naming the cycle and the
// repair path, never spin forever. The engine's own invariants keep produced
// states acyclic; this covers hand-edited/recovered files, the same input
// class validate's ParentCycle problem kind reports.
func TestTopRefusesParentCycle(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCheckout(t, "feat-a")

	gitDir, err := git.GitCommonDir()
	if err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(gitDir, "stacked", "state.json"), map[string]any{
		"version": 1,
		"trunk":   "main",
		"branches": map[string]any{
			"feat-a": map[string]any{"name": "feat-a", "parent": "feat-b"},
			"feat-b": map[string]any{"name": "feat-b", "parent": "feat-a"},
		},
	})

	err = runTop(nil)
	if err == nil || !strings.Contains(err.Error(), "parent cycle") {
		t.Fatalf("top on cyclic state = %v, want a parent-cycle error pointing at validate/repair", err)
	}
}
