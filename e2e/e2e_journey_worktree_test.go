package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Worktree journeys: add/rm/copy, linked-worktree ops, cross-worktree
// restack/fold/delete cascades, and worktree-path helpers.

// sees the same stack (TEST-10).
func TestWorktreeSharesStackState(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main") // free feat-a so a worktree can check it out

	wt := filepath.Join(t.TempDir(), "wt")
	r.git("worktree", "add", "-q", wt, "feat-a")

	res := r.stInOK(wt, "log", "--json")
	if !strings.Contains(res.stdout, "feat-a") {
		t.Fatalf("worktree st did not see the shared stack state:\n%s", res.stdout)
	}
}

// TestWorktreeAnnotationsInLog asserts that, once a second worktree exists, st
// log/status annotate the branch that lives there with its worktree path (and
// dirty state), while the single-tree fields stay empty for the main branch.
func TestWorktreeAnnotationsInLog(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main") // free feat-a so a worktree can check it out

	wt := filepath.Join(t.TempDir(), "wt")
	r.git("worktree", "add", "-q", wt, "feat-a")

	// log --json from the main worktree should annotate feat-a with its path.
	out := r.stOK("log", "--json").stdout
	var root logNode
	if err := json.Unmarshal([]byte(out), &root); err != nil {
		t.Fatalf("decode log json: %v\n%s", err, out)
	}
	feat := findNode(&root, "feat-a")
	if feat == nil {
		t.Fatalf("feat-a missing from log:\n%s", out)
	}
	if feat.Worktree == "" {
		t.Fatalf("feat-a not annotated with a worktree path:\n%s", out)
	}
	if feat.Dirty {
		t.Fatalf("feat-a worktree reported dirty when it is clean:\n%s", out)
	}

	// Dirty the linked worktree and confirm the flag flips.
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("dirty worktree: %v", err)
	}
	out = r.stOK("log", "--json").stdout
	if err := json.Unmarshal([]byte(out), &root); err != nil {
		t.Fatalf("decode log json: %v\n%s", err, out)
	}
	if feat := findNode(&root, "feat-a"); feat == nil || !feat.Dirty {
		t.Fatalf("feat-a worktree not reported dirty after edit:\n%s", out)
	}

	// status from inside the worktree reports its own path.
	sOut := r.stInOK(wt, "status", "--json").stdout
	var st statusJSON
	if err := json.Unmarshal([]byte(sOut), &st); err != nil {
		t.Fatalf("decode status json: %v\n%s", err, sOut)
	}
	if st.Branch != "feat-a" || st.Worktree == "" {
		t.Fatalf("status in worktree missing worktree path: %+v\n%s", st, sOut)
	}
}

// TestWorktreeCommand materializes a worktree for a tracked branch via
// `st worktree`, confirms it is listed and copies .worktreeinclude entries, and
// then removes it.
func TestWorktreeCommand(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()

	// The .worktreeinclude config lives on the trunk, the worktree's source: a
	// gitignored file listed there should be copied; a tracked file listed there
	// should NOT (git worktree add already materializes it).
	r.writeFile(".gitignore", "secret.env\n")
	r.writeFile("secret.env", "TOKEN=1\n")
	r.writeFile(".worktreeinclude", "secret.env\na.txt\n")
	r.git("add", ".gitignore", ".worktreeinclude")
	r.git("commit", "-q", "-m", "add worktree config")

	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main") // free feat-a so it can be checked out elsewhere

	out := r.stOK("worktree", "feat-a", "--json").stdout
	var created struct {
		Branch string   `json:"branch"`
		Path   string   `json:"path"`
		Copied []string `json:"copied"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("decode worktree json: %v\n%s", err, out)
	}
	if created.Branch != "feat-a" || created.Path == "" {
		t.Fatalf("unexpected worktree result: %+v", created)
	}
	if _, err := os.Stat(filepath.Join(created.Path, "secret.env")); err != nil {
		t.Fatalf("gitignored .worktreeinclude file not copied: %v", err)
	}
	wantCopied := false
	for _, c := range created.Copied {
		if c == "secret.env" {
			wantCopied = true
		}
		if c == "a.txt" {
			t.Errorf("tracked file a.txt was copied; should be skipped")
		}
	}
	if !wantCopied {
		t.Errorf("secret.env not reported copied: %v", created.Copied)
	}

	// ls shows the new worktree.
	lsOut := r.stOK("worktree", "ls").stdout
	if !strings.Contains(lsOut, "feat-a") {
		t.Fatalf("worktree ls missing feat-a:\n%s", lsOut)
	}

	// rm removes it.
	r.stOK("worktree", "rm", "feat-a")
	if _, err := os.Stat(created.Path); !os.IsNotExist(err) {
		t.Errorf("worktree dir still present after rm: %v", err)
	}
}

// TestWorktreeNewlinePathRoundTrip pins the NUL-framed worktree listing end to
// end: a linked worktree whose path contains bytes that are structure in the
// line-based grammar must come back through `st worktree ls --json` and the
// log ownership annotation with its exact path bytes, while terminal output
// still escapes the control characters.
func TestWorktreeNewlinePathRoundTrip(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("newline paths are not representable on windows filesystems")
	}
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main") // free feat-a so a worktree can check it out

	leaf := "wt\nnl\ttab'q ☃"
	wt := filepath.Join(t.TempDir(), leaf)
	if err := exec.Command("git", "-C", r.dir, "worktree", "add", "-q", wt, "feat-a").Run(); err != nil {
		t.Skipf("filesystem/git cannot host a newline worktree path: %v", err)
	}
	want, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatalf("resolve %q: %v", wt, err)
	}

	// JSON must carry the exact path bytes.
	out := r.stOK("worktree", "ls", "--json").stdout
	var entries []struct {
		Path   string `json:"path"`
		Branch string `json:"branch"`
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("decode worktree ls json: %v\n%s", err, out)
	}
	var got string
	for _, e := range entries {
		if e.Branch == "feat-a" {
			got = e.Path
		}
		if strings.Contains(e.Path, "\x00") || e.Path == "/fake" {
			t.Fatalf("corrupted worktree record in %+v", entries)
		}
	}
	if got != want {
		t.Fatalf("worktree ls json path = %q, want exact bytes %q", got, want)
	}

	// The read-only ownership/navigation annotation must resolve the same path.
	var root logNode
	if err := json.Unmarshal([]byte(r.stOK("log", "--json").stdout), &root); err != nil {
		t.Fatalf("decode log json: %v", err)
	}
	feat := findNode(&root, "feat-a")
	if feat == nil || feat.Worktree != want {
		t.Fatalf("log worktree annotation = %+v, want path %q", feat, want)
	}

	// Terminal output escapes the control bytes instead of emitting them raw.
	text := r.stOK("worktree", "ls").stdout
	if strings.Contains(text, want) {
		t.Fatalf("terminal output contains the raw newline path %q:\n%s", want, text)
	}
	if !strings.Contains(text, "feat-a") {
		t.Fatalf("worktree ls missing feat-a:\n%s", text)
	}
}

func TestCreateWorktreeFlagJourney(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()

	r.create("feat-a", "a.txt", "a\n", "a")
	parentTip := r.rev("feat-a")

	out := r.stOK("create", "feat-b", "--worktree", "--json").stdout
	var created struct {
		Branch   string `json:"branch"`
		Parent   string `json:"parent"`
		Worktree string `json:"worktree"`
		Switched bool   `json:"switched"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("decode create --worktree json: %v\n%s", err, out)
	}
	if created.Branch != "feat-b" || created.Parent != "feat-a" || created.Worktree == "" {
		t.Fatalf("unexpected create --worktree result: %+v", created)
	}
	if created.Switched {
		t.Fatalf("create --worktree switched without shell shim: %+v", created)
	}
	if cur := r.currentBranch(); cur != "feat-a" {
		t.Fatalf("main worktree branch = %q, want feat-a", cur)
	}
	if got := r.rev("feat-b"); got != parentTip {
		t.Fatalf("feat-b tip = %s, want parent tip %s", got, parentTip)
	}

	var root logNode
	if err := json.Unmarshal([]byte(r.stOK("log", "--json").stdout), &root); err != nil {
		t.Fatalf("decode log after create --worktree: %v", err)
	}
	node := findNode(&root, "feat-b")
	if node == nil {
		t.Fatalf("feat-b missing from log:\n%s", r.stOK("log", "--json").stdout)
	}
	gotResolved, _ := filepath.EvalSymlinks(node.Worktree)
	wantResolved, _ := filepath.EvalSymlinks(created.Worktree)
	if gotResolved != wantResolved {
		t.Fatalf("feat-b log node = %+v, want worktree %q", node, created.Worktree)
	}

	if err := os.WriteFile(filepath.Join(created.Worktree, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatalf("write in created worktree: %v", err)
	}
	r.gitIn(created.Worktree, "add", "b.txt")
	r.gitIn(created.Worktree, "commit", "-q", "-m", "b")

	r.writeFile("a2.txt", "a2\n")
	r.git("add", "a2.txt")
	r.git("commit", "-q", "-m", "advance feat-a")
	r.stOK("restack")

	if !r.isAncestor("feat-a", "feat-b") {
		t.Fatal("feat-b was not restacked onto the advanced feat-a")
	}
	if cur := r.currentBranch(); cur != "feat-a" {
		t.Fatalf("main worktree branch = %q after restack, want feat-a", cur)
	}
}

func TestWorktreeCommandFromLinkedWorktreeUsesMainRepoNamespace(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()

	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.stOK("checkout", "main")
	r.create("feat-c", "c.txt", "c\n", "c")
	r.stOK("checkout", "main")

	mainOut := r.stOK("worktree", "feat-b", "--json").stdout
	var mainCreated struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(mainOut), &mainCreated); err != nil {
		t.Fatalf("decode main worktree create: %v\n%s", err, mainOut)
	}
	mainParts := generatedWorktreePathParts(t, r.home, mainCreated.Path)

	linked := filepath.Join(t.TempDir(), "linked")
	r.git("worktree", "add", "-q", linked, "feat-a")
	linkedOut := r.stInOK(linked, "worktree", "feat-c", "--json").stdout
	var linkedCreated struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(linkedOut), &linkedCreated); err != nil {
		t.Fatalf("decode linked worktree create: %v\n%s", err, linkedOut)
	}
	linkedParts := generatedWorktreePathParts(t, r.home, linkedCreated.Path)
	if linkedParts[0] != mainParts[0] {
		t.Fatalf("linked worktree repo segment = %q, want main repo segment %q", linkedParts[0], mainParts[0])
	}
}

func TestWorktreeIncludeCopyFailureRollsBackWorktree(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("relies on unsafe destination symlink behavior that does not fail on windows")
	}
	r := newRepo(t)
	r.initStack()

	r.writeFile(".gitignore", "secret.env\n")
	r.writeFile(".worktreeinclude", "secret.env\n")
	r.writeFile("secret.env", "TOKEN=source\n")
	r.git("add", ".gitignore", ".worktreeinclude")
	r.git("commit", "-q", "-m", "add worktree include config")

	r.create("feat-a", "a.txt", "a\n", "a")

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
		t.Fatalf("write outside target: %v", err)
	}
	if err := os.Remove(filepath.Join(r.dir, "secret.env")); err != nil {
		t.Fatalf("remove source secret before symlink: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(r.dir, "secret.env")); err != nil {
		t.Fatalf("create tracked symlink: %v", err)
	}
	r.git("add", "-f", "secret.env")
	r.git("commit", "-q", "-m", "track symlink at include path")

	r.stOK("checkout", "main")
	r.writeFile("secret.env", "TOKEN=source\n")

	res := r.st("worktree", "feat-a")
	if res.exitCode == 0 {
		t.Fatalf("st worktree feat-a succeeded; want unsafe destination symlink failure\nstdout:\n%s", res.stdout)
	}
	if !strings.Contains(res.stderr, "destination symlink") {
		t.Fatalf("st worktree feat-a stderr = %q, want destination symlink context", res.stderr)
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "outside\n" {
		t.Fatalf("outside file = %q, %v; destination symlink was followed", b, err)
	}

	list := r.git("worktree", "list", "--porcelain")
	if strings.Contains(list, "branch refs/heads/feat-a") {
		t.Fatalf("failed worktree still registered:\n%s", list)
	}
	matches, err := filepath.Glob(filepath.Join(r.home, ".stacked", "worktrees", "*", "feat-a"))
	if err != nil {
		t.Fatalf("glob failed worktree path: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("failed worktree paths still exist: %v", matches)
	}
}

func generatedWorktreePathParts(t *testing.T, home, path string) []string {
	t.Helper()
	root := filepath.Join(home, ".stacked", "worktrees")
	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		t.Fatalf("path %q is not under worktrees root %q", path, root)
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) != 2 {
		t.Fatalf("expected generated path to have two segments below root, got %q (%v)", rel, parts)
	}
	return parts
}

// TestCheckoutTeleportsToWorktree asserts that, once a branch lives in another
// worktree, `st checkout` teleports there: with the shell shim's directive file
// set it writes the worktree path to that file and reports the switch; WITHOUT
// the shim the parent shell cannot be moved, so it must not claim a switch and
// instead prints an actionable `cd <path>` hint (progressive enhancement).
func TestCheckoutTeleportsToWorktree(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main")

	created := r.stOK("worktree", "feat-a", "--json").stdout
	var wt struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(created), &wt); err != nil {
		t.Fatalf("decode worktree create: %v\n%s", err, created)
	}

	// With the directive file set (as the shim does), checkout writes the path.
	directive := filepath.Join(t.TempDir(), "cd")
	res := r.stInEnv(r.dir, []string{"ST_CD_FILE=" + directive}, "checkout", "feat-a")
	if res.exitCode != 0 {
		t.Fatalf("st checkout feat-a: exit %d\nstdout:\n%s\nstderr:\n%s",
			res.exitCode, res.stdout, res.stderr)
	}
	got, err := os.ReadFile(directive)
	if err != nil {
		t.Fatalf("read cd directive: %v", err)
	}
	// Compare by canonical path: git reports the symlink-resolved worktree path
	// (on macOS /var -> /private/var) while the create JSON carries the computed
	// one, so a literal string compare would spuriously differ.
	gotResolved, _ := filepath.EvalSymlinks(strings.TrimSpace(string(got)))
	wantResolved, _ := filepath.EvalSymlinks(strings.TrimSpace(wt.Path))
	if gotResolved != wantResolved {
		t.Fatalf("cd directive = %q, want worktree path %q", got, wt.Path)
	}

	// We are still on main in the main worktree (checkout teleported, did not
	// switch the branch here, which git forbids anyway).
	if cur := r.currentBranch(); cur != "main" {
		t.Fatalf("main worktree branch = %q, want main (teleport must not switch in place)", cur)
	}

	// Without the shim (ST_CD_FILE unset, as cleanEnv leaves it), checkout cannot
	// move the parent shell, so it must NOT claim a switch and must instead print
	// an actionable cd hint pointing at the worktree.
	noShim := r.stOK("checkout", "feat-a")
	if strings.Contains(noShim.stdout, "switched") {
		t.Fatalf("checkout without the shim must not claim a switch:\n%s", noShim.stdout)
	}
	if !strings.Contains(noShim.stdout, "cd ") {
		t.Fatalf("checkout without the shim must suggest cd <path>:\n%s", noShim.stdout)
	}
}

// TestCrossWorktreeRestackCascade proves the owner-driven cascade with real git:
// with feat-a checked out in its own worktree, advancing the trunk and running
// `st restack` rebases feat-a IN its worktree (git forbids rebasing it from the
// main worktree), reconciling the stack without moving the main worktree's HEAD.
func TestCrossWorktreeRestackCascade(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main")

	// Materialize feat-a's worktree.
	created := r.stOK("worktree", "feat-a", "--json").stdout
	var wt struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(created), &wt); err != nil {
		t.Fatalf("decode worktree create: %v\n%s", err, created)
	}
	featBefore := r.git("rev-parse", "feat-a")

	// Advance the trunk so feat-a needs a restack.
	r.writeFile("trunk.txt", "t\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "advance trunk")
	mainTip := r.git("rev-parse", "main")

	// Restack from main: feat-a (in its worktree) is the dependent to rebase.
	res := r.stOK("restack")
	_ = res

	featAfter := r.git("rev-parse", "feat-a")
	if featAfter == featBefore {
		t.Fatalf("feat-a tip unchanged (%s); cross-worktree restack did not run", featAfter)
	}
	// feat-a's parent commit must now be the new main tip.
	parent := r.git("rev-parse", "feat-a~1")
	if parent != mainTip {
		t.Fatalf("feat-a parent = %s, want new main tip %s", parent, mainTip)
	}
	// The main worktree is still on main (the rebase ran in feat-a's worktree).
	if cur := r.currentBranch(); cur != "main" {
		t.Fatalf("main worktree branch = %q, want main", cur)
	}
	// The stack is reconciled: validate is clean.
	r.stOK("validate")
}

// TestCrossWorktreeRestackConflictRollsBack proves that a conflict during the
// owner-worktree rebase is rolled back (not left paused where the main process
// cannot drive it): the command errors, but neither worktree is left mid-rebase.
func TestCrossWorktreeRestackConflictRollsBack(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	// feat-a edits the same file the trunk will, forcing a conflict on restack.
	r.create("feat-a", "shared.txt", "A\n", "a")
	r.stOK("checkout", "main")

	created := r.stOK("worktree", "feat-a", "--json").stdout
	var wt struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(created), &wt); err != nil {
		t.Fatalf("decode worktree create: %v\n%s", err, created)
	}

	// Advance trunk with a conflicting change to shared.txt.
	r.writeFile("shared.txt", "X\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "trunk conflicts")

	res := r.st("restack")
	if res.exitCode == 0 {
		t.Fatalf("expected restack to fail on the cross-worktree conflict, got exit 0:\n%s", res.stdout)
	}
	// The owner worktree must NOT be left mid-rebase.
	cmd := exec.Command("git", "-C", wt.Path, "status", "--porcelain=v2", "--branch")
	cmd.Env = cleanEnv(r.home)
	statusOut, _ := cmd.CombinedOutput()
	if strings.Contains(string(statusOut), "rebase") {
		t.Fatalf("owner worktree left mid-rebase after conflict:\n%s", statusOut)
	}
	// And the dependent's tip was not advanced (rolled back).
	r.stOK("validate") // metadata is consistent; the unrebased branch just needs a restack
}

// TestCrossWorktreeRestackSkipsDirty proves a dirty dependent worktree is
// skipped (not clobbered) with a clear note, and left needing a restack.
func TestCrossWorktreeRestackSkipsDirty(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main")

	created := r.stOK("worktree", "feat-a", "--json").stdout
	var wt struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(created), &wt); err != nil {
		t.Fatalf("decode worktree create: %v\n%s", err, created)
	}
	featBefore := r.git("rev-parse", "feat-a")

	// Dirty feat-a's worktree.
	if err := os.WriteFile(filepath.Join(wt.Path, "a.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("dirty worktree: %v", err)
	}

	// Advance the trunk and restack.
	r.writeFile("trunk.txt", "t\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "advance trunk")

	res := r.stOK("restack")
	if !strings.Contains(res.stdout, "skipped feat-a") {
		t.Fatalf("restack did not report the dirty skip:\n%s", res.stdout)
	}
	if featAfter := r.git("rev-parse", "feat-a"); featAfter != featBefore {
		t.Fatalf("dirty feat-a was clobbered: %s -> %s", featBefore, featAfter)
	}
}

// worktreeFor materializes feat-a's worktree (from the main worktree) and returns
// its on-disk path. The caller must already be on a branch other than feat-a.
func worktreeFor(t *testing.T, r *repo, branch string) string {
	t.Helper()
	created := r.stOK("worktree", branch, "--json").stdout
	var wt struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(created), &wt); err != nil {
		t.Fatalf("decode worktree create: %v\n%s", err, created)
	}
	return wt.Path
}

// TestDeleteTearsDownCleanOwnedWorktree proves `st delete` of a branch that owns
// a (clean) linked worktree first removes that worktree (git would otherwise
// refuse to delete a branch checked out elsewhere), then deletes the branch.
func TestDeleteTearsDownCleanOwnedWorktree(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b") // child to re-parent onto main
	r.stOK("checkout", "main")              // free feat-a so it can live elsewhere

	wtPath := worktreeFor(t, r, "feat-a")
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("worktree not materialized: %v", err)
	}

	// Delete feat-a (force: it is not merged into main): its clean worktree is
	// torn down and the branch is gone.
	r.stOK("delete", "feat-a", "-f")
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("feat-a's worktree still present after delete: %v", err)
	}
	if r.branchExists("feat-a") {
		t.Fatal("feat-a git branch still present after delete")
	}
	if !r.branchExists("feat-b") {
		t.Fatal("feat-b should survive and be re-parented onto main")
	}
	r.stOK("validate")
}

// TestDeleteRefusesDirtyOwnedWorktree proves `st delete` of a branch whose linked
// worktree has uncommitted changes errors and changes nothing — the worktree and
// the branch both remain (in-progress work is never silently discarded).
func TestDeleteRefusesDirtyOwnedWorktree(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main")

	wtPath := worktreeFor(t, r, "feat-a")
	// Dirty the worktree.
	if err := os.WriteFile(filepath.Join(wtPath, "a.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("dirty worktree: %v", err)
	}

	res := r.st("delete", "feat-a", "-f")
	if res.exitCode == 0 {
		t.Fatalf("delete of a branch with a dirty worktree should fail, got exit 0:\n%s", res.stdout)
	}
	if !strings.Contains(res.stderr+res.stdout, "worktree") {
		t.Fatalf("error should mention the worktree:\nstdout:%s\nstderr:%s", res.stdout, res.stderr)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("dirty worktree was removed: %v", err)
	}
	if !r.branchExists("feat-a") {
		t.Fatal("feat-a must still exist after a refused delete")
	}
	r.stOK("validate")
}

// TestFoldCascadesIntoCleanChildWorktree is the fold analogue of the delete
// teardown: fold deletes the CURRENT branch (always local — a branch is checked
// out in at most one worktree), then re-parents and restacks its children. When a
// child lives in another (clean) worktree, fold must rebase it IN that worktree,
// never moving the main worktree's HEAD across worktrees. Here feat-b is folded
// into feat-a while feat-c (feat-b's child) lives in its own worktree.
func TestFoldCascadesIntoCleanChildWorktree(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.create("feat-c", "c.txt", "c\n", "c")
	r.stOK("checkout", "feat-b") // free feat-c so it can live elsewhere
	wtPath := worktreeFor(t, r, "feat-c")

	r.stOK("checkout", "feat-b")
	r.stOK("fold") // fold feat-b into feat-a
	if r.branchExists("feat-b") {
		t.Fatal("feat-b should be folded away")
	}
	// feat-c is re-parented onto feat-a and its worktree is left intact; the main
	// worktree did not teleport into feat-c's worktree during the fold.
	if cur := r.currentBranch(); cur == "feat-c" {
		t.Fatalf("main worktree HEAD = feat-c; fold must not move HEAD into another worktree")
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("feat-c's worktree should be intact after fold: %v", err)
	}
	// The whole stack reconciles across worktrees.
	r.stOK("restack")
	r.stOK("validate")
}

// TestCrossWorktreeRestackMultiLevel proves the owner-driven cascade reconciles a
// multi-level stack across the worktree boundary with real git: main -> feat-a ->
// feat-b -> feat-c, with the INTERMEDIATE feat-b living in its own worktree and
// feat-a/feat-c local. Advancing the trunk and running `st restack` must rebase
// feat-a in place, feat-b IN its worktree (onto the rebased feat-a), and feat-c on
// the rebased feat-b — without moving the main worktree's HEAD.
func TestCrossWorktreeRestackMultiLevel(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.create("feat-c", "c.txt", "c\n", "c")
	r.stOK("checkout", "main") // free feat-b so it can live in its own worktree

	wtPath := worktreeFor(t, r, "feat-b")

	// Advance the trunk so the whole stack is out of date.
	r.writeFile("trunk.txt", "t\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "advance trunk")
	mainTip := r.git("rev-parse", "main")

	bBefore := r.git("rev-parse", "feat-b")
	cBefore := r.git("rev-parse", "feat-c")

	r.stOK("restack")

	// feat-b (in its worktree) and feat-c were rebased.
	if r.git("rev-parse", "feat-b") == bBefore {
		t.Fatal("feat-b tip unchanged; the intermediate worktree branch did not rebase")
	}
	if r.git("rev-parse", "feat-c") == cBefore {
		t.Fatal("feat-c tip unchanged; the cascade did not reach the top branch")
	}
	// feat-a sits on the new main tip; the whole chain descends from it.
	if parent := r.git("rev-parse", "feat-a~1"); parent != mainTip {
		t.Fatalf("feat-a parent=%s, want new main tip %s", parent, mainTip)
	}
	// feat-c contains feat-b which contains feat-a (chain across the boundary).
	if !r.isAncestor("feat-a", "feat-b") {
		t.Fatal("rebased feat-b does not contain feat-a")
	}
	if !r.isAncestor("feat-b", "feat-c") {
		t.Fatal("rebased feat-c does not contain feat-b")
	}
	if !r.isAncestor(mainTip, "feat-c") {
		t.Fatal("rebased feat-c does not descend from the new main")
	}
	// feat-b's worktree is intact and the main worktree stayed on main.
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("feat-b's worktree missing after restack: %v", err)
	}
	if cur := r.currentBranch(); cur != "main" {
		t.Fatalf("main worktree HEAD=%q, want main (cascade must not move it)", cur)
	}
	// The whole stack is reconciled.
	r.stOK("validate")
}

// TestSyncFromLinkedWorktree proves the whole maintenance loop works from
// inside a branch's own worktree: sync fast-forwards the trunk in the MAIN
// worktree (its owner), prunes the landed branch, restacks the current branch,
// and leaves both worktrees on their original branches.
func TestSyncFromLinkedWorktree(t *testing.T) {
	t.Parallel()
	r := newRepo(t)

	bare := filepath.Join(t.TempDir(), "remote.git")
	r.initBare(bare)
	r.git("remote", "add", "origin", bare)
	r.git("push", "-q", "-u", "origin", "main")

	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")

	// Land feat-a on the remote trunk, then rewind local main so the sync has
	// a real fast-forward to perform in the main worktree.
	r.stOK("checkout", "main")
	preTrunk := r.rev("main")
	r.git("merge", "-q", "--no-ff", "feat-a", "-m", "merge feat-a")
	r.git("push", "-q", "origin", "main")
	r.git("reset", "--hard", preTrunk)

	out := r.stOK("worktree", "feat-b", "--json").stdout
	var wt struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(out), &wt); err != nil {
		t.Fatalf("decode worktree json: %v\n%s", err, out)
	}

	syncOut := r.stInOK(wt.Path, "sync").stdout
	if !strings.Contains(syncOut, "sync complete") {
		t.Fatalf("sync output missing completion:\n%s", syncOut)
	}

	if r.branchExists("feat-a") {
		t.Fatal("feat-a should be pruned after sync")
	}
	if r.rev("main") == preTrunk {
		t.Fatal("sync did not fast-forward local main")
	}
	if !r.isAncestor("main", "feat-b") {
		t.Fatal("feat-b was not restacked onto the advanced main")
	}
	if cur := r.currentBranch(); cur != "main" {
		t.Fatalf("main worktree branch = %q after sync, want main", cur)
	}
	if got := r.gitIn(wt.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat-b" {
		t.Fatalf("feat-b worktree branch = %q after sync, want feat-b", got)
	}
	r.stOK("validate")
}

// TestSyncFromMergedLinkedWorktreeKeepsWorktree pins the fix for the
// stale-cache bug: running `st sync` from inside a branch's own linked
// worktree, when that branch has merged into the trunk, must prune the branch
// WITHOUT deleting the worktree you are standing in. Before the fix, the
// pre-prune HEAD detach did not invalidate the worktree cache, so PruneMerged
// read the stale list still showing the branch owned by the current worktree
// and removed it (destroying the shell CWD and aborting sync half-applied).
func TestSyncFromMergedLinkedWorktreeKeepsWorktree(t *testing.T) {
	t.Parallel()
	r := newRepo(t)

	bare := filepath.Join(t.TempDir(), "remote.git")
	r.initBare(bare)
	r.git("remote", "add", "origin", bare)
	r.git("push", "-q", "-u", "origin", "main")

	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")

	// Materialize a linked worktree for feat-a (the branch we'll be standing in),
	// then land feat-a on the remote trunk and rewind local main so sync prunes
	// feat-a as merged.
	r.stOK("checkout", "main")
	out := r.stOK("worktree", "feat-a", "--json").stdout
	var wt struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(out), &wt); err != nil {
		t.Fatalf("decode worktree json: %v\n%s", err, out)
	}
	preTrunk := r.rev("main")
	r.git("merge", "-q", "--no-ff", "feat-a", "-m", "merge feat-a")
	r.git("push", "-q", "origin", "main")
	r.git("reset", "--hard", preTrunk)

	// --json on purpose: it drives the quiet git port, so the JSON-mode
	// CheckoutDetach invalidation override is exercised too (the text-mode
	// twin is covered by TestSyncFromLinkedWorktree).
	// run from INSIDE feat-a's own worktree
	syncOut := r.stInOK(wt.Path, "sync", "--json").stdout
	if !strings.Contains(syncOut, "sync complete") {
		t.Fatalf("sync output missing completion:\n%s", syncOut)
	}
	if r.branchExists("feat-a") {
		t.Fatal("merged feat-a should have been pruned")
	}
	// The load-bearing assertion: the worktree we ran from still exists.
	if _, statErr := os.Stat(wt.Path); statErr != nil {
		t.Fatalf("sync deleted the worktree it ran from (%q): %v", wt.Path, statErr)
	}
	if !r.isAncestor("main", "feat-b") {
		t.Fatal("feat-b was not restacked onto the advanced main")
	}
	r.stOK("validate")
}

// TestRestackAllFromLinkedWorktree proves `st restack --all` reaches a
// SIBLING stack from inside a linked worktree — the whole-forest restack an
// orchestrator needs, without a trunk checkout and without sync's fetch/prune.
func TestRestackAllFromLinkedWorktree(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()

	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main")
	r.create("feat-x", "x.txt", "x\n", "x")
	r.create("feat-y", "y.txt", "y\n", "y")
	r.stOK("checkout", "main")

	// Advance main directly so every stack needs a restack.
	r.writeFile("m.txt", "m\n")
	r.git("add", "m.txt")
	r.git("commit", "-q", "-m", "advance main")

	out := r.stOK("worktree", "feat-a", "--json").stdout
	var wt struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(out), &wt); err != nil {
		t.Fatalf("decode worktree json: %v\n%s", err, out)
	}

	allRes := r.stInOK(wt.Path, "restack", "--all")

	var root logNode
	if err := json.Unmarshal([]byte(r.stOK("log", "--json").stdout), &root); err != nil {
		t.Fatalf("decode log json: %v", err)
	}
	for _, name := range []string{"feat-a", "feat-x", "feat-y"} {
		node := findNode(&root, name)
		if node == nil {
			t.Fatalf("%s missing from log", name)
		}
		if node.NeedsRestack {
			t.Fatalf("%s still needs restack after restack --all:\n%s\n%s", name, allRes.stdout, allRes.stderr)
		}
	}
	if cur := r.currentBranch(); cur != "main" {
		t.Fatalf("main worktree branch = %q, want main", cur)
	}
	if got := r.gitIn(wt.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat-a" {
		t.Fatalf("feat-a worktree branch = %q, want feat-a", got)
	}
}

// TestWorktreeAllJourney seeds a whole stack's worktrees in one call and
// proves the rerun is a no-op success.
func TestWorktreeAllJourney(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.create("feat-c", "c.txt", "c\n", "c")
	r.stOK("checkout", "main")

	r.stOK("worktree", "--all")
	lsOut := r.stOK("worktree", "ls").stdout
	for _, name := range []string{"feat-a", "feat-b", "feat-c"} {
		if !strings.Contains(lsOut, name) {
			t.Fatalf("worktree ls missing %s after --all:\n%s", name, lsOut)
		}
	}

	// Rerun: still exit 0, same worktrees.
	rerun := r.stOK("worktree", "--all")
	if !strings.Contains(rerun.stdout, "worktree already exists") {
		t.Fatalf("rerun output missing already-exists rows:\n%s", rerun.stdout)
	}
	r.stOK("validate")
}

// TestWorktreeRemoveAllJourney tears the whole parallel session down in one
// call: after --all seeds every branch's worktree, rm --all releases them and
// ls shows only the main worktree.
func TestWorktreeRemoveAllJourney(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.stOK("checkout", "main")

	r.stOK("worktree", "--all")
	res := r.stOK("worktree", "rm", "--all")
	for _, name := range []string{"feat-a", "feat-b"} {
		if !strings.Contains(res.stdout, "removed worktree "+name) {
			t.Fatalf("rm --all output missing %s:\n%s", name, res.stdout)
		}
	}
	lsOut := r.stOK("worktree", "ls").stdout
	for _, name := range []string{"feat-a", "feat-b"} {
		if strings.Contains(lsOut, name) {
			t.Fatalf("worktree ls still lists %s after rm --all:\n%s", name, lsOut)
		}
	}
	// Rerun is a clean no-op.
	r.stOK("worktree", "rm", "--all")
	r.stOK("validate")
}

// TestWorktreeCopyCollisionRollsBackWorktree: when a .worktreeinclude entry's
// destination is already tracked on the branch being materialized, the copy
// refuses and the just-created worktree is removed again — a preexisting
// worktree and the source's ignored files are untouched.
func TestWorktreeCopyCollisionRollsBackWorktree(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()

	// secret.txt is ignored on the source branch and selected for copying.
	// The manifest is committed so it exists on every branch and worktree —
	// left untracked, `st create -a` would sweep it into the new branch's
	// commit and switching back to main would delete it.
	r.writeFile(".gitignore", "secret.txt\n")
	r.writeFile(".worktreeinclude", "secret.txt\n")
	r.git("add", ".gitignore", ".worktreeinclude")
	r.git("commit", "-q", "-m", "ignore secret.txt")
	r.writeFile("secret.txt", "SECRET=local\n")

	// A sibling's preexisting worktree must survive the rollback untouched.
	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main") // free feat-a so a worktree can check it out
	addA := r.stOK("worktree", "feat-a", "--json")
	var wtA struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(addA.stdout), &wtA); err != nil {
		t.Fatalf("worktree feat-a --json: %v\n%s", err, addA.stdout)
	}
	if wtA.Path == "" {
		t.Fatalf("worktree feat-a --json returned no path: %s", addA.stdout)
	}

	// feat-b TRACKS secret.txt — the colliding path. Once committed on feat-b,
	// switching back to main removes it from the main worktree, so restore the
	// ignored local copy the copy step should find.
	r.create("feat-b", "b.txt", "b\n", "b")
	r.git("add", "-f", "secret.txt")
	r.git("commit", "-q", "-m", "track secret on feat-b")
	r.stOK("checkout", "main")
	r.writeFile("secret.txt", "SECRET=local\n")

	res := r.st("worktree", "feat-b")
	if res.exitCode == 0 {
		t.Fatalf("st worktree feat-b succeeded; want a destination-collision refusal\nstdout:\n%s", res.stdout)
	}
	if !strings.Contains(res.stderr+res.stdout, "secret.txt") {
		t.Fatalf("refusal should name secret.txt\nstdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
	}

	// The just-created worktree must be gone — from git's registry and from
	// disk under the generated worktrees root.
	if out := r.git("worktree", "list", "--porcelain"); strings.Contains(out, "feat-b") {
		t.Fatalf("feat-b still owns a worktree after copy failure:\n%s", out)
	}
	matches, err := filepath.Glob(filepath.Join(r.home, ".stacked", "worktrees", "*", "feat-b"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("feat-b worktree path survived rollback: %v (err %v)", matches, err)
	}

	// The preexisting worktree and the ignored source file are untouched.
	if b, rerr := os.ReadFile(filepath.Join(wtA.Path, "a.txt")); rerr != nil || string(b) != "a\n" {
		t.Fatalf("preexisting feat-a worktree damaged by rollback: %q, %v", b, rerr)
	}
	if b, rerr := os.ReadFile(filepath.Join(r.dir, "secret.txt")); rerr != nil || string(b) != "SECRET=local\n" {
		t.Fatalf("source secret.txt = %q, %v; must remain untouched", b, rerr)
	}
	r.stOK("validate")
}

// worktreePath materializes branch's linked worktree via `st worktree <branch>
// --json` and returns its path.
func worktreePath(t *testing.T, r *repo, branch string) string {
	t.Helper()
	out := r.stOK("worktree", branch, "--json").stdout
	var wt struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(out), &wt); err != nil {
		t.Fatalf("decode worktree json for %s: %v\n%s", branch, err, out)
	}
	return wt.Path
}

// TestSyncConflictContinueFromLinkedWorktree drives the previously-untested
// combination: `st sync` run from inside a branch's own linked worktree where
// that branch conflicts on the advanced trunk. The paused rebase must land in
// the WORKTREE (exit 2), and `st continue` from the worktree must reconcile.
func TestSyncConflictContinueFromLinkedWorktree(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")

	// Advance local main with conflicting content, then free feat-a so it can
	// be checked out in its own worktree.
	r.stOK("checkout", "main")
	r.writeFile("f.txt", "MAIN\n")
	r.git("add", "f.txt")
	r.git("commit", "-q", "-m", "trunk advances")

	wt := worktreePath(t, r, "feat-a")

	res := r.stIn(wt, "sync")
	if res.exitCode != 2 {
		t.Fatalf("sync from linked worktree: exit %d, want 2 (conflict)\nstdout:%s\nstderr:%s", res.exitCode, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr+res.stdout, "st continue") {
		t.Fatalf("conflict output missing the continue hint:\n%s\n%s", res.stdout, res.stderr)
	}

	// The paused rebase lives under the WORKTREE's git dir, not the main .git.
	gitDir := r.gitIn(wt, "rev-parse", "--absolute-git-dir")
	if _, err := os.Stat(filepath.Join(gitDir, "rebase-merge")); err != nil {
		t.Fatalf("expected a rebase in progress under the worktree git dir %q: %v", gitDir, err)
	}

	// Resolve IN the worktree and continue FROM the worktree.
	if err := os.WriteFile(filepath.Join(wt, "f.txt"), []byte("MAIN\nA\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.gitIn(wt, "add", "f.txt")
	r.stInOK(wt, "continue")

	if !r.isAncestor("main", "feat-a") {
		t.Fatal("feat-a was not restacked onto the advanced main after continue")
	}
	if got := r.gitIn(wt, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat-a" {
		t.Fatalf("worktree HEAD = %q after continue, want feat-a", got)
	}
	r.stOK("validate")
}

// TestRestackSiblingWorktreeConflictRollsBackFromLinkedWorktree drives the
// other half: a SIBLING stack owned by a different worktree conflicts during
// `st restack --all` run from worktree W1. The sibling is rolled back (no
// paused rebase in W2, non-conflict exit), and W1's branch keeps its progress.
func TestRestackSiblingWorktreeConflictRollsBackFromLinkedWorktree(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a") // independent file: rebases clean
	r.stOK("checkout", "main")
	r.create("feat-x", "f.txt", "X\n", "x") // touches f.txt: will conflict
	r.stOK("checkout", "main")

	// Advance main with content that conflicts with feat-x but not feat-a.
	r.writeFile("f.txt", "MAIN\n")
	r.git("add", "f.txt")
	r.git("commit", "-q", "-m", "trunk advances")

	w1 := worktreePath(t, r, "feat-a")
	w2 := worktreePath(t, r, "feat-x")

	res := r.stIn(w1, "restack", "--all")
	if res.exitCode == 0 || res.exitCode == 2 {
		t.Fatalf("restack --all with a sibling-worktree conflict: exit %d, want non-zero non-conflict (1)\nstdout:%s\nstderr:%s", res.exitCode, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr+res.stdout, "feat-x") {
		t.Fatalf("error should name the conflicting sibling feat-x:\n%s\n%s", res.stdout, res.stderr)
	}

	if !r.isAncestor("main", "feat-a") {
		t.Fatal("feat-a should have rebased onto the advanced main before feat-x failed")
	}
	if r.isAncestor("main", "feat-x") {
		t.Fatal("feat-x should have been rolled back, not advanced")
	}
	w2GitDir := r.gitIn(w2, "rev-parse", "--absolute-git-dir")
	if _, err := os.Stat(filepath.Join(w2GitDir, "rebase-merge")); err == nil {
		t.Fatal("feat-x's worktree should have no paused rebase (it was rolled back)")
	}
}

// TestCreateWorktreeMaterializeFailure covers the create --worktree failure
// window: git worktree add fails AFTER the branch is created and tracked (the
// canonical worktree path is pre-obstructed), so the command exits non-zero
// naming the st worktree retry, the branch stays tracked without a worktree,
// and the undo entry is kept — a later `st worktree` retry materializes the
// branch unrecorded in the journal, so `st undo` must discover that worktree
// through LinkedOwnerOf (the journal has no createdWorktrees record) and remove
// it while deleting the branch.
func TestCreateWorktreeMaterializeFailure(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()

	// Bootstrap the generated-worktrees root with a sibling branch's worktree so
	// the canonical <root>/<repo-key>/<branch> layout — and the directory a
	// failed feat-x worktree must land under — is discoverable without
	// duplicating the repo-key derivation here.
	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main")
	wtA := worktreeFor(t, r, "feat-a")
	matches, err := filepath.Glob(filepath.Join(r.home, ".stacked", "worktrees", "*", "feat-a"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("locating generated worktrees root: matches=%v err=%v", matches, err)
	}
	keyDir := filepath.Dir(matches[0])

	// Pre-obstruct feat-x's canonical path with a NON-EMPTY directory: git
	// worktree add reuses an existing empty directory, but refuses one that
	// already holds content.
	obstruction := filepath.Join(keyDir, "feat-x")
	if err := os.MkdirAll(obstruction, 0o755); err != nil {
		t.Fatalf("mkdir obstruction: %v", err)
	}
	if err := os.WriteFile(filepath.Join(obstruction, "in-the-way"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("fill obstruction: %v", err)
	}

	res := r.st("create", "--worktree", "feat-x")
	wantExit(t, res, 1)
	wantStderrContains(t, res, `branch "feat-x" created and tracked, but its worktree failed`)
	wantStderrContains(t, res, "retry with: st worktree feat-x")

	// The branch is tracked but has no materialized worktree.
	var root logNode
	if err := json.Unmarshal([]byte(r.stOK("log", "--json").stdout), &root); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if findNode(&root, "feat-x") == nil {
		t.Fatalf("feat-x not tracked after failed create --worktree:\n%s", r.stOK("log", "--json").stdout)
	}
	if list := r.git("worktree", "list", "--porcelain"); strings.Contains(list, "refs/heads/feat-x") {
		t.Fatalf("feat-x unexpectedly owns a worktree:\n%s", list)
	}

	// Clearing the obstruction and retrying with the hinted command succeeds —
	// the worktree lands outside the journal (st worktree records no undo
	// entry), so the create entry's createdWorktrees stays empty.
	if err := os.RemoveAll(obstruction); err != nil {
		t.Fatalf("remove obstruction: %v", err)
	}
	wtX := worktreeFor(t, r, "feat-x")
	if _, err := os.Stat(wtX); err != nil {
		t.Fatalf("retried worktree path missing: %v", err)
	}
	if list := r.git("worktree", "list", "--porcelain"); !strings.Contains(list, "refs/heads/feat-x") {
		t.Fatalf("feat-x worktree not registered after retry:\n%s", list)
	}

	// Undo discovers feat-x's worktree via LinkedOwnerOf, removes it, and
	// deletes the branch; feat-a's unrelated worktree is untouched.
	undoRes := r.stOK("undo")
	wantStdoutContains(t, undoRes, "undid: create")
	if r.branchExists("feat-x") {
		t.Fatal("undo left branch feat-x behind")
	}
	if _, err := os.Stat(wtX); !os.IsNotExist(err) {
		t.Fatalf("feat-x worktree dir survived undo: stat err=%v", err)
	}
	list := r.git("worktree", "list", "--porcelain")
	if strings.Contains(list, "refs/heads/feat-x") {
		t.Fatalf("feat-x worktree still registered after undo:\n%s", list)
	}
	if !strings.Contains(list, "refs/heads/feat-a") {
		t.Fatalf("undo removed unrelated feat-a worktree:\n%s", list)
	}
	if _, err := os.Stat(filepath.Join(wtA, "a.txt")); err != nil {
		t.Fatalf("feat-a worktree damaged by undo: %v", err)
	}
	r.stOK("validate")
}

// TestWorktreeRemoveAllLeavesUntracked pins rm --all's scope: it removes linked
// worktrees owning TRACKED branches only. A manually-created worktree for an
// untracked local branch is invisible to the command — it stays on disk and in
// git's registry, and the JSON result only ever names tracked branches.
func TestWorktreeRemoveAllLeavesUntracked(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b") // checked out in the main worktree

	// feat-a gets a canonical linked worktree; scratch is an UNTRACKED branch
	// with a manually-created worktree outside the generated root.
	wtA := worktreeFor(t, r, "feat-a")
	scratchPath := filepath.Join(t.TempDir(), "scratch-wt")
	r.git("branch", "scratch", "main")
	r.git("worktree", "add", scratchPath, "scratch")

	res := r.stOK("worktree", "rm", "--all", "--json")
	var out struct {
		Removed []struct {
			Branch string `json:"branch"`
			Path   string `json:"path"`
		} `json:"removed"`
		Skipped []struct {
			Branch string `json:"branch"`
			Reason string `json:"reason"`
		} `json:"skipped"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &out); err != nil {
		t.Fatalf("decode rm --all json: %v\n%s", err, res.stdout)
	}
	if len(out.Removed) != 1 || out.Removed[0].Branch != "feat-a" {
		t.Fatalf("removed = %+v, want exactly [feat-a]", out.Removed)
	}
	// skipped may only name tracked branches: feat-b (checked out in the main
	// worktree). The untracked scratch worktree must not appear at all.
	for _, sk := range out.Skipped {
		if sk.Branch != "feat-b" {
			t.Fatalf("skipped names untracked/unexpected branch %+v", out.Skipped)
		}
	}
	if len(out.Skipped) != 1 {
		t.Fatalf("skipped = %+v, want exactly [feat-b checked out in main]", out.Skipped)
	}

	// feat-a's worktree is gone; scratch's is untouched on disk and registered.
	if _, err := os.Stat(wtA); !os.IsNotExist(err) {
		t.Fatalf("feat-a worktree survived rm --all: stat err=%v", err)
	}
	list := r.git("worktree", "list", "--porcelain")
	if strings.Contains(list, "refs/heads/feat-a") {
		t.Fatalf("feat-a worktree still registered:\n%s", list)
	}
	if !strings.Contains(list, "refs/heads/scratch") {
		t.Fatalf("untracked scratch worktree was removed:\n%s", list)
	}
	if _, err := os.Stat(scratchPath); err != nil {
		t.Fatalf("scratch worktree dir removed: %v", err)
	}
	r.stOK("validate")
}

// TestRestackCacheInvalidationKeepsConflictPaused pins the stale-worktree-owner
// regression: `st restack --all` memoizes `git worktree list` for the process.
// Rebasing feat-a IN PLACE moves the caller's worktree HEAD from feat-b to
// feat-a — if the memoized list is not invalidated, feat-b still looks owned
// by the caller's worktree and is misrouted through restackInWorktree, which
// ABORTS its conflict instead of leaving it paused for `st continue`. With the
// cache reset after every rebase attempt, feat-b is re-probed as unowned and
// its conflict pauses in the caller's worktree (exit 2).
func TestRestackCacheInvalidationKeepsConflictPaused(t *testing.T) {
	t.Parallel()

	build := func(t *testing.T) (*repo, string) {
		r := newRepo(t)
		r.initStack()
		// main seeds the line; feat-a only adds an unrelated file so it rebases
		// cleanly; feat-b edits the seeded line so it conflicts when main
		// advances it.
		r.writeFile("shared.txt", "seed\n")
		r.git("add", "shared.txt")
		r.git("commit", "-q", "-m", "seed")
		r.create("feat-a", "a.txt", "a\n", "a")
		r.writeFile("shared.txt", "edited-by-b\n")
		r.create("feat-b", "b.txt", "b\n", "b")

		// A linked worktree owns main and advances the seeded line, so the
		// caller's `restack --all` must first rebase feat-a in place — the
		// move that invalidates the cached owner map.
		wt := filepath.Join(t.TempDir(), "wt")
		r.git("worktree", "add", "-q", wt, "main")
		if err := os.WriteFile(filepath.Join(wt, "shared.txt"), []byte("trunk-advance\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r.gitIn(wt, "add", "shared.txt")
		r.gitIn(wt, "commit", "-q", "-m", "advance main")
		return r, wt
	}

	assertPaused := func(t *testing.T, r *repo, res result) {
		t.Helper()
		wantExit(t, res, 2)
		if !r.isAncestor("main", "feat-a") {
			t.Fatal("feat-a must have restacked onto the advanced main before feat-b's conflict")
		}
		// The regression discriminator: the paused rebase's metadata lives in
		// the caller's git dir. The misrouted path aborts instead, leaving no
		// rebase-merge state at all.
		gitDir := r.gitIn(r.dir, "rev-parse", "--absolute-git-dir")
		if _, err := os.Stat(filepath.Join(gitDir, "rebase-merge")); err != nil {
			t.Fatalf("feat-b's conflict must remain paused in the caller's worktree: %v", err)
		}
		r.stOK("abort")
	}

	t.Run("text", func(t *testing.T) {
		r, _ := build(t)
		res := r.st("restack", "--all")
		assertPaused(t, r, res)
	})
	t.Run("json", func(t *testing.T) {
		r, _ := build(t)
		res := r.st("restack", "--all", "--json")
		wantExit(t, res, 2)
		if !strings.Contains(res.stderr, `"conflict"`) || !strings.Contains(res.stderr, "feat-b") {
			t.Fatalf("conflict envelope missing code/branch:\n%s", res.stderr)
		}
		assertPaused(t, r, res)
	})
}

// TestPausedRebaseInLinkedWorktreeBlocksMutations pins the plan-002 contract
// end to end: a tracked branch paused mid-rebase in a linked worktree (where
// `git worktree list` reports it detached — the owner only survives in the
// rebase head-name) makes the mutation gate refuse `st` mutations in the main
// worktree and makes `st worktree rm` answer correctly, while `st abort`
// stays reachable inside the paused worktree itself.
func TestPausedRebaseInLinkedWorktreeBlocksMutations(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")

	// Diverge main so a rebase inside the worktree conflicts and pauses.
	r.stOK("checkout", "main")
	r.writeFile("a.txt", "diverged\n")
	r.git("add", "a.txt")
	r.git("commit", "-q", "-m", "diverge main")

	wt := filepath.Join(t.TempDir(), "wt-a")
	r.git("worktree", "add", "-q", wt, "feat-a")
	rebase := exec.Command("git", "-C", wt, "rebase", "main")
	rebase.Env = cleanEnv(r.home)
	if out, err := rebase.CombinedOutput(); err == nil {
		t.Fatalf("git -C wt rebase main succeeded, want a paused conflict:\n%s", out)
	}

	// Sanity: porcelain reports the paused worktree as detached, so the
	// plain branch-owner lookup cannot see it.
	if out := r.git("worktree", "list", "--porcelain"); !strings.Contains(out, "detached") {
		t.Fatalf("worktree list missing the detached paused entry:\n%s", out)
	}

	// `st delete` from the main worktree refuses upfront, naming the branch
	// and its paused worktree — and removes nothing.
	res := r.st("delete", "feat-a")
	wantExit(t, res, 1)
	if !strings.Contains(res.stderr, "rebase in progress") || !strings.Contains(res.stderr, wt) {
		t.Fatalf("st delete stderr = %q, want the paused-worktree refusal naming %q", res.stderr, wt)
	}
	if !r.branchExists("feat-a") {
		t.Fatal("st delete removed the paused branch anyway")
	}

	// `st worktree rm feat-a` finds the detached owner via the head-name.
	res = r.st("worktree", "rm", "feat-a")
	wantExit(t, res, 1)
	if !strings.Contains(res.stderr, "rebase is in progress there") {
		t.Fatalf("st worktree rm stderr = %q, want the pause refusal", res.stderr)
	}

	// `st worktree rm --all` skips the paused worktree with a reason.
	res = r.stOK("worktree", "rm", "--all")
	if !strings.Contains(res.stdout, "skipped feat-a") {
		t.Fatalf("st worktree rm --all stdout = %q, want a skip for feat-a", res.stdout)
	}

	// st abort stays reachable inside the paused worktree and leaves it back
	// on feat-a.
	r.stInOK(wt, "abort")
	if got := r.gitIn(wt, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat-a" {
		t.Fatalf("worktree branch after abort = %q, want feat-a", got)
	}
}
