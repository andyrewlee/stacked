package cmd

import (
	"flag"
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
	fs := flag.NewFlagSet("continue", flag.ContinueOnError)
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "output the result as JSON")
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: st continue [--json]") }
	if err := fs.Parse(args); err != nil {
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
	if err := s.RecordUndo("continue"); err != nil {
		return err
	}

	res, err := stack.Continue(stackEnv(s), s)
	if err != nil {
		return err
	}
	if err := s.Save(); err != nil {
		return fmt.Errorf("saving stack state: %w", err)
	}
	return renderResult(res, asJSON)
}
