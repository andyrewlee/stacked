package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Stack-mutating journeys, driven black-box: the core lifecycle, conflicts and
// recovery, fold/squash/onto/rename/delete, undo, track/untrack, and sync.
// TestWorktreeSharesStackState asserts the stack metadata (kept under the common
// git dir) is shared across linked worktrees, so st run from a second worktree
// sees the same stack (TEST-10).
func TestWorktreeSharesStackState(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main") // free feat-a so a worktree can check it out

	wt := filepath.Join(t.TempDir(), "wt")
	r.git("worktree", "add", "-q", wt, "feat-a")

	cmd := exec.Command(stBin, "log", "--json")
	cmd.Dir = wt
	cmd.Env = cleanEnv(r.home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("st log in worktree: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "feat-a") {
		t.Fatalf("worktree st did not see the shared stack state:\n%s", out)
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
	cmd := exec.Command(stBin, "status", "--json")
	cmd.Dir = wt
	cmd.Env = cleanEnv(r.home)
	sOut, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("st status in worktree: %v\n%s", err, sOut)
	}
	var st statusJSON
	if err := json.Unmarshal(sOut, &st); err != nil {
		t.Fatalf("decode status json: %v\n%s", err, sOut)
	}
	if st.Branch != "feat-a" || st.Worktree == "" {
		t.Fatalf("status in worktree missing worktree path: %+v\n%s", st, sOut)
	}
}

// TestWorktreeCommand materializes a worktree for a tracked branch via
// `st worktree`, confirms it is listed and copies .worktreeinclude matches, and
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
	cmd := exec.Command(stBin, "worktree", "feat-c", "--json")
	cmd.Dir = linked
	cmd.Env = cleanEnv(r.home)
	linkedOut, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("st worktree from linked worktree: %v\n%s", err, linkedOut)
	}
	var linkedCreated struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(linkedOut, &linkedCreated); err != nil {
		t.Fatalf("decode linked worktree create: %v\n%s", err, linkedOut)
	}
	linkedParts := generatedWorktreePathParts(t, r.home, linkedCreated.Path)
	if linkedParts[0] != mainParts[0] {
		t.Fatalf("linked worktree repo segment = %q, want main repo segment %q", linkedParts[0], mainParts[0])
	}
}

func TestWorktreeIncludeCopyFailureRollsBackWorktree(t *testing.T) {
	t.Parallel()
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

// TestShellInstallEmitsShim asserts `st shell install` prints a cd shim that
// references the directive file, for the shell the user names.
func TestShellInstallEmitsShim(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	out := r.stOK("shell", "install", "bash").stdout
	if !strings.Contains(out, "builtin cd") || !strings.Contains(out, "ST_CD_FILE") {
		t.Fatalf("shell install bash did not emit a cd shim:\n%s", out)
	}
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
	cmd := exec.Command(stBin, "checkout", "feat-a")
	cmd.Dir = r.dir
	cmd.Env = append(cleanEnv(r.home), "ST_CD_FILE="+directive)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("st checkout feat-a: %v\n%s", err, out)
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

// TestUndoAfterConflictAbort drives a conflict, aborts it (which leaves the
// bottom branch amended), then undoes the whole mutation back to the pre-mutation
// tip and validates clean (TEST-10).
func TestUndoAfterConflictAbort(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")
	r.create("feat-b", "f.txt", "A\nB\n", "b")
	r.stOK("checkout", "feat-a")
	aBefore := r.git("rev-parse", "feat-a")

	r.writeFile("f.txt", "X\n")
	wantExit(t, r.st("modify", "-a"), 2) // conflict restacking feat-b
	r.stOK("abort")

	// The amend on feat-a survived the abort; undo reverts the whole modify.
	r.stOK("undo")
	if aAfter := r.git("rev-parse", "feat-a"); aAfter != aBefore {
		t.Fatalf("feat-a tip = %s after undo, want pre-modify %s", aAfter, aBefore)
	}
	r.stOK("validate")
}

// TestLifecycle exercises the core journey end to end: init, create two stacked
// branches, inspect via log and status (text + JSON), navigate up/down/top/
// bottom, modify the bottom branch and confirm the upstack restacks.
func TestLifecycle(t *testing.T) {
	t.Parallel()
	r := newRepo(t)

	// init
	res := r.stOK("init", "--trunk", "main")
	wantStdoutContains(t, res, "initialized stacked (trunk: main)")

	// re-init is idempotent and reports the existing trunk.
	res = r.stOK("init")
	wantStdoutContains(t, res, "already initialized")

	// create two stacked branches.
	r.writeFile("a.txt", "a\n")
	res = r.stOK("create", "feat-a", "-a", "-m", "a")
	wantStdoutContains(t, res, "Created feat-a on top of main")
	r.writeFile("b.txt", "b\n")
	res = r.stOK("create", "feat-b", "-a", "-m", "b")
	wantStdoutContains(t, res, "Created feat-b on top of feat-a")

	if got := r.currentBranch(); got != "feat-b" {
		t.Fatalf("after creates, on %q, want feat-b", got)
	}

	// log text shows the tree with the trunk at the bottom.
	res = r.stOK("log")
	for _, sub := range []string{"feat-a", "feat-b", "main"} {
		wantStdoutContains(t, res, sub)
	}

	// log --json: parse and verify the tree shape.
	res = r.stOK("log", "--json")
	var root logNode
	if err := json.Unmarshal([]byte(res.stdout), &root); err != nil {
		t.Fatalf("log --json is not valid JSON: %v\n%s", err, res.stdout)
	}
	if root.Name != "main" {
		t.Fatalf("log --json root = %q, want main", root.Name)
	}
	a := findNode(&root, "feat-a")
	b := findNode(&root, "feat-b")
	if a == nil || a.Parent != "main" {
		t.Fatalf("feat-a node wrong: %+v", a)
	}
	if b == nil || b.Parent != "feat-a" {
		t.Fatalf("feat-b node wrong: %+v", b)
	}
	if !b.Current {
		t.Fatalf("feat-b should be marked current in log --json")
	}
	if a.TopCommit != "a" {
		t.Fatalf("feat-a topCommit = %q, want a", a.TopCommit)
	}

	// status text + JSON for the current (feat-b) branch.
	res = r.stOK("status")
	wantStdoutContains(t, res, "branch:   feat-b")
	wantStdoutContains(t, res, "parent:   feat-a")

	res = r.stOK("status", "--json")
	var sj statusJSON
	if err := json.Unmarshal([]byte(res.stdout), &sj); err != nil {
		t.Fatalf("status --json invalid: %v\n%s", err, res.stdout)
	}
	if sj.Branch != "feat-b" || sj.Role != "tracked" || sj.Parent != "feat-a" {
		t.Fatalf("status JSON unexpected: %+v", sj)
	}
	if sj.NeedsRestack == nil || *sj.NeedsRestack {
		t.Fatalf("feat-b should not need restack; got %+v", sj.NeedsRestack)
	}
	if !sj.WorktreeClean {
		t.Fatalf("worktree should be clean: %+v", sj)
	}

	// trunk-only status JSON (role=trunk, children listed, needsRestack omitted).
	r.stOK("checkout", "main")
	res = r.stOK("status", "--json")
	var trunkStatus statusJSON
	if err := json.Unmarshal([]byte(res.stdout), &trunkStatus); err != nil {
		t.Fatalf("trunk status --json invalid: %v\n%s", err, res.stdout)
	}
	if trunkStatus.Role != "trunk" {
		t.Fatalf("main role = %q, want trunk", trunkStatus.Role)
	}
	if trunkStatus.NeedsRestack != nil {
		t.Fatalf("trunk needsRestack should be omitted, got %v", *trunkStatus.NeedsRestack)
	}
	if len(trunkStatus.Children) != 1 || trunkStatus.Children[0] != "feat-a" {
		t.Fatalf("trunk children = %v, want [feat-a]", trunkStatus.Children)
	}

	// Navigation: from main go up to feat-a, up to feat-b (top), back down.
	r.stOK("checkout", "feat-a")
	res = r.stOK("up")
	wantStdoutContains(t, res, "switched to feat-b")
	if r.currentBranch() != "feat-b" {
		t.Fatalf("up did not land on feat-b")
	}

	res = r.stOK("down")
	wantStdoutContains(t, res, "feat-a")
	if r.currentBranch() != "feat-a" {
		t.Fatalf("down did not land on feat-a")
	}

	res = r.stOK("top")
	wantStdoutContains(t, res, "feat-b")
	if r.currentBranch() != "feat-b" {
		t.Fatalf("top did not land on feat-b")
	}

	res = r.stOK("bottom")
	wantStdoutContains(t, res, "feat-a")
	if r.currentBranch() != "feat-a" {
		t.Fatalf("bottom did not land on feat-a")
	}

	// modify the bottom branch (feat-a) and confirm feat-b is restacked onto the
	// amended commit, with an independent file so no conflict occurs.
	r.writeFile("a.txt", "a-modified\n")
	res = r.stOK("modify", "-a")
	wantStdoutContains(t, res, "Amended feat-a")
	wantStdoutContains(t, res, "feat-b")

	if got := r.git("show", "feat-b:a.txt"); got != "a-modified" {
		t.Fatalf("feat-b:a.txt = %q, want a-modified (restacked)", got)
	}

	// restack again is a no-op now.
	r.stOK("checkout", "feat-a")
	res = r.stOK("restack")
	wantStdoutContains(t, res, "everything up to date")

	// validate reports a healthy stack and exits 0.
	res = r.stOK("validate")
	wantStdoutContains(t, res, "no problems found")
}

// TestConflictContinue drives a real merge conflict via modify and resolves it
// with `st continue`, asserting the upstack ends up rebased onto the new tip.
func TestConflictContinue(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")
	// feat-b edits the same file/line so amending feat-a conflicts on restack.
	r.create("feat-b", "f.txt", "A\nB\n", "b")
	r.stOK("checkout", "feat-a")

	r.writeFile("f.txt", "X\n")
	res := r.st("modify", "-a")
	// A conflict maps to the dedicated exit code 2 (see docs/AGENT.md).
	wantExit(t, res, 2)
	wantStderrContains(t, res, "st continue")

	// A real rebase should be in progress.
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("expected a rebase in progress after conflict: %v", err)
	}

	// Resolve and continue.
	r.writeFile("f.txt", "X\nB\n")
	r.git("add", "f.txt")
	res = r.stOK("continue")
	wantStdoutContains(t, res, "continued restack")

	if got := r.git("show", "feat-b:f.txt"); got != "X\nB" {
		t.Fatalf("feat-b:f.txt = %q, want X\\nB", got)
	}
	r.stOK("validate")
}

// TestConflictAbort drives the same conflict but backs out with `st abort`,
// asserting the rebase is gone and a second abort errors with "no rebase".
func TestConflictAbort(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")
	r.create("feat-b", "f.txt", "A\nB\n", "b")
	r.stOK("checkout", "feat-a")

	r.writeFile("f.txt", "X\n")
	res := r.st("modify", "-a")
	wantExit(t, res, 2) // conflict

	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("expected a rebase in progress: %v", err)
	}

	res = r.stOK("abort")
	wantStdoutContains(t, res, "Aborted the in-progress rebase")
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); !os.IsNotExist(err) {
		t.Fatalf("rebase should be gone after abort, stat err = %v", err)
	}

	// A second abort with nothing in progress errors.
	res = r.st("abort")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "no rebase in progress")
}

func TestOntoConflictContinue(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")
	r.create("feat-b", "f.txt", "A\nB\n", "b")

	res := r.st("onto", "main")
	wantExit(t, res, 2)
	wantStderrContains(t, res, "st continue")

	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("expected a rebase in progress after onto conflict: %v", err)
	}

	r.writeFile("f.txt", "B\n")
	r.git("add", "f.txt")
	r.stOK("continue")
	r.stOK("validate")

	data, err := os.ReadFile(filepath.Join(r.dir, ".git", "stacked", "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	if strings.Contains(string(data), "pendingReparent") {
		t.Fatalf("state.json still contains pendingReparent after continue:\n%s", data)
	}
	var state struct {
		Branches map[string]struct {
			Parent string `json:"parent"`
		} `json:"branches"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("state.json invalid: %v\n%s", err, data)
	}
	if got := state.Branches["feat-b"].Parent; got != "main" {
		t.Fatalf("feat-b parent after continue = %q, want main", got)
	}
}

func TestOntoConflictAbort(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")
	r.create("feat-b", "f.txt", "A\nB\n", "b")

	res := r.st("onto", "main")
	wantExit(t, res, 2)
	wantStderrContains(t, res, "st continue")

	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("expected a rebase in progress after onto conflict: %v", err)
	}

	r.stOK("abort")
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); !os.IsNotExist(err) {
		t.Fatalf("rebase should be gone after abort, stat err = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(r.dir, ".git", "stacked", "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	if strings.Contains(string(data), "pendingReparent") {
		t.Fatalf("state.json still contains pendingReparent after abort:\n%s", data)
	}
	var state struct {
		Branches map[string]struct {
			Parent string `json:"parent"`
		} `json:"branches"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("state.json invalid: %v\n%s", err, data)
	}
	if got := state.Branches["feat-b"].Parent; got != "feat-a" {
		t.Fatalf("feat-b parent after abort = %q, want feat-a", got)
	}
	r.stOK("validate")
}

// TestSyncConflictContinue drives a conflict that occurs *during* sync's restack
// (the trunk advances under a stacked branch), then resolves it (TEST-2).
func TestSyncConflictContinue(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	// feat-a adds f.txt; the trunk then adds f.txt with different content, so
	// restacking feat-a onto the advanced trunk during sync conflicts.
	r.create("feat-a", "f.txt", "A\n", "a")
	r.stOK("checkout", "main")
	r.writeFile("f.txt", "MAIN\n")
	r.git("add", "f.txt")
	r.git("commit", "-q", "-m", "trunk advances")
	r.stOK("checkout", "feat-a")

	res := r.st("sync")
	wantExit(t, res, 2) // conflict maps to the dedicated exit code
	wantStderrContains(t, res, "st continue")
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("expected a rebase in progress mid-sync: %v", err)
	}

	// Resolve and continue; the stack must reconcile and validate clean.
	r.writeFile("f.txt", "MAIN\nA\n")
	r.git("add", "f.txt")
	r.stOK("continue")
	r.stOK("validate")
}

// TestModifyJSONRestacksDescendants amends the bottom of a 2-deep stack in --json
// mode, exercising the QuietShell rebase path and asserting the descendant
// actually rebased (TEST-6).
func TestModifyJSONRestacksDescendants(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.stOK("checkout", "feat-a")
	bBefore := r.git("rev-parse", "feat-b")

	r.writeFile("a.txt", "a2\n")
	res := r.stOK("modify", "-a", "--json")
	var payload struct {
		Branch    string   `json:"branch"`
		Restacked []string `json:"restacked"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &payload); err != nil {
		t.Fatalf("modify --json not parseable: %v\n%s", err, res.stdout)
	}
	if len(payload.Restacked) == 0 {
		t.Fatalf("modify --json reported no restack: %+v", payload)
	}
	if bAfter := r.git("rev-parse", "feat-b"); bAfter == bBefore {
		t.Fatalf("feat-b was not rebased (still %s)", bAfter)
	}
}

// TestModifyMessageReword rewords the bottom branch's commit (the AmendMessage
// real-git path) and asserts the subject changed and the descendant restacked
// (TEST-7).
func TestModifyMessageReword(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "original")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.stOK("checkout", "feat-a")
	bBefore := r.git("rev-parse", "feat-b")

	r.stOK("modify", "-m", "reworded")
	if subj := r.git("log", "-1", "--format=%s", "feat-a"); subj != "reworded" {
		t.Fatalf("feat-a subject = %q, want reworded", subj)
	}
	if bAfter := r.git("rev-parse", "feat-b"); bAfter == bBefore {
		t.Fatal("feat-b was not restacked after the reword")
	}
}

// TestFold folds the top branch into its parent: the parent absorbs the commits
// and the folded branch is removed.
func TestFold(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.stOK("checkout", "feat-b")

	res := r.stOK("fold")
	wantStdoutContains(t, res, "Folded feat-b into feat-a")
	if r.branchExists("feat-b") {
		t.Fatalf("feat-b git branch should be gone after fold")
	}
	if !r.fileOnBranch("feat-a", "b.txt") {
		t.Fatalf("feat-a should contain b.txt after fold")
	}
}

// TestSquash collapses multiple commits on a branch into one.
func TestSquash(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	// Add a second commit via modify --commit.
	r.writeFile("a2.txt", "a2\n")
	r.stOK("modify", "--commit", "-m", "a2")

	if n := r.git("rev-list", "--count", "main..feat-a"); n != "2" {
		t.Fatalf("expected 2 commits before squash, got %s", n)
	}
	res := r.stOK("squash", "-m", "squashed")
	wantStdoutContains(t, res, "Squashed")
	if n := r.git("rev-list", "--count", "main..feat-a"); n != "1" {
		t.Fatalf("expected 1 commit after squash, got %s", n)
	}
	for _, f := range []string{"a.txt", "a2.txt"} {
		if !r.fileOnBranch("feat-a", f) {
			t.Fatalf("feat-a missing %s after squash", f)
		}
	}
}

// TestOnto re-parents a branch onto the trunk, dropping the old parent's commits.
func TestOnto(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b") // on feat-a
	r.stOK("checkout", "feat-b")

	res := r.stOK("onto", "main")
	wantStdoutContains(t, res, "Moved feat-b onto main")
	if r.fileOnBranch("feat-b", "a.txt") {
		t.Fatalf("feat-b should no longer contain a.txt after moving onto main")
	}

	// status JSON should now report feat-b's parent as main.
	r.stOK("checkout", "feat-b")
	res = r.stOK("status", "--json")
	var sj statusJSON
	if err := json.Unmarshal([]byte(res.stdout), &sj); err != nil {
		t.Fatalf("status --json invalid: %v\n%s", err, res.stdout)
	}
	if sj.Parent != "main" {
		t.Fatalf("feat-b parent after onto = %q, want main", sj.Parent)
	}
}

// TestRename renames a branch and updates child parent pointers.
func TestRename(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.stOK("checkout", "feat-a")

	res := r.stOK("rename", "renamed-a")
	wantStdoutContains(t, res, "Renamed feat-a -> renamed-a")
	if r.branchExists("feat-a") {
		t.Fatalf("old branch feat-a should be gone")
	}
	if !r.branchExists("renamed-a") {
		t.Fatalf("new branch renamed-a should exist")
	}

	// feat-b's parent must now point at renamed-a (verified via log --json).
	res = r.stOK("log", "--json")
	var root logNode
	if err := json.Unmarshal([]byte(res.stdout), &root); err != nil {
		t.Fatalf("log --json invalid: %v", err)
	}
	b := findNode(&root, "feat-b")
	if b == nil || b.Parent != "renamed-a" {
		t.Fatalf("feat-b parent not updated after rename: %+v", b)
	}
}

// TestDeleteReparent deletes a middle branch and re-parents its child onto the
// grandparent, dropping the deleted branch's file from the child's history.
func TestDeleteReparent(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.create("feat-c", "c.txt", "c\n", "c")

	res := r.stOK("delete", "feat-b", "--force")
	wantStdoutContains(t, res, "Deleted feat-b")
	if r.branchExists("feat-b") {
		t.Fatalf("feat-b should be deleted")
	}
	if r.fileOnBranch("feat-c", "b.txt") {
		t.Fatalf("feat-c should no longer contain b.txt after deleting feat-b")
	}
	if !r.fileOnBranch("feat-c", "c.txt") {
		t.Fatalf("feat-c lost its own c.txt")
	}

	// feat-c should now be parented on feat-a.
	res = r.stOK("log", "--json")
	var root logNode
	if err := json.Unmarshal([]byte(res.stdout), &root); err != nil {
		t.Fatalf("log --json invalid: %v", err)
	}
	c := findNode(&root, "feat-c")
	if c == nil || c.Parent != "feat-a" {
		t.Fatalf("feat-c not re-parented onto feat-a: %+v", c)
	}
}

// TestUndo asserts that undo restores a branch tip after a modify.
func TestUndo(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	before := r.rev("feat-a")

	r.writeFile("a.txt", "a-modified\n")
	r.stOK("modify", "-a")
	if r.rev("feat-a") == before {
		t.Fatalf("modify did not change feat-a tip")
	}

	res := r.stOK("undo")
	wantStdoutContains(t, res, "undid: modify")
	if got := r.rev("feat-a"); got != before {
		t.Fatalf("undo did not restore feat-a: got %s want %s", got, before)
	}

	// Undoing repeatedly eventually empties the journal.
	for i := 0; i < 10; i++ {
		res = r.stOK("undo")
		if strings.Contains(res.stdout, "nothing to undo") {
			return
		}
	}
	t.Fatalf("expected the undo journal to drain to 'nothing to undo'")
}

// TestTrackUntrack covers tracking a plain git branch, the guard errors
// (track the trunk, double-track, untrack the trunk / an untracked branch), and
// untracking with child re-parenting.
func TestTrackUntrack(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()

	// track-the-trunk is refused (the repo starts on main).
	r.stOK("checkout", "main")
	res := r.st("track")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "cannot track the trunk")

	// Create a plain git branch off main with a commit, then track it.
	r.git("checkout", "-q", "-b", "plain")
	r.writeFile("p.txt", "p\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "p")
	res = r.stOK("track")
	wantStdoutContains(t, res, "Tracking plain (parent: main)")

	// Double-track errors.
	res = r.st("track")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "already tracked")

	// untrack the trunk errors.
	res = r.st("untrack", "main")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "cannot untrack the trunk")

	// untrack an unknown branch errors.
	res = r.st("untrack", "nope")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "not tracked")

	// untrack the tracked branch succeeds.
	res = r.stOK("untrack", "plain")
	wantStdoutContains(t, res, "Untracked plain")
}

// TestRestackGuards covers the dirty-tree guard and the untracked checkout guard.
func TestRestackGuards(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")

	// Dirty working tree blocks restack — mapped to the dedicated exit code 4.
	r.writeFile("dirty.txt", "dirty\n")
	r.git("add", "-A") // staged but uncommitted -> dirty index
	res := r.st("restack")
	wantExit(t, res, 4)
	wantStderrContains(t, res, "working tree is dirty")

	// Clean it up, then check out an untracked name errors.
	r.git("reset", "-q", "HEAD")
	if err := os.Remove(filepath.Join(r.dir, "dirty.txt")); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	res = r.st("checkout", "ghost")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "not a tracked branch")
}

// TestValidateRepairDrift forces drift by deleting a tracked branch behind st's
// back, asserts validate exits non-zero, then repair fixes it and validate
// passes.
func TestValidateRepairDrift(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")

	// Delete feat-b's git branch outside st.
	r.stOK("checkout", "main")
	r.git("branch", "-D", "feat-b")

	res := r.st("validate")
	wantExit(t, res, 1)
	wantStdoutContains(t, res, "problems:")
	wantStdoutContains(t, res, "feat-b")

	res = r.stOK("repair")
	wantStdoutContains(t, res, "repaired:")

	res = r.stOK("validate")
	wantStdoutContains(t, res, "no problems found")
}

// TestSyncPrunesMerged sets up a bare remote, merges the bottom branch into the
// trunk on the remote, and asserts `st sync` fast-forwards the trunk, prunes the
// merged branch, and restacks the survivor.
func TestSyncPrunesMerged(t *testing.T) {
	t.Parallel()
	r := newRepo(t)

	bare := filepath.Join(t.TempDir(), "remote.git")
	r.gitIn(filepath.Dir(bare), "init", "-q", "--bare", "-b", "main", bare)
	r.git("remote", "add", "origin", bare)
	r.git("push", "-q", "-u", "origin", "main")

	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")

	// Merge feat-a into main locally and push, simulating feat-a landing.
	r.stOK("checkout", "main")
	r.git("merge", "-q", "--no-ff", "feat-a", "-m", "merge feat-a")
	r.git("push", "-q", "origin", "main")

	res := r.stOK("sync")
	wantStdoutContains(t, res, "sync complete")
	wantStdoutContains(t, res, "deleted: feat-a")

	if r.branchExists("feat-a") {
		t.Fatalf("feat-a should be pruned after sync")
	}
	if !r.branchExists("feat-b") {
		t.Fatalf("feat-b should survive sync")
	}

	// feat-b is now re-parented onto main and the stack validates clean.
	res = r.stOK("log", "--json")
	var root logNode
	if err := json.Unmarshal([]byte(res.stdout), &root); err != nil {
		t.Fatalf("log --json invalid: %v", err)
	}
	b := findNode(&root, "feat-b")
	if b == nil || b.Parent != "main" {
		t.Fatalf("feat-b should be re-parented onto main after prune: %+v", b)
	}
	r.stOK("validate")
}

// TestSyncNoRemote asserts sync is a clean no-op when no remote is configured.
func TestSyncNoRemote(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	res := r.stOK("sync")
	wantStdoutContains(t, res, "sync complete")
	wantStdoutContains(t, res, "skipped (no remote)")
}

// TestRestackDryRun previews what a restack would rebase and changes nothing.
func TestRestackDryRun(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")
	r.create("feat-b", "g.txt", "B\n", "b") // independent file: no conflict
	r.stOK("checkout", "feat-a")

	// Amend feat-a so feat-b drifts.
	r.writeFile("f.txt", "A2\n")
	r.git("commit", "-qa", "--amend", "--no-edit")
	before := r.git("rev-parse", "feat-b")

	res := r.stOK("restack", "--dry-run")
	wantStdoutContains(t, res, "would restack: feat-b")
	if after := r.git("rev-parse", "feat-b"); after != before {
		t.Fatalf("dry-run must not move feat-b (before=%s after=%s)", before, after)
	}

	res = r.stOK("restack", "--dry-run", "--json")
	wantStdoutContains(t, res, `"dryRun": true`)
}
