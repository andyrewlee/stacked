package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stacked/internal/git"
)

// captureStdout swaps os.Stdout for a pipe while fn runs and returns everything
// fn wrote to stdout. TestMain points os.Stdout at /dev/null for the rest of the
// suite; this helper temporarily restores a real pipe so output-shape tests can
// assert on what a command prints, then puts os.Stdout back.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()

	fn()
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// --- log -------------------------------------------------------------------

func TestLogTextAndJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCheckout(t, "feat-b")

	// Text output marks the current branch and lists trunk + both branches.
	text := captureStdout(t, func() {
		if err := runLog(nil); err != nil {
			t.Fatalf("log: %v", err)
		}
	})
	for _, want := range []string{"main", "feat-a", "feat-b"} {
		if !strings.Contains(text, want) {
			t.Fatalf("log text missing %q:\n%s", want, text)
		}
	}

	// JSON output is a valid tree rooted at trunk with the documented fields.
	jsonOut := captureStdout(t, func() {
		if err := runLog([]string{"--json"}); err != nil {
			t.Fatalf("log --json: %v", err)
		}
	})

	var root logNode
	if err := json.Unmarshal([]byte(jsonOut), &root); err != nil {
		t.Fatalf("log --json not valid JSON: %v\n%s", err, jsonOut)
	}
	if root.Name != "main" {
		t.Fatalf("root name = %q, want main", root.Name)
	}
	if len(root.Children) != 1 || root.Children[0].Name != "feat-a" {
		t.Fatalf("root children wrong: %+v", root.Children)
	}
	a := root.Children[0]
	if a.Parent != "main" {
		t.Fatalf("feat-a parent = %q, want main", a.Parent)
	}
	if len(a.Children) != 1 || a.Children[0].Name != "feat-b" {
		t.Fatalf("feat-a children wrong: %+v", a.Children)
	}
	b := a.Children[0]
	if !b.Current {
		t.Fatalf("feat-b should be marked current: %+v", b)
	}
	if b.ParentSHA == "" {
		t.Fatalf("feat-b should carry a parentSHA: %+v", b)
	}
	if b.TopCommit != "b" {
		t.Fatalf("feat-b topCommit = %q, want b", b.TopCommit)
	}
}

func TestLogJSONTrunkOnly(t *testing.T) {
	newRepo(t)
	mustInit(t)

	jsonOut := captureStdout(t, func() {
		if err := runLog([]string{"--json"}); err != nil {
			t.Fatalf("log --json: %v", err)
		}
	})
	var root logNode
	if err := json.Unmarshal([]byte(jsonOut), &root); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jsonOut)
	}
	if root.Name != "main" || len(root.Children) != 0 {
		t.Fatalf("trunk-only tree wrong: %+v", root)
	}
}

func TestLogNeedsRestackFlag(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b") // independent file: no conflict

	// Advance feat-a with a raw git commit (no auto-restack), so feat-b's
	// recorded parentSHA drifts and the JSON should flag needsRestack=true.
	mustCheckout(t, "feat-a")
	write(t, "a2.txt", "a2\n")
	mustRun(t, "git", "add", "-A")
	mustRun(t, "git", "commit", "-q", "-m", "a2")

	jsonOut := captureStdout(t, func() {
		if err := runLog([]string{"--json"}); err != nil {
			t.Fatalf("log --json: %v", err)
		}
	})
	var root logNode
	if err := json.Unmarshal([]byte(jsonOut), &root); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jsonOut)
	}
	// root(main) -> feat-a -> feat-b; feat-b should need a restack.
	a := root.Children[0]
	b := a.Children[0]
	if !b.NeedsRestack {
		t.Fatalf("feat-b should report needsRestack=true after parent moved: %+v", b)
	}
}

// --- status ----------------------------------------------------------------

type statusPayload struct {
	Branch        string   `json:"branch"`
	Role          string   `json:"role"`
	Parent        string   `json:"parent"`
	Children      []string `json:"children"`
	NeedsRestack  *bool    `json:"needsRestack"`
	WorktreeClean bool     `json:"worktreeClean"`
}

func TestStatusTextAndJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCheckout(t, "feat-a")

	text := captureStdout(t, func() {
		if err := runStatus(nil); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	for _, want := range []string{"branch:", "feat-a", "tracked", "feat-b"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status text missing %q:\n%s", want, text)
		}
	}

	jsonOut := captureStdout(t, func() {
		if err := runStatus([]string{"--json"}); err != nil {
			t.Fatalf("status --json: %v", err)
		}
	})
	var p statusPayload
	if err := json.Unmarshal([]byte(jsonOut), &p); err != nil {
		t.Fatalf("status --json invalid: %v\n%s", err, jsonOut)
	}
	if p.Branch != "feat-a" || p.Role != "tracked" || p.Parent != "main" {
		t.Fatalf("status payload wrong: %+v", p)
	}
	if len(p.Children) != 1 || p.Children[0] != "feat-b" {
		t.Fatalf("status children wrong: %+v", p.Children)
	}
	if p.NeedsRestack == nil || *p.NeedsRestack {
		t.Fatalf("status needsRestack want false-pointer, got %v", p.NeedsRestack)
	}
	if !p.WorktreeClean {
		t.Fatalf("status worktreeClean want true")
	}
}

func TestStatusTrunkJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)

	jsonOut := captureStdout(t, func() {
		if err := runStatus([]string{"--json"}); err != nil {
			t.Fatalf("status --json on trunk: %v", err)
		}
	})
	var p statusPayload
	if err := json.Unmarshal([]byte(jsonOut), &p); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jsonOut)
	}
	if p.Branch != "main" || p.Role != "trunk" {
		t.Fatalf("trunk status wrong: %+v", p)
	}
	if p.NeedsRestack != nil {
		t.Fatalf("trunk should have no needsRestack, got %v", *p.NeedsRestack)
	}
}

func TestStatusUntrackedJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustRun(t, "git", "checkout", "-q", "-b", "loose")

	jsonOut := captureStdout(t, func() {
		if err := runStatus([]string{"--json"}); err != nil {
			t.Fatalf("status --json on untracked: %v", err)
		}
	})
	var p statusPayload
	if err := json.Unmarshal([]byte(jsonOut), &p); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jsonOut)
	}
	if p.Branch != "loose" || p.Role != "untracked" {
		t.Fatalf("untracked status wrong: %+v", p)
	}
}

func TestStatusDirtyWorktree(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	write(t, "a.txt", "dirty\n") // unstaged change

	jsonOut := captureStdout(t, func() {
		if err := runStatus([]string{"--json"}); err != nil {
			t.Fatalf("status --json dirty: %v", err)
		}
	})
	var p statusPayload
	if err := json.Unmarshal([]byte(jsonOut), &p); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jsonOut)
	}
	if p.WorktreeClean {
		t.Fatalf("expected worktreeClean=false for a dirty tree")
	}
}

// --- track / untrack -------------------------------------------------------

func TestTrackAndUntrack(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	// Create a plain git branch off feat-a, then track it explicitly.
	mustRun(t, "git", "checkout", "-q", "-b", "feat-b")
	write(t, "b.txt", "b\n")
	mustRun(t, "git", "add", "-A")
	mustRun(t, "git", "commit", "-q", "-m", "b")

	if err := runTrack([]string{"--parent", "feat-a"}); err != nil {
		t.Fatalf("track: %v", err)
	}
	if b, _ := stateT(t).Get("feat-b"); b == nil || b.Parent != "feat-a" {
		t.Fatalf("feat-b not tracked with parent feat-a: %+v", b)
	}

	// Untrack feat-a; feat-b should be re-parented onto main (feat-a's parent).
	if err := runUntrack([]string{"feat-a"}); err != nil {
		t.Fatalf("untrack: %v", err)
	}
	s := stateT(t)
	if s.IsTracked("feat-a") {
		t.Fatalf("feat-a still tracked after untrack")
	}
	if b, _ := s.Get("feat-b"); b == nil || b.Parent != "main" {
		t.Fatalf("feat-b not re-parented onto main: %+v", b)
	}
}

func TestTrackInferParent(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	mustRun(t, "git", "checkout", "-q", "-b", "feat-b")
	write(t, "b.txt", "b\n")
	mustRun(t, "git", "add", "-A")
	mustRun(t, "git", "commit", "-q", "-m", "b")

	// No --parent: inferParent should pick feat-a (closest ancestor) over main.
	if err := runTrack(nil); err != nil {
		t.Fatalf("track (infer): %v", err)
	}
	if b, _ := stateT(t).Get("feat-b"); b == nil || b.Parent != "feat-a" {
		t.Fatalf("inferred parent wrong: %+v", b)
	}
}

func TestTrackGuards(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	// Tracking the trunk is rejected.
	mustCheckout(t, "main")
	if err := runTrack(nil); err == nil {
		t.Fatalf("expected error tracking the trunk")
	}

	// Tracking an already-tracked branch is rejected.
	mustCheckout(t, "feat-a")
	if err := runTrack(nil); err == nil {
		t.Fatalf("expected error tracking an already-tracked branch")
	}

	// Bad explicit parent is rejected.
	mustRun(t, "git", "checkout", "-q", "-b", "feat-c")
	write(t, "c.txt", "c\n")
	mustRun(t, "git", "add", "-A")
	mustRun(t, "git", "commit", "-q", "-m", "c")
	if err := runTrack([]string{"--parent", "no-such-branch"}); err == nil {
		t.Fatalf("expected error for unknown parent")
	}
	if err := runTrack([]string{"--parent", "feat-c"}); err == nil {
		t.Fatalf("expected error for self-parent")
	}
}

func TestUntrackGuards(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	if err := runUntrack([]string{"main"}); err == nil {
		t.Fatalf("expected error untracking the trunk")
	}
	if err := runUntrack([]string{"ghost"}); err == nil {
		t.Fatalf("expected error untracking an unknown branch")
	}
	if err := runUntrack([]string{"a", "b"}); err == nil {
		t.Fatalf("expected error for too many args")
	}
}

// --- restack ---------------------------------------------------------------

func TestRestackUpToDate(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCheckout(t, "feat-b")

	out := captureStdout(t, func() {
		if err := runRestack(nil); err != nil {
			t.Fatalf("restack: %v", err)
		}
	})
	if !strings.Contains(out, "up to date") {
		t.Fatalf("restack on clean stack should say up to date, got:\n%s", out)
	}
}

func TestRestackAfterParentMoves(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "f.txt", "A\n", "a")
	mustCreate(t, "feat-b", "g.txt", "B\n", "b") // independent file: no conflict

	// Advance feat-a by a raw commit (no auto-restack), so feat-b drifts.
	mustCheckout(t, "feat-a")
	write(t, "f.txt", "A2\n")
	mustRun(t, "git", "commit", "-aqm", "a2")

	// From the trunk, restack should rebase the whole stack and report feat-b.
	mustCheckout(t, "main")
	out := captureStdout(t, func() {
		if err := runRestack(nil); err != nil {
			t.Fatalf("restack: %v", err)
		}
	})
	if !strings.Contains(out, "restacked") || !strings.Contains(out, "feat-b") {
		t.Fatalf("restack should report feat-b restacked, got:\n%s", out)
	}
	if got := mustRun(t, "git", "show", "feat-b:f.txt"); got != "A2" {
		t.Fatalf("feat-b not rebased onto advanced feat-a: f.txt = %q", got)
	}
}

func TestRestackDirtyGuard(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	write(t, "a.txt", "dirty\n")
	if err := runRestack(nil); err == nil {
		t.Fatalf("expected restack to refuse a dirty working tree")
	}
}

// --- abort -----------------------------------------------------------------

func TestAbortNoRebase(t *testing.T) {
	newRepo(t)
	mustInit(t)
	if err := runAbort(nil); err == nil {
		t.Fatalf("expected error: no rebase in progress")
	}
}

func TestAbortRestoresState(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "f.txt", "A\n", "a")
	write(t, "f.txt", "A\nB\n")
	if err := runCreate([]string{"feat-b", "-a", "-m", "b"}); err != nil {
		t.Fatal(err)
	}
	mustCheckout(t, "feat-a")

	// Force a conflict via modify's auto-restack, then abort.
	write(t, "f.txt", "X\n")
	if err := runModify([]string{"-a"}); err == nil {
		t.Fatalf("expected a conflict")
	}
	if inProgress, _ := git.RebaseInProgress(); !inProgress {
		t.Fatalf("expected a rebase in progress")
	}
	if err := runAbort(nil); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if inProgress, _ := git.RebaseInProgress(); inProgress {
		t.Fatalf("rebase still in progress after abort")
	}
}

// --- submit ----------------------------------------------------------------

func TestSubmitNoRemote(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	if err := runSubmit(nil); err == nil {
		t.Fatalf("expected error: remote origin does not exist")
	}
}

func TestSubmitAtTrunk(t *testing.T) {
	newRepo(t)
	mustInit(t)
	// Configure a (file) remote so the remote-exists check passes.
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)

	mustCheckout(t, "main")
	out := captureStdout(t, func() {
		if err := runSubmit(nil); err != nil {
			t.Fatalf("submit at trunk: %v", err)
		}
	})
	if !strings.Contains(out, "nothing to submit") {
		t.Fatalf("expected 'nothing to submit' at trunk, got:\n%s", out)
	}
}

func TestSubmitDryRun(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)

	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCheckout(t, "feat-b")

	out := captureStdout(t, func() {
		if err := runSubmit([]string{"--dry-run"}); err != nil {
			t.Fatalf("submit --dry-run: %v", err)
		}
	})
	// Bottom-up order: feat-a then feat-b, both "would push", none pushed.
	for _, want := range []string{"would push feat-a", "would push feat-b", "dry run"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out)
		}
	}
}

func TestSubmitUntracked(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)

	mustRun(t, "git", "checkout", "-q", "-b", "loose")
	write(t, "x.txt", "x\n")
	mustRun(t, "git", "add", "-A")
	mustRun(t, "git", "commit", "-q", "-m", "x")
	if err := runSubmit(nil); err == nil {
		t.Fatalf("expected error submitting an untracked branch")
	}
}

// --- completion ------------------------------------------------------------

func TestCompletionShells(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		shell := shell
		out := captureStdout(t, func() {
			if err := runCompletion([]string{shell}); err != nil {
				t.Fatalf("completion %s: %v", shell, err)
			}
		})
		if strings.TrimSpace(out) == "" {
			t.Fatalf("completion %s produced no output", shell)
		}
		// Every script should mention the "create" subcommand somewhere.
		if !strings.Contains(out, "create") && !strings.Contains(out, "st") {
			t.Fatalf("completion %s missing command list:\n%s", shell, out)
		}
	}
}

func TestCompletionErrors(t *testing.T) {
	if err := runCompletion(nil); err == nil {
		t.Fatalf("expected error: completion needs a shell argument")
	}
	if err := runCompletion([]string{"powershell"}); err == nil {
		t.Fatalf("expected error: unsupported shell")
	}
	if err := runCompletion([]string{"bash", "extra"}); err == nil {
		t.Fatalf("expected error: too many args")
	}
}

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

// --- sync with a real remote ----------------------------------------------

func TestSyncFastForwardsTrunk(t *testing.T) {
	newRepo(t)
	mustInit(t)

	// Set up a bare origin and publish main.
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)
	mustRun(t, "git", "push", "-q", "-u", "origin", "main")
	mustRun(t, "git", "--git-dir", remoteDir, "symbolic-ref", "HEAD", "refs/heads/main")

	// Build a stack on the local main.
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	// Advance origin/main from a separate clone so the local trunk is behind.
	clone := t.TempDir()
	mustRun(t, "git", "clone", "-q", remoteDir, clone)
	mustRun(t, "git", "-C", clone, "config", "user.email", "t@e.com")
	mustRun(t, "git", "-C", clone, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(clone, "up.txt"), []byte("up\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "git", "-C", clone, "add", "-A")
	mustRun(t, "git", "-C", clone, "commit", "-q", "-m", "upstream advance")
	mustRun(t, "git", "-C", clone, "push", "-q", "origin", "main")

	// Sync: fetch + fast-forward trunk, then restack feat-a onto the new tip.
	mustCheckout(t, "feat-a")
	out := captureStdout(t, func() {
		if err := runSync(nil); err != nil {
			t.Fatalf("sync: %v", err)
		}
	})
	if !strings.Contains(out, "fast-forwarded") {
		t.Fatalf("sync should report a fast-forward, got:\n%s", out)
	}
	// The upstream file must now be present on the local trunk and on feat-a.
	if !hasFile("main", "up.txt") {
		t.Fatalf("trunk did not fast-forward to include up.txt")
	}
	if !hasFile("feat-a", "up.txt") {
		t.Fatalf("feat-a was not restacked onto the advanced trunk")
	}
}

// --- detectTrunk -----------------------------------------------------------

func TestDetectTrunkFallsBackToCurrentBranch(t *testing.T) {
	newRepo(t) // current branch is "main", no origin/HEAD configured
	if got := detectTrunk(); got != "main" {
		t.Fatalf("detectTrunk = %q, want main (current branch)", got)
	}
}

// --- init guards -----------------------------------------------------------

func TestInitAlreadyInitialized(t *testing.T) {
	newRepo(t)
	mustInit(t)
	out := captureStdout(t, func() {
		if err := runInit(nil); err != nil {
			t.Fatalf("second init: %v", err)
		}
	})
	if !strings.Contains(out, "already initialized") {
		t.Fatalf("re-init should report already initialized, got:\n%s", out)
	}
}
