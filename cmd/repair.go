package cmd

import "github.com/andyrewlee/stacked/internal/stack"

func init() {
	register(&Command{
		Name:    "repair",
		Summary: "Reconcile the stack metadata with the repository (fix drift)",
		Usage:   "st repair [--json]",
		Run:     runRepair,
	})
}

// runRepair fixes the inconsistencies that `st validate` reports: it untracks
// branches whose git branch was deleted outside st (re-parenting their children),
// re-parents branches whose parent is no longer valid onto the trunk, and breaks
// parent cycles by re-parenting onto the trunk. Re-parented branches may then
// need `st restack`. The repair itself lives in the engine (stack.Repair); this
// adapter renders its fixes as the {repaired, fixes} JSON.
func runRepair(args []string) error {
	asJSON, err := parsePlain("repair", args)
	if err != nil {
		return err
	}

	var fixes []string
	if err := mutateState("repair", asJSON, func(env stack.Env, s *stack.State) error {
		res, err := stack.Repair(env, s)
		if err != nil {
			return err
		}
		fixes = res.Notes
		return nil
	}); err != nil {
		return err
	}

	if fixes == nil {
		fixes = []string{}
	}
	payload := struct {
		Repaired bool     `json:"repaired"`
		Fixes    []string `json:"fixes"`
	}{len(fixes) > 0, fixes}
	return emit(asJSON, payload, func() {
		if len(fixes) == 0 {
			out("nothing to repair; the stack is consistent with the repository\n")
			return
		}
		out("repaired:\n")
		for _, f := range fixes {
			out("  - %s\n", sanitizeForTerminal(f))
		}
		out("run `st restack` to rebase any re-parented branches onto their new parents.\n")
	})
}
