// diff.go — staged-diff capture and reassembly: hunk parsing, per-target
// patch assembly, blame, and the temp-index amend machinery absorb uses.
package git

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

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
