package cmd

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "log",
		Aliases: []string{"ls"},
		Summary: "Show the stack as a tree",
		Usage:   "st log [--json]",
		Run:     runLog,
	})
}

// runLog renders the tracked stacks as a tree (trunk at the bottom, branches
// drawn above their parents) or, with --json, as a structured tree for scripting.
func runLog(args []string) error {
	asJSON, err := parsePlain("log", args)
	if err != nil {
		return err
	}

	s, err := loadState()
	if err != nil {
		return err
	}

	// The current branch may be unavailable (detached HEAD); that is not fatal
	// for rendering, so treat it as "no marker".
	cur, _ := currentBranch()
	index := s.ChildIndex()
	renderedNames, rendered := renderedBranches(s)

	// Two exact cat-file batch reads power the whole render for only the
	// branches that can appear in the tree: one for tips (drift) and one for tip
	// subjects, instead of a `git log` spawn per branch.
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

	// Worktree annotations are gated on a multi-worktree repo so the single-tree
	// output stays byte-for-byte identical: in the common case wtInfo is empty
	// and every annotation site is skipped.
	wtInfo, err := branchWorktrees(rendered)
	if err != nil {
		return err
	}

	d := logData{
		index:     index,
		cur:       cur,
		drift:     drift,
		tips:      tips,
		subjects:  subjects,
		ancestors: ancestors,
		wtInfo:    wtInfo,
	}
	if asJSON {
		return printLogJSON(s, d)
	}
	printLogTree(s, d)
	return nil
}

func renderedBranches(s *stack.State) (names []string, rendered map[string]bool) {
	rendered = make(map[string]bool, len(s.Branches)+1)
	names = make([]string, 0, len(s.Branches)+1)
	names = append(names, s.Trunk)
	rendered[s.Trunk] = true
	tracked := make([]string, 0, len(s.Branches))
	for name := range s.Branches {
		if name != s.Trunk {
			tracked = append(tracked, name)
			rendered[name] = true
		}
	}
	sort.Strings(tracked)
	return append(names, tracked...), rendered
}

// worktreeInfo annotates a branch that lives in a linked worktree.
type worktreeInfo struct {
	path  string
	dirty bool
}

// branchWorktrees returns, for a multi-worktree repo, a map from branch name to
// where it lives and whether that worktree is dirty. For a single-tree repo it
// returns an empty map so callers add no annotations and the output is
// unchanged. Only rendered branches are probed for dirtiness; unrelated local
// branches cannot appear in log output.
func branchWorktrees(rendered map[string]bool) (map[string]worktreeInfo, error) {
	wts, err := worktrees()
	if err != nil {
		return nil, err
	}
	if !stack.IsMultiWorktree(wts) {
		return map[string]worktreeInfo{}, nil
	}
	main, _ := stack.MainWorktree(wts)
	info := make(map[string]worktreeInfo, len(wts))
	for _, wt := range wts {
		if wt.Path == main.Path {
			continue
		}
		if wt.Branch == "" || !rendered[wt.Branch] {
			continue
		}
		clean, err := git.IsCleanAt(wt.Path)
		if err != nil {
			// A worktree we cannot stat (e.g. pruned on disk) is reported without a
			// dirty flag rather than failing the whole render.
			clean = true
		}
		info[wt.Branch] = worktreeInfo{path: wt.Path, dirty: !clean}
	}
	return info, nil
}

// logNode is the JSON shape of a branch in the stack tree.
type logNode struct {
	Name         string     `json:"name"`
	Parent       string     `json:"parent,omitempty"`
	ParentSHA    string     `json:"parentSHA,omitempty"`
	Current      bool       `json:"current"`
	NeedsRestack bool       `json:"needsRestack"`
	TopCommit    string     `json:"topCommit,omitempty"`
	Worktree     string     `json:"worktree,omitempty"`
	Dirty        bool       `json:"dirty,omitempty"`
	Children     []*logNode `json:"children"`
}

// logData bundles the render context both log printers share: the child index,
// the current branch ("" when detached), per-branch drift, the tip/subject/
// ancestor maps from the batched cat-file reads, and worktree annotations.
type logData struct {
	index     map[string][]string
	cur       string
	drift     map[string]bool
	tips      map[string]string
	subjects  map[string]string
	ancestors map[ancestorPair]bool
	wtInfo    map[string]worktreeInfo
}

func printLogJSON(s *stack.State, d logData) error {
	var build func(name, parent string) *logNode
	build = func(name, parent string) *logNode {
		node := &logNode{Name: name, Parent: parent, Current: name == d.cur, Children: []*logNode{}}
		if b, ok := s.Get(name); ok {
			node.ParentSHA = b.ParentSHA
			node.NeedsRestack = d.drift[name]
			if subject, ok := topSubject(b, d.tips, d.subjects, d.ancestors); ok {
				node.TopCommit = subject
			}
		}
		if wt, ok := d.wtInfo[name]; ok {
			node.Worktree = wt.path
			node.Dirty = wt.dirty
		}
		for _, child := range d.index[name] {
			node.Children = append(node.Children, build(child, name))
		}
		return node
	}
	root := build(s.Trunk, "")
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out("%s\n", data)
	return nil
}

// printLogTree prints the forest with the deepest branches first so the trunk
// ends up at the bottom of the output.
func printLogTree(s *stack.State, d logData) {
	var printBranch func(name string, depth int)
	printBranch = func(name string, depth int) {
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

		if b, ok := s.Get(name); ok {
			if d.drift[name] {
				line += " " + paint("(needs restack)", ansiYellow)
			}
			if subject, ok := topSubject(b, d.tips, d.subjects, d.ancestors); ok {
				line += "  " + paint(sanitizeForTerminal(subject), ansiDim)
			}
		}
		if wt, ok := d.wtInfo[name]; ok {
			safePath := sanitizeForTerminal(wt.path)
			tag := "(worktree: " + safePath + ")"
			if wt.dirty {
				tag = "(worktree: " + safePath + ", dirty)"
			}
			line += " " + paint(tag, ansiCyan)
		}
		out("%s\n", line)
	}
	printBranch(s.Trunk, 0)
}

// ancestorPair is one ancestry question: is the child's tip reachable from the
// parent's tip (i.e. an ancestor-or-equal of it). Branches that share a tip and
// parent share one answer, so the cache is keyed by SHAs, not branch names.
type ancestorPair struct {
	tip       string
	parentTip string
}

// tipAncestors answers the topCommit visibility question for every rendered
// branch with at most one bounded `merge-base --is-ancestor` probe per
// distinct tip pair — instead of materializing the whole reachable history in
// a `rev-list --parents` graph. Missing or equal tips need no probe at all, so
// a trunk-only log asks git nothing.
func tipAncestors(s *stack.State, tips map[string]string) (map[ancestorPair]bool, error) {
	ancestors := make(map[ancestorPair]bool)
	for _, b := range s.Branches {
		tip := tips[b.Name]
		parentTip := tips[b.Parent]
		if tip == "" || parentTip == "" || tip == parentTip {
			continue
		}
		pair := ancestorPair{tip: tip, parentTip: parentTip}
		if _, seen := ancestors[pair]; seen {
			continue
		}
		// Fully-qualified refs skip IsAncestor's branch-existence probes —
		// one merge-base spawn per pair, not three — while the SHA-keyed
		// cache keeps the answer pinned to the measured tips.
		isAncestor, err := git.IsAncestor("refs/heads/"+b.Name, "refs/heads/"+b.Parent)
		if err != nil {
			return nil, err
		}
		ancestors[pair] = isAncestor
	}
	return ancestors, nil
}

// topSubject returns the subject of b's tip commit and whether one should be
// shown, from prefetched maps and the resolved tip-pair ancestry answers. A
// branch with no commits beyond its parent shows nothing — matching the old
// per-branch `git log parent..branch`, which returned an empty range there.
func topSubject(b *stack.Branch, tips, subjects map[string]string, ancestors map[ancestorPair]bool) (string, bool) {
	tip, ok := tips[b.Name]
	if !ok || tip == "" {
		return "", false
	}
	parentTip, ok := tips[b.Parent]
	if !ok || parentTip == "" || tip == parentTip || ancestors[ancestorPair{tip: tip, parentTip: parentTip}] {
		return "", false
	}
	subject, ok := subjects[b.Name]
	if !ok || subject == "" {
		return "", false
	}
	return subject, true
}
