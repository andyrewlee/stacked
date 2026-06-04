// Package e2e is a black-box end-to-end test suite for the "st" binary. Unlike
// the white-box tests in package cmd (which call run* functions in-process),
// these tests build the real binary once and drive it as a subprocess the way a
// user would, asserting on real exit codes, stdout/stderr, JSON output shape,
// and the resulting git repository state.
//
// The suite is hermetic: every invocation runs with HOME pointed at a temp dir
// and GIT_CONFIG_GLOBAL/GIT_CONFIG_SYSTEM pointed at /dev/null so it never reads
// the developer's real git configuration, and committer/author identity is
// injected via the environment so commits work without any config at all.
package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stBin is the absolute path to the binary built by TestMain.
var stBin string

// TestMain builds the st binary once into a temp dir and runs the suite. The
// build runs from the repository root (two directories up from this test file's
// package, i.e. the module root) so it picks up the real cmd/st main package.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "st-e2e-bin")
	if err != nil {
		panic("mkdir temp bin dir: " + err.Error())
	}

	bin := filepath.Join(tmp, "st")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}

	repoRoot, err := moduleRoot()
	if err != nil {
		os.RemoveAll(tmp)
		panic("find module root: " + err.Error())
	}

	buildArgs := []string{"build", "-o", bin, "./cmd/st"}
	if os.Getenv("GOCOVERDIR") != "" {
		// Coverage-instrument the binary so e2e runs contribute to coverage.
		// Use atomic counter mode so the data merges with the race-instrumented
		// in-process coverage (which is always atomic).
		buildArgs = []string{"build", "-cover", "-covermode=atomic", "-o", bin, "./cmd/st"}
	}
	build := exec.Command("go", buildArgs...)
	build.Dir = repoRoot
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		os.RemoveAll(tmp)
		panic("go build ./cmd/st failed: " + err.Error() + "\n" + string(out))
	}
	stBin = bin

	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

// moduleRoot walks up from this source file's directory until it finds go.mod,
// returning the directory that contains it (the module root).
func moduleRoot() (string, error) {
	_, file, _, ok := runtimeCaller()
	if !ok {
		// Fall back to the working directory (go test runs in the package dir).
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return ascendToGoMod(wd)
	}
	return ascendToGoMod(filepath.Dir(file))
}

// runtimeCaller is a tiny indirection so moduleRoot stays testable/simple.
func runtimeCaller() (uintptr, string, int, bool) {
	return runtime.Caller(0)
}

func ascendToGoMod(start string) (string, error) {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the filesystem root without finding go.mod; fall back to
			// the starting directory so the build still has a chance to run.
			return start, os.ErrNotExist
		}
		dir = parent
	}
}

// --- subprocess harness ----------------------------------------------------

// result captures the outcome of running the st binary.
type result struct {
	stdout   string
	stderr   string
	exitCode int
}

// cleanEnv returns a minimal, hermetic environment for running st (and git):
// PATH is preserved so the git executable is found, HOME points at home so no
// real user config is read, git's global/system config are disabled, and a
// fixed committer/author identity is supplied so commits never depend on config.
func cleanEnv(home string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_AUTHOR_NAME=stacked test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=stacked test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		// Keep git deterministic and non-interactive regardless of the host.
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"GIT_EDITOR=true",
	}
	// Preserve the Go toolchain location for the one-time build path; harmless
	// otherwise.
	if v := os.Getenv("GOPATH"); v != "" {
		env = append(env, "GOPATH="+v)
	}
	if v := os.Getenv("GOCACHE"); v != "" {
		env = append(env, "GOCACHE="+v)
	}
	// Forward GOCOVERDIR so a coverage-instrumented st binary emits its coverage
	// data, letting the e2e suite contribute to the overall coverage number.
	if v := os.Getenv("GOCOVERDIR"); v != "" {
		env = append(env, "GOCOVERDIR="+v)
	}
	return env
}

// repo is a hermetic scenario: a temp working tree plus its own temp HOME.
type repo struct {
	t    *testing.T
	dir  string // working tree
	home string // isolated HOME for this repo
}

// newRepo creates a fresh git repository on branch main with one initial commit
// and returns a handle for driving st and git against it.
func newRepo(t *testing.T) *repo {
	t.Helper()
	base := t.TempDir()
	r := &repo{
		t:    t,
		dir:  filepath.Join(base, "repo"),
		home: filepath.Join(base, "home"),
	}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := os.MkdirAll(r.home, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	r.git("init", "-q", "-b", "main")
	r.writeFile("base.txt", "base\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "init")
	return r
}

// st runs the st binary with the given args in the repo's working tree and
// returns the captured result. It never fails the test on a non-zero exit;
// callers assert on the exit code explicitly.
func (r *repo) st(args ...string) result {
	r.t.Helper()
	cmd := exec.Command(stBin, args...)
	cmd.Dir = r.dir
	cmd.Env = cleanEnv(r.home)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if ee, ok := err.(*exec.ExitError); ok {
			exitErr = ee
			code = exitErr.ExitCode()
		} else {
			r.t.Fatalf("running st %v: %v", args, err)
		}
	}
	return result{stdout: stdout.String(), stderr: stderr.String(), exitCode: code}
}

// stOK runs st and fails the test if the command exits non-zero.
func (r *repo) stOK(args ...string) result {
	r.t.Helper()
	res := r.st(args...)
	if res.exitCode != 0 {
		r.t.Fatalf("st %v exited %d\nstdout:\n%s\nstderr:\n%s",
			args, res.exitCode, res.stdout, res.stderr)
	}
	return res
}

// git runs a git command in the working tree with the same hermetic env and
// returns trimmed combined output; it fails the test on error.
func (r *repo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = cleanEnv(r.home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitIn runs a git command in an arbitrary directory (e.g. a bare remote) with
// the hermetic env and returns trimmed combined output.
func (r *repo) gitIn(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = cleanEnv(r.home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git -C %s %v failed: %v\n%s", dir, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *repo) writeFile(name, content string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, name), []byte(content), 0o644); err != nil {
		r.t.Fatalf("write %s: %v", name, err)
	}
}

// initStack runs `st init --trunk main`.
func (r *repo) initStack() {
	r.t.Helper()
	r.stOK("init", "--trunk", "main")
}

// create writes file=content and creates a tracked branch committing it via
// `st create <name> -a -m <msg>`.
func (r *repo) create(name, file, content, msg string) {
	r.t.Helper()
	r.writeFile(file, content)
	r.stOK("create", name, "-a", "-m", msg)
}

func (r *repo) currentBranch() string {
	r.t.Helper()
	return r.git("rev-parse", "--abbrev-ref", "HEAD")
}

// branchExists reports whether a local git branch exists.
func (r *repo) branchExists(name string) bool {
	r.t.Helper()
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	cmd.Dir = r.dir
	cmd.Env = cleanEnv(r.home)
	return cmd.Run() == nil
}

// fileOnBranch reports whether the given path exists in the tree of branch.
func (r *repo) fileOnBranch(branch, file string) bool {
	r.t.Helper()
	cmd := exec.Command("git", "cat-file", "-e", branch+":"+file)
	cmd.Dir = r.dir
	cmd.Env = cleanEnv(r.home)
	return cmd.Run() == nil
}

// rev returns the full commit SHA for a ref.
func (r *repo) rev(ref string) string {
	r.t.Helper()
	return r.git("rev-parse", ref)
}

// --- assertion helpers -----------------------------------------------------

func wantExit(t *testing.T, res result, code int) {
	t.Helper()
	if res.exitCode != code {
		t.Fatalf("exit code = %d, want %d\nstdout:\n%s\nstderr:\n%s",
			res.exitCode, code, res.stdout, res.stderr)
	}
}

func wantStdoutContains(t *testing.T, res result, sub string) {
	t.Helper()
	if !strings.Contains(res.stdout, sub) {
		t.Fatalf("stdout does not contain %q\nstdout:\n%s", sub, res.stdout)
	}
}

func wantStderrContains(t *testing.T, res result, sub string) {
	t.Helper()
	if !strings.Contains(res.stderr, sub) {
		t.Fatalf("stderr does not contain %q\nstderr:\n%s", sub, res.stderr)
	}
}

// --- JSON shapes (mirror cmd/log.go and cmd/status.go) ----------------------

type logNode struct {
	Name         string     `json:"name"`
	Parent       string     `json:"parent"`
	ParentSHA    string     `json:"parentSHA"`
	Current      bool       `json:"current"`
	NeedsRestack bool       `json:"needsRestack"`
	TopCommit    string     `json:"topCommit"`
	Children     []*logNode `json:"children"`
}

type statusJSON struct {
	Branch        string   `json:"branch"`
	Role          string   `json:"role"`
	Parent        string   `json:"parent"`
	Children      []string `json:"children"`
	NeedsRestack  *bool    `json:"needsRestack"`
	WorktreeClean bool     `json:"worktreeClean"`
}

// findNode returns the first node in the tree (depth-first) with the given name.
func findNode(root *logNode, name string) *logNode {
	if root == nil {
		return nil
	}
	if root.Name == name {
		return root
	}
	for _, c := range root.Children {
		if n := findNode(c, name); n != nil {
			return n
		}
	}
	return nil
}

// =====================================================================
// Tests
// =====================================================================

// TestVersion asserts `st version` prints build info and exits 0, and that the
// -v/--version aliases behave the same.
func TestVersion(t *testing.T) {
	r := newRepo(t)
	for _, arg := range []string{"version", "-v", "--version"} {
		res := r.st(arg)
		wantExit(t, res, 0)
		wantStdoutContains(t, res, "st 0.1.0")
		// debug.ReadBuildInfo embeds the Go version line in all build modes.
		wantStdoutContains(t, res, "go:")
	}
}

// TestHelpListsAllCommands asserts the top-level help (no args, help, -h, --help)
// exits 0 and lists every documented subcommand plus the help/version pseudo
// commands.
func TestHelpListsAllCommands(t *testing.T) {
	r := newRepo(t)

	commands := []string{
		"init", "create", "checkout", "up", "down", "top", "bottom", "log",
		"status", "track", "untrack", "modify", "restack", "continue", "abort",
		"fold", "squash", "onto", "rename", "delete", "sync", "submit", "undo",
		"validate", "repair", "completion", "help", "version",
	}

	for _, form := range [][]string{nil, {"help"}, {"-h"}, {"--help"}} {
		res := r.st(form...)
		wantExit(t, res, 0)
		wantStdoutContains(t, res, "st - manage stacked diffs on top of git")
		for _, c := range commands {
			if !strings.Contains(res.stdout, c) {
				t.Fatalf("help (%v) missing command %q\nstdout:\n%s", form, c, res.stdout)
			}
		}
	}
}

// TestUnknownCommand asserts an unrecognized command exits 1 and writes the
// "unknown command" diagnostic to stderr while still printing help.
func TestUnknownCommand(t *testing.T) {
	r := newRepo(t)
	res := r.st("definitely-not-a-command")
	wantExit(t, res, 1)
	wantStderrContains(t, res, `st: unknown command "definitely-not-a-command"`)
}

// TestLifecycle exercises the core journey end to end: init, create two stacked
// branches, inspect via log and status (text + JSON), navigate up/down/top/
// bottom, modify the bottom branch and confirm the upstack restacks.
func TestLifecycle(t *testing.T) {
	r := newRepo(t)

	// init
	res := r.stOK("init", "--trunk", "main")
	wantStdoutContains(t, res, "initialized stacked (trunk: main)")

	// re-init is idempotent and reports the existing trunk.
	res = r.stOK("init")
	wantStdoutContains(t, res, "already initialized")

	// create two stacked branches.
	r.writeFile("a.txt", "a\n")
	res = r.stOK("create", "feat-a", "-a", "-m", "a")
	wantStdoutContains(t, res, "Created feat-a on top of main")
	r.writeFile("b.txt", "b\n")
	res = r.stOK("create", "feat-b", "-a", "-m", "b")
	wantStdoutContains(t, res, "Created feat-b on top of feat-a")

	if got := r.currentBranch(); got != "feat-b" {
		t.Fatalf("after creates, on %q, want feat-b", got)
	}

	// log text shows the tree with the trunk at the bottom.
	res = r.stOK("log")
	for _, sub := range []string{"feat-a", "feat-b", "main"} {
		wantStdoutContains(t, res, sub)
	}

	// log --json: parse and verify the tree shape.
	res = r.stOK("log", "--json")
	var root logNode
	if err := json.Unmarshal([]byte(res.stdout), &root); err != nil {
		t.Fatalf("log --json is not valid JSON: %v\n%s", err, res.stdout)
	}
	if root.Name != "main" {
		t.Fatalf("log --json root = %q, want main", root.Name)
	}
	a := findNode(&root, "feat-a")
	b := findNode(&root, "feat-b")
	if a == nil || a.Parent != "main" {
		t.Fatalf("feat-a node wrong: %+v", a)
	}
	if b == nil || b.Parent != "feat-a" {
		t.Fatalf("feat-b node wrong: %+v", b)
	}
	if !b.Current {
		t.Fatalf("feat-b should be marked current in log --json")
	}
	if a.TopCommit != "a" {
		t.Fatalf("feat-a topCommit = %q, want a", a.TopCommit)
	}

	// status text + JSON for the current (feat-b) branch.
	res = r.stOK("status")
	wantStdoutContains(t, res, "branch:   feat-b")
	wantStdoutContains(t, res, "parent:   feat-a")

	res = r.stOK("status", "--json")
	var sj statusJSON
	if err := json.Unmarshal([]byte(res.stdout), &sj); err != nil {
		t.Fatalf("status --json invalid: %v\n%s", err, res.stdout)
	}
	if sj.Branch != "feat-b" || sj.Role != "tracked" || sj.Parent != "feat-a" {
		t.Fatalf("status JSON unexpected: %+v", sj)
	}
	if sj.NeedsRestack == nil || *sj.NeedsRestack {
		t.Fatalf("feat-b should not need restack; got %+v", sj.NeedsRestack)
	}
	if !sj.WorktreeClean {
		t.Fatalf("worktree should be clean: %+v", sj)
	}

	// trunk-only status JSON (role=trunk, children listed, needsRestack omitted).
	r.stOK("checkout", "main")
	res = r.stOK("status", "--json")
	var trunkStatus statusJSON
	if err := json.Unmarshal([]byte(res.stdout), &trunkStatus); err != nil {
		t.Fatalf("trunk status --json invalid: %v\n%s", err, res.stdout)
	}
	if trunkStatus.Role != "trunk" {
		t.Fatalf("main role = %q, want trunk", trunkStatus.Role)
	}
	if trunkStatus.NeedsRestack != nil {
		t.Fatalf("trunk needsRestack should be omitted, got %v", *trunkStatus.NeedsRestack)
	}
	if len(trunkStatus.Children) != 1 || trunkStatus.Children[0] != "feat-a" {
		t.Fatalf("trunk children = %v, want [feat-a]", trunkStatus.Children)
	}

	// Navigation: from main go up to feat-a, up to feat-b (top), back down.
	r.stOK("checkout", "feat-a")
	res = r.stOK("up")
	wantStdoutContains(t, res, "switched to feat-b")
	if r.currentBranch() != "feat-b" {
		t.Fatalf("up did not land on feat-b")
	}

	res = r.stOK("down")
	wantStdoutContains(t, res, "feat-a")
	if r.currentBranch() != "feat-a" {
		t.Fatalf("down did not land on feat-a")
	}

	res = r.stOK("top")
	wantStdoutContains(t, res, "feat-b")
	if r.currentBranch() != "feat-b" {
		t.Fatalf("top did not land on feat-b")
	}

	res = r.stOK("bottom")
	wantStdoutContains(t, res, "feat-a")
	if r.currentBranch() != "feat-a" {
		t.Fatalf("bottom did not land on feat-a")
	}

	// modify the bottom branch (feat-a) and confirm feat-b is restacked onto the
	// amended commit, with an independent file so no conflict occurs.
	r.writeFile("a.txt", "a-modified\n")
	res = r.stOK("modify", "-a")
	wantStdoutContains(t, res, "Amended feat-a")
	wantStdoutContains(t, res, "feat-b")

	if got := r.git("show", "feat-b:a.txt"); got != "a-modified" {
		t.Fatalf("feat-b:a.txt = %q, want a-modified (restacked)", got)
	}

	// restack again is a no-op now.
	r.stOK("checkout", "feat-a")
	res = r.stOK("restack")
	wantStdoutContains(t, res, "everything up to date")

	// validate reports a healthy stack and exits 0.
	res = r.stOK("validate")
	wantStdoutContains(t, res, "no problems found")
}

// TestNavigationEdges covers the ambiguous-up fork, top branch-point error, the
// "already at the top/bottom/trunk" notices, and invalid count arguments.
func TestNavigationEdges(t *testing.T) {
	r := newRepo(t)
	r.initStack()
	// Build a fork: feat-a has two children feat-b1 and feat-b2.
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b1", "b1.txt", "b1\n", "b1")
	r.stOK("checkout", "feat-a")
	r.create("feat-b2", "b2.txt", "b2\n", "b2")

	// up from the fork point is ambiguous.
	r.stOK("checkout", "feat-a")
	res := r.stOK("up")
	wantStdoutContains(t, res, "multiple children of feat-a")
	wantStdoutContains(t, res, "feat-b1")
	wantStdoutContains(t, res, "feat-b2")
	if r.currentBranch() != "feat-a" {
		t.Fatalf("ambiguous up should not move past the fork; on %q", r.currentBranch())
	}

	// top from the fork point is a branch-point error (exit 1).
	res = r.st("top")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "branch point")

	// up at a leaf says "already at the top".
	r.stOK("checkout", "feat-b1")
	res = r.stOK("up")
	wantStdoutContains(t, res, "already at the top of the stack")

	// down at trunk notice.
	r.stOK("checkout", "main")
	res = r.stOK("down")
	wantStdoutContains(t, res, "already at trunk")

	// bottom at trunk notice.
	res = r.stOK("bottom")
	wantStdoutContains(t, res, "at trunk")

	// invalid / non-positive counts are rejected with exit 1.
	r.stOK("checkout", "feat-b1")
	res = r.st("up", "abc")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "invalid level count")

	res = r.st("down", "0")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "at least 1")
}

// TestConflictContinue drives a real merge conflict via modify and resolves it
// with `st continue`, asserting the upstack ends up rebased onto the new tip.
func TestConflictContinue(t *testing.T) {
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")
	// feat-b edits the same file/line so amending feat-a conflicts on restack.
	r.create("feat-b", "f.txt", "A\nB\n", "b")
	r.stOK("checkout", "feat-a")

	r.writeFile("f.txt", "X\n")
	res := r.st("modify", "-a")
	// A conflict maps to the dedicated exit code 2 (see docs/AGENT.md).
	wantExit(t, res, 2)
	wantStderrContains(t, res, "st continue")

	// A real rebase should be in progress.
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("expected a rebase in progress after conflict: %v", err)
	}

	// Resolve and continue.
	r.writeFile("f.txt", "X\nB\n")
	r.git("add", "f.txt")
	res = r.stOK("continue")
	wantStdoutContains(t, res, "continued restack")

	if got := r.git("show", "feat-b:f.txt"); got != "X\nB" {
		t.Fatalf("feat-b:f.txt = %q, want X\\nB", got)
	}
	r.stOK("validate")
}

// TestConflictAbort drives the same conflict but backs out with `st abort`,
// asserting the rebase is gone and a second abort errors with "no rebase".
func TestConflictAbort(t *testing.T) {
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")
	r.create("feat-b", "f.txt", "A\nB\n", "b")
	r.stOK("checkout", "feat-a")

	r.writeFile("f.txt", "X\n")
	res := r.st("modify", "-a")
	wantExit(t, res, 2) // conflict

	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("expected a rebase in progress: %v", err)
	}

	res = r.stOK("abort")
	wantStdoutContains(t, res, "Aborted the in-progress rebase")
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); !os.IsNotExist(err) {
		t.Fatalf("rebase should be gone after abort, stat err = %v", err)
	}

	// A second abort with nothing in progress errors.
	res = r.st("abort")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "no rebase in progress")
}

// TestFold folds the top branch into its parent: the parent absorbs the commits
// and the folded branch is removed.
func TestFold(t *testing.T) {
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.stOK("checkout", "feat-b")

	res := r.stOK("fold")
	wantStdoutContains(t, res, "Folded feat-b into feat-a")
	if r.branchExists("feat-b") {
		t.Fatalf("feat-b git branch should be gone after fold")
	}
	if !r.fileOnBranch("feat-a", "b.txt") {
		t.Fatalf("feat-a should contain b.txt after fold")
	}
}

// TestSquash collapses multiple commits on a branch into one.
func TestSquash(t *testing.T) {
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	// Add a second commit via modify --commit.
	r.writeFile("a2.txt", "a2\n")
	r.stOK("modify", "--commit", "-m", "a2")

	if n := r.git("rev-list", "--count", "main..feat-a"); n != "2" {
		t.Fatalf("expected 2 commits before squash, got %s", n)
	}
	res := r.stOK("squash", "-m", "squashed")
	wantStdoutContains(t, res, "Squashed")
	if n := r.git("rev-list", "--count", "main..feat-a"); n != "1" {
		t.Fatalf("expected 1 commit after squash, got %s", n)
	}
	for _, f := range []string{"a.txt", "a2.txt"} {
		if !r.fileOnBranch("feat-a", f) {
			t.Fatalf("feat-a missing %s after squash", f)
		}
	}
}

// TestOnto re-parents a branch onto the trunk, dropping the old parent's commits.
func TestOnto(t *testing.T) {
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b") // on feat-a
	r.stOK("checkout", "feat-b")

	res := r.stOK("onto", "main")
	wantStdoutContains(t, res, "Moved feat-b onto main")
	if r.fileOnBranch("feat-b", "a.txt") {
		t.Fatalf("feat-b should no longer contain a.txt after moving onto main")
	}

	// status JSON should now report feat-b's parent as main.
	r.stOK("checkout", "feat-b")
	res = r.stOK("status", "--json")
	var sj statusJSON
	if err := json.Unmarshal([]byte(res.stdout), &sj); err != nil {
		t.Fatalf("status --json invalid: %v\n%s", err, res.stdout)
	}
	if sj.Parent != "main" {
		t.Fatalf("feat-b parent after onto = %q, want main", sj.Parent)
	}
}

// TestRename renames a branch and updates child parent pointers.
func TestRename(t *testing.T) {
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.stOK("checkout", "feat-a")

	res := r.stOK("rename", "renamed-a")
	wantStdoutContains(t, res, "Renamed feat-a -> renamed-a")
	if r.branchExists("feat-a") {
		t.Fatalf("old branch feat-a should be gone")
	}
	if !r.branchExists("renamed-a") {
		t.Fatalf("new branch renamed-a should exist")
	}

	// feat-b's parent must now point at renamed-a (verified via log --json).
	res = r.stOK("log", "--json")
	var root logNode
	if err := json.Unmarshal([]byte(res.stdout), &root); err != nil {
		t.Fatalf("log --json invalid: %v", err)
	}
	b := findNode(&root, "feat-b")
	if b == nil || b.Parent != "renamed-a" {
		t.Fatalf("feat-b parent not updated after rename: %+v", b)
	}
}

// TestDeleteReparent deletes a middle branch and re-parents its child onto the
// grandparent, dropping the deleted branch's file from the child's history.
func TestDeleteReparent(t *testing.T) {
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.create("feat-c", "c.txt", "c\n", "c")

	res := r.stOK("delete", "feat-b", "--force")
	wantStdoutContains(t, res, "Deleted feat-b")
	if r.branchExists("feat-b") {
		t.Fatalf("feat-b should be deleted")
	}
	if r.fileOnBranch("feat-c", "b.txt") {
		t.Fatalf("feat-c should no longer contain b.txt after deleting feat-b")
	}
	if !r.fileOnBranch("feat-c", "c.txt") {
		t.Fatalf("feat-c lost its own c.txt")
	}

	// feat-c should now be parented on feat-a.
	res = r.stOK("log", "--json")
	var root logNode
	if err := json.Unmarshal([]byte(res.stdout), &root); err != nil {
		t.Fatalf("log --json invalid: %v", err)
	}
	c := findNode(&root, "feat-c")
	if c == nil || c.Parent != "feat-a" {
		t.Fatalf("feat-c not re-parented onto feat-a: %+v", c)
	}
}

// TestUndo asserts that undo restores a branch tip after a modify.
func TestUndo(t *testing.T) {
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	before := r.rev("feat-a")

	r.writeFile("a.txt", "a-modified\n")
	r.stOK("modify", "-a")
	if r.rev("feat-a") == before {
		t.Fatalf("modify did not change feat-a tip")
	}

	res := r.stOK("undo")
	wantStdoutContains(t, res, "undid: modify")
	if got := r.rev("feat-a"); got != before {
		t.Fatalf("undo did not restore feat-a: got %s want %s", got, before)
	}

	// Undoing repeatedly eventually empties the journal.
	for i := 0; i < 10; i++ {
		res = r.stOK("undo")
		if strings.Contains(res.stdout, "nothing to undo") {
			return
		}
	}
	t.Fatalf("expected the undo journal to drain to 'nothing to undo'")
}

// TestTrackUntrack covers tracking a plain git branch, the guard errors
// (track the trunk, double-track, untrack the trunk / an untracked branch), and
// untracking with child re-parenting.
func TestTrackUntrack(t *testing.T) {
	r := newRepo(t)
	r.initStack()

	// track-the-trunk is refused (the repo starts on main).
	r.stOK("checkout", "main")
	res := r.st("track")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "cannot track the trunk")

	// Create a plain git branch off main with a commit, then track it.
	r.git("checkout", "-q", "-b", "plain")
	r.writeFile("p.txt", "p\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "p")
	res = r.stOK("track")
	wantStdoutContains(t, res, "Tracking plain (parent: main)")

	// Double-track errors.
	res = r.st("track")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "already tracked")

	// untrack the trunk errors.
	res = r.st("untrack", "main")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "cannot untrack the trunk")

	// untrack an unknown branch errors.
	res = r.st("untrack", "nope")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "not tracked")

	// untrack the tracked branch succeeds.
	res = r.stOK("untrack", "plain")
	wantStdoutContains(t, res, "Untracked plain")
}

// TestRestackGuards covers the dirty-tree guard and the untracked checkout guard.
func TestRestackGuards(t *testing.T) {
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")

	// Dirty working tree blocks restack — mapped to the dedicated exit code 4.
	r.writeFile("dirty.txt", "dirty\n")
	r.git("add", "-A") // staged but uncommitted -> dirty index
	res := r.st("restack")
	wantExit(t, res, 4)
	wantStderrContains(t, res, "working tree is dirty")

	// Clean it up, then check out an untracked name errors.
	r.git("reset", "-q", "HEAD")
	if err := os.Remove(filepath.Join(r.dir, "dirty.txt")); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	res = r.st("checkout", "ghost")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "not a tracked branch")
}

// TestValidateRepairDrift forces drift by deleting a tracked branch behind st's
// back, asserts validate exits non-zero, then repair fixes it and validate
// passes.
func TestValidateRepairDrift(t *testing.T) {
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")

	// Delete feat-b's git branch outside st.
	r.stOK("checkout", "main")
	r.git("branch", "-D", "feat-b")

	res := r.st("validate")
	wantExit(t, res, 1)
	wantStdoutContains(t, res, "problems:")
	wantStdoutContains(t, res, "feat-b")

	res = r.stOK("repair")
	wantStdoutContains(t, res, "repaired:")

	res = r.stOK("validate")
	wantStdoutContains(t, res, "no problems found")
}

// TestSubmitDryRunAndURL asserts submit prints the planned pushes, exits 0 in
// dry-run, and emits the repository URL; it also covers the at-trunk no-op and
// the no-remote error.
func TestSubmitDryRunAndURL(t *testing.T) {
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")

	// No remote configured yet: submit errors.
	res := r.st("submit")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "does not exist")

	// Configure an ssh remote and dry-run.
	r.git("remote", "add", "origin", "git@example.com:acme/widgets.git")
	res = r.stOK("submit", "--dry-run")
	wantStdoutContains(t, res, "would push feat-a")
	wantStdoutContains(t, res, "would push feat-b")
	wantStdoutContains(t, res, "https://example.com/acme/widgets")

	// At trunk there is nothing to submit.
	r.stOK("checkout", "main")
	res = r.stOK("submit", "--dry-run")
	wantStdoutContains(t, res, "at trunk; nothing to submit")
}

// TestSubmitRealPushSetsUpstream pushes to a real bare remote and confirms the
// branch landed on the remote with an upstream set.
func TestSubmitRealPushSetsUpstream(t *testing.T) {
	r := newRepo(t)

	// Create a bare remote and wire it as origin.
	bare := filepath.Join(t.TempDir(), "remote.git")
	r.gitIn(filepath.Dir(bare), "init", "-q", "--bare", "-b", "main", bare)
	r.git("remote", "add", "origin", bare)
	r.git("push", "-q", "-u", "origin", "main")

	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")

	res := r.stOK("submit")
	wantStdoutContains(t, res, "pushed feat-a")
	wantStdoutContains(t, res, "pushed feat-b")
	wantStdoutContains(t, res, "submitted 2 branch(es) to origin")

	// Both branches must now exist on the bare remote.
	remoteBranches := r.gitIn(bare, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	for _, b := range []string{"feat-a", "feat-b"} {
		if !strings.Contains(remoteBranches, b) {
			t.Fatalf("remote missing branch %q; remote has:\n%s", b, remoteBranches)
		}
	}
	// Upstream tracking was set by push -u.
	up := r.git("rev-parse", "--abbrev-ref", "feat-b@{upstream}")
	if up != "origin/feat-b" {
		t.Fatalf("feat-b upstream = %q, want origin/feat-b", up)
	}
}

// TestSyncPrunesMerged sets up a bare remote, merges the bottom branch into the
// trunk on the remote, and asserts `st sync` fast-forwards the trunk, prunes the
// merged branch, and restacks the survivor.
func TestSyncPrunesMerged(t *testing.T) {
	r := newRepo(t)

	bare := filepath.Join(t.TempDir(), "remote.git")
	r.gitIn(filepath.Dir(bare), "init", "-q", "--bare", "-b", "main", bare)
	r.git("remote", "add", "origin", bare)
	r.git("push", "-q", "-u", "origin", "main")

	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")

	// Merge feat-a into main locally and push, simulating feat-a landing.
	r.stOK("checkout", "main")
	r.git("merge", "-q", "--no-ff", "feat-a", "-m", "merge feat-a")
	r.git("push", "-q", "origin", "main")

	res := r.stOK("sync")
	wantStdoutContains(t, res, "sync complete")
	wantStdoutContains(t, res, "deleted: feat-a")

	if r.branchExists("feat-a") {
		t.Fatalf("feat-a should be pruned after sync")
	}
	if !r.branchExists("feat-b") {
		t.Fatalf("feat-b should survive sync")
	}

	// feat-b is now re-parented onto main and the stack validates clean.
	res = r.stOK("log", "--json")
	var root logNode
	if err := json.Unmarshal([]byte(res.stdout), &root); err != nil {
		t.Fatalf("log --json invalid: %v", err)
	}
	b := findNode(&root, "feat-b")
	if b == nil || b.Parent != "main" {
		t.Fatalf("feat-b should be re-parented onto main after prune: %+v", b)
	}
	r.stOK("validate")
}

// TestSyncNoRemote asserts sync is a clean no-op when no remote is configured.
func TestSyncNoRemote(t *testing.T) {
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	res := r.stOK("sync")
	wantStdoutContains(t, res, "sync complete")
	wantStdoutContains(t, res, "skipped (no remote)")
}

// TestCompletion asserts each supported shell emits a non-empty script and an
// unsupported shell errors.
func TestCompletion(t *testing.T) {
	r := newRepo(t)
	for _, shell := range []string{"bash", "zsh", "fish"} {
		res := r.stOK("completion", shell)
		if strings.TrimSpace(res.stdout) == "" {
			t.Fatalf("completion %s produced empty output", shell)
		}
		// Every script should mention the st command name.
		wantStdoutContains(t, res, "st")
	}

	res := r.st("completion", "powershell")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "unsupported shell")

	// Missing the shell argument is a usage error.
	res = r.st("completion")
	wantExit(t, res, 1)
}

// TestUninitialized asserts commands that require initialization fail cleanly in
// a repo where `st init` has not been run.
func TestUninitialized(t *testing.T) {
	r := newRepo(t)
	res := r.st("status")
	// "not initialized" maps to the dedicated exit code 3 (see docs/AGENT.md).
	wantExit(t, res, 3)
	wantStderrContains(t, res, "st:")
}

// TestJSONError asserts that in --json mode a failure is emitted as a structured
// envelope on stderr with a stable machine code, and the exit code still applies.
func TestJSONError(t *testing.T) {
	r := newRepo(t)
	res := r.st("status", "--json")
	wantExit(t, res, 3)
	wantStderrContains(t, res, `"code": "not_initialized"`)
}

// TestRestackDryRun previews what a restack would rebase and changes nothing.
func TestRestackDryRun(t *testing.T) {
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")
	r.create("feat-b", "g.txt", "B\n", "b") // independent file: no conflict
	r.stOK("checkout", "feat-a")

	// Amend feat-a so feat-b drifts.
	r.writeFile("f.txt", "A2\n")
	r.git("commit", "-qa", "--amend", "--no-edit")
	before := r.git("rev-parse", "feat-b")

	res := r.stOK("restack", "--dry-run")
	wantStdoutContains(t, res, "would restack: feat-b")
	if after := r.git("rev-parse", "feat-b"); after != before {
		t.Fatalf("dry-run must not move feat-b (before=%s after=%s)", before, after)
	}

	res = r.stOK("restack", "--dry-run", "--json")
	wantStdoutContains(t, res, `"dryRun": true`)
}

// TestGuide asserts the agent guide prints text and JSON and exits 0.
func TestGuide(t *testing.T) {
	r := newRepo(t)
	wantStdoutContains(t, r.stOK("guide"), "recommended workflow")
	wantStdoutContains(t, r.stOK("guide", "--json"), `"steps"`)
}
