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

// redactCredentials scrubs credential userinfo out of arbitrary text captured
// from a child process — the output-side sibling of redactURLArg's argv mask,
// applied where captured git stderr/stdout is folded into a wrapped error. It
// rewrites "scheme://userinfo@host" to "scheme://<redacted>@host" and the
// scp-like "user:pass@host:" prefix to "<redacted>@host:", the two shapes a
// credential-bearing remote can embed in git stderr (a transport diagnostic
// or a relayed remote-side hook line). Scheme and host survive so the message
// stays actionable; bare "user@host" ssh references and "@"s inside a URL path
// pass through untouched — masking those would erase legitimate context.
func redactCredentials(text string) string {
	// URL arm: after "://", an "@" before the first "/" (or a delimiter) marks
	// userinfo. A "://" without such an "@" has no credentials — skip it and
	// keep scanning; later URLs in the same text may still carry one.
	for off := 0; ; {
		i := strings.Index(text[off:], "://")
		if i < 0 {
			break
		}
		i += off
		rest := text[i+3:]
		at := strings.IndexByte(rest, '@')
		stop := strings.IndexAny(rest, "/ \t\n'\"")
		if at > 0 && (stop < 0 || at < stop) {
			text = text[:i+3] + "<redacted>" + rest[at:]
			off = i + 3 + len("<redacted>") + 1
		} else {
			off = i + 3
		}
	}
	// scp arm: "user:pass@host:" — the token before "@" must contain a ":" (a
	// password is present; bare "user@host:" is a legitimate ssh reference and
	// stays) and no "/" (rules out the already-masked "scheme://x@" shape), and
	// the host after "@" must be followed by ":" before any "/" — the scp
	// host:path separator that proves this is a remote reference.
	for off := 0; ; {
		at := strings.IndexByte(text[off:], '@')
		if at < 0 {
			break
		}
		at += off
		start := at
		for start > 0 && !strings.ContainsRune(" \t\n'\"()=<>\r", rune(text[start-1])) {
			start--
		}
		token := text[start:at]
		rest := text[at+1:]
		colon := strings.IndexByte(rest, ':')
		stop := strings.IndexAny(rest, "/ \t\n'\"\r")
		if strings.ContainsRune(token, ':') && !strings.ContainsRune(token, '/') &&
			colon > 0 && (stop < 0 || colon < stop) {
			text = text[:start] + "<redacted>" + text[at:]
			off = start + len("<redacted>") + 1
		} else {
			off = at + 1
		}
	}
	return text
}
