package git

// debug.go — the opt-in spawn trace: ST_DEBUG=1 (any value but "" or "0")
// writes one stderr line per git invocation with its argv and wall duration:
//
//	st: git -C /path --version (8ms)
//
// Debugging aid only: it never writes to stdout, so --json output and the
// __complete endpoint stay byte-identical with the trace on, and it changes
// no behavior beyond the extra write. Args that could carry credentials
// (a remote URL containing "://", or a scp-like user@host:path) are redacted
// to <url> before printing.

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// debugOn reports whether the spawn trace is enabled. Checked per spawn (not
// cached) so a test can toggle the env var between invocations.
func debugOn() bool {
	v := os.Getenv("ST_DEBUG")
	return v != "" && v != "0"
}

// spawnRun runs cmd (a git invocation) like cmd.Run(), tracing it when
// ST_DEBUG is enabled.
func spawnRun(cmd *exec.Cmd) error {
	start := time.Now()
	err := cmd.Run()
	traceSpawn(cmd.Args, time.Since(start))
	return err
}

// spawnOutput is spawnRun for cmd.Output() callers (spawn sites that need the
// child's stdout bytes, e.g. check-ignore).
func spawnOutput(cmd *exec.Cmd) ([]byte, error) {
	start := time.Now()
	out, err := cmd.Output()
	traceSpawn(cmd.Args, time.Since(start))
	return out, err
}

// spawnCombined is spawnRun for cmd.CombinedOutput() callers.
func spawnCombined(cmd *exec.Cmd) ([]byte, error) {
	start := time.Now()
	out, err := cmd.CombinedOutput()
	traceSpawn(cmd.Args, time.Since(start))
	return out, err
}

// traceSpawn writes the trace line for one completed spawn. It is a no-op
// unless ST_DEBUG is enabled, and it always writes to stderr — stdout is
// machine-readable output and must stay byte-identical.
func traceSpawn(argv []string, dur time.Duration) {
	if !debugOn() {
		return
	}
	redacted := make([]string, len(argv))
	for i, a := range argv {
		redacted[i] = redactURLArg(a)
	}
	fmt.Fprintf(os.Stderr, "st: %s (%dms)\n", strings.Join(redacted, " "), dur.Milliseconds())
}

// redactURLArg masks args that could carry remote credentials: anything
// URL-shaped ("://" — https://user:pass@host/…), scp-like ("user@host:path"),
// or an explicit credential userinfo ("user:pass@host").
func redactURLArg(arg string) string {
	if strings.Contains(arg, "://") {
		return "<url>"
	}
	if at := strings.Index(arg, "@"); at > 0 {
		rest := arg[at+1:]
		// scp-like "user@host:path" or "user:pass@host" — no "/" before "@"
		// means the userinfo is host-bound rather than a local path.
		if !strings.Contains(arg[:at], "/") &&
			(strings.Contains(rest, ":") || !strings.Contains(rest, "/")) {
			return "<url>"
		}
	}
	return arg
}
