#!/usr/bin/env python3
"""Validate the design-015 artifacts: fixture structure, reason registry,
action-descriptor shapes, and cross-references between examples and the
acceptance document. Standard library only; it checks artifact consistency —
not runtime behavior."""

import json
import re
import sys
from pathlib import Path

HERE = Path(__file__).parent
EXAMPLES = HERE / "015-recovery-examples.json"
DESIGN = HERE / "015-structured-recovery-status.md"
ACCEPTANCE = HERE / "015-recovery-acceptance.md"

REQUIRED_SCENARIOS = {
    "clean_success",
    "dirty_remote_worktree_skip",
    "current_worktree_paused_conflict",
    "continuation_conflicts_again",
    "cross_worktree_conflict_rolled_back",
    "partial_earlier_progress",
    "not_initialized",
    "escaped_path_bytes",
}

REASONS = {"worktree_dirty", "conflict_paused", "conflict_rolled_back"}
STATES = {"skipped", "paused", "rolled_back"}
ACTION_KINDS = {"argv", "manual"}
REQUIRED_SECTIONS = [
    "Current contract", "Proposed contract", "Compatibility",
    "State and concurrency", "Examples", "Acceptance", "Deferred work",
]

errors = []


def fail(msg):
    errors.append(msg)


def dotted_get(obj, dotted):
    """Resolve 'a.b.c' against nested dicts; return (found, value)."""
    cur = obj
    for part in dotted.split("."):
        if not isinstance(cur, dict) or part not in cur:
            return False, None
        cur = cur[part]
    return True, cur


def recovery_entries(payload):
    """Yield every recovery entry found under a payload object."""
    found = []
    if isinstance(payload, dict) and isinstance(payload.get("recovery"), list):
        found.extend(payload["recovery"])
    return found


def main():
    try:
        examples = json.loads(EXAMPLES.read_text())
    except Exception as e:  # noqa: BLE001 — report any parse failure
        fail(f"{EXAMPLES.name}: invalid JSON: {e}")
        examples = []
    design = DESIGN.read_text() if DESIGN.exists() else ""
    acceptance = ACCEPTANCE.read_text() if ACCEPTANCE.exists() else ""

    if not isinstance(examples, list) or not examples:
        fail("examples file must be a non-empty JSON array")
        examples = []

    scenarios = set()
    for i, ex in enumerate(examples):
        sc = ex.get("scenario")
        if not sc:
            fail(f"example[{i}] missing 'scenario'")
            continue
        if sc in scenarios:
            fail(f"duplicate scenario {sc!r}")
        scenarios.add(sc)
        for k in ("command", "stdout", "stderr", "exitCode", "proposed"):
            if k not in ex:
                fail(f"{sc}: missing key {k!r}")
        if not isinstance(ex.get("exitCode"), int):
            fail(f"{sc}: exitCode must be an int")
        if not isinstance(ex.get("proposed"), list):
            fail(f"{sc}: 'proposed' must be an array of dotted paths")

        entries = recovery_entries(ex.get("stdout")) + recovery_entries(ex.get("stderr"))
        if not ex.get("proposed") and entries:
            fail(f"{sc}: contains recovery entries but marks nothing proposed")
        for j, entry in enumerate(entries):
            where = f"{sc}.recovery[{j}]"
            for k in ("branch", "reason", "state", "action"):
                if k not in entry:
                    fail(f"{where}: missing {k!r}")
            if entry.get("reason") not in REASONS:
                fail(f"{where}: unregistered reason {entry.get('reason')!r}")
            if entry.get("state") not in STATES:
                fail(f"{where}: unknown state {entry.get('state')!r}")
            action = entry.get("action")
            if not isinstance(action, dict) or action.get("kind") not in ACTION_KINDS:
                fail(f"{where}: action.kind must be one of {sorted(ACTION_KINDS)}")
            elif action["kind"] == "argv":
                if not isinstance(action.get("argv"), list) or not all(
                    isinstance(a, str) for a in action["argv"]
                ):
                    fail(f"{where}: argv action must carry a string array")
                if "cwd" in action and not isinstance(action["cwd"], str):
                    fail(f"{where}: cwd must be a string")
                if entry.get("state") != "paused":
                    fail(f"{where}: argv actions are only valid for state 'paused'")
            elif entry.get("state") == "paused" and action["kind"] != "argv":
                fail(f"{where}: paused entries should offer an argv action")

        # Every proposed dotted path must resolve inside the payload.
        for path in ex.get("proposed", []):
            root, _, rest = path.partition(".")
            target = {"stdout": ex.get("stdout"), "stderr": ex.get("stderr")}.get(root)
            ok, _ = (False, None) if target is None else dotted_get(target, rest)
            if target is None or not ok:
                fail(f"{sc}: proposed path {path!r} does not exist in the fixture")

    missing = REQUIRED_SCENARIOS - scenarios
    if missing:
        fail(f"missing required scenarios: {sorted(missing)}")

    # The design doc must define each reason code and the required sections.
    for reason in REASONS:
        if reason not in design:
            fail(f"design doc does not define reason {reason!r}")
    for section in REQUIRED_SECTIONS:
        if not re.search(rf"^## {re.escape(section)}$", design, re.M):
            fail(f"design doc missing section '## {section}'")

    # Every example scenario must appear in the acceptance table.
    for sc in sorted(scenarios):
        if sc not in acceptance:
            fail(f"acceptance doc has no row for scenario {sc!r}")

    if errors:
        for e in errors:
            print(f"FAIL: {e}")
        return 1
    print(f"OK: {len(examples)} scenarios, {len(REASONS)} reasons, "
          f"all shapes and cross-references consistent")
    return 0


if __name__ == "__main__":
    sys.exit(main())
