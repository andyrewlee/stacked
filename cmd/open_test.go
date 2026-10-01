package cmd

// Tests for st open: the compare-URL derivation reuses submit's engine pieces;
// the spawn seam (openFunc) is stubbed so no test ever launches a browser.

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// stubOpen replaces the browser spawn with a recorder and returns it.
func stubOpen(t *testing.T) *[]string {
	t.Helper()
	var opened []string
	old := openFunc
	openFunc = func(url string) error {
		opened = append(opened, url)
		return nil
	}
	t.Cleanup(func() { openFunc = old })
	return &opened
}

// forbidOpen arms the spawn seam to fail the test if anything launches.
func forbidOpen(t *testing.T) {
	t.Helper()
	old := openFunc
	openFunc = func(url string) error {
		t.Errorf("spawned a browser for %q", url)
		return nil
	}
	t.Cleanup(func() { openFunc = old })
}

// addGitHubRemote registers a github-shaped origin (no network needed — the
// URL only feeds compare-link derivation).
func addGitHubRemote(t *testing.T) {
	t.Helper()
	mustRun(t, "git", "remote", "add", "origin", "git@github.com:owner/repo.git")
}

// TestOpenCurrentBranch: bare open spawns exactly the current branch's compare
// URL — not the whole stack path.
func TestOpenCurrentBranch(t *testing.T) {
	newRepo(t)
	mustInit(t)
	addGitHubRemote(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	opened := stubOpen(t)

	out := captureStdout(t, func() {
		if err := runOpen(nil); err != nil {
			t.Fatalf("open: %v", err)
		}
	})
	want := "https://github.com/owner/repo/compare/feat-a...feat-b"
	if !reflect.DeepEqual(*opened, []string{want}) {
		t.Fatalf("opened = %v, want [%s]", *opened, want)
	}
	if !strings.Contains(out, "opened feat-b -> feat-a") {
		t.Fatalf("output missing opened line:\n%s", out)
	}
}

// TestOpenAll: --all opens every tracked branch's compare URL in
// parents-first order.
func TestOpenAll(t *testing.T) {
	newRepo(t)
	mustInit(t)
	addGitHubRemote(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCheckout(t, "main")
	opened := stubOpen(t)

	captureStdout(t, func() {
		if err := runOpen([]string{"--all"}); err != nil {
			t.Fatalf("open --all: %v", err)
		}
	})
	want := []string{
		"https://github.com/owner/repo/compare/main...feat-a",
		"https://github.com/owner/repo/compare/feat-a...feat-b",
	}
	if !reflect.DeepEqual(*opened, want) {
		t.Fatalf("opened = %v, want %v", *opened, want)
	}
}

// TestOpenDryRunNoSpawn: --dry-run prints the URLs and spawns nothing.
func TestOpenDryRunNoSpawn(t *testing.T) {
	newRepo(t)
	mustInit(t)
	addGitHubRemote(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	forbidOpen(t)

	out := captureStdout(t, func() {
		if err := runOpen([]string{"--dry-run"}); err != nil {
			t.Fatalf("open --dry-run: %v", err)
		}
	})
	if !strings.Contains(out, "would open feat-a -> main  https://github.com/owner/repo/compare/main...feat-a") {
		t.Fatalf("dry-run output = %q", out)
	}
}

// TestOpenJSONNoSpawn: --json emits the URL list as data and never spawns.
func TestOpenJSONNoSpawn(t *testing.T) {
	newRepo(t)
	mustInit(t)
	addGitHubRemote(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	forbidOpen(t)

	out := captureStdout(t, func() {
		if err := runOpen([]string{"--json"}); err != nil {
			t.Fatalf("open --json: %v", err)
		}
	})
	requireJSONObjectKeys(t, "open --json", out, "remote", "dryRun", "repoURL", "prHints")
	var got openResult
	decodeStrictJSON(t, "open --json", out, &got)
	if got.Remote != "origin" || got.DryRun {
		t.Fatalf("payload = %+v", got)
	}
	want := []prHint{{Head: "feat-a", Base: "main", CompareURL: "https://github.com/owner/repo/compare/main...feat-a"}}
	if !reflect.DeepEqual(got.PRHints, want) {
		t.Fatalf("prHints = %+v, want %+v", got.PRHints, want)
	}
}

// TestOpenUnknownRemote: a remote whose URL is not a recognized forge shape
// yields the "nothing to open" outcome — exit 0, no spawn, no URLs.
func TestOpenUnknownRemote(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	forbidOpen(t)

	out := captureStdout(t, func() {
		if err := runOpen(nil); err != nil {
			t.Fatalf("open with unrecognized remote: %v", err)
		}
	})
	if !strings.Contains(out, "nothing to open") {
		t.Fatalf("unrecognized-remote output = %q", out)
	}
}

// TestOpenNothingToOpen: at trunk and with an empty forest the command reports
// rather than errors.
func TestOpenNothingToOpen(t *testing.T) {
	newRepo(t)
	mustInit(t)
	addGitHubRemote(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCheckout(t, "main")
	forbidOpen(t)

	out := captureStdout(t, func() {
		if err := runOpen(nil); err != nil {
			t.Fatalf("open at trunk: %v", err)
		}
	})
	if !strings.Contains(out, "at trunk; nothing to open") {
		t.Fatalf("at-trunk output = %q", out)
	}
}

// TestOpenErrors: the honest failures — no remote configured, untracked
// current branch, a positional argument.
func TestOpenErrors(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	forbidOpen(t)

	if err := runOpen(nil); err == nil || !strings.Contains(err.Error(), `remote "origin" does not exist`) {
		t.Fatalf("open without remote = %v", err)
	}

	addGitHubRemote(t)
	mustRun(t, "git", "checkout", "-qb", "untracked")
	if err := runOpen(nil); err == nil || !strings.Contains(err.Error(), "not tracked") {
		t.Fatalf("open on untracked branch = %v", err)
	}

	if err := runOpen([]string{"extra"}); err == nil {
		t.Fatalf("open with positional = nil, want usage error")
	}
}

// TestOpenSpawnFailure: a failing opener surfaces as an error naming the URL.
func TestOpenSpawnFailure(t *testing.T) {
	newRepo(t)
	mustInit(t)
	addGitHubRemote(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	old := openFunc
	openFunc = func(url string) error {
		return errors.New("xdg-open not found — install it or open the URL manually")
	}
	t.Cleanup(func() { openFunc = old })

	captureStdout(t, func() {
		err := runOpen(nil)
		if err == nil || !strings.Contains(err.Error(), "github.com") {
			t.Fatalf("open spawn failure = %v", err)
		}
	})
}

// TestOpenInBrowserSpawnsOpener: the platform spawn passes the URL as a single
// argv element to a PATH-resolved binary — a stub opener proves the wiring
// without launching a real browser. Windows opens via rundll32, which a PATH
// shim cannot intercept, so the success path is unix-only.
func TestOpenInBrowserSpawnsOpener(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows opens via rundll32, which a PATH shim cannot intercept")
	}
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	stub := "#!/bin/sh\nprintf '%s' \"$1\" > \"" + marker + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := openInBrowser("https://example.com/x"); err != nil {
		t.Fatalf("openInBrowser: %v", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("stub opener did not run: %v", err)
	}
	if string(got) != "https://example.com/x" {
		t.Fatalf("opener received argv %q, want the URL alone", got)
	}
}

// TestOpenInBrowserMissingOpener: with no opener on PATH the error is a clear
// install hint, not a crash.
func TestOpenInBrowserMissingOpener(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows opens via rundll32, which is always present")
	}
	t.Setenv("PATH", t.TempDir())
	err := openInBrowser("https://example.com/x")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("openInBrowser without an opener = %v, want a not-found hint", err)
	}
}
