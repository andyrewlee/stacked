package cmd

// st __complete — the hidden, read-only endpoint the generated shell
// completion scripts call once per keystroke for branch-name candidates. It is
// deliberately outside every listing surface (help, help --json, word-1
// suggestions, did-you-mean): the double-underscore name marks it as plumbing.

import (
	"flag"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "__complete",
		Usage:      "st __complete <command> <index> -- <words...>",
		Hidden:     true,
		NewFlagSet: emptyFlagSet("__complete"),
		Run:        runCompleteEndpoint,
	})
}

// completionCtx carries everything a command's Completion rule can need: where
// the cursor sits (inside a flag's value, or at the positional index implied by
// prior positionals plus the flag names already present), the loaded stack
// state, and the git inventories — fetched lazily through the methods so each
// command pays only for the probes its rule reads. A nil probe result means
// the fetch failed; completers return nil on it, which is the endpoint's
// standard silent-empty outcome.
type completionCtx struct {
	flagName    string
	positionals []string
	seen        map[string]bool
	s           *stack.State

	locals      map[string]string
	localsDone  bool
	wts         []git.Worktree
	wtsDone     bool
	cur         string
	curDone     bool
	fetchLocals func() map[string]string
	fetchWts    func() []git.Worktree
	fetchCur    func() string
}

// localTips returns every local branch -> tip, or nil when the probe failed.
func (c *completionCtx) localTips() map[string]string {
	if !c.localsDone {
		c.locals, c.localsDone = c.fetchLocals(), true
	}
	return c.locals
}

// worktrees returns the repository's worktrees, or nil when the probe failed.
func (c *completionCtx) worktrees() []git.Worktree {
	if !c.wtsDone {
		c.wts, c.wtsDone = c.fetchWts(), true
	}
	return c.wts
}

// current returns the checked-out branch, or "" on detached HEAD or failure.
func (c *completionCtx) current() string {
	if !c.curDone {
		c.cur, c.curDone = c.fetchCur(), true
	}
	return c.cur
}

// runCompleteEndpoint prints candidate branch names, one per line, for the
// word under the cursor. Protocol:
//
//	st __complete <command-or-alias> <index> -- <prior words...>
//
// <index> is the count of words between the command word and the cursor; the
// generated calls always pass every prior word, so the endpoint requires
// index == len(words) rather than trusting the number blindly. Anything that
// is not malformed argv is silent success: unknown command, outside a repo,
// uninitialized/unreadable/future state, detached HEAD, probe failures — all
// print nothing and exit 0, because a completion that pollutes the tty is
// worse than none. It never takes the lock and never writes: the reads are
// the state file plus at most two flat git probes (for-each-ref or worktree
// list, and the HEAD lookup onto needs) — no fetch, no history walk.
func runCompleteEndpoint(args []string) error {
	if len(args) < 3 || args[2] != "--" {
		return fmt.Errorf("usage: st __complete <command> <index> -- <words...>")
	}
	idx, err := strconv.Atoi(args[1])
	words := args[3:]
	if err != nil || idx != len(words) {
		return fmt.Errorf("usage: st __complete <command> <index> -- <words...>")
	}
	c, ok := byName[args[0]]
	if !ok || c.Hidden || c.Completion == nil {
		return nil
	}
	flagName, positionals, seen := completionCursor(completionFlagSet(c), words)

	s, err := stack.Load()
	if err != nil {
		return nil
	}
	cc := &completionCtx{
		flagName:    flagName,
		positionals: positionals,
		seen:        seen,
		s:           s,
		fetchLocals: func() map[string]string { l, _ := git.Tips(); return l },
		fetchWts:    func() []git.Worktree { w, _ := git.Worktrees(); return w },
		fetchCur:    func() string { b, _ := currentBranch(); return b },
	}
	for _, name := range c.Completion(cc) {
		if completableName(name) {
			out("%s\n", name)
		}
	}
	return nil
}

// completionFlagSet builds the command's real flag set for the positional
// classifier — the same NewFlagSet constructor Run parses with, or the
// --json-only default for commands that declare nothing extra. Value-taking
// flags are detected via IsBoolFlag, so a flag added in flagsets.go changes
// the parse here automatically.
func completionFlagSet(c *Command) *flag.FlagSet {
	if c.NewFlagSet != nil {
		return c.NewFlagSet()
	}
	var asJSON bool
	return newFlagSet(c.Name, &asJSON)
}

// completionCursor replays parseArgs' flag/positional classification over the
// words before the cursor: "--" terminates flag parsing, a declared non-bool
// flag consumes the next word as its value (so does an unknown flag — the real
// parser reshuffles it the same way before erroring), a -N numeric token is a
// positional, and any other flag-shaped token before the terminator consumes
// nothing. It reports the name of the flag whose VALUE the cursor is
// completing ("" when the cursor sits on a positional), the positional words
// seen so far, and the flag names already present.
func completionCursor(fs *flag.FlagSet, words []string) (flagName string, positionals []string, seen map[string]bool) {
	seen = map[string]bool{}
	terminated := false
	for i := 0; i < len(words); i++ {
		a := words[i]
		if !terminated {
			if a == "--" {
				terminated = true
				continue
			}
			if len(a) > 1 && a[0] == '-' && !looksNumeric(a) {
				name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
				seen[name] = true
				if !strings.Contains(a, "=") && !isBoolFlag(fs, a) {
					i++ // a value-taking flag swallows the next word
					if i >= len(words) {
						return name, positionals, seen
					}
				}
				continue
			}
		}
		positionals = append(positionals, a)
	}
	return "", positionals, seen
}

// completeCandidates is the pure-policy view of a command's Completion rule —
// it feeds the literal inventories tests supply into the registry's completer
// so the policy can be exercised without a repo. In production the endpoint
// calls c.Completion directly with lazily-fetched probes.
func completeCandidates(cmd, flagName string, positionals []string, seen map[string]bool,
	s *stack.State, locals map[string]string, wts []git.Worktree, cur string,
) []string {
	c, ok := byName[cmd]
	if !ok || c.Completion == nil {
		return nil
	}
	return c.Completion(&completionCtx{
		flagName:    flagName,
		positionals: positionals,
		seen:        seen,
		s:           s,
		fetchLocals: func() map[string]string { return locals },
		fetchWts:    func() []git.Worktree { return wts },
		fetchCur:    func() string { return cur },
	})
}

// trackedAndTrunk returns the trunk plus every tracked branch, sorted.
func trackedAndTrunk(s *stack.State) []string {
	names := make([]string, 0, len(s.Branches)+1)
	names = append(names, s.Trunk)
	for name := range s.Branches {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// trackedBranches returns every tracked branch, sorted — the domain of
// commands that act only on stack members (delete, untrack): the trunk is
// never among them.
func trackedBranches(s *stack.State) []string {
	names := make([]string, 0, len(s.Branches))
	for name := range s.Branches {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// minusNames drops every name in excl, preserving order.
func minusNames(names []string, excl map[string]bool) []string {
	kept := names[:0]
	for _, n := range names {
		if !excl[n] {
			kept = append(kept, n)
		}
	}
	return kept
}

// linkedOwnerNames returns the set of branches owning a LINKED worktree — the
// main worktree's branch (usually trunk) is not "its own worktree" for `st
// worktree` purposes, matching LinkedOwnerOf.
func linkedOwnerNames(wts []git.Worktree) map[string]bool {
	main, _ := stack.MainWorktree(wts)
	owners := map[string]bool{}
	for _, wt := range wts {
		if wt.Branch != "" && wt.Path != main.Path {
			owners[wt.Branch] = true
		}
	}
	return owners
}

func sortedKeys(set map[string]bool) []string {
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// completableName reports whether name can occupy one output line without
// breaking the protocol's newline framing or smuggling display-changing bytes
// into the terminal. Git refname rules already forbid control bytes and
// spaces; this is the contract's defensive check — drop the name rather than
// emit bytes a shell would mangle into a different branch. Rejects (never
// sanitizes — completion must echo the exact stored name): C0/DEL/C1
// controls, Unicode format chars (bidi overrides, zero-width), spaces, and
// invalid UTF-8 — the same class sanitizeForTerminal handles.
func completableName(name string) bool {
	if name == "" {
		return false
	}
	return !needsSanitizing(name, func(r rune) bool {
		return r == ' ' || isTerminalControl(r)
	})
}
