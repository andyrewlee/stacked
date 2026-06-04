// Package stack is the stacked-diff engine. It holds the metadata model (State,
// Branch) and topology helpers, the persistence (store.go) and undo journal
// (undo.go), and the git-aware operations — create, modify, restack, fold,
// squash, onto, delete, sync, continue (engine.go, restack.go). Operations reach
// the repository only through the Git and Remote ports (git.go), so they run in
// tests against an in-memory fake instead of spawning git.
package stack

import (
	"errors"
	"sort"
)

// ErrNotInitialized is returned when stacked has not been initialized in the
// current repository (no state file exists).
var ErrNotInitialized = errors.New("stacked is not initialized in this repo (run: st init)")

// Branch is a single tracked branch within a stack.
type Branch struct {
	// Name is the git branch name.
	Name string `json:"name"`
	// Parent is the parent BRANCH NAME; for a bottom branch this is the
	// trunk's name.
	Parent string `json:"parent"`
	// ParentSHA is the parent commit SHA this branch was last (re)based onto.
	ParentSHA string `json:"parentSHA"`
}

// PendingReparent records an onto operation whose rebase stopped on conflicts.
// The normal Branch metadata remains on the old parent until `st continue`
// completes the rebase and promotes this pending parent.
type PendingReparent struct {
	Branch    string `json:"branch"`
	Parent    string `json:"parent"`
	ParentSHA string `json:"parentSHA"`
}

// State is the complete stacked metadata for a repository: the trunk branch
// name and the set of tracked branches keyed by branch name.
type State struct {
	// Trunk is the name of the trunk branch (e.g. "main").
	Trunk string `json:"trunk"`
	// Branches maps a branch name to its tracked metadata.
	Branches map[string]*Branch `json:"branches"`
	// PendingReparent is set only while an onto rebase is paused on conflicts.
	PendingReparent *PendingReparent `json:"pendingReparent,omitempty"`
}

// Get returns the tracked Branch with the given name and whether it exists.
func (s *State) Get(name string) (*Branch, bool) {
	b, ok := s.Branches[name]
	return b, ok
}

// IsTracked reports whether name is a tracked branch (and not the trunk).
func (s *State) IsTracked(name string) bool {
	if name == s.Trunk {
		return false
	}
	_, ok := s.Branches[name]
	return ok
}

// Track upserts a branch into the state with the given parent branch name and
// parent commit SHA.
func (s *State) Track(name, parent, parentSHA string) {
	if s.Branches == nil {
		s.Branches = make(map[string]*Branch)
	}
	if b, ok := s.Branches[name]; ok {
		b.Parent = parent
		b.ParentSHA = parentSHA
		return
	}
	s.Branches[name] = &Branch{Name: name, Parent: parent, ParentSHA: parentSHA}
}

// Untrack removes a branch from the state.
func (s *State) Untrack(name string) {
	delete(s.Branches, name)
}

// Children returns the branches whose Parent equals name, sorted by Name.
func (s *State) Children(name string) []*Branch {
	var children []*Branch
	for _, b := range s.Branches {
		if b.Parent == name {
			children = append(children, b)
		}
	}
	sort.Slice(children, func(i, j int) bool {
		return children[i].Name < children[j].Name
	})
	return children
}

// Descendants returns all transitive children of name in topological order
// (parents appear before their children). The branch name itself is not
// included. Children at each level are visited in sorted-by-name order.
func (s *State) Descendants(name string) []string {
	index := s.ChildIndex()
	var result []string
	visited := map[string]bool{name: true}
	var walk func(parent string)
	walk = func(parent string) {
		for _, child := range index[parent] {
			if visited[child] {
				continue // guard against a corrupted state with a cycle
			}
			visited[child] = true
			result = append(result, child)
			walk(child)
		}
	}
	walk(name)
	return result
}

// ChildIndex returns a map from each branch name to its child branch names in
// sorted order, built in a single pass. Callers that walk the whole forest (log
// rendering, restacking) use it to avoid the repeated O(n) scans that calling
// Children once per node would incur.
func (s *State) ChildIndex() map[string][]string {
	idx := make(map[string][]string, len(s.Branches))
	names := make([]string, 0, len(s.Branches))
	for name := range s.Branches {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		idx[s.Branches[name].Parent] = append(idx[s.Branches[name].Parent], name)
	}
	return idx
}

// Ancestors returns the chain from the branch's parent up to and including the
// trunk, nearest first. If the branch is not tracked, the result is empty.
func (s *State) Ancestors(name string) []string {
	var result []string
	b, ok := s.Get(name)
	if !ok {
		return result
	}
	seen := map[string]bool{name: true}
	for {
		parent := b.Parent
		result = append(result, parent)
		if parent == s.Trunk || seen[parent] {
			return result // stop at trunk, or on a cycle in a corrupted state
		}
		seen[parent] = true
		next, ok := s.Get(parent)
		if !ok {
			return result
		}
		b = next
	}
}

// BottomOf walks Parent links until it reaches the branch whose Parent is the
// trunk and returns that branch's name. If name is the trunk or is not
// tracked, name is returned unchanged.
func (s *State) BottomOf(name string) string {
	if name == s.Trunk {
		return name
	}
	b, ok := s.Get(name)
	if !ok {
		return name
	}
	seen := map[string]bool{}
	for b.Parent != s.Trunk {
		if seen[b.Name] {
			return b.Name // guard against a cycle in a corrupted state
		}
		seen[b.Name] = true
		next, ok := s.Get(b.Parent)
		if !ok {
			return b.Name
		}
		b = next
	}
	return b.Name
}
