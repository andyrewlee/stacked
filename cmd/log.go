package cmd

import (
	"encoding/json"
	"flag"
	"fmt"

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
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "output the stack as JSON")
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: st log [--json]") }
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

	// The current branch may be unavailable (detached HEAD); that is not fatal
	// for rendering, so treat it as "no marker".
	cur, _ := currentBranch()
	index := s.ChildIndex()

	if asJSON {
		return printLogJSON(s, index, cur)
	}
	printLogTree(s, index, cur)
	return nil
}

// logNode is the JSON shape of a branch in the stack tree.
type logNode struct {
	Name         string     `json:"name"`
	Parent       string     `json:"parent,omitempty"`
	ParentSHA    string     `json:"parentSHA,omitempty"`
	Current      bool       `json:"current"`
	NeedsRestack bool       `json:"needsRestack"`
	TopCommit    string     `json:"topCommit,omitempty"`
	Children     []*logNode `json:"children,omitempty"`
}

func printLogJSON(s *stack.State, index map[string][]string, cur string) error {
	var build func(name, parent string) *logNode
	build = func(name, parent string) *logNode {
		node := &logNode{Name: name, Parent: parent, Current: name == cur}
		if b, ok := s.Get(name); ok {
			node.ParentSHA = b.ParentSHA
			node.NeedsRestack = needsRestack(s, name)
			if subject, ok := topSubject(s, b); ok {
				node.TopCommit = subject
			}
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
func printLogTree(s *stack.State, index map[string][]string, cur string) {
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
			if needsRestack(s, name) {
				line += " " + paint("(needs restack)", ansiYellow)
			}
			if subject, ok := topSubject(s, b); ok {
				line += "  " + paint(subject, ansiDim)
			}
		}
		out("%s\n", line)
	}
	printBranch(s.Trunk, 0)
}

// needsRestack reports whether name has drifted from its recorded parent tip.
// Any error (e.g. a missing parent ref) is treated as "no" so log stays robust.
func needsRestack(s *stack.State, name string) bool {
	need, err := s.NeedsRestack(gitShell, name)
	return err == nil && need
}

// topSubject returns the subject of b's most recent commit relative to its
// parent branch, and whether one was found.
func topSubject(s *stack.State, b *stack.Branch) (string, bool) {
	subjects, err := git.CommitSubjects(b.Parent, b.Name)
	if err != nil || len(subjects) == 0 {
		return "", false
	}
	return subjects[0], true
}
