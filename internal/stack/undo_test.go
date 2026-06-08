package stack

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	if err := s.RecordUndo("snapshot"); err != nil {
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
	if err := s.RecordUndo("after-corruption"); err != nil {
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
