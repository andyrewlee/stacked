package git

import (
	"io"
	"os"
	"os/exec"
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
