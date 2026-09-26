#!/usr/bin/env python3
"""Validate the design-016 undo-preview contract artifacts.

Design-time consistency checks only: fixtures vs. acceptance doc vs. the
design doc's invariants. Runtime truth is the future implementation tests'
job, not this script's.
"""
import json
import re
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
DESIGN = HERE / "016-undo-preview.md"
EXAMPLES = HERE / "016-undo-preview-examples.json"
ACCEPTANCE = HERE / "016-undo-preview-acceptance.md"

BLOCKER_CODES = {
    "journal_empty",
    "rebase_in_progress",
    "state_too_new",
    "malformed_snapshot",
}
BLOCKER_PREFIXES = ("worktree_dirty:", "cwd_inside_created_worktree:",
                    "recorded_worktree_mismatch:")

REQUIRED_SCENARIOS = {
    "no_journal", "ordinary_modify", "create_with_worktree",
    "later_created_owning_worktree", "dirty_created_worktree",
    "cwd_inside_created_worktree", "changed_live_tips",
    "missing_ref_object", "future_schema", "malformed_current_state",
    "active_rebase",
}

INVARIANTS = {"state file bytes", "journal bytes", "index", "refs",
              "worktrees", "cwd"}

failures = []


def fail(msg):
    failures.append(msg)


def valid_blocker(b):
    return b in BLOCKER_CODES or b.startswith(BLOCKER_PREFIXES)


def main():
    if not DESIGN.exists():
        fail("missing 016-undo-preview.md")
    if not ACCEPTANCE.exists():
        fail("missing 016-undo-preview-acceptance.md")
    if not EXAMPLES.exists():
        fail("missing 016-undo-preview-examples.json")
        return report()

    design = DESIGN.read_text() if DESIGN.exists() else ""
    acc = ACCEPTANCE.read_text() if ACCEPTANCE.exists() else ""

    for heading in ("## Current contract", "## Proposed command",
                    "## Impact model", "## Validation and blockers",
                    "## Concurrency", "## Examples", "## Acceptance",
                    "## Deferred work"):
        if heading not in design:
            fail(f"design doc missing heading {heading!r}")
    for term in ("dry-run", "mutually exclusive", "unknown", "future",
                 "reservation", "DropUndo"):
        if term not in design:
            fail(f"design doc missing required term {term!r}")

    try:
        scenarios = json.loads(EXAMPLES.read_text())
    except json.JSONDecodeError as e:
        fail(f"examples JSON invalid: {e}")
        return report()
    if not isinstance(scenarios, list):
        fail("examples root must be a list")
        return report()

    names = {s.get("scenario") for s in scenarios if isinstance(s, dict)}
    for req in REQUIRED_SCENARIOS - names:
        fail(f"missing scenario {req!r}")
    for extra in names - REQUIRED_SCENARIOS:
        fail(f"unregistered scenario {extra!r}")

    for s in scenarios:
        n = s.get("scenario", "<unnamed>")
        for key in ("command", "stdout", "stderr", "exitCode", "proposed",
                    "mustRemainUnchanged"):
            if key not in s:
                fail(f"{n}: missing key {key!r}")
        if s.get("command") != "st undo --dry-run --json":
            fail(f"{n}: command must be `st undo --dry-run --json`")
        if s.get("exitCode") not in (0, 1):
            fail(f"{n}: unexpected exitCode {s.get('exitCode')}")
        inv = set(s.get("mustRemainUnchanged", []))
        if inv != INVARIANTS:
            fail(f"{n}: mustRemainUnchanged must be exactly {sorted(INVARIANTS)}")
        out = s.get("stdout", {})
        if not isinstance(out, dict) or out.get("dryRun") is not True:
            fail(f"{n}: stdout must carry dryRun:true")
        for b in out.get("blockers", []):
            if not valid_blocker(b):
                fail(f"{n}: unregistered blocker code {b!r}")
        for wr in out.get("wouldRestore", []):
            clr = wr.get("commitsLostFromRef")
            if clr != "unknown" and (not isinstance(clr, int) or clr < 0):
                fail(f"{n}: commitsLostFromRef must be a non-negative int or "
                     f"\"unknown\", got {clr!r}")
        for wd in out.get("wouldDelete", []):
            if "worktreeDirty" not in wd:
                fail(f"{n}: wouldDelete entry missing worktreeDirty")
        for p in s.get("proposed", []):
            if not isinstance(p, str) or not p.startswith("stdout"):
                fail(f"{n}: proposed path {p!r} must start with 'stdout'")

    # Cross-reference: every scenario appears in the acceptance table.
    for n in sorted(names):
        if n not in acc:
            fail(f"scenario {n!r} not covered in acceptance doc")
    if "commit-stash" in acc or "rebase_in_progress" not in acc:
        fail("acceptance doc must reference the rebase_in_progress case")

    report()


def report():
    if failures:
        print("check-undo-preview: FAIL")
        for f in failures:
            print(f"  - {f}")
        sys.exit(1)
    print("check-undo-preview: OK")


if __name__ == "__main__":
    main()
