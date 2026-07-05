package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"stacked/internal/stack"
)

// Read-only command output and the machine (JSON) contract: log, status,
// submit, completion, guide, and the quiet-git JSON environment.

func decodeStrictJSON(t *testing.T, label, raw string, dst any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		t.Fatalf("%s did not match its strict JSON shape: %v\n%s", label, err, raw)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("%s emitted trailing JSON/content after the payload: %v\n%s", label, err, raw)
	}
}

func requireJSONObjectKeys(t *testing.T, label, raw string, want ...string) {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("%s was not a JSON object: %v\n%s", label, err, raw)
	}
	for _, key := range want {
		if _, ok := obj[key]; !ok {
			t.Fatalf("%s missing JSON key %q in %v", label, key, sortedJSONKeys(obj))
		}
	}
	if len(obj) != len(want) {
		t.Fatalf("%s JSON keys = %v, want exactly %v", label, sortedJSONKeys(obj), want)
	}
}

func sortedJSONKeys(obj map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sameExistingPath(a, b string) bool {
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

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

func TestLogJSONOmitsUnrelatedLocalBranch(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "tracked subject")

	unrelatedName := "unrelated-log-branch"
	unrelatedSubject := "unrelated log subject 025"
	mustRun(t, "git", "checkout", "-q", "-b", unrelatedName, "main")
	write(t, "unrelated.txt", "unrelated\n")
	mustRun(t, "git", "add", "-A")
	mustRun(t, "git", "commit", "-q", "-m", unrelatedSubject)
	mustCheckout(t, "feat-a")

	jsonOut := captureStdout(t, func() {
		if err := runLog([]string{"--json"}); err != nil {
			t.Fatalf("log --json: %v", err)
		}
	})
	if strings.Contains(jsonOut, unrelatedName) {
		t.Fatalf("log --json included unrelated branch name %q:\n%s", unrelatedName, jsonOut)
	}
	if strings.Contains(jsonOut, unrelatedSubject) {
		t.Fatalf("log --json included unrelated branch subject %q:\n%s", unrelatedSubject, jsonOut)
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

// --- init ------------------------------------------------------------------

func TestInitJSONFreshAndRepeatedShape(t *testing.T) {
	newRepo(t)

	type initJSON struct {
		Trunk              string `json:"trunk"`
		Initialized        bool   `json:"initialized"`
		AlreadyInitialized bool   `json:"alreadyInitialized"`
	}

	freshOut := captureStdout(t, func() {
		if err := runInit([]string{"--trunk", "main", "--json"}); err != nil {
			t.Fatalf("fresh init --json: %v", err)
		}
	})
	requireJSONObjectKeys(t, "fresh init --json", freshOut, "trunk", "initialized", "alreadyInitialized")
	var fresh initJSON
	decodeStrictJSON(t, "fresh init --json", freshOut, &fresh)
	if fresh.Trunk != "main" || !fresh.Initialized || fresh.AlreadyInitialized {
		t.Fatalf("fresh init payload = %+v, want initialized main", fresh)
	}

	repeatedOut := captureStdout(t, func() {
		if err := runInit([]string{"--json"}); err != nil {
			t.Fatalf("repeated init --json: %v", err)
		}
	})
	requireJSONObjectKeys(t, "repeated init --json", repeatedOut, "trunk", "initialized", "alreadyInitialized")
	var repeated initJSON
	decodeStrictJSON(t, "repeated init --json", repeatedOut, &repeated)
	if repeated.Trunk != "main" || repeated.Initialized || !repeated.AlreadyInitialized {
		t.Fatalf("repeated init payload = %+v, want alreadyInitialized main", repeated)
	}
}

// --- worktree --------------------------------------------------------------

func TestWorktreeJSONCreateListRemoveShapes(t *testing.T) {
	newRepo(t)
	t.Setenv("HOME", t.TempDir())
	mustInit(t)

	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	write(t, ".gitignore", "secret.env\n")
	write(t, "secret.env", "TOKEN=1\n")
	write(t, ".worktreeinclude", "secret.env\na.txt\n")
	mustRun(t, "git", "add", ".gitignore", ".worktreeinclude")
	mustRun(t, "git", "commit", "-q", "-m", "add worktree config")
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCheckout(t, "main")
	resetWorktreeCache()

	type worktreeCreateJSON struct {
		Branch  string   `json:"branch"`
		Path    string   `json:"path"`
		Copied  []string `json:"copied"`
		Summary string   `json:"summary"`
	}
	createOut := captureStdout(t, func() {
		if err := runWorktree([]string{"feat-a", "--json"}); err != nil {
			t.Fatalf("worktree feat-a --json: %v", err)
		}
	})
	requireJSONObjectKeys(t, "worktree create --json", createOut, "branch", "path", "copied", "summary")
	var created worktreeCreateJSON
	decodeStrictJSON(t, "worktree create --json", createOut, &created)
	t.Cleanup(func() {
		resetWorktreeCache()
		_ = runWorktree([]string{"rm", "feat-a"})
	})
	if created.Branch != "feat-a" || created.Path == "" || created.Summary != "created worktree" {
		t.Fatalf("worktree create payload = %+v, want created feat-a worktree", created)
	}
	if want := []string{"secret.env"}; !reflect.DeepEqual(created.Copied, want) {
		t.Fatalf("worktree create copied = %v, want %v", created.Copied, want)
	}

	resetWorktreeCache()
	listOut := captureStdout(t, func() {
		if err := runWorktree([]string{"ls", "--json"}); err != nil {
			t.Fatalf("worktree ls --json: %v", err)
		}
	})
	var rawEntries []json.RawMessage
	if err := json.Unmarshal([]byte(listOut), &rawEntries); err != nil {
		t.Fatalf("worktree ls --json did not emit an array: %v\n%s", err, listOut)
	}
	for i, raw := range rawEntries {
		requireJSONObjectKeys(t, fmt.Sprintf("worktree ls --json entry %d", i), string(raw), "path", "branch", "head")
	}
	type worktreeListJSON struct {
		Path   string `json:"path"`
		Branch string `json:"branch"`
		Head   string `json:"head"`
	}
	var listed []worktreeListJSON
	decodeStrictJSON(t, "worktree ls --json", listOut, &listed)
	if len(listed) != 2 {
		t.Fatalf("worktree ls entries = %+v, want main and feat-a", listed)
	}
	byBranch := map[string]worktreeListJSON{}
	for _, entry := range listed {
		if entry.Path == "" || entry.Branch == "" || entry.Head == "" {
			t.Fatalf("worktree ls entry missing populated fields: %+v", entry)
		}
		byBranch[entry.Branch] = entry
	}
	if byBranch["main"].Path != root {
		if !sameExistingPath(byBranch["main"].Path, root) {
			t.Fatalf("main worktree path = %q, want %q", byBranch["main"].Path, root)
		}
	}
	listedFeatPath := byBranch["feat-a"].Path
	if !sameExistingPath(listedFeatPath, created.Path) {
		t.Fatalf("feat-a worktree path = %q, want %q", listedFeatPath, created.Path)
	}

	resetWorktreeCache()
	type worktreeRemoveJSON struct {
		Branch  string `json:"branch"`
		Removed string `json:"removed"`
	}
	removeOut := captureStdout(t, func() {
		if err := runWorktree([]string{"rm", "feat-a", "--json"}); err != nil {
			t.Fatalf("worktree rm feat-a --json: %v", err)
		}
	})
	requireJSONObjectKeys(t, "worktree rm --json", removeOut, "branch", "removed")
	var removed worktreeRemoveJSON
	decodeStrictJSON(t, "worktree rm --json", removeOut, &removed)
	if removed.Branch != "feat-a" || removed.Removed != listedFeatPath {
		t.Fatalf("worktree rm payload = %+v, want feat-a removed from %q", removed, listedFeatPath)
	}
	if _, err := os.Stat(removed.Removed); !os.IsNotExist(err) {
		t.Fatalf("worktree path still exists after rm: %v", err)
	}
	if created.Path != removed.Removed {
		if _, err := os.Stat(created.Path); !os.IsNotExist(err) {
			t.Fatalf("created worktree path still exists after rm: %v", err)
		}
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

func TestSubmitDryRunJSONShape(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)

	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCheckout(t, "feat-b")

	out := captureStdout(t, func() {
		if err := runSubmit([]string{"--dry-run", "--json"}); err != nil {
			t.Fatalf("submit --dry-run --json: %v", err)
		}
	})
	requireJSONObjectKeys(t, "submit --dry-run --json", out, "remote", "dryRun", "pushed")
	type submitDryRunJSON struct {
		Remote string   `json:"remote"`
		DryRun bool     `json:"dryRun"`
		Pushed []string `json:"pushed"`
	}
	var got submitDryRunJSON
	decodeStrictJSON(t, "submit --dry-run --json", out, &got)
	if got.Remote != "origin" {
		t.Fatalf("dry-run remote = %q, want origin", got.Remote)
	}
	if !got.DryRun {
		t.Fatalf("dry-run payload = %+v, want dryRun true", got)
	}
	if want := []string{"feat-a", "feat-b"}; !reflect.DeepEqual(got.Pushed, want) {
		t.Fatalf("dry-run pushed = %v, want %v", got.Pushed, want)
	}
	if refs := mustRun(t, "git", "--git-dir", remoteDir, "for-each-ref", "--format=%(refname)", "refs/heads"); refs != "" {
		t.Fatalf("dry-run created remote refs:\n%s", refs)
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
	gitShell = cachedShell{}
	defer func() { gitShell = orig }()

	// JSON mode swaps the production port for its quiet variant (so rebase chatter
	// cannot corrupt the payload); both keep the cached Worktrees() override.
	if _, ok := stackEnv(&stack.State{}, true).Git.(cachedQuietShell); !ok {
		t.Fatal("JSON stack env did not use the quiet cached shell")
	}
	if _, ok := stackEnv(&stack.State{}, false).Git.(cachedShell); !ok {
		t.Fatal("text stack env did not use the cached shell")
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

// TestAgentDocDocumentsExitCodes pins docs/AGENT.md's exit-code table and prose
// to errorClasses (plus the two codes defined outside it: 1=error and
// exitInternal=70). AGENT.md is the machine contract agents branch on, yet only
// st guide's copy was drift-checked before; adding or renaming a code without
// updating the doc now fails here.
func TestAgentDocDocumentsExitCodes(t *testing.T) {
	doc := agentDoc(t)
	type ec struct {
		code int
		name string
	}
	want := make([]ec, 0, len(errorClasses)+2)
	for _, c := range errorClasses {
		want = append(want, ec{c.code, c.name})
	}
	want = append(want, ec{1, "error"}, ec{exitInternal, "internal"})
	for _, w := range want {
		if cell := fmt.Sprintf("| %d |", w.code); !strings.Contains(doc, cell) {
			t.Errorf("docs/AGENT.md exit-code table missing row %q", cell)
		}
		// Bind the name TO the code; a bare "error"/"conflict" appears elsewhere
		// as prose, so match the doc's own "`name` (code" pairing instead.
		if prose := fmt.Sprintf("`%s` (%d", w.name, w.code); !strings.Contains(doc, prose) {
			t.Errorf("docs/AGENT.md does not bind error code to name: missing %q", prose)
		}
	}
}

// TestAgentDocDocumentsContractStructs pins the named, documented JSON contract
// structs to AGENT.md. Their omitempty fields (failed, branch, restacked, deleted,
// notes, dryRun, aliases, default) do not appear on a happy-path run, so reflection
// — not execution — is the only way to catch a new undocumented field. commandInfo
// and flagInfo back the `help --json` payload (AGENT.md's "Discoverability"); no
// golden captures that payload and the decode test follows any tag rename, so a
// renamed json tag (e.g. flagInfo `type`->`kind`) would otherwise drift silently.
func TestAgentDocDocumentsContractStructs(t *testing.T) {
	doc := agentDoc(t)
	for _, typ := range []reflect.Type{
		reflect.TypeOf(submitResult{}),
		reflect.TypeOf(stack.OpResult{}),
		reflect.TypeOf(initResult{}),
		reflect.TypeOf(commandInfo{}),
		reflect.TypeOf(flagInfo{}),
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

// TestAgentDocDocumentsEmittedKeys runs the read/navigation commands and the
// operational commands (abort/undo/repair) that emit anonymous inline structs
// (status, checkout, validate, navigation, log, abort, undo, repair) — which
// reflection cannot reach by name — and asserts every JSON key they actually
// emit is documented in AGENT.md. The fixture surfaces status's omitempty keys
// (parent, needsRestack) by running on a tracked branch with a parent and child,
// and drives a real paused rebase so abort has something to abort.
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
	// The paused rebase is still in progress; aborting it surfaces abort's keys.
	collect("abort --json", func() error { return runAbort([]string{"--json"}) })

	// repair on a consistent stack and undo of the last mutation surface the
	// remaining operational keys (repaired/fixes, undone/label/restored).
	newRepo(t)
	mustInit(t)
	mustCreate(t, "op-a", "a.txt", "a\n", "add a")
	collect("repair --json", func() error { return runRepair([]string{"--json"}) })
	collect("undo --json", func() error { return runUndo([]string{"--json"}) })

	for k := range keys {
		if !documentsKey(doc, k) {
			t.Errorf("docs/AGENT.md does not document emitted JSON key %q", k)
		}
	}
}
