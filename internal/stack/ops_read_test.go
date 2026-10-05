package stack

import (
	"strings"
	"testing"
)

func TestCommitsPlanListsRecordedRange(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	mkBranch(t, env, s, f, "feat-a", "feat-b")
	f.commit("c-feat-b-2")
	b, _ := s.Get("feat-b")
	b.ParentSHA = mustParentSHA(t, f, "feat-a") // record feat-a's tip as the base

	res, err := CommitsPlan(env, s, "feat-b")
	if err != nil {
		t.Fatalf("CommitsPlan: %v", err)
	}
	if res.Branch != "feat-b" || res.ParentSHA != b.ParentSHA {
		t.Fatalf("CommitsResult = %+v, want branch feat-b with its recorded base", res)
	}
	if len(res.Commits) != 2 || res.Commits[0].Subject != "c-feat-b-2" || res.Commits[1].Subject != "c-feat-b" {
		t.Fatalf("commits = %+v, want newest-first [c-feat-b-2 c-feat-b]", res.Commits)
	}
	if res.Commits[0].SHA != f.branches["feat-b"] {
		t.Fatalf("newest SHA = %q, want feat-b tip %q", res.Commits[0].SHA, f.branches["feat-b"])
	}
}

func TestCommitsPlanDefaultsToCurrent(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	b, _ := s.Get("feat-a")
	b.ParentSHA = f.branches["main"]

	res, err := CommitsPlan(env, s, "")
	if err != nil {
		t.Fatalf("CommitsPlan: %v", err)
	}
	if res.Branch != "feat-a" || len(res.Commits) != 1 {
		t.Fatalf("CommitsResult = %+v, want feat-a's single commit", res)
	}
}

func TestCommitsPlanRefusesUntrackedAndTrunk(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")

	if _, err := CommitsPlan(env, s, "loose"); err == nil || !strings.Contains(err.Error(), "not tracked") {
		t.Fatalf("untracked CommitsPlan err = %v, want the not-tracked refusal", err)
	}
	if _, err := CommitsPlan(env, s, "main"); err == nil || !strings.Contains(err.Error(), "trunk") {
		t.Fatalf("trunk CommitsPlan err = %v, want the trunk refusal", err)
	}
}

func TestCommitsPlanEmptyRange(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	b, _ := s.Get("feat-a")
	b.ParentSHA = f.branches["feat-a"] // recorded base == tip: nothing in range

	res, err := CommitsPlan(env, s, "feat-a")
	if err != nil {
		t.Fatalf("CommitsPlan: %v", err)
	}
	if res.Commits == nil || len(res.Commits) != 0 {
		t.Fatalf("commits = %v, want present-but-empty", res.Commits)
	}
}

// mustParentSHA returns a branch's live tip for tests that record it as the
// stack base.
func mustParentSHA(t *testing.T, f *fakeGit, name string) string {
	t.Helper()
	tip, ok := f.branches[name]
	if !ok {
		t.Fatalf("fake has no branch %q", name)
	}
	return tip
}
