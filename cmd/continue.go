package cmd

import (
	"fmt"

	"github.com/andyrewlee/stacked/internal/stack"
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
	asJSON, err := parsePlain("continue", args)
	if err != nil {
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
