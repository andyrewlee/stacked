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
	"reflect"
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
	d.dirtyFile("base.txt", "dirtied\n")
}

// dirtyFile marks the worktree dirty by writing distinct content — callers
// needing a second, tree-altering dirty pass a different name/content so the
// staged tree differs from HEAD (an identical write can hash to the same
// commit inside git's one-second timestamp resolution).
func (d *confDriver) dirtyFile(name, content string) {
	d.t.Helper()
	if d.fake != nil {
		d.fake.clean = false
		return
	}
	confWrite(d.t, name, content)
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

// confGitIn runs git inside dir with the shell arm's cwd-independent form. It
// returns output and error without failing the test — used for probes that
// are EXPECTED to fail (a paused rebase, a bogus -C dir).
func confGitIn(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// dirty diverges branch from main on the shared base.txt: branch commits
// "branch side" while main commits "main side", so replaying branch onto main
// conflicts on both arms (the fake needs conflictOn armed by the caller; the
// shell hits a real content conflict).
func (d *confDriver) diverge(branch string) {
	d.t.Helper()
	if err := d.g.CreateBranch(branch); err != nil {
		d.t.Fatalf("CreateBranch %q: %v", branch, err)
	}
	if err := d.g.Checkout(branch); err != nil {
		d.t.Fatalf("checkout %q: %v", branch, err)
	}
	if d.fake != nil {
		d.fake.commit(branch + "1")
	} else {
		confWrite(d.t, "base.txt", branch+" side\n")
		confGit(d.t, "add", "-A")
		confGit(d.t, "commit", "-q", "-m", branch+"1")
	}
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
}

// Worktree ownership is a real git invariant the fake must model: checkout,
// force-update, and rebase all refuse a branch checked out in another
// worktree, and an in-worktree rebase refuses a branch owned by a DIFFERENT
// worktree.
func TestConformanceOwnedBranchRefusals(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		if err := d.g.CreateBranch("a"); err != nil {
			d.t.Fatalf("CreateBranch a: %v", err)
		}
		if err := d.g.CreateBranch("b"); err != nil {
			d.t.Fatalf("CreateBranch b: %v", err)
		}
		// CreateBranch is checkout -b: park HEAD back on main so the worktree
		// adds can own a and b.
		if err := d.g.Checkout("main"); err != nil {
			d.t.Fatalf("checkout main: %v", err)
		}
		wtA := d.addWorktree("a")
		wtB := d.addWorktree("b")

		if err := d.g.Checkout("a"); err == nil {
			d.t.Fatal("Checkout of a worktree-owned branch succeeded")
		}
		if err := d.g.ForceBranch("a", "main"); err == nil {
			d.t.Fatal("ForceBranch of a worktree-owned branch succeeded")
		}
		if err := d.g.RebaseOnto("main", "main", "a"); err == nil {
			d.t.Fatal("RebaseOnto of a worktree-owned branch succeeded")
		}
		if err := d.g.RebaseOntoIn(wtB, "main", "main", "a"); err == nil {
			d.t.Fatal("RebaseOntoIn rebased a from a worktree that does not own it")
		}

		if err := d.g.WorktreeRemove(wtA, false); err != nil {
			d.t.Fatalf("WorktreeRemove: %v", err)
		}
		if err := d.g.Checkout("a"); err != nil {
			d.t.Fatalf("Checkout after the owner worktree was removed: %v", err)
		}
	})
}

// RebaseOntoIn rebases a worktree-owned branch inside its OWN worktree: the
// branch tip moves, the new base is incorporated, and the main worktree's
// HEAD never moves.
func TestConformanceRebaseOntoInOwnerWorktree(t *testing.T) {
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
		wtPath := d.addWorktree("a")

		if err := d.g.RebaseOntoIn(wtPath, "main", initTip, "a"); err != nil {
			d.t.Fatalf("RebaseOntoIn by the owner: %v", err)
		}
		if got := d.tip("a"); got == aTip {
			d.t.Fatal("RebaseOntoIn did not move a's tip")
		}
		if anc, err := d.g.IsAncestor("main", "a"); err != nil || !anc {
			d.t.Fatalf("IsAncestor(main, a) = (%v, %v) after in-worktree rebase, want true", anc, err)
		}
		if head, err := d.g.CurrentBranch(); err != nil || head != "main" {
			d.t.Fatalf("CurrentBranch = (%q, %v); the owner's rebase must not move the main HEAD", head, err)
		}
		if in, err := d.g.RebaseInProgressIn(wtPath); err != nil || in {
			d.t.Fatalf("RebaseInProgressIn after a clean rebase = (%v, %v), want false", in, err)
		}
	})
}

// A paused rebase lives in the worktree's own git dir: the per-worktree
// probes report it by path, and RebaseAbortIn clears exactly that dir.
func TestConformanceRebaseProbesInPausedWorktree(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		d.diverge("a")
		var wtPath string
		if d.fake != nil {
			wtPath = "/wt/a"
			d.fake.addPausedWorktree(wtPath, "a")
		} else {
			wtPath = d.addWorktree("a")
			if _, err := confGitIn(d.t, wtPath, "rebase", "main"); err == nil {
				d.t.Fatal("the seeded worktree rebase did not pause on the conflict")
			}
		}
		if in, err := d.g.RebaseInProgressIn(wtPath); err != nil || !in {
			d.t.Fatalf("RebaseInProgressIn(paused wt) = (%v, %v), want true", in, err)
		}
		if name, err := d.g.RebaseHeadNameIn(wtPath); err != nil || name != "a" {
			d.t.Fatalf("RebaseHeadNameIn = (%q, %v), want a", name, err)
		}
		if err := d.g.RebaseAbortIn(wtPath); err != nil {
			d.t.Fatalf("RebaseAbortIn: %v", err)
		}
		if in, err := d.g.RebaseInProgressIn(wtPath); err != nil || in {
			d.t.Fatalf("RebaseInProgressIn after abort = (%v, %v), want false", in, err)
		}
		if name, err := d.g.RebaseHeadNameIn(wtPath); err != nil || name != "" {
			d.t.Fatalf("RebaseHeadNameIn after abort = (%q, %v), want empty", name, err)
		}
	})
}

// `git -C <bogus>` fails — every *In probe must surface that error rather
// than answer against default state.
func TestConformanceWorktreeProbesOnUnknownDir(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		bogus := "no-such-worktree-dir"
		calls := []struct {
			name string
			fn   func() error
		}{
			{"RebaseInProgressIn", func() error { _, err := d.g.RebaseInProgressIn(bogus); return err }},
			{"RebaseHeadNameIn", func() error { _, err := d.g.RebaseHeadNameIn(bogus); return err }},
			{"RebaseAbortIn", func() error { return d.g.RebaseAbortIn(bogus) }},
			{"ResetHardIn", func() error { return d.g.ResetHardIn(bogus, "HEAD") }},
			{"RebaseOntoIn", func() error { return d.g.RebaseOntoIn(bogus, "main", "main", "main") }},
			{"WorktreeRemove", func() error { return d.g.WorktreeRemove(bogus, false) }},
			{"IsCleanIn", func() error { _, err := d.g.IsCleanIn(bogus); return err }},
		}
		for _, c := range calls {
			if err := c.fn(); err == nil {
				d.t.Errorf("%s on a bogus worktree dir succeeded", c.name)
			}
		}
	})
}

// WorktreeRemove refuses a dirty worktree without force and deregisters a
// clean one; ResetHardIn restores a dirty worktree to clean.
func TestConformanceWorktreeRemoveAndResetHardIn(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		if err := d.g.CreateBranch("a"); err != nil {
			d.t.Fatalf("CreateBranch: %v", err)
		}
		if err := d.g.Checkout("main"); err != nil {
			d.t.Fatalf("checkout main: %v", err)
		}
		wtPath := d.addWorktree("a")
		d.dirtyWorktree("a", wtPath)
		if clean, err := d.g.IsCleanIn(wtPath); err != nil || clean {
			d.t.Fatalf("IsCleanIn(dirty wt) = (%v, %v), want false", clean, err)
		}
		if err := d.g.WorktreeRemove(wtPath, false); err == nil {
			d.t.Fatal("WorktreeRemove removed a dirty worktree without force")
		}
		if err := d.g.ResetHardIn(wtPath, "HEAD"); err != nil {
			d.t.Fatalf("ResetHardIn: %v", err)
		}
		if clean, err := d.g.IsCleanIn(wtPath); err != nil || !clean {
			d.t.Fatalf("IsCleanIn after reset --hard = (%v, %v), want true", clean, err)
		}
		if err := d.g.WorktreeRemove(wtPath, false); err != nil {
			d.t.Fatalf("WorktreeRemove on a clean worktree: %v", err)
		}
		wts, err := d.g.Worktrees()
		if err != nil {
			d.t.Fatalf("Worktrees: %v", err)
		}
		if len(wts) != 1 {
			d.t.Fatalf("Worktrees len = %d after removal, want the main worktree only", len(wts))
		}
	})
}

// The branch-mutation plumbing surface: CreateBranchAt, UpdateRef,
// RenameBranch, MergeBase, CommitRange, TipsFor.
func TestConformanceRefOps(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		initTip := d.tip("main")
		if err := d.g.CreateBranchAt("x", initTip); err != nil {
			d.t.Fatalf("CreateBranchAt: %v", err)
		}
		if got := d.tip("x"); got != initTip {
			d.t.Fatalf("CreateBranchAt put x at %s, want the pinned ref %s", got, initTip)
		}
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

		if err := d.g.UpdateRef("refs/heads/x", aTip); err != nil {
			d.t.Fatalf("UpdateRef: %v", err)
		}
		if got := d.tip("x"); got != aTip {
			d.t.Fatalf("UpdateRef moved x to %s, want %s", got, aTip)
		}
		if err := d.g.RenameBranch("x", "y"); err != nil {
			d.t.Fatalf("RenameBranch: %v", err)
		}
		if !equalNames(d.branchNames(), "a", "main", "y") {
			d.t.Fatalf("branches = %v after rename, want [a main y]", d.branchNames())
		}

		mb, err := d.g.MergeBase("main", "a")
		if err != nil || mb != initTip {
			d.t.Fatalf("MergeBase(main, a) = (%s, %v), want the fork point %s", mb, err, initTip)
		}
		set, err := d.g.CommitRange(initTip, "a")
		if err != nil {
			d.t.Fatalf("CommitRange: %v", err)
		}
		if len(set) != 1 || !set[aTip] {
			d.t.Fatalf("CommitRange(init..a) = %v, want just a's tip", set)
		}
		tips, err := d.g.TipsFor([]string{"main", "a"})
		if err != nil || len(tips) != 2 || tips["a"] != aTip {
			d.t.Fatalf("TipsFor = (%v, %v), want both branch tips", tips, err)
		}
		if !d.g.BranchExists("a") {
			d.t.Fatal("BranchExists(a) = false, want true")
		}
		if d.g.BranchExists("ghost") {
			d.t.Fatal("BranchExists(ghost) = true, want false")
		}
		if root, err := d.g.RepoRoot(); err != nil {
			d.t.Fatalf("RepoRoot: %v", err)
		} else if d.fake == nil {
			// git resolves symlinks (t.TempDir's /var is a symlink to
			// /private/var on macOS) — compare canonical paths.
			gotRoot, _ := filepath.EvalSymlinks(root)
			wantRoot, _ := filepath.EvalSymlinks(d.dir)
			if gotRoot != wantRoot {
				d.t.Fatalf("RepoRoot = %q, want the repo dir %q", gotRoot, wantRoot)
			}
		}
	})
}

// The index/commit plumbing: Add stages, Commit lands it, AmendNoEdit and
// AmendMessage rewrite the tip, ResetSoft rewinds leaving the index staged.
func TestConformanceCommitAmendResetSoft(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		initTip := d.tip("main")
		d.dirtyMain()
		if dirty, err := d.g.HasUnstagedChanges(); err != nil || !dirty {
			d.t.Fatalf("HasUnstagedChanges on a dirtied tree = (%v, %v), want true", dirty, err)
		}
		if err := d.g.Add("base.txt"); err != nil {
			d.t.Fatalf("Add: %v", err)
		}
		if staged, err := d.g.HasStagedChanges(); err != nil || !staged {
			d.t.Fatalf("HasStagedChanges after add = (%v, %v), want true", staged, err)
		}
		if err := d.g.Commit("c1", false); err != nil {
			d.t.Fatalf("Commit: %v", err)
		}
		if got := d.tip("main"); got == initTip {
			d.t.Fatal("Commit did not move the tip")
		}
		if subs, err := d.g.CommitSubjects(initTip, "main"); err != nil || len(subs) != 1 || subs[0] != "c1" {
			d.t.Fatalf("CommitSubjects = (%v, %v), want [c1]", subs, err)
		}
		if err := d.g.AmendMessage("c1-amended", false); err != nil {
			d.t.Fatalf("AmendMessage: %v", err)
		}
		if subs, err := d.g.CommitSubjects(initTip, "main"); err != nil || len(subs) != 1 || subs[0] != "c1-amended" {
			d.t.Fatalf("CommitSubjects after amend = (%v, %v), want [c1-amended]", subs, err)
		}
		d.dirtyFile("extra.txt", "extra\n")
		if err := d.g.Add("extra.txt"); err != nil {
			d.t.Fatalf("Add: %v", err)
		}
		before := d.tip("main")
		if err := d.g.AmendNoEdit(false); err != nil {
			d.t.Fatalf("AmendNoEdit: %v", err)
		}
		if d.tip("main") == before {
			d.t.Fatal("AmendNoEdit did not move the tip")
		}
		if err := d.g.ResetSoft(initTip); err != nil {
			d.t.Fatalf("ResetSoft: %v", err)
		}
		if got := d.tip("main"); got != initTip {
			d.t.Fatalf("ResetSoft left tip at %s, want %s", got, initTip)
		}
		if staged, err := d.g.HasStagedChanges(); err != nil || !staged {
			d.t.Fatalf("HasStagedChanges after reset --soft = (%v, %v), want true (index kept)", staged, err)
		}
	})
}

// MergedInto reports ancestor tips; ChangesContainedIn reports the
// squash-merge case where content landed without the commits.
func TestConformanceMergedAndContained(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		if err := d.g.CreateBranch("b"); err != nil {
			d.t.Fatalf("CreateBranch b: %v", err)
		}
		// a diverges from main: one commit on a, then a DIFFERENT commit with
		// identical content lands on main (the squash-merge shape).
		if err := d.g.CreateBranch("a"); err != nil {
			d.t.Fatalf("CreateBranch a: %v", err)
		}
		if err := d.g.Checkout("a"); err != nil {
			d.t.Fatalf("checkout a: %v", err)
		}
		if d.fake != nil {
			d.fake.commit("a1")
		} else {
			confWrite(d.t, "shared.txt", "payload\n")
			confGit(d.t, "add", "-A")
			confGit(d.t, "commit", "-q", "-m", "a1")
		}
		if err := d.g.Checkout("main"); err != nil {
			d.t.Fatalf("checkout main: %v", err)
		}
		if d.fake != nil {
			d.fake.squashInto(d.t, "main", "a")
		} else {
			confWrite(d.t, "shared.txt", "payload\n")
			confGit(d.t, "add", "-A")
			confGit(d.t, "commit", "-q", "-m", "squash a")
		}

		merged, err := d.g.MergedInto("main")
		if err != nil {
			d.t.Fatalf("MergedInto: %v", err)
		}
		if !merged["b"] {
			d.t.Fatal("MergedInto(main) missed b, a branch at main's tip")
		}
		if merged["a"] {
			d.t.Fatal("MergedInto(main) claims the squash-merged a — its tip is no ancestor")
		}
		contained, err := d.g.ChangesContainedIn("main", "a")
		if err != nil || !contained {
			d.t.Fatalf("ChangesContainedIn(main, a) = (%v, %v), want true (squash-merged)", contained, err)
		}
		if anc, err := d.g.IsAncestor("a", "main"); err != nil || anc {
			d.t.Fatalf("IsAncestor(a, main) = (%v, %v), want false", anc, err)
		}
	})
}

// A paused rebase in the main worktree exposes HeadName/OntoSHA until
// RebaseContinue resolves it; RebaseAbort restores the pre-rebase tip.
func TestConformanceRebaseContinueResolves(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		initTip := d.tip("main")
		d.diverge("a")
		aTip := d.tip("a")
		if d.fake != nil {
			d.fake.conflictOn("a")
		}
		if err := d.g.RebaseOnto("main", initTip, "a"); err == nil {
			d.t.Fatal("RebaseOnto over a conflicting commit succeeded")
		}
		if got := d.tip("a"); got != aTip {
			d.t.Fatalf("a's tip moved to %s while the rebase paused", got)
		}
		if in, err := d.g.RebaseInProgress(); err != nil || !in {
			d.t.Fatalf("RebaseInProgress = (%v, %v), want true", in, err)
		}
		if name, err := d.g.RebaseHeadName(); err != nil || name != "a" {
			d.t.Fatalf("RebaseHeadName = (%q, %v), want a", name, err)
		}
		if onto, err := d.g.RebaseOntoSHA(); err != nil || onto != d.tip("main") {
			d.t.Fatalf("RebaseOntoSHA = (%q, %v), want main's tip %q", onto, err, d.tip("main"))
		}

		if d.fake == nil {
			confWrite(d.t, "base.txt", "resolved\n")
			confGit(d.t, "add", "base.txt")
		}
		if err := d.g.RebaseContinue(); err != nil {
			d.t.Fatalf("RebaseContinue: %v", err)
		}
		if in, err := d.g.RebaseInProgress(); err != nil || in {
			d.t.Fatalf("RebaseInProgress after continue = (%v, %v), want false", in, err)
		}
		if anc, err := d.g.IsAncestor("main", "a"); err != nil || !anc {
			d.t.Fatalf("IsAncestor(main, a) = (%v, %v) after continue, want true", anc, err)
		}
	})
}

func TestConformanceCheckoutDetach(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		initTip := d.tip("main")
		if err := d.g.CheckoutDetach(initTip); err != nil {
			d.t.Fatalf("CheckoutDetach: %v", err)
		}
		wts, err := d.g.Worktrees()
		if err != nil {
			d.t.Fatalf("Worktrees: %v", err)
		}
		if len(wts) != 1 || !wts[0].Detached {
			d.t.Fatalf("Worktrees = %+v, want the main worktree detached", wts)
		}
		if err := d.g.Checkout("main"); err != nil {
			d.t.Fatalf("re-attach checkout main: %v", err)
		}
	})
}

// Absorb's read side and temp-index write path: staged hunks classify,
// blame attributes, per-target patches assemble, and the amend moves only
// the target tip.
func TestConformanceAbsorbSurface(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		if err := d.g.CreateBranch("a"); err != nil {
			d.t.Fatalf("CreateBranch: %v", err)
		}
		if err := d.g.Checkout("a"); err != nil {
			d.t.Fatalf("checkout a: %v", err)
		}
		d.commit("a1")
		aTip := d.tip("a")

		var hunks []git.Hunk
		var unsupported []git.UnsupportedRecord
		if d.fake != nil {
			d.fake.stagedHunks = []git.Hunk{{File: "base.txt", OldStart: 1, OldN: 1, NewStart: 1, NewN: 1}}
			d.fake.stagedPatch = []byte("fake patch")
			d.fake.blame = map[string]map[int]git.BlameLine{
				"base.txt": {1: blameID(aTip, 1, "base.txt")},
			}
		} else {
			confWrite(d.t, "base.txt", "dirtied\n")
			confGit(d.t, "add", "base.txt")
		}
		var err error
		hunks, unsupported, err = d.g.DiffCachedHunks()
		if err != nil {
			d.t.Fatalf("DiffCachedHunks: %v", err)
		}
		if len(hunks) == 0 || hunks[0].File != "base.txt" {
			d.t.Fatalf("DiffCachedHunks = (%v, %v), want a base.txt hunk", hunks, unsupported)
		}
		if len(unsupported) != 0 {
			d.t.Fatalf("DiffCachedHunks unsupported = %v, want none for a plain text change", unsupported)
		}
		blame, err := d.g.BlamePorcelain("base.txt", "HEAD")
		if err != nil || len(blame) == 0 {
			d.t.Fatalf("BlamePorcelain = (%v, %v), want per-line provenance", blame, err)
		}
		for ln, bl := range blame {
			if bl.Commit == "" || bl.OriginalLine == 0 || bl.Path == "" {
				d.t.Fatalf("blame line %d = %+v, want complete provenance", ln, bl)
			}
		}
		patches, err := d.g.DiffCachedPatchesFor(map[string][]git.Hunk{"target": hunks})
		if err != nil || len(patches["target"]) == 0 {
			d.t.Fatalf("DiffCachedPatchesFor = (%v, %v), want a non-empty per-target patch", patches, err)
		}
	})
}

// AmendTipWithPatch amends a branch's tip through a temporary index — no
// checkout, no worktree touch.
func TestConformanceAmendTipWithPatch(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		if err := d.g.CreateBranch("a"); err != nil {
			d.t.Fatalf("CreateBranch: %v", err)
		}
		if err := d.g.Checkout("a"); err != nil {
			d.t.Fatalf("checkout a: %v", err)
		}
		d.commit("a1")
		aTip := d.tip("a")

		var patch []byte
		if d.fake != nil {
			patch = []byte("content not modeled")
		} else {
			confWrite(d.t, "base.txt", "base\nextra line\n")
			patch = []byte(confGit(d.t, "diff"))
			confGit(d.t, "checkout", "--", "base.txt")
		}
		newTip, err := d.g.AmendTipWithPatch("a", patch)
		if err != nil {
			d.t.Fatalf("AmendTipWithPatch: %v", err)
		}
		if newTip == "" || newTip == aTip {
			d.t.Fatalf("AmendTipWithPatch returned %q, want a new tip SHA", newTip)
		}
		if got := d.tip("a"); got != newTip {
			d.t.Fatalf("a's tip = %s, want the amended %s", got, newTip)
		}
		if head, err := d.g.CurrentBranch(); err != nil || head != "a" {
			d.t.Fatalf("CurrentBranch = (%q, %v); the temp-index amend must not move HEAD", head, err)
		}
	})
}

// BuildAmendedTip + LandAmendedTip split the amend across the mutation
// boundary: the build computes a new tip and reports the tip it read, moving
// nothing; the land is a compare-and-swap on that read — a branch that moved
// in between must refuse rather than be clobbered.
func TestConformanceAmendTipBuildLand(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		if err := d.g.CreateBranch("a"); err != nil {
			d.t.Fatalf("CreateBranch: %v", err)
		}
		if err := d.g.Checkout("a"); err != nil {
			d.t.Fatalf("checkout a: %v", err)
		}
		d.commit("a1")
		aTip := d.tip("a")

		var patch []byte
		if d.fake != nil {
			patch = []byte("content not modeled")
		} else {
			confWrite(d.t, "base.txt", "base\nextra line\n")
			patch = []byte(confGit(d.t, "diff"))
			confGit(d.t, "checkout", "--", "base.txt")
		}

		newTip, oldTip, err := d.g.BuildAmendedTip("a", patch)
		if err != nil {
			d.t.Fatalf("BuildAmendedTip: %v", err)
		}
		if oldTip != aTip {
			d.t.Fatalf("oldTip = %q, want the tip the build read %q", oldTip, aTip)
		}
		if newTip == "" || newTip == aTip {
			d.t.Fatalf("newTip = %q, want a fresh SHA", newTip)
		}
		if got := d.tip("a"); got != aTip {
			d.t.Fatalf("a's tip = %s after build — the build must not move a ref", got)
		}

		// Moving the branch between build and land fails the CAS; the live
		// ref keeps the interloper's tip.
		d.commit("a2")
		moved := d.tip("a")
		if err := d.g.LandAmendedTip("a", oldTip, newTip); err == nil {
			d.t.Fatal("land with a stale oldTip succeeded — the CAS is gone")
		}
		if got := d.tip("a"); got != moved {
			d.t.Fatalf("a's tip = %s after a refused land, want untouched %s", got, moved)
		}

		newTip2, oldTip2, err := d.g.BuildAmendedTip("a", patch)
		if err != nil {
			d.t.Fatalf("BuildAmendedTip (retry): %v", err)
		}
		if oldTip2 != moved {
			d.t.Fatalf("rebuilt oldTip = %q, want the moved tip %q", oldTip2, moved)
		}
		if err := d.g.LandAmendedTip("a", oldTip2, newTip2); err != nil {
			d.t.Fatalf("LandAmendedTip: %v", err)
		}
		if got := d.tip("a"); got != newTip2 {
			d.t.Fatalf("a's tip = %s, want the landed %s", got, newTip2)
		}
	})
}

// LooseBranchTip reads the tip straight from the loose ref file: present
// after any ref write (a branch is always loose post-write), missing in a
// packed-only layout — where the RevParse fallback still resolves.
func TestConformanceLooseBranchTip(t *testing.T) {
	runConformance(t, func(d *confDriver) {
		if err := d.g.CreateBranch("a"); err != nil {
			d.t.Fatalf("CreateBranch: %v", err)
		}
		want := d.tip("a")
		got, ok := d.g.LooseBranchTip("a")
		if !ok || got != want {
			d.t.Fatalf("LooseBranchTip = (%q, %v), want (%q, true) — a written ref is loose", got, ok, want)
		}
		if _, ok := d.g.LooseBranchTip("ghost"); ok {
			d.t.Fatal("LooseBranchTip answered a nonexistent branch")
		}
		if d.fake != nil {
			d.fake.looseOff = true
		} else {
			// --prune removes the loose files pack-refs just wrote, leaving a
			// packed-only layout (the default in modern git, but explicit).
			confGit(d.t, "pack-refs", "--all", "--prune")
		}
		if _, ok := d.g.LooseBranchTip("a"); ok {
			d.t.Fatal("LooseBranchTip answered a packed-only ref — miss must degrade to RevParse")
		}
		if got := d.tip("a"); got != want {
			d.t.Fatalf("RevParse fallback = %q, want %q", got, want)
		}
	})
}

// Remote is the second port in git.go: the fake arm is the scripted
// fakeRemote, the shell arm git.RemoteShell over a local bare remote.
func TestConformanceRemote(t *testing.T) {
	t.Run("fake", func(t *testing.T) {
		f := newFakeGit()
		r := &fakeRemote{exists: true, ff: "ff ok", git: f}
		if !r.Exists("origin") {
			t.Fatal("Exists did not honor the scripted knob")
		}
		if err := r.Fetch("origin"); err != nil || r.fetches != 1 {
			t.Fatalf("Fetch = (%v), fetches = %d", err, r.fetches)
		}
		if got, err := r.FastForward("main", "origin", "", false); err != nil || got != "ff ok" {
			t.Fatalf("FastForward = (%q, %v)", got, err)
		}
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

		bare := filepath.Join(t.TempDir(), "remote.git")
		confGit(t, "init", "-q", "--bare", bare)
		confGit(t, "--git-dir", bare, "symbolic-ref", "HEAD", "refs/heads/main")
		confGit(t, "remote", "add", "origin", bare)
		confGit(t, "push", "-q", "origin", "main")

		var rs git.RemoteShell
		if !rs.Exists("origin") {
			t.Fatal("Exists(origin) = false for a configured remote")
		}
		if rs.Exists("definitely-not-a-remote") {
			t.Fatal("Exists = true for an unknown remote")
		}
		if err := rs.Fetch("origin"); err != nil {
			t.Fatalf("Fetch: %v", err)
		}

		// Advance the remote through a second clone, then fetch it in.
		other := filepath.Join(t.TempDir(), "other")
		confGit(t, "clone", "-q", bare, other)
		confGit(t, "-C", other, "config", "user.email", "t@e.com")
		confGit(t, "-C", other, "config", "user.name", "t")
		confGit(t, "-C", other, "checkout", "-q", "main")
		if err := os.WriteFile(filepath.Join(other, "up.txt"), []byte("up\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		confGit(t, "-C", other, "add", "-A")
		confGit(t, "-C", other, "commit", "-q", "-m", "up")
		confGit(t, "-C", other, "push", "-q", "origin", "main")
		if err := rs.Fetch("origin"); err != nil {
			t.Fatalf("Fetch after remote advance: %v", err)
		}
		localBefore := confGit(t, "rev-parse", "main")
		res, err := rs.FastForward("main", "origin", "", true)
		if err != nil {
			t.Fatalf("FastForward: %v", err)
		}
		if res == "" {
			t.Fatal("FastForward returned an empty result string")
		}
		localAfter := confGit(t, "rev-parse", "main")
		if localAfter == localBefore {
			t.Fatal("FastForward did not move the local trunk")
		}
		if remote := confGit(t, "rev-parse", "origin/main"); localAfter != remote {
			t.Fatalf("local trunk = %s, want the remote tip %s", localAfter, remote)
		}

		// A diverged trunk refuses the fast-forward.
		confWrite(t, "local.txt", "local\n")
		confGit(t, "add", "-A")
		confGit(t, "commit", "-q", "-m", "local")
		confGit(t, "-C", other, "pull", "-q", "--no-rebase", "origin", "main")
		if err := os.WriteFile(filepath.Join(other, "remote.txt"), []byte("remote\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		confGit(t, "-C", other, "add", "-A")
		confGit(t, "-C", other, "commit", "-q", "-m", "remote")
		confGit(t, "-C", other, "push", "-q", "origin", "main")
		if err := rs.Fetch("origin"); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if _, err := rs.FastForward("main", "origin", "", true); err == nil {
			t.Fatal("FastForward over a diverged trunk succeeded")
		}
	})
}

// conformanceCovered names every port method a row in this file exercises;
// conformanceSkipped names the ones deliberately left out with the reason.
// A new Git/Remote method not in either map fails this test — that is the
// "new method ⇒ new row" contract, enforced mechanically.
func TestConformancePortCoverageGuard(t *testing.T) {
	covered := map[string]bool{
		"Add": true, "AmendMessage": true, "AmendNoEdit": true, "AmendTipWithPatch": true,
		"BuildAmendedTip": true, "LandAmendedTip": true,
		"BlamePorcelain": true, "BranchExists": true, "ChangesContainedIn": true,
		"Checkout": true, "CheckoutDetach": true, "Commit": true, "CommitRange": true,
		"CommitSubjects": true, "CreateBranch": true, "CreateBranchAt": true,
		"CurrentBranch": true, "DeleteBranches": true, "DiffCachedHunks": true,
		"DiffCachedPatchesFor": true, "ForceBranch": true, "HasStagedChanges": true,
		"HasUnstagedChanges": true, "IsAncestor": true, "IsClean": true,
		"IsCleanIn": true, "LooseBranchTip": true, "MergeBase": true, "MergedInto": true,
		"RebaseAbort": true, "RebaseAbortIn": true, "RebaseContinue": true,
		"RebaseHeadName": true, "RebaseHeadNameIn": true, "RebaseInProgress": true,
		"RebaseInProgressIn": true, "RebaseOnto": true, "RebaseOntoIn": true,
		"RenameBranch": true, "RepoRoot": true, "RebaseOntoSHA": true,
		"ResetHardIn": true, "ResetSoft": true, "RevParse": true, "Tips": true,
		"TipsFor": true, "UpdateRef": true, "UpdateRefs": true, "UpdateRefsCas": true,
		"WorktreeRemove": true, "Worktrees": true,
		// Remote
		"Exists": true, "FastForward": true, "Fetch": true,
	}
	skipped := map[string]string{
		"DeleteBranch": "exercised only through DeleteBranches, whose row asserts the partial-success contract both share",
	}
	inIface := map[string]bool{}
	for _, iface := range []reflect.Type{reflect.TypeOf((*Git)(nil)).Elem(), reflect.TypeOf((*Remote)(nil)).Elem()} {
		for i := 0; i < iface.NumMethod(); i++ {
			name := iface.Method(i).Name
			inIface[name] = true
			if !covered[name] {
				if _, why := skipped[name]; !why {
					t.Errorf("port method %s has no conformance row and no declared skip", name)
				}
			}
		}
	}
	for name := range covered {
		if !inIface[name] {
			t.Errorf("covered[%s] names no Git/Remote method (stale declaration — a renamed method lost its row)", name)
		}
	}
	for name := range skipped {
		if !inIface[name] {
			t.Errorf("skipped[%s] names no Git/Remote method", name)
		}
	}
}
