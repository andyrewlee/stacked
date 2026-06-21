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

// isAncestor reports whether ancestor is an ancestor of descendant (i.e. the
// commit ancestor is contained in descendant's history), via
// `git merge-base --is-ancestor`.
func (r *repo) isAncestor(ancestor, descendant string) bool {
	r.t.Helper()
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestor, descendant)
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
	Worktree     string     `json:"worktree"`
	Dirty        bool       `json:"dirty"`
	Children     []*logNode `json:"children"`
}

type statusJSON struct {
	Branch        string   `json:"branch"`
	Role          string   `json:"role"`
	Parent        string   `json:"parent"`
	Children      []string `json:"children"`
	NeedsRestack  *bool    `json:"needsRestack"`
	WorktreeClean bool     `json:"worktreeClean"`
	Worktree      string   `json:"worktree"`
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
