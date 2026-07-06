package cmd

import (
	"flag"
	"fmt"
)

// Each command with flags beyond --json declares them ONCE here: newXxxFlags
// binds a *xxxOpts that its Run reads, and the no-arg xxxFlagSet() wrapper feeds
// the same declaration to help introspection (Command.NewFlagSet). So `help
// --json` reports exactly what Run parses, by construction — no second
// declaration to drift. Aliases (-m/--message, -a/--all, -f/--force) bind to one
// field so command-line last-wins is preserved. (completion has no flags;
// commands whose only flag is --json need no entry — help derives that from
// newFlagSet.)

// withDefaults gives a command's flag set a usage line plus its flag defaults,
// the help body the five flag-rich commands print on -h.
func withDefaults(fs *flag.FlagSet, name string) *flag.FlagSet {
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), usageLine(name))
		fs.PrintDefaults()
	}
	return fs
}

type createOpts struct {
	asJSON   bool
	message  string
	all      bool
	worktree bool
}

func newCreateFlags(o *createOpts) *flag.FlagSet {
	fs := newFlagSet("create", &o.asJSON)
	fs.StringVar(&o.message, "m", "", "commit message for the new branch")
	fs.StringVar(&o.message, "message", "", "commit message for the new branch")
	fs.BoolVar(&o.all, "a", false, "stage all changes before committing")
	fs.BoolVar(&o.all, "all", false, "stage all changes before committing")
	fs.BoolVar(&o.worktree, "worktree", false, "create the branch in its own worktree instead of switching to it")
	return withDefaults(fs, "create")
}

func createFlagSet() *flag.FlagSet { return newCreateFlags(&createOpts{}) }

type modifyOpts struct {
	asJSON  bool
	message string
	all     bool
	commit  bool
}

func newModifyFlags(o *modifyOpts) *flag.FlagSet {
	fs := newFlagSet("modify", &o.asJSON)
	fs.StringVar(&o.message, "m", "", "commit message")
	fs.StringVar(&o.message, "message", "", "commit message")
	// Default to staging all changes so a bare invocation (and the "amend" alias)
	// behaves like "stage all + amend"; only -a=false disables it.
	fs.BoolVar(&o.all, "a", true, "stage all tracked changes before amending/committing")
	fs.BoolVar(&o.all, "all", true, "stage all tracked changes before amending/committing")
	fs.BoolVar(&o.commit, "commit", false, "create a new commit instead of amending the tip")
	return withDefaults(fs, "modify")
}

func modifyFlagSet() *flag.FlagSet { return newModifyFlags(&modifyOpts{}) }

type deleteOpts struct {
	asJSON bool
	force  bool
	dryRun bool
}

func newDeleteFlags(o *deleteOpts) *flag.FlagSet {
	fs := newFlagSet("delete", &o.asJSON)
	fs.BoolVar(&o.force, "f", false, "force delete the branch even if not fully merged")
	fs.BoolVar(&o.force, "force", false, "force delete the branch even if not fully merged")
	fs.BoolVar(&o.dryRun, "dry-run", false, "show what would be deleted/restacked without changing anything")
	return withDefaults(fs, "delete")
}

func deleteFlagSet() *flag.FlagSet { return newDeleteFlags(&deleteOpts{}) }

type foldOpts struct {
	asJSON bool
	dryRun bool
}

func newFoldFlags(o *foldOpts) *flag.FlagSet {
	fs := newFlagSet("fold", &o.asJSON)
	fs.BoolVar(&o.dryRun, "dry-run", false, "show what would be folded/restacked without changing anything")
	return fs
}

func foldFlagSet() *flag.FlagSet { return newFoldFlags(&foldOpts{}) }

type initOpts struct {
	asJSON bool
	trunk  string
}

func newInitFlags(o *initOpts) *flag.FlagSet {
	fs := newFlagSet("init", &o.asJSON)
	fs.StringVar(&o.trunk, "trunk", "", "name of the trunk branch (default: detected)")
	return withDefaults(fs, "init")
}

func initFlagSet() *flag.FlagSet { return newInitFlags(&initOpts{}) }

type trackOpts struct {
	asJSON bool
	parent string
}

func newTrackFlags(o *trackOpts) *flag.FlagSet {
	fs := newFlagSet("track", &o.asJSON)
	fs.StringVar(&o.parent, "parent", "", "parent branch (trunk or a tracked branch)")
	return withDefaults(fs, "track")
}

func trackFlagSet() *flag.FlagSet { return newTrackFlags(&trackOpts{}) }

type submitOpts struct {
	asJSON bool
	remote string
	dryRun bool
}

func newSubmitFlags(o *submitOpts) *flag.FlagSet {
	fs := newFlagSet("submit", &o.asJSON)
	fs.StringVar(&o.remote, "remote", "origin", "remote to push to")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print what would be pushed without pushing")
	return fs
}

func submitFlagSet() *flag.FlagSet { return newSubmitFlags(&submitOpts{}) }

type syncOpts struct {
	asJSON   bool
	noDelete bool
	remote   string
	dryRun   bool
}

func newSyncFlags(o *syncOpts) *flag.FlagSet {
	fs := newFlagSet("sync", &o.asJSON)
	fs.BoolVar(&o.noDelete, "no-delete", false, "do not delete merged branches")
	fs.StringVar(&o.remote, "remote", "origin", "remote to fetch and fast-forward from")
	fs.BoolVar(&o.dryRun, "dry-run", false, "show what would be pruned/restacked without changing anything")
	return fs
}

func syncFlagSet() *flag.FlagSet { return newSyncFlags(&syncOpts{}) }

type restackOpts struct {
	asJSON bool
	dryRun bool
}

func newRestackFlags(o *restackOpts) *flag.FlagSet {
	fs := newFlagSet("restack", &o.asJSON)
	fs.BoolVar(&o.dryRun, "dry-run", false, "show what would be restacked without changing anything")
	return fs
}

func restackFlagSet() *flag.FlagSet { return newRestackFlags(&restackOpts{}) }

type ontoOpts struct {
	asJSON bool
	dryRun bool
}

func newOntoFlags(o *ontoOpts) *flag.FlagSet {
	fs := newFlagSet("onto", &o.asJSON)
	fs.BoolVar(&o.dryRun, "dry-run", false, "show what would be moved/restacked without changing anything")
	return fs
}

func ontoFlagSet() *flag.FlagSet { return newOntoFlags(&ontoOpts{}) }

type squashOpts struct {
	asJSON  bool
	message string
	dryRun  bool
}

func newSquashFlags(o *squashOpts) *flag.FlagSet {
	fs := newFlagSet("squash", &o.asJSON)
	fs.StringVar(&o.message, "m", "", "commit message for the squashed commit")
	fs.StringVar(&o.message, "message", "", "commit message for the squashed commit")
	fs.BoolVar(&o.dryRun, "dry-run", false, "show what would be squashed/restacked without changing anything")
	return fs
}

func squashFlagSet() *flag.FlagSet { return newSquashFlags(&squashOpts{}) }
