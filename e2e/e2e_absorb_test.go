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

// TestAbsorbUndoRecoveryPointerJourney pins the end-to-end recovery contract:
// `st undo` after an absorb restores the pre-absorb refs, which orphans the
// amended commit carrying the staged edit — so both `undo --dry-run` and
// `undo` must NAME that commit, and `git cherry-pick` on it must reproduce
// the absorbed content.
func TestAbsorbUndoRecoveryPointerJourney(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.writeFile("shared.txt", "A0\np\nq\nB0\n")
	r.git("add", "shared.txt")
	r.git("commit", "-q", "-m", "seed")
	r.create("feat-a", "shared.txt", "A1\np\nq\nB0\n", "a")
	r.create("feat-b", "shared.txt", "A1\np\nq\nB1\n", "b")

	// Stage an edit to line 1, owned by feat-a's tip.
	r.writeFile("shared.txt", "A2\np\nq\nB1\n")
	r.git("add", "shared.txt")

	out := r.stOK("absorb", "--json").stdout
	var res struct {
		Absorbed []struct {
			Branch string `json:"branch"`
			Commit string `json:"commit"`
		} `json:"absorbed"`
		Notes []string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode absorb json: %v\n%s", err, out)
	}
	if len(res.Absorbed) != 1 {
		t.Fatalf("absorbed = %+v, want one hunk", res.Absorbed)
	}
	amended := res.Absorbed[0].Commit
	if joined := strings.Join(res.Notes, "\n"); !strings.Contains(joined, amended) || !strings.Contains(joined, "cherry-pick") {
		t.Fatalf("absorb notes = %v, want the amended commit and a recovery pointer", res.Notes)
	}

	// The dry-run warns BEFORE the user orphans the commit.
	dry := r.stOK("undo", "--dry-run")
	if !strings.Contains(dry.stdout, amended) || !strings.Contains(dry.stdout, "cherry-pick") {
		t.Fatalf("undo --dry-run = %q, want the amended SHA and recovery hint", dry.stdout)
	}

	undo := r.stOK("undo")
	if !strings.Contains(undo.stdout, amended) || !strings.Contains(undo.stdout, "git cherry-pick "+amended) {
		t.Fatalf("undo = %q, want the amended SHA and a cherry-pick command", undo.stdout)
	}
	if got := r.git("cat-file", "-t", amended); got != "commit" {
		t.Fatalf("cat-file -t %s = %q after undo, want a still-resolvable commit", amended, got)
	}
	if got := r.git("show", amended+":shared.txt"); got != "A2\np\nq\nB0" {
		t.Fatalf("%s:shared.txt = %q, want the absorbed staged edit", amended, got)
	}

	// Recovery per the note: cherry-pick the named commit onto its own
	// parent — the absorbed edit is reproduced in full. Undo deliberately
	// never touches the worktree, so the leftover absorbed content is still
	// there; force the detached checkout, then restore feat-b's files.
	r.git("checkout", "-qf", "--detach", amended+"^")
	r.git("cherry-pick", amended)
	if got := r.git("show", "HEAD:shared.txt"); got != "A2\np\nq\nB0" {
		t.Fatalf("cherry-picked HEAD:shared.txt = %q, want the recovered edit", got)
	}
	r.git("checkout", "-qf", "feat-b")
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
// paused cascade, then one undo restores every pre-absorb tip — and names
// the amended commit that still carries the staged edit, so it can be
// cherry-picked back.
func TestAbsorbConflictAbortUndoJourney(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	tipsBefore := absorbConflictFixture(t, r)
	// The amend landed before the cascade paused; its checkpoint is already
	// in the retained undo entry even though absorb errored.
	amended := r.rev("feat-a")

	r.stOK("abort")
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); !os.IsNotExist(err) {
		t.Fatalf("rebase still in progress after abort: %v", err)
	}
	r.stOK("validate")

	// The preview names the commit BEFORE it would be orphaned.
	dry := r.stOK("undo", "--dry-run").stdout
	if !strings.Contains(dry, amended) || !strings.Contains(dry, "cherry-pick") {
		t.Fatalf("undo --dry-run = %q, want the amended SHA %s and a recovery hint", dry, amended)
	}

	undoOut := r.stOK("undo").stdout
	if !strings.Contains(undoOut, amended) || !strings.Contains(undoOut, "git cherry-pick "+amended) {
		t.Fatalf("undo = %q, want the amended SHA %s and a cherry-pick command", undoOut, amended)
	}
	for b, tip := range tipsBefore {
		if got := r.rev(b); got != tip {
			t.Fatalf("%s = %s after undo, want restored %s\nundo output:\n%s", b, got, tip, undoOut)
		}
	}

	// The pointer is usable: the amended commit is still resolvable and
	// cherry-picking it reproduces the absorbed edit in full.
	if got := r.git("cat-file", "-t", amended); got != "commit" {
		t.Fatalf("cat-file -t %s = %q, want a still-resolvable commit", amended, got)
	}
	r.git("checkout", "-qf", "-b", "recovery", tipsBefore["main"])
	r.git("cherry-pick", amended)
	if got := r.git("show", "HEAD:shared.txt"); got != "A2\nB0" {
		t.Fatalf("recovery:shared.txt = %q, want the absorbed edit", got)
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

// TestAbsorbOffPathSiblingTip proves the on-path attribution guard end to
// end: a second tracked stack whose tip SHARES the owning commit must never
// receive the hunk — attribution stays on the current stack's path, the apply
// amends the on-path tip and restacks the current chain, and the side stack
// is left untouched.
func TestAbsorbOffPathSiblingTip(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.writeFile("shared.txt", "A0\np\nq\nB0\n")
	r.git("add", "shared.txt")
	r.git("commit", "-q", "-m", "seed")
	r.create("feat-a", "shared.txt", "A1\np\nq\nB0\n", "a")
	r.create("feat-b", "shared.txt", "A1\np\nq\nB1\n", "b")

	// side is a second tracked stack rooted at the trunk, pointing at the
	// SAME commit as feat-a — the off-path sibling sharing the owning tip.
	r.git("checkout", "-q", "-b", "side", "feat-a")
	r.stOK("track", "--parent", "main")
	r.git("checkout", "-q", "feat-b")
	sideTip := r.rev("side")

	// Stage an edit to line 1, owned by feat-a's tip (= side's tip).
	r.writeFile("shared.txt", "A2\np\nq\nB1\n")
	r.git("add", "shared.txt")

	// The dry run must attribute on-path — never to side.
	out := r.stOK("absorb", "--dry-run", "--json").stdout
	var plan struct {
		Absorbed []struct {
			Branch string `json:"branch"`
		} `json:"absorbed"`
		Refused []struct {
			Reason string `json:"reason"`
		} `json:"refused"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("decode absorb --dry-run json: %v\n%s", err, out)
	}
	if len(plan.Absorbed) != 1 || plan.Absorbed[0].Branch != "feat-a" || len(plan.Refused) != 0 {
		t.Fatalf("plan = %+v, want one hunk into on-path feat-a, none refused", plan)
	}

	out = r.stOK("absorb", "--json").stdout
	var res struct {
		Absorbed []struct {
			Branch string `json:"branch"`
		} `json:"absorbed"`
		Restacked []string `json:"restacked"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode absorb json: %v\n%s", err, out)
	}
	if len(res.Absorbed) != 1 || res.Absorbed[0].Branch != "feat-a" {
		t.Fatalf("absorbed = %+v, want the hunk in feat-a", res.Absorbed)
	}
	if len(res.Restacked) != 1 || res.Restacked[0] != "feat-b" {
		t.Fatalf("restacked = %v, want [feat-b]", res.Restacked)
	}
	if r.rev("side") != sideTip {
		t.Fatal("side's tip moved; the hunk must never land off the current stack's path")
	}
	if got := r.git("show", "feat-b:shared.txt"); got != "A2\np\nq\nB1" {
		t.Fatalf("feat-b:shared.txt = %q, want the absorbed edit restacked in", got)
	}
	if got := r.git("status", "--porcelain"); got != "" {
		t.Fatalf("status = %q, want clean", got)
	}
	if got := r.currentBranch(); got != "feat-b" {
		t.Fatalf("HEAD = %q, want feat-b", got)
	}
	r.stOK("validate")
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

// TestAbsorbPreservesMixedEmptyFileChanges proves the staged-diff accounting
// invariant end to end: a staged set pairing an attributable text edit with a
// metadata-only change (an empty file add or delete — a diff section that
// yields NO hunks) is refused wholesale by both --dry-run and apply, leaving
// the index, worktree bytes, refs, and the undo journal exactly as they were.
// Before the parser accounted for empty files, the apply's index reset would
// have silently discarded the empty-file change.
func TestAbsorbPreservesMixedEmptyFileChanges(t *testing.T) {
	t.Parallel()
	for _, variant := range []string{"add", "delete"} {
		t.Run(variant, func(t *testing.T) {
			r := newRepo(t)
			r.initStack()
			r.writeFile("shared.txt", "A0\np\nq\nB0\n")
			r.git("add", "shared.txt")
			if variant == "delete" {
				r.writeFile("empty.txt", "")
				r.git("add", "empty.txt")
			}
			r.git("commit", "-q", "-m", "seed")
			r.create("feat-a", "shared.txt", "A1\np\nq\nB0\n", "a")

			// Stage the attributable edit plus the metadata-only change.
			r.writeFile("shared.txt", "A2\np\nq\nB0\n")
			r.git("add", "shared.txt")
			if variant == "add" {
				r.writeFile("empty.txt", "")
				r.git("add", "empty.txt")
			} else {
				r.git("rm", "-q", "--", "empty.txt")
			}

			snapshot := func() (index, status, undo string) {
				index = r.git("ls-files", "--stage")
				status = r.git("status", "--porcelain")
				undo = r.stOK("undo", "--list").stdout
				return index, status, undo
			}
			indexBefore, statusBefore, undoBefore := snapshot()
			tipsBefore := map[string]string{"main": r.rev("main"), "feat-a": r.rev("feat-a")}

			dry := r.stOK("absorb", "--dry-run")
			if !strings.Contains(dry.stdout, "refuse empty.txt") {
				t.Fatalf("dry-run = %q, want a refusal naming empty.txt", dry.stdout)
			}
			res := r.stOK("absorb")
			if !strings.Contains(res.stdout, "not applied:") || !strings.Contains(res.stdout, "refuse empty.txt") {
				t.Fatalf("apply = %q, want the not-applied summary refusing empty.txt", res.stdout)
			}

			indexAfter, statusAfter, undoAfter := snapshot()
			if indexAfter != indexBefore {
				t.Fatalf("index changed during refused absorb\nbefore:\n%s\nafter:\n%s", indexBefore, indexAfter)
			}
			if statusAfter != statusBefore {
				t.Fatalf("worktree/index state changed during refused absorb\nbefore:\n%s\nafter:\n%s", statusBefore, statusAfter)
			}
			if undoAfter != undoBefore {
				t.Fatalf("undo journal changed during refused absorb\nbefore:\n%s\nafter:\n%s", undoBefore, undoAfter)
			}
			for b, tip := range tipsBefore {
				if r.rev(b) != tip {
					t.Fatalf("%s moved during a refused absorb: %s -> %s", b, tip, r.rev(b))
				}
			}
			// The staged bytes themselves are untouched (r.git trims, so the
			// trailing newline is intentionally absent here).
			if got := r.git("show", ":shared.txt"); got != "A2\np\nq\nB0" {
				t.Fatalf("staged shared.txt = %q, want the staged edit preserved", got)
			}
			r.stOK("validate")
		})
	}
}

// TestAbsorbRefusesShiftedRepeatedLines pins the coordinate-integrity gate:
// blame identifies the OWNING commit, but absorb's -U0 patch applies at HEAD
// line numbers against that commit's tree. When a descendant shifted the
// lines (insert/delete) or renamed the file, the HEAD coordinates do not
// exist at the target tip — and with repeated text the patch can apply to the
// WRONG occurrence. Those mappings are refused, preserving refs, the index,
// worktree bytes, and the undo journal. An unshifted repeated line still
// absorbs, so the ban is on unsafe coordinates, not repeated text.
func TestAbsorbRefusesShiftedRepeatedLines(t *testing.T) {
	t.Parallel()

	preserved := func(t *testing.T, r *repo, branches []string) func() {
		t.Helper()
		tips := map[string]string{}
		for _, b := range branches {
			tips[b] = r.rev(b)
		}
		indexBefore := r.git("ls-files", "--stage")
		statusBefore := r.git("status", "--porcelain")
		undoBefore := r.stOK("undo", "--list").stdout
		return func() {
			t.Helper()
			for b, tip := range tips {
				if r.rev(b) != tip {
					t.Fatalf("%s moved during a refused absorb: %s -> %s", b, tip, r.rev(b))
				}
			}
			if got := r.git("ls-files", "--stage"); got != indexBefore {
				t.Fatalf("index changed during refused absorb\nbefore:\n%s\nafter:\n%s", indexBefore, got)
			}
			if got := r.git("status", "--porcelain"); got != statusBefore {
				t.Fatalf("worktree/index changed during refused absorb\nbefore:\n%s\nafter:\n%s", statusBefore, got)
			}
			if got := r.stOK("undo", "--list").stdout; got != undoBefore {
				t.Fatalf("undo journal changed during refused absorb\nbefore:\n%s\nafter:\n%s", undoBefore, got)
			}
		}
	}

	t.Run("insert-shifted repeated line is refused", func(t *testing.T) {
		r := newRepo(t)
		r.initStack()
		// main seeds f.txt; feat-a owns the FIRST "same" (its line 2); feat-b
		// inserts two lines at the top, pushing the owned line to HEAD line 4.
		r.writeFile("f.txt", "top\nx\nmiddle\nsame\nbottom\n")
		r.git("add", "-A")
		r.git("commit", "-q", "-m", "seed")
		r.create("feat-a", "f.txt", "top\nsame\nmiddle\nsame\nbottom\n", "a")
		r.create("feat-b", "f.txt", "n1\nn2\ntop\nsame\nmiddle\nsame\nbottom\n", "b")

		// Stage an edit of feat-a's line (HEAD line 4).
		r.writeFile("f.txt", "n1\nn2\ntop\nEDITED\nmiddle\nsame\nbottom\n")
		r.git("add", "f.txt")

		check := preserved(t, r, []string{"main", "feat-a", "feat-b"})
		featATip := r.rev("feat-a")

		dry := r.stOK("absorb", "--dry-run")
		if !strings.Contains(dry.stdout, "refuse f.txt") {
			t.Fatalf("dry-run = %q, want a refusal for f.txt", dry.stdout)
		}
		res := r.stOK("absorb")
		if !strings.Contains(res.stdout, "not applied:") {
			t.Fatalf("apply = %q, want the not-applied summary", res.stdout)
		}
		check()

		// The corruption this refuses: applying @@ -4 +4 @@ -same +EDITED to
		// feat-a's tree lands on the OTHER "same" (its line 4). Verify both
		// occurrences survived untouched.
		if got := r.git("show", "feat-a:f.txt"); got != "top\nsame\nmiddle\nsame\nbottom" {
			t.Fatalf("feat-a:f.txt = %q, want both same lines intact (feat-a tip %s)", got, featATip)
		}
	})

	t.Run("delete-shifted line is refused", func(t *testing.T) {
		r := newRepo(t)
		r.initStack()
		// feat-a owns line 3; feat-b deletes line 1, shifting it to HEAD line 2.
		r.writeFile("f.txt", "gone\ntop\nx\nmiddle\nbottom\n")
		r.git("add", "-A")
		r.git("commit", "-q", "-m", "seed")
		r.create("feat-a", "f.txt", "gone\ntop\nkeep\nmiddle\nbottom\n", "a")
		r.create("feat-b", "f.txt", "top\nkeep\nmiddle\nbottom\n", "b")

		r.writeFile("f.txt", "top\nEDITED\nmiddle\nbottom\n")
		r.git("add", "f.txt")

		check := preserved(t, r, []string{"main", "feat-a", "feat-b"})

		dry := r.stOK("absorb", "--dry-run")
		if !strings.Contains(dry.stdout, "refuse f.txt") {
			t.Fatalf("dry-run = %q, want a refusal for f.txt", dry.stdout)
		}
		res := r.stOK("absorb")
		if !strings.Contains(res.stdout, "not applied:") {
			t.Fatalf("apply = %q, want the not-applied summary", res.stdout)
		}
		check()
		if got := r.git("show", "feat-a:f.txt"); got != "gone\ntop\nkeep\nmiddle\nbottom" {
			t.Fatalf("feat-a:f.txt = %q, want unchanged", got)
		}
	})

	t.Run("historical rename is refused", func(t *testing.T) {
		r := newRepo(t)
		r.initStack()
		// feat-a owns line 2 of f.txt; feat-b renames the file to g.txt, so
		// blame on g.txt reports the old path.
		r.writeFile("f.txt", "top\nx\nbottom\n")
		r.git("add", "-A")
		r.git("commit", "-q", "-m", "seed")
		r.create("feat-a", "f.txt", "top\nkeep\nbottom\n", "a")
		r.stOK("create", "feat-b")
		r.git("mv", "f.txt", "g.txt")
		r.git("commit", "-q", "-m", "rename f to g")

		r.writeFile("g.txt", "top\nEDITED\nbottom\n")
		r.git("add", "g.txt")

		check := preserved(t, r, []string{"main", "feat-a", "feat-b"})

		dry := r.stOK("absorb", "--dry-run")
		if !strings.Contains(dry.stdout, "refuse g.txt") {
			t.Fatalf("dry-run = %q, want a refusal for g.txt", dry.stdout)
		}
		res := r.stOK("absorb")
		if !strings.Contains(res.stdout, "not applied:") {
			t.Fatalf("apply = %q, want the not-applied summary", res.stdout)
		}
		check()
	})

	t.Run("unshifted repeated line still absorbs", func(t *testing.T) {
		r := newRepo(t)
		r.initStack()
		// feat-a owns the FIRST "same" at line 2 and nothing shifts it; the
		// second "same" (line 4) stays main's.
		r.writeFile("f.txt", "top\nx\nmiddle\nsame\nbottom\n")
		r.git("add", "-A")
		r.git("commit", "-q", "-m", "seed")
		r.create("feat-a", "f.txt", "top\nsame\nmiddle\nsame\nbottom\n", "a")

		r.writeFile("f.txt", "top\nEDITED\nmiddle\nsame\nbottom\n")
		r.git("add", "f.txt")

		res := r.stOK("absorb")
		if !strings.Contains(res.stdout, "absorbed 1 hunk(s) into feat-a") {
			t.Fatalf("apply = %q, want the unshifted hunk absorbed into feat-a", res.stdout)
		}
		if got := r.git("show", "feat-a:f.txt"); got != "top\nEDITED\nmiddle\nsame\nbottom" {
			t.Fatalf("feat-a:f.txt = %q, want the first same edited", got)
		}
	})
}
