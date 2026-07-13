package cmd

import (
	"flag"
	"fmt"
	"sort"
	"strings"
)

func init() {
	register(&Command{
		Name:    "completion",
		Summary: "Print a shell completion script (bash|zsh|fish)",
		Usage:   "st completion <bash|zsh|fish>",
		Run:     runCompletion,
	})
}

// runCompletion prints a shell completion script. Command names come from the
// live registry; each command's second-word completions (flags + sub-verbs)
// come from the same NewFlagSet constructors help --json uses, so new commands
// and flags are picked up automatically.
func runCompletion(args []string) error {
	fs := flag.NewFlagSet("completion", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: st completion <bash|zsh|fish>") }
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		usageUnlessJSON(fs, args)
		return fmt.Errorf("completion requires one shell argument: bash, zsh, or fish")
	}

	switch rest[0] {
	case "bash":
		out("%s", bashCompletionScript())
	case "zsh":
		out("%s", zshCompletionScript())
	case "fish":
		out("%s", fishCompletionScript())
	default:
		return fmt.Errorf("unsupported shell %q (use bash, zsh, or fish)", rest[0])
	}
	return nil
}

// commandNames returns all primary command names plus the built-in help/version
// pseudo-commands, sorted.
func commandNames() []string {
	names := make([]string, 0, len(registry)+2)
	for _, c := range registry {
		names = append(names, c.Name)
	}
	names = append(names, "help", "version")
	sort.Strings(names)
	return names
}

// subVerbs maps a command to its literal sub-verbs (not derivable from the
// declared flags). Keep in sync with each command's Usage string.
var subVerbs = map[string][]string{
	"worktree":   {"list", "ls", "remove", "rm"},
	"shell":      {"install"},
	"completion": {"bash", "zsh", "fish"},
}

// commandCompletions returns the tokens completable AFTER a command name: its
// declared flags (rendered as -x / --xxx) plus its sub-verbs, deduped and
// sorted so the generated scripts are deterministic.
func commandCompletions(c *Command) []string {
	seen := map[string]bool{}
	var toks []string
	add := func(tok string) {
		if !seen[tok] {
			seen[tok] = true
			toks = append(toks, tok)
		}
	}
	for _, f := range commandFlags(c) { // nil for completion/shell: sub-verbs only
		add(flagToken(f.Name))
	}
	for _, v := range subVerbs[c.Name] {
		add(v)
	}
	sort.Strings(toks)
	return toks
}

// flagToken renders a flag name in its CLI form: single-char -> "-x", else "--xxx".
func flagToken(name string) string {
	if len(name) == 1 {
		return "-" + name
	}
	return "--" + name
}

// casePattern renders a command's case-arm pattern: the canonical name plus
// any aliases, pipe-joined ("worktree|wt"), so `st wt <TAB>` completes the
// same sub-verbs/flags as the canonical form. Word-1 completion deliberately
// stays canonical-only (advertising aliases there is noise).
func casePattern(c *Command) string {
	return strings.Join(append([]string{c.Name}, c.Aliases...), "|")
}

// fishCompletionScript generates the fish completion: canonical names (with
// summaries) at word 1; sub-verb/flag tokens keyed on the seen subcommand,
// aliases included.
func fishCompletionScript() string {
	var b strings.Builder
	for _, c := range registry {
		fmt.Fprintf(&b, "complete -c st -n __fish_use_subcommand -a %s -d %q\n", c.Name, c.Summary)
		if toks := commandCompletions(c); len(toks) > 0 {
			names := strings.Join(append([]string{c.Name}, c.Aliases...), " ")
			fmt.Fprintf(&b, "complete -c st -n \"__fish_seen_subcommand_from %s\" -a %q\n", names, strings.Join(toks, " "))
		}
	}
	return b.String()
}

// bashCompletionScript generates the bash completion: command names at word 1,
// then per-command flags/sub-verbs keyed on the chosen command.
func bashCompletionScript() string {
	var b strings.Builder
	b.WriteString("# bash completion for st\n_st_complete() {\n    local cur=\"${COMP_WORDS[COMP_CWORD]}\"\n")
	fmt.Fprintf(&b, "    if [ \"$COMP_CWORD\" -eq 1 ]; then\n        COMPREPLY=( $(compgen -W %q -- \"$cur\") )\n        return\n    fi\n", strings.Join(commandNames(), " "))
	b.WriteString("    case \"${COMP_WORDS[1]}\" in\n")
	for _, c := range registry {
		if toks := commandCompletions(c); len(toks) > 0 {
			fmt.Fprintf(&b, "        %s) COMPREPLY=( $(compgen -W %q -- \"$cur\") );;\n", casePattern(c), strings.Join(toks, " "))
		}
	}
	b.WriteString("    esac\n}\ncomplete -F _st_complete st\n")
	return b.String()
}

// zshCompletionScript generates the zsh completion with the same two levels.
func zshCompletionScript() string {
	var b strings.Builder
	b.WriteString("#compdef st\n_st() {\n    local -a cmds\n")
	fmt.Fprintf(&b, "    cmds=(%s)\n", strings.Join(commandNames(), " "))
	b.WriteString("    if (( CURRENT == 2 )); then\n        compadd -- $cmds\n        return\n    fi\n")
	b.WriteString("    case \"${words[2]}\" in\n")
	for _, c := range registry {
		if toks := commandCompletions(c); len(toks) > 0 {
			fmt.Fprintf(&b, "        %s) compadd -- %s;;\n", casePattern(c), strings.Join(toks, " "))
		}
	}
	b.WriteString("    esac\n}\n_st \"$@\"\n")
	return b.String()
}
