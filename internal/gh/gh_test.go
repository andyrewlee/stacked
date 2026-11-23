package gh

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSummarizeChecks(t *testing.T) {
	cases := []struct {
		name  string
		items []statusContext
		want  string
	}{
		{"empty", nil, ""},
		{"all passing", []statusContext{{Status: "COMPLETED", Conclusion: "SUCCESS"}, {State: "SUCCESS"}}, "passing"},
		{"neutral and skipped pass", []statusContext{{Status: "COMPLETED", Conclusion: "NEUTRAL"}, {Status: "COMPLETED", Conclusion: "SKIPPED"}}, "passing"},
		{"in progress is pending", []statusContext{{Status: "COMPLETED", Conclusion: "SUCCESS"}, {Status: "IN_PROGRESS"}}, "pending"},
		{"failure wins over pending", []statusContext{{Status: "IN_PROGRESS"}, {Status: "COMPLETED", Conclusion: "FAILURE"}}, "failing"},
		{"status context error is failing", []statusContext{{State: "ERROR"}}, "failing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarizeChecks(tc.items); got != tc.want {
				t.Fatalf("summarizeChecks() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseCreatedPRURL(t *testing.T) {
	url, num, err := parseCreatedPRURL("Creating pull request...\nhttps://github.com/acme/repo/pull/42\n")
	if err != nil {
		t.Fatalf("parseCreatedPRURL: %v", err)
	}
	if url != "https://github.com/acme/repo/pull/42" || num != 42 {
		t.Fatalf("got (%q, %d), want (https://github.com/acme/repo/pull/42, 42)", url, num)
	}
	if _, _, err := parseCreatedPRURL("not a url"); err == nil {
		t.Fatal("expected error for invalid output")
	}
}

func TestShellWithMockGH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock requires unix sh")
	}
	dir := t.TempDir()
	logFile := filepath.Join(dir, "gh.log")
	script := filepath.Join(dir, "fake-gh")
	body := `#!/bin/sh
echo "ARGS:$*" >> "$GH_LOG"
if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  if echo "$*" | grep -q "merged"; then
    echo '[{"number":10,"url":"https://github.com/o/r/pull/10","title":"old","state":"MERGED","headRefName":"old-br","baseRefName":"main"}]'
  else
    echo '[{"number":11,"url":"https://github.com/o/r/pull/11","title":"feat a","body":"hello","state":"OPEN","isDraft":false,"headRefName":"feat-a","baseRefName":"main","statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"}]}]'
  fi
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "create" ]; then
  cat >> "$GH_LOG"
  echo "" >> "$GH_LOG"
  echo "https://github.com/o/r/pull/12"
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "edit" ]; then
  if echo "$*" | grep -q -- "--body-file"; then
    cat >> "$GH_LOG"
    echo "" >> "$GH_LOG"
  fi
  exit 0
fi
echo "unexpected: $*" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(script, []byte(body), 0o755)
	t.Setenv("ST_GH_BIN", script)
	t.Setenv("GH_LOG", logFile)

	if !Available() {
		t.Fatal("Available() = false with mock ST_GH_BIN")
	}
	var sh Shell
	openPRs, err := sh.ListOpenPRs()
	if err != nil {
		t.Fatalf("ListOpenPRs: %v", err)
	}
	if len(openPRs) != 1 || openPRs[0].Number != 11 || openPRs[0].Checks != "passing" {
		t.Fatalf("unexpected openPRs: %+v", openPRs)
	}
	mergedPRs, err := sh.ListMergedPRs(50)
	if err != nil {
		t.Fatalf("ListMergedPRs: %v", err)
	}
	if len(mergedPRs) != 1 || mergedPRs[0].HeadRefName != "old-br" {
		t.Fatalf("unexpected mergedPRs: %+v", mergedPRs)
	}
	created, err := sh.CreatePR(CreatePRParams{Head: "feat-b", Base: "feat-a", Title: "add b", Body: "body b", Draft: true})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if created.Number != 12 || !created.IsDraft {
		t.Fatalf("unexpected created PR: %+v", created)
	}
	if err := sh.EditPRBase(12, "main"); err != nil {
		t.Fatalf("EditPRBase: %v", err)
	}
	if err := sh.EditPRBody(12, "updated body"); err != nil {
		t.Fatalf("EditPRBody: %v", err)
	}
	logged, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pr create --head feat-b --base feat-a --title add b --body-file - --draft", "body b", "pr edit 12 --base main", "updated body"} {
		if !strings.Contains(string(logged), want) {
			t.Fatalf("gh log missing %q:\n%s", want, string(logged))
		}
	}
}
