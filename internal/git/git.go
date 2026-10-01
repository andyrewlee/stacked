// Package git is a thin wrapper around the git command line via os/exec.
// All functions operate on the git repository containing the current working
// directory.
package git

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// gitEnv returns the environment for git invocations whose output is parsed:
// the current environment with the locale pinned to C, so git's messages and
// formatting never vary with the user's LANG/LC_* settings. Interactive
// invocations (RunInteractive, RebaseContinue) keep the inherited environment —
// their output goes to the user and is never parsed.
func gitEnv() []string {
	return append(os.Environ(), "LC_ALL=C")
}

// run executes "git args..." and returns the combined stdout/stderr output. On
// failure it returns an error whose message includes the git stderr so callers
// get an actionable diagnostic.
func run(args ...string) (string, error) {
	return runWith(nil, nil, args...)
}

// runWith is run with extra environment entries and an optional stdin payload,
// for plumbing calls (temp-index apply, commit-tree) that are driven by env
// vars and byte streams rather than flags.
func runWith(extraEnv []string, stdin []byte, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Env = append(gitEnv(), extraEnv...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return stdout.String(), fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), msg, err)
		}
		return stdout.String(), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}

// ok reports whether "git args..." exits successfully. It surfaces no error for
// a non-zero exit, which is used by predicate helpers like IsAncestor.
func ok(args ...string) bool {
	cmd := exec.Command("git", args...)
	cmd.Env = gitEnv()
	return cmd.Run() == nil
}

// Run runs "git args..." and returns the trimmed combined stdout. The returned
// error includes the git stderr on failure.
func Run(args ...string) (string, error) {
	out, err := run(args...)
	return strings.TrimSpace(out), err
}

// RunInteractive runs "git args..." with stdin, stdout and stderr inherited from
// the current process, which is required for operations (such as rebases) that
// may prompt or report conflicts interactively.
func RunInteractive(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// ErrDetachedHEAD is returned by CurrentBranch when HEAD is not on a branch.
var ErrDetachedHEAD = errors.New("not on a branch (detached HEAD); check out a branch first")

// validRefArg guards branch/remote names that are passed to git as bare
// positional arguments. Git itself forbids ref components that begin with "-"
// (check-ref-format), so any such value here is either corrupt state or an
// attempt to smuggle a flag (e.g. a state.json branch named "--exec=...").
// Rejecting it before exec keeps git from parsing data as options.
func validRefArg(kind, name string) error {
	if name == "" {
		return fmt.Errorf("%s name is empty", kind)
	}
	if name[0] == '-' {
		return fmt.Errorf("%s name %q is not a valid git ref name", kind, name)
	}
	return nil
}

// hasControlOrSpace reports whether s carries any byte that could break a
// record framing (space, C0 control incl. NL/NUL, or DEL). Legitimate git
// refnames and hex SHAs never contain these.
func hasControlOrSpace(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] <= 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// CheckBranchName reports whether name is a usable git branch name, deferring to
// git's own check-ref-format so the rules match exactly (no spaces, no "..",
// no trailing ".lock", etc.). It returns a friendly one-line error instead of
// letting the raw multi-line "fatal: ... is not a valid branch name" + advice
// hints leak out of `git branch`/`git checkout -b` further down the line.
func CheckBranchName(name string) error {
	if name == "" {
		return fmt.Errorf("branch name is empty")
	}
	if !ok("check-ref-format", "refs/heads/"+name) {
		return fmt.Errorf("%q is not a valid branch name", name)
	}
	return nil
}

// CurrentBranch returns the name of the currently checked-out branch. It returns
// ErrDetachedHEAD when HEAD is detached (for example, mid-rebase or sitting on a
// raw commit).
func CurrentBranch() (string, error) {
	out, err := Run("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if out == "HEAD" {
		return "", ErrDetachedHEAD
	}
	return out, nil
}

// BranchExists reports whether a local branch with the given name exists.
func BranchExists(name string) bool {
	return ok("show-ref", "--verify", "--quiet", "refs/heads/"+name)
}

// Tips returns the tip SHA of every local branch, keyed by branch name, in a
// single git invocation — so callers walking a whole forest (log, validate) do
// one spawn instead of two per branch. Full refnames are requested and the
// refs/heads/ prefix stripped, since short refnames can be ambiguous when a
// tag shares a branch's name.
func Tips() (map[string]string, error) {
	out, err := Run("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	if err != nil {
		return nil, err
	}
	tips := map[string]string{}
	if out == "" {
		return tips, nil
	}
	for _, line := range strings.Split(out, "\n") {
		ref, sha, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		tips[strings.TrimPrefix(ref, "refs/heads/")] = sha
	}
	return tips, nil
}

// TipsFor returns the tip SHA of the named local branches, keyed by branch
// name, using exact full-ref matches. Missing branches are omitted.
func TipsFor(names []string) (map[string]string, error) {
	unique, refs, err := scopedBranchRefs(names)
	if err != nil {
		return nil, err
	}
	tips := map[string]string{}
	if len(refs) == 0 {
		return tips, nil
	}
	args := append([]string{"for-each-ref", "--format=%(refname) %(objectname)"}, refs...)
	out, err := Run(args...)
	if err != nil {
		return nil, err
	}
	refToName := make(map[string]string, len(refs))
	for i, ref := range refs {
		refToName[ref] = unique[i]
	}
	for _, line := range strings.Split(out, "\n") {
		ref, sha, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if name, ok := refToName[ref]; ok {
			tips[name] = sha
		}
	}
	return tips, nil
}

// MergedInto returns the local branch names whose tips are ancestors of ref in
// one git invocation, matching `merge-base --is-ancestor <branch> <ref>` for
// every local branch.
func MergedInto(ref string) (map[string]bool, error) {
	if err := validRefArg("ref", ref); err != nil {
		return nil, err
	}
	out, err := Run("for-each-ref", "--format=%(refname)", "--merged", localBranchRef(ref), "refs/heads")
	if err != nil {
		return nil, err
	}
	merged := map[string]bool{}
	if out == "" {
		return merged, nil
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		merged[strings.TrimPrefix(line, "refs/heads/")] = true
	}
	return merged, nil
}

// ChangesContainedIn reports whether upstream's tree already contains every
// content change branch makes relative to their merge base — the squash-merge
// and fully-cherry-picked case that ancestry checks (MergedInto, `git cherry`)
// cannot see: branch's tip is no ancestor of upstream, yet merging it would
// add nothing.
//
// The check is exact tree-content equality, not a patch-id heuristic: P is the
// path set of `git diff --no-renames upstream...branch` (three-dot =
// merge-base to branch), and branch is contained iff `git diff --quiet
// upstream branch -- P` finds no disagreement on exactly those paths.
// --no-renames keeps BOTH sides of a rename in P: a branch renaming A to B
// also deletes A, and a path set listing only B would overlook that deletion
// when upstream merely copied A to B while keeping A. Equivalently, the
// three-way merge of
// branch into upstream would yield upstream's exact tree: at every contested
// path ours==theirs, so no conflict is possible and upstream's other changes
// are untouched. A branch whose squash-merge landed in a trunk that later
// moved those same files forward is NOT contained — the conservative
// direction, keeping the branch rather than guessing. An empty P (a branch
// carrying no net change) is contained. Two spawns, no worktree, no index.
func ChangesContainedIn(upstream, branch string) (bool, error) {
	if err := validRefArg("ref", upstream); err != nil {
		return false, err
	}
	if err := validRefArg("ref", branch); err != nil {
		return false, err
	}
	up := localBranchRef(upstream)
	br := localBranchRef(branch)
	out, err := run("diff", "--name-only", "--no-renames", "-z", up+"..."+br)
	if err != nil {
		return false, err
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return true, nil
	}
	args := append([]string{"diff", "--quiet", up, br, "--"}, paths...)
	cmd := exec.Command("git", args...)
	// Path names came from git itself; literal pathspecs keep a file named like
	// pathspec magic (":(glob)" etc.) from being reinterpreted on the way back
	// in.
	cmd.Env = append(gitEnv(), "GIT_LITERAL_PATHSPECS=1")
	err = cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git diff --quiet %s %s -- <changed paths>: %w", up, br, err)
}

// TipSubjectsFor returns the subject line of each named local branch's tip
// commit, keyed by branch name, in a single exact-ref cat-file invocation.
// Missing branches and non-commit objects are omitted.
func TipSubjectsFor(names []string) (map[string]string, error) {
	unique, refs, err := scopedBranchRefs(names)
	if err != nil {
		return nil, err
	}
	subjects := map[string]string{}
	if len(refs) == 0 {
		return subjects, nil
	}
	out, err := catFile(refs, "--batch")
	if err != nil {
		return nil, err
	}
	pos := 0
	for _, name := range unique {
		header, next, ok := readCatFileLine(out, pos)
		if !ok {
			return nil, fmt.Errorf("git cat-file --batch ended before ref %q", name)
		}
		pos = next
		fields := strings.Fields(header)
		if len(fields) == 2 && fields[1] == "missing" {
			continue
		}
		if len(fields) != 3 {
			return nil, fmt.Errorf("unexpected git cat-file --batch header %q", header)
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 {
			return nil, fmt.Errorf("unexpected git cat-file object size in header %q", header)
		}
		if pos+size > len(out) {
			return nil, fmt.Errorf("git cat-file --batch object for %q ended early", name)
		}
		body := out[pos : pos+size]
		pos += size
		if pos < len(out) && out[pos] == '\n' {
			pos++
		}
		if fields[1] != "commit" {
			continue
		}
		if subject, ok := commitSubject(body); ok {
			subjects[name] = subject
		}
	}
	if pos != len(out) {
		return nil, fmt.Errorf("git cat-file --batch returned trailing data")
	}
	return subjects, nil
}

func scopedBranchRefs(names []string) (unique, refs []string, err error) {
	seen := map[string]bool{}
	unique = make([]string, 0, len(names))
	refs = make([]string, 0, len(names))
	for _, name := range names {
		if err := validRefArg("branch", name); err != nil {
			return nil, nil, err
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		unique = append(unique, name)
		refs = append(refs, localBranchNameRef(name))
	}
	return unique, refs, nil
}

func catFile(stdin []string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"cat-file"}, args...)...)
	cmd.Env = gitEnv()
	input := strings.Join(stdin, "\n")
	if input != "" {
		input += "\n"
	}
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return stdout.Bytes(), fmt.Errorf("git cat-file %s: %s: %w", strings.Join(args, " "), msg, err)
		}
		return stdout.Bytes(), fmt.Errorf("git cat-file %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

func readCatFileLine(out []byte, pos int) (line string, next int, ok bool) {
	if pos >= len(out) {
		return "", pos, false
	}
	offset := bytes.IndexByte(out[pos:], '\n')
	if offset < 0 {
		return string(out[pos:]), len(out), true
	}
	end := pos + offset
	return string(out[pos:end]), end + 1, true
}

func commitSubject(body []byte) (string, bool) {
	_, message, ok := bytes.Cut(body, []byte("\n\n"))
	if !ok {
		return "", false
	}
	if end := bytes.IndexByte(message, '\n'); end >= 0 {
		message = message[:end]
	}
	return string(message), true
}

// Worktree describes a single git worktree linked to the repository, as
// reported by `git worktree list --porcelain`. The main worktree is included.
// Branch is the short branch name checked out there (empty when detached or
// bare); Head is the checked-out commit SHA.
// Hunk is one staged change region from `git diff --cached -U0`. Old* describe
// the pre-image (HEAD) side; New* the index side. OldN==0 marks a pure addition
// (no pre-image lines).
type Hunk struct {
	File     string `json:"file"`
	OldStart int    `json:"oldStart"`
	OldN     int    `json:"oldN"`
	NewStart int    `json:"newStart"`
	NewN     int    `json:"newN"`
}

// UnsupportedRecord describes one staged diff section absorb cannot classify
// as plain text hunks: binary content, mode changes, renames/copies, or a
// path git must quote (control bytes or quotes in the name). File is the
// best-known path for display.
type UnsupportedRecord struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// diffSection accumulates the per-section state of the diff parser. Header
// lines (---/+++/mode/rename/Binary) are only structural BEFORE the first @@
// of the section; after that, only "@@ " and "diff --git " matter — a -U0
// content line may legitimately begin with "--- a/" (a removed line reading
// "-- a/…"), and anchoring on section position keeps it from desyncing the
// file tracking.
type diffSection struct {
	open       bool
	file       string
	pendingOld string
	gitName    string // best-effort name from the "diff --git a/X b/X" line
	sawHunk    bool
	hunks      []Hunk
	modeChange bool
	newFile    bool
	deleted    bool
	rename     bool
	renameFrom string
	renameTo   string
	binary     bool
	quoted     bool
	unnamed    bool // a hunk arrived before any file header could name it
}

// diffCachedArgs is the one normalized staged-diff invocation shared by
// DiffCachedHunks and DiffCachedPatchFor, so the parser and
// the patch reassembler always consume the same byte stream. The explicit
// flags pin the machine grammar against user configuration: --no-color and
// --no-ext-diff/--no-textconv defeat color.ui, diff.external and textconv
// settings, and --src-prefix/--dst-prefix override diff.noprefix and
// diff.mnemonicPrefix (the newer --default-prefix shortcut is deliberately
// not used — the documented floor is Git 2.17). quotepath=false makes
// non-ASCII paths arrive raw; git still C-quotes paths carrying control
// bytes, quotes or backslashes, and those sections are refused rather than
// misparsed.
func diffCachedArgs() []string {
	return []string{
		"-c", "core.quotepath=false", "diff", "--cached", "-U0",
		"--no-color", "--no-ext-diff", "--no-textconv",
		"--src-prefix=a/", "--dst-prefix=b/",
	}
}

// stagedPathInventory returns every staged path as a raw NUL-separated set
// with rename detection disabled, so a rename's source and destination are
// both listed. DiffCachedHunks cross-checks its classified sections against
// this inventory: a staged entry no parsed section claimed must become an
// explicit refusal rather than silently missing from a zero-refusal plan.
func stagedPathInventory() (map[string]bool, error) {
	out, err := run("-c", "core.quotepath=false", "diff", "--cached", "--name-only", "-z", "--no-renames")
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths[p] = true
		}
	}
	return paths, nil
}

// DiffCachedHunks returns the staged text-change regions from `git diff
// --cached -U0` plus an UnsupportedRecord for every staged section that is
// not plain text hunks. The contract is classify-or-refuse: every staged
// change is represented in one of the two return slices, so a caller gating
// on "zero refusals" (absorb) really covers the whole staged diff. A
// deletion (+++ /dev/null) keeps the --- a/<path> name, since a deleted
// file's pre-image lines are still attributable. A mode change on a section
// that ALSO has text hunks keeps the hunks and adds a record: the hunks are
// attributable, but the mode bit would silently ride any whole-patch apply.
// A section with no hunks at all — an empty added or deleted file, or any
// other metadata-only change — yields a record too: it carries data the
// hunk stream cannot express. Finally the parsed sections are cross-checked
// against a rename-disabled staged-path inventory, so even a section shape
// this parser never imagined still ends in a refusal, never an omission.
func DiffCachedHunks() ([]Hunk, []UnsupportedRecord, error) {
	inventory, err := stagedPathInventory()
	if err != nil {
		return nil, nil, err
	}
	out, err := run(diffCachedArgs()...)
	if err != nil {
		return nil, nil, err
	}
	var hunks []Hunk
	var unsupported []UnsupportedRecord
	claimed := map[string]bool{}
	var sec diffSection
	flush := func() {
		if !sec.open {
			return
		}
		// Claim every name the section mentioned, so a rename accounts for
		// both its endpoints in the inventory.
		for _, p := range []string{sec.file, sec.pendingOld, sec.gitName, sec.renameFrom, sec.renameTo} {
			if p != "" {
				claimed[p] = true
			}
		}
		name := sec.file
		if name == "" {
			name = sec.pendingOld
		}
		if name == "" {
			name = sec.gitName
		}
		if name == "" {
			name = sec.renameTo
		}
		if name == "" {
			name = sec.renameFrom
		}
		refuse := func(reason string) {
			unsupported = append(unsupported, UnsupportedRecord{File: name, Reason: reason})
		}
		switch {
		case sec.binary:
			refuse("binary file")
		case sec.rename:
			refuse("rename")
		case sec.quoted:
			refuse("path needs quoting (control bytes or quotes in name)")
		case sec.unnamed:
			refuse("hunks without a usable file header")
		default:
			if sec.modeChange {
				refuse("mode change")
			}
			if len(sec.hunks) == 0 {
				// No hunks and no earlier refusal: the section still
				// carries a staged change the hunk stream cannot express.
				switch {
				case sec.newFile:
					refuse("empty new file")
				case sec.deleted:
					refuse("empty deleted file")
				case !sec.modeChange:
					refuse("metadata-only change")
				}
			}
			hunks = append(hunks, sec.hunks...)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			sec = diffSection{open: true}
			if i := strings.LastIndex(line, " b/"); i >= 0 {
				sec.gitName = line[i+3:]
			}
			if strings.Contains(line, ` "`) {
				sec.quoted = true
			}
		case !sec.open:
			if line != "" {
				return nil, nil, fmt.Errorf("git diff --cached: unexpected output before first section: %q", line)
			}
		case !sec.sawHunk && (strings.HasPrefix(line, "old mode ") || strings.HasPrefix(line, "new mode ")):
			sec.modeChange = true
		case !sec.sawHunk && strings.HasPrefix(line, "new file mode "):
			sec.newFile = true
		case !sec.sawHunk && strings.HasPrefix(line, "deleted file mode "):
			sec.deleted = true
		case !sec.sawHunk && strings.HasPrefix(line, "rename from "):
			sec.rename = true
			sec.renameFrom = strings.TrimPrefix(line, "rename from ")
		case !sec.sawHunk && strings.HasPrefix(line, "rename to "):
			sec.rename = true
			sec.renameTo = strings.TrimPrefix(line, "rename to ")
		case !sec.sawHunk && (strings.HasPrefix(line, "copy from ") || strings.HasPrefix(line, "copy to ")):
			sec.rename = true
		case !sec.sawHunk && (strings.HasPrefix(line, "Binary files ") || strings.HasPrefix(line, "GIT binary patch")):
			sec.binary = true
		case !sec.sawHunk && (strings.HasPrefix(line, `--- "`) || strings.HasPrefix(line, `+++ "`)):
			sec.quoted = true
		case !sec.sawHunk && strings.HasPrefix(line, "--- a/"):
			sec.pendingOld = strings.TrimPrefix(line, "--- a/")
		case !sec.sawHunk && strings.HasPrefix(line, "+++ b/"):
			sec.file = strings.TrimPrefix(line, "+++ b/")
		case !sec.sawHunk && strings.HasPrefix(line, "+++ /dev/null"):
			sec.file = sec.pendingOld // deletion: the pre-image name is the touched file
		case strings.HasPrefix(line, "@@ "):
			h, ok := parseHunkHeader(line)
			if !ok {
				continue
			}
			sec.sawHunk = true
			h.File = sec.file
			if h.File == "" {
				h.File = sec.pendingOld
			}
			if h.File == "" {
				sec.unnamed = true
				continue
			}
			sec.hunks = append(sec.hunks, h)
		}
	}
	flush()
	for p := range inventory {
		if !claimed[p] {
			unsupported = append(unsupported, UnsupportedRecord{File: p, Reason: "unaccounted staged change"})
		}
	}
	return hunks, unsupported, nil
}

// parseHunkHeader parses "@@ -<oldStart>[,<oldN>] +<newStart>[,<newN>] @@ ...";
// an absent ,<n> means n==1.
func parseHunkHeader(line string) (Hunk, bool) {
	rest := strings.TrimPrefix(line, "@@ ")
	end := strings.Index(rest, " @@")
	if end < 0 {
		return Hunk{}, false
	}
	fields := strings.Fields(rest[:end])
	if len(fields) != 2 || !strings.HasPrefix(fields[0], "-") || !strings.HasPrefix(fields[1], "+") {
		return Hunk{}, false
	}
	oldStart, oldN, ok1 := parseHunkRange(strings.TrimPrefix(fields[0], "-"))
	newStart, newN, ok2 := parseHunkRange(strings.TrimPrefix(fields[1], "+"))
	if !ok1 || !ok2 {
		return Hunk{}, false
	}
	return Hunk{OldStart: oldStart, OldN: oldN, NewStart: newStart, NewN: newN}, true
}

// parseHunkRange parses "<start>[,<n>]"; an absent ,<n> means n==1.
func parseHunkRange(spec string) (start, n int, ok bool) {
	n = 1
	numPart, countPart, hasCount := strings.Cut(spec, ",")
	start, err := strconv.Atoi(numPart)
	if err != nil {
		return 0, 0, false
	}
	if hasCount {
		n, err = strconv.Atoi(countPart)
		if err != nil {
			return 0, 0, false
		}
	}
	return start, n, true
}

// DiffCachedPatchFor returns a minimal unified diff containing ONLY the
// given staged hunks, reassembled from one `diff --cached -U0` capture: per
// file with ≥1 wanted hunk, the original header lines verbatim plus the
// wanted hunk blocks in original order. Hunks are keyed by the same
// (File, OldStart, OldN, NewStart, NewN) tuple DiffCachedHunks returns, so
// attribution entries join losslessly.
//
// Line-number subtlety: OldStart values are relative to the shared pre-image
// (HEAD) and never shift, but NewStart values assume EVERY staged hunk
// applied — omitting another target's earlier hunk in the same file shifts
// this hunk's post-image position by that hunk's net line delta. Each
// emitted hunk's NewStart is therefore corrected by the cumulative
// (NewN−OldN) of the file's OMITTED earlier hunks. The classify-or-refuse
// contract guarantees the stream holds only plain text-hunk sections
// whenever absorb's apply gate passes, so no binary/rename/mode cases arise
// here.
func DiffCachedPatchFor(want []Hunk) ([]byte, error) {
	out, err := run(diffCachedArgs()...)
	if err != nil {
		return nil, err
	}
	return assemblePatchFor(out, want), nil
}

// DiffCachedPatchesFor is the batched twin: ONE `diff --cached` capture feeds
// every target's assembly. Absorb's per-target loop would otherwise run the
// same full-index diff once per target — T identical captures + parses.
func DiffCachedPatchesFor(wantByTarget map[string][]Hunk) (map[string][]byte, error) {
	out, err := run(diffCachedArgs()...)
	if err != nil {
		return nil, err
	}
	patches := make(map[string][]byte, len(wantByTarget))
	for target, want := range wantByTarget {
		patches[target] = assemblePatchFor(out, want)
	}
	return patches, nil
}

// assemblePatchFor emits the minimal patch containing only the wanted hunks
// of the given `diff --cached` capture. The delta correction is per emitted
// patch, computed against the capture's OMITTED hunks — running it per target
// over a shared capture is sound because the refusal contract keeps targets'
// hunk sets line-disjoint.
func assemblePatchFor(out string, want []Hunk) []byte {
	wanted := make(map[Hunk]bool, len(want))
	for _, h := range want {
		wanted[h] = true
	}
	var buf strings.Builder
	lines := strings.Split(out, "\n")
	var headers []string // current section's header lines, verbatim
	var file, pendingOld string
	emittedHeader := false
	delta := 0 // cumulative NewN-OldN of omitted earlier hunks in this file
	i := 0
	for i < len(lines) {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "diff --git "):
			headers = headers[:0]
			file, pendingOld = "", ""
			emittedHeader = false
			delta = 0
			for i < len(lines) && !strings.HasPrefix(lines[i], "@@ ") {
				hl := lines[i]
				headers = append(headers, hl)
				switch {
				case strings.HasPrefix(hl, "--- a/"):
					pendingOld = strings.TrimPrefix(hl, "--- a/")
				case strings.HasPrefix(hl, "+++ b/"):
					file = strings.TrimPrefix(hl, "+++ b/")
				case strings.HasPrefix(hl, "+++ /dev/null"):
					file = pendingOld
				}
				i++
			}
		case strings.HasPrefix(line, "@@ "):
			h, ok := parseHunkHeader(line)
			end := i + 1
			for end < len(lines) && !strings.HasPrefix(lines[end], "@@ ") && !strings.HasPrefix(lines[end], "diff --git ") {
				end++
			}
			if ok {
				h.File = file
				if h.File == "" {
					h.File = pendingOld
				}
				if wanted[h] {
					if !emittedHeader {
						for _, hl := range headers {
							buf.WriteString(hl)
							buf.WriteByte('\n')
						}
						emittedHeader = true
					}
					buf.WriteString(formatHunkHeader(h.OldStart, h.OldN, h.NewStart-delta, h.NewN))
					buf.WriteByte('\n')
					for idx := i + 1; idx < end; idx++ {
						// Drop only the artifact empty element strings.Split
						// leaves after the stream's final newline.
						if idx == len(lines)-1 && lines[idx] == "" {
							continue
						}
						buf.WriteString(lines[idx])
						buf.WriteByte('\n')
					}
				} else {
					delta += h.NewN - h.OldN
				}
			}
			i = end
		default:
			i++
		}
	}
	return []byte(buf.String())
}

// formatHunkHeader renders "@@ -<old> +<new> @@" with git's single-line
// shorthand (",1" omitted).
func formatHunkHeader(oldStart, oldN, newStart, newN int) string {
	rng := func(start, n int) string {
		if n == 1 {
			return fmt.Sprintf("%d", start)
		}
		return fmt.Sprintf("%d,%d", start, n)
	}
	return fmt.Sprintf("@@ -%s +%s @@", rng(oldStart, oldN), rng(newStart, newN))
}

// AmendTipWithPatch rewrites branch's tip commit to also contain patch,
// without touching any worktree or the real index: the patch is applied to
// the tip's tree in a throwaway temporary index, a new tree and commit are
// built with plumbing (write-tree/commit-tree, preserving the tip's author,
// date, message, and parents), and the branch ref is moved with a
// compare-and-swap on the old tip. On any failure — including "patch does not
// apply to that tree" — the repository is untouched. Returns the new tip SHA.
func AmendTipWithPatch(branch string, patch []byte) (string, error) {
	if err := validRefArg("branch", branch); err != nil {
		return "", err
	}
	ref := localBranchNameRef(branch)
	// One log call reads everything: %H is the tip (replacing RevParse), %P
	// the space-separated parents (rev-list --parents preserved merge tips —
	// commit-tree -p per parent — though stack tips are single-parent in
	// practice), then author name/email/date and the raw message for reuse.
	metaOut, err := run("log", "-1", "--format=%H%x00%P%x00%an%x00%ae%x00%aD%x00%B", ref)
	if err != nil {
		return "", err
	}
	meta := strings.SplitN(metaOut, "\x00", 6)
	if len(meta) != 6 {
		return "", fmt.Errorf("unexpected commit metadata for %s", ref)
	}
	tip := meta[0]
	parents := strings.Fields(meta[1])
	if len(parents) == 0 {
		return "", fmt.Errorf("branch %q's tip is a root commit; cannot amend it via absorb", branch)
	}

	tmp, err := os.CreateTemp("", "st-absorb-index-")
	if err != nil {
		return "", fmt.Errorf("creating temporary index: %w", err)
	}
	indexFile := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(indexFile)
	indexEnv := []string{"GIT_INDEX_FILE=" + indexFile}
	if _, err := runWith(indexEnv, nil, "read-tree", tip); err != nil {
		return "", err
	}
	// --unidiff-zero matches DiffCachedPatch's -U0 capture; the hunk's own
	// pre-image lines (which attribution proved live in this tree) anchor it.
	if _, err := runWith(indexEnv, patch, "apply", "--cached", "--unidiff-zero", "-"); err != nil {
		return "", fmt.Errorf("staged patch does not apply cleanly to the tip of %q: %w", branch, err)
	}
	treeOut, err := runWith(indexEnv, nil, "write-tree")
	if err != nil {
		return "", err
	}
	commitEnv := []string{
		"GIT_AUTHOR_NAME=" + meta[2],
		"GIT_AUTHOR_EMAIL=" + meta[3],
		"GIT_AUTHOR_DATE=" + meta[4],
	}
	commitArgs := []string{"commit-tree", strings.TrimSpace(treeOut)}
	for _, p := range parents {
		commitArgs = append(commitArgs, "-p", p)
	}
	newTipOut, err := runWith(commitEnv, []byte(meta[5]), commitArgs...)
	if err != nil {
		return "", err
	}
	newTip := strings.TrimSpace(newTipOut)
	// Compare-and-swap on the old tip: a concurrent move of the branch fails
	// the whole amend instead of being clobbered.
	if _, err := run("update-ref", ref, newTip, tip); err != nil {
		return "", err
	}
	return newTip, nil
}

// ResetHardIn runs `git reset --hard <ref>` inside the worktree at dir (""
// means the current worktree). Callers must ensure nothing unsaved can be
// lost: absorb only calls it after the staged content is committed in the
// target branch (to drop the now-redundant staged copy), or on a
// verified-clean worktree to sync it to its amended HEAD.
func ResetHardIn(dir, ref string) error {
	if err := validRefArg("ref", ref); err != nil {
		return err
	}
	var args []string
	if dir != "" {
		args = append(args, "-C", dir)
	}
	args = append(args, "reset", "--hard", ref)
	_, err := run(args...)
	return err
}

// BlameLine is one line's provenance from `git blame --line-porcelain`:
// Commit last touched it, OriginalLine is its number in THAT commit's
// version of the file, FinalLine its number at the blamed rev, and Path the
// name it carried in Commit — which differs from the queried path when the
// file was renamed since. An empty Path means the porcelain record carried
// no decodable filename; consumers must treat it as missing provenance and
// refuse rather than guess coordinates.
type BlameLine struct {
	Commit       string
	OriginalLine int
	FinalLine    int
	Path         string
}

// BlamePorcelain maps each final line of file at rev to its full
// provenance via one `git blame --line-porcelain` spawn. --line-porcelain
// (unlike --porcelain) repeats EVERY metadata record for every line, so each
// line's `filename` is present instead of relying on a commit-boundary
// repeat. Each line's header is `<40-hex> <origLine> <finalLine>` (content
// lines start with a TAB and metadata lines with a keyword, so the hex
// prefix is unambiguous). A line whose header or filename record is
// malformed is dropped from the map — callers treat the absence as
// unattributable, never as a reason to guess coordinates.
func BlamePorcelain(file, rev string) (map[int]BlameLine, error) {
	if err := validRefArg("ref", rev); err != nil {
		return nil, err
	}
	out, err := run("blame", "--line-porcelain", rev, "--", file)
	if err != nil {
		return nil, err
	}
	return parseBlamePorcelain(out), nil
}

// parseBlamePorcelain is BlamePorcelain's parser, split out so the porcelain
// grammar — line headers, per-line `filename` records (including Git's
// C-quoted names), and malformed input — can be exercised on canned fixtures
// without spawning git. The invariant: a line enters the map only with BOTH
// coordinates and a decoded Path; anything less is dropped so the caller
// fails closed.
func parseBlamePorcelain(out string) map[int]BlameLine {
	lines := map[int]BlameLine{}
	var cur *BlameLine
	flush := func() {
		if cur != nil && cur.Path != "" {
			lines[cur.FinalLine] = *cur
		}
		cur = nil
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) >= 42 && line[40] == ' ' && isHex40(line[:40]) {
			flush()
			if fields := strings.Fields(line[41:]); len(fields) >= 2 {
				orig, err1 := strconv.Atoi(fields[0])
				final, err2 := strconv.Atoi(fields[1])
				if err1 == nil && err2 == nil {
					cur = &BlameLine{Commit: line[:40], OriginalLine: orig, FinalLine: final}
				}
			}
			continue
		}
		if cur == nil {
			continue
		}
		if name, ok := strings.CutPrefix(line, "filename "); ok {
			p, ok := decodeBlameFilename(name)
			if !ok {
				cur = nil
				continue
			}
			cur.Path = p
			continue
		}
		if strings.HasPrefix(line, "\t") {
			flush()
		}
	}
	flush()
	return lines
}

// decodeBlameFilename decodes the value of a blame `filename ` record. Git
// C-quotes a name when it contains control bytes, '"', '\', or (under the
// default core.quotePath=true) non-ASCII bytes; the quoting uses the usual
// escapes plus octal \NNN, all of which strconv.Unquote decodes to the same
// bytes. Unquoted values are verbatim — never whitespace-split or trimmed,
// since a filename may itself contain spaces or quotes. The second result
// is false when the value claims quoting (leading '"') but is not a
// well-formed C string, so the caller fails closed rather than trust a
// partially-decoded path.
func decodeBlameFilename(v string) (string, bool) {
	if !strings.HasPrefix(v, "\"") {
		return v, true
	}
	p, err := strconv.Unquote(v)
	if err != nil {
		return "", false
	}
	return p, true
}

func isHex40(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

type Worktree struct {
	Path     string `json:"path"`
	Branch   string `json:"branch,omitempty"`
	Head     string `json:"head,omitempty"`
	Bare     bool   `json:"bare,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	Locked   bool   `json:"locked,omitempty"`
}

// Worktrees lists every worktree linked to the repository (the main worktree
// plus any added with `git worktree add`), in a single git invocation. It uses
// the NUL-terminated `-z` porcelain grammar (git 2.36+) so path bytes survive
// losslessly; on a git that does not know -z it falls back to the legacy
// line-based grammar, but only after proving from the worktrees/*/gitdir
// registration metadata that no registered path can smuggle record structure.
func Worktrees() ([]Worktree, error) {
	stdout, stderr, err := worktreeListZ()
	if err == nil {
		return parseWorktreesZ(stdout)
	}
	if !unsupportedWorktreeListZ(stderr, err) {
		return nil, fmt.Errorf("git worktree list --porcelain -z: %s: %w", strings.TrimSpace(stderr), err)
	}
	return worktreesLegacy()
}

// worktreeListZ runs `git worktree list --porcelain -z` with stdout and stderr
// captured separately so the caller can distinguish the pre-2.36 unsupported
// option diagnostic from a genuine failure.
func worktreeListZ() (stdout, stderr string, err error) {
	cmd := exec.Command("git", "worktree", "list", "--porcelain", "-z")
	cmd.Env = gitEnv()
	var so, se strings.Builder
	cmd.Stdout = &so
	cmd.Stderr = &se
	err = cmd.Run()
	return so.String(), se.String(), err
}

// unsupportedWorktreeListZ reports whether a -z failure is the C-locale
// unsupported-option diagnostic (usage exit 129 plus "unknown option" on
// stderr) that pre-2.36 git emits. Any other failure is a real error and must
// not trigger the legacy retry.
func unsupportedWorktreeListZ(stderr string, err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 129 {
		return false
	}
	return strings.Contains(stderr, "unknown option")
}

// worktreesLegacy is the guarded pre-2.36 path: prove the line grammar is
// unambiguous for this repository, then parse `worktree list --porcelain`
// strictly.
func worktreesLegacy() ([]Worktree, error) {
	registrations, err := checkLegacyWorktreeMetadata()
	if err != nil {
		return nil, err
	}
	out, err := run("worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	list, err := parseWorktreesLegacy(out)
	if err != nil {
		return nil, err
	}
	// Every linked worktree has a worktrees/<id>/gitdir registration; the main
	// worktree adds exactly one more record. A mismatch means the listing's
	// line structure cannot be reconciled with the registration metadata —
	// refuse rather than guess which records are real.
	if len(list) != registrations+1 {
		return nil, fmt.Errorf("git worktree list --porcelain reported %d records but %d gitdir registrations exist; "+
			"a worktree path likely contains bytes the legacy grammar cannot represent (upgrade to git >= 2.36 for `-z` support)",
			len(list), registrations)
	}
	return list, nil
}

// checkLegacyWorktreeMetadata proves the legacy line grammar is unambiguous for
// this repository: the common git directory, the main worktree path derived
// from it, and every raw worktrees/<id>/gitdir registration content must be
// free of CR/LF bytes (which would split or forge records). It returns the
// number of linked-worktree registrations validated.
func checkLegacyWorktreeMetadata() (int, error) {
	raw, err := run("rev-parse", "--git-common-dir")
	if err != nil {
		return 0, err
	}
	common := strings.TrimSuffix(raw, "\n")
	if strings.ContainsAny(common, "\r\n") {
		return 0, fmt.Errorf("git common directory %q contains CR/LF bytes; the legacy worktree listing cannot represent it "+
			"(upgrade to git >= 2.36 for `worktree list -z`)", common)
	}
	// The standard layout puts the common dir at <main worktree>/.git, so the
	// main worktree path — the first legacy record — is covered too. Layouts
	// where that does not hold (e.g. a .git-file pointer to an external dir)
	// cannot be proven safe here and are refused rather than guessed at.
	abs, err := filepath.Abs(common)
	if err != nil {
		return 0, fmt.Errorf("resolve git common directory %q: %w", common, err)
	}
	mainPath, ok := strings.CutSuffix(abs, string(filepath.Separator)+".git")
	if !ok {
		return 0, fmt.Errorf("git common directory %q is not <worktree>/.git; cannot prove the main worktree path is "+
			"safe for the legacy worktree listing (upgrade to git >= 2.36 for `worktree list -z`)", abs)
	}
	if strings.ContainsAny(mainPath, "\r\n") {
		return 0, fmt.Errorf("main worktree path %q contains CR/LF bytes; the legacy worktree listing cannot represent it "+
			"(upgrade to git >= 2.36 for `worktree list -z`)", mainPath)
	}
	regs, err := filepath.Glob(filepath.Join(common, "worktrees", "*", "gitdir"))
	if err != nil {
		return 0, fmt.Errorf("enumerate gitdir registrations under %q: %w", common, err)
	}
	for _, reg := range regs {
		data, err := os.ReadFile(reg)
		if err != nil {
			return 0, fmt.Errorf("read worktree registration %s: %w", reg, err)
		}
		// The gitdir file stores <worktree>/.git followed by a single trailing
		// newline. Strip exactly one terminator; any remaining CR/LF is path
		// content the line grammar cannot represent.
		content := strings.TrimSuffix(string(data), "\n")
		if strings.ContainsAny(content, "\r\n") {
			return 0, fmt.Errorf("worktree registration %s contains CR/LF bytes; the legacy worktree listing cannot "+
				"represent it (upgrade to git >= 2.36 for `worktree list -z`)", reg)
		}
	}
	return len(regs), nil
}

// parseWorktreesZ parses the NUL-terminated `git worktree list --porcelain -z`
// grammar: every attribute is NUL-terminated and records are separated by an
// empty attribute. A path therefore keeps its exact bytes — including newlines
// and text that would look like fields in the line grammar. Structural fields
// are validated; an incomplete or unrecognized record is an error rather than
// a silently dropped or fabricated worktree.
func parseWorktreesZ(out string) ([]Worktree, error) {
	if out == "" {
		return nil, nil
	}
	var (
		list []Worktree
		cur  *Worktree
	)
	flush := func() error {
		if cur == nil {
			return nil
		}
		if !cur.Bare && cur.Head == "" {
			return fmt.Errorf("git worktree list -z: record for %q has no HEAD and is not bare", cur.Path)
		}
		list = append(list, *cur)
		cur = nil
		return nil
	}
	for _, field := range strings.Split(out, "\x00") {
		if field == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		key, val, hasVal := strings.Cut(field, " ")
		switch key {
		case "worktree":
			if cur != nil {
				return nil, fmt.Errorf("git worktree list -z: new worktree record without a record separator")
			}
			if !hasVal || val == "" {
				return nil, fmt.Errorf("git worktree list -z: worktree record without a path")
			}
			cur = &Worktree{Path: val}
		case "HEAD":
			if cur == nil || !hasVal || val == "" {
				return nil, fmt.Errorf("git worktree list -z: malformed HEAD attribute %q", field)
			}
			cur.Head = val
		case "branch":
			if cur == nil || !hasVal || val == "" {
				return nil, fmt.Errorf("git worktree list -z: malformed branch attribute %q", field)
			}
			cur.Branch = strings.TrimPrefix(val, "refs/heads/")
		case "detached", "bare", "locked", "prunable":
			if cur == nil {
				return nil, fmt.Errorf("git worktree list -z: attribute %q before any worktree record", field)
			}
			switch key {
			case "detached":
				cur.Detached = true
			case "bare":
				cur.Bare = true
			case "locked":
				cur.Locked = true
			}
		default:
			return nil, fmt.Errorf("git worktree list -z: unrecognized attribute %q", field)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return list, nil
}

// parseWorktreesLegacy parses the pre-2.36 line-based `git worktree list
// --porcelain` grammar strictly: records are blank-line separated, every known
// attribute requires an open record, and unknown or duplicated structural
// lines are refused. It only runs after checkLegacyWorktreeMetadata has proven
// no registered path carries the bytes that would make the grammar ambiguous.
func parseWorktreesLegacy(out string) ([]Worktree, error) {
	if out == "" {
		return nil, nil
	}
	var (
		list []Worktree
		cur  *Worktree
	)
	flush := func() error {
		if cur == nil {
			return nil
		}
		if !cur.Bare && cur.Head == "" {
			return fmt.Errorf("git worktree list: record for %q has no HEAD and is not bare", cur.Path)
		}
		list = append(list, *cur)
		cur = nil
		return nil
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		key, val, hasVal := strings.Cut(line, " ")
		switch key {
		case "worktree":
			// Real porcelain always blank-separates records; a worktree line
			// inside an open record is a path fragment or corruption.
			if cur != nil {
				return nil, fmt.Errorf("git worktree list: worktree record without a blank-line separator")
			}
			if !hasVal || val == "" {
				return nil, fmt.Errorf("git worktree list: worktree record without a path")
			}
			cur = &Worktree{Path: val}
		case "HEAD":
			if cur == nil || !hasVal || val == "" {
				return nil, fmt.Errorf("git worktree list: malformed HEAD line %q", line)
			}
			cur.Head = val
		case "branch":
			if cur == nil || !hasVal || val == "" {
				return nil, fmt.Errorf("git worktree list: malformed branch line %q", line)
			}
			cur.Branch = strings.TrimPrefix(val, "refs/heads/")
		case "detached", "bare", "locked", "prunable":
			if cur == nil {
				return nil, fmt.Errorf("git worktree list: attribute line %q before any worktree record", line)
			}
			switch key {
			case "detached":
				cur.Detached = true
			case "bare":
				cur.Bare = true
			case "locked":
				cur.Locked = true
			}
		default:
			return nil, fmt.Errorf("git worktree list: unrecognized line %q", line)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return list, nil
}

// WorktreeAdd creates a new linked worktree at path checked out on the existing
// branch. The "--" separates options from the path/branch operands so a path
// beginning with a dash can never be read as an option.
func WorktreeAdd(path, branch string) error {
	if err := validRefArg("branch", branch); err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("worktree path is empty")
	}
	_, err := Run("worktree", "add", "--", path, branch)
	return err
}

// WorktreeRemove removes the linked worktree at path. When force is true the
// worktree is removed even if it has uncommitted changes.
func WorktreeRemove(path string, force bool) error {
	if path == "" {
		return fmt.Errorf("worktree path is empty")
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, "--", path)
	_, err := Run(args...)
	return err
}

// MergeFFOnlyIn runs "git -C <dir> merge --ff-only upstream", advancing the
// branch checked out in the worktree at dir (its working tree included) the
// way running the merge inside that worktree would.
func MergeFFOnlyIn(dir, upstream string) error {
	if dir == "" {
		return fmt.Errorf("worktree dir is empty")
	}
	if err := validRefArg("ref", upstream); err != nil {
		return err
	}
	_, err := run("-C", dir, "merge", "--ff-only", upstream)
	return err
}

// RebaseOntoIn runs "git -C <dir> rebase --onto newBase oldBase branch" with no
// inherited stdio, so a branch checked out in the worktree at dir is rebased by
// its owner (git refuses to rebase a branch checked out in another worktree).
func RebaseOntoIn(dir, newBase, oldBase, branch string) error {
	if dir == "" {
		return fmt.Errorf("worktree dir is empty")
	}
	if err := validRefArg("ref", newBase); err != nil {
		return err
	}
	if err := validRefArg("ref", oldBase); err != nil {
		return err
	}
	if err := validRefArg("branch", branch); err != nil {
		return err
	}
	_, err := run("-C", dir, "rebase", "--quiet", "--onto", newBase, oldBase, branch)
	return err
}

// RebaseAbortIn aborts an in-progress rebase inside the worktree at dir.
func RebaseAbortIn(dir string) error {
	if dir == "" {
		return fmt.Errorf("worktree dir is empty")
	}
	_, err := run("-C", dir, "rebase", "--abort")
	return err
}

// IsCleanIn reports whether the worktree at dir has no staged or unstaged
// changes, via `git -C <dir> status --porcelain`. It is the port-level twin of
// IsCleanAt.
func IsCleanIn(dir string) (bool, error) {
	return IsCleanAt(dir)
}

// LsFilesZ returns the tracked paths of the worktree at dir, parsed from one
// `git -C dir ls-files -z` — NUL-separated so paths with spaces or quotes
// survive byte-exact. A failure to run git is an error; dir must be a git
// worktree.
func LsFilesZ(dir string) ([]string, error) {
	out, err := run("-C", dir, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// CheckIgnored reports which of rels (paths relative to root) git ignores,
// from one batched `git -C root check-ignore -z --stdin`. A single path git
// cannot classify (for example one beyond a symlinked directory) poisons the
// whole batch, so a non-clean batch failure falls back to per-entry probes: a
// path git cannot classify counts as not ignored.
func CheckIgnored(root string, rels []string) (map[string]bool, error) {
	ignored := make(map[string]bool, len(rels))
	if len(rels) == 0 {
		return ignored, nil
	}
	cmd := exec.Command("git", "-C", root, "check-ignore", "-z", "--stdin")
	cmd.Env = gitEnv()
	cmd.Stdin = bytes.NewReader([]byte(strings.Join(rels, "\x00") + "\x00"))
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return nil, fmt.Errorf("git check-ignore: %w", err)
		}
		switch exitErr.ExitCode() {
		case 1:
			return ignored, nil // none of the paths are ignored
		default:
			for _, rel := range rels {
				if checkIgnored(root, rel) {
					ignored[rel] = true
				}
			}
			return ignored, nil
		}
	}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			ignored[p] = true
		}
	}
	return ignored, nil
}

// checkIgnored reports whether rel (relative to root) is ignored by git, via
// `git -C root check-ignore`. check-ignore exits 0 when the path is ignored,
// 1 when it is not, so a nil error means ignored. It is the per-entry fallback
// when the batched CheckIgnored probe hits a fatal entry.
func checkIgnored(root, rel string) bool {
	return ok("-C", root, "check-ignore", "-q", "--", rel)
}

// Checkout switches the working tree to the named branch.
func Checkout(name string) error {
	if err := validRefArg("branch", name); err != nil {
		return err
	}
	_, err := Run("checkout", name)
	return err
}

// CheckoutDetach detaches HEAD at ref without switching to a branch.
func CheckoutDetach(ref string) error {
	if err := validRefArg("ref", ref); err != nil {
		return err
	}
	_, err := Run("checkout", "--detach", ref)
	return err
}

// CreateBranch creates a new branch off the current HEAD and switches to it.
func CreateBranch(name string) error {
	if err := validRefArg("branch", name); err != nil {
		return err
	}
	_, err := Run("checkout", "-b", name)
	return err
}

// CreateBranchAt creates a new local branch at ref without checking it out.
func CreateBranchAt(name, ref string) error {
	if err := validRefArg("branch", name); err != nil {
		return err
	}
	if err := validRefArg("ref", ref); err != nil {
		return err
	}
	_, err := Run("branch", name, ref)
	return err
}

// DeleteBranch deletes the named local branch. When force is true the branch is
// removed even if it is not fully merged.
func DeleteBranch(name string, force bool) error {
	if err := validRefArg("branch", name); err != nil {
		return err
	}
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := Run("branch", flag, name)
	return err
}

// DeleteBranches deletes the named local branches in ONE `git branch -D`
// invocation — a prune of N merged branches costs one spawn instead of N. git
// processes the arguments independently: it deletes every branch it can and
// reports the rest on stderr with a nonzero exit, so a non-nil error means a
// PARTIAL delete may already have happened. Callers needing the exact set
// must re-probe.
func DeleteBranches(names []string, force bool) error {
	for _, name := range names {
		if err := validRefArg("branch", name); err != nil {
			return err
		}
	}
	flag := "-d"
	if force {
		flag = "-D"
	}
	args := append([]string{"branch", flag}, names...)
	_, err := Run(args...)
	return err
}

// RevParse returns the full commit SHA that the given ref resolves to. The ref
// is rejected if it begins with "-" so a corrupt or hostile state.json value
// (e.g. a branch named "--git-dir") cannot be parsed by git as an option.
// Bare local branch names are qualified first — like MergedInto, IsAncestor,
// MergeBase, and ChangesContainedIn — so a tag or SHA-shaped string of the
// same name cannot shadow the branch.
func RevParse(ref string) (string, error) {
	if err := validRefArg("ref", ref); err != nil {
		return "", err
	}
	return Run("rev-parse", localBranchRef(ref))
}

// localBranchRef qualifies a bare local branch name to refs/heads/<name> so
// callers resolve it unambiguously — a tag or SHA-shaped string of the same
// name cannot shadow the branch. HEAD, already-qualified refs, and SHAs pass
// through unchanged. The existence probe is BranchExists (`git show-ref
// --verify`), an exact-ref read rather than a namespace listing, so each call
// is O(1) in ref count. Do not "optimize" this to `for-each-ref
// refs/heads/<name>`: that enumerates and formats matches (strictly more
// work) and matches at slash boundaries, so it would need an exact-match
// post-filter just to preserve today's semantics.
func localBranchRef(ref string) string {
	// A 40-hex value can never be a branch name (refname rules forbid it), so
	// it skips the show-ref probe outright.
	if ref == "HEAD" || strings.HasPrefix(ref, "refs/") || isHex40(ref) {
		return ref
	}
	if BranchExists(ref) {
		return "refs/heads/" + ref
	}
	return ref
}

func localBranchNameRef(name string) string {
	return "refs/heads/" + name
}

func absPathFromGitOutput(path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	// Git emits relative repository paths from the process working directory,
	// not from the repository root.
	return filepath.Abs(path)
}

func isSingleAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && !strings.Contains(path, "\n")
}

// IsClean reports whether the working tree has no staged or unstaged changes,
// i.e. "git status --porcelain" produces no output.
func IsClean() (bool, error) {
	out, err := Run("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out == "", nil
}

// IsCleanAt reports whether the working tree at dir has no staged or unstaged
// changes, by running `git -C <dir> status --porcelain`. It exists so callers
// can report the dirty state of a LINKED worktree (a different directory than
// the process cwd) — the only place git -C is needed for the worktree feature.
func IsCleanAt(dir string) (bool, error) {
	out, err := Run("-C", dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out == "", nil
}

// UnmergedFiles returns the paths with unresolved merge conflicts — the files a
// paused rebase is waiting on — or nil when there are none. Newline output (not
// -z) is intentional: Run() trims and every parser here splits on "\n", so a NUL
// stream would leave a stray empty field.
func UnmergedFiles() ([]string, error) {
	out, err := Run("diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// HasStagedChanges reports whether there are staged changes in the index.
func HasStagedChanges() (bool, error) {
	cmd := exec.Command("git", "diff", "--cached", "--quiet")
	cmd.Env = gitEnv()
	err := cmd.Run()
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() == 1 {
			return true, nil
		}
	}
	return false, fmt.Errorf("git diff --cached --quiet: %w", err)
}

// HasUnstagedChanges reports whether the working tree has unstaged tracked
// changes or untracked files.
func HasUnstagedChanges() (bool, error) {
	cmd := exec.Command("git", "diff", "--quiet")
	cmd.Env = gitEnv()
	err := cmd.Run()
	if err == nil {
		out, err := Run("ls-files", "--others", "--exclude-standard")
		if err != nil {
			return false, err
		}
		return out != "", nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, fmt.Errorf("git diff --quiet: %w", err)
}

// Add stages the given paths. When no paths are provided it stages all changes
// in the repository ("git add -A").
func Add(paths ...string) error {
	if len(paths) == 0 {
		_, err := Run("add", "-A")
		return err
	}
	args := append([]string{"add", "--"}, paths...)
	_, err := Run(args...)
	return err
}

// Commit creates a commit with the given message. When all is true, modified and
// deleted tracked files are staged automatically ("git commit -a").
func Commit(message string, all bool) error {
	args := []string{"commit"}
	if all {
		args = append(args, "-a")
	}
	args = append(args, "-m", message)
	_, err := Run(args...)
	return err
}

// AmendNoEdit amends the most recent commit without changing its message. When
// all is true, modified and deleted tracked files are staged automatically.
func AmendNoEdit(all bool) error {
	args := []string{"commit"}
	if all {
		args = append(args, "-a")
	}
	args = append(args, "--amend", "--no-edit")
	_, err := Run(args...)
	return err
}

// AmendMessage amends the most recent commit, replacing its message. When all is
// true, modified and deleted tracked files are staged automatically.
func AmendMessage(message string, all bool) error {
	args := []string{"commit", "--amend", "-m", message}
	if all {
		args = append(args, "-a")
	}
	_, err := Run(args...)
	return err
}

// RebaseOnto runs "git rebase --onto newBase oldBase branch" with inherited
// stdio so a conflict's details surface to the user. --quiet suppresses git's
// chatty success progress ("Rebasing (1/1)…Successfully rebased…") so that on a
// clean rebase the CLI's own one-line summary is the only output, while conflict
// messages (which --quiet does NOT silence) still reach the user.
func RebaseOnto(newBase, oldBase, branch string) error {
	if err := validRefArg("ref", newBase); err != nil {
		return err
	}
	if err := validRefArg("ref", oldBase); err != nil {
		return err
	}
	if err := validRefArg("branch", branch); err != nil {
		return err
	}
	return RunInteractive("rebase", "--quiet", "--onto", newBase, oldBase, branch)
}

// RebaseOntoQuiet runs rebase without inheriting stdout/stderr, for callers that
// need machine-readable output.
func RebaseOntoQuiet(newBase, oldBase, branch string) error {
	if err := validRefArg("ref", newBase); err != nil {
		return err
	}
	if err := validRefArg("ref", oldBase); err != nil {
		return err
	}
	if err := validRefArg("branch", branch); err != nil {
		return err
	}
	_, err := run("rebase", "--quiet", "--onto", newBase, oldBase, branch)
	return err
}

// RebaseInProgress reports whether a git rebase is currently in progress (for
// example, because it stopped on a merge conflict).
func RebaseInProgress() (bool, error) {
	gitDir, err := GitDir()
	if err != nil {
		return false, err
	}
	return rebaseInProgressAt(gitDir)
}

// RebaseInProgressIn reports whether a git rebase is in progress in the
// worktree at dir — including one this process did not start. Rebase metadata
// is per-worktree: it lives under that worktree's own git dir
// (.git/worktrees/<name>/ for a linked worktree), resolved via `git -C dir
// rev-parse --absolute-git-dir` rather than assumed from the caller's git dir.
func RebaseInProgressIn(dir string) (bool, error) {
	if dir == "" {
		return false, fmt.Errorf("worktree dir is empty")
	}
	gitDir, err := Run("-C", dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return false, err
	}
	return rebaseInProgressAt(gitDir)
}

func rebaseInProgressAt(gitDir string) (bool, error) {
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		_, err := os.Stat(filepath.Join(gitDir, name))
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("stat %s: %w", name, err)
		}
	}
	return false, nil
}

// RebaseHeadName returns the branch being rebased while a rebase is in progress,
// or an empty string if it cannot be determined.
func RebaseHeadName() (string, error) {
	gitDir, err := GitDir()
	if err != nil {
		return "", err
	}
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		data, err := os.ReadFile(filepath.Join(gitDir, dir, "head-name"))
		if err != nil {
			continue
		}
		ref := strings.TrimSpace(string(data))
		return strings.TrimPrefix(ref, "refs/heads/"), nil
	}
	return "", nil
}

// RebaseOntoSHA returns the commit the in-progress rebase is replaying onto,
// read from the worktree-local rebase metadata (rebase-merge/onto or
// rebase-apply/onto). The metadata lives under GitDir — not GitCommonDir — so
// a rebase paused inside a linked worktree resolves against that worktree's
// state. The recorded value must resolve to a commit; a missing/unreadable
// file and an unresolvable value are distinct actionable errors — a caller
// must never substitute the current parent ref for a target it cannot read.
func RebaseOntoSHA() (string, error) {
	gitDir, err := GitDir()
	if err != nil {
		return "", err
	}
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(gitDir, dir)); err != nil {
			continue // this backend is not the one in progress
		}
		data, err := os.ReadFile(filepath.Join(gitDir, dir, "onto"))
		if err != nil {
			return "", fmt.Errorf("reading %s/onto: %w", dir, err)
		}
		target := strings.TrimSpace(string(data))
		sha, err := Run("rev-parse", "--verify", "--quiet", target+"^{commit}")
		if err != nil {
			return "", fmt.Errorf("%s/onto value %q does not resolve to a commit: %w", dir, target, err)
		}
		return sha, nil
	}
	return "", errors.New("no in-progress rebase found (rebase-merge/onto, rebase-apply/onto)")
}

// RebaseContinue runs "git rebase --continue", reusing the existing commit
// messages without opening an editor so it never blocks on interactive input. It
// returns an error if the rebase does not run to completion (for example because
// conflicts remain to be resolved).
func RebaseContinue() error {
	cmd := exec.Command("git", "rebase", "--continue")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git rebase --continue: %w", err)
	}
	return nil
}

// RebaseContinueQuiet resumes a rebase without writing git's human output to
// stdout/stderr.
func RebaseContinueQuiet() error {
	cmd := exec.Command("git", "rebase", "--continue")
	cmd.Stdin = os.Stdin
	cmd.Env = append(gitEnv(), "GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("git rebase --continue: %s: %w", msg, err)
		}
		return fmt.Errorf("git rebase --continue: %w", err)
	}
	return nil
}

// Fetch updates remote-tracking refs for the named remote.
func Fetch(remote string) error {
	if err := validRefArg("remote", remote); err != nil {
		return err
	}
	_, err := Run("fetch", remote)
	return err
}

// PushStatus is the remote's confirmed outcome for one pushed ref, taken from
// the push's own porcelain status records — never inferred from process exit.
type PushStatus int

const (
	// PushUnconfirmed means no usable status record arrived for the ref.
	PushUnconfirmed PushStatus = iota
	// PushUpdated means the remote moved the ref (new, fast-forward, or
	// forced update).
	PushUpdated
	// PushUpToDate means the remote already had the requested tip.
	PushUpToDate
	// PushRejected means the remote explicitly refused the ref update.
	PushRejected
)

// PushResult reports the confirmed per-ref outcome of a single push
// invocation. Status holds one entry per requested branch; entries that stay
// PushUnconfirmed must never be reported as pushed or failed — the transport
// error (if any) explains why.
type PushResult struct {
	Status map[string]PushStatus
}

// PushBranches pushes the given branches to remote in a single git invocation
// and records upstreams (-u) for each branch. The returned PushResult carries
// every per-ref status the remote confirmed; on a partial rejection the
// process error and the confirmed records both come back so callers can
// report exactly which refs landed without pushing again. A push that exits
// zero but reports no status for a requested ref is itself an error — an
// unrecorded ref is never assumed pushed.
func PushBranches(remote string, branches []string, force bool) (*PushResult, error) {
	if err := validRefArg("remote", remote); err != nil {
		return nil, err
	}
	for _, branch := range branches {
		if err := validRefArg("branch", branch); err != nil {
			return nil, err
		}
	}
	res := &PushResult{Status: make(map[string]PushStatus, len(branches))}
	for _, branch := range branches {
		res.Status[branch] = PushUnconfirmed
	}
	if len(branches) == 0 {
		return res, nil
	}

	args := []string{"push", "--porcelain", "-u"}
	if force {
		args = append(args, "--force-with-lease")
	}
	args = append(args, remote)
	for _, branch := range branches {
		refspec := "refs/heads/" + branch + ":refs/heads/" + branch
		args = append(args, refspec)
	}
	out, err := run(args...)
	parsePushPorcelain(out, branches, res)
	if err != nil {
		return res, err
	}
	for _, branch := range branches {
		if res.Status[branch] == PushUnconfirmed {
			return res, fmt.Errorf("git push: no per-ref status reported for %q", branch)
		}
	}
	return res, nil
}

// parsePushPorcelain folds `git push --porcelain` status records into res.
// Each record is "<flag>\t<src>:<dst>\t<summary>" — the flag can be a literal
// space, so lines are never trimmed. Only records whose dst is a requested
// refs/heads/<branch> are honored: unrelated refs, non-record chatter (the
// "To <url>" header, "set up to track" lines, "Done") and malformed lines
// never fabricate outcomes. A duplicate record simply rewrites the status —
// the remote's final word stands.
func parsePushPorcelain(out string, branches []string, res *PushResult) {
	want := make(map[string]string, len(branches))
	for _, b := range branches {
		want["refs/heads/"+b] = b
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 3 || line[1] != '\t' {
			continue
		}
		refs, _, ok := strings.Cut(line[2:], "\t")
		if !ok {
			continue
		}
		_, dst, ok := strings.Cut(refs, ":")
		if !ok || dst == "" {
			continue
		}
		branch, ok := want[dst]
		if !ok {
			continue
		}
		switch line[0] {
		case ' ', '*', '+', '-':
			res.Status[branch] = PushUpdated
		case '=':
			res.Status[branch] = PushUpToDate
		case '!':
			res.Status[branch] = PushRejected
		}
	}
}

// RemoteExists reports whether a remote with the given name is configured. A
// flag-like name is simply "not configured": it is rejected before exec so git
// can never parse it as an option.
func RemoteExists(name string) bool {
	if err := validRefArg("remote", name); err != nil {
		return false
	}
	return ok("remote", "get-url", name)
}

// MergeBase returns the best common ancestor commit of the two given refs.
func MergeBase(a, b string) (string, error) {
	if err := validRefArg("ref", a); err != nil {
		return "", err
	}
	if err := validRefArg("ref", b); err != nil {
		return "", err
	}
	return Run("merge-base", localBranchRef(a), localBranchRef(b))
}

// IsAncestor reports whether ancestor is an ancestor of descendant. A valid
// negative answer returns false, nil; invalid refs and other git failures return
// an error.
func IsAncestor(ancestor, descendant string) (bool, error) {
	if err := validRefArg("ref", ancestor); err != nil {
		return false, err
	}
	if err := validRefArg("ref", descendant); err != nil {
		return false, err
	}
	ancestorRef := localBranchRef(ancestor)
	descendantRef := localBranchRef(descendant)
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestorRef, descendantRef)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	msg := strings.TrimSpace(string(out))
	if msg != "" {
		return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %s: %w", ancestorRef, descendantRef, msg, err)
	}
	return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %w", ancestorRef, descendantRef, err)
}

// CommitRange returns the set of commit SHAs reachable from include but not
// from exclude — `git rev-list include ^exclude`, i.e. exclude..include — in
// one bounded invocation. The walk stops at the excluded history, so the cost
// is O(range size), not O(repo history). The "^" prefix on the exclude ref
// also means it can never be parsed as an option.
func CommitRange(exclude, include string) (map[string]bool, error) {
	if err := validRefArg("ref", exclude); err != nil {
		return nil, err
	}
	if err := validRefArg("ref", include); err != nil {
		return nil, err
	}
	out, err := Run("rev-list", localBranchRef(include), "^"+localBranchRef(exclude))
	if err != nil {
		return nil, err
	}
	return revListSet(out), nil
}

// revListSet splits rev-list output into a SHA set.
func revListSet(out string) map[string]bool {
	set := map[string]bool{}
	if out == "" {
		return set
	}
	for _, line := range strings.Split(out, "\n") {
		if line != "" {
			set[line] = true
		}
	}
	return set
}

// GitDir returns the absolute path to the repository's .git directory.
func GitDir() (string, error) {
	return Run("rev-parse", "--absolute-git-dir")
}

// RepoRoot returns the absolute path to the top level of the working tree.
func RepoRoot() (string, error) {
	return Run("rev-parse", "--show-toplevel")
}

// GitCommonDir returns the absolute path to the repository's common git
// directory. For a linked worktree this is the shared git dir of the main
// worktree, so stack metadata is shared across all worktrees of a repository.
func GitCommonDir() (string, error) {
	dir, err := Run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err == nil && isSingleAbsolutePath(dir) {
		return dir, nil
	}
	// Fall back for git < 2.31 (no --path-format): resolve a possibly
	// relative --git-common-dir from the current working directory.
	dir, err = Run("rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(dir) {
		dir, err = absPathFromGitOutput(dir)
		if err != nil {
			return "", err
		}
	}
	return dir, nil
}

// RebaseAbort aborts an in-progress rebase, restoring the pre-rebase state.
func RebaseAbort() error {
	_, err := Run("rebase", "--abort")
	return err
}

// ResetSoft moves the current branch to ref without touching the index or
// working tree ("git reset --soft").
func ResetSoft(ref string) error {
	if err := validRefArg("ref", ref); err != nil {
		return err
	}
	_, err := Run("reset", "--soft", ref)
	return err
}

// RenameBranch renames a local branch ("git branch -m old new").
func RenameBranch(oldName, newName string) error {
	if err := validRefArg("branch", oldName); err != nil {
		return err
	}
	if err := validRefArg("branch", newName); err != nil {
		return err
	}
	_, err := Run("branch", "-m", oldName, newName)
	return err
}

// ForceBranch points the branch name at ref without checking it out
// ("git branch -f name ref"). It refuses to move the currently checked-out
// branch, matching git's own behavior.
func ForceBranch(name, ref string) error {
	if err := validRefArg("branch", name); err != nil {
		return err
	}
	if err := validRefArg("ref", ref); err != nil {
		return err
	}
	_, err := Run("branch", "-f", name, ref)
	return err
}

// UpdateRef sets a ref (e.g. "refs/heads/feature") to the given commit SHA,
// creating it if it does not yet exist. The ref is rejected if it begins with
// "-", and "--" terminates option parsing so neither the ref nor the SHA can be
// interpreted by git as an option.
func UpdateRef(ref, sha string) error {
	if err := validRefArg("ref", ref); err != nil {
		return err
	}
	_, err := Run("update-ref", "--", ref, sha)
	return err
}

// UpdateRefs sets every ref in updates to its SHA in a single
// `git update-ref -z --stdin` invocation. git applies the batch as one
// transaction: on any failure no ref is updated. An `update` directive
// creates a missing ref, which is what resurrects pruned branches on undo.
// Empty input is a no-op.
//
// The batch is NUL-framed (-z) and both refs and values are rejected if they
// carry whitespace/control bytes: the inputs come from the undo journal,
// which the threat model treats as potentially hostile, and a newline in a
// space/LF-framed record would inject a second directive into the
// transaction (the argv-framed single-ref UpdateRef never had that problem).
func UpdateRefs(updates map[string]string) error {
	if len(updates) == 0 {
		return nil
	}
	refs := make([]string, 0, len(updates))
	for ref, val := range updates {
		if err := validRefArg("ref", ref); err != nil {
			return err
		}
		if hasControlOrSpace(ref) {
			return fmt.Errorf("ref %q contains whitespace or control bytes", ref)
		}
		if hasControlOrSpace(val) {
			return fmt.Errorf("update value for %q contains whitespace or control bytes", ref)
		}
		refs = append(refs, ref)
	}
	sort.Strings(refs) // deterministic batch for tests and debuggability
	var b strings.Builder
	for _, ref := range refs {
		// -z record grammar: update SP <ref> NUL <newvalue> NUL <oldvalue> NUL.
		// The old-oid field must be PRESENT mid-batch (omitting it makes git
		// consume the next record as the old value) but EMPTY, which means "no
		// verification" — same semantics as the old newline form, verified
		// against real git: empty old-oid updates existing refs and creates
		// missing ones.
		fmt.Fprintf(&b, "update %s\x00%s\x00\x00", ref, updates[ref])
	}
	cmd := exec.Command("git", "update-ref", "-z", "--stdin")
	cmd.Env = gitEnv()
	cmd.Stdin = strings.NewReader(b.String())
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return fmt.Errorf("git update-ref --stdin: %s: %w", msg, err)
		}
		return fmt.Errorf("git update-ref --stdin: %w", err)
	}
	return nil
}

// RemoteURL returns the configured fetch URL of the named remote.
func RemoteURL(remote string) (string, error) {
	if err := validRefArg("remote", remote); err != nil {
		return "", err
	}
	return Run("remote", "get-url", remote)
}

// CommitSubjects returns the subject lines of the commits in the local branch
// range base..branch, newest first.
func CommitSubjects(base, branch string) ([]string, error) {
	if err := validRefArg("ref", base); err != nil {
		return nil, err
	}
	if err := validRefArg("branch", branch); err != nil {
		return nil, err
	}
	out, err := Run("log", "--format=%s", localBranchRef(base)+".."+localBranchNameRef(branch))
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// MinVersion is the minimum git version st requires: the documented floor
// (README/CONTRIBUTING promise "Git 2.17+"). Worktree `-z` porcelain (2.36)
// and common-dir path resolution (2.31) have graceful fallbacks, so they are
// NOT part of the floor — bumping this is a docs-visible contract change.
var MinVersion = [3]int{2, 17, 0}

// RequireMinVersion verifies the git on PATH meets MinVersion, so an
// unsupported git fails at startup with a clear message rather than
// mid-command on an unrecognized flag or grammar.
func RequireMinVersion() error {
	out, err := Run("version")
	if err != nil {
		return fmt.Errorf("git version: %w", err)
	}
	found, err := parseGitVersion(out)
	if err != nil {
		return err
	}
	if !versionAtLeast(found, MinVersion) {
		return fmt.Errorf("st requires git >= %d.%d.%d (found %d.%d.%d)",
			MinVersion[0], MinVersion[1], MinVersion[2], found[0], found[1], found[2])
	}
	return nil
}

// parseGitVersion extracts the dotted numeric version from `git version`
// output ("git version 2.46.0", and distro-suffixed forms like
// "git version 2.39.5 (Apple Git-154)").
func parseGitVersion(out string) ([3]int, error) {
	fields := strings.Fields(strings.TrimSpace(out))
	for _, f := range fields {
		if f[0] < '0' || f[0] > '9' {
			continue
		}
		var v [3]int
		parts := strings.SplitN(f, ".", 4)
		if len(parts) < 2 {
			continue
		}
		ok := true
		for i := 0; i < 3; i++ {
			if i >= len(parts) {
				break
			}
			n, err := strconv.Atoi(parts[i])
			if err != nil {
				ok = false
				break
			}
			v[i] = n
		}
		if ok {
			return v, nil
		}
	}
	return [3]int{}, fmt.Errorf("cannot parse git version from %q", out)
}

// versionAtLeast reports whether found >= want, comparing major, minor, patch
// in order.
func versionAtLeast(found, want [3]int) bool {
	for i := 0; i < 3; i++ {
		if found[i] != want[i] {
			return found[i] > want[i]
		}
	}
	return true
}
