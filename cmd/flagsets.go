package cmd

import (
	"flag"
	"fmt"
)

// Each command with flags beyond --json declares them ONCE here: newXxxFlags
// binds a *xxxOpts that its Run reads, and the no-arg xxxFlagSet() wrapper feeds
// the same declaration to help introspection (Command.NewFlagSet). So `help
// --json` reports exactly what Run parses, by construction — no second
// declaration to drift. Aliases (-m/--message, -a/--all, -f/--force, -s/--submit)
// bind to one field so command-line last-wins is preserved.

// emptyFlagSet declares the flag set for commands that take no flags at all
// (completion, __complete), so help introspection reports zero flags.
func emptyFlagSet(name string) func() *flag.FlagSet {
	return func() *flag.FlagSet { return flag.NewFlagSet(name, flag.ContinueOnError) }
}

// withDefaults gives a command's flag set a usage line plus its flag defaults,
// the help body the flag-rich commands print on -h.
func withDefaults(fs *flag.FlagSet, name string) *flag.FlagSet {
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), usageLine(name))
		fs.PrintDefaults()
	}
	return fs
}

type createOpts struct {
	asJSON  bool
	message string
	all     bool
	dryRun  bool
}

func newCreateFlags(o *createOpts) *flag.FlagSet {
	fs := newFlagSet("create", &o.asJSON)
	fs.StringVar(&o.message, "m", "", "commit message for the new branch")
	fs.StringVar(&o.message, "message", "", "commit message for the new branch")
	fs.BoolVar(&o.all, "a", false, "stage tracked changes before committing")
	fs.BoolVar(&o.all, "all", false, "stage tracked changes before committing")
	fs.BoolVar(&o.dryRun, "dry-run", false, "preview the branch without creating anything")
	return withDefaults(fs, "create")
}

func createFlagSet() *flag.FlagSet { return newCreateFlags(&createOpts{}) }

type undoOpts struct {
	asJSON bool
	force  bool
}

func newUndoFlags(o *undoOpts) *flag.FlagSet {
	fs := newFlagSet("undo", &o.asJSON)
	fs.BoolVar(&o.force, "force", false, "restore recorded tips even when a branch moved outside st since the command ran")
	return withDefaults(fs, "undo")
}

func undoFlagSet() *flag.FlagSet { return newUndoFlags(&undoOpts{}) }

type modifyOpts struct {
	asJSON  bool
	message string
	all     bool
	commit  bool
	dryRun  bool
}

func newModifyFlags(o *modifyOpts) *flag.FlagSet {
	fs := newFlagSet("modify", &o.asJSON)
	fs.StringVar(&o.message, "m", "", "commit message")
	fs.StringVar(&o.message, "message", "", "commit message")
	fs.BoolVar(&o.all, "a", false, "stage tracked changes before amending/committing")
	fs.BoolVar(&o.all, "all", false, "stage tracked changes before amending/committing")
	fs.BoolVar(&o.commit, "commit", false, "create a new commit instead of amending the tip")
	fs.BoolVar(&o.dryRun, "dry-run", false, "preview the amend/commit and its upstack restack without changing anything")
	return withDefaults(fs, "modify")
}

func modifyFlagSet() *flag.FlagSet { return newModifyFlags(&modifyOpts{}) }

type untrackOpts struct {
	asJSON bool
	dryRun bool
}

func newUntrackFlags(o *untrackOpts) *flag.FlagSet {
	fs := newFlagSet("untrack", &o.asJSON)
	fs.BoolVar(&o.dryRun, "dry-run", false, "preview the untrack and its child re-parenting without changing anything")
	return withDefaults(fs, "untrack")
}

func untrackFlagSet() *flag.FlagSet { return newUntrackFlags(&untrackOpts{}) }

type renameOpts struct {
	asJSON bool
	dryRun bool
}

func newRenameFlags(o *renameOpts) *flag.FlagSet {
	fs := newFlagSet("rename", &o.asJSON)
	fs.BoolVar(&o.dryRun, "dry-run", false, "preview the rename and its parent-pointer updates without changing anything")
	return withDefaults(fs, "rename")
}

func renameFlagSet() *flag.FlagSet { return newRenameFlags(&renameOpts{}) }

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
	all    bool
	parent string
	dryRun bool
}

func newTrackFlags(o *trackOpts) *flag.FlagSet {
	fs := newFlagSet("track", &o.asJSON)
	fs.BoolVar(&o.all, "all", false, "adopt every untracked local branch, inferring topology")
	fs.StringVar(&o.parent, "parent", "", "parent branch (trunk or a tracked branch)")
	fs.BoolVar(&o.dryRun, "dry-run", false, "with --all, preview the inferred parent map without tracking")
	return withDefaults(fs, "track")
}

func trackFlagSet() *flag.FlagSet { return newTrackFlags(&trackOpts{}) }

type submitOpts struct {
	asJSON bool
	all    bool
	upto   bool
	remote string
	draft  bool
	noPR   bool
	title  string
	body   string
	dryRun bool
}

func newSubmitFlags(o *submitOpts) *flag.FlagSet {
	fs := newFlagSet("submit", &o.asJSON)
	fs.BoolVar(&o.all, "all", false, "submit every tracked branch in the forest, not just the current stack")
	fs.BoolVar(&o.upto, "upto", false, "submit only up to the current branch (bottom..current), omitting upstack descendants")
	fs.StringVar(&o.remote, "remote", "origin", "remote to push to")
	fs.BoolVar(&o.draft, "draft", false, "create newly opened GitHub PRs as drafts")
	fs.BoolVar(&o.noPR, "no-pr", false, "push branches only; skip GitHub PR creation, retargeting, and stack-block updates")
	fs.StringVar(&o.title, "title", "", "explicit PR title when creating a PR for the current branch")
	fs.StringVar(&o.body, "body", "", "explicit PR body when creating a PR for the current branch")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print what would be pushed/synced without changing anything")
	return withDefaults(fs, "submit")
}

func submitFlagSet() *flag.FlagSet { return newSubmitFlags(&submitOpts{}) }

type syncOpts struct {
	asJSON   bool
	all      bool
	noDelete bool
	noFetch  bool
	submit   bool
	remote   string
	dryRun   bool
}

func newSyncFlags(o *syncOpts) *flag.FlagSet {
	fs := newFlagSet("sync", &o.asJSON)
	fs.BoolVar(&o.all, "all", false, "sync every tracked stack in the forest, not just the current stack")
	fs.BoolVar(&o.noDelete, "no-delete", false, "do not delete merged branches")
	fs.BoolVar(&o.noFetch, "no-fetch", false, "skip fetch/fast-forward; prune+restack against the local trunk")
	fs.BoolVar(&o.submit, "s", false, "push and update GitHub PRs for the surviving stack after syncing")
	fs.BoolVar(&o.submit, "submit", false, "push and update GitHub PRs for the surviving stack after syncing")
	fs.StringVar(&o.remote, "remote", "origin", "remote to fetch and fast-forward from")
	fs.BoolVar(&o.dryRun, "dry-run", false, "show what would be pruned/restacked without changing anything")
	return withDefaults(fs, "sync")
}

func syncFlagSet() *flag.FlagSet { return newSyncFlags(&syncOpts{}) }

type restackOpts struct {
	asJSON bool
	dryRun bool
	all    bool
}

func newRestackFlags(o *restackOpts) *flag.FlagSet {
	fs := newFlagSet("restack", &o.asJSON)
	fs.BoolVar(&o.dryRun, "dry-run", false, "show what would be restacked without changing anything")
	fs.BoolVar(&o.all, "all", false, "restack every tracked branch, not just the current one and its upstack")
	return withDefaults(fs, "restack")
}

func restackFlagSet() *flag.FlagSet { return newRestackFlags(&restackOpts{}) }

type ontoOpts struct {
	asJSON bool
	dryRun bool
}

func newOntoFlags(o *ontoOpts) *flag.FlagSet {
	fs := newFlagSet("onto", &o.asJSON)
	fs.BoolVar(&o.dryRun, "dry-run", false, "show what would be moved/restacked without changing anything")
	return withDefaults(fs, "onto")
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
	return withDefaults(fs, "squash")
}

func squashFlagSet() *flag.FlagSet { return newSquashFlags(&squashOpts{}) }

type logOpts struct {
	asJSON bool
	all    bool
	pr     bool
}

func newLogFlags(o *logOpts) *flag.FlagSet {
	fs := newFlagSet("log", &o.asJSON)
	fs.BoolVar(&o.all, "all", false, "show every tracked stack in the forest, not just the current stack")
	fs.BoolVar(&o.pr, "pr", false, "annotate branches with open GitHub PR status via gh")
	return withDefaults(fs, "log")
}

func logFlagSet() *flag.FlagSet { return newLogFlags(&logOpts{}) }

type statusOpts struct {
	asJSON bool
	pr     bool
}

func newStatusFlags(o *statusOpts) *flag.FlagSet {
	fs := newFlagSet("status", &o.asJSON)
	fs.BoolVar(&o.pr, "pr", false, "include open GitHub PR status for the current branch via gh")
	return withDefaults(fs, "status")
}

func statusFlagSet() *flag.FlagSet { return newStatusFlags(&statusOpts{}) }

type doctorOpts struct {
	asJSON bool
	fix    bool
	all    bool
}

func newDoctorFlags(o *doctorOpts) *flag.FlagSet {
	fs := newFlagSet("doctor", &o.asJSON)
	fs.BoolVar(&o.fix, "fix", false, "reconcile metadata inconsistencies automatically")
	fs.BoolVar(&o.all, "all", false, "check restack drift warnings for every stack, not just the current stack")
	return withDefaults(fs, "doctor")
}

func doctorFlagSet() *flag.FlagSet { return newDoctorFlags(&doctorOpts{}) }
