package stack

import (
	"fmt"
	"strings"

	"github.com/andyrewlee/stacked/internal/gh"
)

// GitHub is the port the stack engine uses to manage stacked pull requests.
// internal/gh.Shell is the production implementation; unit tests use a fake.
type GitHub interface {
	ListOpenPRs() ([]gh.PR, error)
	ListMergedPRs(limit int) ([]gh.PR, error)
	CreatePR(p gh.CreatePRParams) (*gh.PR, error)
	EditPRBase(number int, base string) error
	EditPRBody(number int, body string) error
}

// StackBlockStart and StackBlockEnd delimit the managed stack-navigation block
// inside each GitHub PR body so author prose outside the markers is preserved.
const (
	StackBlockStart = "<!-- st:stack:start -->"
	StackBlockEnd   = "<!-- st:stack:end -->"
)

// SubmitPROpts controls how SyncStackPRs creates and updates GitHub PRs.
type SubmitPROpts struct {
	Draft        bool
	DryRun       bool
	Title        string // optional explicit title applied to TargetBranch
	Body         string // optional explicit body applied to TargetBranch
	TargetBranch string // branch receiving explicit Title/Body (usually current branch)
}

// PROutcome reports the GitHub PR action taken (or planned under DryRun) for
// one submitted branch.
type PROutcome struct {
	Branch string `json:"branch"`
	Base   string `json:"base"`
	Number int    `json:"number,omitempty"`
	URL    string `json:"url,omitempty"`
	Draft  bool   `json:"draft,omitempty"`
	// Action is one of: created, retargeted, updated, unchanged,
	// would_create, would_retarget, would_update.
	Action string `json:"action"`
}

// StackPREntry describes one row in a PR's rendered stack block.
type StackPREntry struct {
	Branch string
	Base   string
	Number int
	Merged bool
}

// OpenPRsByHead indexes open PRs by their headRefName.
func OpenPRsByHead(prs []gh.PR) map[string]gh.PR {
	byHead := make(map[string]gh.PR, len(prs))
	for i := range prs {
		if prs[i].HeadRefName != "" {
			byHead[prs[i].HeadRefName] = prs[i]
		}
	}
	return byHead
}

// RetargetStackPRs updates the GitHub base branch of any already-open PR in
// branches whose current baseRefName differs from its local stack parent.
// Running this BEFORE pushing avoids GitHub showing stale diffs or auto-closing
// PRs when a parent branch changes or was pruned.
func RetargetStackPRs(client GitHub, s *State, branches []string, openByHead map[string]gh.PR, dryRun bool) (map[string]bool, error) {
	retargeted := map[string]bool{}
	for _, head := range branches {
		b, ok := s.Get(head)
		if !ok {
			continue
		}
		pr, exists := openByHead[head]
		if !exists || pr.BaseRefName == b.Parent {
			continue
		}
		if !dryRun {
			if err := client.EditPRBase(pr.Number, b.Parent); err != nil {
				return retargeted, fmt.Errorf("retargeting PR #%d (%s -> %s): %w", pr.Number, head, b.Parent, err)
			}
		}
		pr.BaseRefName = b.Parent
		openByHead[head] = pr
		retargeted[head] = true
	}
	return retargeted, nil
}

// SyncStackPRs creates missing GitHub PRs for branches (bottom-up), ensures
// every PR's base matches its stack parent, and refreshes the managed
// `<!-- st:stack:start -->` block across every open PR in each touched stack.
// retargeted records branches already retargeted pre-push by RetargetStackPRs.
func SyncStackPRs(client GitHub, g Git, s *State, branches []string, openByHead map[string]gh.PR, retargeted map[string]bool, opts SubmitPROpts) ([]PROutcome, error) {
	if openByHead == nil {
		openByHead = map[string]gh.PR{}
	}
	if retargeted == nil {
		retargeted = map[string]bool{}
	}

	outcomes := make([]PROutcome, 0, len(branches))
	outcomeIdx := make(map[string]int, len(branches))

	for _, head := range branches {
		b, ok := s.Get(head)
		if !ok {
			continue
		}
		if pr, exists := openByHead[head]; exists {
			action := "unchanged"
			if retargeted[head] {
				if opts.DryRun {
					action = "would_retarget"
				} else {
					action = "retargeted"
				}
			}
			outcomeIdx[head] = len(outcomes)
			outcomes = append(outcomes, PROutcome{
				Branch: head,
				Base:   b.Parent,
				Number: pr.Number,
				URL:    pr.URL,
				Draft:  pr.IsDraft,
				Action: action,
			})
			continue
		}

		// No open PR yet — create one (or plan creation under DryRun).
		if opts.DryRun {
			outcomeIdx[head] = len(outcomes)
			outcomes = append(outcomes, PROutcome{
				Branch: head,
				Base:   b.Parent,
				Draft:  opts.Draft,
				Action: "would_create",
			})
			continue
		}

		title, body := defaultPRTitleAndBody(g, s, head)
		if head == opts.TargetBranch || (opts.TargetBranch == "" && len(branches) == 1) {
			if strings.TrimSpace(opts.Title) != "" {
				title = strings.TrimSpace(opts.Title)
			}
			if opts.Body != "" {
				body = opts.Body
			}
		}

		created, err := client.CreatePR(gh.CreatePRParams{
			Head:  head,
			Base:  b.Parent,
			Title: title,
			Body:  body,
			Draft: opts.Draft,
		})
		if err != nil {
			return outcomes, fmt.Errorf("creating PR for %q (base %q): %w", head, b.Parent, err)
		}
		openByHead[head] = *created
		outcomeIdx[head] = len(outcomes)
		outcomes = append(outcomes, PROutcome{
			Branch: head,
			Base:   b.Parent,
			Number: created.Number,
			URL:    created.URL,
			Draft:  created.IsDraft,
			Action: "created",
		})
	}

	// Refresh the managed stack block for every distinct stack touched by
	// branches. Skip editing under DryRun (just mark would_update where needed).
	seenStack := map[string]bool{}
	for _, head := range branches {
		if !s.IsTracked(head) {
			continue
		}
		bottom := s.BottomOf(head)
		if seenStack[bottom] {
			continue
		}
		seenStack[bottom] = true

		stackBranches := s.StackBranches(bottom)
		var entries []StackPREntry
		for _, name := range stackBranches {
			b, ok := s.Get(name)
			if !ok {
				continue
			}
			if pr, exists := openByHead[name]; exists {
				entries = append(entries, StackPREntry{
					Branch: name,
					Base:   b.Parent,
					Number: pr.Number,
				})
			}
		}
		if len(entries) == 0 {
			continue
		}

		for _, entry := range entries {
			pr := openByHead[entry.Branch]
			hasExistingBlock := strings.Contains(pr.Body, StackBlockStart) && strings.Contains(pr.Body, StackBlockEnd)
			// Avoid adding stack boilerplate to a standalone single-PR branch
			// unless it already carries a managed stack block (e.g. a stack
			// that shrank to its last remaining PR after merges).
			if len(entries) <= 1 && !hasExistingBlock {
				continue
			}
			block := RenderStackBlock(entries, entry.Branch)
			newBody := UpsertStackBlock(pr.Body, block)
			if newBody == pr.Body {
				continue
			}
			if !opts.DryRun {
				if err := client.EditPRBody(pr.Number, newBody); err != nil {
					return outcomes, fmt.Errorf("updating stack links on PR #%d (%s): %w", pr.Number, entry.Branch, err)
				}
				pr.Body = newBody
				openByHead[entry.Branch] = pr
			}
			if idx, ok := outcomeIdx[entry.Branch]; ok && outcomes[idx].Action == "unchanged" {
				if opts.DryRun {
					outcomes[idx].Action = "would_update"
				} else {
					outcomes[idx].Action = "updated"
				}
			}
		}
	}

	return outcomes, nil
}

// MergedGitHubBranches returns the subset of candidates that have a merged PR
// on GitHub. It makes one ListMergedPRs call and matches by headRefName.
func MergedGitHubBranches(client GitHub, candidates []string) (map[string]bool, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	mergedPRs, err := client.ListMergedPRs(100)
	if err != nil {
		return nil, err
	}
	want := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		want[c] = true
	}
	merged := map[string]bool{}
	for i := range mergedPRs {
		if want[mergedPRs[i].HeadRefName] && strings.EqualFold(mergedPRs[i].State, "MERGED") {
			merged[mergedPRs[i].HeadRefName] = true
		}
	}
	return merged, nil
}

// RenderStackBlock renders the managed markdown stack summary for a PR body.
func RenderStackBlock(entries []StackPREntry, currentBranch string) string {
	var b strings.Builder
	b.WriteString(StackBlockStart)
	b.WriteString("\n**Stack:**\n")
	for _, e := range entries {
		switch {
		case e.Merged:
			fmt.Fprintf(&b, "- ✅ #%d (`%s`, merged)\n", e.Number, e.Branch)
		case e.Branch == currentBranch:
			fmt.Fprintf(&b, "- 👉 **#%d** (`%s` → `%s`) *(this PR)*\n", e.Number, e.Branch, e.Base)
		default:
			fmt.Fprintf(&b, "- #%d (`%s` → `%s`)\n", e.Number, e.Branch, e.Base)
		}
	}
	b.WriteString("\n<sub>Managed by `st`. Merge bottom-up.</sub>\n")
	b.WriteString(StackBlockEnd)
	return b.String()
}

// UpsertStackBlock inserts or replaces the managed stack block inside body,
// leaving all author content outside the markers untouched. It is idempotent.
func UpsertStackBlock(body, block string) string {
	start := strings.Index(body, StackBlockStart)
	end := strings.Index(body, StackBlockEnd)
	if start >= 0 && end >= start {
		endIdx := end + len(StackBlockEnd)
		return body[:start] + block + body[endIdx:]
	}
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return block
	}
	return strings.TrimRight(body, "\r\n") + "\n\n---\n\n" + block
}

// defaultPRTitleAndBody derives a sensible PR title and body from the commits
// in branch's recorded stack range (parentSHA..branch).
func defaultPRTitleAndBody(g Git, s *State, branch string) (title, body string) {
	b, ok := s.Get(branch)
	if !ok {
		return branch, ""
	}
	commits, err := g.CommitList(b.ParentSHA, branch)
	if err != nil || len(commits) == 0 {
		return branch, ""
	}
	// CommitList returns newest-first; the oldest commit is usually the branch's
	// primary change when follow-up commits exist.
	if len(commits) == 1 {
		title = commits[0].Subject
		if cb, err := g.CommitBody(commits[0].SHA); err == nil {
			body = cb
		}
		return title, body
	}
	oldest := commits[len(commits)-1]
	title = oldest.Subject
	var lines []string
	for i := len(commits) - 1; i >= 0; i-- {
		lines = append(lines, "- "+commits[i].Subject)
	}
	return title, strings.Join(lines, "\n")
}
