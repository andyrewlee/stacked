package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Open contract: `st open` derives compare URLs from the recorded remote and
// never pushes or calls a host API. --dry-run/--json print the URLs; the bare
// command spawns the platform opener (proven via a PATH shim).

// TestOpenDryRun pins the URL derivation end to end: a github-shaped remote
// plus a tracked branch yields the compare URL in both JSON and text output.
func TestOpenDryRun(t *testing.T) {
	t.Parallel()
	r := newRepo(t)

	r.git("remote", "add", "origin", "git@github.com:owner/repo.git")
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")

	wantURL := "https://github.com/owner/repo/compare/feat-a...feat-b"
	res := r.st("open", "--dry-run", "--json")
	wantExit(t, res, 0)
	var payload struct {
		DryRun  bool   `json:"dryRun"`
		RepoURL string `json:"repoURL"`
		PRHints []struct {
			Head       string `json:"head"`
			Base       string `json:"base"`
			CompareURL string `json:"compareURL"`
		} `json:"prHints"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &payload); err != nil {
		t.Fatalf("open --json invalid: %v\n%s", err, res.stdout)
	}
	if !payload.DryRun {
		t.Fatal("dryRun = false, want true")
	}
	if payload.RepoURL != "https://github.com/owner/repo" {
		t.Fatalf("repoURL = %q, want the https github URL", payload.RepoURL)
	}
	// Bare open resolves the current branch only (HEAD is on feat-b after the
	// last create).
	if len(payload.PRHints) != 1 {
		t.Fatalf("prHints = %+v, want exactly the current branch's hint", payload.PRHints)
	}
	h := payload.PRHints[0]
	if h.Head != "feat-b" || h.Base != "feat-a" {
		t.Fatalf("hint = %+v, want feat-b onto feat-a", h)
	}
	if h.CompareURL != wantURL {
		t.Fatalf("compareURL = %q, want %q", h.CompareURL, wantURL)
	}

	// Text mode prints the same URL and still spawns nothing.
	res = r.st("open", "--dry-run")
	wantExit(t, res, 0)
	wantStdoutContains(t, res, wantURL)
	wantStdoutContains(t, res, "would open")
}

// TestOpenSpawnsOpener pins the spawn arm: a stub platform opener on PATH
// must be invoked with the compare URL as its single argument — the only
// non-git process st ever launches. Unix-only: Windows opens via rundll32,
// which a PATH shim cannot intercept.
func TestOpenSpawnsOpener(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("windows opens via rundll32, which a PATH shim cannot intercept")
	}
	r := newRepo(t)

	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	shimDir := t.TempDir()
	marker := filepath.Join(shimDir, "ran")
	stub := "#!/bin/sh\nprintf '%s' \"$1\" > \"" + marker + "\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, name), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	r.git("remote", "add", "origin", "git@github.com:owner/repo.git")
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")

	res := r.stInEnv(r.dir, []string{"PATH=" + shimDir + string(os.PathListSeparator) + os.Getenv("PATH")}, "open")
	wantExit(t, res, 0)
	wantStdoutContains(t, res, "opened feat-a -> main")

	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("stub opener did not run: %v\nstdout:\n%s", err, res.stdout)
	}
	want := "https://github.com/owner/repo/compare/main...feat-a"
	if string(got) != want {
		t.Fatalf("opener argv = %q, want %q as the single argument", got, want)
	}
}

// TestOpenUnrecognizedRemote pins the no-URL arm: a remote shape st cannot
// turn into a compare URL reports the reason instead of a partial result.
func TestOpenUnrecognizedRemote(t *testing.T) {
	t.Parallel()
	r := newRepo(t)

	r.git("remote", "add", "origin", "/local/path/remote.git")
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")

	res := r.st("open")
	wantExit(t, res, 0)
	if !strings.Contains(res.stdout, "unrecognized host") {
		t.Fatalf("open stdout = %q, want the unrecognized-host reason", res.stdout)
	}
}
