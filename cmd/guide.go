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
	"st create feat-a -a -m '...' # create a new branch stacked on the current one",
	"st create feat-b -a -m '...' # stack another branch on top",
	"st log [--pr] [--all]        # inspect the current stack (with optional GitHub PR status)",
	"st status [--pr]             # inspect current branch, commits, and PR status",
	"st down / st up / st bottom  # navigate the stack",
	"st modify [-a] [-m '...']    # amend (or --commit) and auto-restack descendants",
	"st restack [--dry-run]       # rebase after drift; on conflict -> st continue / st abort",
	"st submit [--draft]          # retarget PRs, force-push stack, create/update GitHub PRs",
	"st sync [--submit]           # fetch+ff trunk, prune merged PRs/branches, restack (and submit)",
	"st doctor [--fix]            # check stack consistency and drift (--fix to repair)",
	"st undo                      # revert the last stack-mutating command",
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
		out("\nEvery command except __complete and completion accepts --json; failures use exit codes %s.\n", errorCodeSummary())
		out("Use `st help <command>` for command usage.\n")
	})
}
