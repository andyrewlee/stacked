package stack

import (
	"strings"
	"testing"

	"github.com/andyrewlee/stacked/internal/gh"
)

type fakeGitHub struct {
	openPRs   []gh.PR
	mergedPRs []gh.PR
	nextNum   int
	created   []gh.CreatePRParams
	baseEdits []struct {
		num  int
		base string
	}
	bodyEdits []struct {
		num  int
		body string
	}
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{nextNum: 100}
}

func (f *fakeGitHub) ListOpenPRs() ([]gh.PR, error) {
	out := make([]gh.PR, len(f.openPRs))
	copy(out, f.openPRs)
	return out, nil
}

func (f *fakeGitHub) ListMergedPRs(_ int) ([]gh.PR, error) {
	out := make([]gh.PR, len(f.mergedPRs))
	copy(out, f.mergedPRs)
	return out, nil
}

func (f *fakeGitHub) CreatePR(p gh.CreatePRParams) (*gh.PR, error) {
	f.created = append(f.created, p)
	num := f.nextNum
	f.nextNum++
	pr := gh.PR{
		Number:      num,
		URL:         "https://github.com/acme/repo/pull/" + itoa(num),
		Title:       p.Title,
		Body:        p.Body,
		State:       "OPEN",
		IsDraft:     p.Draft,
		HeadRefName: p.Head,
		BaseRefName: p.Base,
	}
	f.openPRs = append(f.openPRs, pr)
	return &pr, nil
}

func (f *fakeGitHub) EditPRBase(number int, base string) error {
	f.baseEdits = append(f.baseEdits, struct {
		num  int
		base string
	}{number, base})
	for i := range f.openPRs {
		if f.openPRs[i].Number == number {
			f.openPRs[i].BaseRefName = base
		}
	}
	return nil
}

func (f *fakeGitHub) EditPRBody(number int, body string) error {
	f.bodyEdits = append(f.bodyEdits, struct {
		num  int
		body string
	}{number, body})
	for i := range f.openPRs {
		if f.openPRs[i].Number == number {
			f.openPRs[i].Body = body
		}
	}
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func TestUpsertStackBlockIdempotentAndPreservesProse(t *testing.T) {
	entries := []StackPREntry{
		{Branch: "feat-a", Base: "main", Number: 10},
		{Branch: "feat-b", Base: "feat-a", Number: 11},
	}
	block := RenderStackBlock(entries, "feat-b")
	first := UpsertStackBlock("## Summary\n\nMy PR notes.", block)
	if !strings.Contains(first, "## Summary\n\nMy PR notes.") {
		t.Fatalf("author prose lost:\n%s", first)
	}
	if !strings.Contains(first, "👉 **#11** (`feat-b` → `feat-a`)") {
		t.Fatalf("missing current marker:\n%s", first)
	}
	second := UpsertStackBlock(first, block)
	if second != first {
		t.Fatalf("UpsertStackBlock not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}

	// Updating stack after feat-a merges preserves prose and updates only the block.
	updatedEntries := []StackPREntry{
		{Branch: "feat-b", Base: "main", Number: 11},
	}
	third := UpsertStackBlock(first, RenderStackBlock(updatedEntries, "feat-b"))
	if !strings.Contains(third, "## Summary\n\nMy PR notes.") {
		t.Fatalf("author prose lost on update:\n%s", third)
	}
	if strings.Contains(third, "#10") {
		t.Fatalf("stale PR #10 survived update:\n%s", third)
	}
	if !strings.Contains(third, "👉 **#11** (`feat-b` → `main`)") {
		t.Fatalf("updated base missing:\n%s", third)
	}
}

func TestSyncStackPRsCreatesRetargetsAndUpdatesStackBlocks(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	mkBranch(t, env, s, f, "feat-a", "feat-b")

	client := newFakeGitHub()
	openByHead := OpenPRsByHead(client.openPRs)

	branches := []string{"feat-a", "feat-b"}
	outcomes, err := SyncStackPRs(client, env.Git, s, branches, openByHead, nil, SubmitPROpts{})
	if err != nil {
		t.Fatalf("SyncStackPRs: %v", err)
	}
	if len(outcomes) != 2 || outcomes[0].Action != "created" || outcomes[1].Action != "created" {
		t.Fatalf("unexpected outcomes: %+v", outcomes)
	}
	if len(client.created) != 2 || client.created[0].Base != "main" || client.created[1].Base != "feat-a" {
		t.Fatalf("unexpected creates: %+v", client.created)
	}
	// Both PRs should have received the 2-item stack block.
	if len(client.bodyEdits) != 2 {
		t.Fatalf("expected 2 stack block body edits, got %d", len(client.bodyEdits))
	}

	// Second run is a pure no-op (zero creates, zero base edits, zero body edits).
	client.bodyEdits = nil
	openByHead = OpenPRsByHead(client.openPRs)
	outcomes2, err := SyncStackPRs(client, env.Git, s, branches, openByHead, nil, SubmitPROpts{})
	if err != nil {
		t.Fatalf("second SyncStackPRs: %v", err)
	}
	if len(client.bodyEdits) != 0 {
		t.Fatalf("expected 0 body edits on idempotent rerun, got %d", len(client.bodyEdits))
	}
	if outcomes2[0].Action != "unchanged" || outcomes2[1].Action != "unchanged" {
		t.Fatalf("expected unchanged outcomes, got %+v", outcomes2)
	}

	// Simulate feat-a pruned and feat-b reparented onto main: RetargetStackPRs + SyncStackPRs retargets #101 to main and updates its stack block.
	s.RemoveBranch("feat-a")
	openByHead = OpenPRsByHead(client.openPRs)
	retargeted, err := RetargetStackPRs(client, s, []string{"feat-b"}, openByHead, false)
	if err != nil {
		t.Fatalf("RetargetStackPRs: %v", err)
	}
	if !retargeted["feat-b"] || len(client.baseEdits) != 1 || client.baseEdits[0].base != "main" {
		t.Fatalf("expected feat-b retargeted to main, got %+v / %+v", retargeted, client.baseEdits)
	}
	outcomes3, err := SyncStackPRs(client, env.Git, s, []string{"feat-b"}, openByHead, retargeted, SubmitPROpts{})
	if err != nil {
		t.Fatalf("SyncStackPRs after reparent: %v", err)
	}
	if outcomes3[0].Action != "retargeted" {
		t.Fatalf("expected retargeted action, got %+v", outcomes3)
	}
	if len(client.bodyEdits) != 1 || !strings.Contains(client.bodyEdits[0].body, "`feat-b` → `main`") {
		t.Fatalf("expected stack block refreshed on #101, got %+v", client.bodyEdits)
	}
}

func TestSyncStackPRsStandaloneSinglePROmitsStackBlock(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "solo")
	client := newFakeGitHub()

	outcomes, err := SyncStackPRs(client, env.Git, s, []string{"solo"}, OpenPRsByHead(nil), nil, SubmitPROpts{})
	if err != nil {
		t.Fatalf("SyncStackPRs: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Action != "created" {
		t.Fatalf("unexpected outcomes: %+v", outcomes)
	}
	if len(client.bodyEdits) != 0 {
		t.Fatalf("standalone single PR should not get stack block edit, got %+v", client.bodyEdits)
	}
}

func TestMergedGitHubBranches(t *testing.T) {
	client := newFakeGitHub()
	client.mergedPRs = []gh.PR{
		{Number: 1, HeadRefName: "feat-a", State: "MERGED"},
		{Number: 2, HeadRefName: "unrelated", State: "MERGED"},
	}
	got, err := MergedGitHubBranches(client, []string{"feat-a", "feat-b"})
	if err != nil {
		t.Fatalf("MergedGitHubBranches: %v", err)
	}
	if !got["feat-a"] || got["feat-b"] || got["unrelated"] {
		t.Fatalf("unexpected merged set: %+v", got)
	}
}
