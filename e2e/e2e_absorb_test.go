package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAbsorbApplyJourney is the mandatory real-git proof of the absorb apply
// slice: a staged hunk owned by a mid-stack branch is committed into that
// branch's tip WITHOUT any checkout (amend in place — same parent, same
// message), the descendants are restacked onto the amended tip, the working
// tree ends clean, and one `st undo` restores every pre-absorb tip.
func TestAbsorbApplyJourney(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	// Trunk seeds the file; each branch then owns one well-separated line
	// (adjacent-line edits would be a genuine rebase conflict, not absorb's
	// concern). HEAD ends on feat-c.
	r.writeFile("shared.txt", "A0\np\nq\nB0\nr\ns\nC0\n")
	r.git("add", "shared.txt")
	r.git("commit", "-q", "-m", "seed")
	r.create("feat-a", "shared.txt", "A1\np\nq\nB0\nr\ns\nC0\n", "a")
	r.create("feat-b", "shared.txt", "A1\np\nq\nB1\nr\ns\nC0\n", "b")
	r.create("feat-c", "shared.txt", "A1\np\nq\nB1\nr\ns\nC1\n", "c")

	tipsBefore := map[string]string{}
	for _, b := range []string{"main", "feat-a", "feat-b", "feat-c"} {
		tipsBefore[b] = r.rev(b)
	}
	parentBefore := r.rev("feat-a^")

	// Stage an edit to line 1, owned by feat-a's tip.
	r.writeFile("shared.txt", "A2\np\nq\nB1\nr\ns\nC1\n")
	r.git("add", "shared.txt")

	out := r.stOK("absorb", "--json").stdout
	var res struct {
		Summary  string `json:"summary"`
		Absorbed []struct {
			Branch string `json:"branch"`
			Commit string `json:"commit"`
		} `json:"absorbed"`
		Restacked []string `json:"restacked"`
		DryRun    bool     `json:"dryRun"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode absorb json: %v\n%s", err, out)
	}
	if res.DryRun {
		t.Fatalf("applied absorb still marked dryRun: %s", out)
	}
	if len(res.Absorbed) != 1 || res.Absorbed[0].Branch != "feat-a" {
		t.Fatalf("absorbed = %+v, want one hunk into feat-a", res.Absorbed)
	}

	// feat-a was amended IN PLACE: new tip, same parent, same message, and it
	// now contains the edit.
	newATip := r.rev("feat-a")
	if newATip == tipsBefore["feat-a"] {
		t.Fatal("feat-a tip unchanged; the absorb did not land")
	}
	if res.Absorbed[0].Commit != newATip {
		t.Fatalf("absorbed commit = %s, want feat-a's new tip %s", res.Absorbed[0].Commit, newATip)
	}
	if got := r.rev("feat-a^"); got != parentBefore {
		t.Fatalf("feat-a^ = %s, want unchanged %s (amend, not a commit on top)", got, parentBefore)
	}
	if got := r.git("log", "-1", "--format=%s", "feat-a"); got != "a" {
		t.Fatalf("feat-a subject = %q, want the original preserved", got)
	}
	if got := r.git("show", "feat-a:shared.txt"); got != "A2\np\nq\nB0\nr\ns\nC0" {
		t.Fatalf("feat-a:shared.txt = %q, want the absorbed edit and feat-a's tree only", got)
	}

	// Descendants restacked onto the amended tip; the stack still reads
	// A!/B/C at the top.
	if len(res.Restacked) != 2 || res.Restacked[0] != "feat-b" || res.Restacked[1] != "feat-c" {
		t.Fatalf("restacked = %v, want [feat-b feat-c]", res.Restacked)
	}
	for _, b := range []string{"feat-b", "feat-c"} {
		if r.rev(b) == tipsBefore[b] {
			t.Fatalf("%s tip unchanged; not restacked onto the amended feat-a", b)
		}
	}
	if !r.isAncestor(newATip, r.rev("feat-c")) {
		t.Fatal("feat-c does not contain the amended feat-a")
	}
	if got := r.git("show", "feat-c:shared.txt"); got != "A2\np\nq\nB1\nr\ns\nC1" {
		t.Fatalf("feat-c:shared.txt = %q, want the full stacked content with the edit", got)
	}

	// The staged edit is now history, not index: clean tree, still on feat-c.
	if got := r.git("status", "--porcelain"); got != "" {
		t.Fatalf("status = %q, want a clean tree after absorb", got)
	}
	if got := r.currentBranch(); got != "feat-c" {
		t.Fatalf("HEAD = %q after absorb, want feat-c", got)
	}
	r.stOK("validate")

	// One undo entry reverts the amend AND the cascade.
	undoOut := r.stOK("undo").stdout
	for b, tip := range tipsBefore {
		if got := r.rev(b); got != tip {
			t.Fatalf("%s = %s after undo, want restored %s\nundo output:\n%s", b, got, tip, undoOut)
		}
	}
	r.stOK("validate")
}

// absorbConflictFixture builds the adjacency fixture: feat-a owns line 1,
// feat-b edits the ADJACENT line 2, so absorbing a line-1 edit forces a
// genuine rebase conflict when feat-b cascades onto the amended feat-a. It
// stages the conflicting edit, runs bare absorb, asserts exit code 2 with
// the amend landed and a rebase paused, and returns the pre-absorb tips.
func absorbConflictFixture(t *testing.T, r *repo) map[string]string {
	t.Helper()
	r.initStack()
	r.writeFile("shared.txt", "A0\nB0\n")
	r.git("add", "shared.txt")
	r.git("commit", "-q", "-m", "seed")
	r.create("feat-a", "shared.txt", "A1\nB0\n", "a")
	r.create("feat-b", "shared.txt", "A1\nB1\n", "b")

	tipsBefore := map[string]string{}
	for _, b := range []string{"main", "feat-a", "feat-b"} {
		tipsBefore[b] = r.rev(b)
	}
	r.writeFile("shared.txt", "A2\nB1\n")
	r.git("add", "shared.txt")

	res := r.st("absorb")
	if res.exitCode != 2 {
		t.Fatalf("absorb exit = %d, want 2 (conflict)\nstdout:\n%s\nstderr:\n%s", res.exitCode, res.stdout, res.stderr)
	}
	if r.rev("feat-a") == tipsBefore["feat-a"] {
		t.Fatal("feat-a tip unchanged; the amend should land before the cascade conflicts")
	}
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("expected a paused rebase after the absorb conflict: %v", err)
	}
	return tipsBefore
}

// TestAbsorbConflictContinueJourney proves the dangerous half of absorb: the
// amend lands, the staged copy is gone, the cascade conflicts — and
// `st continue` still reconciles the stack with the edit preserved.
func TestAbsorbConflictContinueJourney(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	absorbConflictFixture(t, r)

	// Resolve the conflict in feat-b's favor of both edits and continue.
	r.writeFile("shared.txt", "A2\nB1\n")
	r.git("add", "shared.txt")
	r.stOK("continue")

	if got := r.git("status", "--porcelain"); got != "" {
		t.Fatalf("status = %q, want a clean tree after continue", got)
	}
	if got := r.git("show", "feat-b:shared.txt"); got != "A2\nB1" {
		t.Fatalf("feat-b:shared.txt = %q, want the absorbed edit plus feat-b's line", got)
	}
	r.stOK("validate")
}

// TestAbsorbConflictAbortUndoJourney proves the other recovery: abort the
// paused cascade, then one undo restores every pre-absorb tip.
func TestAbsorbConflictAbortUndoJourney(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	tipsBefore := absorbConflictFixture(t, r)

	r.stOK("abort")
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); !os.IsNotExist(err) {
		t.Fatalf("rebase still in progress after abort: %v", err)
	}
	r.stOK("validate")

	undoOut := r.stOK("undo").stdout
	for b, tip := range tipsBefore {
		if got := r.rev(b); got != tip {
			t.Fatalf("%s = %s after undo, want restored %s\nundo output:\n%s", b, got, tip, undoOut)
		}
	}
	// Undo restores refs, never the working tree (documented); the edit
	// stays reachable in the dangling amended commit. The repo must be usable.
	r.stOK("status")
}

// TestAbsorbMultiTargetJourney proves absorb v2 end to end: one staged set
// carrying edits owned by TWO different stack branches lands as two in-place
// amends plus one cascade, the top of the stack carries both edits, the tree
// ends clean, and a single st undo restores every tip.
func TestAbsorbMultiTargetJourney(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.writeFile("shared.txt", "A0\np\nq\nB0\nr\ns\nC0\n")
	r.git("add", "shared.txt")
	r.git("commit", "-q", "-m", "seed")
	r.create("feat-a", "shared.txt", "A1\np\nq\nB0\nr\ns\nC0\n", "a")
	r.create("feat-b", "shared.txt", "A1\np\nq\nB1\nr\ns\nC0\n", "b")
	r.create("feat-c", "shared.txt", "A1\np\nq\nB1\nr\ns\nC1\n", "c")

	tipsBefore := map[string]string{}
	for _, b := range []string{"main", "feat-a", "feat-b", "feat-c"} {
		tipsBefore[b] = r.rev(b)
	}
	parentABefore := r.rev("feat-a^")

	// One staged set editing feat-a's line 1 AND feat-b's line 4.
	r.writeFile("shared.txt", "A2\np\nq\nB2\nr\ns\nC1\n")
	r.git("add", "shared.txt")

	out := r.stOK("absorb", "--json").stdout
	var res struct {
		Absorbed []struct {
			Branch string `json:"branch"`
			Commit string `json:"commit"`
		} `json:"absorbed"`
		Restacked []string `json:"restacked"`
		DryRun    bool     `json:"dryRun"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode absorb json: %v\n%s", err, out)
	}
	if res.DryRun || len(res.Absorbed) != 2 {
		t.Fatalf("result = %+v, want two applied hunks", res)
	}
	if res.Absorbed[0].Branch != "feat-a" || res.Absorbed[1].Branch != "feat-b" {
		t.Fatalf("absorbed = %+v, want feat-a then feat-b", res.Absorbed)
	}
	// Both amended in place: feat-a's parent unchanged; each reported commit
	// is the branch's live tip.
	if r.rev("feat-a") == tipsBefore["feat-a"] || r.rev("feat-b") == tipsBefore["feat-b"] {
		t.Fatal("both target tips must move")
	}
	if got := r.rev("feat-a^"); got != parentABefore {
		t.Fatalf("feat-a^ = %s, want unchanged %s", got, parentABefore)
	}
	if res.Absorbed[0].Commit != r.rev("feat-a") || res.Absorbed[1].Commit != r.rev("feat-b") {
		t.Fatalf("absorbed commits = %+v, want the LIVE post-cascade tips", res.Absorbed)
	}
	if got := r.git("show", "feat-a:shared.txt"); got != "A2\np\nq\nB0\nr\ns\nC0" {
		t.Fatalf("feat-a:shared.txt = %q, want ONLY feat-a's edit", got)
	}
	if got := r.git("show", "feat-b:shared.txt"); got != "A2\np\nq\nB2\nr\ns\nC0" {
		t.Fatalf("feat-b:shared.txt = %q, want both edits below feat-c", got)
	}
	if got := r.git("show", "feat-c:shared.txt"); got != "A2\np\nq\nB2\nr\ns\nC1" {
		t.Fatalf("feat-c:shared.txt = %q, want the full stack content", got)
	}
	if got := r.git("status", "--porcelain"); got != "" {
		t.Fatalf("status = %q, want clean", got)
	}
	r.stOK("validate")

	undoOut := r.stOK("undo").stdout
	for b, tip := range tipsBefore {
		if got := r.rev(b); got != tip {
			t.Fatalf("%s = %s after undo, want %s\n%s", b, got, tip, undoOut)
		}
	}
	r.stOK("validate")
}

// TestAbsorbMultiHunkSingleTarget pins the most common real absorb: two
// separated staged edits both owned by ONE branch land in a single amend.
func TestAbsorbMultiHunkSingleTarget(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.writeFile("shared.txt", "A0\np\nq\nr\ns\nt\nZ0\n")
	r.git("add", "shared.txt")
	r.git("commit", "-q", "-m", "seed")
	// feat-a owns lines 1 AND 7.
	r.create("feat-a", "shared.txt", "A1\np\nq\nr\ns\nt\nZ1\n", "a")

	tipBefore := r.rev("feat-a")
	r.writeFile("shared.txt", "A2\np\nq\nr\ns\nt\nZ2\n")
	r.git("add", "shared.txt")

	out := r.stOK("absorb", "--json").stdout
	var res struct {
		Absorbed []struct {
			Branch string `json:"branch"`
			Lines  string `json:"lines"`
		} `json:"absorbed"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode absorb json: %v\n%s", err, out)
	}
	if len(res.Absorbed) != 2 || res.Absorbed[0].Branch != "feat-a" || res.Absorbed[1].Branch != "feat-a" {
		t.Fatalf("absorbed = %+v, want two hunks both into feat-a", res.Absorbed)
	}
	if r.rev("feat-a") == tipBefore {
		t.Fatal("feat-a tip unchanged")
	}
	if got := r.git("show", "feat-a:shared.txt"); got != "A2\np\nq\nr\ns\nt\nZ2" {
		t.Fatalf("feat-a:shared.txt = %q, want both edits landed in one amend", got)
	}
	if got := r.git("status", "--porcelain"); got != "" {
		t.Fatalf("status = %q, want clean", got)
	}
}

// TestAbsorbRefusesModeRideAlong pins the classify-or-refuse gate end to end:
// a cleanly absorbable text hunk co-staged with a chmod on another file must
// come back unapplied (the mode bit would otherwise silently ride the applied
// patch into the target commit), with refs and the staged set untouched.
func TestAbsorbRefusesModeRideAlong(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.writeFile("shared.txt", "A0\np\nq\nB0\n")
	r.writeFile("tool.sh", "echo hi\n")
	r.git("add", "shared.txt", "tool.sh")
	r.git("commit", "-q", "-m", "seed")
	r.create("feat-a", "shared.txt", "A1\np\nq\nB0\n", "a")

	tipBefore := r.rev("feat-a")
	// A single-target text edit plus a staged chmod on tool.sh. The chmod is
	// applied to BOTH the worktree file and the index: on unix the two must
	// agree or the unstaged-mode guard fires first; on Windows
	// core.filemode=false makes the worktree bit invisible to git, so only
	// the explicit update-index staging creates the mode change there.
	r.writeFile("shared.txt", "A2\np\nq\nB0\n")
	r.git("add", "shared.txt")
	if err := os.Chmod(filepath.Join(r.dir, "tool.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.git("add", "tool.sh")
	r.git("update-index", "--chmod=+x", "tool.sh")

	res := r.stOK("absorb")
	if !strings.Contains(res.stdout, "not applied:") || !strings.Contains(res.stdout, "mode change") {
		t.Fatalf("stdout = %q, want the not-applied summary naming the mode change", res.stdout)
	}
	if r.rev("feat-a") != tipBefore {
		t.Fatal("feat-a moved during a refused absorb")
	}
	if diff := r.git("diff", "--cached", "--name-only"); !strings.Contains(diff, "shared.txt") || !strings.Contains(diff, "tool.sh") {
		t.Fatalf("refused absorb disturbed the staged set; diff --cached = %q", diff)
	}
}
