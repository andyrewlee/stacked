package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

type mockGHCheck struct {
	Status     string `json:"status,omitempty"`
	State      string `json:"state,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
}

type mockGHPR struct {
	Number            int           `json:"number"`
	URL               string        `json:"url"`
	Title             string        `json:"title"`
	Body              string        `json:"body"`
	State             string        `json:"state"`
	IsDraft           bool          `json:"isDraft"`
	HeadRefName       string        `json:"headRefName"`
	BaseRefName       string        `json:"baseRefName"`
	StatusCheckRollup []mockGHCheck `json:"statusCheckRollup,omitempty"`
}

type mockGHState struct {
	NextNumber int        `json:"nextNumber"`
	Open       []mockGHPR `json:"open"`
	Merged     []mockGHPR `json:"merged"`
	Calls      []string   `json:"calls"`
}

func runMockGHProcess() int {
	statePath := os.Getenv("ST_MOCK_GH_STATE")
	var args []string
	for i, a := range os.Args {
		if a == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	var st mockGHState
	if data, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	if st.NextNumber == 0 {
		st.NextNumber = 1
	}
	save := func() {
		raw, _ := json.MarshalIndent(st, "", "  ")
		_ = os.WriteFile(statePath, raw, 0o644)
	}
	st.Calls = append(st.Calls, strings.Join(args, " "))
	if len(args) < 2 || args[0] != "pr" {
		save()
		return 1
	}
	switch args[1] {
	case "list":
		stateFilter := "open"
		for i := 2; i < len(args)-1; i++ {
			if args[i] == "--state" {
				stateFilter = args[i+1]
			}
		}
		list := st.Open
		if stateFilter == "merged" {
			list = st.Merged
		}
		if list == nil {
			list = []mockGHPR{}
		}
		save()
		raw, _ := json.Marshal(list)
		fmt.Fprintln(os.Stdout, string(raw))
		return 0
	case "create":
		var head, base, title string
		var draft bool
		for i := 2; i < len(args); i++ {
			switch args[i] {
			case "--head":
				i++
				head = args[i]
			case "--base":
				i++
				base = args[i]
			case "--title":
				i++
				title = args[i]
			case "--draft":
				draft = true
			case "--body-file":
				i++
			}
		}
		bodyBytes, _ := io.ReadAll(os.Stdin)
		num := st.NextNumber
		st.NextNumber++
		url := fmt.Sprintf("https://github.com/acme/widgets/pull/%d", num)
		st.Open = append(st.Open, mockGHPR{
			Number:            num,
			URL:               url,
			Title:             title,
			Body:              string(bodyBytes),
			State:             "OPEN",
			IsDraft:           draft,
			HeadRefName:       head,
			BaseRefName:       base,
			StatusCheckRollup: []mockGHCheck{{Conclusion: "SUCCESS"}},
		})
		save()
		fmt.Fprintln(os.Stdout, url)
		return 0
	case "edit":
		num, err := strconv.Atoi(args[2])
		if err != nil {
			save()
			return 1
		}
		for i := range st.Open {
			if st.Open[i].Number == num {
				for j := 3; j < len(args); j++ {
					switch args[j] {
					case "--base":
						j++
						st.Open[i].BaseRefName = args[j]
					case "--body-file":
						j++
						bodyBytes, _ := io.ReadAll(os.Stdin)
						st.Open[i].Body = string(bodyBytes)
					}
				}
				save()
				return 0
			}
		}
		save()
		return 1
	}
	save()
	return 1
}

// TestGitHubStackedPRJourney drives the full native GitHub stack lifecycle
// through the real compiled `st` binary with a stateful mock `gh`:
//  1. Create a 3-PR stack (`feat-a -> feat-b -> feat-c`).
//  2. `st submit --json` pushes all 3, creates PRs #1, #2, #3 bottom-up, and
//     maintains `<!-- st:stack:start -->` blocks in every PR body.
//  3. `st log --pr --json` and `st status --pr --json` surface PR numbers,
//     check rollups, and commit lists.
//  4. Simulate PR #1 squash-merged on GitHub while an untracked `plans/note.md`
//     sits in the working tree.
//  5. `st sync --submit --json` prunes `feat-a`, advances HEAD to `feat-b`,
//     restacks `feat-b` & `feat-c`, retargets PR #2 to `main` BEFORE pushing,
//     force-pushes both, refreshes stack blocks, and leaves `plans/note.md`
//     untouched.
func TestGitHubStackedPRJourney(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell wrapper for mock gh")
	}

	r := newRepo(t)
	bare := filepath.Join(t.TempDir(), "remote.git")
	r.initBare(bare)
	r.git("remote", "add", "origin", bare)
	r.git("push", "-q", "-u", "origin", "main")

	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "add a")
	r.create("feat-b", "b.txt", "b\n", "add b")
	r.create("feat-c", "c.txt", "c\n", "add c")

	mockDir := t.TempDir()
	statePath := filepath.Join(mockDir, "gh.json")
	if err := os.WriteFile(statePath, []byte(`{"nextNumber":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(mockDir, "mock-gh")
	script := fmt.Sprintf("#!/bin/sh\nST_MOCK_GH_HELPER=1 ST_MOCK_GH_STATE=%q exec %q -- \"$@\"\n", statePath, os.Args[0])
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ghEnv := []string{"ST_GH_BIN=" + binPath, "ST_GH_FORCE=1"}

	// 1. Submit the 3-PR stack.
	subRes := r.stInEnv(r.dir, ghEnv, "submit", "--json")
	wantExit(t, subRes, 0)
	var sub struct {
		Pushed []string `json:"pushed"`
		PRs    []struct {
			Branch string `json:"branch"`
			Base   string `json:"base"`
			Number int    `json:"number"`
			Action string `json:"action"`
		} `json:"prs"`
	}
	if err := json.Unmarshal([]byte(subRes.stdout), &sub); err != nil {
		t.Fatalf("decode submit --json: %v\n%s", err, subRes.stdout)
	}
	if len(sub.Pushed) != 3 || len(sub.PRs) != 3 {
		t.Fatalf("submit result = %+v, want 3 pushed and 3 PRs", sub)
	}
	if sub.PRs[0].Number != 1 || sub.PRs[0].Base != "main" ||
		sub.PRs[1].Number != 2 || sub.PRs[1].Base != "feat-a" ||
		sub.PRs[2].Number != 3 || sub.PRs[2].Base != "feat-b" {
		t.Fatalf("created PRs unexpected: %+v", sub.PRs)
	}

	// 2. Inspect with `st log --pr --json` and `st status --pr --json`.
	logRes := r.stInEnv(r.dir, ghEnv, "log", "--pr", "--json")
	wantExit(t, logRes, 0)
	if !strings.Contains(logRes.stdout, `"number": 3`) || !strings.Contains(logRes.stdout, `"checks": "passing"`) {
		t.Fatalf("log --pr --json missing PR #3 / passing checks:\n%s", logRes.stdout)
	}

	statusRes := r.stInEnv(r.dir, ghEnv, "status", "--pr", "--json")
	wantExit(t, statusRes, 0)
	if !strings.Contains(statusRes.stdout, `"number": 3`) || !strings.Contains(statusRes.stdout, `"subject": "add c"`) {
		t.Fatalf("status --pr --json missing PR #3 / commit:\n%s", statusRes.stdout)
	}

	// 3. Simulate PR #1 merged on GitHub, trunk advanced, and untracked plan file present.
	r.stOK("checkout", "main")
	preTrunk := r.rev("main")
	r.git("merge", "-q", "--squash", "feat-a")
	r.git("commit", "-q", "-m", "add a (#1)")
	r.git("push", "-q", "origin", "main")
	r.git("reset", "-q", "--hard", preTrunk)
	r.stOK("checkout", "feat-a")

	var ghState mockGHState
	rawState, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawState, &ghState); err != nil {
		t.Fatal(err)
	}
	mergedPR := ghState.Open[0]
	mergedPR.State = "MERGED"
	ghState.Merged = append(ghState.Merged, mergedPR)
	ghState.Open = ghState.Open[1:]
	rawState, _ = json.MarshalIndent(ghState, "", "  ")
	if err := os.WriteFile(statePath, rawState, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(r.dir, "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.writeFile(filepath.Join("plans", "note.md"), "agent scratch plan\n")

	// 4. `st sync --submit --json` handles the whole post-merge drill in one shot!
	syncRes := r.stInEnv(r.dir, ghEnv, "sync", "--submit", "--json")
	wantExit(t, syncRes, 0)
	var synced struct {
		Deleted []string `json:"deleted"`
		Pushed  []string `json:"pushed"`
		PRs     []struct {
			Branch string `json:"branch"`
			Base   string `json:"base"`
			Number int    `json:"number"`
			Action string `json:"action"`
		} `json:"prs"`
	}
	if err := json.Unmarshal([]byte(syncRes.stdout), &synced); err != nil {
		t.Fatalf("decode sync --submit --json: %v\n%s", err, syncRes.stdout)
	}
	if len(synced.Deleted) != 1 || synced.Deleted[0] != "feat-a" {
		t.Fatalf("deleted = %v, want [feat-a]", synced.Deleted)
	}
	if len(synced.Pushed) != 2 || synced.Pushed[0] != "feat-b" || synced.Pushed[1] != "feat-c" {
		t.Fatalf("pushed = %v, want [feat-b feat-c]", synced.Pushed)
	}
	if len(synced.PRs) != 2 || synced.PRs[0].Number != 2 || synced.PRs[0].Base != "main" {
		t.Fatalf("prs = %+v, want PR #2 retargeted to main", synced.PRs)
	}
	if cur := r.currentBranch(); cur != "feat-b" {
		t.Fatalf("HEAD = %q after sync, want feat-b", cur)
	}
	if r.branchExists("feat-a") {
		t.Fatal("feat-a should be pruned")
	}

	// Verify mock GH state: PR #2 base is main, stack block no longer lists #1.
	rawState, _ = os.ReadFile(statePath)
	ghState = mockGHState{}
	_ = json.Unmarshal(rawState, &ghState)
	if ghState.Open[0].BaseRefName != "main" {
		t.Fatalf("PR #2 base = %q, want main", ghState.Open[0].BaseRefName)
	}
	if strings.Contains(ghState.Open[0].Body, "feat-a") || !strings.Contains(ghState.Open[0].Body, "#2") || !strings.Contains(ghState.Open[0].Body, "#3") {
		t.Fatalf("PR #2 stack block not refreshed cleanly:\n%s", ghState.Open[0].Body)
	}

	// Untracked plan file survived untouched and untracked.
	if b, err := os.ReadFile(filepath.Join(r.dir, "plans", "note.md")); err != nil || string(b) != "agent scratch plan\n" {
		t.Fatalf("plans/note.md = %q err %v, want untouched", b, err)
	}
	if r.fileOnBranch("feat-b", "plans/note.md") {
		t.Fatal("untracked plans/note.md leaked into feat-b commit")
	}
	r.stOK("doctor")
}
