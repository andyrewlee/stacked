package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"stacked/internal/stack"
)

// withArgs runs fn with os.Args set to {"st"} + args, restoring os.Args after.
func withArgs(t *testing.T, args []string, fn func()) {
	t.Helper()
	orig := os.Args
	os.Args = append([]string{"st"}, args...)
	defer func() { os.Args = orig }()
	fn()
}

func TestExecuteHelp(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"-h"}, {"--help"}} {
		var code int
		out := captureStdout(t, func() {
			withArgs(t, args, func() { code = Execute() })
		})
		if code != 0 {
			t.Fatalf("Execute(%v) = %d, want 0", args, code)
		}
		// Help lists registered commands plus the built-in pseudo-commands.
		for _, want := range []string{"st - manage stacked diffs", "create", "help", "version"} {
			if !strings.Contains(out, want) {
				t.Fatalf("help(%v) missing %q:\n%s", args, want, out)
			}
		}
	}
}

func TestExecuteVersion(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"-v"}, {"--version"}} {
		var code int
		out := captureStdout(t, func() {
			withArgs(t, args, func() { code = Execute() })
		})
		if code != 0 {
			t.Fatalf("Execute(%v) = %d, want 0", args, code)
		}
		if !strings.HasPrefix(out, "st "+version) {
			t.Fatalf("version(%v) = %q, want prefix %q", args, out, "st "+version)
		}
	}
}

func TestExecuteUnknownCommand(t *testing.T) {
	var code int
	withArgs(t, []string{"frobnicate"}, func() {
		// Help still goes to stdout; we only assert the exit code here. The
		// stderr "unknown command" line is verified by the e2e suite.
		_ = captureStdout(t, func() { code = Execute() })
	})
	if code != 1 {
		t.Fatalf("Execute(unknown) = %d, want 1", code)
	}
}

func TestExecuteCommandError(t *testing.T) {
	// `st up` in an uninitialized repo returns ErrNotInitialized, which the
	// dispatcher maps to the dedicated exit code 3 (see docs/AGENT.md).
	newRepo(t)
	var code int
	withArgs(t, []string{"up"}, func() {
		_ = captureStdout(t, func() { code = Execute() })
	})
	if code != 3 {
		t.Fatalf("Execute(up uninitialized) = %d, want 3 (not_initialized)", code)
	}
}

func TestExitCodeAndErrorCodeMapping(t *testing.T) {
	cases := []struct {
		err  error
		code int
		json string
	}{
		{stack.ErrNotInitialized, 3, "not_initialized"},
		{stack.ErrConflict, 2, "conflict"},
		{stack.ErrDirty, 4, "dirty"},
		{errors.New("boom"), 1, "error"},
		{fmt.Errorf("wrapped: %w", stack.ErrConflict), 2, "conflict"},
	}
	for _, c := range cases {
		if got := exitCode(c.err); got != c.code {
			t.Errorf("exitCode(%v) = %d, want %d", c.err, got, c.code)
		}
		if got := errorCode(c.err); got != c.json {
			t.Errorf("errorCode(%v) = %q, want %q", c.err, got, c.json)
		}
	}
}

func TestJSONRequested(t *testing.T) {
	if !jsonRequested([]string{"create", "x", "--json"}) {
		t.Error("want true when --json present")
	}
	if jsonRequested([]string{"create", "--", "--json"}) {
		t.Error("want false when --json is after a -- terminator")
	}
	if jsonRequested([]string{"log"}) {
		t.Error("want false when absent")
	}
}

func TestExecuteDispatchesSubcommand(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	// A real subcommand run through the dispatcher should succeed (exit 0).
	var code int
	withArgs(t, []string{"status"}, func() {
		_ = captureStdout(t, func() { code = Execute() })
	})
	if code != 0 {
		t.Fatalf("Execute(status) = %d, want 0", code)
	}
}
