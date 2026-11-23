// Package gh is a thin, zero-dependency wrapper around the GitHub CLI (`gh`)
// for creating, retargeting, listing, and updating stacked pull requests.
package gh

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// PR describes a GitHub pull request as returned by `gh pr list` / `gh pr create`.
type PR struct {
	Number         int    `json:"number"`
	URL            string `json:"url"`
	Title          string `json:"title,omitempty"`
	Body           string `json:"body,omitempty"`
	State          string `json:"state,omitempty"` // OPEN, MERGED, CLOSED
	IsDraft        bool   `json:"isDraft,omitempty"`
	HeadRefName    string `json:"headRefName"`
	BaseRefName    string `json:"baseRefName"`
	ReviewDecision string `json:"reviewDecision,omitempty"`
	Checks         string `json:"checks,omitempty"` // passing, failing, pending, or ""
}

// CreatePRParams holds the arguments for creating a pull request.
type CreatePRParams struct {
	Head  string
	Base  string
	Title string
	Body  string
	Draft bool
}

// Bin returns the gh executable path: ST_GH_BIN if set (useful in tests or
// custom installs), otherwise "gh".
func Bin() string {
	if b := strings.TrimSpace(os.Getenv("ST_GH_BIN")); b != "" {
		return b
	}
	return "gh"
}

// Available reports whether the gh binary is present on PATH (or at ST_GH_BIN).
func Available() bool {
	_, err := exec.LookPath(Bin())
	return err == nil
}

// Shell is the production implementation of the stack engine's GitHub port.
type Shell struct{}

// ListOpenPRs returns open pull requests in the current repository.
func (Shell) ListOpenPRs() ([]PR, error) { return ListOpenPRs() }

// ListMergedPRs returns recently merged pull requests in the current repository.
func (Shell) ListMergedPRs(limit int) ([]PR, error) { return ListMergedPRs(limit) }

// CreatePR opens a new pull request via `gh pr create`.
func (Shell) CreatePR(p CreatePRParams) (*PR, error) { return CreatePR(p) }

// EditPRBase changes the base branch of pull request number.
func (Shell) EditPRBase(number int, base string) error { return EditPRBase(number, base) }

// EditPRBody replaces the body of pull request number.
func (Shell) EditPRBody(number int, body string) error { return EditPRBody(number, body) }

// rawPR mirrors gh's JSON schema before collapsing statusCheckRollup into Checks.
type rawPR struct {
	Number            int             `json:"number"`
	URL               string          `json:"url"`
	Title             string          `json:"title"`
	Body              string          `json:"body"`
	State             string          `json:"state"`
	IsDraft           bool            `json:"isDraft"`
	HeadRefName       string          `json:"headRefName"`
	BaseRefName       string          `json:"baseRefName"`
	ReviewDecision    string          `json:"reviewDecision"`
	StatusCheckRollup []statusContext `json:"statusCheckRollup"`
}

type statusContext struct {
	Status     string `json:"status"`
	State      string `json:"state"`
	Conclusion string `json:"conclusion"`
}

func (r *rawPR) toPR() PR {
	return PR{
		Number:         r.Number,
		URL:            r.URL,
		Title:          r.Title,
		Body:           r.Body,
		State:          r.State,
		IsDraft:        r.IsDraft,
		HeadRefName:    r.HeadRefName,
		BaseRefName:    r.BaseRefName,
		ReviewDecision: r.ReviewDecision,
		Checks:         summarizeChecks(r.StatusCheckRollup),
	}
}

func summarizeChecks(items []statusContext) string {
	if len(items) == 0 {
		return ""
	}
	hasPending := false
	for _, item := range items {
		c := strings.ToUpper(item.Conclusion)
		st := strings.ToUpper(item.Status)
		state := strings.ToUpper(item.State)
		switch {
		case c == "FAILURE" || c == "TIMED_OUT" || c == "CANCELLED" || c == "ACTION_REQUIRED" || c == "STARTUP_FAILURE" || c == "STALE" ||
			state == "FAILURE" || state == "ERROR":
			return "failing"
		case st == "IN_PROGRESS" || st == "QUEUED" || st == "PENDING" || st == "WAITING" || st == "REQUESTED" ||
			state == "PENDING" || state == "EXPECTED" || (c == "" && state == "" && st != "COMPLETED"):
			hasPending = true
		}
	}
	if hasPending {
		return "pending"
	}
	return "passing"
}

// ListOpenPRs fetches up to 200 open PRs for the current repository.
func ListOpenPRs() ([]PR, error) {
	out, err := run("", "pr", "list",
		"--state", "open",
		"--limit", "200",
		"--json", "number,url,title,body,state,isDraft,headRefName,baseRefName,reviewDecision,statusCheckRollup",
	)
	if err != nil {
		return nil, err
	}
	return decodePRList(out)
}

// ListMergedPRs fetches up to limit recently merged PRs for the current repository.
func ListMergedPRs(limit int) ([]PR, error) {
	if limit <= 0 {
		limit = 100
	}
	out, err := run("", "pr", "list",
		"--state", "merged",
		"--limit", strconv.Itoa(limit),
		"--json", "number,url,title,state,headRefName,baseRefName",
	)
	if err != nil {
		return nil, err
	}
	return decodePRList(out)
}

func decodePRList(raw string) ([]PR, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	var items []rawPR
	if err := json.Unmarshal([]byte(trimmed), &items); err != nil {
		return nil, fmt.Errorf("parse gh pr list output: %w", err)
	}
	prs := make([]PR, len(items))
	for i := range items {
		prs[i] = items[i].toPR()
	}
	return prs, nil
}

// CreatePR creates a GitHub PR using `gh pr create` and returns the created PR
// metadata (URL and parsed PR number).
func CreatePR(p CreatePRParams) (*PR, error) {
	if p.Head == "" || p.Base == "" {
		return nil, errors.New("gh pr create requires head and base branches")
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		title = p.Head
	}
	args := []string{
		"pr", "create",
		"--head", p.Head,
		"--base", p.Base,
		"--title", title,
		"--body-file", "-",
	}
	if p.Draft {
		args = append(args, "--draft")
	}
	out, err := run(p.Body, args...)
	if err != nil {
		return nil, err
	}
	prURL, number, err := parseCreatedPRURL(out)
	if err != nil {
		return nil, err
	}
	return &PR{
		Number:      number,
		URL:         prURL,
		Title:       title,
		Body:        p.Body,
		State:       "OPEN",
		IsDraft:     p.Draft,
		HeadRefName: p.Head,
		BaseRefName: p.Base,
	}, nil
}

// EditPRBase retargets pull request number to base.
func EditPRBase(number int, base string) error {
	if number <= 0 || base == "" {
		return errors.New("gh pr edit --base requires a PR number and base branch")
	}
	_, err := run("", "pr", "edit", strconv.Itoa(number), "--base", base)
	return err
}

// EditPRBody updates the body of pull request number via stdin.
func EditPRBody(number int, body string) error {
	if number <= 0 {
		return errors.New("gh pr edit --body requires a PR number")
	}
	_, err := run(body, "pr", "edit", strconv.Itoa(number), "--body-file", "-")
	return err
}

// parseCreatedPRURL extracts the PR URL and numeric ID from `gh pr create` stdout.
func parseCreatedPRURL(stdout string) (prURL string, number int, err error) {
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if idx := strings.LastIndex(line, "/pull/"); idx >= 0 {
			numStr := strings.TrimRight(line[idx+len("/pull/"):], "/")
			if slash := strings.IndexAny(numStr, "/?# "); slash >= 0 {
				numStr = numStr[:slash]
			}
			n, convErr := strconv.Atoi(numStr)
			if convErr == nil && n > 0 {
				return line, n, nil
			}
		}
	}
	return "", 0, fmt.Errorf("could not parse PR URL from gh output: %q", strings.TrimSpace(stdout))
}

func run(stdin string, args ...string) (string, error) {
	bin := Bin()
	cmd := exec.Command(bin, args...)
	if stdin != "" || slicesContains(args, "--body-file") {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return "", fmt.Errorf("gh %s: %s: %w", strings.Join(args, " "), msg, err)
		}
		return "", fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}

func slicesContains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
