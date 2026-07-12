package cmd

func init() {
	register(&Command{
		Name:    "guide",
		Summary: "Print the recommended stacked-diff workflow",
		Usage:   "st guide [--json]",
		Run:     runGuide,
	})
}

var guideSteps = []string{
	"st init                      # one-time: start tracking stacks in this repo",
	"st create feat-a -a -m '...' # make a change as a new branch on top of the current one",
	"st create feat-b -a -m '...' # stack another change on top",
	"st log                       # see the stack (trunk at the bottom)",
	"st down / st up              # move down/up the stack to edit a branch",
	"st modify                    # amend the current branch; descendants auto-restack",
	"st restack --dry-run         # preview what a restack would rebase (nothing changes)",
	"st restack                   # rebase after drift; on conflict -> st continue / st abort",
	"st sync                      # fast-forward trunk, prune merged branches, restack",
	"st submit                    # push the stack (login-free; prints the repo URL)",
	"st undo                      # revert the last stack-mutating command",
	"# parallel work (one worktree per branch):",
	"st shell install             # once per shell: eval its output so st can cd between worktrees",
	"st create feat-c --worktree  # new stacked branch straight into its own worktree",
	"st worktree feat-a           # give an existing branch its own worktree",
	"st worktree ls               # see which branch lives in which worktree",
	"st log --json                # orchestrators: worktree/dirty/needsRestack per branch",
}

// runGuide prints the recommended workflow for driving st (handy for agents).
func runGuide(args []string) error {
	asJSON, err := parsePlain("guide", args)
	if err != nil {
		return err
	}

	payload := struct {
		Steps []string `json:"steps"`
		Docs  string   `json:"docs"`
	}{guideSteps, "`st help <command>` for command usage"}

	return emit(asJSON, payload, func() {
		out("stacked — recommended workflow\n\n")
		for _, step := range guideSteps {
			out("  %s\n", step)
		}
		out("\nEvery command except completion and shell accepts --json; failures use exit codes %s.\n", errorCodeSummary())
		out("Use `st help <command>` for command usage.\n")
	})
}
