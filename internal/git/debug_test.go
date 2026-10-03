package git

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// debug_test.go — the ST_DEBUG spawn trace: env gating, stderr-only writes,
// and credential redaction.

func TestDebugOn(t *testing.T) {
	for _, tc := range []struct {
		v    string
		set  bool
		want bool
	}{
		{"", false, false},
		{"", true, false},
		{"0", true, false},
		{"1", true, true},
		{"true", true, true},
	} {
		if tc.set {
			t.Setenv("ST_DEBUG", tc.v)
		} else {
			t.Setenv("ST_DEBUG", "")
			os.Unsetenv("ST_DEBUG")
		}
		if got := debugOn(); got != tc.want {
			t.Errorf("ST_DEBUG=%q (set=%v): debugOn() = %v, want %v", tc.v, tc.set, got, tc.want)
		}
	}
}

func TestTraceSpawnWritesStderrOnly(t *testing.T) {
	t.Setenv("ST_DEBUG", "1")
	// Capture stderr at the fd level: os.Stderr is a *os.File, so swap it for
	// a pipe, run a real spawn through the wrapper, and read the trace.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = saved }()

	cmd := exec.Command("git", "--version")
	if err := spawnRun(cmd); err != nil {
		t.Fatalf("spawnRun: %v", err)
	}
	w.Close()
	out, _ := io.ReadAll(r)
	os.Stderr = saved

	line := string(out)
	if !strings.HasPrefix(line, "st: git --version (") || !strings.HasSuffix(strings.TrimSpace(line), "ms)") {
		t.Fatalf("trace line = %q, want 'st: git --version (<n>ms)'", line)
	}
}

func TestTraceSpawnSilentWhenDisabled(t *testing.T) {
	t.Setenv("ST_DEBUG", "")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = saved }()

	cmd := exec.Command("git", "--version")
	if err := spawnRun(cmd); err != nil {
		t.Fatalf("spawnRun: %v", err)
	}
	w.Close()
	out, _ := io.ReadAll(r)
	os.Stderr = saved
	if len(out) != 0 {
		t.Fatalf("trace wrote %q with ST_DEBUG unset; want silence", out)
	}
}

func TestRedactURLArg(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"https://user:pw@github.com/o/r.git", "<url>"},
		{"https://github.com/o/r.git", "<url>"},
		{"ssh://git@gitlab.example.com:2222/o/r", "<url>"},
		{"user@github.com:o/r.git", "<url>"},
		{"user:pw@github.com", "<url>"},
		{"main", "main"},
		{"refs/heads/feat-a", "refs/heads/feat-a"},
		{"--force-with-lease", "--force-with-lease"},
		{"feat/x@y", "feat/x@y"},
		{"/abs/path/file.txt", "/abs/path/file.txt"},
	}
	for _, tc := range cases {
		if got := redactURLArg(tc.in); got != tc.want {
			t.Errorf("redactURLArg(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRedactCredentials(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		// The git transport diagnostic this exists for.
		{
			"fatal: unable to access 'https://oauth2:SECRET@h/r/': Could not resolve host",
			"fatal: unable to access 'https://<redacted>@h/r/': Could not resolve host",
		},
		// URL arm: any userinfo — even a bare username — inside a scheme URL.
		{"https://user@host/repo", "https://<redacted>@host/repo"},
		{"ssh://git@host:2222/o/r", "ssh://<redacted>@host:2222/o/r"},
		// No userinfo, or the "@" sits inside the path — untouched.
		{"https://host/repo", "https://host/repo"},
		{"https://@host/repo", "https://@host/repo"},
		{"https://host/path@x", "https://host/path@x"},
		// scp arm: a password-bearing prefix before "host:path" is masked; a
		// bare user@host ssh reference is not credential-shaped and stays.
		{"user:pass@host:org/repo", "<redacted>@host:org/repo"},
		{"git@host:org/repo", "git@host:org/repo"},
		// "user:pass@host" without the host:path separator is not the scp
		// shape — masking there would catch non-credential tokens.
		{"user:pass@host/path", "user:pass@host/path"},
		// Plain text and ordinary mail-style "user@host" pass through.
		{"plain error text", "plain error text"},
		{"email me at user@host", "email me at user@host"},
		{"", ""},
		// Multiline payloads mask every occurrence.
		{
			"remote: see https://a:b@h/x and user:pw@h:y\nremote: done",
			"remote: see https://<redacted>@h/x and <redacted>@h:y\nremote: done",
		},
	}
	for _, tc := range cases {
		if got := redactCredentials(tc.in); got != tc.want {
			t.Errorf("redactCredentials(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRunErrorRedactsCredentials drives the real wrap path: a PATH-shim `git`
// fails with credential-bearing stderr, and the wrapped error must already be
// scrubbed — the token can never reach a --json envelope or transcript.
func TestRunErrorRedactsCredentials(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim uses a shell script")
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "git")
	script := "#!/bin/sh\n" +
		"echo \"fatal: unable to access 'https://oauth2:TOPSECRET@h/r/': could not resolve\" >&2\n" +
		"exit 128\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := run("status")
	if err == nil {
		t.Fatal("expected shim failure")
	}
	if strings.Contains(err.Error(), "TOPSECRET") {
		t.Fatalf("wrapped error leaked credential: %v", err)
	}
	if !strings.Contains(err.Error(), "https://<redacted>@h/r/") {
		t.Fatalf("wrapped error = %v; want scheme+host retained after masking userinfo", err)
	}
}
