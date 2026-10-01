package e2e

// Black-box coverage of the __complete endpoint protocol and of the generated
// scripts' dynamic path: candidate bytes must reach the shell's completion
// machinery as DATA — a refname-shaped-like-execution must never run.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// sentinelBranch is a refname-legal name that is a working command
// substitution under POSIX shells — on unix it also carries a redirection,
// so if any layer of the completion pipeline EVALUATES candidate text
// instead of carrying it as data, the write leaves sentinelMarker on disk.
// Windows forbids `>` in filenames and loose refs are filename-backed, so no
// redirect can appear in a branch name there; `$(id)` alone still detects
// substitution because an evaluated candidate becomes uid=… text, which the
// byte-exact assertions catch — and there is no marker to check.
var (
	sentinelBranch = "$(id>pwned_sentinel)"
	sentinelMarker = "pwned_sentinel"
)

func init() {
	if runtime.GOOS == "windows" {
		sentinelBranch = "$(id)"
		sentinelMarker = ""
	}
}

// TestCompleteEndpointContract pins the protocol edge cases end to end:
// silent-empty on unknown/uninitialized/unrelated commands, usage errors on
// malformed argv, and byte-exact candidate emission.
func TestCompleteEndpointContract(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")

	res := r.st("__complete", "checkout", "0", "--")
	wantExit(t, res, 0)
	if res.stderr != "" {
		t.Fatalf("__complete wrote stderr: %q", res.stderr)
	}
	if res.stdout != "feat-a\nmain\n" {
		t.Fatalf("candidates = %q, want feat-a+main", res.stdout)
	}

	// Aliases resolve to the same policy.
	res = r.st("__complete", "co", "0", "--")
	wantExit(t, res, 0)
	if res.stdout != "feat-a\nmain\n" {
		t.Fatalf("co candidates = %q", res.stdout)
	}

	// delete's domain is tracked branches only — no trunk, no untracked.
	res = r.st("__complete", "delete", "0", "--")
	wantExit(t, res, 0)
	if res.stdout != "feat-a\n" {
		t.Fatalf("delete candidates = %q, want feat-a", res.stdout)
	}

	// Unknown command / non-branch command: silent empty, exit 0.
	for _, name := range []string{"bogus", "log"} {
		res = r.st("__complete", name, "0", "--")
		wantExit(t, res, 0)
		if res.stdout != "" || res.stderr != "" {
			t.Fatalf("__complete %s emitted %q/%q — want silence", name, res.stdout, res.stderr)
		}
	}

	// Malformed argv: usage error (exit 1), message on stderr only.
	for _, args := range [][]string{
		{},
		{"checkout"},
		{"checkout", "x", "--"},
		{"checkout", "1", "--"},
	} {
		res = r.st(append([]string{"__complete"}, args...)...)
		wantExit(t, res, 1)
		if res.stdout != "" {
			t.Fatalf("__complete %v wrote stdout %q", args, res.stdout)
		}
	}

	// Byte-exact sentinel: a substitution-shaped name passes through as data.
	r.git("checkout", "-qb", sentinelBranch)
	r.writeFile("s.txt", "s\n")
	r.git("add", "-A")
	r.git("commit", "-qm", "s")
	r.stOK("track", sentinelBranch)
	res = r.st("__complete", "checkout", "0", "--")
	wantExit(t, res, 0)
	if !strings.Contains(res.stdout, sentinelBranch) {
		t.Fatalf("sentinel name mangled: %q", res.stdout)
	}
	if sentinelMarker != "" {
		if _, err := os.Stat(filepath.Join(r.dir, sentinelMarker)); !os.IsNotExist(err) {
			t.Fatalf("endpoint evaluated candidate text — %s exists", sentinelMarker)
		}
	}
}

// TestCompleteDropsTerminalHostileNames pins plan-010 end to end: a branch
// whose name carries bytes that are legal in a refname but hostile to a
// terminal (Unicode format chars — bidi overrides, zero-width) must never be
// emitted as a completion candidate — the endpoint drops it rather than
// printing a name that displays as a different branch.
func TestCompleteDropsTerminalHostileNames(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("loose refs are filename-backed; Cf bytes in filenames are unreliable on Windows")
	}
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")

	// update-ref accepts names checkout -b refuses to even try; U+202E is a
	// legal refname byte sequence that reorders terminal display.
	hostile := "feat\u202eevil"
	r.git("update-ref", "refs/heads/"+hostile, "feat-a")
	r.stOK("track", hostile)

	res := r.st("__complete", "checkout", "0", "--")
	wantExit(t, res, 0)
	if strings.Contains(res.stdout, hostile) {
		t.Fatalf("__complete emitted a format-char name: %q", res.stdout)
	}
	if !strings.Contains(res.stdout, "feat-a") {
		t.Fatalf("__complete dropped the safe name too: %q", res.stdout)
	}
}

// TestCompleteSilentOutsideRepo: the endpoint degrades to silence where there
// is nothing to complete — no repo, or a repo with no st state.
func TestCompleteSilentOutsideRepo(t *testing.T) {
	t.Parallel()
	r := newRepo(t)

	bare := t.TempDir()
	res := r.stIn(bare, "__complete", "checkout", "0", "--")
	wantExit(t, res, 0)
	if res.stdout != "" || res.stderr != "" {
		t.Fatalf("outside a repo __complete emitted %q/%q", res.stdout, res.stderr)
	}

	// A git repo without st state: same silence.
	res = r.st("__complete", "checkout", "0", "--")
	wantExit(t, res, 0)
	if res.stdout != "" || res.stderr != "" {
		t.Fatalf("uninitialized __complete emitted %q/%q", res.stdout, res.stderr)
	}
}

// TestCompletionHiddenFromHelp: the endpoint is invisible to a human browsing
// the CLI — not in `st`'s command list, not in help --json.
func TestCompletionHiddenFromHelp(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	res := r.st()
	wantExit(t, res, 0)
	if strings.Contains(res.stdout, "__complete") {
		t.Fatalf("st help lists the hidden endpoint:\n%s", res.stdout)
	}
	res = r.stOK("help", "--json")
	if strings.Contains(res.stdout, "__complete") {
		t.Fatalf("help --json lists the hidden endpoint:\n%s", res.stdout)
	}
}

// TestCompletionBashDynamicPath executes the REAL generated bash function end
// to end: source the script, position the cursor, assert COMPREPLY carries the
// sentinel name byte-exactly and nothing was evaluated along the way.
func TestCompletionBashDynamicPath(t *testing.T) {
	t.Parallel()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.git("checkout", "-qb", sentinelBranch)
	r.writeFile("s.txt", "s\n")
	r.git("add", "-A")
	r.git("commit", "-qm", "s")
	r.stOK("track", sentinelBranch)
	r.git("checkout", "-q", "main")

	script := r.stOK("completion", "bash").stdout
	driver := script + `
COMP_WORDS=(st checkout "")
COMP_CWORD=2
_st_complete
printf '%s\n' "${COMPREPLY[@]}"
`
	cmd := exec.Command(bash, "-c", driver)
	cmd.Dir = r.dir
	cmd.Env = append(cleanEnv(r.home), "ST_COMPLETE_BIN="+stBin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash completion driver failed: %v\n%s", err, out)
	}
	got := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if !contains(got, sentinelBranch) || !contains(got, "feat-a") || !contains(got, "main") {
		t.Fatalf("bash COMPREPLY = %v, want sentinel+feat-a+main", got)
	}
	if sentinelMarker != "" {
		if _, err := os.Stat(filepath.Join(r.dir, sentinelMarker)); !os.IsNotExist(err) {
			t.Fatalf("bash completion evaluated candidate text — %s exists", sentinelMarker)
		}
	}

	// Second positional: no candidates (checkout takes one name).
	driver = script + `
COMP_WORDS=(st checkout feat-a "")
COMP_CWORD=3
_st_complete
printf '%s\n' "${COMPREPLY[@]}"
`
	cmd = exec.Command(bash, "-c", driver)
	cmd.Dir = r.dir
	cmd.Env = append(cleanEnv(r.home), "ST_COMPLETE_BIN="+stBin)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash second-positional driver failed: %v\n%s", err, out)
	}
	for _, cand := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if cand == sentinelBranch || cand == "feat-a" || cand == "main" {
			t.Fatalf("bash offered a branch at positional 2: %q", out)
		}
	}
}

// TestCompletionZshDynamicPath exercises the generated script's exact
// expansion idiom: "${(@f)$(st __complete ...)}" must split candidates on
// newlines without globbing or evaluating their bytes.
func TestCompletionZshDynamicPath(t *testing.T) {
	t.Parallel()
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not on PATH")
	}
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.git("checkout", "-qb", sentinelBranch)
	r.writeFile("s.txt", "s\n")
	r.git("add", "-A")
	r.git("commit", "-qm", "s")
	r.stOK("track", sentinelBranch)
	r.git("checkout", "-q", "main")

	// A brace/glob-hairy legal name joins the sentinel: under an unsafe
	// expansion a{b},c would brace-expand into two names.
	globby := "gl{a,b}c"
	r.git("checkout", "-qb", globby)
	r.writeFile("g.txt", "g\n")
	r.git("add", "-A")
	r.git("commit", "-qm", "g")
	r.stOK("track", globby)
	r.git("checkout", "-q", "main")

	// Drive the generated _st for real: compadd only exists inside the
	// completion system, so shadow it with a function that records candidates
	// exactly as compadd received them (one word per line).
	scriptPath := filepath.Join(t.TempDir(), "_st")
	if err := os.WriteFile(scriptPath, []byte(r.stOK("completion", "zsh").stdout), 0o644); err != nil {
		t.Fatal(err)
	}
	driver := `
compadd() { print -l -- "$@" }
source ` + scriptPath + `
words=(st checkout "")
CURRENT=3
_st
`
	cmd := exec.Command(zsh, "-f", "-c", driver)
	cmd.Dir = r.dir
	cmd.Env = append(cleanEnv(r.home), "ST_COMPLETE_BIN="+stBin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("zsh completion driver failed: %v\n%s", err, out)
	}
	got := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if !contains(got, sentinelBranch) || !contains(got, globby) {
		t.Fatalf("zsh candidates = %v, want sentinel+globby byte-exact", got)
	}
	if sentinelMarker != "" {
		if _, err := os.Stat(filepath.Join(r.dir, sentinelMarker)); !os.IsNotExist(err) {
			t.Fatalf("zsh completion evaluated candidate text — %s exists", sentinelMarker)
		}
	}
	// Brace expansion would have split gl{a,b}c into glac/glbc.
	if contains(got, "glac") || contains(got, "glbc") {
		t.Fatalf("zsh glob-expanded a candidate: %v", got)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
