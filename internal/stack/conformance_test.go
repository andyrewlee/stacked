package stack

// conformance_test.go — fake↔shell behavioral conformance for the Git port.
// make test-fast's authority rests on fakeGit modeling git faithfully; nothing
// else asserts the two implementations agree on a shared scenario. Each test
// here seeds an equivalent repository shape on both (newFakeGit's seeded
// main == `git init` + `symbolic-ref HEAD refs/heads/main` + one commit),
// drives the same port calls, and
// asserts RELATIONAL outcomes — which refs exist, whether tips moved, what
// ancestry holds — never literal SHAs or paths. A fake divergence is a fake
// bug to fix in fakegit_test.go; a shell divergence is a production bug to
// report. New port methods should grow a row here — that is the contract.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/andyrewlee/stacked/internal/git"
)

// confDriver is one conformance arm: the port under test plus the
// impl-specific seeding knobs the port itself cannot express (real commits,
// worktree dirt).
type confDriver struct {
	t    *testing.T
	g    Git
	fake *fakeGit // non-nil only on the fake arm
	dir  string   // repo root, only on the shell arm
	seq  int      // unique file counter for shell commits
}

// runConformance drives fn against the fake, then against git.Shell on a real
// temp-dir repo chdir'd into (the port's cwd-scoped methods need it; the shell
// arm therefore must not t.Parallel).
func runConformance(t *testing.T, fn func(d *confDriver)) {
	t.Helper()
	t.Run("fake", func(t *testing.T) {
		f := newFakeGit()
		fn(&confDriver{t: t, g: f, fake: f})
	})
	t.Run("shell", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		confGit(t, "init", "-q")
		confGit(t, "symbolic-ref", "HEAD", "refs/heads/main")
		confGit(t, "config", "user.email", "test@example.com")
		confGit(t, "config", "user.name", "test")
		confWrite(t, "base.txt", "base\n")
		confGit(t, "add", "-A")
		confGit(t, "commit", "-q", "-m", "init")
		fn(&confDriver{t: t, g: git.Shell{}, dir: dir})
	})
}

func confGit(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func confWrite(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// commit appends one commit carrying subject to the checked-out branch —
// the fake's synthetic commit on one arm, a real file+commit on the other.
func (d *confDriver) commit(subject string) {
	d.t.Helper()
	if d.fake != nil {
		d.fake.commit(subject)
		return
	}
	d.seq++
	confWrite(d.t, fmt.Sprintf("file-%d.txt", d.seq), subject+"\n")
	confGit(d.t, "add", "-A")
	confGit(d.t, "commit", "-q", "-m", subject)
}

// dirtyMain leaves uncommitted modifications in the worktree the driver runs
// in: the fake's clean flag on one arm, a real modified tracked file on the
// other.
func (d *confDriver) dirtyMain() {
	d.t.Helper()
	if d.fake != nil {
		d.fake.clean = false
		return
	}
	confWrite(d.t, "base.txt", "dirtied\n")
}

// dirtyWorktree marks branch's linked worktree dirty on both arms.
func (d *confDriver) dirtyWorktree(branch, wtPath string) {
	d.t.Helper()
	if d.fake != nil {
		d.fake.markWorktreeDirty(branch)
		return
	}
	confWrite(d.t, filepath.Join(wtPath, "base.txt"), "dirtied\n")
}

// addWorktree gives branch a linked worktree at wtPath (relative on the fake
// arm, absolute inside the temp repo on the shell arm — callers normalize via
// d.wtPath).
func (d *confDriver) addWorktree(branch string) string {
	d.t.Helper()
	if d.fake != nil {
		d.fake.addWorktree("/wt/"+branch, branch)
		return "/wt/" + branch
	}
	p := filepath.Join(d.dir, "wt-"+branch)
	confGit(d.t, "worktree", "add", "-q", p, branch)
	return p
}

func (d *confDriver) tip(name string) string {
	d.t.Helper()
	sha, err := d.g.RevParse(name)
	if err != nil {
		d.t.Fatalf("RevParse(%q): %v", name, err)
	}
	return sha
}

// branchNames is the sorted local-branch name set — the relational answer to
// "which refs exist" that compares across SHA representations.
func (d *confDriver) branchNames() []string {
	d.t.Helper()
	tips, err := d.g.Tips()
	if err != nil {
		d.t.Fatalf("Tips: %v", err)
	}
	names := make([]string, 0, len(tips))
	for name := range tips {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func equalNames(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestConformanceCreateBranchShowsUpInTips(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		if names := d.branchNames(); !equalNames(names, "main") {
			d.t.Fatalf("fresh repo branches = %v, want [main]", names)
		}
		if err := d.g.CreateBranch("a"); err != nil {
			d.t.Fatalf("CreateBranch: %v", err)
		}
		if names := d.branchNames(); !equalNames(names, "a", "main") {
			d.t.Fatalf("branches after create = %v, want [a main]", names)
		}
		if d.tip("a") != d.tip("main") {
			d.t.Fatal("a must share main's tip at creation")
		}
		// CreateBranch is checkout -b: the new branch becomes HEAD.
		if head, err := d.g.CurrentBranch(); err != nil || head != "a" {
			d.t.Fatalf("CurrentBranch = (%q, %v) after create, want a", head, err)
		}
	})
}

func TestConformanceUpdateRefsIsAllOrNothing(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		if err := d.g.CreateBranch("a"); err != nil {
			d.t.Fatalf("CreateBranch: %v", err)
		}
		if err := d.g.Checkout("main"); err != nil { // CreateBranch is checkout -b
			d.t.Fatalf("checkout main: %v", err)
		}
		d.commit("m2") // advance main so 'a' visibly lags
		aTip, mainTip := d.tip("a"), d.tip("main")
		// One batch member cannot resolve ("missing" is not an object name on
		// either impl) — the port contract says NO ref may move.
		err := d.g.UpdateRefs(map[string]string{
			"refs/heads/a": mainTip,
			"refs/heads/b": "missing",
		})
		if err == nil {
			d.t.Fatal("UpdateRefs with an unresolvable member must fail")
		}
		if got := d.tip("a"); got != aTip {
			d.t.Fatalf("a's tip moved in a failed batch: %q -> %q", aTip, got)
		}
		if d.g.BranchExists("b") {
			d.t.Fatal("the failed batch still created b")
		}
	})
}

func TestConformanceUpdateRefsCasHonorsExpectations(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		if err := d.g.CreateBranch("a"); err != nil {
			d.t.Fatalf("CreateBranch: %v", err)
		}
		if err := d.g.Checkout("main"); err != nil { // CreateBranch is checkout -b
			d.t.Fatalf("checkout main: %v", err)
		}
		d.commit("m2")
		aTip, mainTip := d.tip("a"), d.tip("main")
		// Wrong expected-old: the whole batch refuses and nothing moves.
		err := d.g.UpdateRefsCas(map[string]git.RefUpdate{
			"refs/heads/a": {New: mainTip, Old: mainTip},
		})
		if err == nil {
			d.t.Fatal("CAS with a wrong expected-old must fail")
		}
		if got := d.tip("a"); got != aTip {
			d.t.Fatalf("a's tip moved in a refused CAS: %q -> %q", aTip, got)
		}
		// Right expected-old applies.
		if err := d.g.UpdateRefsCas(map[string]git.RefUpdate{
			"refs/heads/a": {New: mainTip, Old: aTip},
		}); err != nil {
			d.t.Fatalf("CAS with the right expected-old: %v", err)
		}
		if d.tip("a") != mainTip {
			d.t.Fatal("a was not CAS'd to main's tip")
		}
		// Zero-old means must-not-exist: it creates c, then refuses to touch it.
		if err := d.g.UpdateRefsCas(map[string]git.RefUpdate{
			"refs/heads/c": {New: mainTip, Old: zeroSHA},
		}); err != nil {
			d.t.Fatalf("CAS create-on-absent: %v", err)
		}
		if err := d.g.UpdateRefsCas(map[string]git.RefUpdate{
			"refs/heads/c": {New: aTip, Old: zeroSHA},
		}); err == nil {
			d.t.Fatal("zero-old CAS must refuse an existing ref")
		}
	})
}

func TestConformanceDeleteBranchesPartialSuccess(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		if err := d.g.CreateBranch("a"); err != nil {
			d.t.Fatalf("CreateBranch a: %v", err)
		}
		if err := d.g.CreateBranch("b"); err != nil {
			d.t.Fatalf("CreateBranch b: %v", err)
		}
		if err := d.g.Checkout("main"); err != nil { // each create moved HEAD
			d.t.Fatalf("checkout main: %v", err)
		}
		// The checked-out branch cannot be deleted, but the batch still
		// applies to the others — the error reports the refusal, not a veto.
		err := d.g.DeleteBranches([]string{"a", "main", "b"}, true)
		if err == nil {
			d.t.Fatal("deleting the current branch must error")
		}
		if names := d.branchNames(); !equalNames(names, "main") {
			d.t.Fatalf("branches after partial delete = %v, want [main]", names)
		}
		if head, err := d.g.CurrentBranch(); err != nil || head != "main" {
			d.t.Fatalf("CurrentBranch = (%q, %v), want main", head, err)
		}
	})
}

func TestConformanceRebaseOntoMovesTip(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		initTip := d.tip("main")
		if err := d.g.CreateBranch("a"); err != nil {
			d.t.Fatalf("CreateBranch: %v", err)
		}
		if err := d.g.Checkout("a"); err != nil {
			d.t.Fatalf("checkout a: %v", err)
		}
		d.commit("a1")
		aTip := d.tip("a")
		if err := d.g.Checkout("main"); err != nil {
			d.t.Fatalf("checkout main: %v", err)
		}
		d.commit("m2")
		if err := d.g.RebaseOnto("main", initTip, "a"); err != nil {
			d.t.Fatalf("RebaseOnto: %v", err)
		}
		if d.tip("a") == aTip {
			d.t.Fatal("rebase did not move a's tip")
		}
		if anc, err := d.g.IsAncestor("main", "a"); err != nil || !anc {
			d.t.Fatalf("IsAncestor(main, a) = (%v, %v) after rebase, want true", anc, err)
		}
		if head, err := d.g.CurrentBranch(); err != nil || head != "a" {
			d.t.Fatalf("CurrentBranch = (%q, %v), want the rebased branch a", head, err)
		}
	})
}

func TestConformanceRebaseConflictPauseAndAbort(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		initTip := d.tip("main")
		if err := d.g.CreateBranch("a"); err != nil {
			d.t.Fatalf("CreateBranch: %v", err)
		}
		if err := d.g.Checkout("a"); err != nil {
			d.t.Fatalf("checkout a: %v", err)
		}
		if d.fake != nil {
			d.fake.conflictOn("a")
			d.fake.commit("a1")
		} else {
			confWrite(d.t, "base.txt", "a side\n")
			confGit(d.t, "add", "-A")
			confGit(d.t, "commit", "-q", "-m", "a1")
		}
		aTip := d.tip("a")
		if err := d.g.Checkout("main"); err != nil {
			d.t.Fatalf("checkout main: %v", err)
		}
		if d.fake != nil {
			d.fake.commit("m2")
		} else {
			confWrite(d.t, "base.txt", "main side\n")
			confGit(d.t, "add", "-A")
			confGit(d.t, "commit", "-q", "-m", "m2")
		}
		if err := d.g.RebaseOnto("main", initTip, "a"); err == nil {
			d.t.Fatal("conflicting rebase must error")
		}
		if in, err := d.g.RebaseInProgress(); err != nil || !in {
			d.t.Fatalf("RebaseInProgress = (%v, %v), want true", in, err)
		}
		if name, err := d.g.RebaseHeadName(); err != nil || name != "a" {
			d.t.Fatalf("RebaseHeadName = (%q, %v), want a", name, err)
		}
		if onto, err := d.g.RebaseOntoSHA(); err != nil || onto != d.tip("main") {
			d.t.Fatalf("RebaseOntoSHA = (%q, %v), want main's tip", onto, err)
		}
		if err := d.g.RebaseAbort(); err != nil {
			d.t.Fatalf("RebaseAbort: %v", err)
		}
		if in, err := d.g.RebaseInProgress(); err != nil || in {
			d.t.Fatalf("RebaseInProgress after abort = (%v, %v), want false", in, err)
		}
		if got := d.tip("a"); got != aTip {
			d.t.Fatalf("abort did not restore a's tip: %q -> %q", aTip, got)
		}
	})
}

func TestConformanceIsAncestor(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		if err := d.g.CreateBranch("a"); err != nil {
			d.t.Fatalf("CreateBranch: %v", err)
		}
		if err := d.g.Checkout("a"); err != nil {
			d.t.Fatalf("checkout a: %v", err)
		}
		d.commit("a1")
		if anc, err := d.g.IsAncestor("main", "a"); err != nil || !anc {
			d.t.Fatalf("IsAncestor(main, a) = (%v, %v), want true", anc, err)
		}
		if anc, err := d.g.IsAncestor("a", "main"); err != nil || anc {
			d.t.Fatalf("IsAncestor(a, main) = (%v, %v), want false", anc, err)
		}
	})
}

func TestConformanceWorktreesSingleTreeShape(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		wts, err := d.g.Worktrees()
		if err != nil {
			d.t.Fatalf("Worktrees: %v", err)
		}
		if len(wts) != 1 {
			d.t.Fatalf("Worktrees len = %d, want the single main worktree", len(wts))
		}
		wt := wts[0]
		if wt.Branch != "main" || wt.Detached || wt.Bare {
			d.t.Fatalf("main worktree = %+v, want Branch=main attached non-bare", wt)
		}
	})
}

func TestConformanceIsClean(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		if clean, err := d.g.IsClean(); err != nil || !clean {
			d.t.Fatalf("IsClean on a fresh repo = (%v, %v), want true", clean, err)
		}
		d.dirtyMain()
		if clean, err := d.g.IsClean(); err != nil || clean {
			d.t.Fatalf("IsClean on a dirtied tree = (%v, %v), want false", clean, err)
		}
	})
}

func TestConformanceIsCleanInLinkedWorktree(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		if err := d.g.CreateBranch("a"); err != nil {
			d.t.Fatalf("CreateBranch: %v", err)
		}
		if err := d.g.Checkout("main"); err != nil { // git refuses a worktree for a checked-out branch
			d.t.Fatalf("checkout main: %v", err)
		}
		wtPath := d.addWorktree("a")
		if clean, err := d.g.IsCleanIn(wtPath); err != nil || !clean {
			d.t.Fatalf("IsCleanIn on a fresh worktree = (%v, %v), want true", clean, err)
		}
		d.dirtyWorktree("a", wtPath)
		if clean, err := d.g.IsCleanIn(wtPath); err != nil || clean {
			d.t.Fatalf("IsCleanIn on a dirtied worktree = (%v, %v), want false", clean, err)
		}
	})
}

// IsCleanIn must answer for the caller's own worktree too — "" and the repo
// root mean "here", so a dirty main tree cannot read clean just because it is
// not a linked worktree.
func TestConformanceIsCleanInMainWorktree(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		mainDir := "."
		if d.fake == nil {
			root, err := d.g.RepoRoot()
			if err != nil {
				d.t.Fatalf("RepoRoot: %v", err)
			}
			mainDir = root
		}
		if clean, err := d.g.IsCleanIn(mainDir); err != nil || !clean {
			d.t.Fatalf("IsCleanIn(%q) on a fresh repo = (%v, %v), want true", mainDir, clean, err)
		}
		d.dirtyMain()
		if clean, err := d.g.IsCleanIn(mainDir); err != nil || clean {
			d.t.Fatalf("IsCleanIn(%q) on a dirtied tree = (%v, %v), want false", mainDir, clean, err)
		}
	})
}

func TestConformanceCommitSubjects(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		initTip := d.tip("main")
		if err := d.g.CreateBranch("a"); err != nil {
			d.t.Fatalf("CreateBranch: %v", err)
		}
		if err := d.g.Checkout("a"); err != nil {
			d.t.Fatalf("checkout a: %v", err)
		}
		d.commit("a1")
		d.commit("a2")
		subs, err := d.g.CommitSubjects(initTip, "a")
		if err != nil {
			d.t.Fatalf("CommitSubjects: %v", err)
		}
		if len(subs) != 2 || subs[0] != "a2" || subs[1] != "a1" {
			d.t.Fatalf("CommitSubjects = %v, want [a2 a1] tip-first", subs)
		}
	})
}
