package cmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"stacked/internal/git"
	"stacked/internal/stack"
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
	graph, err := tipGraph(tips)
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

	if asJSON {
		return printLogJSON(s, index, cur, drift, tips, subjects, graph, wtInfo)
	}
	printLogTree(s, index, cur, drift, tips, subjects, graph, wtInfo)
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
	info := make(map[string]worktreeInfo, len(wts))
	for _, wt := range wts {
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

func printLogJSON(s *stack.State, index map[string][]string, cur string, drift map[string]bool, tips, subjects map[string]string, graph commitGraph, wtInfo map[string]worktreeInfo) error {
	var build func(name, parent string) *logNode
	build = func(name, parent string) *logNode {
		node := &logNode{Name: name, Parent: parent, Current: name == cur, Children: []*logNode{}}
		if b, ok := s.Get(name); ok {
			node.ParentSHA = b.ParentSHA
			node.NeedsRestack = drift[name]
			if subject, ok := topSubject(b, tips, subjects, graph); ok {
				node.TopCommit = subject
			}
		}
		if wt, ok := wtInfo[name]; ok {
			node.Worktree = wt.path
			node.Dirty = wt.dirty
		}
		for _, child := range index[name] {
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
func printLogTree(s *stack.State, index map[string][]string, cur string, drift map[string]bool, tips, subjects map[string]string, graph commitGraph, wtInfo map[string]worktreeInfo) {
	var printBranch func(name string, depth int)
	printBranch = func(name string, depth int) {
		for _, child := range index[name] {
			printBranch(child, depth+1)
		}

		indent := ""
		for i := 0; i < depth; i++ {
			indent += "  "
		}

		marker, label := "○", name
		if name == cur {
			marker = paint("◉", ansiBold, ansiGreen)
			label = paint(name, ansiBold, ansiGreen)
		}
		line := fmt.Sprintf("%s%s %s", indent, marker, label)

		if b, ok := s.Get(name); ok {
			if drift[name] {
				line += " " + paint("(needs restack)", ansiYellow)
			}
			if subject, ok := topSubject(b, tips, subjects, graph); ok {
				line += "  " + paint(subject, ansiDim)
			}
		}
		if wt, ok := wtInfo[name]; ok {
			tag := "(worktree: " + wt.path + ")"
			if wt.dirty {
				tag = "(worktree: " + wt.path + ", dirty)"
			}
			line += " " + paint(tag, ansiCyan)
		}
		out("%s\n", line)
	}
	printBranch(s.Trunk, 0)
}

type commitGraph map[string][]string

func tipGraph(tips map[string]string) (commitGraph, error) {
	seen := map[string]bool{}
	args := []string{"rev-list", "--parents"}
	for _, tip := range tips {
		if tip == "" || seen[tip] {
			continue
		}
		seen[tip] = true
		args = append(args, tip)
	}
	graph := commitGraph{}
	if len(args) == 2 {
		return graph, nil
	}
	out, err := git.Run(args...)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		graph[fields[0]] = fields[1:]
	}
	return graph, nil
}

func (g commitGraph) reachable(from, target string) bool {
	if from == "" || target == "" {
		return false
	}
	pending := []string{from}
	seen := map[string]bool{}
	for len(pending) > 0 {
		cur := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		if cur == target {
			return true
		}
		pending = append(pending, g[cur]...)
	}
	return false
}

// topSubject returns the subject of b's tip commit and whether one should be
// shown, from prefetched maps and one prefetched commit graph. A branch with no
// commits beyond its parent shows nothing — matching the old per-branch
// `git log parent..branch`, which returned an empty range there.
func topSubject(b *stack.Branch, tips, subjects map[string]string, graph commitGraph) (string, bool) {
	tip, ok := tips[b.Name]
	if !ok {
		return "", false
	}
	parentTip, ok := tips[b.Parent]
	if !ok || graph.reachable(parentTip, tip) {
		return "", false
	}
	subject, ok := subjects[b.Name]
	if !ok || subject == "" {
		return "", false
	}
	return subject, true
}
