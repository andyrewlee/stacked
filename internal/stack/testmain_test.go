package stack

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitFixtureChildMarker marks a re-launched test binary as the child half of
// TestGitFixtureEnvironmentIsolation.
const gitFixtureChildMarker = "ST_GITFIXTURE_CHILD"

// TestMain normalizes the Git environment once for the whole suite, so
// real-Git fixtures exercise stacked rather than the developer's hooks,
// signing configuration, or repository location.
func TestMain(m *testing.M) {
	if err := normalizeGitTestEnv(); err != nil {
		fmt.Fprintln(os.Stderr, "normalize git test env:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// normalizeGitTestEnv removes every inherited GIT_* variable — including
// indexed GIT_CONFIG_KEY_*/GIT_CONFIG_VALUE_* pairs and repository-routing
// variables such as GIT_DIR — then pins deterministic noninteractive
// settings. Individual tests may still set Git variables afterwards;
// normalization happens once at startup.
func normalizeGitTestEnv() error {
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(k, "GIT_") {
			if err := os.Unsetenv(k); err != nil {
				return fmt.Errorf("unset %s: %w", k, err)
			}
		}
	}
	for _, kv := range [][2]string{
		{"GIT_CONFIG_GLOBAL", os.DevNull},
		{"GIT_CONFIG_SYSTEM", os.DevNull},
		{"GIT_TERMINAL_PROMPT", "0"},
		{"GIT_PAGER", "cat"},
		{"GIT_EDITOR", "true"},
	} {
		if err := os.Setenv(kv[0], kv[1]); err != nil {
			return fmt.Errorf("set %s: %w", kv[0], err)
		}
	}
	return nil
}

// TestGitFixtureEnvironmentIsolation proves the suite's startup normalization
// (TestMain) keeps hostile inherited Git configuration out of fixture
// creation: it re-runs this binary's child helper under a contaminated
// environment and expects the fixture to succeed in its own temporary
// directory.
func TestGitFixtureEnvironmentIsolation(t *testing.T) {
	if os.Getenv(gitFixtureChildMarker) == "1" {
		t.Skip("child half runs via TestGitFixtureEnvironmentIsolationChild")
	}

	hostile := t.TempDir()

	// A decoy repository the routing variables point at: if GIT_DIR or
	// GIT_WORK_TREE leak into the child, its fixture commits land here.
	decoy := filepath.Join(hostile, "decoy")
	fixtureGit(t, "", "init", "-q", "-b", "main", decoy)

	// A signer and a hook that record any invocation to a sentinel file and
	// then fail, so contamination breaks the fixture commit and is provable
	// afterwards.
	sentinel := filepath.Join(hostile, "sentinel")
	signer := writeSentinelStub(t, hostile, "signer", sentinel)
	writeSentinelStub(t, filepath.Join(hostile, "hooks"), "pre-commit", sentinel)

	config := filepath.Join(hostile, "gitconfig")
	content := fmt.Sprintf("[commit]\n\tgpgSign = true\n[gpg]\n\tprogram = %s\n[core]\n\thooksPath = %s\n",
		signer, filepath.Join(hostile, "hooks"))
	if err := os.WriteFile(config, []byte(content), 0o644); err != nil {
		t.Fatalf("write hostile gitconfig: %v", err)
	}

	env := hostileFixtureEnv(
		gitFixtureChildMarker+"=1",
		"GIT_CONFIG_GLOBAL="+config,
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=commit.gpgSign",
		"GIT_CONFIG_VALUE_0=true",
		"GIT_CONFIG_KEY_1=gpg.program",
		"GIT_CONFIG_VALUE_1="+signer,
		"GIT_DIR="+filepath.Join(decoy, ".git"),
		"GIT_WORK_TREE="+decoy,
	)

	cmd := exec.Command(os.Args[0], "-test.run", "^TestGitFixtureEnvironmentIsolationChild$", "-test.v")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated fixture child failed: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Fatalf("hostile signer/hook was invoked: %s exists", sentinel)
	}
	if exec.Command("git", "-C", decoy, "rev-parse", "--verify", "-q", "HEAD").Run() == nil {
		t.Fatal("fixture commit landed in the decoy repository")
	}
}

// TestGitFixtureEnvironmentIsolationChild is the helper half of
// TestGitFixtureEnvironmentIsolation. The parent re-launches this binary with
// the marker set and a hostile Git environment; this helper then builds the
// package's ordinary fixture and asserts the repository is its own temporary
// directory — which only holds once TestMain has stripped the contamination.
func TestGitFixtureEnvironmentIsolationChild(t *testing.T) {
	if os.Getenv(gitFixtureChildMarker) != "1" {
		t.Skip("helper for TestGitFixtureEnvironmentIsolation")
	}
	dir := initGitRepo(t)
	gitDir := fixtureGit(t, dir, "rev-parse", "--absolute-git-dir")
	if want := filepath.Join(dir, ".git"); gitDir != want {
		t.Fatalf("fixture git dir = %q, want %q", gitDir, want)
	}
	if top := fixtureGit(t, dir, "rev-parse", "--show-toplevel"); top != dir {
		t.Fatalf("fixture worktree = %q, want %q", top, dir)
	}
}

// fixtureGit runs git in dir (or the working directory when dir is empty) and
// returns trimmed output, failing the test on error.
func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// hostileFixtureEnv returns the current environment with every GIT_* variable
// removed and the given overrides appended, so the child inherits exactly the
// contaminated setup (plus coverage/runtime variables such as GOCOVERDIR).
func hostileFixtureEnv(overrides ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(k, "GIT_") {
			continue
		}
		env = append(env, e)
	}
	return append(env, overrides...)
}

// writeSentinelStub writes an executable shell script that records its
// invocation by appending its own name to sentinel, then exits nonzero. Git
// runs hooks and gpg.program through sh (git-for-windows ships sh), so the
// stub is portable.
func writeSentinelStub(t *testing.T, dir, name, sentinel string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\nprintf '%s\\n' '" + name + "' >> '" + sentinel + "'\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", path, err)
	}
	return path
}
