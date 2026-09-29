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
		Name:   "__complete",
		Usage:  "st __complete <command> <index> -- <words...>",
		Hidden: true,
		Run:    runCompleteEndpoint,
	})
}

// branchCompletionCommands are the commands whose positional grammar takes a
// branch name. The generated scripts embed a `st __complete` call for exactly
// these, and the endpoint's candidate policy switches on the same set — a
// command not listed here produces no dynamic candidates at all.
var branchCompletionCommands = map[string]bool{
	"checkout": true,
	"onto":     true,
	"track":    true,
	"worktree": true,
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
	if !ok || c.Hidden || !branchCompletionCommands[c.Name] {
		return nil
	}
	flagName, positionals, seen := completionCursor(completionFlagSet(c), words)

	s, err := stack.Load()
	if err != nil {
		return nil
	}
	var (
		locals map[string]string
		wts    []git.Worktree
		cur    string
	)
	switch c.Name {
	case "onto":
		if b, err := currentBranch(); err == nil {
			cur = b
		}
	case "track":
		l, err := git.Tips()
		if err != nil {
			return nil
		}
		locals = l
	case "worktree":
		w, err := git.Worktrees()
		if err != nil {
			return nil
		}
		wts = w
	}
	for _, name := range completeCandidates(c.Name, flagName, positionals, seen, s, locals, wts, cur) {
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

// completeCandidates is the pure policy behind __complete: the command's
// canonical name, where the cursor sits (a flag's value, or the positional
// index implied by the prior positionals), the loaded state, and the
// branch/worktree inventories in, sorted names out — never history, remotes,
// or the working tree.
func completeCandidates(cmd, flagName string, positionals []string, seen map[string]bool,
	s *stack.State, locals map[string]string, wts []git.Worktree, cur string,
) []string {
	if flagName != "" {
		// The one flag whose value is a branch name; other value flags
		// (messages, remotes, counts) get no candidates.
		if cmd == "track" && flagName == "parent" {
			return trackedAndTrunk(s)
		}
		return nil
	}
	posIdx := len(positionals)
	switch cmd {
	case "checkout":
		if posIdx == 0 {
			return trackedAndTrunk(s)
		}
	case "onto":
		if posIdx == 0 {
			// Moving a subtree onto itself is refused; every other branch —
			// trunk included — is a legal target.
			excl := map[string]bool{cur: true}
			for _, d := range s.Descendants(cur) {
				excl[d] = true
			}
			return minusNames(trackedAndTrunk(s), excl)
		}
	case "track":
		if posIdx == 0 && !seen["all"] {
			var names []string
			for name := range locals {
				if name != s.Trunk && !s.IsTracked(name) {
					names = append(names, name)
				}
			}
			sort.Strings(names)
			return names
		}
	case "worktree":
		if seen["all"] {
			return nil
		}
		owned := linkedOwnerNames(wts)
		if posIdx == 0 {
			// The create form offers tracked branches that lack their own
			// worktree; trunk owns the main worktree, so it never qualifies.
			excl := map[string]bool{s.Trunk: true}
			for n := range owned {
				excl[n] = true
			}
			return minusNames(trackedAndTrunk(s), excl)
		}
		if posIdx == 1 && (positionals[0] == "rm" || positionals[0] == "remove") {
			return sortedKeys(owned)
		}
	}
	return nil
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
// breaking the protocol's newline framing. Git refname rules already forbid
// control bytes and spaces; this is the contract's defensive check — drop the
// name rather than emit bytes a shell would mangle into a different branch.
func completableName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		if name[i] <= ' ' || name[i] == 0x7f {
			return false
		}
	}
	return true
}
