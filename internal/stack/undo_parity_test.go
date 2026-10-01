package stack

import (
	"errors"
	"strings"
	"testing"
)

// undo_parity_test.go pins UndoPreview against the real Undo: every refusal a
// real run can hit must be predicted by a preview blocker (and the preview
// must not claim a checkout a real run cannot perform). The preview and the
// op are twin code paths by design; this file is the drift alarm.
//
// Each fixture is built TWICE — once for the read-only preview, once for the
// mutating op — because fakeGit state is consumed by Undo. The fake's ids
// are deterministic per construction order, so identical steps produce
// identical worlds.

// undoParityCase describes one shared scenario and what each twin must say.
type undoParityCase struct {
	name string
	// build returns a fresh (fakeGit, state, journal-entry) fixture.
	build func(t *testing.T) (*fakeGit, *State, *UndoEntry)
	// canTeleport is the shell-shim fact cmd passes to the preview.
	canTeleport bool
	// wantBlockers is the EXACT expected blocker list (catches both drift
	// directions: an unpredicted refusal and an over-predicted blocker).
	wantBlockers []string
	// wantDetach: the preview predicts HEAD would park detached (the doomed
	// current branch's intermediate checkout is blocked).
	wantDetach bool
	// wantCheckout: predicted final landing branch; "" means null.
	wantCheckout string
	// wantUndoErr is a substring of the real op's error, or "" when a real
	// run must succeed.
	wantUndoErr string
	// wantDetached asserts the real run ended with HEAD detached.
	wantDetached bool
	// skipRealUndo marks gates that live in cmd (a real engine Undo would
	// proceed — the refusal is asserted as a preview blocker only).
	skipRealUndo bool
	// afterUndo, when set, runs extra assertions on the post-undo fake.
	afterUndo func(t *testing.T, f *fakeGit)
}

func TestUndoPreviewParity(t *testing.T) {
	cases := []undoParityCase{
		{
			name: "clean created branch",
			build: func(t *testing.T) (*fakeGit, *State, *UndoEntry) {
				f, s, _ := newEnvState()
				entry := mustSnapshot(t, s, f, "create feat-x")
				mkBranch(t, Env{Git: f}, s, f, "main", "feat-x")
				if err := f.Checkout("main"); err != nil {
					t.Fatal(err)
				}
				entry.CreatedBranches = []string{"feat-x"}
				return f, s, entry
			},
			wantCheckout: "main",
		},
		{
			name: "dirty linked owner refuses in both",
			build: func(t *testing.T) (*fakeGit, *State, *UndoEntry) {
				f, s, _ := newEnvState()
				entry := mustSnapshot(t, s, f, "create feat-x")
				mkBranch(t, Env{Git: f}, s, f, "main", "feat-x")
				f.addWorktree("/wt/feat-x", "feat-x")
				f.markWorktreeDirty("feat-x")
				if err := f.Checkout("main"); err != nil {
					t.Fatal(err)
				}
				entry.CreatedBranches = []string{"feat-x"}
				return f, s, entry
			},
			wantBlockers: []string{"worktree_dirty:feat-x"},
			wantUndoErr:  "uncommitted changes",
		},
		{
			name: "recorded worktree path mismatch refuses in both",
			build: func(t *testing.T) (*fakeGit, *State, *UndoEntry) {
				f, s, _ := newEnvState()
				entry := mustSnapshot(t, s, f, "create feat-x")
				mkBranch(t, Env{Git: f}, s, f, "main", "feat-x")
				f.addWorktree("/wt/actual", "feat-x")
				if err := f.Checkout("main"); err != nil {
					t.Fatal(err)
				}
				entry.CreatedBranches = []string{"feat-x"}
				entry.CreatedWorktrees = map[string]string{"feat-x": "/wt/recorded"}
				return f, s, entry
			},
			wantBlockers: []string{"recorded_worktree_mismatch:feat-x"},
			wantUndoErr:  "undo recorded created worktree",
		},
		{
			name: "doomed current branch with unresolvable target refuses in both",
			build: func(t *testing.T) (*fakeGit, *State, *UndoEntry) {
				f, s, _ := newEnvState()
				entry := mustSnapshot(t, s, f, "create feat-b")
				mkBranch(t, Env{Git: f}, s, f, "main", "feat-b")
				// HEAD stays on feat-b (the doomed branch); main is gone and
				// the entry recorded no ref for it — undo's intermediate
				// checkout target cannot be restored.
				delete(entry.Refs, "main")
				if err := f.DeleteBranch("main", true); err != nil {
					t.Fatal(err)
				}
				entry.CreatedBranches = []string{"feat-b"}
				return f, s, entry
			},
			wantBlockers: []string{"missing_restore_target:feat-b"},
			wantUndoErr:  "cannot restore checkout target",
		},
		{
			name: "doomed current branch whose target is checked out elsewhere detaches",
			build: func(t *testing.T) (*fakeGit, *State, *UndoEntry) {
				f, s, _ := newEnvState()
				entry := mustSnapshot(t, s, f, "create feat-x")
				mkBranch(t, Env{Git: f}, s, f, "main", "feat-x")
				// HEAD stays on feat-x; main is checked out in a linked
				// worktree, so the intermediate checkout is swallowed and
				// HEAD parks detached.
				f.addWorktree("/wt/main", "main")
				f.checkoutErr["main"] = errors.New("fatal: 'main' is already checked out at '/wt/main'")
				entry.CreatedBranches = []string{"feat-x"}
				return f, s, entry
			},
			wantDetach:   true,
			wantDetached: true,
		},
		{
			name: "doomed current branch with dirty caller worktree detaches",
			build: func(t *testing.T) (*fakeGit, *State, *UndoEntry) {
				f, s, _ := newEnvState()
				entry := mustSnapshot(t, s, f, "create feat-x")
				mkBranch(t, Env{Git: f}, s, f, "main", "feat-x")
				// feat-x is checked out in the CALLER's worktree (main
				// worktree — no linked owner), which is dirty; the
				// intermediate checkout fails with local changes.
				f.clean = false
				f.checkoutErr["main"] = errors.New("error: your local changes to 'f.txt' would be overwritten by checkout")
				entry.CreatedBranches = []string{"feat-x"}
				return f, s, entry
			},
			wantDetach:   true,
			wantDetached: true,
		},
		{
			name: "cwd inside doomed linked worktree blocks without the shim",
			build: func(t *testing.T) (*fakeGit, *State, *UndoEntry) {
				f, s, _ := newEnvState()
				entry := mustSnapshot(t, s, f, "create feat-x")
				mkBranch(t, Env{Git: f}, s, f, "main", "feat-x")
				f.addWorktree("/wt/feat-x", "feat-x")
				f.repoRoot = "/wt/feat-x" // caller is standing in it
				entry.CreatedBranches = []string{"feat-x"}
				return f, s, entry
			},
			wantBlockers: []string{"cwd_inside_created_worktree:feat-x"},
			wantUndoErr:  "you are inside it",
		},
		{
			name: "LocalBranches==nil degrade: created resources ignored by both",
			build: func(t *testing.T) (*fakeGit, *State, *UndoEntry) {
				f, s, _ := newEnvState()
				entry := mustSnapshot(t, s, f, "pre-local-branches entry")
				mkBranch(t, Env{Git: f}, s, f, "main", "feat-x")
				f.addWorktree("/wt/feat-x", "feat-x")
				if err := f.Checkout("main"); err != nil {
					t.Fatal(err)
				}
				// A journal entry predating the local-branch capture: undo
				// cannot tell what was created, so BOTH twins skip all
				// created-resource cleanup — the worktree AND branch stay.
				entry.LocalBranches = nil
				entry.CreatedWorktrees = map[string]string{"feat-x": "/wt/feat-x"}
				return f, s, entry
			},
			wantCheckout: "main",
			afterUndo: func(t *testing.T, f *fakeGit) {
				t.Helper()
				if _, ok := f.branches["feat-x"]; !ok {
					t.Fatal("degrade run deleted feat-x")
				}
				if f.linkedWorktrees["feat-x"] == "" {
					t.Fatal("degrade run removed feat-x's worktree")
				}
			},
		},
		{
			name: "malformed snapshot refuses in both",
			build: func(t *testing.T) (*fakeGit, *State, *UndoEntry) {
				f, s, _ := newEnvState()
				entry := mustSnapshot(t, s, f, "corrupt")
				entry.State = []byte("{bad json")
				return f, s, entry
			},
			wantBlockers: []string{"malformed_snapshot"},
			wantUndoErr:  "parsing undo state",
		},
		{
			name: "snapshot from a newer schema refuses in both",
			build: func(t *testing.T) (*fakeGit, *State, *UndoEntry) {
				f, s, _ := newEnvState()
				entry := mustSnapshot(t, s, f, "future")
				entry.State = []byte(`{"version":999}`)
				return f, s, entry
			},
			wantBlockers: []string{"state_too_new"},
			wantUndoErr:  "schema v999",
		},
		{
			name: "paused rebase blocks the cmd gate",
			build: func(t *testing.T) (*fakeGit, *State, *UndoEntry) {
				f, s, _ := newEnvState()
				entry := mustSnapshot(t, s, f, "modify")
				f.rebaseActive = true
				f.rebaseWT = ""
				return f, s, entry
			},
			// The refusal lives in cmd/runUndo's gate, not the engine — a
			// real Undo call would proceed, so only the preview leg runs.
			skipRealUndo: true,
			wantBlockers: []string{"rebase_in_progress"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pf, ps, pe := tc.build(t)
			callsBefore := pf.callsSnapshot()
			preview, err := UndoPreview(Env{Git: pf}, ps, pe, tc.canTeleport, 1)
			if err != nil {
				t.Fatalf("UndoPreview: %v", err)
			}
			assertNoMutation(t, pf, callsBefore)

			if len(preview.Blockers) != len(tc.wantBlockers) {
				t.Fatalf("preview blockers = %v, want exactly %v", preview.Blockers, tc.wantBlockers)
			}
			for i, want := range tc.wantBlockers {
				if preview.Blockers[i] != want {
					t.Fatalf("blocker[%d] = %q, want %q (all: %v)", i, preview.Blockers[i], want, preview.Blockers)
				}
			}
			if preview.WouldDetach != tc.wantDetach {
				t.Fatalf("wouldDetach = %v, want %v", preview.WouldDetach, tc.wantDetach)
			}
			gotCheckout := ""
			if preview.WouldCheckout != nil {
				gotCheckout = *preview.WouldCheckout
			}
			if gotCheckout != tc.wantCheckout {
				t.Fatalf("wouldCheckout = %q, want %q", gotCheckout, tc.wantCheckout)
			}

			if tc.skipRealUndo {
				return
			}
			// The real run on an identical fresh fixture.
			rf, rs, re := tc.build(t)
			_, uerr := Undo(Env{Git: rf}, rs, re)
			if tc.wantUndoErr == "" {
				if uerr != nil {
					t.Fatalf("Undo: %v (preview predicted %v blockers)", uerr, preview.Blockers)
				}
				if tc.wantDetached && rf.head != "" {
					t.Fatalf("HEAD = %q after undo, want detached", rf.head)
				}
			} else {
				if uerr == nil {
					t.Fatalf("Undo succeeded; preview blockers = %v, want error containing %q", preview.Blockers, tc.wantUndoErr)
				}
				if !strings.Contains(uerr.Error(), tc.wantUndoErr) {
					t.Fatalf("Undo error = %q, want substring %q", uerr.Error(), tc.wantUndoErr)
				}
			}
			// Cross-direction: a predicted blocker list and a predicted
			// refusal must agree (the table author pins the mapping).
			if !tc.skipRealUndo && (tc.wantUndoErr != "") != (len(tc.wantBlockers) > 0) {
				t.Fatalf("bad case: wantUndoErr=%q but wantBlockers=%v — every real refusal needs a predicted blocker", tc.wantUndoErr, tc.wantBlockers)
			}
			if tc.afterUndo != nil && uerr == nil {
				tc.afterUndo(t, rf)
			}
		})
	}
}
