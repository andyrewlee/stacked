package cmd

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"stacked/internal/stack"
)

// This file holds the shared helpers for the cmd test suite. The engine logic is
// exercised by the fast fake-git tests in internal/stack, and the real-binary
// behavior by the black-box suite in ./e2e; the cmd tests here cover only the
// adapter layer (flag parsing, output rendering, dispatch).

// TestMain silences command stdout during the cmd tests (assertions read git
// state or captured output explicitly, not the default stdout).
func TestMain(m *testing.M) {
	if devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
		os.Stdout = devnull
	}
	os.Exit(m.Run())
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
	mustRun(t, "git", "init", "-q", "-b", "main")
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
