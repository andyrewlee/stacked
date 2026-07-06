package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stacked/internal/git"
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

func TestWorktreePathCanonical(t *testing.T) {
	got, err := WorktreePath("app", "api")
	if err != nil {
		t.Fatalf("WorktreePath: %v", err)
	}
	parts := worktreePathParts(t, got)
	if parts[0] != "app" {
		t.Errorf("repo segment = %q, want app", parts[0])
	}
	if parts[1] != "api" {
		t.Errorf("branch segment = %q, want api", parts[1])
	}
}

func TestWorktreeStableRepoKeyIncludesIdentityPath(t *testing.T) {
	base := t.TempDir()
	idA, err := StableRepoKey("repo", filepath.Join(base, "clone-a", "repo", ".git"))
	if err != nil {
		t.Fatalf("StableRepoKey A: %v", err)
	}
	idB, err := StableRepoKey("repo", filepath.Join(base, "clone-b", "repo", ".git"))
	if err != nil {
		t.Fatalf("StableRepoKey B: %v", err)
	}
	if idA == idB {
		t.Fatalf("same-basename repo keys collided: %q", idA)
	}
	for _, id := range []string{idA, idB} {
		if !strings.HasPrefix(id, "repo-") {
			t.Fatalf("repo key %q missing sanitized basename prefix", id)
		}
		if strings.Contains(id, string(os.PathSeparator)) {
			t.Fatalf("repo key %q contains a path separator", id)
		}
	}
}

func TestWorktreeStableRepoBaseUsesCommonGitDirParent(t *testing.T) {
	base := t.TempDir()
	mainRoot := filepath.Join(base, "repo")
	linkedRoot := filepath.Join(base, "linked")
	commonDir := filepath.Join(mainRoot, ".git")

	if got := StableRepoBase(linkedRoot, commonDir); got != "repo" {
		t.Fatalf("StableRepoBase linked = %q, want main repo basename %q", got, "repo")
	}
	if got := StableRepoBase(linkedRoot, filepath.Join(base, "repo.git")); got != "linked" {
		t.Fatalf("StableRepoBase fallback = %q, want linked root basename", got)
	}
}

func TestWorktreePathSanitizesBranchAndRepo(t *testing.T) {
	got, err := WorktreePath("my repo", "feat/foo/bar")
	if err != nil {
		t.Fatalf("WorktreePath: %v", err)
	}
	parts := worktreePathParts(t, got)
	if parts[0] != "my-repo" {
		t.Errorf("repo segment = %q, want %q", parts[0], "my-repo")
	}
	if parts[1] != "feat%2Ffoo%2Fbar" {
		t.Errorf("branch segment = %q, want %q", parts[1], "feat%2Ffoo%2Fbar")
	}
}

func TestWorktreePathBranchSegmentCollisionResistance(t *testing.T) {
	slash, err := WorktreePath("repo", "feat/foo")
	if err != nil {
		t.Fatalf("WorktreePath slash: %v", err)
	}
	dash, err := WorktreePath("repo", "feat-foo")
	if err != nil {
		t.Fatalf("WorktreePath dash: %v", err)
	}
	if slash == dash {
		t.Fatalf("WorktreePath collision: feat/foo and feat-foo both mapped to %q", slash)
	}
	slashParts := worktreePathParts(t, slash)
	dashParts := worktreePathParts(t, dash)
	if slashParts[1] == dashParts[1] {
		t.Fatalf("branch segment collision: %q", slashParts[1])
	}
}

func TestWorktreePathStaysUnderRootWithTwoSegments(t *testing.T) {
	got, err := WorktreePath("../repo name", "../feat:one two")
	if err != nil {
		t.Fatalf("WorktreePath: %v", err)
	}
	parts := worktreePathParts(t, got)
	if len(parts) != 2 {
		t.Fatalf("expected two path parts, got %v", parts)
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

func TestEncodeBranchSegment(t *testing.T) {
	cases := map[string]string{
		"plain":    "plain",
		"feat/foo": "feat%2Ffoo",
		"feat-foo": "feat-foo",
		"a\\b":     "a%5Cb",
		"x:y":      "x%3Ay",
		"with sp":  "with%20sp",
		"..hidden": "%2E.hidden",
		"100%":     "100%25",
		"~":        "%7E",
		"":         "~",
	}
	for in, want := range cases {
		if got := encodeBranchSegment(in); got != want {
			t.Errorf("encodeBranchSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

func worktreePathParts(t *testing.T, path string) []string {
	t.Helper()
	root, err := WorktreesRoot()
	if err != nil {
		t.Fatalf("WorktreesRoot: %v", err)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." {
		t.Fatalf("path %q is not under worktrees root %q", path, root)
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) != 2 {
		t.Fatalf("expected <repo>/<branch> (2 segments), got %q (%v)", rel, parts)
	}
	return parts
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
