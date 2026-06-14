package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"stacked/internal/git"
	"stacked/internal/stack"
)

// Read-only command output and the machine (JSON) contract: log, status,
// submit, completion, guide, and the quiet-git JSON environment.
// --- log -------------------------------------------------------------------

func TestLogTextAndJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCheckout(t, "feat-b")

	// Text output marks the current branch and lists trunk + both branches.
	text := captureStdout(t, func() {
		if err := runLog(nil); err != nil {
			t.Fatalf("log: %v", err)
		}
	})
	for _, want := range []string{"main", "feat-a", "feat-b"} {
		if !strings.Contains(text, want) {
			t.Fatalf("log text missing %q:\n%s", want, text)
		}
	}

	// JSON output is a valid tree rooted at trunk with the documented fields.
	jsonOut := captureStdout(t, func() {
		if err := runLog([]string{"--json"}); err != nil {
			t.Fatalf("log --json: %v", err)
		}
	})

	var root logNode
	if err := json.Unmarshal([]byte(jsonOut), &root); err != nil {
		t.Fatalf("log --json not valid JSON: %v\n%s", err, jsonOut)
	}
	if root.Name != "main" {
		t.Fatalf("root name = %q, want main", root.Name)
	}
	if len(root.Children) != 1 || root.Children[0].Name != "feat-a" {
		t.Fatalf("root children wrong: %+v", root.Children)
	}
	a := root.Children[0]
	if a.Parent != "main" {
		t.Fatalf("feat-a parent = %q, want main", a.Parent)
	}
	if len(a.Children) != 1 || a.Children[0].Name != "feat-b" {
		t.Fatalf("feat-a children wrong: %+v", a.Children)
	}
	b := a.Children[0]
	if !b.Current {
		t.Fatalf("feat-b should be marked current: %+v", b)
	}
	if b.ParentSHA == "" {
		t.Fatalf("feat-b should carry a parentSHA: %+v", b)
	}
	if b.TopCommit != "b" {
		t.Fatalf("feat-b topCommit = %q, want b", b.TopCommit)
	}
}

func TestLogJSONTrunkOnly(t *testing.T) {
	newRepo(t)
	mustInit(t)

	jsonOut := captureStdout(t, func() {
		if err := runLog([]string{"--json"}); err != nil {
			t.Fatalf("log --json: %v", err)
		}
	})
	var root logNode
	if err := json.Unmarshal([]byte(jsonOut), &root); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jsonOut)
	}
	if root.Name != "main" || len(root.Children) != 0 {
		t.Fatalf("trunk-only tree wrong: %+v", root)
	}
}

func TestLogNeedsRestackFlag(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b") // independent file: no conflict

	// Advance feat-a with a raw git commit (no auto-restack), so feat-b's
	// recorded parentSHA drifts and the JSON should flag needsRestack=true.
	mustCheckout(t, "feat-a")
	write(t, "a2.txt", "a2\n")
	mustRun(t, "git", "add", "-A")
	mustRun(t, "git", "commit", "-q", "-m", "a2")

	jsonOut := captureStdout(t, func() {
		if err := runLog([]string{"--json"}); err != nil {
			t.Fatalf("log --json: %v", err)
		}
	})
	var root logNode
	if err := json.Unmarshal([]byte(jsonOut), &root); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jsonOut)
	}
	// root(main) -> feat-a -> feat-b; feat-b should need a restack.
	a := root.Children[0]
	b := a.Children[0]
	if !b.NeedsRestack {
		t.Fatalf("feat-b should report needsRestack=true after parent moved: %+v", b)
	}
}

func TestLogOmitsTopCommitWhenBranchTipIsReachableFromParent(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	if err := runCreate([]string{"feat-b"}); err != nil {
		t.Fatalf("create feat-b: %v", err)
	}

	mustCheckout(t, "feat-a")
	write(t, "a2.txt", "a2\n")
	mustRun(t, "git", "add", "-A")
	mustRun(t, "git", "commit", "-q", "-m", "a2")

	jsonOut := captureStdout(t, func() {
		if err := runLog([]string{"--json"}); err != nil {
			t.Fatalf("log --json: %v", err)
		}
	})
	var root logNode
	if err := json.Unmarshal([]byte(jsonOut), &root); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jsonOut)
	}
	b := root.Children[0].Children[0]
	if b.TopCommit != "" {
		t.Fatalf("feat-b topCommit = %q, want empty for branch behind parent", b.TopCommit)
	}
}

// --- status ----------------------------------------------------------------

type statusPayload struct {
	Branch        string   `json:"branch"`
	Role          string   `json:"role"`
	Parent        string   `json:"parent"`
	Children      []string `json:"children"`
	NeedsRestack  *bool    `json:"needsRestack"`
	WorktreeClean bool     `json:"worktreeClean"`
}

func TestStatusTextAndJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCheckout(t, "feat-a")

	text := captureStdout(t, func() {
		if err := runStatus(nil); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	for _, want := range []string{"branch:", "feat-a", "tracked", "feat-b"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status text missing %q:\n%s", want, text)
		}
	}

	jsonOut := captureStdout(t, func() {
		if err := runStatus([]string{"--json"}); err != nil {
			t.Fatalf("status --json: %v", err)
		}
	})
	var p statusPayload
	if err := json.Unmarshal([]byte(jsonOut), &p); err != nil {
		t.Fatalf("status --json invalid: %v\n%s", err, jsonOut)
	}
	if p.Branch != "feat-a" || p.Role != "tracked" || p.Parent != "main" {
		t.Fatalf("status payload wrong: %+v", p)
	}
	if len(p.Children) != 1 || p.Children[0] != "feat-b" {
		t.Fatalf("status children wrong: %+v", p.Children)
	}
	if p.NeedsRestack == nil || *p.NeedsRestack {
		t.Fatalf("status needsRestack want false-pointer, got %v", p.NeedsRestack)
	}
	if !p.WorktreeClean {
		t.Fatalf("status worktreeClean want true")
	}
}

func TestStatusTrunkJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)

	jsonOut := captureStdout(t, func() {
		if err := runStatus([]string{"--json"}); err != nil {
			t.Fatalf("status --json on trunk: %v", err)
		}
	})
	var p statusPayload
	if err := json.Unmarshal([]byte(jsonOut), &p); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jsonOut)
	}
	if p.Branch != "main" || p.Role != "trunk" {
		t.Fatalf("trunk status wrong: %+v", p)
	}
	if p.NeedsRestack != nil {
		t.Fatalf("trunk should have no needsRestack, got %v", *p.NeedsRestack)
	}
}

func TestStatusUntrackedJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustRun(t, "git", "checkout", "-q", "-b", "loose")

	jsonOut := captureStdout(t, func() {
		if err := runStatus([]string{"--json"}); err != nil {
			t.Fatalf("status --json on untracked: %v", err)
		}
	})
	var p statusPayload
	if err := json.Unmarshal([]byte(jsonOut), &p); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jsonOut)
	}
	if p.Branch != "loose" || p.Role != "untracked" {
		t.Fatalf("untracked status wrong: %+v", p)
	}
}

func TestStatusDirtyWorktree(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	write(t, "a.txt", "dirty\n") // unstaged change

	jsonOut := captureStdout(t, func() {
		if err := runStatus([]string{"--json"}); err != nil {
			t.Fatalf("status --json dirty: %v", err)
		}
	})
	var p statusPayload
	if err := json.Unmarshal([]byte(jsonOut), &p); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jsonOut)
	}
	if p.WorktreeClean {
		t.Fatalf("expected worktreeClean=false for a dirty tree")
	}
}

// --- submit ----------------------------------------------------------------

func TestSubmitNoRemote(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	if err := runSubmit(nil); err == nil {
		t.Fatalf("expected error: remote origin does not exist")
	}
}

func TestSubmitAtTrunk(t *testing.T) {
	newRepo(t)
	mustInit(t)
	// Configure a (file) remote so the remote-exists check passes.
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)

	mustCheckout(t, "main")
	out := captureStdout(t, func() {
		if err := runSubmit(nil); err != nil {
			t.Fatalf("submit at trunk: %v", err)
		}
	})
	if !strings.Contains(out, "nothing to submit") {
		t.Fatalf("expected 'nothing to submit' at trunk, got:\n%s", out)
	}
}

func TestSubmitDryRun(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)

	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCheckout(t, "feat-b")

	out := captureStdout(t, func() {
		if err := runSubmit([]string{"--dry-run"}); err != nil {
			t.Fatalf("submit --dry-run: %v", err)
		}
	})
	// Bottom-up order: feat-a then feat-b, both "would push", none pushed.
	for _, want := range []string{"would push feat-a", "would push feat-b", "dry run"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out)
		}
	}
}

// TestSubmitJSONSingleShape asserts the trunk early-return and the pushed path
// emit the same JSON object — no unknown keys in either direction.
func TestSubmitJSONSingleShape(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	decode := func(t *testing.T, raw string) submitResult {
		t.Helper()
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		var got submitResult
		if err := dec.Decode(&got); err != nil {
			t.Fatalf("submit --json did not unmarshal into the single shape: %v\n%s", err, raw)
		}
		return got
	}

	out := captureStdout(t, func() {
		if err := runSubmit([]string{"--json"}); err != nil {
			t.Fatalf("submit --json: %v", err)
		}
	})
	pushed := decode(t, out)
	if pushed.Remote != "origin" || len(pushed.Pushed) != 1 || pushed.Pushed[0] != "feat-a" {
		t.Fatalf("pushed-case payload = %+v, want feat-a pushed to origin", pushed)
	}

	mustCheckout(t, "main")
	out = captureStdout(t, func() {
		if err := runSubmit([]string{"--json"}); err != nil {
			t.Fatalf("submit --json at trunk: %v", err)
		}
	})
	trunk := decode(t, out)
	if trunk.Summary != "at trunk; nothing to submit" {
		t.Fatalf("trunk-case summary = %q, want the nothing-to-submit summary", trunk.Summary)
	}
	if trunk.Pushed == nil || len(trunk.Pushed) != 0 {
		t.Fatalf("trunk-case pushed = %v, want present-but-empty", trunk.Pushed)
	}
}

// TestSubmitPartialFailureJSON asserts that when a push fails partway up the
// stack, --json still reports the branches pushed so far plus the one that
// failed, so a machine consumer can observe the partial state (the command
// still returns a non-zero error).
func TestSubmitPartialFailureJSON(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")

	// A server-side hook rejects feat-b, so feat-a is pushed but feat-b fails.
	hook := filepath.Join(remoteDir, "hooks", "pre-receive")
	script := "#!/bin/sh\nwhile read _ _ ref; do\n\t[ \"$ref\" = refs/heads/feat-b ] && exit 1\ndone\nexit 0\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}

	var runErr error
	out := captureStdout(t, func() {
		runErr = runSubmit([]string{"--json"})
	})
	if runErr == nil {
		t.Fatal("submit with a rejected branch should return a non-nil error")
	}

	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	var got submitResult
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("partial submit --json did not unmarshal into submitResult: %v\n%s", err, out)
	}
	if got.Failed != "feat-b" {
		t.Fatalf("partial result Failed = %q, want feat-b", got.Failed)
	}
	if len(got.Pushed) != 1 || got.Pushed[0] != "feat-a" {
		t.Fatalf("partial result Pushed = %v, want [feat-a]", got.Pushed)
	}
}

func TestSubmitPartialFailureJSONFirstBranchKeepsPushedArray(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	hook := filepath.Join(remoteDir, "hooks", "pre-receive")
	script := "#!/bin/sh\nexit 1\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}

	var runErr error
	out := captureStdout(t, func() {
		runErr = runSubmit([]string{"--json"})
	})
	if runErr == nil {
		t.Fatal("submit with a rejected first branch should return a non-nil error")
	}

	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	var got submitResult
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("partial submit --json did not unmarshal into submitResult: %v\n%s", err, out)
	}
	if got.Failed != "feat-a" {
		t.Fatalf("partial result Failed = %q, want feat-a", got.Failed)
	}
	if got.Pushed == nil || len(got.Pushed) != 0 {
		t.Fatalf("partial result Pushed = %v, want present-but-empty", got.Pushed)
	}
}

func TestSubmitUsesSelectedRemote(t *testing.T) {
	newRepo(t)
	mustInit(t)
	upstream := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", upstream)
	mustRun(t, "git", "remote", "add", "upstream", upstream)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	if err := runSubmit([]string{"--remote", "upstream"}); err != nil {
		t.Fatalf("submit --remote upstream: %v", err)
	}
	if out := mustRun(t, "git", "--git-dir", upstream, "rev-parse", "--verify", "refs/heads/feat-a"); out == "" {
		t.Fatal("feat-a was not pushed to upstream")
	}
}

func TestSubmitRejectsPositionalArgs(t *testing.T) {
	if err := runSubmit([]string{"typo", "--dry-run"}); err == nil {
		t.Fatal("submit accepted a positional argument")
	}
	if err := runSubmit([]string{"origin", "--remote"}); err == nil {
		t.Fatal("submit accepted a positional argument before a missing-value flag")
	}
}

func TestRemoteToHTTPSStripsCredentials(t *testing.T) {
	webURL, host := remoteToHTTPS("https://TOKEN@example.com/owner/repo.git?access_token=SECRET#frag")
	if webURL != "https://example.com/owner/repo" || host != "example.com" {
		t.Fatalf("credential URL converted to (%q, %q), want sanitized example.com URL", webURL, host)
	}
	if strings.Contains(webURL, "TOKEN") || strings.Contains(webURL, "SECRET") || strings.Contains(webURL, "frag") || strings.Contains(host, "TOKEN") {
		t.Fatalf("credential leaked in converted remote: %q %q", webURL, host)
	}

	webURL, host = remoteToHTTPS("git@example.com:owner/repo.git?token=SECRET#frag")
	if webURL != "https://example.com/owner/repo" || host != "example.com" {
		t.Fatalf("scp-like URL converted to (%q, %q), want sanitized example.com URL", webURL, host)
	}
	if strings.Contains(webURL, "SECRET") || strings.Contains(webURL, "token") || strings.Contains(webURL, "frag") {
		t.Fatalf("scp-like credential leaked in converted remote: %q %q", webURL, host)
	}

	webURL, host = remoteToHTTPS("ssh://user:SECRET@example.com/owner/repo.git?token=SECRET#frag")
	if webURL != "https://example.com/owner/repo" || host != "example.com" {
		t.Fatalf("credential SSH URL converted to (%q, %q), want sanitized example.com URL", webURL, host)
	}
	if strings.Contains(webURL, "SECRET") || strings.Contains(webURL, "user") || strings.Contains(webURL, "token") || strings.Contains(webURL, "frag") {
		t.Fatalf("SSH credential leaked in converted remote: %q %q", webURL, host)
	}

	webURL, host = remoteToHTTPS("ssh://git@[2001:db8::1]/owner/repo.git")
	if webURL != "https://[2001:db8::1]/owner/repo" || host != "2001:db8::1" {
		t.Fatalf("IPv6 SSH URL converted to (%q, %q), want bracketed web URL", webURL, host)
	}

	webURL, host = remoteToHTTPS("ssh://user:SECRET@example.com:2222/owner/repo.git")
	if webURL != "https://example.com:2222/owner/repo" || host != "example.com" {
		t.Fatalf("credential SSH URL with port converted to (%q, %q), want sanitized example.com:2222 URL", webURL, host)
	}
}

func TestSubmitUntracked(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)

	mustRun(t, "git", "checkout", "-q", "-b", "loose")
	write(t, "x.txt", "x\n")
	mustRun(t, "git", "add", "-A")
	mustRun(t, "git", "commit", "-q", "-m", "x")
	if err := runSubmit(nil); err == nil {
		t.Fatalf("expected error submitting an untracked branch")
	}
}

// --- completion ------------------------------------------------------------

func TestCompletionShells(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		shell := shell
		out := captureStdout(t, func() {
			if err := runCompletion([]string{shell}); err != nil {
				t.Fatalf("completion %s: %v", shell, err)
			}
		})
		if strings.TrimSpace(out) == "" {
			t.Fatalf("completion %s produced no output", shell)
		}
		// Every script should mention the "create" subcommand somewhere.
		if !strings.Contains(out, "create") && !strings.Contains(out, "st") {
			t.Fatalf("completion %s missing command list:\n%s", shell, out)
		}
	}
}

func TestCompletionErrors(t *testing.T) {
	if err := runCompletion(nil); err == nil {
		t.Fatalf("expected error: completion needs a shell argument")
	}
	if err := runCompletion([]string{"powershell"}); err == nil {
		t.Fatalf("expected error: unsupported shell")
	}
	if err := runCompletion([]string{"bash", "extra"}); err == nil {
		t.Fatalf("expected error: too many args")
	}
}

func TestNoArgReadOnlyCommandsRejectPositionalArgs(t *testing.T) {
	tests := []struct {
		name string
		run  func([]string) error
	}{
		{"guide", runGuide},
		{"log", runLog},
		{"status", runStatus},
		{"validate", runValidate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run([]string{"unexpected"}); err == nil {
				t.Fatalf("%s accepted a positional argument", tt.name)
			}
		})
	}
}

func TestGuideDoesNotReferenceMissingDocs(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runGuide([]string{"--json"}); err != nil {
			t.Fatalf("guide --json: %v", err)
		}
	})
	if strings.Contains(out, "docs/AGENT.md") {
		t.Fatalf("guide references missing docs path:\n%s", out)
	}
	var payload struct {
		Docs string `json:"docs"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("guide JSON invalid: %v\n%s", err, out)
	}
	if payload.Docs == "" {
		t.Fatal("guide JSON omitted docs guidance")
	}
}

func TestJSONStackEnvUsesQuietGit(t *testing.T) {
	orig := gitShell
	gitShell = git.Shell{}
	defer func() { gitShell = orig }()

	if _, ok := stackEnv(&stack.State{}, true).Git.(git.QuietShell); !ok {
		t.Fatal("JSON stack env did not use QuietShell")
	}
	if _, ok := stackEnv(&stack.State{}, false).Git.(git.Shell); !ok {
		t.Fatal("text stack env did not use Shell")
	}
}

// TestStatusJSONSurfacesConflict drives a restack into a conflict and asserts
// status --json reports the paused rebase (branch + conflicted files) instead of
// failing on the detached HEAD a rebase leaves behind.
func TestStatusJSONSurfacesConflict(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "f.txt", "A\n", "a")
	mustCreate(t, "feat-b", "f.txt", "A\nB\n", "b")
	mustCheckout(t, "feat-a")
	write(t, "f.txt", "X\n")
	if err := runModify([]string{"-a"}); err == nil {
		t.Fatal("expected a conflict restacking feat-b onto the amended feat-a")
	}

	out := captureStdout(t, func() {
		if err := runStatus([]string{"--json"}); err != nil {
			t.Fatalf("status --json during a conflict: %v", err)
		}
	})
	var st struct {
		RebaseInProgress bool     `json:"rebaseInProgress"`
		RebaseBranch     string   `json:"rebaseBranch"`
		ConflictedFiles  []string `json:"conflictedFiles"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("status --json not parseable: %v\n%s", err, out)
	}
	if !st.RebaseInProgress {
		t.Error("status should report rebaseInProgress during a conflict")
	}
	if st.RebaseBranch != "feat-b" {
		t.Errorf("rebaseBranch = %q, want feat-b", st.RebaseBranch)
	}
	if len(st.ConflictedFiles) == 0 {
		t.Error("status should list the conflicted files during a conflict")
	}
}

// --- AGENT.md result-shape contract ----------------------------------------
//
// docs/AGENT.md is the machine interface agents script against. There is
// exit-code drift protection (TestExitCodeAndErrorCodeMapping); these two tests
// are the equivalent guard for the JSON result-shape contract — the drift that
// shipped submitResult.Failed undocumented. They check the *forward* direction:
// every field the code emits must be documented (a documented-but-unemitted key
// is allowed, e.g. an omitempty field absent on a happy path).

// agentDoc reads docs/AGENT.md relative to this source file (the cmd package
// sits one directory below the repo root), mirroring testdataDir's
// runtime.Caller trick so the lookup survives a test that has t.Chdir'd into a
// temp repo.
func agentDoc(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(file), "..", "docs", "AGENT.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// documentsKey reports whether AGENT.md documents key as a JSON key ("key") or a
// backticked code span (`key`) — the two forms the doc uses — so an incidental
// prose word does not count as documentation.
func documentsKey(doc, key string) bool {
	return strings.Contains(doc, `"`+key+`"`) || strings.Contains(doc, "`"+key+"`")
}

// TestAgentDocDocumentsContractStructs pins the central named result structs to
// AGENT.md. Their omitempty fields (failed, branch, restacked, deleted, notes,
// dryRun) do not appear on a happy-path run, so reflection — not execution — is
// the only way to catch a new undocumented field.
func TestAgentDocDocumentsContractStructs(t *testing.T) {
	doc := agentDoc(t)
	for _, typ := range []reflect.Type{
		reflect.TypeOf(submitResult{}),
		reflect.TypeOf(stack.OpResult{}),
		reflect.TypeOf(initResult{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			tag := typ.Field(i).Tag.Get("json")
			if tag == "" || tag == "-" {
				continue
			}
			key := strings.Split(tag, ",")[0]
			if key == "" {
				continue
			}
			if !documentsKey(doc, key) {
				t.Errorf("docs/AGENT.md does not document %s JSON key %q", typ.Name(), key)
			}
		}
	}
}

// collectJSONKeys records every object key at any depth of a decoded JSON value.
func collectJSONKeys(v any, into map[string]bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			into[k] = true
			collectJSONKeys(val, into)
		}
	case []any:
		for _, e := range t {
			collectJSONKeys(e, into)
		}
	}
}

// TestAgentDocDocumentsEmittedKeys runs the read/navigation commands that emit
// anonymous inline structs (status, checkout, validate, navigation, log) — which
// reflection cannot reach by name — and asserts every JSON key they actually
// emit is documented in AGENT.md. The fixture surfaces status's omitempty keys
// (parent, needsRestack) by running on a tracked branch with a parent and child.
func TestAgentDocDocumentsEmittedKeys(t *testing.T) {
	doc := agentDoc(t)
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "add a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "add b")
	mustCheckout(t, "feat-a") // tracked, parent main, child feat-b

	keys := map[string]bool{}
	collect := func(label string, run func() error) {
		var perr error
		got := captureStdout(t, func() { perr = run() })
		if perr != nil {
			t.Fatalf("%s: %v", label, perr)
		}
		got = strings.TrimSpace(got)
		if got == "" {
			t.Fatalf("%s: emitted no JSON", label)
		}
		var v any
		if err := json.Unmarshal([]byte(got), &v); err != nil {
			t.Fatalf("%s: emitted invalid JSON %q: %v", label, got, err)
		}
		collectJSONKeys(v, keys)
	}

	collect("status --json", func() error { return runStatus([]string{"--json"}) })
	collect("checkout --json", func() error { return runCheckout([]string{"--json"}) })
	collect("checkout feat-a --json", func() error { return runCheckout([]string{"feat-a", "--json"}) })
	collect("validate --json", func() error { return runValidate([]string{"--json"}) })
	collect("log --json", func() error { return runLog([]string{"--json"}) })
	// Navigation moves HEAD; the emitted keys do not depend on the destination.
	collect("top --json", func() error { return runTop([]string{"--json"}) })
	collect("bottom --json", func() error { return runBottom([]string{"--json"}) })
	collect("up --json", func() error { return runUp([]string{"--json"}) })
	collect("down --json", func() error { return runDown([]string{"--json"}) })

	// Drive status into a paused rebase so its omitempty conflict keys
	// (rebaseInProgress/rebaseBranch/conflictedFiles) are emitted and checked too.
	newRepo(t)
	mustInit(t)
	mustCreate(t, "conf-a", "f.txt", "A\n", "a")
	mustCreate(t, "conf-b", "f.txt", "A\nB\n", "b")
	mustCheckout(t, "conf-a")
	write(t, "f.txt", "X\n")
	if err := runModify([]string{"-a"}); err == nil {
		t.Fatal("expected a conflict restacking conf-b onto the amended conf-a")
	}
	collect("status --json (conflict)", func() error { return runStatus([]string{"--json"}) })

	for k := range keys {
		if !documentsKey(doc, k) {
			t.Errorf("docs/AGENT.md does not document emitted JSON key %q", k)
		}
	}
}
