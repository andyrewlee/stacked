package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stacked/internal/git"
)

func TestWorktreePathCanonical(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	got, err := WorktreePath("app", "api")
	if err != nil {
		t.Fatalf("WorktreePath: %v", err)
	}
	want := filepath.Join(home, ".stacked", "worktrees", "app", "api")
	if got != want {
		t.Errorf("WorktreePath = %q, want %q", got, want)
	}
}

func TestWorktreePathSanitizesBranchAndRepo(t *testing.T) {
	got, err := WorktreePath("my repo", "feat/foo/bar")
	if err != nil {
		t.Fatalf("WorktreePath: %v", err)
	}
	// Each component must be a single path segment under the repo dir: no extra
	// separators leaking from the branch name, slashes collapsed to dashes.
	root, _ := WorktreesRoot()
	rel, err := filepath.Rel(root, got)
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) != 2 {
		t.Fatalf("expected <repo>/<branch> (2 segments), got %q (%v)", rel, parts)
	}
	if parts[0] != "my-repo" {
		t.Errorf("repo segment = %q, want %q", parts[0], "my-repo")
	}
	if parts[1] != "feat-foo-bar" {
		t.Errorf("branch segment = %q, want %q", parts[1], "feat-foo-bar")
	}
}

func TestSanitizeSegment(t *testing.T) {
	cases := map[string]string{
		"plain":     "plain",
		"a/b":       "a-b",
		"a\\b":      "a-b",
		"x:y":       "x-y",
		"with sp":   "with-sp",
		"..hidden":  "hidden",
		"-edge-":    "edge",
		"":          "_",
		"/":         "_",
		"...":       "_",
		"feat/long": "feat-long",
	}
	for in, want := range cases {
		if got := sanitizeSegment(in); got != want {
			t.Errorf("sanitizeSegment(%q) = %q, want %q", in, got, want)
		}
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
