package stack

import (
	"os"
	"os/exec"
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
