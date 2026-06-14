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

	"stacked/internal/git"
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
	if err := s.RecordUndo(git.Shell{}, "snapshot"); err != nil {
		t.Fatalf("RecordUndo: %v", err)
	}

	entry, ok, err := PopUndo()
	if err != nil {
		t.Fatalf("PopUndo: %v", err)
	}
	if !ok {
		t.Fatal("PopUndo returned no undo entry")
	}
	if got := entry.Refs["feature"]; got != branchSHA {
		t.Fatalf("undo ref for feature = %q, want branch tip %q", got, branchSHA)
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

	entry, err := s.SnapshotUndo(f, "test-op")
	if err != nil {
		t.Fatalf("SnapshotUndo: %v", err)
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
	if _, err := s.SnapshotUndo(counting, "test-op"); err != nil {
		t.Fatalf("SnapshotUndo: %v", err)
	}
	if counting.revParseCalls != 0 {
		t.Fatalf("RevParse calls = %d, want 0", counting.revParseCalls)
	}
	if counting.tipsCalls != 1 {
		t.Fatalf("Tips calls = %d, want 1", counting.tipsCalls)
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

	if _, err := s.SnapshotUndo(g, "op"); !errors.Is(err, boom) {
		t.Fatalf("SnapshotUndo with failing Tips = %v, want wrapped %v", err, boom)
	}
	if err := s.RecordUndo(g, "op"); !errors.Is(err, boom) {
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
	if err := s.RecordUndo(git.Shell{}, "after-corruption"); err != nil {
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
		if err := s.RecordUndo(f, "op"); err != nil {
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
