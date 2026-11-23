package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

// Read-only command output and the machine (JSON) contract: log, status,
// submit, completion, guide, and the quiet-git JSON environment.

func decodeStrictJSON(t *testing.T, label, raw string, dst any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		t.Fatalf("%s did not match its strict JSON shape: %v\n%s", label, err, raw)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("%s emitted trailing JSON/content after the payload: %v\n%s", label, err, raw)
	}
}

func requireJSONObjectKeys(t *testing.T, label, raw string, want ...string) {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("%s was not a JSON object: %v\n%s", label, err, raw)
	}
	for _, key := range want {
		if _, ok := obj[key]; !ok {
			t.Fatalf("%s missing JSON key %q in %v", label, key, sortedJSONKeys(obj))
		}
	}
	if len(obj) != len(want) {
		t.Fatalf("%s JSON keys = %v, want exactly %v", label, sortedJSONKeys(obj), want)
	}
}

func sortedJSONKeys(obj map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sameExistingPath(a, b string) bool {
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
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
	// tip is the LIVE ref — parentSHA's sibling (the recorded base), not the
	// same SHA.
	if want := mustRun(t, "git", "rev-parse", "feat-b"); b.Tip != want || b.Tip == b.ParentSHA {
		t.Fatalf("feat-b tip = %q, want live tip %q (parentSHA %q)", b.Tip, want, b.ParentSHA)
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

// TestLogWarnsUnreachable pins plan-006: tracked branches the trunk walk never
// reaches (a parent cycle or a dangling parent) are named in a text warning
// and in the JSON root's `unreachable` field — the reachable tree still renders.
func TestLogWarnsUnreachable(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	sha := mustRun(t, "git", "rev-parse", "feat-a")
	mustRun(t, "git", "branch", "lost", "feat-a")
	mustRun(t, "git", "branch", "cy-a", "feat-a")
	mustRun(t, "git", "branch", "cy-b", "feat-a")
	// Corrupt the saved state: "lost" dangles off a missing parent, and
	// cy-a/cy-b form a detached cycle — both invisible to the trunk walk.
	s, err := loadState()
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	s.Track("lost", "ghost", sha)
	s.Track("cy-a", "cy-b", sha)
	s.Track("cy-b", "cy-a", sha)
	if err := s.Save(); err != nil {
		t.Fatalf("save state: %v", err)
	}

	text := captureStdout(t, func() {
		if err := runLog(nil); err != nil {
			t.Fatalf("log: %v", err)
		}
	})
	if !strings.Contains(text, "feat-a") {
		t.Fatalf("log text missing reachable branch:\n%s", text)
	}
	if !strings.Contains(text, "unreachable") || !strings.Contains(text, "lost") ||
		!strings.Contains(text, "cy-a") || !strings.Contains(text, "cy-b") ||
		!strings.Contains(text, "st doctor --fix") {
		t.Fatalf("log text missing the unreachable warning:\n%s", text)
	}

	jsonOut := captureStdout(t, func() {
		if err := runLog([]string{"--json"}); err != nil {
			t.Fatalf("log --json: %v", err)
		}
	})
	var root logNode
	if err := json.Unmarshal([]byte(jsonOut), &root); err != nil {
		t.Fatalf("log --json not valid JSON: %v\n%s", err, jsonOut)
	}
	want := []string{"cy-a", "cy-b", "lost"}
	if !reflect.DeepEqual(root.Unreachable, want) {
		t.Fatalf("root.unreachable = %v, want %v", root.Unreachable, want)
	}
	if len(root.Children) != 1 || root.Children[0].Name != "feat-a" {
		t.Fatalf("reachable tree wrong: %+v", root.Children)
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

// logTopCommit renders `log --json` and returns branch's topCommit field.
func logTopCommit(t *testing.T, branch string) string {
	t.Helper()
	jsonOut := captureStdout(t, func() {
		if err := runLog([]string{"--json"}); err != nil {
			t.Fatalf("log --json: %v", err)
		}
	})
	var root logNode
	if err := json.Unmarshal([]byte(jsonOut), &root); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jsonOut)
	}
	var find func(n *logNode) *logNode
	find = func(n *logNode) *logNode {
		if n.Name == branch {
			return n
		}
		for _, c := range n.Children {
			if got := find(c); got != nil {
				return got
			}
		}
		return nil
	}
	node := find(&root)
	if node == nil {
		t.Fatalf("branch %q not rendered in log tree: %s", branch, jsonOut)
	}
	return node.TopCommit
}

// TestLogOmitsTopCommitWhenBranchTipIsReachableFromParent pins the topCommit
// visibility rule: a subject is shown only when the branch's tip holds commits
// its parent's tip does not reach (the old `git log parent..branch` question,
// now answered by a bounded ancestry query per distinct tip pair).
func TestLogOmitsTopCommitWhenBranchTipIsReachableFromParent(t *testing.T) {
	t.Run("child behind parent", func(t *testing.T) {
		newRepo(t)
		mustInit(t)
		mustCreate(t, "feat-a", "a.txt", "a\n", "a")
		if err := runCreate([]string{"feat-b"}); err != nil {
			t.Fatalf("create feat-b: %v", err)
		}

		mustCheckout(t, "feat-a")
		write(t, "a2.txt", "a2\n")
		mustRun(t, "git", "add", "-A")
		mustRun(t, "git", "commit", "-q", "-m", "a2")

		if got := logTopCommit(t, "feat-b"); got != "" {
			t.Fatalf("feat-b topCommit = %q, want empty for branch behind parent", got)
		}
	})

	t.Run("child ahead of parent", func(t *testing.T) {
		newRepo(t)
		mustInit(t)
		mustCreate(t, "feat-a", "a.txt", "a\n", "a")
		mustCreate(t, "feat-b", "b.txt", "b\n", "b")

		if got := logTopCommit(t, "feat-b"); got != "b" {
			t.Fatalf("feat-b topCommit = %q, want %q", got, "b")
		}
	})

	t.Run("equal tips", func(t *testing.T) {
		newRepo(t)
		mustInit(t)
		mustCreate(t, "feat-a", "a.txt", "a\n", "a")
		mustRun(t, "git", "branch", "feat-b", "feat-a")
		if err := runTrack([]string{"feat-b", "--parent", "feat-a"}); err != nil {
			t.Fatalf("track feat-b: %v", err)
		}

		if got := logTopCommit(t, "feat-b"); got != "" {
			t.Fatalf("feat-b topCommit = %q, want empty for a tip equal to its parent", got)
		}
	})

	t.Run("diverged child shows its subject", func(t *testing.T) {
		newRepo(t)
		mustInit(t)
		mustCreate(t, "feat-a", "a.txt", "a\n", "a")
		mustCreate(t, "feat-b", "b.txt", "b\n", "b")

		// Move feat-a past feat-b's base while feat-b keeps its own commit.
		mustCheckout(t, "feat-a")
		write(t, "a2.txt", "a2\n")
		mustRun(t, "git", "add", "-A")
		mustRun(t, "git", "commit", "-q", "-m", "a2")

		if got := logTopCommit(t, "feat-b"); got != "b" {
			t.Fatalf("feat-b topCommit = %q, want %q for a diverged tip", got, "b")
		}
	})

	t.Run("tip reachable through a merge", func(t *testing.T) {
		newRepo(t)
		mustInit(t)
		mustCreate(t, "feat-a", "a.txt", "a\n", "a")
		mustCreate(t, "feat-b", "b.txt", "b\n", "b")

		// feat-a merges feat-b: b's tip is reachable from a's tip through the
		// merge parent, so b has no commits beyond its parent.
		mustCheckout(t, "feat-a")
		mustRun(t, "git", "merge", "-q", "-m", "merge b", "feat-b")

		if got := logTopCommit(t, "feat-b"); got != "" {
			t.Fatalf("feat-b topCommit = %q, want empty for a tip reachable via merge", got)
		}
	})

	t.Run("missing branch tip", func(t *testing.T) {
		newRepo(t)
		mustInit(t)
		mustCreate(t, "feat-a", "a.txt", "a\n", "a")
		mustCreate(t, "feat-b", "b.txt", "b\n", "b")
		mustCheckout(t, "main")
		mustRun(t, "git", "branch", "-D", "feat-b")

		if got := logTopCommit(t, "feat-b"); got != "" {
			t.Fatalf("feat-b topCommit = %q, want empty for a missing tip", got)
		}
	})

	t.Run("missing parent tip", func(t *testing.T) {
		newRepo(t)
		mustInit(t)
		mustCreate(t, "feat-a", "a.txt", "a\n", "a")
		mustCreate(t, "feat-b", "b.txt", "b\n", "b")
		mustCheckout(t, "main")
		mustRun(t, "git", "update-ref", "-d", "refs/heads/feat-a")

		if got := logTopCommit(t, "feat-b"); got != "" {
			t.Fatalf("feat-b topCommit = %q, want empty when the parent tip is missing", got)
		}
	})
}

// recordGitCommands prepends a logging git shim to PATH. Every spawned git
// appends its argv (space-joined, one line) to the returned log file while
// GIT_CMD_LOG is set; setup commands run before recording starts stay out.
func recordGitCommands(t *testing.T) (logPath string, start func()) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("resolve git: %v", err)
	}
	logPath = filepath.Join(t.TempDir(), "git-commands.log")
	shimDir := t.TempDir()
	var shimName, shim string
	if runtime.GOOS == "windows" {
		// Windows cannot exec an extensionless sh script, and a "git" file
		// would win the LookPath race over every PATHEXT suffix. git.cmd is
		// resolved and run via cmd by os/exec instead.
		shimName, shim = "git.cmd", "@echo off\r\nif defined GIT_CMD_LOG echo %*>> \"%GIT_CMD_LOG%\"\r\n\""+realGit+"\" %*\r\n"
	} else {
		shimName, shim = "git", fmt.Sprintf("#!/bin/sh\n[ -n \"$GIT_CMD_LOG\" ] && echo \"$@\" >> \"$GIT_CMD_LOG\"\nexec %q \"$@\"\n", realGit)
	}
	if err := os.WriteFile(filepath.Join(shimDir, shimName), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath, func() { t.Setenv("GIT_CMD_LOG", logPath) }
}

func gitCommandLog(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read git command log: %v", err)
	}
	// The Windows shim echoes CRLF-terminated lines; normalize before use.
	return strings.Split(strings.TrimRight(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n"), "\n")
}

// TestLogDoesNotMaterializeHistory: log must never ask git for an unbounded
// `rev-list --parents` history walk; ancestry questions go through bounded
// merge-base probes, once per distinct tip pair. A trunk-only render asks no
// ancestry question at all.
func TestLogDoesNotMaterializeHistory(t *testing.T) {
	t.Run("trunk only asks no ancestry question", func(t *testing.T) {
		newRepo(t)
		mustInit(t)
		logPath, start := recordGitCommands(t)
		start()
		if err := runLog([]string{"--json"}); err != nil {
			t.Fatalf("log --json: %v", err)
		}
		for _, line := range gitCommandLog(t, logPath) {
			if strings.HasPrefix(line, "rev-list") || strings.HasPrefix(line, "merge-base") {
				t.Fatalf("trunk-only log spawned a history query: %q", line)
			}
		}
	})

	t.Run("no unbounded rev-list for a stack", func(t *testing.T) {
		newRepo(t)
		mustInit(t)
		mustCreate(t, "feat-a", "a.txt", "a\n", "a")
		mustCreate(t, "feat-b", "b.txt", "b\n", "b")
		mustCreate(t, "feat-c", "c.txt", "c\n", "c")
		logPath, start := recordGitCommands(t)
		start()
		if err := runLog([]string{"--json"}); err != nil {
			t.Fatalf("log --json: %v", err)
		}
		for _, line := range gitCommandLog(t, logPath) {
			if strings.HasPrefix(line, "rev-list") {
				t.Fatalf("log materialized history via %q", line)
			}
		}
	})

	t.Run("shared tip pair is queried once", func(t *testing.T) {
		newRepo(t)
		mustInit(t)
		// feat-a sits at Y on main; feat-b and feat-c BOTH sit at X (a commit
		// on top of Y) and BOTH track feat-a — the same (childTip, parentTip)
		// pair, which must cost exactly one merge-base probe.
		mustRun(t, "git", "checkout", "-q", "-b", "feat-a")
		write(t, "a.txt", "a\n")
		mustRun(t, "git", "add", "-A")
		mustRun(t, "git", "commit", "-q", "-m", "a")
		mustRun(t, "git", "checkout", "-q", "-b", "feat-b")
		write(t, "b.txt", "b\n")
		mustRun(t, "git", "add", "-A")
		mustRun(t, "git", "commit", "-q", "-m", "b")
		mustRun(t, "git", "branch", "feat-c", "feat-b")
		mustCheckout(t, "main")
		if err := runTrack([]string{"feat-a", "--parent", "main"}); err != nil {
			t.Fatalf("track feat-a: %v", err)
		}
		if err := runTrack([]string{"feat-b", "--parent", "feat-a"}); err != nil {
			t.Fatalf("track feat-b: %v", err)
		}
		if err := runTrack([]string{"feat-c", "--parent", "feat-a"}); err != nil {
			t.Fatalf("track feat-c: %v", err)
		}
		logPath, start := recordGitCommands(t)
		start()
		if err := runLog([]string{"--json"}); err != nil {
			t.Fatalf("log --json: %v", err)
		}
		// Either feat-b or feat-c issues the shared pair's single probe —
		// map order is not deterministic — but feat-a's pair is always asked.
		shared, total := 0, 0
		for _, line := range gitCommandLog(t, logPath) {
			if strings.HasPrefix(line, "rev-list") {
				t.Fatalf("log materialized history via %q", line)
			}
			if strings.HasPrefix(line, "merge-base") {
				total++
				if line == "merge-base --is-ancestor refs/heads/feat-b refs/heads/feat-a" ||
					line == "merge-base --is-ancestor refs/heads/feat-c refs/heads/feat-a" {
					shared++
				}
			}
		}
		if shared != 1 {
			t.Fatalf("shared pair probed %d times, want 1; log:\n%s", shared, strings.Join(gitCommandLog(t, logPath), "\n"))
		}
		if total != 2 {
			t.Fatalf("merge-base probes = %d, want 2 (feat-a's pair + the shared pair)", total)
		}
	})
}

func TestLogJSONOmitsUnrelatedLocalBranch(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "tracked subject")

	unrelatedName := "unrelated-log-branch"
	unrelatedSubject := "unrelated log subject 025"
	mustRun(t, "git", "checkout", "-q", "-b", unrelatedName, "main")
	write(t, "unrelated.txt", "unrelated\n")
	mustRun(t, "git", "add", "-A")
	mustRun(t, "git", "commit", "-q", "-m", unrelatedSubject)
	mustCheckout(t, "feat-a")

	jsonOut := captureStdout(t, func() {
		if err := runLog([]string{"--json"}); err != nil {
			t.Fatalf("log --json: %v", err)
		}
	})
	if strings.Contains(jsonOut, unrelatedName) {
		t.Fatalf("log --json included unrelated branch name %q:\n%s", unrelatedName, jsonOut)
	}
	if strings.Contains(jsonOut, unrelatedSubject) {
		t.Fatalf("log --json included unrelated branch subject %q:\n%s", unrelatedSubject, jsonOut)
	}
}

func TestLogJSONDoesNotAnnotateMainWorktreeBranch(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "tracked subject")
	linkedMain := filepath.Join(t.TempDir(), "main-wt")
	mustRun(t, "git", "worktree", "add", "-q", linkedMain, "main")
	resetProcCaches()

	jsonOut := captureStdout(t, func() {
		if err := runLog([]string{"--json"}); err != nil {
			t.Fatalf("log --json: %v", err)
		}
	})
	var root logNode
	if err := json.Unmarshal([]byte(jsonOut), &root); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jsonOut)
	}
	if !sameExistingPath(root.Worktree, linkedMain) {
		t.Fatalf("trunk worktree = %q, want linked worktree %q", root.Worktree, linkedMain)
	}
	if len(root.Children) != 1 || root.Children[0].Name != "feat-a" {
		t.Fatalf("log tree = %+v, want feat-a child", root)
	}
	if got := root.Children[0].Worktree; got != "" {
		t.Fatalf("main worktree branch got linked-worktree annotation %q", got)
	}
}

// TestLogJSONPausedRebaseWorktree pins the plan-008 contract: a tracked branch
// paused mid-rebase in a LINKED worktree lists detached under `git worktree
// list`, but log --json must still annotate the owning branch — resolved
// through the rebase head-name — with its path, dirty state, the paused flag,
// and the conflicted paths the pause is waiting on. That is the one fleet
// state an orchestrator most needs to see without per-worktree `st status`.
func TestLogJSONPausedRebaseWorktree(t *testing.T) {
	newRepo(t)
	t.Setenv("HOME", t.TempDir())
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	mustCheckout(t, "main")
	write(t, "a.txt", "diverged\n")
	mustRun(t, "git", "add", "a.txt")
	mustRun(t, "git", "commit", "-q", "-m", "diverge main")

	wtPath := filepath.Join(t.TempDir(), "wt-feat-a")
	mustRun(t, "git", "worktree", "add", "-q", wtPath, "feat-a")
	if err := exec.Command("git", "-C", wtPath, "rebase", "main").Run(); err == nil {
		t.Fatal("git -C wt rebase main succeeded — fixture did not pause mid-rebase")
	}
	resetProcCaches()

	jsonOut := captureStdout(t, func() {
		if err := runLog([]string{"--json"}); err != nil {
			t.Fatalf("log --json: %v", err)
		}
	})
	var root logNode
	if err := json.Unmarshal([]byte(jsonOut), &root); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jsonOut)
	}
	var node *logNode
	for _, c := range root.Children {
		if c.Name == "feat-a" {
			node = c
		}
	}
	if node == nil {
		t.Fatalf("log tree missing feat-a:\n%s", jsonOut)
	}
	if !sameExistingPath(node.Worktree, wtPath) {
		t.Fatalf("paused branch worktree = %q, want %q", node.Worktree, wtPath)
	}
	if !node.Paused {
		t.Fatalf("paused branch did not report paused:true:\n%s", jsonOut)
	}
	if !node.Dirty {
		t.Fatal("paused worktree is conflicted-dirty; want dirty:true")
	}
	foundConflict := false
	for _, f := range node.ConflictedFiles {
		if f == "a.txt" {
			foundConflict = true
		}
	}
	if !foundConflict {
		t.Fatalf("conflictedFiles = %v, want a.txt", node.ConflictedFiles)
	}
	if node.Locked {
		t.Fatal("unlocked worktree reported locked")
	}

	// A `git worktree lock` on the paused worktree surfaces as `locked` on the
	// same node — the hold plan 007's restack/removal gates honor.
	mustRun(t, "git", "worktree", "lock", wtPath)
	resetProcCaches()
	jsonOut = captureStdout(t, func() {
		if err := runLog([]string{"--json"}); err != nil {
			t.Fatalf("log --json after lock: %v", err)
		}
	})
	root = logNode{}
	if err := json.Unmarshal([]byte(jsonOut), &root); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jsonOut)
	}
	node = nil
	for _, c := range root.Children {
		if c.Name == "feat-a" {
			node = c
		}
	}
	if node == nil || !node.Locked || !node.Paused {
		t.Fatalf("locked paused worktree = %+v, want locked+paused", node)
	}
}

// TestLogTextPausedRebaseWorktree covers the text render of the same state:
// the branch keeps its worktree tag, now with `paused` appended.
func TestLogTextPausedRebaseWorktree(t *testing.T) {
	newRepo(t)
	t.Setenv("HOME", t.TempDir())
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	mustCheckout(t, "main")
	write(t, "a.txt", "diverged\n")
	mustRun(t, "git", "add", "a.txt")
	mustRun(t, "git", "commit", "-q", "-m", "diverge main")

	wtPath := filepath.Join(t.TempDir(), "wt-feat-a")
	mustRun(t, "git", "worktree", "add", "-q", wtPath, "feat-a")
	if err := exec.Command("git", "-C", wtPath, "rebase", "main").Run(); err == nil {
		t.Fatal("git -C wt rebase main succeeded — fixture did not pause mid-rebase")
	}
	resetProcCaches()

	out := captureStdout(t, func() {
		if err := runLog(nil); err != nil {
			t.Fatalf("log: %v", err)
		}
	})
	if !strings.Contains(out, "paused") {
		t.Fatalf("text log lost the pause signal for feat-a:\n%s", out)
	}
}

// --- status ----------------------------------------------------------------

type statusPayload struct {
	Branch        string   `json:"branch"`
	Role          string   `json:"role"`
	Parent        string   `json:"parent"`
	Tip           string   `json:"tip"`
	Published     string   `json:"published"`
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
	if want := mustRun(t, "git", "rev-parse", "feat-a"); p.Tip != want {
		t.Fatalf("status tip = %q, want live feat-a tip %q", p.Tip, want)
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

// --- commits -----------------------------------------------------------------

// --- init ------------------------------------------------------------------

func TestInitJSONFreshAndRepeatedShape(t *testing.T) {
	newRepo(t)

	type initJSON struct {
		Trunk              string `json:"trunk"`
		Initialized        bool   `json:"initialized"`
		AlreadyInitialized bool   `json:"alreadyInitialized"`
	}

	freshOut := captureStdout(t, func() {
		if err := runInit([]string{"--trunk", "main", "--json"}); err != nil {
			t.Fatalf("fresh init --json: %v", err)
		}
	})
	requireJSONObjectKeys(t, "fresh init --json", freshOut, "trunk", "initialized", "alreadyInitialized")
	var fresh initJSON
	decodeStrictJSON(t, "fresh init --json", freshOut, &fresh)
	if fresh.Trunk != "main" || !fresh.Initialized || fresh.AlreadyInitialized {
		t.Fatalf("fresh init payload = %+v, want initialized main", fresh)
	}

	repeatedOut := captureStdout(t, func() {
		if err := runInit([]string{"--json"}); err != nil {
			t.Fatalf("repeated init --json: %v", err)
		}
	})
	requireJSONObjectKeys(t, "repeated init --json", repeatedOut, "trunk", "initialized", "alreadyInitialized")
	var repeated initJSON
	decodeStrictJSON(t, "repeated init --json", repeatedOut, &repeated)
	if repeated.Trunk != "main" || repeated.Initialized || !repeated.AlreadyInitialized {
		t.Fatalf("repeated init payload = %+v, want alreadyInitialized main", repeated)
	}
}

// --- create --worktree -----------------------------------------------------

// --- worktree --------------------------------------------------------------

// --- submit ----------------------------------------------------------------

func TestSubmitNoRemote(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	if err := runSubmit(nil); err == nil {
		t.Fatalf("expected error: remote origin does not exist")
	}
}

// TestSubmitTakesAdvisoryLock pins plan-009: submit was the only stack-reading
// command that never serialized — a concurrent sync/delete/undo mid-submit
// could publish a stale tip or fail on a vanished branch. With the repo lock
// held and no wait budget, submit must refuse with ErrLocked before touching
// state or remotes.
func TestSubmitTakesAdvisoryLock(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	release, err := stack.Lock()
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer release()
	t.Setenv("ST_LOCK_WAIT", "0")

	for _, args := range [][]string{nil, {"--all"}, {"--dry-run"}} {
		if err := runSubmit(args); !errors.Is(err, stack.ErrLocked) {
			t.Fatalf("submit %v under held lock = %v, want ErrLocked", args, err)
		}
	}
	// Nothing was pushed or printed: the refusal happens before any remote work.
	if out := mustRun(t, "git", "--git-dir", remoteDir, "for-each-ref"); strings.Contains(out, "feat-a") {
		t.Fatalf("feat-a reached the remote despite the held lock:\n%s", out)
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

func TestSubmitDryRunJSONShape(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)

	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCheckout(t, "feat-b")

	out := captureStdout(t, func() {
		if err := runSubmit([]string{"--no-pr", "--dry-run", "--json"}); err != nil {
			t.Fatalf("submit --dry-run --json: %v", err)
		}
	})
	requireJSONObjectKeys(t, "submit --dry-run --json", out, "remote", "dryRun", "pushed", "prHints", "published")
	type submitDryRunJSON struct {
		Remote    string            `json:"remote"`
		DryRun    bool              `json:"dryRun"`
		Pushed    []string          `json:"pushed"`
		Published map[string]string `json:"published"`
		PRHints   []stack.PRHint    `json:"prHints"`
	}
	var got submitDryRunJSON
	decodeStrictJSON(t, "submit --dry-run --json", out, &got)
	if got.Remote != "origin" {
		t.Fatalf("dry-run remote = %q, want origin", got.Remote)
	}
	if !got.DryRun {
		t.Fatalf("dry-run payload = %+v, want dryRun true", got)
	}
	if want := []string{"feat-a", "feat-b"}; !reflect.DeepEqual(got.Pushed, want) {
		t.Fatalf("dry-run pushed = %v, want %v", got.Pushed, want)
	}
	// Nothing was ever pushed: every would-push branch reads missing.
	wantPub := map[string]string{"feat-a": "missing", "feat-b": "missing"}
	if !reflect.DeepEqual(got.Published, wantPub) {
		t.Fatalf("dry-run published = %v, want %v", got.Published, wantPub)
	}
	wantHints := []stack.PRHint{{Head: "feat-a", Base: "main"}, {Head: "feat-b", Base: "feat-a"}}
	if !reflect.DeepEqual(got.PRHints, wantHints) {
		t.Fatalf("dry-run prHints = %+v, want %+v", got.PRHints, wantHints)
	}
	if refs := mustRun(t, "git", "--git-dir", remoteDir, "for-each-ref", "--format=%(refname)", "refs/heads"); refs != "" {
		t.Fatalf("dry-run created remote refs:\n%s", refs)
	}
}

// TestPublishedStatesAcrossSurfaces exercises the published enum end to end:
// submit --dry-run reports every would-push branch's state against origin's
// tracking refs, and status --json reports the checked-out branch's — all from
// local refs, so the same fixture also proves no fetch happens (the stale
// branch's remote-side advance is invisible until a fetch).
func TestPublishedStatesAcrossSurfaces(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)

	mustCreate(t, "feat-current", "c.txt", "c\n", "c")
	mustRun(t, "git", "push", "-q", "-u", "origin", "feat-current")

	mustCreate(t, "feat-stale", "s.txt", "s\n", "s")
	mustRun(t, "git", "push", "-q", "-u", "origin", "feat-stale")
	mustRun(t, "git", "commit", "-q", "--allow-empty", "-m", "ahead")

	mustCreate(t, "feat-diverged", "d.txt", "d\n", "d")
	mustRun(t, "git", "push", "-q", "-u", "origin", "feat-diverged")
	write(t, "d.txt", "rewritten\n")
	mustRun(t, "git", "commit", "-q", "-a", "--amend", "--no-edit")

	mustCreate(t, "feat-missing", "m.txt", "m\n", "m")
	mustCheckout(t, "feat-missing")

	out := captureStdout(t, func() {
		if err := runSubmit([]string{"--no-pr", "--dry-run", "--json"}); err != nil {
			t.Fatalf("submit --dry-run --json: %v", err)
		}
	})
	var sub submitResult
	decodeStrictJSON(t, "submit --dry-run published", out, &sub)
	want := map[string]git.PublishedState{
		"feat-current":  git.PublishedCurrent,
		"feat-stale":    git.PublishedStale,
		"feat-diverged": git.PublishedDiverged,
		"feat-missing":  git.PublishedMissing,
	}
	if !reflect.DeepEqual(sub.Published, want) {
		t.Fatalf("submit published = %v, want %v", sub.Published, want)
	}

	mustCheckout(t, "feat-stale")
	statusOut := captureStdout(t, func() {
		if err := runStatus([]string{"--json"}); err != nil {
			t.Fatalf("status --json: %v", err)
		}
	})
	var p statusPayload
	if err := json.Unmarshal([]byte(statusOut), &p); err != nil {
		t.Fatalf("status --json invalid: %v\n%s", err, statusOut)
	}
	if p.Published != string(git.PublishedStale) {
		t.Fatalf("status published = %q, want %q", p.Published, git.PublishedStale)
	}

	// Proof the reads were offline: drop every tracking ref under origin and
	// status must report missing rather than fetching them back.
	for _, ref := range strings.Fields(mustRun(t, "git", "for-each-ref", "--format=%(refname)", "refs/remotes")) {
		mustRun(t, "git", "update-ref", "-d", ref)
	}
	statusOut = captureStdout(t, func() {
		if err := runStatus([]string{"--json"}); err != nil {
			t.Fatalf("status --json after ref drop: %v", err)
		}
	})
	var p2 statusPayload
	if err := json.Unmarshal([]byte(statusOut), &p2); err != nil {
		t.Fatalf("status --json invalid: %v\n%s", err, statusOut)
	}
	if p2.Published != string(git.PublishedMissing) {
		t.Fatalf("status published after ref drop = %q, want %q (a fetch would have restored stale)", p2.Published, git.PublishedMissing)
	}
}

func TestStatusPublishedAbsentWithoutRemote(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	out := captureStdout(t, func() {
		if err := runStatus([]string{"--json"}); err != nil {
			t.Fatalf("status --json: %v", err)
		}
	})
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("status --json invalid: %v\n%s", err, out)
	}
	if _, ok := raw["published"]; ok {
		t.Fatalf("status --json emitted published with no remote configured:\n%s", out)
	}
}

// TestSubmitJSONSingleShape asserts the trunk early-return and the pushed path
// emit the same JSON object — no unknown keys in either direction.
func TestSubmitJSONSingleShape(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	decode := func(t *testing.T, raw string) submitResult {
		t.Helper()
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		var got submitResult
		if err := dec.Decode(&got); err != nil {
			t.Fatalf("submit --json did not unmarshal into the single shape: %v\n%s", err, raw)
		}
		return got
	}

	out := captureStdout(t, func() {
		if err := runSubmit([]string{"--json"}); err != nil {
			t.Fatalf("submit --json: %v", err)
		}
	})
	pushed := decode(t, out)
	if pushed.Remote != "origin" || len(pushed.Pushed) != 1 || pushed.Pushed[0] != "feat-a" {
		t.Fatalf("pushed-case payload = %+v, want feat-a pushed to origin", pushed)
	}

	mustCheckout(t, "main")
	out = captureStdout(t, func() {
		if err := runSubmit([]string{"--json"}); err != nil {
			t.Fatalf("submit --json at trunk: %v", err)
		}
	})
	trunk := decode(t, out)
	if trunk.Summary != "at trunk; nothing to submit" {
		t.Fatalf("trunk-case summary = %q, want the nothing-to-submit summary", trunk.Summary)
	}
	if trunk.Pushed == nil || len(trunk.Pushed) != 0 {
		t.Fatalf("trunk-case pushed = %v, want present-but-empty", trunk.Pushed)
	}
}

func TestSubmitPRHintsGitHubJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustRun(t, "git", "remote", "add", "origin", "git@github.com:owner/repo.git")
	mustCreate(t, "feat/slash", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")

	out := captureStdout(t, func() {
		if err := runSubmit([]string{"--no-pr", "--dry-run", "--json"}); err != nil {
			t.Fatalf("submit --dry-run --json: %v", err)
		}
	})
	var got submitResult
	decodeStrictJSON(t, "submit pr hints", out, &got)
	want := []stack.PRHint{
		{
			Head:       "feat/slash",
			Base:       "main",
			CompareURL: "https://github.com/owner/repo/compare/main...feat%2Fslash",
		},
		{
			Head:       "feat-b",
			Base:       "feat/slash",
			CompareURL: "https://github.com/owner/repo/compare/feat%2Fslash...feat-b",
		},
	}
	if !reflect.DeepEqual(got.PRHints, want) {
		t.Fatalf("prHints = %+v, want %+v", got.PRHints, want)
	}
}

func TestSubmitPRHintsGitLabJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustRun(t, "git", "remote", "add", "origin", "git@gitlab.com:owner/repo.git")
	mustCreate(t, "feat/slash", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")

	out := captureStdout(t, func() {
		if err := runSubmit([]string{"--no-pr", "--dry-run", "--json"}); err != nil {
			t.Fatalf("submit --dry-run --json: %v", err)
		}
	})
	var got submitResult
	decodeStrictJSON(t, "submit gitlab pr hints", out, &got)
	if got.RepoURL != "https://gitlab.com/owner/repo" {
		t.Fatalf("repoURL = %q, want https://gitlab.com/owner/repo", got.RepoURL)
	}
	want := []stack.PRHint{
		{
			Head:       "feat/slash",
			Base:       "main",
			CompareURL: "https://gitlab.com/owner/repo/-/compare/main...feat%2Fslash",
		},
		{
			Head:       "feat-b",
			Base:       "feat/slash",
			CompareURL: "https://gitlab.com/owner/repo/-/compare/feat%2Fslash...feat-b",
		},
	}
	if !reflect.DeepEqual(got.PRHints, want) {
		t.Fatalf("prHints = %+v, want %+v", got.PRHints, want)
	}
}

// TestSubmitPRHintsGitLabMixedCaseHostJSON pins prCompareURL's lowercased host
// matching: a mixed-case remote host must still take the gitlab /-/compare/
// path shape (stack.RemoteToHTTPS preserves the host's case in the URL prefix).
func TestSubmitPRHintsGitLabMixedCaseHostJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustRun(t, "git", "remote", "add", "origin", "git@GitLab.com:owner/repo.git")
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	out := captureStdout(t, func() {
		if err := runSubmit([]string{"--no-pr", "--dry-run", "--json"}); err != nil {
			t.Fatalf("submit --dry-run --json: %v", err)
		}
	})
	var got submitResult
	decodeStrictJSON(t, "submit mixed-case gitlab pr hints", out, &got)
	want := []stack.PRHint{
		{
			Head:       "feat-a",
			Base:       "main",
			CompareURL: "https://GitLab.com/owner/repo/-/compare/main...feat-a",
		},
	}
	if !reflect.DeepEqual(got.PRHints, want) {
		t.Fatalf("prHints = %+v, want %+v", got.PRHints, want)
	}
}

func TestSubmitPRHintsUnknownRemoteJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	out := captureStdout(t, func() {
		if err := runSubmit([]string{"--no-pr", "--dry-run", "--json"}); err != nil {
			t.Fatalf("submit --dry-run --json: %v", err)
		}
	})
	var got submitResult
	decodeStrictJSON(t, "submit unknown remote pr hints", out, &got)
	want := []stack.PRHint{{Head: "feat-a", Base: "main"}}
	if !reflect.DeepEqual(got.PRHints, want) {
		t.Fatalf("prHints = %+v, want %+v", got.PRHints, want)
	}
}

// TestSubmitPartialFailureJSON asserts that when a push fails partway up the
// stack, --json still reports the branches confirmed pushed plus the one the
// remote rejected, so a machine consumer can observe the partial state (the
// command still returns a non-zero error).
func TestSubmitPartialFailureJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")

	// A per-ref update hook rejects feat-b while feat-a lands in the same
	// batch (a pre-receive hook would refuse the whole push atomically).
	hook := filepath.Join(remoteDir, "hooks", "update")
	script := "#!/bin/sh\n[ \"$1\" = refs/heads/feat-b ] && exit 1\nexit 0\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}

	var runErr error
	out := captureStdout(t, func() {
		runErr = runSubmit([]string{"--json"})
	})
	if runErr == nil {
		t.Fatal("submit with a rejected branch should return a non-nil error")
	}

	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	var got submitResult
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("partial submit --json did not unmarshal into submitResult: %v\n%s", err, out)
	}
	if got.Failed != "feat-b" {
		t.Fatalf("partial result Failed = %q, want feat-b", got.Failed)
	}
	if len(got.Pushed) != 1 || got.Pushed[0] != "feat-a" {
		t.Fatalf("partial result Pushed = %v, want [feat-a]", got.Pushed)
	}
}

// TestSubmitPartialFailureJSONFirstBranchKeepsPushedArray: a pre-receive hook
// refuses the whole push atomically, so every requested ref is a confirmed
// rejection — failed names the FIRST one and pushed stays present-but-empty.
func TestSubmitPartialFailureJSONFirstBranchKeepsPushedArray(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	hook := filepath.Join(remoteDir, "hooks", "pre-receive")
	script := "#!/bin/sh\nexit 1\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}

	var runErr error
	out := captureStdout(t, func() {
		runErr = runSubmit([]string{"--json"})
	})
	if runErr == nil {
		t.Fatal("submit with a rejected first branch should return a non-nil error")
	}

	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	var got submitResult
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("partial submit --json did not unmarshal into submitResult: %v\n%s", err, out)
	}
	if got.Failed != "feat-a" {
		t.Fatalf("partial result Failed = %q, want feat-a", got.Failed)
	}
	if got.Pushed == nil || len(got.Pushed) != 0 {
		t.Fatalf("partial result Pushed = %v, want present-but-empty", got.Pushed)
	}
}

// TestSubmitReportsNonPrefixPartialPush: one batch push can update feat-a and
// feat-c while the remote rejects feat-b — a partial outcome that is NOT a
// prefix. Submit must report every confirmed outcome (pushed [a,c], failed b)
// in stack order from that single push's per-ref statuses, without retrying
// the failed batch branch-by-branch (the old fallback stopped at b and hid
// c's confirmed update).
func TestSubmitReportsNonPrefixPartialPush(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCreate(t, "feat-c", "c.txt", "c\n", "c")

	// An update hook runs once per ref: reject feat-b while feat-a and feat-c
	// land in the same batch — unlike pre-receive, which refuses atomically.
	hook := filepath.Join(remoteDir, "hooks", "update")
	script := "#!/bin/sh\n[ \"$1\" = refs/heads/feat-b ] && exit 1\nexit 0\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}

	// Count push invocations server-side: post-receive fires once per push
	// that lands at least one ref. A per-branch retry would push a, then c,
	// and bump this twice — the single batch must bump it exactly once. (A
	// PATH shim cannot intercept git on Windows; hooks run through git's own
	// sh everywhere.)
	pushLog := filepath.Join(t.TempDir(), "pushes.log")
	counter := "#!/bin/sh\necho push >> \"" + filepath.ToSlash(pushLog) + "\"\nexit 0\n"
	postReceive := filepath.Join(remoteDir, "hooks", "post-receive")
	if err := os.WriteFile(postReceive, []byte(counter), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(postReceive, 0o755); err != nil {
		t.Fatal(err)
	}

	var runErr error
	out := captureStdout(t, func() {
		runErr = runSubmit([]string{"--json"})
	})
	if runErr == nil {
		t.Fatal("submit with a rejected middle branch should return a non-nil error")
	}

	logBytes, err := os.ReadFile(pushLog)
	if err != nil {
		t.Fatalf("push log unreadable: %v", err)
	}
	if n := strings.Count(strings.TrimSpace(string(logBytes))+"\n", "\n"); n != 1 {
		t.Fatalf("git push ran %d times; want exactly 1 (no per-branch retry):\n%s", n, logBytes)
	}

	// The remote itself confirms the non-prefix outcome: a and c landed, b
	// did not.
	mustRun(t, "git", "-C", remoteDir, "rev-parse", "--verify", "-q", "refs/heads/feat-a")
	mustRun(t, "git", "-C", remoteDir, "rev-parse", "--verify", "-q", "refs/heads/feat-c")
	if err := exec.Command("git", "-C", remoteDir, "rev-parse", "--verify", "-q", "refs/heads/feat-b").Run(); err == nil {
		t.Fatal("remote refs/heads/feat-b exists — the hook-rejected ref was pushed")
	}

	var got submitResult
	decodeStrictJSON(t, "non-prefix partial submit", out, &got)
	if !reflect.DeepEqual(got.Pushed, []string{"feat-a", "feat-c"}) {
		t.Fatalf("partial result Pushed = %v, want [feat-a feat-c] in stack order", got.Pushed)
	}
	if got.Failed != "feat-b" {
		t.Fatalf("partial result Failed = %q, want feat-b", got.Failed)
	}
}

func TestSubmitUsesSelectedRemote(t *testing.T) {
	newRepo(t)
	mustInit(t)
	upstream := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", upstream)
	mustRun(t, "git", "remote", "add", "upstream", upstream)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	if err := runSubmit([]string{"--remote", "upstream"}); err != nil {
		t.Fatalf("submit --remote upstream: %v", err)
	}
	if out := mustRun(t, "git", "--git-dir", upstream, "rev-parse", "--verify", "refs/heads/feat-a"); out == "" {
		t.Fatal("feat-a was not pushed to upstream")
	}
}

func TestSubmitRejectsPositionalArgs(t *testing.T) {
	if err := runSubmit([]string{"typo", "--dry-run"}); err == nil {
		t.Fatal("submit accepted a positional argument")
	}
	if err := runSubmit([]string{"origin", "--remote"}); err == nil {
		t.Fatal("submit accepted a positional argument before a missing-value flag")
	}
}

func TestRemoteToHTTPSStripsCredentials(t *testing.T) {
	webURL, host := stack.RemoteToHTTPS("https://TOKEN@example.com/owner/repo.git?access_token=SECRET#frag")
	if webURL != "https://example.com/owner/repo" || host != "example.com" {
		t.Fatalf("credential URL converted to (%q, %q), want sanitized example.com URL", webURL, host)
	}
	if strings.Contains(webURL, "TOKEN") || strings.Contains(webURL, "SECRET") || strings.Contains(webURL, "frag") || strings.Contains(host, "TOKEN") {
		t.Fatalf("credential leaked in converted remote: %q %q", webURL, host)
	}

	webURL, host = stack.RemoteToHTTPS("git@example.com:owner/repo.git?token=SECRET#frag")
	if webURL != "https://example.com/owner/repo" || host != "example.com" {
		t.Fatalf("scp-like URL converted to (%q, %q), want sanitized example.com URL", webURL, host)
	}
	if strings.Contains(webURL, "SECRET") || strings.Contains(webURL, "token") || strings.Contains(webURL, "frag") {
		t.Fatalf("scp-like credential leaked in converted remote: %q %q", webURL, host)
	}

	webURL, host = stack.RemoteToHTTPS("ssh://user:SECRET@example.com/owner/repo.git?token=SECRET#frag")
	if webURL != "https://example.com/owner/repo" || host != "example.com" {
		t.Fatalf("credential SSH URL converted to (%q, %q), want sanitized example.com URL", webURL, host)
	}
	if strings.Contains(webURL, "SECRET") || strings.Contains(webURL, "user") || strings.Contains(webURL, "token") || strings.Contains(webURL, "frag") {
		t.Fatalf("SSH credential leaked in converted remote: %q %q", webURL, host)
	}

	webURL, host = stack.RemoteToHTTPS("ssh://git@[2001:db8::1]/owner/repo.git")
	if webURL != "https://[2001:db8::1]/owner/repo" || host != "2001:db8::1" {
		t.Fatalf("IPv6 SSH URL converted to (%q, %q), want bracketed web URL", webURL, host)
	}

	webURL, host = stack.RemoteToHTTPS("ssh://user:SECRET@example.com:2222/owner/repo.git")
	if webURL != "https://example.com:2222/owner/repo" || host != "example.com" {
		t.Fatalf("credential SSH URL with port converted to (%q, %q), want sanitized example.com:2222 URL", webURL, host)
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
		// Second-word completions: representative flags and sub-verbs from the
		// per-command generator must appear in every shell's script.
		for _, tok := range []string{"--dry-run", "--submit", "--pr", "--fix"} {
			if !strings.Contains(out, tok) {
				t.Fatalf("completion %s missing per-command token %q:\n%s", shell, tok, out)
			}
		}
	}
}

// TestCommandCompletions pins the per-command token generator directly: flags
// come from the same constructors help --json uses, sub-verbs from the
// hand-maintained map, deduped and sorted.
func TestCommandCompletions(t *testing.T) {
	toks := commandCompletions(byName["doctor"])
	want := []string{"--all", "--fix", "--json"}
	if !reflect.DeepEqual(toks, want) {
		t.Fatalf("doctor completions = %v, want %v", toks, want)
	}
	toks = commandCompletions(byName["restack"])
	want = []string{"--all", "--dry-run", "--json"}
	if !reflect.DeepEqual(toks, want) {
		t.Fatalf("restack completions = %v, want %v", toks, want)
	}
	// completion declares no flags: sub-verbs only.
	toks = commandCompletions(byName["completion"])
	want = []string{"bash", "fish", "zsh"}
	if !reflect.DeepEqual(toks, want) {
		t.Fatalf("completion completions = %v, want %v", toks, want)
	}
}

// TestSubVerbsMatchUsage pins each registration's SubVerbs field against the
// command's Usage line — the same source the dispatcher documents. A new
// sub-verb added to Usage without a SubVerbs entry (or vice versa) fails
// here. One-way limitation, stated honestly: a verb missing from BOTH the
// field and the Usage cannot be caught by any test.
func TestSubVerbsMatchUsage(t *testing.T) {
	for _, c := range registry {
		for _, v := range c.SubVerbs {
			if !strings.Contains(c.Usage, v) {
				t.Errorf("command %q lists sub-verb %q but the Usage (%q) does not mention it", c.Name, v, c.Usage)
			}
		}
	}
}

// TestSubVerbsReachHelpJSON pins the schema emission: every registered
// SubVerbs list must reach `help <name> --json` verbatim, and commands without
// sub-verbs must omit the key — the registry→JSON honesty the Usage pin
// asserts for prose.
func TestSubVerbsReachHelpJSON(t *testing.T) {
	for _, c := range registry {
		if c.Hidden {
			continue
		}
		out := captureStdout(t, func() {
			withArgs(t, []string{"help", c.Name, "--json"}, func() { _ = Execute() })
		})
		var info commandInfo
		if err := json.Unmarshal([]byte(out), &info); err != nil {
			t.Fatalf("help %s --json not parseable: %v\n%s", c.Name, err, out)
		}
		if !reflect.DeepEqual(info.SubVerbs, c.SubVerbs) {
			t.Errorf("help %s --json subVerbs = %v, want registered %v", c.Name, info.SubVerbs, c.SubVerbs)
		}
	}
}

// TestCompletionScriptsParse feeds each generated script to its real shell's
// no-execute parser when the interpreter is on PATH (skip otherwise, for
// hermeticity). The substring assertions elsewhere document content; this
// proves the emitted script is actually loadable in a user's shell.
func TestCompletionScriptsParse(t *testing.T) {
	cases := []struct {
		shell  string
		flag   string
		script string
	}{
		{"bash", "-n", bashCompletionScript()},
		{"zsh", "-n", zshCompletionScript()},
		{"fish", "--no-execute", fishCompletionScript()},
	}
	for _, tc := range cases {
		t.Run(tc.shell, func(t *testing.T) {
			path, err := exec.LookPath(tc.shell)
			if err != nil {
				t.Skipf("%s not on PATH", tc.shell)
			}
			file := filepath.Join(t.TempDir(), "st."+tc.shell)
			if err := os.WriteFile(file, []byte(tc.script), 0o644); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(path, tc.flag, file).CombinedOutput(); err != nil {
				t.Fatalf("%s rejected the generated script: %v\n%s", tc.shell, err, out)
			}
		})
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

func TestNoArgReadOnlyCommandsRejectPositionalArgs(t *testing.T) {
	tests := []struct {
		name string
		run  func([]string) error
	}{
		{"guide", runGuide},
		{"log", runLog},
		{"status", runStatus},
		{"doctor", runDoctor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run([]string{"unexpected"}); err == nil {
				t.Fatalf("%s accepted a positional argument", tt.name)
			}
		})
	}
}

func TestGuideDoesNotReferenceMissingDocs(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runGuide([]string{"--json"}); err != nil {
			t.Fatalf("guide --json: %v", err)
		}
	})
	if strings.Contains(out, "docs/AGENT.md") {
		t.Fatalf("guide references missing docs path:\n%s", out)
	}
	var payload struct {
		Docs string `json:"docs"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("guide JSON invalid: %v\n%s", err, out)
	}
	if payload.Docs == "" {
		t.Fatal("guide JSON omitted docs guidance")
	}
}

func TestJSONStackEnvUsesQuietGit(t *testing.T) {
	// JSON mode swaps the production port for its quiet variant (so rebase chatter
	// cannot corrupt the payload); both keep the cached Worktrees() override.
	jsonGit, ok := stackEnv(&stack.State{}, true).Git.(*cachedPort)
	if !ok {
		t.Fatal("JSON stack env did not use the cached port")
	}
	if _, ok := jsonGit.Git.(git.QuietShell); !ok {
		t.Fatalf("JSON cached port wraps %T, want git.QuietShell", jsonGit.Git)
	}
	plainGit, ok := stackEnv(&stack.State{}, false).Git.(*cachedPort)
	if !ok {
		t.Fatal("text stack env did not use the cached port")
	}
	if _, ok := plainGit.Git.(git.Shell); !ok {
		t.Fatalf("text cached port wraps %T, want git.Shell", plainGit.Git)
	}
}

// TestStatusJSONSurfacesConflict drives a restack into a conflict and asserts
// status --json reports the paused rebase (branch + conflicted files) instead of
// failing on the detached HEAD a rebase leaves behind.
func TestStatusJSONSurfacesConflict(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "f.txt", "A\n", "a")
	mustCreate(t, "feat-b", "f.txt", "A\nB\n", "b")
	mustCheckout(t, "feat-a")
	write(t, "f.txt", "X\n")
	if err := runModify([]string{"-a"}); err == nil {
		t.Fatal("expected a conflict restacking feat-b onto the amended feat-a")
	}

	out := captureStdout(t, func() {
		if err := runStatus([]string{"--json"}); err != nil {
			t.Fatalf("status --json during a conflict: %v", err)
		}
	})
	var st struct {
		RebaseInProgress bool     `json:"rebaseInProgress"`
		RebaseBranch     string   `json:"rebaseBranch"`
		ConflictedFiles  []string `json:"conflictedFiles"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("status --json not parseable: %v\n%s", err, out)
	}
	if !st.RebaseInProgress {
		t.Error("status should report rebaseInProgress during a conflict")
	}
	if st.RebaseBranch != "feat-b" {
		t.Errorf("rebaseBranch = %q, want feat-b", st.RebaseBranch)
	}
	if len(st.ConflictedFiles) == 0 {
		t.Error("status should list the conflicted files during a conflict")
	}
}

// TestContinueJSONAfterConflict resumes a paused restack through the JSON
// path, which routes the engine through cachedPort{git.QuietShell} — so the
// rebase runs via QuietShell.RebaseContinue and git's chatter cannot corrupt
// the payload.
func TestContinueJSONAfterConflict(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "f.txt", "A\n", "a")
	mustCreate(t, "feat-b", "f.txt", "A\nB\n", "b")
	mustCheckout(t, "feat-a")
	write(t, "f.txt", "X\n")
	if err := runModify([]string{"-a"}); err == nil {
		t.Fatal("expected a conflict restacking feat-b onto the amended feat-a")
	}

	write(t, "f.txt", "X\nB\n")
	mustRun(t, "git", "add", "f.txt")
	out := captureStdout(t, func() {
		if err := runContinue([]string{"--json"}); err != nil {
			t.Fatalf("continue --json: %v", err)
		}
	})
	var res stack.OpResult
	decodeStrictJSON(t, "continue --json", out, &res)
	if res.Summary != "continued restack" {
		t.Fatalf("continue --json summary = %q, want %q", res.Summary, "continued restack")
	}
	if len(res.Notes) != 1 || res.Notes[0] != "completed: feat-b" {
		t.Fatalf("continue --json notes = %v, want [completed: feat-b]", res.Notes)
	}
	if inProgress, _ := git.RebaseInProgress(); inProgress {
		t.Fatal("rebase still in progress after continue --json")
	}
}

// --- AGENT.md result-shape contract ----------------------------------------
//
// docs/AGENT.md is the machine interface agents script against. There is
// exit-code drift protection (TestExitCodeAndErrorCodeMapping); these two tests
// are the equivalent guard for the JSON result-shape contract — the drift that
// shipped submitResult.Failed undocumented. They check the *forward* direction:
// every field the code emits must be documented (a documented-but-unemitted key
// is allowed, e.g. an omitempty field absent on a happy path).

// agentDoc reads docs/AGENT.md relative to this source file (the cmd package
// sits one directory below the repo root), mirroring testdataDir's
// runtime.Caller trick so the lookup survives a test that has t.Chdir'd into a
// temp repo.
func agentDoc(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(file), "..", "docs", "AGENT.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// documentsKey reports whether AGENT.md documents key as a JSON key ("key") or a
// backticked code span (`key`) — the two forms the doc uses — so an incidental
// prose word does not count as documentation.
func documentsKey(doc, key string) bool {
	return strings.Contains(doc, `"`+key+`"`) || strings.Contains(doc, "`"+key+"`")
}

// TestAgentDocDocumentsExitCodes pins docs/AGENT.md's exit-code table and prose
// to errorClasses (plus the two codes defined outside it: 1=error and
// exitInternal=70). AGENT.md is the machine contract agents branch on, yet only
// st guide's copy was drift-checked before; adding or renaming a code without
// updating the doc now fails here.
func TestAgentDocDocumentsExitCodes(t *testing.T) {
	doc := agentDoc(t)
	type ec struct {
		code int
		name string
	}
	want := make([]ec, 0, len(errorClasses)+2)
	for _, c := range errorClasses {
		want = append(want, ec{c.code, c.name})
	}
	want = append(want, ec{1, "error"}, ec{exitInternal, "internal"})
	for _, w := range want {
		if cell := fmt.Sprintf("| %d |", w.code); !strings.Contains(doc, cell) {
			t.Errorf("docs/AGENT.md exit-code table missing row %q", cell)
		}
		// Bind the name TO the code; a bare "error"/"conflict" appears elsewhere
		// as prose, so match the doc's own "`name` (code" pairing instead.
		if prose := fmt.Sprintf("`%s` (%d", w.name, w.code); !strings.Contains(doc, prose) {
			t.Errorf("docs/AGENT.md does not bind error code to name: missing %q", prose)
		}
	}
}

// TestAgentDocDocumentsContractStructs pins the named, documented JSON contract
// structs to AGENT.md. Their omitempty fields (failed, branch, restacked, deleted,
// notes, dryRun, aliases, default) do not appear on a happy-path run, so reflection
// — not execution — is the only way to catch a new undocumented field. commandInfo
// and flagInfo back the `help --json` payload (AGENT.md's "Discoverability"); no
// golden captures that payload and the decode test follows any tag rename, so a
// renamed json tag (e.g. flagInfo `type`->`kind`) would otherwise drift silently.
func TestAgentDocDocumentsContractStructs(t *testing.T) {
	doc := agentDoc(t)
	for _, typ := range []reflect.Type{
		reflect.TypeOf(submitResult{}),
		reflect.TypeOf(stack.OpResult{}),
		reflect.TypeOf(initResult{}),
		reflect.TypeOf(commandInfo{}),
		reflect.TypeOf(flagInfo{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			tag := typ.Field(i).Tag.Get("json")
			if tag == "" || tag == "-" {
				continue
			}
			key := strings.Split(tag, ",")[0]
			if key == "" {
				continue
			}
			if !documentsKey(doc, key) {
				t.Errorf("docs/AGENT.md does not document %s JSON key %q", typ.Name(), key)
			}
		}
	}
}

// collectJSONKeys records every object key at any depth of a decoded JSON value.
func collectJSONKeys(v any, into map[string]bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			into[k] = true
			collectJSONKeys(val, into)
		}
	case []any:
		for _, e := range t {
			collectJSONKeys(e, into)
		}
	}
}

// TestAgentDocDocumentsEmittedKeys runs the read/navigation commands and the
// operational commands (abort/undo/repair) that emit anonymous inline structs
// (status, checkout, validate, navigation, log, abort, undo, repair) — which
// reflection cannot reach by name — and asserts every JSON key they actually
// emit is documented in AGENT.md. The fixture surfaces status's omitempty keys
// (parent, needsRestack) by running on a tracked branch with a parent and child,
// and drives a real paused rebase so abort has something to abort.
func TestAgentDocDocumentsEmittedKeys(t *testing.T) {
	doc := agentDoc(t)
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "add a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "add b")
	mustCheckout(t, "feat-a") // tracked, parent main, child feat-b

	keys := map[string]bool{}
	collect := func(label string, run func() error) {
		var perr error
		got := captureStdout(t, func() { perr = run() })
		if perr != nil {
			t.Fatalf("%s: %v", label, perr)
		}
		got = strings.TrimSpace(got)
		if got == "" {
			t.Fatalf("%s: emitted no JSON", label)
		}
		var v any
		if err := json.Unmarshal([]byte(got), &v); err != nil {
			t.Fatalf("%s: emitted invalid JSON %q: %v", label, got, err)
		}
		collectJSONKeys(v, keys)
	}

	collect("status --json", func() error { return runStatus([]string{"--json"}) })
	collect("checkout --json", func() error { return runCheckout([]string{"--json"}) })
	collect("checkout feat-a --json", func() error { return runCheckout([]string{"feat-a", "--json"}) })
	collect("doctor --json", func() error { return runDoctor([]string{"--json"}) })
	collect("log --json", func() error { return runLog([]string{"--json"}) })
	// Navigation moves HEAD; the emitted keys do not depend on the destination.
	collect("top --json", func() error { return runTop([]string{"--json"}) })
	collect("bottom --json", func() error { return runBottom([]string{"--json"}) })
	collect("up --json", func() error { return runUp([]string{"--json"}) })
	collect("down --json", func() error { return runDown([]string{"--json"}) })

	// Drive status into a paused rebase so its omitempty conflict keys
	// (rebaseInProgress/rebaseBranch/conflictedFiles) are emitted and checked too.
	newRepo(t)
	mustInit(t)
	mustCreate(t, "conf-a", "f.txt", "A\n", "a")
	mustCreate(t, "conf-b", "f.txt", "A\nB\n", "b")
	mustCheckout(t, "conf-a")
	write(t, "f.txt", "X\n")
	if err := runModify([]string{"-a"}); err == nil {
		t.Fatal("expected a conflict restacking conf-b onto the amended conf-a")
	}
	collect("status --json (conflict)", func() error { return runStatus([]string{"--json"}) })
	// The paused rebase is still in progress; aborting it surfaces abort's keys.
	collect("abort --json", func() error { return runAbort([]string{"--json"}) })

	// repair on a consistent stack and undo of the last mutation surface the
	// remaining operational keys (repaired/fixes, undone/label/restored).
	newRepo(t)
	mustInit(t)
	mustCreate(t, "op-a", "a.txt", "a\n", "add a")
	collect("doctor --fix --json", func() error { return runDoctor([]string{"--fix", "--json"}) })
	collect("undo --json", func() error { return runUndo([]string{"--json"}) })

	for k := range keys {
		if !documentsKey(doc, k) {
			t.Errorf("docs/AGENT.md does not document emitted JSON key %q", k)
		}
	}
}

// TestSubmitPRHintsGitHubEnterpriseJSON: a GHE host gets github.com's
// /compare/ shape.
func TestSubmitPRHintsGitHubEnterpriseJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustRun(t, "git", "remote", "add", "origin", "git@github.example.com:owner/repo.git")
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	out := captureStdout(t, func() {
		if err := runSubmit([]string{"--no-pr", "--dry-run", "--json"}); err != nil {
			t.Fatalf("submit --dry-run --json: %v", err)
		}
	})
	var got submitResult
	decodeStrictJSON(t, "submit ghe pr hints", out, &got)
	want := []stack.PRHint{{
		Head:       "feat-a",
		Base:       "main",
		CompareURL: "https://github.example.com/owner/repo/compare/main...feat-a",
	}}
	if !reflect.DeepEqual(got.PRHints, want) {
		t.Fatalf("prHints = %+v, want %+v", got.PRHints, want)
	}
}

// TestSubmitPRHintsLookalikeHostStaysUnknown pins the label-matching boundary:
// a host merely CONTAINING "gitlab" is not classified, so no URL is guessed.
func TestSubmitPRHintsLookalikeHostStaysUnknown(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustRun(t, "git", "remote", "add", "origin", "git@mygitlab.example.com:owner/repo.git")
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	out := captureStdout(t, func() {
		if err := runSubmit([]string{"--no-pr", "--dry-run", "--json"}); err != nil {
			t.Fatalf("submit --dry-run --json: %v", err)
		}
	})
	var got submitResult
	decodeStrictJSON(t, "submit lookalike host pr hints", out, &got)
	want := []stack.PRHint{{Head: "feat-a", Base: "main"}}
	if !reflect.DeepEqual(got.PRHints, want) {
		t.Fatalf("prHints = %+v, want no compare URL: %+v", got.PRHints, want)
	}
}

// --- dry-run parity (modify / untrack / rename / create) --------------------

// TestModifyDryRunJSON pins the no-mutation contract: the result reports the
// would-be amend plus its upstack cascade, while neither HEAD, the tip, nor
// the recorded state moves.
func TestModifyDryRunJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCheckout(t, "feat-a")
	tip := mustRun(t, "git", "rev-parse", "feat-a")

	type dryRunJSON struct {
		Summary   string   `json:"summary"`
		Branch    string   `json:"branch"`
		Restacked []string `json:"restacked"`
		Notes     []string `json:"notes"`
		DryRun    bool     `json:"dryRun"`
	}
	out := captureStdout(t, func() {
		if err := runModify([]string{"--dry-run", "--json"}); err != nil {
			t.Fatalf("modify --dry-run --json: %v", err)
		}
	})
	var got dryRunJSON
	decodeStrictJSON(t, "modify --dry-run --json", out, &got)
	if !got.DryRun || got.Branch != "feat-a" || got.Summary != "would amend feat-a" {
		t.Fatalf("modify --dry-run payload = %+v", got)
	}
	if want := []string{"feat-b"}; !reflect.DeepEqual(got.Restacked, want) {
		t.Fatalf("modify --dry-run restacked = %v, want %v", got.Restacked, want)
	}
	if after := mustRun(t, "git", "rev-parse", "feat-a"); after != tip {
		t.Fatalf("modify --dry-run moved feat-a: %s -> %s", tip, after)
	}
}

func TestModifyDryRunCommitIsHonest(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	before := mustRun(t, "git", "rev-list", "--count", "feat-a")
	write(t, "a2.txt", "a2\n")
	mustRun(t, "git", "add", "a2.txt")

	out := captureStdout(t, func() {
		if err := runModify([]string{"--commit", "-m", "wip", "--dry-run", "--json"}); err != nil {
			t.Fatalf("modify --commit --dry-run --json: %v", err)
		}
	})
	var got struct {
		Summary string `json:"summary"`
		Branch  string `json:"branch"`
		DryRun  bool   `json:"dryRun"`
	}
	decodeStrictJSON(t, "modify --commit --dry-run --json", out, &got)
	if got.Summary != "would commit on feat-a" || got.Branch != "feat-a" || !got.DryRun {
		t.Fatalf("modify --commit --dry-run payload = %+v", got)
	}
	if after := mustRun(t, "git", "rev-list", "--count", "feat-a"); after != before {
		t.Fatalf("modify --commit --dry-run changed feat-a's history: %s -> %s commits", before, after)
	}
}

func TestUntrackDryRunJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCheckout(t, "feat-a")

	out := captureStdout(t, func() {
		if err := runUntrack([]string{"--dry-run", "--json"}); err != nil {
			t.Fatalf("untrack --dry-run --json: %v", err)
		}
	})
	var got struct {
		Summary string   `json:"summary"`
		Branch  string   `json:"branch"`
		Notes   []string `json:"notes"`
		DryRun  bool     `json:"dryRun"`
	}
	decodeStrictJSON(t, "untrack --dry-run --json", out, &got)
	if !got.DryRun || got.Branch != "feat-a" {
		t.Fatalf("untrack --dry-run payload = %+v", got)
	}
	wantNotes := []string{"child feat-b would re-parent onto main"}
	if !reflect.DeepEqual(got.Notes, wantNotes) {
		t.Fatalf("untrack --dry-run notes = %v, want %v", got.Notes, wantNotes)
	}
	if !stateT(t).IsTracked("feat-a") {
		t.Fatal("untrack --dry-run dropped feat-a from state")
	}
}

func TestRenameDryRunJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCheckout(t, "feat-a")

	out := captureStdout(t, func() {
		if err := runRename([]string{"feat-a", "feat-renamed", "--dry-run", "--json"}); err != nil {
			t.Fatalf("rename --dry-run --json: %v", err)
		}
	})
	var got struct {
		Summary string   `json:"summary"`
		Branch  string   `json:"branch"`
		Notes   []string `json:"notes"`
		DryRun  bool     `json:"dryRun"`
	}
	decodeStrictJSON(t, "rename --dry-run --json", out, &got)
	if !got.DryRun || got.Branch != "feat-renamed" || got.Summary != "would rename feat-a -> feat-renamed" {
		t.Fatalf("rename --dry-run payload = %+v", got)
	}
	wantNotes := []string{"child feat-b would re-parent onto feat-renamed"}
	if !reflect.DeepEqual(got.Notes, wantNotes) {
		t.Fatalf("rename --dry-run notes = %v, want %v", got.Notes, wantNotes)
	}
	mustRun(t, "git", "rev-parse", "--verify", "feat-a")
	if !stateT(t).IsTracked("feat-a") || stateT(t).IsTracked("feat-renamed") {
		t.Fatal("rename --dry-run changed the recorded stack")
	}
}

func TestCreateDryRunJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	out := captureStdout(t, func() {
		if err := runCreate([]string{"feat-b", "--dry-run", "--json"}); err != nil {
			t.Fatalf("create --dry-run --json: %v", err)
		}
	})
	var got struct {
		Summary string `json:"summary"`
		Branch  string `json:"branch"`
		DryRun  bool   `json:"dryRun"`
	}
	decodeStrictJSON(t, "create --dry-run --json", out, &got)
	if !got.DryRun || got.Branch != "feat-b" || got.Summary != "would create feat-b on top of feat-a" {
		t.Fatalf("create --dry-run payload = %+v", got)
	}
	if err := exec.Command("git", "rev-parse", "--verify", "-q", "feat-b").Run(); err == nil {
		t.Fatal("create --dry-run created feat-b")
	}
	if stateT(t).IsTracked("feat-b") {
		t.Fatal("create --dry-run tracked feat-b")
	}
}

func TestSubmitScopeFlagsAndCompareURLTextFiltering(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	// Set origin's fetch URL to a GitHub-shaped URL and its push URL to the local
	// bare repo so PushBranches executes against a real remote while RemoteToHTTPS
	// still builds compare URLs.
	const ghURL = "https://github.com/acme/widgets.git"
	mustRun(t, "git", "remote", "add", "origin", ghURL)
	mustRun(t, "git", "remote", "set-url", "--push", "origin", remoteDir)

	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCheckout(t, "main")
	mustCreate(t, "other", "o.txt", "o\n", "o")
	mustCheckout(t, "feat-a")

	// --all and --upto are mutually exclusive.
	if err := runSubmit([]string{"--all", "--upto"}); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("submit --all --upto = %v, want mutual exclusion error", err)
	}

	// Default from bottom of stack pushes the full current stack [feat-a, feat-b],
	// excluding sibling stack [other].
	type submitJSON struct {
		Pushed  []string       `json:"pushed"`
		PRHints []stack.PRHint `json:"prHints"`
	}
	dryAll := captureStdout(t, func() {
		if err := runSubmit([]string{"--no-pr", "--dry-run", "--json"}); err != nil {
			t.Fatalf("submit --dry-run --json: %v", err)
		}
	})
	var gotFull submitJSON
	if err := json.Unmarshal([]byte(dryAll), &gotFull); err != nil {
		t.Fatalf("unmarshal dry-run: %v\n%s", err, dryAll)
	}
	if want := []string{"feat-a", "feat-b"}; !reflect.DeepEqual(gotFull.Pushed, want) {
		t.Fatalf("default submit from feat-a pushed = %v, want %v", gotFull.Pushed, want)
	}

	// --upto stops at the current branch [feat-a].
	dryUpto := captureStdout(t, func() {
		if err := runSubmit([]string{"--no-pr", "--upto", "--dry-run", "--json"}); err != nil {
			t.Fatalf("submit --upto --dry-run --json: %v", err)
		}
	})
	var gotUpto submitJSON
	if err := json.Unmarshal([]byte(dryUpto), &gotUpto); err != nil {
		t.Fatalf("unmarshal --upto dry-run: %v\n%s", err, dryUpto)
	}
	if want := []string{"feat-a"}; !reflect.DeepEqual(gotUpto.Pushed, want) {
		t.Fatalf("submit --upto pushed = %v, want %v", gotUpto.Pushed, want)
	}

	// Real first push of feat-a creates the remote branch -> text prints compare URL.
	firstText := captureStdout(t, func() {
		if err := runSubmit([]string{"--no-pr", "--upto"}); err != nil {
			t.Fatalf("first submit --upto: %v", err)
		}
	})
	if !strings.Contains(firstText, "open pull requests on your host:") || !strings.Contains(firstText, "compare/main...feat-a") {
		t.Fatalf("first submit text should print compare URL for newly created branch:\n%s", firstText)
	}

	// Second push of full stack: feat-a is updated/up-to-date, feat-b is newly created.
	// Text mode prints compare URL ONLY for feat-b (not feat-a).
	secondText := captureStdout(t, func() {
		if err := runSubmit([]string{"--no-pr"}); err != nil {
			t.Fatalf("second submit: %v", err)
		}
	})
	if strings.Contains(secondText, "compare/main...feat-a") {
		t.Fatalf("second submit text should omit compare URL for already-existing feat-a:\n%s", secondText)
	}
	if !strings.Contains(secondText, "compare/feat-a...feat-b") {
		t.Fatalf("second submit text should print compare URL for newly created feat-b:\n%s", secondText)
	}

	// Third push (all already exist): text omits the compare URL block entirely,
	// while --json still emits full prHints for both branches.
	write(t, "a.txt", "a2\n")
	if err := runModify([]string{"-a"}); err != nil {
		t.Fatalf("modify feat-a: %v", err)
	}
	repushText := captureStdout(t, func() {
		if err := runSubmit([]string{"--no-pr"}); err != nil {
			t.Fatalf("re-submit text: %v", err)
		}
	})
	if strings.Contains(repushText, "open pull requests on your host:") || strings.Contains(repushText, "compare/") {
		t.Fatalf("re-submit text should omit compare URLs when no branch was newly created:\n%s", repushText)
	}
	repushJSON := captureStdout(t, func() {
		if err := runSubmit([]string{"--no-pr", "--json"}); err != nil {
			t.Fatalf("re-submit --json: %v", err)
		}
	})
	var gotJSON submitJSON
	if err := json.Unmarshal([]byte(repushJSON), &gotJSON); err != nil {
		t.Fatalf("unmarshal re-submit --json: %v\n%s", err, repushJSON)
	}
	if len(gotJSON.PRHints) != 2 {
		t.Fatalf("--json prHints len = %d, want 2: %+v", len(gotJSON.PRHints), gotJSON.PRHints)
	}
}

func TestDeleteReparentedRenderingTextAndJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")

	dryText := captureStdout(t, func() {
		if err := runDelete([]string{"feat-a", "-f", "--dry-run"}); err != nil {
			t.Fatalf("delete --dry-run: %v", err)
		}
	})
	if !strings.Contains(dryText, "would re-parent: feat-b -> main") {
		t.Fatalf("delete --dry-run text missing would re-parent:\n%s", dryText)
	}

	liveJSON := captureStdout(t, func() {
		if err := runDelete([]string{"feat-a", "-f", "--json"}); err != nil {
			t.Fatalf("delete --json: %v", err)
		}
	})
	var res stack.OpResult
	if err := json.Unmarshal([]byte(liveJSON), &res); err != nil {
		t.Fatalf("unmarshal delete --json: %v\n%s", err, liveJSON)
	}
	want := []stack.Reparent{{Branch: "feat-b", OldParent: "feat-a", NewParent: "main"}}
	if !reflect.DeepEqual(res.Reparented, want) {
		t.Fatalf("delete --json reparented = %+v, want %+v", res.Reparented, want)
	}
}
