package stack

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/andyrewlee/stacked/internal/git"
)

func mustStackGit(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeStackFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRecordUndoUsesLocalBranchRefs(t *testing.T) {
	initGitRepo(t)

	writeStackFile(t, "main.txt", "main\n")
	mustStackGit(t, "add", "-A")
	mustStackGit(t, "commit", "-q", "-m", "main")
	mainSHA := mustStackGit(t, "rev-parse", "HEAD")

	mustStackGit(t, "checkout", "-q", "-b", "feature")
	writeStackFile(t, "feature.txt", "feature\n")
	mustStackGit(t, "add", "-A")
	mustStackGit(t, "commit", "-q", "-m", "feature")
	branchSHA := mustStackGit(t, "rev-parse", "refs/heads/feature")

	mustStackGit(t, "checkout", "-q", "main")
	mustStackGit(t, "tag", "feature", mainSHA)
	tagSHA := mustStackGit(t, "rev-parse", "refs/tags/feature")
	if tagSHA == branchSHA {
		t.Fatal("test setup failed: tag and branch resolve to the same commit")
	}

	s := &State{Trunk: "main", Branches: map[string]*Branch{}}
	s.Track("feature", "main", mainSHA)
	if _, err := s.RecordUndo(git.Shell{}, "snapshot"); err != nil {
		t.Fatalf("RecordUndo: %v", err)
	}

	entry, ok, err := PeekUndo()
	if err != nil {
		t.Fatalf("PeekUndo: %v", err)
	}
	if !ok {
		t.Fatal("PeekUndo returned no undo entry")
	}
	if got := entry.Refs["feature"]; got != branchSHA {
		t.Fatalf("undo ref for feature = %q, want branch tip %q", got, branchSHA)
	}
	if err := DropUndo(); err != nil {
		t.Fatalf("DropUndo: %v", err)
	}
	if _, ok, err := PeekUndo(); err != nil || ok {
		t.Fatalf("PeekUndo after DropUndo = (ok=%v, err=%v), want empty journal", ok, err)
	}
}

// TestSnapshotUndoCapturesViaPort drives the snapshot capture against the
// in-memory fake: the refs map, local-branch list, and current branch must all
// come from the port, never from the concrete git package — no real git, no
// disk.
func TestSnapshotUndoCapturesViaPort(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	// A tracked branch whose git ref is gone must be omitted, not fatal.
	s.Track("ghost", "main", "nope")

	entry, err := s.snapshotUndo(f, "test-op")
	if err != nil {
		t.Fatalf("snapshotUndo: %v", err)
	}
	if entry.Label != "test-op" {
		t.Fatalf("label = %q, want test-op", entry.Label)
	}
	wantRefs := map[string]string{}
	for _, name := range []string{"main", "a", "b"} {
		sha, err := f.RevParse(name)
		if err != nil {
			t.Fatalf("RevParse(%s): %v", name, err)
		}
		wantRefs[name] = sha
	}
	if !reflect.DeepEqual(entry.Refs, wantRefs) {
		t.Fatalf("refs = %v, want %v", entry.Refs, wantRefs)
	}
	tips, _ := f.Tips()
	wantBranches := make([]string, 0, len(tips))
	for name := range tips {
		wantBranches = append(wantBranches, name)
	}
	sort.Strings(wantBranches)
	if !reflect.DeepEqual(entry.LocalBranches, wantBranches) {
		t.Fatalf("localBranches = %v, want %v", entry.LocalBranches, wantBranches)
	}
	if entry.CurrentBranch != "a" {
		t.Fatalf("currentBranch = %q, want a", entry.CurrentBranch)
	}
	var snap State
	if err := json.Unmarshal(entry.State, &snap); err != nil {
		t.Fatalf("snapshot state does not parse: %v", err)
	}
	if snap.Trunk != "main" || len(snap.Branches) != len(s.Branches) {
		t.Fatalf("snapshot state = %+v, want a copy of the live state", snap)
	}
}

type countingSnapshotGit struct {
	Git
	revParseCalls int
	tipsCalls     int
}

func (g *countingSnapshotGit) RevParse(ref string) (string, error) {
	g.revParseCalls++
	return g.Git.RevParse(ref)
}

func (g *countingSnapshotGit) Tips() (map[string]string, error) {
	g.tipsCalls++
	return g.Git.Tips()
}

func TestSnapshotUndoSpawns(t *testing.T) {
	f, s, env := newEnvState()
	parent := "main"
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		mkBranch(t, env, s, f, parent, name)
		parent = name
	}

	counting := &countingSnapshotGit{Git: f}
	if _, err := s.snapshotUndo(counting, "test-op"); err != nil {
		t.Fatalf("snapshotUndo: %v", err)
	}
	if counting.revParseCalls != 0 {
		t.Fatalf("RevParse calls = %d, want 0", counting.revParseCalls)
	}
	if counting.tipsCalls != 1 {
		t.Fatalf("Tips calls = %d, want 1", counting.tipsCalls)
	}
}

func setupCountingUndoCloseout(t *testing.T) (*fakeGit, *State, *countingSnapshotGit) {
	t.Helper()
	initGitRepo(t)
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	counting := &countingSnapshotGit{Git: f}
	if _, err := s.RecordUndo(counting, "op"); err != nil {
		t.Fatalf("RecordUndo: %v", err)
	}
	counting.revParseCalls = 0
	counting.tipsCalls = 0
	return f, s, counting
}

func TestFinalizeUndoCloseoutTipsOnce(t *testing.T) {
	_, s, counting := setupCountingUndoCloseout(t)
	entry, ok, err := PeekUndo()
	if err != nil {
		t.Fatalf("PeekUndo: %v", err)
	}
	if !ok {
		t.Fatal("PeekUndo returned no undo entry")
	}
	if err := FinalizeUndo(counting, s, entry); err != nil {
		t.Fatalf("FinalizeUndo: %v", err)
	}
	if counting.revParseCalls != 0 {
		t.Fatalf("RevParse calls during FinalizeUndo = %d, want 0", counting.revParseCalls)
	}
	if counting.tipsCalls != 1 {
		t.Fatalf("Tips calls during FinalizeUndo = %d, want 1", counting.tipsCalls)
	}
	entries, err := loadUndo()
	if err != nil {
		t.Fatalf("loadUndo: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("journal = %d entries after successful no-op, want 0", len(entries))
	}
}

func TestCleanupUndoOnErrorCloseoutTipsOnce(t *testing.T) {
	_, s, counting := setupCountingUndoCloseout(t)
	boom := errors.New("op failed")
	if err := CleanupUndoOnError(counting, s, boom); err != nil {
		t.Fatalf("CleanupUndoOnError: %v", err)
	}
	if counting.revParseCalls != 0 {
		t.Fatalf("RevParse calls during CleanupUndoOnError = %d, want 0", counting.revParseCalls)
	}
	if counting.tipsCalls != 1 {
		t.Fatalf("Tips calls during CleanupUndoOnError = %d, want 1", counting.tipsCalls)
	}
	entries, err := loadUndo()
	if err != nil {
		t.Fatalf("loadUndo: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("journal = %d entries after failed no-op, want 0", len(entries))
	}
}

func TestCleanupUndoOnErrorChangedStateCreatedBranchTipsOnce(t *testing.T) {
	f, s, counting := setupCountingUndoCloseout(t)
	mustCheckout(t, f, "a")
	if err := f.CreateBranch("fresh"); err != nil {
		t.Fatal(err)
	}
	s.Track("fresh", "a", s.Branches["a"].ParentSHA)

	boom := errors.New("op failed")
	if err := CleanupUndoOnError(counting, s, boom); err != nil {
		t.Fatalf("CleanupUndoOnError: %v", err)
	}
	if counting.revParseCalls != 0 {
		t.Fatalf("RevParse calls during CleanupUndoOnError = %d, want 0", counting.revParseCalls)
	}
	if counting.tipsCalls != 1 {
		t.Fatalf("Tips calls during CleanupUndoOnError = %d, want 1", counting.tipsCalls)
	}
	entries, err := loadUndo()
	if err != nil {
		t.Fatalf("loadUndo: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("journal = %d entries after changed failure, want 1", len(entries))
	}
	if len(entries[0].CreatedBranches) != 1 || entries[0].CreatedBranches[0] != "fresh" {
		t.Fatalf("createdBranches = %v, want [fresh]", entries[0].CreatedBranches)
	}
}

// tipsErrGit makes Tips fail while delegating everything else to the embedded
// port, to exercise the undo snapshot's handling of an unreadable ref list.
type tipsErrGit struct {
	Git
	err error
}

func (g tipsErrGit) Tips() (map[string]string, error) { return nil, g.err }

// A Tips failure must fail the snapshot rather than recording an entry with no
// refs: such an entry would make a later `st undo` silently restore zero branch
// tips. Failing here aborts the mutation before anything changes.
func TestSnapshotUndoFailsWhenTipsUnavailable(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")

	boom := errors.New("git for-each-ref failed")
	g := tipsErrGit{Git: f, err: boom}

	if _, err := s.snapshotUndo(g, "op"); !errors.Is(err, boom) {
		t.Fatalf("snapshotUndo with failing Tips = %v, want wrapped %v", err, boom)
	}
	if _, err := s.RecordUndo(g, "op"); !errors.Is(err, boom) {
		t.Fatalf("RecordUndo with failing Tips = %v, want wrapped %v", err, boom)
	}
}

// A corrupt or truncated undo.json must not brick the tool: loadUndo treats it
// as empty, and the next mutation can record over the garbage. Before this fix a
// single bad byte in undo.json made every mutating command abort (ENG-1).
func TestLoadUndoRecoversFromCorruptJournal(t *testing.T) {
	initGitRepo(t)

	writeStackFile(t, "main.txt", "main\n")
	mustStackGit(t, "add", "-A")
	mustStackGit(t, "commit", "-q", "-m", "main")

	path, err := undoPath()
	if err != nil {
		t.Fatalf("undoPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{ this is not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := loadUndo()
	if err != nil {
		t.Fatalf("loadUndo on corrupt journal returned error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("loadUndo on corrupt journal = %d entries, want 0", len(entries))
	}

	s := &State{Trunk: "main", Branches: map[string]*Branch{}}
	if _, err := s.RecordUndo(git.Shell{}, "after-corruption"); err != nil {
		t.Fatalf("RecordUndo after corruption: %v", err)
	}
	got, err := loadUndo()
	if err != nil {
		t.Fatalf("loadUndo after record: %v", err)
	}
	if len(got) != 1 || got[0].Label != "after-corruption" {
		t.Fatalf("journal after recovery = %+v, want one entry labeled after-corruption", got)
	}
}

// TestUndoProtocol drives the no-op/finalize protocol over the in-memory fake
// (the journal itself lives in a throwaway repo dir): tentative entries are
// dropped after no-ops, kept and trimmed after real changes, annotated with
// created branches, and preserved across an in-progress conflict.
func TestUndoProtocol(t *testing.T) {
	type protoEnv struct {
		f *fakeGit
		s *State
	}
	setup := func(t *testing.T) protoEnv {
		t.Helper()
		initGitRepo(t)
		f, s, env := newEnvState()
		mkBranch(t, env, s, f, "main", "a")
		if _, err := s.RecordUndo(f, "op"); err != nil {
			t.Fatalf("RecordUndo: %v", err)
		}
		return protoEnv{f, s}
	}
	journal := func(t *testing.T) []UndoEntry {
		t.Helper()
		entries, err := loadUndo()
		if err != nil {
			t.Fatalf("loadUndo: %v", err)
		}
		return entries
	}
	boom := errors.New("op failed")

	t.Run("failed op with nothing changed drops the entry", func(t *testing.T) {
		p := setup(t)
		if err := CleanupUndoOnError(p.f, p.s, boom); err != nil {
			t.Fatalf("CleanupUndoOnError: %v", err)
		}
		if got := journal(t); len(got) != 0 {
			t.Fatalf("journal = %d entries after no-op failure, want 0", len(got))
		}
	})

	t.Run("failed op that moved a ref keeps the entry", func(t *testing.T) {
		p := setup(t)
		mustCheckout(t, p.f, "a")
		p.f.commit("half-applied")
		if err := CleanupUndoOnError(p.f, p.s, boom); err != nil {
			t.Fatalf("CleanupUndoOnError: %v", err)
		}
		got := journal(t)
		if len(got) != 1 || got[0].Label != "op" {
			t.Fatalf("journal = %+v after real failure, want the kept entry", got)
		}
	})

	t.Run("successful no-op drops the entry", func(t *testing.T) {
		p := setup(t)
		entry, _, _ := PeekUndo()
		if err := FinalizeUndo(p.f, p.s, entry); err != nil {
			t.Fatalf("FinalizeUndo: %v", err)
		}
		if got := journal(t); len(got) != 0 {
			t.Fatalf("journal = %d entries after successful no-op, want 0", len(got))
		}
	})

	t.Run("success that created a branch records it", func(t *testing.T) {
		p := setup(t)
		entry, _, _ := PeekUndo()
		mustCheckout(t, p.f, "a")
		if err := p.f.CreateBranch("fresh"); err != nil {
			t.Fatal(err)
		}
		p.s.Track("fresh", "a", p.s.Branches["a"].ParentSHA)
		if err := FinalizeUndo(p.f, p.s, entry); err != nil {
			t.Fatalf("FinalizeUndo: %v", err)
		}
		got := journal(t)
		if len(got) != 1 {
			t.Fatalf("journal = %d entries, want 1", len(got))
		}
		if len(got[0].CreatedBranches) != 1 || got[0].CreatedBranches[0] != "fresh" {
			t.Fatalf("createdBranches = %v, want [fresh]", got[0].CreatedBranches)
		}
	})

	t.Run("conflict with a rebase in progress keeps the entry", func(t *testing.T) {
		p := setup(t)
		p.f.rebaseActive = true
		if err := CleanupUndoOnError(p.f, p.s, ErrConflict); err != nil {
			t.Fatalf("CleanupUndoOnError: %v", err)
		}
		got := journal(t)
		if len(got) != 1 || got[0].Label != "op" {
			t.Fatalf("journal = %+v after in-progress conflict, want the kept entry", got)
		}
	})
}

// writeUndo goes through the atomic temp+rename writer; it must not leave any
// .tmp turds behind in the stacked dir.
func TestWriteUndoLeavesNoTempFiles(t *testing.T) {
	initGitRepo(t)

	if err := writeUndo([]UndoEntry{{Label: "one"}}); err != nil {
		t.Fatalf("writeUndo: %v", err)
	}
	dir, err := stackedDir()
	if err != nil {
		t.Fatalf("stackedDir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read stacked dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover temp file after writeUndo: %s", e.Name())
		}
	}
}

// TestSetLastUndoAbsorbedRequiresActiveAbsorb pins the absorb checkpoint's
// precondition: the durable map lands only on an active absorb entry — an
// absent, malformed, empty, or wrong-label journal must error instead of
// silently claiming (or corrupting) durability.
func TestSetLastUndoAbsorbedRequiresActiveAbsorb(t *testing.T) {
	t.Run("absent journal errors and creates nothing", func(t *testing.T) {
		initGitRepo(t)
		if err := SetLastUndoAbsorbed(map[string]string{"a": "sha-a"}); err == nil {
			t.Fatal("SetLastUndoAbsorbed on an absent journal: want an error")
		}
		path, err := undoPath()
		if err != nil {
			t.Fatal(err)
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("undo.json appeared after a refused checkpoint (stat=%v)", statErr)
		}
	})

	t.Run("malformed journal errors and preserves raw bytes", func(t *testing.T) {
		initGitRepo(t)
		path, err := undoPath()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		garbage := []byte("{ this is not valid json")
		if err := os.WriteFile(path, garbage, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := SetLastUndoAbsorbed(map[string]string{"a": "sha-a"}); err == nil {
			t.Fatal("SetLastUndoAbsorbed on a malformed journal: want an error")
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(garbage) {
			t.Fatal("malformed journal rewritten — corruption recovery is loadUndo's job, not the setter's")
		}
	})

	t.Run("empty journal errors", func(t *testing.T) {
		initGitRepo(t)
		if err := writeUndo([]UndoEntry{}); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(mustUndoPath(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := SetLastUndoAbsorbed(map[string]string{"a": "sha-a"}); err == nil {
			t.Fatal("SetLastUndoAbsorbed on an empty journal: want an error")
		}
		after, err := os.ReadFile(mustUndoPath(t))
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatal("empty journal rewritten by a refused checkpoint")
		}
	})

	t.Run("wrong-label latest entry errors", func(t *testing.T) {
		initGitRepo(t)
		if err := writeUndo([]UndoEntry{{Label: "create"}, {Label: "modify"}}); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(mustUndoPath(t))
		if err != nil {
			t.Fatal(err)
		}
		err = SetLastUndoAbsorbed(map[string]string{"a": "sha-a"})
		if err == nil || !strings.Contains(err.Error(), `"modify"`) {
			t.Fatalf("SetLastUndoAbsorbed = %v, want an error naming the wrong label", err)
		}
		after, err := os.ReadFile(mustUndoPath(t))
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatal("journal rewritten by a refused checkpoint")
		}
	})

	t.Run("active absorb entry takes cumulative updates", func(t *testing.T) {
		initGitRepo(t)
		sha := func(c byte) string { return strings.Repeat(string(c), 40) }
		first := UndoEntry{Label: "create", Refs: map[string]string{"main": sha('f')}, CurrentBranch: "a"}
		absorb := UndoEntry{
			Label:           "absorb",
			State:           json.RawMessage(`{"version":1,"trunk":"main","branches":{}}`),
			Refs:            map[string]string{"a": sha('a')},
			CreatedBranches: []string{"b"},
		}
		if err := writeUndo([]UndoEntry{first, absorb}); err != nil {
			t.Fatal(err)
		}

		if err := SetLastUndoAbsorbed(map[string]string{"a": sha('1')}); err != nil {
			t.Fatalf("first checkpoint: %v", err)
		}
		if err := SetLastUndoAbsorbed(map[string]string{"a": sha('1'), "b": sha('2')}); err != nil {
			t.Fatalf("cumulative checkpoint: %v", err)
		}

		entries, err := loadUndo()
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 2 {
			t.Fatalf("journal = %d entries, want 2 (earlier entries preserved)", len(entries))
		}
		if entries[0].Label != first.Label || !reflect.DeepEqual(entries[0].Refs, first.Refs) ||
			entries[0].CurrentBranch != first.CurrentBranch || len(entries[0].AbsorbedCommits) != 0 {
			t.Fatalf("earlier entry = %+v, want its label/refs/checkout preserved and no absorbed map", entries[0])
		}
		got := entries[1]
		if !reflect.DeepEqual(got.AbsorbedCommits, map[string]string{"a": sha('1'), "b": sha('2')}) {
			t.Fatalf("AbsorbedCommits = %v, want the cumulative map", got.AbsorbedCommits)
		}
		var gotState, wantState map[string]any
		if err := json.Unmarshal(got.State, &gotState); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(absorb.State, &wantState); err != nil {
			t.Fatal(err)
		}
		if got.Label != "absorb" || !reflect.DeepEqual(gotState, wantState) ||
			!reflect.DeepEqual(got.Refs, absorb.Refs) ||
			!reflect.DeepEqual(got.CreatedBranches, absorb.CreatedBranches) {
			t.Fatalf("absorb entry = %+v, want snapshot/refs/created metadata preserved", got)
		}
	})
}

func mustUndoPath(t *testing.T) string {
	t.Helper()
	path, err := undoPath()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// Journal values that are not full commit ids are dropped at load like
// unparseable JSON: the file is user-writable, and a revision expression
// (HEAD~2) or the all-zeros delete value handed to update-ref would resolve
// to a commit undo never recorded. Healthy sibling entries survive.
func TestLoadUndoDropsMalformedRefValues(t *testing.T) {
	initGitRepo(t)
	writeStackFile(t, "main.txt", "main\n")
	mustStackGit(t, "add", "-A")
	mustStackGit(t, "commit", "-q", "-m", "main")
	tip := strings.TrimSpace(mustStackGit(t, "rev-parse", "HEAD"))
	zeros := strings.Repeat("0", 40)
	state := json.RawMessage(`{"version":1,"trunk":"main","branches":{}}`)

	cases := []struct {
		name   string
		poison func(*UndoEntry)
	}{
		{"revision expression", func(e *UndoEntry) { e.Refs["main"] = "HEAD~2" }},
		{"zero new value", func(e *UndoEntry) { e.Refs["main"] = zeros }},
		{"short value", func(e *UndoEntry) { e.Refs["main"] = "abc123" }},
		{"postRef revision", func(e *UndoEntry) { e.PostRefs = map[string]string{"main": "HEAD~1"} }},
		{"absorbed revision", func(e *UndoEntry) { e.AbsorbedCommits = map[string]string{"feat": "main^"} }},
		{"control-byte ref key", func(e *UndoEntry) { e.Refs["x\ny"] = tip }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			good := UndoEntry{Label: "good", State: state, Refs: map[string]string{"main": tip}}
			bad := UndoEntry{Label: "bad", State: state, Refs: map[string]string{"main": tip}}
			tc.poison(&bad)
			if err := writeUndo([]UndoEntry{good, bad}); err != nil {
				t.Fatal(err)
			}
			entries, err := loadUndo()
			if err != nil {
				t.Fatalf("loadUndo: %v", err)
			}
			if len(entries) != 1 || entries[0].Label != "good" {
				t.Fatalf("loadUndo kept %d entries (%+v), want only the healthy one", len(entries), entries)
			}
		})
	}
}

// A retained entry pins the post-operation tips undo's compare-and-swap
// restore verifies against: every recorded branch's live tip after the op,
// plus the tips of branches the op created.
func TestFinalizeUndoRecordsPostRefs(t *testing.T) {
	initGitRepo(t)
	writeStackFile(t, "main.txt", "main\n")
	mustStackGit(t, "add", "-A")
	mustStackGit(t, "commit", "-q", "-m", "main")
	mustStackGit(t, "checkout", "-q", "-b", "feat")

	mainTip := strings.TrimSpace(mustStackGit(t, "rev-parse", "main"))
	s := &State{Trunk: "main", Branches: map[string]*Branch{}}
	s.Track("feat", "main", mainTip)
	entry, err := s.RecordUndo(git.Shell{}, "op")
	if err != nil {
		t.Fatalf("RecordUndo: %v", err)
	}
	preTip := entry.Refs["feat"]

	// The op moves feat's tip and creates a branch.
	writeStackFile(t, "feat.txt", "feat\n")
	mustStackGit(t, "add", "-A")
	mustStackGit(t, "commit", "-q", "-m", "feat work")
	mustStackGit(t, "branch", "created-by-op")
	postTip := strings.TrimSpace(mustStackGit(t, "rev-parse", "feat"))

	if err := FinalizeUndo(git.Shell{}, s, entry); err != nil {
		t.Fatalf("FinalizeUndo: %v", err)
	}
	if entry.PostRefs["feat"] != postTip || entry.PostRefs["created-by-op"] != postTip {
		t.Fatalf("in-memory postRefs = %v, want feat and created-by-op at %s", entry.PostRefs, postTip)
	}
	entries, err := loadUndo()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("journal = %d entries, want 1", len(entries))
	}
	got := entries[0]
	if got.Refs["feat"] != preTip {
		t.Fatalf("refs[feat] = %s, want the pre-op tip %s", got.Refs["feat"], preTip)
	}
	if got.PostRefs["feat"] != postTip || got.PostRefs["created-by-op"] != postTip {
		t.Fatalf("postRefs = %v, want both branches pinned at %s", got.PostRefs, postTip)
	}
}

// FinalizeUndo used to pay three journal round-trips after the op — one per
// annotation setter plus the trim — each a load + marshal + atomic write +
// fsync. The annotations are bookkeeping, not crash boundaries, so the whole
// closeout must land in ONE write.
func TestFinalizeUndoWritesJournalOnce(t *testing.T) {
	f, s, _ := setupCountingUndoCloseout(t)
	mustCheckout(t, f, "a")
	if err := f.CreateBranch("fresh"); err != nil {
		t.Fatal(err)
	}
	s.Track("fresh", "a", s.Branches["a"].ParentSHA)

	writes := 0
	orig := writeUndo
	writeUndo = func(entries []UndoEntry) error {
		writes++
		return orig(entries)
	}
	t.Cleanup(func() { writeUndo = orig })

	entry, ok, err := PeekUndo()
	if err != nil || !ok {
		t.Fatalf("PeekUndo: ok=%v err=%v", ok, err)
	}
	if err := FinalizeUndo(f, s, entry); err != nil {
		t.Fatalf("FinalizeUndo: %v", err)
	}
	if writes != 1 {
		t.Fatalf("journal writes during FinalizeUndo = %d, want 1", writes)
	}
	entries, err := loadUndo()
	if err != nil {
		t.Fatalf("loadUndo: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("journal = %d entries, want 1 retained", len(entries))
	}
	got := entries[0]
	if len(got.CreatedBranches) != 1 || got.CreatedBranches[0] != "fresh" {
		t.Fatalf("createdBranches = %v, want [fresh]", got.CreatedBranches)
	}
	if got.PostRefs == nil {
		t.Fatal("postRefs was not annotated in the retained entry")
	}
}
