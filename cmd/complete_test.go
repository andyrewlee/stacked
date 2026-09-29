package cmd

// Tests for st __complete — the hidden completion endpoint — and the pure
// candidate policy behind it. Endpoint tests run against real-git fixtures
// (newRepo); policy tests feed literals through completeCandidates.

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

// runComplete drives the endpoint and returns its stdout (and any error).
func runComplete(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		err = runCompleteEndpoint(args)
	})
	return out, err
}

// TestCompleteCheckoutCandidates: checkout's positional offers trunk plus every
// tracked branch, sorted — never untracked locals.
func TestCompleteCheckoutCandidates(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustRun(t, "git", "branch", "scratch")

	out, err := runComplete(t, "checkout", "0", "--")
	if err != nil {
		t.Fatalf("__complete checkout: %v", err)
	}
	if want := "feat-a\nfeat-b\nmain\n"; out != want {
		t.Fatalf("checkout candidates = %q, want %q", out, want)
	}
}

// TestCompleteAliasResolves: the alias word-1 token produces checkout's set.
func TestCompleteAliasResolves(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "fix-1", "f.txt", "f\n", "f")

	out, err := runComplete(t, "co", "0", "--")
	if err != nil {
		t.Fatalf("__complete co: %v", err)
	}
	if want := "feat-a\nfix-1\nmain\n"; out != want {
		t.Fatalf("co candidates = %q, want %q", out, want)
	}
}

// TestCompleteOntoExcludesSubtree: onto offers tracked branches outside the
// moving subtree — the current branch and its descendants are out, trunk in.
func TestCompleteOntoExcludesSubtree(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCreate(t, "feat-c", "c.txt", "c\n", "c")
	mustCheckout(t, "feat-b")

	out, err := runComplete(t, "onto", "0", "--")
	if err != nil {
		t.Fatalf("__complete onto: %v", err)
	}
	if want := "feat-a\nmain\n"; out != want {
		t.Fatalf("onto candidates = %q, want %q", out, want)
	}
}

// TestCompleteTrackUntracked: track's positional offers local branches that are
// neither tracked nor trunk.
func TestCompleteTrackUntracked(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustRun(t, "git", "branch", "scratch")
	mustRun(t, "git", "branch", "wip")

	out, err := runComplete(t, "track", "0", "--")
	if err != nil {
		t.Fatalf("__complete track: %v", err)
	}
	if want := "scratch\nwip\n"; out != want {
		t.Fatalf("track candidates = %q, want %q", out, want)
	}
}

// TestCompleteFlagValuePosition: after --parent the cursor completes TRACKED
// branches (the flag's value domain), not track's untracked-branch positional.
func TestCompleteFlagValuePosition(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustRun(t, "git", "branch", "scratch")

	out, err := runComplete(t, "track", "1", "--", "--parent")
	if err != nil {
		t.Fatalf("__complete track --parent: %v", err)
	}
	if want := "feat-a\nfeat-b\nmain\n"; out != want {
		t.Fatalf("track --parent candidates = %q, want %q", out, want)
	}

	// With the value already given, the next word is the untracked-branch
	// positional again.
	out, err = runComplete(t, "track", "2", "--", "--parent", "feat-a")
	if err != nil {
		t.Fatalf("__complete track --parent feat-a: %v", err)
	}
	if want := "scratch\n"; out != want {
		t.Fatalf("track post-value candidates = %q, want %q", out, want)
	}
}

// TestCompleteDoubleDashPositional: words after -- count as positionals even
// when flag-shaped; checkout's one positional already consumed completes empty.
func TestCompleteDoubleDashPositional(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")

	out, err := runComplete(t, "checkout", "2", "--", "feat-a", "--")
	if err != nil {
		t.Fatalf("__complete checkout post-terminator: %v", err)
	}
	if out != "" {
		t.Fatalf("post-terminator candidates = %q, want empty", out)
	}
}

// TestCompleteWorktree: the create form offers tracked branches without a
// linked worktree; after rm|remove it offers owners only.
func TestCompleteWorktree(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCheckout(t, "main")
	resetWorktreeCache()
	if err := runWorktree([]string{"feat-b"}); err != nil {
		t.Fatalf("worktree feat-b: %v", err)
	}
	t.Cleanup(func() {
		resetWorktreeCache()
		_ = runWorktree([]string{"rm", "feat-b"})
	})

	out, err := runComplete(t, "worktree", "0", "--")
	if err != nil {
		t.Fatalf("__complete worktree: %v", err)
	}
	if want := "feat-a\n"; out != want {
		t.Fatalf("worktree create candidates = %q, want %q", out, want)
	}

	out, err = runComplete(t, "worktree", "1", "--", "rm")
	if err != nil {
		t.Fatalf("__complete worktree rm: %v", err)
	}
	if want := "feat-b\n"; out != want {
		t.Fatalf("worktree rm candidates = %q, want %q", out, want)
	}

	// wt resolves to the same policy; ls offers nothing further.
	out, err = runComplete(t, "wt", "1", "--", "ls")
	if err != nil {
		t.Fatalf("__complete wt ls: %v", err)
	}
	if out != "" {
		t.Fatalf("worktree ls candidates = %q, want empty", out)
	}
}

// TestCompleteByteExactNames: legal-but-hairy refname bytes — slashes, braces,
// commas, a $(...)-shaped name, Unicode — round-trip untouched, one per line.
// (Square brackets are NOT legal refname bytes; $ ( ) are.) The byte-exactness
// assertion is also the inertness assertion: a name that looks like a command
// substitution is printed as data, never evaluated.
func TestCompleteByteExactNames(t *testing.T) {
	newRepo(t)
	mustInit(t)
	for i, name := range []string{"feat/a{b},c", "dasher--name", "$(touch_pwned)", "ユニコード"} {
		mustRun(t, "git", "checkout", "-qb", name)
		write(t, fmt.Sprintf("f%d.txt", i), "x\n")
		mustRun(t, "git", "add", "-A")
		mustRun(t, "git", "commit", "-qm", "c")
		if err := runTrack([]string{name}); err != nil {
			t.Fatalf("track %q: %v", name, err)
		}
	}
	mustCheckout(t, "main")

	out, err := runComplete(t, "checkout", "0", "--")
	if err != nil {
		t.Fatalf("__complete checkout: %v", err)
	}
	want := "$(touch_pwned)\ndasher--name\nfeat/a{b},c\nmain\nユニコード\n"
	if out != want {
		t.Fatalf("byte-exact candidates = %q, want %q", out, want)
	}
}

// TestCompleteDetachedHead: detached HEAD still offers checkout candidates —
// the endpoint needs no current branch to answer.
func TestCompleteDetachedHead(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustRun(t, "git", "checkout", "-q", "--detach")

	out, err := runComplete(t, "checkout", "0", "--")
	if err != nil {
		t.Fatalf("__complete detached: %v", err)
	}
	if want := "feat-a\nmain\n"; out != want {
		t.Fatalf("detached candidates = %q, want %q", out, want)
	}
	// onto degenerates to all tracked + trunk when there is no moving subtree.
	out, err = runComplete(t, "onto", "0", "--")
	if err != nil {
		t.Fatalf("__complete onto detached: %v", err)
	}
	if want := "feat-a\nmain\n"; out != want {
		t.Fatalf("onto detached candidates = %q, want %q", out, want)
	}
}

// TestCompleteSilentFailures: outside a repo, uninitialized, or facing a future
// state schema the endpoint is silent — empty output, nil error, no state file
// created as a side effect.
func TestCompleteSilentFailures(t *testing.T) {
	t.Run("no repository", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		resetWorktreeCache()
		out, err := runComplete(t, "checkout", "0", "--")
		if err != nil || out != "" {
			t.Fatalf("no-repo __complete = %q, %v — want empty/nil", out, err)
		}
	})

	t.Run("uninitialized", func(t *testing.T) {
		newRepo(t)
		out, err := runComplete(t, "checkout", "0", "--")
		if err != nil || out != "" {
			t.Fatalf("uninitialized __complete = %q, %v — want empty/nil", out, err)
		}
		if _, statErr := os.Stat(filepath.Join(".git", "stacked")); !os.IsNotExist(statErr) {
			t.Fatalf("__complete created state dir")
		}
	})

	t.Run("future state schema", func(t *testing.T) {
		newRepo(t)
		mustInit(t)
		mustCreate(t, "feat-a", "a.txt", "a\n", "a")
		path := filepath.Join(".git", "stacked", "state.json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read state: %v", err)
		}
		fut := strings.Replace(string(data), `"version": 1`, `"version": 9999`, 1)
		if fut == string(data) {
			t.Fatalf("state fixture lacks a version field to bump:\n%s", data)
		}
		if err := os.WriteFile(path, []byte(fut), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := runComplete(t, "checkout", "0", "--")
		if err != nil || out != "" {
			t.Fatalf("future-state __complete = %q, %v — want empty/nil", out, err)
		}
	})
}

// TestCompleteEndpointArgv: the protocol's hard errors — missing parts, no
// terminator, non-numeric index, index/word-count mismatch — exit nonzero;
// unknown and non-branch commands are silent empty.
func TestCompleteEndpointArgv(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	for _, args := range [][]string{
		{},
		{"checkout"},
		{"checkout", "0"},
		{"checkout", "x", "--"},
		{"checkout", "1", "--"},          // index without its word
		{"checkout", "0", "--", "extra"}, // fewer words than index
	} {
		if _, err := runComplete(t, args...); err == nil {
			t.Fatalf("__complete %v: want usage error, got nil", args)
		}
	}

	out, err := runComplete(t, "bogus", "0", "--")
	if err != nil || out != "" {
		t.Fatalf("unknown command = %q, %v — want empty/nil", out, err)
	}
	out, err = runComplete(t, "log", "0", "--")
	if err != nil || out != "" {
		t.Fatalf("non-positional command = %q, %v — want empty/nil", out, err)
	}
	out, err = runComplete(t, "__complete", "0", "--")
	if err != nil || out != "" {
		t.Fatalf("__complete itself = %q, %v — want empty/nil", out, err)
	}
}

// TestCompleteBoundedGitCalls: the endpoint's git probe count stays flat —
// state-file resolution plus at most a for-each-ref or worktree list — no
// rev-list history walks, no network.
func TestCompleteBoundedGitCalls(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustRun(t, "git", "branch", "scratch")

	logPath, start := recordGitCommands(t)
	start()
	out, err := runComplete(t, "track", "0", "--")
	if err != nil {
		t.Fatalf("__complete track: %v", err)
	}
	if want := "scratch\n"; out != want {
		t.Fatalf("track candidates = %q, want %q", out, want)
	}
	cmds := gitCommandLog(t, logPath)
	if len(cmds) > 4 {
		t.Fatalf("track completion spawned %d git calls (>4): %v", len(cmds), cmds)
	}
	for _, c := range cmds {
		for _, banned := range []string{"rev-list", "fetch", "ls-remote", "push"} {
			if strings.Contains(c, banned) {
				t.Fatalf("completion ran a history/network git call %q in %v", c, cmds)
			}
		}
	}
}

// TestCompleteHiddenFromListing: __complete never appears in the surfaces a
// user browses — word-1 candidates, help --json, or did-you-mean suggestions.
func TestCompleteHiddenFromListing(t *testing.T) {
	for _, n := range commandNames() {
		if n == "__complete" {
			t.Fatalf("commandNames exposed __complete")
		}
	}
	for _, c := range registry {
		if c.Name == "__complete" && !c.Hidden {
			t.Fatalf("__complete registered without Hidden")
		}
	}
	if got := suggestCommand("__complet"); got == "__complete" {
		t.Fatalf("suggestCommand offered the hidden command")
	}
}

// TestCompletionCursorClassifier: the positional parser reproduces parseArgs'
// rules — flags stop mattering after "--", non-bool flags eat their value, a
// trailing value-flag leaves the cursor in value position.
func TestCompletionCursorClassifier(t *testing.T) {
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	var b bool
	var s string
	fs.BoolVar(&b, "all", false, "")
	fs.BoolVar(&b, "json", false, "")
	fs.StringVar(&s, "parent", "", "")

	for _, tc := range []struct {
		words    []string
		wantFlag string
		wantPos  []string
	}{
		{nil, "", nil},
		{[]string{"feat-a"}, "", []string{"feat-a"}},
		{[]string{"--all"}, "", nil},
		{[]string{"--all", "feat-a"}, "", []string{"feat-a"}},
		{[]string{"feat-a", "--json"}, "", []string{"feat-a"}}, // reshuffled flag is still a flag
		{[]string{"--parent"}, "parent", nil},
		{[]string{"--parent", "feat-a"}, "", nil},
		{[]string{"--parent=x"}, "", nil},
		{[]string{"feat-a", "--"}, "", []string{"feat-a"}},
		{[]string{"feat-a", "--", "--json"}, "", []string{"feat-a", "--json"}},
		{[]string{"-3"}, "", []string{"-3"}},
		{[]string{"--bogus", "v"}, "", nil}, // unknown flag still eats its "value"
		{[]string{"--bogus"}, "bogus", nil},
	} {
		gotFlag, gotPos, _ := completionCursor(fs, tc.words)
		if gotFlag != tc.wantFlag || !reflect.DeepEqual(gotPos, tc.wantPos) {
			t.Fatalf("completionCursor(%v) = flag %q pos %v, want %q %v",
				tc.words, gotFlag, gotPos, tc.wantFlag, tc.wantPos)
		}
	}
}

// TestCompleteCandidatesPure: the candidate function over literal state —
// subtree exclusion, owner gating, and the all-flag suppression — without git.
func TestCompleteCandidatesPure(t *testing.T) {
	s := &stack.State{
		Trunk: "main",
		Branches: map[string]*stack.Branch{
			"feat-a": {Name: "feat-a", Parent: "main"},
			"feat-b": {Name: "feat-b", Parent: "feat-a"},
			"feat-c": {Name: "feat-c", Parent: "feat-b"},
		},
	}
	locals := map[string]string{"main": "0", "feat-a": "1", "feat-b": "2", "feat-c": "3", "wip": "4"}
	wts := []git.Worktree{
		{Path: "/repo", Branch: "main"},
		{Path: "/repo-wt", Branch: "feat-b"},
	}

	if got := completeCandidates("checkout", "", nil, nil, s, locals, wts, "feat-a"); !reflect.DeepEqual(got,
		[]string{"feat-a", "feat-b", "feat-c", "main"}) {
		t.Fatalf("checkout candidates = %v", got)
	}
	if got := completeCandidates("onto", "", nil, nil, s, locals, wts, "feat-b"); !reflect.DeepEqual(got,
		[]string{"feat-a", "main"}) {
		t.Fatalf("onto candidates = %v", got)
	}
	if got := completeCandidates("onto", "", []string{"feat-a"}, nil, s, locals, wts, "feat-b"); got != nil {
		t.Fatalf("onto second positional = %v, want nil", got)
	}
	if got := completeCandidates("track", "", nil, nil, s, locals, wts, "feat-a"); !reflect.DeepEqual(got,
		[]string{"wip"}) {
		t.Fatalf("track candidates = %v", got)
	}
	if got := completeCandidates("track", "", nil, map[string]bool{"all": true}, s, locals, wts, "feat-a"); got != nil {
		t.Fatalf("track --all candidates = %v, want nil", got)
	}
	if got := completeCandidates("track", "parent", nil, nil, s, locals, wts, "feat-a"); !reflect.DeepEqual(got,
		[]string{"feat-a", "feat-b", "feat-c", "main"}) {
		t.Fatalf("track --parent candidates = %v", got)
	}
	if got := completeCandidates("worktree", "", nil, nil, s, locals, wts, "feat-a"); !reflect.DeepEqual(got,
		[]string{"feat-a", "feat-c"}) {
		t.Fatalf("worktree create candidates = %v", got)
	}
	if got := completeCandidates("worktree", "", []string{"rm"}, nil, s, locals, wts, "feat-a"); !reflect.DeepEqual(got,
		[]string{"feat-b"}) {
		t.Fatalf("worktree rm candidates = %v", got)
	}
	if got := completeCandidates("worktree", "", nil, map[string]bool{"all": true}, s, locals, wts, "feat-a"); got != nil {
		t.Fatalf("worktree --all candidates = %v, want nil", got)
	}
}

// TestCompletableName: the defensive emitter drops names that would break the
// one-per-line framing — impossible from git, exercised for the contract.
func TestCompletableName(t *testing.T) {
	if completableName("") || completableName("a b") || completableName("a\nb") || completableName("a\x7fb") {
		t.Fatalf("completableName accepted an unframeable name")
	}
	if !completableName("feat/a{b},c[d]") || !completableName("ユニコード") {
		t.Fatalf("completableName rejected legal refname bytes")
	}
}
