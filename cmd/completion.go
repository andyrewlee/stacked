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

// runCompletion prints a shell completion script that completes st's subcommand
// names. The command list is generated from the live registry so new commands
// are picked up automatically.
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

	list := strings.Join(commandNames(), " ")
	switch rest[0] {
	case "bash":
		out(bashCompletion, list)
	case "zsh":
		out(zshCompletion, list)
	case "fish":
		var b strings.Builder
		for _, c := range registry {
			fmt.Fprintf(&b, "complete -c st -n __fish_use_subcommand -a %s -d %q\n", c.Name, c.Summary)
		}
		out("%s", b.String())
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

const bashCompletion = `# bash completion for st
_st_complete() {
    local cur="${COMP_WORDS[COMP_CWORD]}"
    if [ "$COMP_CWORD" -eq 1 ]; then
        COMPREPLY=( $(compgen -W "%s" -- "$cur") )
    fi
}
complete -F _st_complete st
`

const zshCompletion = `#compdef st
_st() {
    local -a cmds
    cmds=(%s)
    if (( CURRENT == 2 )); then
        compadd -- $cmds
    fi
}
_st "$@"
`
