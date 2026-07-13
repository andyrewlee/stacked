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
	// A single-target text edit plus a staged chmod on tool.sh (worktree and
	// index agree on the new mode, so only the STAGED set carries it).
	r.writeFile("shared.txt", "A2\np\nq\nB0\n")
	r.git("add", "shared.txt")
	if err := os.Chmod(filepath.Join(r.dir, "tool.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.git("add", "tool.sh")

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

// TestAbsorbApplyRefusesMultiTarget pins the v1 single-target gate end to end:
// staged hunks attributed to two different branches come back as an unapplied
// plan (exit 0, refs untouched, edit still staged).
func TestAbsorbApplyRefusesMultiTarget(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.writeFile("shared.txt", "A0\np\nq\nB0\n")
	r.git("add", "shared.txt")
	r.git("commit", "-q", "-m", "seed")
	r.create("feat-a", "shared.txt", "A1\np\nq\nB0\n", "a")
	r.create("feat-b", "shared.txt", "A1\np\nq\nB1\n", "b")

	tipsBefore := map[string]string{"feat-a": r.rev("feat-a"), "feat-b": r.rev("feat-b")}
	// Line 1 is feat-a's, line 4 is feat-b's: two targets in one staged set.
	r.writeFile("shared.txt", "A2\np\nq\nB2\n")
	r.git("add", "shared.txt")

	res := r.stOK("absorb")
	if !strings.Contains(res.stdout, "not applied: absorb v1 handles a single target") {
		t.Fatalf("stdout = %q, want the not-applied summary", res.stdout)
	}
	for b, tip := range tipsBefore {
		if r.rev(b) != tip {
			t.Fatalf("%s moved during an unapplied absorb", b)
		}
	}
	if diff := r.git("diff", "--cached", "--name-only"); !strings.Contains(diff, "shared.txt") {
		t.Fatalf("unapplied absorb unstaged the edit; diff --cached = %q", diff)
	}
}
