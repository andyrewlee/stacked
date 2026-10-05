package e2e

import (
	"encoding/json"
	"strings"
	"testing"
)

// Wire-level JSON coverage for the envelope surfaces that were only ever
// asserted in-process: an apply-path `undo --json`, `undo --force` over
// external drift, and the `validate`/`repair` JSON pair. Each row parses
// stdout as JSON — a diagnostic leaking onto stdout fails the unmarshal,
// which is exactly the purity contract agents rely on.

// TestUndoJSONApply asserts `st undo --json` emits a parseable
// {undone,label,restored} payload on stdout for the single-entry apply.
func TestUndoJSONApply(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")

	res := r.stOK("undo", "--json")
	var applied struct {
		Undone   bool     `json:"undone"`
		Label    string   `json:"label"`
		Restored []string `json:"restored"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &applied); err != nil {
		t.Fatalf("undo --json not parseable: %v\nstdout:\n%s", err, res.stdout)
	}
	if !applied.Undone || applied.Label != "create" {
		t.Fatalf("undo --json = %+v, want {undone:true label:create}", applied)
	}
	if r.branchExists("feat-a") {
		t.Fatal("undo --json left created branch feat-a behind")
	}
}

// TestUndoForceOutsideDrift moves a created branch's tip with raw git —
// outside st's journal — so undo's drift preflight refuses, then --force
// applies anyway.
func TestUndoForceOutsideDrift(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")

	// A commit landed on feat-a outside st's knowledge: undo would rewind it.
	r.git("commit", "-q", "--allow-empty", "-m", "drift")

	res := r.st("undo")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "moved outside st")
	wantStderrContains(t, res, "--force")

	res = r.stOK("undo", "--force", "--json")
	var applied struct {
		Undone   bool     `json:"undone"`
		Label    string   `json:"label"`
		Restored []string `json:"restored"`
		Notes    []string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &applied); err != nil {
		t.Fatalf("undo --force --json not parseable: %v\nstdout:\n%s", err, res.stdout)
	}
	if !applied.Undone || applied.Label != "create" {
		t.Fatalf("undo --force --json = %+v, want {undone:true label:create}", applied)
	}
	if len(applied.Notes) == 0 || !strings.Contains(strings.Join(applied.Notes, " "), "moved outside st") {
		t.Fatalf("undo --force notes = %v, want the drift note", applied.Notes)
	}
	if r.branchExists("feat-a") {
		t.Fatal("undo --force left created branch feat-a behind")
	}
}

// TestValidateRepairJSON pins the wire payloads of the integrity pair on a
// real corruption: a tracked branch deleted behind st's back must surface in
// validate's problems array and be gone after repair --json.
func TestValidateRepairJSON(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")

	r.stOK("checkout", "main")
	r.git("branch", "-D", "feat-b")

	res := r.st("validate", "--json")
	wantExit(t, res, 1)
	var before struct {
		OK       bool     `json:"ok"`
		Tracked  int      `json:"tracked"`
		Problems []string `json:"problems"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &before); err != nil {
		t.Fatalf("validate --json not parseable: %v\nstdout:\n%s", err, res.stdout)
	}
	if before.OK || len(before.Problems) == 0 ||
		!strings.Contains(strings.Join(before.Problems, " "), "feat-b") {
		t.Fatalf("validate --json = %+v, want ok:false naming feat-b", before)
	}

	res = r.stOK("repair", "--json")
	var repaired struct {
		Repaired bool     `json:"repaired"`
		Fixes    []string `json:"fixes"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &repaired); err != nil {
		t.Fatalf("repair --json not parseable: %v\nstdout:\n%s", err, res.stdout)
	}
	if !repaired.Repaired || len(repaired.Fixes) == 0 {
		t.Fatalf("repair --json = %+v, want repaired:true with fixes", repaired)
	}

	res = r.stOK("validate", "--json")
	var after struct {
		OK       bool     `json:"ok"`
		Problems []string `json:"problems"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &after); err != nil {
		t.Fatalf("post-repair validate --json not parseable: %v\n%s", err, res.stdout)
	}
	if !after.OK || len(after.Problems) != 0 {
		t.Fatalf("post-repair validate --json = %+v, want ok:true, no problems", after)
	}
}
