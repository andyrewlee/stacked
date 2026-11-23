package stack

import (
	"testing"

	"github.com/andyrewlee/stacked/internal/git"
)

type worktreeViewGit struct {
	Git
	current   string
	worktrees []git.Worktree
}

func (g worktreeViewGit) CurrentBranch() (string, error) {
	return g.current, nil
}

func (g worktreeViewGit) Worktrees() ([]git.Worktree, error) {
	return append([]git.Worktree(nil), g.worktrees...), nil
}

func mainOwnerFromLinkedGit(base Git, mainBranch, linkedBranch string) worktreeViewGit {
	return worktreeViewGit{
		Git:     base,
		current: linkedBranch,
		worktrees: []git.Worktree{
			{Path: "/repo", Branch: mainBranch},
			{Path: "/wt/" + linkedBranch, Branch: linkedBranch},
		},
	}
}

func TestOwnerOf(t *testing.T) {
	wts := []git.Worktree{
		{Path: "/main", Branch: "main"},
		{Path: "/wt/api", Branch: "api"},
	}
	if wt, ok := OwnerOf(wts, "api"); !ok || wt.Path != "/wt/api" {
		t.Errorf("OwnerOf(api) = %+v, %v; want /wt/api, true", wt, ok)
	}
	if _, ok := OwnerOf(wts, "nope"); ok {
		t.Error("OwnerOf(nope) reported an owner")
	}
}

func TestMainWorktree(t *testing.T) {
	wts := []git.Worktree{
		{Path: "/main", Branch: "main"},
		{Path: "/wt/api", Branch: "api"},
	}
	if wt, ok := MainWorktree(wts); !ok || wt.Path != "/main" {
		t.Errorf("MainWorktree = %+v, %v; want /main, true", wt, ok)
	}
	if _, ok := MainWorktree(nil); ok {
		t.Error("MainWorktree(nil) reported a worktree")
	}
}

func TestLinkedOwnerOf(t *testing.T) {
	wts := []git.Worktree{
		{Path: "/main", Branch: "main"},
		{Path: "/wt/api", Branch: "api"},
	}
	// A linked worktree's branch is found.
	if wt, ok := LinkedOwnerOf(wts, "api"); !ok || wt.Path != "/wt/api" {
		t.Errorf("LinkedOwnerOf(api) = %+v, %v; want /wt/api, true", wt, ok)
	}
	// The MAIN worktree's branch is NOT a linked owner (unlike OwnerOf), so
	// `st worktree add/rm main` does not treat the main tree as a separate worktree.
	if _, ok := LinkedOwnerOf(wts, "main"); ok {
		t.Error("LinkedOwnerOf(main) treated the main worktree as a linked one")
	}
	if _, ok := LinkedOwnerOf(wts, "nope"); ok {
		t.Error("LinkedOwnerOf(nope) reported an owner")
	}
}

func TestIsMultiWorktree(t *testing.T) {
	if IsMultiWorktree([]git.Worktree{{Path: "/main"}}) {
		t.Error("single worktree should not be multi")
	}
	if !IsMultiWorktree([]git.Worktree{{Path: "/main"}, {Path: "/wt"}}) {
		t.Error("two worktrees should be multi")
	}
	if IsMultiWorktree(nil) {
		t.Error("no worktrees should not be multi")
	}
}

func TestFakeWorktrees(t *testing.T) {
	f := newFakeGit()
	f.addWorktree("/wt/feat", "feat")
	// register a branch so its head resolves
	f.branches["feat"] = f.branches["main"]
	wts, err := f.Worktrees()
	if err != nil {
		t.Fatalf("Worktrees: %v", err)
	}
	if _, ok := OwnerOf(wts, "main"); !ok {
		t.Error("main worktree missing")
	}
	owner, ok := OwnerOf(wts, "feat")
	if !ok || owner.Path != "/wt/feat" {
		t.Errorf("feat owner = %+v, %v", owner, ok)
	}
}

func TestFakeWorktreesDetachedHead(t *testing.T) {
	f := newFakeGit()
	f.head = ""
	f.detachedAt = "c1"
	wts, err := f.Worktrees() // must not panic on detached HEAD
	if err != nil {
		t.Fatalf("Worktrees: %v", err)
	}
	if len(wts) != 1 || !wts[0].Detached {
		t.Errorf("detached main worktree = %+v", wts)
	}
}
