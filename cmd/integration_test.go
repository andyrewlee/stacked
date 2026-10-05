package cmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/andyrewlee/stacked/internal/stack"
)

// This file holds the shared helpers for the cmd test suite. The engine logic is
// exercised by the fast fake-git tests in internal/stack, and the real-binary
// behavior by the black-box suite in ./e2e; the cmd tests here cover only the
// adapter layer (flag parsing, output rendering, dispatch).

// TestMain silences command stdout during the cmd tests (assertions read git
// state or captured output explicitly, not the default stdout). It also keeps
// git deterministic and non-interactive regardless of the host: the cmd suite
// must not depend on real user config.
func TestMain(m *testing.M) {
	replay := silenceStdout()
	if err := normalizeGitTestEnv(); err != nil {
		fmt.Fprintln(os.Stderr, "normalize git test env:", err)
		os.Exit(1)
	}
	code := m.Run()
	replay(code)
	os.Exit(code)
}

// silenceStdout pipes os.Stdout into a buffer so command output does not
// drown test results. The returned restore closes the pipe and, on failure,
// replays the capture to stderr — without it a failing test's "--- FAIL"
// detail would be swallowed along with the noise, leaving CI logs empty.
// ST_TEST_DEBUG=1 keeps stdout streaming live instead.
func silenceStdout() func(code int) {
	if os.Getenv("ST_TEST_DEBUG") != "" {
		return func(int) {}
	}
	r, w, err := os.Pipe()
	if err != nil {
		return func(int) {}
	}
	var buf bytes.Buffer
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(drained)
	}()
	os.Stdout = w
	return func(code int) {
		_ = os.Stdout.Close()
		<-drained
		if code != 0 {
			_, _ = os.Stderr.Write(buf.Bytes())
		}
	}
}

func mustRun(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo creates a fresh temp git repo with one commit on main and chdirs into
// it (t.Chdir restores the working directory on cleanup).
func newRepo(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	// The worktree probe is memoized per process; the test binary runs many
	// commands against different temp repos in one process, so drop any cached
	// list (from a prior repo) before this repo's commands run.
	resetProcCaches()
	mustRun(t, "git", "init", "-q")
	mustRun(t, "git", "symbolic-ref", "HEAD", "refs/heads/main")
	mustRun(t, "git", "config", "user.email", "test@example.com")
	mustRun(t, "git", "config", "user.name", "test")
	write(t, "base.txt", "base\n")
	mustRun(t, "git", "add", "-A")
	mustRun(t, "git", "commit", "-q", "-m", "init")
}

func curBranch(t *testing.T) string {
	t.Helper()
	return mustRun(t, "git", "rev-parse", "--abbrev-ref", "HEAD")
}

func mustInit(t *testing.T) {
	t.Helper()
	if err := runInit([]string{"--trunk", "main"}); err != nil {
		t.Fatalf("init: %v", err)
	}
}

// mustCreate writes file=content, then creates a tracked branch committing it.
func mustCreate(t *testing.T, name, file, content, msg string) {
	t.Helper()
	write(t, file, content)
	if err := runCreate([]string{name, "-a", "-m", msg}); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
}

func mustCheckout(t *testing.T, name string) {
	t.Helper()
	if err := runCheckout([]string{name}); err != nil {
		t.Fatalf("checkout %s: %v", name, err)
	}
}

func stateT(t *testing.T) *stack.State {
	t.Helper()
	s, err := stack.Load()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	return s
}

func hasFile(branch, file string) bool {
	return exec.Command("git", "cat-file", "-e", branch+":"+file).Run() == nil
}
