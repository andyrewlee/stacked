package e2e

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// Prune journeys: the standalone `st prune` mutating command driven through
// the real binary — deletion, the undo restore, the dry-run preview, and the
// explicit --remote basis arm.

// TestPruneJourney merges a branch into the trunk, prunes it through the real
// binary, and pins the adapter boundary end to end: the --json payload names
// the deleted branch, `st log` drops it, `st undo` restores ref + tracking,
// the dry run predicts without deleting, and --remote switches the prune
// basis to the remote-tracking ref.
func TestPruneJourney(t *testing.T) {
	t.Parallel()
	r := newRepo(t)

	bare := filepath.Join(t.TempDir(), "remote.git")
	r.initBare(bare)
	r.git("remote", "add", "origin", bare)
	r.git("push", "-q", "-u", "origin", "main")

	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")

	// Merge feat-a into main so it becomes a prune candidate; push so the
	// remote-tracking ref contains it too.
	r.stOK("checkout", "main")
	r.git("merge", "-q", "--no-ff", "feat-a", "-m", "merge feat-a")
	r.git("push", "-q", "origin", "main")

	// The apply arm: feat-a is deleted and reported in the JSON payload.
	res := r.st("prune", "--json")
	wantExit(t, res, 0)
	var applied map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &applied); err != nil {
		t.Fatalf("prune --json invalid: %v\n%s", err, res.stdout)
	}
	if names, _ := applied["deleted"].([]any); len(names) != 1 || names[0] != "feat-a" {
		t.Fatalf("deleted = %v, want [feat-a]", applied["deleted"])
	}
	if r.branchExists("feat-a") {
		t.Fatal("feat-a should be pruned")
	}

	// The stack reports feat-b re-parented onto main, feat-a gone.
	res = r.stOK("log", "--json")
	var root logNode
	if err := json.Unmarshal([]byte(res.stdout), &root); err != nil {
		t.Fatalf("log --json invalid: %v", err)
	}
	b := findNode(&root, "feat-b")
	if b == nil || b.Parent != "main" {
		t.Fatalf("feat-b should be re-parented onto main: %+v", b)
	}
	if findNode(&root, "feat-a") != nil {
		t.Fatal("feat-a still appears in the log after prune")
	}

	// `st undo` restores the pruned branch — ref and tracking record.
	r.stOK("undo")
	if !r.branchExists("feat-a") {
		t.Fatal("undo should restore the pruned feat-a ref")
	}
	res = r.stOK("log", "--json")
	root = logNode{}
	if err := json.Unmarshal([]byte(res.stdout), &root); err != nil {
		t.Fatalf("log --json invalid: %v", err)
	}
	if a := findNode(&root, "feat-a"); a == nil {
		t.Fatal("undo should restore feat-a's tracking")
	}

	// The dry run predicts the same deletion without applying it.
	res = r.st("prune", "--dry-run", "--json")
	wantExit(t, res, 0)
	var dry map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &dry); err != nil {
		t.Fatalf("prune --dry-run --json invalid: %v\n%s", err, res.stdout)
	}
	if dry["dryRun"] != true {
		t.Fatalf("dryRun = %v, want true", dry["dryRun"])
	}
	if names, _ := dry["deleted"].([]any); len(names) != 1 || names[0] != "feat-a" {
		t.Fatalf("dry-run deleted = %v, want [feat-a]", dry["deleted"])
	}
	if !r.branchExists("feat-a") || !r.fileOnBranch("feat-a", "a.txt") {
		t.Fatal("prune --dry-run deleted feat-a")
	}

	// The --remote arm prunes against the remote-tracking ref instead of the
	// local trunk — origin/main contains the merge, so feat-a goes.
	res = r.st("prune", "--remote", "origin", "--json")
	wantExit(t, res, 0)
	var remoteApplied map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &remoteApplied); err != nil {
		t.Fatalf("prune --remote --json invalid: %v\n%s", err, res.stdout)
	}
	if names, _ := remoteApplied["deleted"].([]any); len(names) != 1 || names[0] != "feat-a" {
		t.Fatalf("remote deleted = %v, want [feat-a]", remoteApplied["deleted"])
	}
	if r.branchExists("feat-a") {
		t.Fatal("feat-a should be pruned against the remote basis")
	}
	r.stOK("validate")
}

// TestPruneRemoteMissingTrackingRef pins the adapter's remote arm guard: an
// explicit --remote whose tracking ref is absent refuses with a fetch hint
// instead of silently falling back to the local trunk.
func TestPruneRemoteMissingTrackingRef(t *testing.T) {
	t.Parallel()
	r := newRepo(t)

	bare := filepath.Join(t.TempDir(), "remote.git")
	r.initBare(bare)
	r.git("remote", "add", "origin", bare)

	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")

	res := r.st("prune", "--remote", "origin")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "has no tracking ref for trunk")
	if !r.branchExists("feat-a") {
		t.Fatal("a refused --remote prune must not delete anything")
	}
}
