package cmd

import "flag"

// Introspection flag sets for `help --json`. Each declares exactly the flags its
// command's Run parses (Run still builds and binds its own set; these mirror it
// for introspection only). They are gathered here so the full set is reviewable
// at a glance, and TestHelpReportsOnlyRealFlags asserts every flag listed here is
// actually accepted by the command — so help can never advertise a flag the
// command rejects. Commands whose only flag is --json need no entry (help derives
// that from newFlagSet); completion has none.

func createFlagSet() *flag.FlagSet {
	var asJSON bool
	fs := newFlagSet("create", &asJSON)
	fs.String("m", "", "commit message for the new branch")
	fs.String("message", "", "commit message for the new branch")
	fs.Bool("a", false, "stage all changes before committing")
	fs.Bool("all", false, "stage all changes before committing")
	return fs
}

func modifyFlagSet() *flag.FlagSet {
	var asJSON bool
	fs := newFlagSet("modify", &asJSON)
	fs.String("m", "", "commit message")
	fs.String("message", "", "commit message")
	fs.Bool("a", true, "stage all tracked changes before amending/committing")
	fs.Bool("all", true, "stage all tracked changes before amending/committing")
	fs.Bool("commit", false, "create a new commit instead of amending the tip")
	return fs
}

func deleteFlagSet() *flag.FlagSet {
	var asJSON bool
	fs := newFlagSet("delete", &asJSON)
	fs.Bool("f", false, "force delete the branch even if not fully merged")
	fs.Bool("force", false, "force delete the branch even if not fully merged")
	return fs
}

func initFlagSet() *flag.FlagSet {
	var asJSON bool
	fs := newFlagSet("init", &asJSON)
	fs.String("trunk", "", "name of the trunk branch (default: detected)")
	return fs
}

func trackFlagSet() *flag.FlagSet {
	var asJSON bool
	fs := newFlagSet("track", &asJSON)
	fs.String("parent", "", "parent branch (trunk or a tracked branch)")
	return fs
}

func submitFlagSet() *flag.FlagSet {
	var asJSON bool
	fs := newFlagSet("submit", &asJSON)
	fs.String("remote", "origin", "remote to push to")
	fs.Bool("dry-run", false, "print what would be pushed without pushing")
	return fs
}

func syncFlagSet() *flag.FlagSet {
	var asJSON bool
	fs := newFlagSet("sync", &asJSON)
	fs.Bool("no-delete", false, "do not delete merged branches")
	fs.String("remote", "origin", "remote to fetch and fast-forward from")
	fs.Bool("dry-run", false, "show what would be pruned/restacked without changing anything")
	return fs
}

func restackFlagSet() *flag.FlagSet {
	var asJSON bool
	fs := newFlagSet("restack", &asJSON)
	fs.Bool("dry-run", false, "show what would be restacked without changing anything")
	return fs
}

func squashFlagSet() *flag.FlagSet {
	var asJSON bool
	fs := newFlagSet("squash", &asJSON)
	fs.String("m", "", "commit message for the squashed commit")
	fs.String("message", "", "commit message for the squashed commit")
	return fs
}
