// worktree.go — worktree inventory, creation, and removal, plus the
// dir-scoped probes (cleanliness, current branch, reset/ff) that operate
// inside a linked worktree rather than the caller's cwd.
package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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
	err = spawnRun(cmd)
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

// IsCleanIn reports whether the worktree at dir has no staged or unstaged
// changes, via `git -C <dir> status --porcelain`. It is the port-level twin of
// IsCleanAt.
func IsCleanIn(dir string) (bool, error) {
	return IsCleanAt(dir)
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
