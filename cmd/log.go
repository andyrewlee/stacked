package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/andyrewlee/stacked/internal/gh"
	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "log",
		Aliases:    []string{"ls"},
		Summary:    "Show the current stack as a tree (--all for all stacks, --pr for GitHub PR status)",
		Usage:      "st log [--all] [--pr] [--json]",
		Run:        runLog,
		NewFlagSet: logFlagSet,
	})
}

// prStatus is the concise GitHub PR view attached to log/status output under --pr.
type prStatus struct {
	Number       int    `json:"number"`
	URL          string `json:"url"`
	State        string `json:"state"`
	Draft        bool   `json:"draft,omitempty"`
	Base         string `json:"base"`
	BaseMismatch bool   `json:"baseMismatch,omitempty"`
	Checks       string `json:"checks,omitempty"`
}

func makePRStatus(pr *gh.PR, expectedBase string) *prStatus {
	return &prStatus{
		Number:       pr.Number,
		URL:          pr.URL,
		State:        pr.State,
		Draft:        pr.IsDraft,
		Base:         pr.BaseRefName,
		BaseMismatch: expectedBase != "" && pr.BaseRefName != expectedBase,
		Checks:       pr.Checks,
	}
}

func formatPRTag(pr *prStatus) string {
	if pr == nil {
		return ""
	}
	parts := []string{fmt.Sprintf("#%d", pr.Number)}
	if pr.Draft {
		parts = append(parts, "draft")
	}
	switch pr.Checks {
	case "passing":
		parts = append(parts, "✓")
	case "failing":
		parts = append(parts, "✗")
	case "pending":
		parts = append(parts, "⋯")
	}
	if pr.BaseMismatch {
		parts = append(parts, fmt.Sprintf("base:%s", sanitizeForTerminal(pr.Base)))
	}
	return "[" + strings.Join(parts, " ") + "]"
}

func loadOpenPRsForDisplay() (map[string]gh.PR, error) {
	if !gh.Available() {
		return nil, fmt.Errorf("--pr requires the GitHub CLI (gh) on PATH")
	}
	prs, err := gh.ListOpenPRs()
	if err != nil {
		return nil, err
	}
	return stack.OpenPRsByHead(prs), nil
}

func trunkBehindCount(p *cachedPort, trunk string) int {
	if !p.remoteExists("origin") {
		return 0
	}
	remoteRef := git.RemoteTrackingRef("origin", trunk)
	if _, err := p.RevParse(remoteRef); err != nil {
		return 0
	}
	n, err := git.BehindCount(git.LocalBranchNameRef(trunk), remoteRef)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// runLog renders the tracked stack(s) as a tree (trunk at the bottom, branches
// drawn above their parents) or, with --json, as a structured tree for scripting.
// By default when on a tracked branch it shows only the current stack; on trunk,
// detached HEAD, or with --all it shows the whole forest.
func runLog(args []string) error {
	var o logOpts
	fs := newLogFlags(&o)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("log", fs.Args()); err != nil {
		return err
	}

	s, err := loadState()
	if err != nil {
		return err
	}

	cur, _ := currentBranch()
	var scope map[string]bool
	if !o.all && s.IsTracked(cur) {
		scope = map[string]bool{s.Trunk: true}
		for _, name := range s.StackBranches(cur) {
			scope[name] = true
		}
	}

	index := filteredChildIndex(s, scope)
	renderedNames, rendered := renderedBranches(s, scope)

	tips, err := git.TipsFor(renderedNames)
	if err != nil {
		return err
	}
	subjects, err := git.TipSubjectsFor(renderedNames)
	if err != nil {
		return err
	}
	ancestors, err := tipAncestors(s, tips)
	if err != nil {
		return err
	}
	drift := s.DriftAgainst(tips)

	wtInfo, err := branchWorktrees(rendered)
	if err != nil {
		return err
	}

	var openPRs map[string]gh.PR
	if o.pr {
		openPRs, err = loadOpenPRsForDisplay()
		if err != nil {
			return err
		}
	}

	trunkBehind := trunkBehindCount(newGitPort(), s.Trunk)

	d := logData{
		index:       index,
		cur:         cur,
		drift:       drift,
		tips:        tips,
		subjects:    subjects,
		ancestors:   ancestors,
		wtInfo:      wtInfo,
		openPRs:     openPRs,
		trunkBehind: trunkBehind,
		unreachable: s.UnreachableBranches(),
	}
	if o.asJSON {
		return printLogJSON(s, &d)
	}
	printLogTree(s, &d)
	return nil
}

func filteredChildIndex(s *stack.State, scope map[string]bool) map[string][]string {
	full := s.ChildIndex()
	if scope == nil {
		return full
	}
	outIdx := make(map[string][]string, len(scope))
	for parent, kids := range full {
		if !scope[parent] {
			continue
		}
		var kept []string
		for _, kid := range kids {
			if scope[kid] {
				kept = append(kept, kid)
			}
		}
		if len(kept) > 0 {
			outIdx[parent] = kept
		}
	}
	return outIdx
}

func renderedBranches(s *stack.State, scope map[string]bool) (names []string, rendered map[string]bool) {
	rendered = make(map[string]bool, len(s.Branches)+1)
	names = make([]string, 0, len(s.Branches)+1)
	names = append(names, s.Trunk)
	rendered[s.Trunk] = true
	tracked := make([]string, 0, len(s.Branches))
	for name := range s.Branches {
		if name != s.Trunk && (scope == nil || scope[name]) {
			tracked = append(tracked, name)
			rendered[name] = true
		}
	}
	sort.Strings(tracked)
	return append(names, tracked...), rendered
}

type worktreeInfo struct {
	path       string
	dirty      bool
	paused     bool
	locked     bool
	conflicted []string
}

// branchWorktrees returns, for a multi-worktree repo, a map from branch name to
// where it lives and whether that worktree is dirty. The caller's own current
// worktree is omitted from path annotations so agents working inside a linked
// worktree (e.g. amux) don't get noisy self-path tags on their own branch;
// paused mid-rebase in the current worktree still surfaces paused+conflicted.
func branchWorktrees(rendered map[string]bool) (map[string]worktreeInfo, error) {
	wts, err := worktrees()
	if err != nil {
		return nil, err
	}
	if !stack.IsMultiWorktree(wts) {
		return map[string]worktreeInfo{}, nil
	}
	main, _ := stack.MainWorktree(wts)
	curRoot, _ := git.RepoRoot()
	isSelfOrMain := func(path string) bool {
		if samePath(path, main.Path) {
			return true
		}
		return curRoot != "" && samePath(path, curRoot)
	}

	var extant []git.Worktree
	for _, wt := range wts {
		if wt.Branch == "" {
			if _, err := os.Stat(wt.Path); err != nil {
				continue
			}
		}
		extant = append(extant, wt)
	}
	paused, err := stack.PausedRebaseOwners(newGitPort(), extant)
	if err != nil {
		return nil, err
	}

	type wtProbe struct {
		path   string
		branch string
		paused bool
	}
	var probes []wtProbe
	locked := map[string]bool{}
	for _, wt := range wts {
		if isSelfOrMain(wt.Path) || wt.Branch == "" || !rendered[wt.Branch] {
			continue
		}
		probes = append(probes, wtProbe{path: wt.Path, branch: wt.Branch})
		locked[wt.Path] = wt.Locked
	}
	var pausedNames []string
	for name := range paused {
		pausedNames = append(pausedNames, name)
	}
	sort.Strings(pausedNames)
	for _, name := range pausedNames {
		if !rendered[name] {
			continue
		}
		wt := paused[name]
		probes = append(probes, wtProbe{path: wt.Path, branch: name, paused: true})
		locked[wt.Path] = wt.Locked
	}

	type wtResult struct {
		clean      bool
		conflicted []string
	}
	slots := make([]wtResult, len(probes))
	_ = git.ParallelProbes(len(probes), func(i int) error {
		var r wtResult
		clean, err := git.IsCleanAt(probes[i].path)
		if err != nil {
			clean = true
		}
		r.clean = clean
		if probes[i].paused {
			if files, uerr := git.UnmergedFilesIn(probes[i].path); uerr == nil {
				r.conflicted = files
			}
		}
		slots[i] = r
		return nil
	})

	info := make(map[string]worktreeInfo, len(wts))
	for i, p := range probes {
		dirty := !slots[i].clean
		if p.paused && isSelfOrMain(p.path) {
			info[p.branch] = worktreeInfo{paused: true, conflicted: slots[i].conflicted}
			continue
		}
		info[p.branch] = worktreeInfo{path: p.path, dirty: dirty, paused: p.paused, locked: locked[p.path], conflicted: slots[i].conflicted}
	}
	return info, nil
}

type logNode struct {
	Name            string     `json:"name"`
	Parent          string     `json:"parent,omitempty"`
	ParentSHA       string     `json:"parentSHA,omitempty"`
	Tip             string     `json:"tip,omitempty"`
	Current         bool       `json:"current"`
	NeedsRestack    bool       `json:"needsRestack"`
	TopCommit       string     `json:"topCommit,omitempty"`
	Worktree        string     `json:"worktree,omitempty"`
	Dirty           bool       `json:"dirty,omitempty"`
	Paused          bool       `json:"paused,omitempty"`
	ConflictedFiles []string   `json:"conflictedFiles,omitempty"`
	Locked          bool       `json:"locked,omitempty"`
	PR              *prStatus  `json:"pr,omitempty"`
	TrunkBehind     int        `json:"trunkBehind,omitempty"`
	Children        []*logNode `json:"children"`
	Unreachable     []string   `json:"unreachable,omitempty"`
}

type logData struct {
	index       map[string][]string
	cur         string
	drift       map[string]bool
	tips        map[string]string
	subjects    map[string]string
	ancestors   map[ancestorPair]bool
	wtInfo      map[string]worktreeInfo
	openPRs     map[string]gh.PR
	trunkBehind int
	unreachable []string
}

func printLogJSON(s *stack.State, d *logData) error {
	visited := make(map[string]bool, len(d.index)+1)
	var build func(name, parent string) *logNode
	build = func(name, parent string) *logNode {
		if visited[name] {
			return nil
		}
		visited[name] = true
		node := &logNode{Name: name, Parent: parent, Tip: d.tips[name], Current: name == d.cur, Children: []*logNode{}}
		if b, ok := s.Get(name); ok {
			node.ParentSHA = b.ParentSHA
			node.NeedsRestack = d.drift[name]
			if subject, ok := topSubject(b, d.tips, d.subjects, d.ancestors); ok {
				node.TopCommit = subject
			}
			if pr, ok := d.openPRs[name]; ok {
				node.PR = makePRStatus(&pr, b.Parent)
			}
		}
		if wt, ok := d.wtInfo[name]; ok {
			node.Worktree = wt.path
			node.Dirty = wt.dirty
			node.Paused = wt.paused
			node.ConflictedFiles = wt.conflicted
			node.Locked = wt.locked
		}
		for _, child := range d.index[name] {
			if childNode := build(child, name); childNode != nil {
				node.Children = append(node.Children, childNode)
			}
		}
		return node
	}
	root := build(s.Trunk, "")
	root.TrunkBehind = d.trunkBehind
	root.Unreachable = d.unreachable
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out("%s\n", data)
	return nil
}

func printLogTree(s *stack.State, d *logData) {
	visited := make(map[string]bool, len(d.index)+1)
	var printBranch func(name string, depth int)
	printBranch = func(name string, depth int) {
		if visited[name] {
			return
		}
		visited[name] = true
		for _, child := range d.index[name] {
			printBranch(child, depth+1)
		}

		indent := ""
		for i := 0; i < depth; i++ {
			indent += "  "
		}

		safeName := sanitizeForTerminal(name)
		marker, label := "○", safeName
		if name == d.cur {
			marker = paint("◉", ansiBold, ansiGreen)
			label = paint(safeName, ansiBold, ansiGreen)
		}
		line := fmt.Sprintf("%s%s %s", indent, marker, label)

		if name == s.Trunk && d.trunkBehind > 0 {
			line += " " + paint(fmt.Sprintf("(%d behind origin/%s)", d.trunkBehind, sanitizeForTerminal(s.Trunk)), ansiYellow)
		}
		if b, ok := s.Get(name); ok {
			if pr, ok := d.openPRs[name]; ok {
				line += " " + paint(formatPRTag(makePRStatus(&pr, b.Parent)), ansiCyan)
			}
			if d.drift[name] {
				line += " " + paint("(needs restack)", ansiYellow)
			}
			if subject, ok := topSubject(b, d.tips, d.subjects, d.ancestors); ok {
				line += "  " + paint(sanitizeForTerminal(subject), ansiDim)
			}
		}
		if wt, ok := d.wtInfo[name]; ok {
			var tag string
			if wt.paused && wt.path == "" {
				tag = "(paused mid-rebase)"
			} else {
				parts := []string{"worktree: " + sanitizeForTerminal(wt.path)}
				if wt.dirty {
					parts = append(parts, "dirty")
				}
				if wt.paused {
					parts = append(parts, "paused")
				}
				if wt.locked {
					parts = append(parts, "locked")
				}
				tag = "(" + strings.Join(parts, ", ") + ")"
			}
			line += " " + paint(tag, ansiCyan)
		}
		out("%s\n", line)
	}
	printBranch(s.Trunk, 0)
	if len(d.unreachable) > 0 {
		out("%s\n", paint(fmt.Sprintf("warning: tracked branches unreachable from trunk %q (cycle or dangling parent): %s — run `st doctor --fix`",
			sanitizeForTerminal(s.Trunk), sanitizeForTerminal(strings.Join(d.unreachable, ", "))), ansiYellow))
	}
}

type ancestorPair struct {
	tip       string
	parentTip string
}

func tipAncestors(s *stack.State, tips map[string]string) (map[ancestorPair]bool, error) {
	type probe struct {
		pair      ancestorPair
		branchRef string
		parentRef string
	}
	var probes []probe
	seen := map[ancestorPair]bool{}
	names := make([]string, 0, len(s.Branches))
	for name := range s.Branches {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		b := s.Branches[name]
		tip, ok := tips[name]
		parentTip, pok := tips[b.Parent]
		if !ok || !pok || tip == parentTip {
			continue
		}
		pair := ancestorPair{tip: tip, parentTip: parentTip}
		if seen[pair] {
			continue
		}
		seen[pair] = true
		probes = append(probes, probe{pair: pair, branchRef: git.LocalBranchNameRef(name), parentRef: git.LocalBranchNameRef(b.Parent)})
	}
	if len(probes) == 0 {
		return map[ancestorPair]bool{}, nil
	}
	results := make([]bool, len(probes))
	if err := git.ParallelProbes(len(probes), func(i int) error {
		anc, err := git.IsAncestor(probes[i].branchRef, probes[i].parentRef)
		if err != nil {
			return err
		}
		results[i] = anc
		return nil
	}); err != nil {
		return nil, err
	}
	outMap := make(map[ancestorPair]bool, len(probes))
	for i, p := range probes {
		outMap[p.pair] = results[i]
	}
	return outMap, nil
}

func topSubject(b *stack.Branch, tips, subjects map[string]string, ancestors map[ancestorPair]bool) (string, bool) {
	tip, ok := tips[b.Name]
	parentTip, pok := tips[b.Parent]
	if !ok || !pok {
		return "", false
	}
	if tip == parentTip || ancestors[ancestorPair{tip: tip, parentTip: parentTip}] {
		return "", false
	}
	subject := subjects[b.Name]
	return subject, subject != ""
}
