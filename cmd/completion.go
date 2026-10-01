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
// pseudo-commands, sorted. Hidden machinery commands are never offered.
func commandNames() []string {
	names := make([]string, 0, len(registry)+2)
	for _, c := range registry {
		if c.Hidden {
			continue
		}
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

// fishBranchCandidatesHelper is the fish function the generated script defines
// once and calls per branch-taking command. `commandline -opc` yields the
// tokens up to the cursor INCLUDING a partially-typed current word (but not an
// empty one), so the helper drops the last token when it equals `commandline
// -t`; what remains after the command word is the prior-word list the endpoint
// expects. ST_COMPLETE_BIN overrides the binary the hook calls.
const fishBranchCandidatesHelper = `function __st_branch_candidates --description 'branch-name candidates from st __complete'
    set -l toks (commandline -opc)[2..-1]
    if test (count $toks) -eq 0
        return
    end
    set -l cmd $toks[1]
    set -e toks[1]
    set -l cur (commandline -t)
    if test (count $toks) -gt 0; and test "$toks[-1]" = "$cur"
        set toks $toks[1..-2]
    end
    set -l bin st
    if set -q ST_COMPLETE_BIN
        set bin $ST_COMPLETE_BIN
    end
    $bin __complete $cmd (count $toks) -- $toks
end
`

// fishCompletionScript generates the fish completion: canonical names (with
// summaries) at word 1; sub-verb/flag tokens keyed on the seen subcommand,
// aliases included; branch-taking commands additionally call __complete at
// completion time (fish runs the -a substitution lazily and treats its output
// as data — candidates are never evaluated).
func fishCompletionScript() string {
	var b strings.Builder
	b.WriteString(fishBranchCandidatesHelper)
	for _, c := range registry {
		if c.Hidden {
			continue
		}
		fmt.Fprintf(&b, "complete -c st -n __fish_use_subcommand -a %s -d %q\n", c.Name, c.Summary)
		names := strings.Join(append([]string{c.Name}, c.Aliases...), " ")
		if toks := commandCompletions(c); len(toks) > 0 {
			fmt.Fprintf(&b, "complete -c st -n \"__fish_seen_subcommand_from %s\" -a %q\n", names, strings.Join(toks, " "))
		}
		if c.Completion != nil {
			fmt.Fprintf(&b, "complete -c st -n \"__fish_seen_subcommand_from %s\" -a \"(__st_branch_candidates)\"\n", names)
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
		if c.Hidden {
			continue
		}
		toks := commandCompletions(c)
		if c.Completion != nil {
			// Branch names go through `while read`, never compgen -W: compgen
			// EXPANDS its wordlist, so a refname-legal candidate like
			// "$(id>pwned)" would be command-substituted. read -r keeps the
			// bytes literal; the [[ == $cur* ]] prefix test reproduces
			// compgen's filtering with $cur quoted (literal, not a pattern).
			fmt.Fprintf(&b, `        %s)
            local -a __st_dyn=()
            while IFS= read -r __st_line; do
                [[ $__st_line == "$cur"* ]] && __st_dyn+=("$__st_line")
            done < <("${ST_COMPLETE_BIN:-st}" __complete "${COMP_WORDS[1]}" $((COMP_CWORD - 2)) -- "${COMP_WORDS[@]:2:$((COMP_CWORD - 2))}")
            COMPREPLY=( $(compgen -W "%s" -- "$cur") "${__st_dyn[@]}" );;`+"\n",
				casePattern(c), strings.Join(toks, " "))
			continue
		}
		if len(toks) > 0 {
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
		if c.Hidden {
			continue
		}
		toks := commandCompletions(c)
		dyn := ""
		if c.Completion != nil {
			// "${(@f)$(...)}" splits the endpoint's output on newlines into
			// separate words without globbing — candidate bytes are data all
			// the way into compadd, so glob metacharacters in legal refnames
			// ([] {} , etc.) cannot expand.
			dyn = ` "${(@f)$("${ST_COMPLETE_BIN:-st}" __complete "${words[2]}" $((CURRENT - 3)) -- "${(@)words[3,CURRENT-1]}")}"`
		}
		if len(toks) > 0 || dyn != "" {
			fmt.Fprintf(&b, "        %s) compadd -- %s;;\n", casePattern(c), strings.TrimSpace(strings.Join(toks, " ")+dyn))
		}
	}
	b.WriteString("    esac\n}\n_st \"$@\"\n")
	return b.String()
}
