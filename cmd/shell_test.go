package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellSnippet(t *testing.T) {
	for _, sh := range []string{"bash", "zsh", "fish"} {
		snippet, ok := shellSnippet(sh)
		if !ok {
			t.Errorf("shellSnippet(%q) reported unsupported", sh)
		}
		if !strings.Contains(snippet, cdDirectiveEnv) {
			t.Errorf("%s snippet does not reference %s", sh, cdDirectiveEnv)
		}
		if !strings.Contains(snippet, "cd") {
			t.Errorf("%s snippet does not cd", sh)
		}
	}
	if _, ok := shellSnippet("powershell"); ok {
		t.Error("shellSnippet accepted an unsupported shell")
	}
}

func TestDetectShell(t *testing.T) {
	cases := map[string]string{
		"/bin/zsh":        "zsh",
		"/usr/bin/fish":   "fish",
		"/bin/bash":       "bash",
		"/usr/local/dash": "bash", // unknown -> bash default
		"":                "bash",
	}
	for in, want := range cases {
		t.Setenv("SHELL", in)
		if got := detectShell(); got != want {
			t.Errorf("detectShell(SHELL=%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRunShellEmitsSnippet(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runShell([]string{"install", "bash"}); err != nil {
			t.Fatalf("runShell: %v", err)
		}
	})
	if !strings.Contains(out, "builtin cd") {
		t.Errorf("runShell bash output missing the cd shim:\n%s", out)
	}
}

func TestRunShellUnsupported(t *testing.T) {
	if err := runShell([]string{"install", "tcsh"}); err == nil {
		t.Error("runShell accepted an unsupported shell")
	}
}

func TestWriteCDDirective(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "directive")
	t.Setenv(cdDirectiveEnv, file)
	writeCDDirective("/some/where")
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read directive: %v", err)
	}
	if string(got) != "/some/where" {
		t.Errorf("directive = %q, want /some/where", got)
	}
}

func TestWriteCDDirectiveNoEnvIsNoop(t *testing.T) {
	t.Setenv(cdDirectiveEnv, "")
	// Must not panic or error when the shim is not installed.
	writeCDDirective("/ignored")
}

func TestNavSummary(t *testing.T) {
	// In-place move: same text regardless of the shim.
	t.Setenv(cdDirectiveEnv, "")
	if got := navSummary("switched to", "feat", ""); got != "switched to feat" {
		t.Errorf("navSummary in-place = %q", got)
	}

	// Teleport WITH the shim active: the parent shell is moved, so reporting the
	// switch (with the worktree path) is accurate.
	t.Setenv(cdDirectiveEnv, "/tmp/cd")
	if got := navSummary("switched to", "feat", "/wt/feat"); got != "switched to feat (worktree: /wt/feat)" {
		t.Errorf("navSummary teleport with shim = %q", got)
	}

	// Teleport WITHOUT the shim: the parent shell did NOT move, so the summary
	// must not claim a switch and must hand the user an actionable cd command.
	t.Setenv(cdDirectiveEnv, "")
	got := navSummary("switched to", "feat", "/wt/feat")
	if strings.Contains(got, "switched") {
		t.Errorf("navSummary teleport without shim must not claim a switch: %q", got)
	}
	if !strings.Contains(got, "cd /wt/feat") {
		t.Errorf("navSummary teleport without shim must suggest cd: %q", got)
	}
}

func TestShimActive(t *testing.T) {
	t.Setenv(cdDirectiveEnv, "")
	if shimActive() {
		t.Error("shimActive true with ST_CD_FILE unset")
	}
	t.Setenv(cdDirectiveEnv, "/tmp/cd")
	if !shimActive() {
		t.Error("shimActive false with ST_CD_FILE set")
	}
}
