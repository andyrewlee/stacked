package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The CLI/agent contract, driven black-box: help/version, error reporting and
// exit codes, navigation wording, submit, completion, and JSON envelopes.
// TestVersion asserts `st version` prints build info and exits 0, and that the
// -v/--version aliases behave the same.
func TestVersion(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	r := newRepo(t)

	// Derive the command set from the binary's own machine-readable help (which
	// is generated from the registry), so this can never drift from a
	// hand-maintained slice (TEST-8).
	var help struct {
		Commands []struct {
			Name string `json:"name"`
		} `json:"commands"`
	}
	res := r.stOK("help", "--json")
	if err := json.Unmarshal([]byte(res.stdout), &help); err != nil {
		t.Fatalf("help --json not parseable: %v\n%s", err, res.stdout)
	}
	if len(help.Commands) < 20 {
		t.Fatalf("help --json listed only %d commands, expected the full registry", len(help.Commands))
	}

	for _, form := range [][]string{nil, {"help"}, {"-h"}, {"--help"}} {
		res := r.st(form...)
		wantExit(t, res, 0)
		wantStdoutContains(t, res, "st - manage stacked diffs on top of git")
		for _, c := range help.Commands {
			if !strings.Contains(res.stdout, c.Name) {
				t.Fatalf("help (%v) missing command %q\nstdout:\n%s", form, c.Name, res.stdout)
			}
		}
	}
}

// TestUnknownCommand asserts an unrecognized command exits 1, writes the
// "unknown command" diagnostic to stderr, emits a parseable JSON envelope under
// --json, and never pollutes stdout (CLI-1).
func TestUnknownCommand(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	res := r.st("definitely-not-a-command")
	wantExit(t, res, 1)
	wantStderrContains(t, res, `st: unknown command "definitely-not-a-command"`)
	if strings.TrimSpace(res.stdout) != "" {
		t.Fatalf("unknown command wrote stdout: %q", res.stdout)
	}

	res = r.st("definitely-not-a-command", "--json")
	wantExit(t, res, 1)
	if strings.TrimSpace(res.stdout) != "" {
		t.Fatalf("unknown command --json wrote stdout: %q", res.stdout)
	}
	var env struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal([]byte(res.stderr), &env); err != nil {
		t.Fatalf("unknown command --json stderr not a JSON envelope: %v\n%s", err, res.stderr)
	}
	if env.Error.Code != "error" {
		t.Errorf("unknown command --json code = %q, want error", env.Error.Code)
	}
}

// TestFlagErrorStaysOnStderr asserts a bad flag reports the error once, on
// stderr only — stdout is the machine-readable data stream and must stay clean
// (CLI-4, CLI-8).
func TestFlagErrorStaysOnStderr(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	res := r.st("status", "--bad")
	wantExit(t, res, 1)
	if strings.TrimSpace(res.stdout) != "" {
		t.Fatalf("flag error wrote stdout: %q", res.stdout)
	}
	wantStderrContains(t, res, "not defined")
	if n := strings.Count(res.stderr, "not defined"); n != 1 {
		t.Fatalf("flag error reported %d times, want exactly 1:\n%s", n, res.stderr)
	}
}

// TestNavigationEdges covers the ambiguous-up fork, top branch-point error, the
// "already at the top/bottom/trunk" notices, and invalid count arguments.
func TestNavigationEdges(t *testing.T) {
	t.Parallel()
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
	wantStderrContains(t, res, "invalid step count")

	res = r.st("down", "0")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "at least 1")
}

func TestLogEscapesControlBytesInSubject(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "evil.txt", "evil\n", "evil\x1b[2Ksubject")

	res := r.stOK("log")
	if strings.Contains(res.stdout, "\x1b") {
		t.Fatalf("log output contains a raw escape byte:\n%q", res.stdout)
	}
	wantStdoutContains(t, res, `evil\x1b[2Ksubject`)
}

// TestExitCodeContract maps each documented exit code (docs/AGENT.md) to a
// triggering scenario, guarding the exit-code contract against drift (LOOP-3).
func TestExitCodeContract(t *testing.T) {
	t.Parallel()
	t.Run("0_success", func(t *testing.T) {
		r := newRepo(t)
		r.initStack()
		wantExit(t, r.st("status"), 0)
	})
	t.Run("1_usage", func(t *testing.T) {
		r := newRepo(t)
		r.initStack()
		wantExit(t, r.st("create"), 1) // missing branch name
	})
	t.Run("2_conflict", func(t *testing.T) {
		r := newRepo(t)
		r.initStack()
		r.create("feat-a", "f.txt", "A\n", "a")
		r.create("feat-b", "f.txt", "A\nB\n", "b")
		r.stOK("checkout", "feat-a")
		r.writeFile("f.txt", "X\n")
		res := r.st("modify", "-a", "--json")
		wantExit(t, res, 2)
		// The conflict envelope names the branch (and parent) it stopped on, so
		// an agent can re-orient without parsing the message.
		var env struct {
			Error struct{ Code, Branch, Onto string }
		}
		if err := json.Unmarshal([]byte(res.stderr), &env); err != nil {
			t.Fatalf("conflict --json stderr not a JSON envelope: %v\n%s", err, res.stderr)
		}
		if env.Error.Code != "conflict" {
			t.Errorf("conflict code = %q, want conflict", env.Error.Code)
		}
		if env.Error.Branch != "feat-b" {
			t.Errorf("conflict branch = %q, want feat-b", env.Error.Branch)
		}
	})
	t.Run("3_not_initialized", func(t *testing.T) {
		r := newRepo(t)
		wantExit(t, r.st("status"), 3)
	})
	t.Run("4_dirty", func(t *testing.T) {
		r := newRepo(t)
		r.initStack()
		r.create("feat-a", "a.txt", "a\n", "a")
		r.create("feat-b", "b.txt", "b\n", "b")
		r.stOK("checkout", "feat-a")
		r.writeFile("a.txt", "dirty\n") // unstaged change
		wantExit(t, r.st("restack"), 4)
	})
}

func TestHostileStateBranchNameRefused(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.git("checkout", "-q", "main")

	state := []byte(`{
  "trunk": "main",
  "branches": {
    "--exec=touch pwned": {
      "name": "--exec=touch pwned",
      "parent": "main",
      "parentSHA": "0000000000000000000000000000000000000000"
    }
  }
}
`)
	if err := os.WriteFile(filepath.Join(r.dir, ".git", "stacked", "state.json"), state, 0o644); err != nil {
		t.Fatalf("write hostile state: %v", err)
	}

	res := r.st("restack")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "not a valid git ref name")
	if _, err := os.Stat(filepath.Join(r.dir, "pwned")); !os.IsNotExist(err) {
		t.Fatalf("pwned file exists or could not be checked: %v", err)
	}
}

func TestCorruptState(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	statePath := filepath.Join(r.dir, ".git", "stacked", "state.json")
	if err := os.WriteFile(statePath, []byte("{not json\n"), 0o644); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}

	res := r.st("log")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "state.json")

	res = r.st("log", "--json")
	wantExit(t, res, 1)
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(res.stderr), &env); err != nil {
		t.Fatalf("corrupt state --json stderr not a JSON envelope: %v\n%s", err, res.stderr)
	}
	if env.Error.Code != "error" {
		t.Errorf("corrupt state --json code = %q, want error", env.Error.Code)
	}
	if !strings.Contains(env.Error.Message, "state.json") {
		t.Fatalf("corrupt state --json message = %q, want state path", env.Error.Message)
	}
}

// TestSubmitDryRunAndURL asserts submit prints the planned pushes, exits 0 in
// dry-run, and emits the repository URL; it also covers the at-trunk no-op and
// the no-remote error.
func TestSubmitDryRunAndURL(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	for _, b := range []string{"feat-a", "feat-b"} {
		up := r.git("rev-parse", "--abbrev-ref", b+"@{upstream}")
		if want := "origin/" + b; up != want {
			t.Fatalf("%s upstream = %q, want %s", b, up, want)
		}
	}
}

// TestCompletion asserts each supported shell emits a non-empty script and an
// unsupported shell errors.
func TestCompletion(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	r := newRepo(t)
	res := r.st("status")
	// "not initialized" maps to the dedicated exit code 3 (see docs/AGENT.md).
	wantExit(t, res, 3)
	wantStderrContains(t, res, "st:")
}

// TestJSONError asserts that in --json mode a failure is emitted as a structured
// envelope on stderr with a stable machine code, and the exit code still applies.
func TestJSONError(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	res := r.st("status", "--json")
	wantExit(t, res, 3)
	wantStderrContains(t, res, `"code": "not_initialized"`)
}

// TestGuide asserts the agent guide prints text and JSON and exits 0.
func TestGuide(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	wantStdoutContains(t, r.stOK("guide"), "recommended workflow")
	wantStdoutContains(t, r.stOK("guide", "--json"), `"steps"`)
}
