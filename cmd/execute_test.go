package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

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
	// dispatcher maps to the dedicated exit code 3.
	newRepo(t)
	var code int
	withArgs(t, []string{"up"}, func() {
		_ = captureStdout(t, func() { code = Execute() })
	})
	if code != 3 {
		t.Fatalf("Execute(up uninitialized) = %d, want 3 (not_initialized)", code)
	}
}

func TestExecuteJSONErrorIsParseable(t *testing.T) {
	var code int
	var stdout string
	errOut := executeCapturingOutput(t, []string{"create", "--json"}, &code, &stdout)
	if code != 1 {
		t.Fatalf("Execute(create --json) = %d, want 1", code)
	}
	if stdout != "" {
		t.Fatalf("JSON error wrote stdout:\n%s", stdout)
	}
	if strings.Contains(errOut, "usage:") {
		t.Fatalf("JSON error included usage text:\n%s", errOut)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(errOut), &payload); err != nil {
		t.Fatalf("JSON error was not parseable: %v\n%s", err, errOut)
	}
}

func TestExecuteJSONFlagParseErrorIsParseable(t *testing.T) {
	cases := [][]string{
		{"create", "--json", "--bad"},
		{"create", "--json=true", "--bad"},
		{"status", "--json", "--bad"},
		{"status", "--json=true", "--bad"},
	}
	for _, args := range cases {
		var code int
		var stdout string
		errOut := executeCapturingOutput(t, args, &code, &stdout)
		if code != 1 {
			t.Fatalf("Execute(%v) = %d, want 1", args, code)
		}
		if stdout != "" {
			t.Fatalf("JSON flag parse error wrote stdout for %v:\n%s", args, stdout)
		}
		if strings.Contains(errOut, "usage:") {
			t.Fatalf("JSON flag parse error included flag output for %v:\n%s", args, errOut)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(errOut), &payload); err != nil {
			t.Fatalf("JSON flag parse error was not parseable for %v: %v\n%s", args, err, errOut)
		}
	}
}

func executeCapturingOutput(t *testing.T, args []string, code *int, stdout *string) string {
	t.Helper()
	return captureStderr(t, func() {
		*stdout = captureStdout(t, func() {
			withArgs(t, args, func() { *code = Execute() })
		})
	})
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
	if !jsonRequested([]string{"create", "--json=true"}) {
		t.Error("want true when --json=true present")
	}
	if jsonRequested([]string{"create", "--json=false"}) {
		t.Error("want false when --json=false present")
	}
	if jsonRequested([]string{"create", "--", "--json"}) {
		t.Error("want false when --json is after a -- terminator")
	}
	if jsonRequested([]string{"log"}) {
		t.Error("want false when absent")
	}
}

// A panic in a command must be recovered into a structured failure with a
// distinct exit code (not 2, the runtime's panic code, which means "conflict").
func TestExecuteRecoversFromPanic(t *testing.T) {
	// Register a throwaway command that panics, removing it afterward so the
	// global registry stays clean for the other (sequential) tests.
	register(&Command{
		Name:    "panic-boom",
		Summary: "test-only panicking command",
		Usage:   "st panic-boom",
		Run:     func([]string) error { panic("kaboom") },
	})
	defer func() {
		registry = registry[:len(registry)-1]
		delete(byName, "panic-boom")
	}()

	// JSON mode: stderr must be a parseable envelope with code "internal".
	var code int
	var stdout string
	errOut := executeCapturingOutput(t, []string{"panic-boom", "--json"}, &code, &stdout)
	if code != exitInternal {
		t.Fatalf("Execute(panic --json) = %d, want %d", code, exitInternal)
	}
	if stdout != "" {
		t.Fatalf("panic wrote stdout:\n%s", stdout)
	}
	var payload struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal([]byte(errOut), &payload); err != nil {
		t.Fatalf("panic envelope not parseable: %v\n%s", err, errOut)
	}
	if payload.Error.Code != "internal" {
		t.Errorf("panic error code = %q, want internal", payload.Error.Code)
	}

	// Plain mode: a human-readable message, still exit exitInternal.
	code = 0
	errOut = executeCapturingOutput(t, []string{"panic-boom"}, &code, &stdout)
	if code != exitInternal {
		t.Fatalf("Execute(panic) = %d, want %d", code, exitInternal)
	}
	if !strings.Contains(errOut, "internal error") {
		t.Errorf("plain panic output missing 'internal error':\n%s", errOut)
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
