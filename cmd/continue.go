package cmd

import (
	"fmt"

	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "continue",
		Summary: "Resume a restack that was interrupted by a merge conflict",
		Usage:   "st continue [--json]",
		Run:     runContinue,
	})
}

func runContinue(args []string) error {
	var asJSON bool
	fs := newFlagSet("continue", &asJSON)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("continue", fs.Args()); err != nil {
		return err
	}

	s, release, err := lockAndLoad()
	if err != nil {
		return err
	}
	defer release()
	res, err := stack.Continue(stackEnv(s, asJSON), s)
	if err != nil {
		return err
	}
	if err := s.Save(); err != nil {
		return fmt.Errorf("saving stack state: %w", err)
	}
	return renderResult(res, asJSON)
}
