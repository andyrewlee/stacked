package cmd

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

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

// --- stateful mock gh process (re-execs the test binary via ST_GH_BIN) ------

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
	ReviewDecision    string        `json:"reviewDecision,omitempty"`
	StatusCheckRollup []mockGHCheck `json:"statusCheckRollup,omitempty"`
}

type mockGHState struct {
	NextNumber int        `json:"nextNumber"`
	Open       []mockGHPR `json:"open"`
	Merged     []mockGHPR `json:"merged"`
	Calls      []string   `json:"calls"`
	FailList   string     `json:"failList,omitempty"`
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
		fmt.Fprintf(os.Stderr, "mock gh: unsupported args %v\n", args)
		save()
		return 1
	}

	switch args[1] {
	case "list":
		if st.FailList != "" {
			fmt.Fprintln(os.Stderr, st.FailList)
			save()
			return 1
		}
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
			Number:      num,
			URL:         url,
			Title:       title,
			Body:        string(bodyBytes),
			State:       "OPEN",
			IsDraft:     draft,
			HeadRefName: head,
			BaseRefName: base,
		})
		save()
		fmt.Fprintln(os.Stdout, url)
		return 0

	case "edit":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "mock gh: edit missing number")
			save()
			return 1
		}
		num, err := strconv.Atoi(args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "mock gh: bad PR number %q\n", args[2])
			save()
			return 1
		}
		idx := -1
		for i := range st.Open {
			if st.Open[i].Number == num {
				idx = i
				break
			}
		}
		if idx < 0 {
			fmt.Fprintf(os.Stderr, "mock gh: PR #%d not found\n", num)
			save()
			return 1
		}
		for i := 3; i < len(args); i++ {
			switch args[i] {
			case "--base":
				i++
				st.Open[idx].BaseRefName = args[i]
			case "--body-file":
				i++
				bodyBytes, _ := io.ReadAll(os.Stdin)
				st.Open[idx].Body = string(bodyBytes)
			}
		}
		save()
		return 0
	}

	fmt.Fprintf(os.Stderr, "mock gh: unknown subcommand %v\n", args)
	save()
	return 1
}

type mockGH struct {
	t         *testing.T
	statePath string
	binPath   string
}

func installMockGH(t *testing.T, initial mockGHState) *mockGH {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell wrapper for mock gh")
	}
	dir := t.TempDir()
	statePath := filepath.Join(dir, "gh-state.json")
	if initial.NextNumber == 0 {
		initial.NextNumber = 1
	}
	raw, err := json.MarshalIndent(initial, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(dir, "mock-gh")
	script := fmt.Sprintf("#!/bin/sh\nST_MOCK_GH_HELPER=1 ST_MOCK_GH_STATE=%q exec %q -- \"$@\"\n", statePath, os.Args[0])
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ST_GH_BIN", binPath)
	t.Setenv("ST_GH_FORCE", "1")
	return &mockGH{t: t, statePath: statePath, binPath: binPath}
}

func (m *mockGH) load() mockGHState {
	m.t.Helper()
	raw, err := os.ReadFile(m.statePath)
	if err != nil {
		m.t.Fatalf("read mock gh state: %v", err)
	}
	var st mockGHState
	if err := json.Unmarshal(raw, &st); err != nil {
		m.t.Fatalf("decode mock gh state: %v", err)
	}
	return st
}

func (m *mockGH) save(st mockGHState) {
	m.t.Helper()
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		m.t.Fatalf("encode mock gh state: %v", err)
	}
	if err := os.WriteFile(m.statePath, raw, 0o644); err != nil {
		m.t.Fatalf("write mock gh state: %v", err)
	}
}

func initBareOrigin(t *testing.T) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "origin.git")
	mustRun(t, "git", "init", "-q", "--bare", bare)
	mustRun(t, "git", "--git-dir", bare, "symbolic-ref", "HEAD", "refs/heads/main")
	mustRun(t, "git", "remote", "add", "origin", bare)
	mustRun(t, "git", "push", "-q", "-u", "origin", "main")
	return bare
}

// --- tests ------------------------------------------------------------------

func TestSubmitGitHubFullLifecycle(t *testing.T) {
	newRepo(t)
	initBareOrigin(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "add a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "add b")

	m := installMockGH(t, mockGHState{})

	// 1. Dry-run previews creating both PRs without mutating mock GH state.
	dryOut := captureStdout(t, func() {
		if err := runSubmit([]string{"--draft", "--dry-run"}); err != nil {
			t.Fatalf("submit --draft --dry-run: %v", err)
		}
	})
	if !strings.Contains(dryOut, "would_create") || !strings.Contains(dryOut, "feat-a -> main") {
		t.Fatalf("dry-run text missing PR plan:\n%s", dryOut)
	}
	if len(m.load().Open) != 0 {
		t.Fatal("dry-run created PRs on mock gh")
	}

	// 2. First submit with --draft --json creates PR #1 and #2 bottom-up and
	// populates stack blocks in both bodies.
	out := captureStdout(t, func() {
		if err := runSubmit([]string{"--draft", "--json"}); err != nil {
			t.Fatalf("submit --draft --json: %v", err)
		}
	})
	var res submitResult
	decodeStrictJSON(t, "submit --draft --json", out, &res)
	if len(res.Pushed) != 2 || len(res.PRs) != 2 {
		t.Fatalf("submit result = %+v, want 2 pushed + 2 prs", res)
	}
	if res.PRs[0].Action != "created" || res.PRs[0].Number != 1 || res.PRs[0].Base != "main" || !res.PRs[0].Draft {
		t.Fatalf("PR #1 outcome = %+v", res.PRs[0])
	}
	if res.PRs[1].Action != "created" || res.PRs[1].Number != 2 || res.PRs[1].Base != "feat-a" || !res.PRs[1].Draft {
		t.Fatalf("PR #2 outcome = %+v", res.PRs[1])
	}

	st := m.load()
	if len(st.Open) != 2 {
		t.Fatalf("mock gh open PRs = %d, want 2", len(st.Open))
	}
	for _, pr := range st.Open {
		if !strings.Contains(pr.Body, stack.StackBlockStart) || !strings.Contains(pr.Body, "#1") || !strings.Contains(pr.Body, "#2") {
			t.Fatalf("PR #%d body missing stack block:\n%s", pr.Number, pr.Body)
		}
	}

	// 3. Re-submit in text mode is idempotent: no new edits, renders PR summary.
	callsBefore := len(m.load().Calls)
	textOut := captureStdout(t, func() {
		if err := runSubmit(nil); err != nil {
			t.Fatalf("re-submit: %v", err)
		}
	})
	if !strings.Contains(textOut, "pull requests:") || !strings.Contains(textOut, "#1 feat-a -> main (unchanged)") {
		t.Fatalf("re-submit text unexpected:\n%s", textOut)
	}
	for _, call := range m.load().Calls[callsBefore:] {
		if strings.HasPrefix(call, "pr edit") || strings.HasPrefix(call, "pr create") {
			t.Fatalf("idempotent re-submit unexpectedly called %q", call)
		}
	}

	// 4. Reparent feat-b onto main and re-submit: PR #2 must be retargeted to
	// main and its body updated.
	if err := runOnto([]string{"main"}); err != nil {
		t.Fatalf("onto main: %v", err)
	}
	out = captureStdout(t, func() {
		if err := runSubmit([]string{"--json"}); err != nil {
			t.Fatalf("submit after onto: %v", err)
		}
	})
	var afterOnto submitResult
	decodeStrictJSON(t, "submit after onto", out, &afterOnto)
	if len(afterOnto.PRs) != 1 || afterOnto.PRs[0].Base != "main" || !strings.Contains(afterOnto.PRs[0].Action, "retargeted") {
		t.Fatalf("after onto PR outcome = %+v, want retargeted onto main", afterOnto.PRs)
	}
	st = m.load()
	if st.Open[1].BaseRefName != "main" {
		t.Fatalf("PR #2 base = %q, want main", st.Open[1].BaseRefName)
	}

	// 5. Validation guards: conflicting flags and unavailable gh.
	if err := runSubmit([]string{"--no-pr", "--draft"}); err == nil {
		t.Fatal("expected --no-pr + --draft to fail")
	}
	t.Setenv("ST_GH_FORCE", "0")
	if err := runSubmit([]string{"--draft"}); err == nil || !strings.Contains(err.Error(), "not a GitHub remote") {
		t.Fatalf("expected non-GitHub remote refusal for --draft, got %v", err)
	}
	t.Setenv("ST_GH_BIN", filepath.Join(t.TempDir(), "missing-gh"))
	if err := runSubmit([]string{"--draft"}); err == nil || !strings.Contains(err.Error(), "require the GitHub CLI") {
		t.Fatalf("expected missing gh refusal for --draft, got %v", err)
	}
}

func TestSyncGitHubMergedAndSubmitFlag(t *testing.T) {
	newRepo(t)
	initBareOrigin(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "add a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "add b")

	m := installMockGH(t, mockGHState{})
	if err := runSubmit(nil); err != nil {
		t.Fatalf("initial submit: %v", err)
	}

	// Advance origin/main with an unrelated commit (simulating a rewritten
	// squash-merge that git ancestry alone wouldn't see) and mark PR #1 merged
	// on mock GitHub.
	mustCheckout(t, "main")
	write(t, "trunk-only.txt", "t\n")
	mustRun(t, "git", "add", "trunk-only.txt")
	mustRun(t, "git", "commit", "-q", "-m", "squash-merged feat-a rewritten")
	mustRun(t, "git", "push", "-q", "origin", "main")
	mustRun(t, "git", "reset", "-q", "--hard", "HEAD~1")
	mustCheckout(t, "feat-a")

	st := m.load()
	pr1 := st.Open[0]
	pr1.State = "MERGED"
	st.Merged = append(st.Merged, pr1)
	st.Open = st.Open[1:]
	m.save(st)

	// Place an untracked plan file in the working tree: sync must NOT block on
	// it and must preserve it intact.
	if err := os.MkdirAll("plans", 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join("plans", "notes.md"), "keep me\n")

	// Dry-run sees feat-a via ExtraMerged from GitHub.
	dryOut := captureStdout(t, func() {
		if err := runSync([]string{"--dry-run", "--json"}); err != nil {
			t.Fatalf("sync --dry-run --json: %v", err)
		}
	})
	if !strings.Contains(dryOut, `"feat-a"`) {
		t.Fatalf("sync --dry-run missed GitHub-merged feat-a:\n%s", dryOut)
	}

	// Real sync --submit --json prunes feat-a, switches HEAD to feat-b,
	// restacks feat-b, retargets PR #2 -> main, and force-pushes feat-b.
	out := captureStdout(t, func() {
		if err := runSync([]string{"--submit", "--json"}); err != nil {
			t.Fatalf("sync --submit --json: %v", err)
		}
	})
	var combined syncSubmitResult
	decodeStrictJSON(t, "sync --submit --json", out, &combined)
	if len(combined.Deleted) != 1 || combined.Deleted[0] != "feat-a" {
		t.Fatalf("deleted = %v, want [feat-a]", combined.Deleted)
	}
	if len(combined.Pushed) != 1 || combined.Pushed[0] != "feat-b" {
		t.Fatalf("pushed = %v, want [feat-b]", combined.Pushed)
	}
	if len(combined.PRs) != 1 || combined.PRs[0].Number != 2 || combined.PRs[0].Base != "main" {
		t.Fatalf("prs = %+v, want PR #2 retargeted to main", combined.PRs)
	}
	if cur := curBranch(t); cur != "feat-b" {
		t.Fatalf("HEAD = %q, want feat-b", cur)
	}
	if got, err := os.ReadFile(filepath.Join("plans", "notes.md")); err != nil || string(got) != "keep me\n" {
		t.Fatalf("untracked plans/notes.md = %q err %v, want untouched", got, err)
	}

	// Also exercise text mode `sync --submit` and graceful fallback when gh list fails.
	st = m.load()
	st.FailList = "simulated offline"
	m.save(st)
	fallbackOut := captureStdout(t, func() {
		if err := runSync(nil); err != nil {
			t.Fatalf("sync with failing gh list should degrade gracefully: %v", err)
		}
	})
	if !strings.Contains(fallbackOut, "github merged-PR check skipped") {
		t.Fatalf("expected graceful fallback note, got:\n%s", fallbackOut)
	}
	st.FailList = ""
	m.save(st)
	textOut := captureStdout(t, func() {
		if err := runSync([]string{"-s"}); err != nil {
			t.Fatalf("sync -s text: %v", err)
		}
	})
	if !strings.Contains(textOut, "submitted 1 branch(es)") || !strings.Contains(textOut, "pull requests:") {
		t.Fatalf("sync -s text unexpected:\n%s", textOut)
	}
}

func TestLogAndStatusPRScopingAndTrunkBehind(t *testing.T) {
	newRepo(t)
	initBareOrigin(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "add a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "add b")
	mustCheckout(t, "main")
	mustCreate(t, "sibling", "s.txt", "s\n", "add sibling")

	// Move origin/main ahead of local main by 1 commit so trunkBehind = 1.
	mustCheckout(t, "main")
	write(t, "ahead.txt", "ahead\n")
	mustRun(t, "git", "add", "ahead.txt")
	mustRun(t, "git", "commit", "-q", "-m", "remote trunk commit")
	mustRun(t, "git", "push", "-q", "origin", "main")
	mustRun(t, "git", "reset", "-q", "--hard", "HEAD~1")

	mustCheckout(t, "feat-b")

	// Default `st log` from feat-b scopes to current stack (omits sibling) and
	// reports trunkBehind without network calls.
	scoped := captureStdout(t, func() {
		if err := runLog(nil); err != nil {
			t.Fatalf("log: %v", err)
		}
	})
	if strings.Contains(scoped, "sibling") {
		t.Fatalf("scoped log leaked unrelated sibling stack:\n%s", scoped)
	}
	if !strings.Contains(scoped, "1 behind origin/main") {
		t.Fatalf("log missing trunkBehind badge:\n%s", scoped)
	}

	// `st log --all` includes sibling.
	allOut := captureStdout(t, func() {
		if err := runLog([]string{"--all"}); err != nil {
			t.Fatalf("log --all: %v", err)
		}
	})
	if !strings.Contains(allOut, "sibling") {
		t.Fatalf("log --all missing sibling stack:\n%s", allOut)
	}

	// Install mock GH with draft+passing PR #10 and failing+mismatched PR #11,
	// plus pending PR #12 on sibling.
	installMockGH(t, mockGHState{
		Open: []mockGHPR{
			{
				Number: 10, URL: "https://github.com/acme/widgets/pull/10",
				State: "OPEN", IsDraft: true, HeadRefName: "feat-a", BaseRefName: "main",
				StatusCheckRollup: []mockGHCheck{{Conclusion: "SUCCESS"}},
			},
			{
				Number: 11, URL: "https://github.com/acme/widgets/pull/11",
				State: "OPEN", HeadRefName: "feat-b", BaseRefName: "wrong-base",
				StatusCheckRollup: []mockGHCheck{{Conclusion: "FAILURE"}},
			},
			{
				Number: 12, URL: "https://github.com/acme/widgets/pull/12",
				State: "OPEN", HeadRefName: "sibling", BaseRefName: "main",
				StatusCheckRollup: []mockGHCheck{{Status: "IN_PROGRESS"}},
			},
		},
	})

	logPRText := captureStdout(t, func() {
		if err := runLog([]string{"--all", "--pr"}); err != nil {
			t.Fatalf("log --all --pr: %v", err)
		}
	})
	for _, want := range []string{"[#10 draft ✓]", "[#11 ✗ base:wrong-base]", "[#12 ⋯]"} {
		if !strings.Contains(logPRText, want) {
			t.Fatalf("log --all --pr missing %q:\n%s", want, logPRText)
		}
	}

	logPRJSON := captureStdout(t, func() {
		if err := runLog([]string{"--pr", "--json"}); err != nil {
			t.Fatalf("log --pr --json: %v", err)
		}
	})
	var root logNode
	if err := json.Unmarshal([]byte(logPRJSON), &root); err != nil {
		t.Fatalf("decode log --pr --json: %v\n%s", err, logPRJSON)
	}
	if root.TrunkBehind != 1 {
		t.Fatalf("log trunkBehind = %d, want 1", root.TrunkBehind)
	}
	if len(root.Children) != 1 || root.Children[0].PR == nil || root.Children[0].PR.Number != 10 {
		t.Fatalf("feat-a PR missing in log --pr --json: %+v", root.Children)
	}
	fb := root.Children[0].Children[0]
	if fb.PR == nil || fb.PR.Number != 11 || !fb.PR.BaseMismatch || fb.PR.Checks != "failing" {
		t.Fatalf("feat-b PR = %+v, want #11 failing baseMismatch", fb.PR)
	}

	statusPRText := captureStdout(t, func() {
		if err := runStatus([]string{"--pr"}); err != nil {
			t.Fatalf("status --pr: %v", err)
		}
	})
	for _, want := range []string{"1 commit(s) behind origin/main", "[#11 ✗ base:wrong-base]", "https://github.com/acme/widgets/pull/11"} {
		if !strings.Contains(statusPRText, want) {
			t.Fatalf("status --pr missing %q:\n%s", want, statusPRText)
		}
	}

	statusPRJSON := captureStdout(t, func() {
		if err := runStatus([]string{"--pr", "--json"}); err != nil {
			t.Fatalf("status --pr --json: %v", err)
		}
	})
	var sj struct {
		TrunkBehind int              `json:"trunkBehind"`
		Commits     []git.CommitInfo `json:"commits"`
		PR          *prStatus        `json:"pr"`
	}
	if err := json.Unmarshal([]byte(statusPRJSON), &sj); err != nil {
		t.Fatalf("decode status --pr --json: %v\n%s", err, statusPRJSON)
	}
	if sj.TrunkBehind != 1 || len(sj.Commits) != 1 || sj.Commits[0].Subject != "add b" || sj.PR == nil || sj.PR.Number != 11 {
		t.Fatalf("status --pr --json = %+v", sj)
	}

	// Without gh available, --pr errors cleanly.
	t.Setenv("ST_GH_BIN", filepath.Join(t.TempDir(), "no-gh"))
	if err := runLog([]string{"--pr"}); err == nil || !strings.Contains(err.Error(), "requires the GitHub CLI") {
		t.Fatalf("log --pr without gh = %v, want missing gh error", err)
	}
	if err := runStatus([]string{"--pr"}); err == nil || !strings.Contains(err.Error(), "requires the GitHub CLI") {
		t.Fatalf("status --pr without gh = %v, want missing gh error", err)
	}
}

func TestUntrackedFilesToleranceAndModifyStagedOnlyDefault(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "add a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "add b")
	mustCheckout(t, "feat-a")

	// Add an untracked plan file — must not mark worktree dirty or block restack.
	if err := os.MkdirAll("plans", 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join("plans", "plan.md"), "untracked plan\n")

	statusOut := captureStdout(t, func() {
		if err := runStatus([]string{"--json"}); err != nil {
			t.Fatalf("status --json: %v", err)
		}
	})
	if !strings.Contains(statusOut, `"worktreeClean": true`) {
		t.Fatalf("untracked file marked worktree dirty:\n%s", statusOut)
	}
	if err := runRestack(nil); err != nil {
		t.Fatalf("restack blocked by untracked file: %v", err)
	}

	// Edit tracked a.txt without staging: bare `st modify` defaults to -a=false
	// and refuses to sweep unstaged edits unless staged or -a is passed.
	write(t, "a.txt", "a-edited\n")
	if err := runModify(nil); err == nil {
		t.Fatal("bare modify with only unstaged changes should fail")
	}

	// `st modify -a` stages tracked a.txt only, leaving plans/plan.md untracked.
	if err := runModify([]string{"-a"}); err != nil {
		t.Fatalf("modify -a: %v", err)
	}
	if hasFile("feat-a", "plans/plan.md") {
		t.Fatal("modify -a swept untracked plans/plan.md into commit")
	}
	if got := mustRun(t, "git", "show", "feat-b:a.txt"); got != "a-edited" {
		t.Fatalf("feat-b:a.txt = %q, want a-edited after restack", got)
	}
}

func TestDoctorScopingAndFix(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "a1", "a1.txt", "a1\n", "a1")
	mustCheckout(t, "main")
	mustCreate(t, "b1", "b1.txt", "b1\n", "b1")
	mustCreate(t, "b2", "b2.txt", "b2\n", "b2")

	// Amend b1 via raw git so b2 drifts while we stand on a1.
	mustCheckout(t, "b1")
	write(t, "b1.txt", "b1-v2\n")
	mustRun(t, "git", "commit", "-qa", "--amend", "--no-edit")
	mustCheckout(t, "a1")

	// Default doctor scopes warnings to current stack (a1 -> no warnings).
	scoped := captureStdout(t, func() {
		if err := runDoctor(nil); err != nil {
			t.Fatalf("doctor scoped: %v", err)
		}
	})
	if strings.Contains(scoped, "b2 needs restack") {
		t.Fatalf("scoped doctor leaked unrelated stack warning:\n%s", scoped)
	}

	// doctor --all reports b2 drift warning.
	allOut := captureStdout(t, func() {
		if err := runDoctor([]string{"--all"}); err != nil {
			t.Fatalf("doctor --all: %v", err)
		}
	})
	if !strings.Contains(allOut, "b2 needs restack") {
		t.Fatalf("doctor --all missing b2 warning:\n%s", allOut)
	}

	// doctor --fix on a consistent repo reports nothing to repair.
	cleanFix := captureStdout(t, func() {
		if err := runDoctor([]string{"--fix"}); err != nil {
			t.Fatalf("doctor --fix clean: %v", err)
		}
	})
	if !strings.Contains(cleanFix, "nothing to repair") {
		t.Fatalf("doctor --fix clean output = %q", cleanFix)
	}

	// Delete b2 behind st's back, then repair via doctor --fix.
	mustRun(t, "git", "branch", "-D", "b2")
	if err := runDoctor(nil); err == nil {
		t.Fatal("expected doctor to error when b2 is missing")
	}
	fixOut := captureStdout(t, func() {
		if err := runDoctor([]string{"--fix"}); err != nil {
			t.Fatalf("doctor --fix: %v", err)
		}
	})
	if !strings.Contains(fixOut, "repaired:") {
		t.Fatalf("doctor --fix output = %q", fixOut)
	}
}
