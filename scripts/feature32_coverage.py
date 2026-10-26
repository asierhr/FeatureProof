"""Measure exact changed-statement coverage for feature 32 from its approved base."""

from __future__ import annotations

import os
import pathlib
import re
import subprocess
import sys
import tempfile


# ROOT anchors every coverage and Git query to the reviewed repository.
ROOT = pathlib.Path(__file__).resolve().parents[1]
# BASE fixes the approved feature-32 comparison point.
BASE = "a7b4a98"
# PACKAGES name the hermetic suites that exercise every changed production package.
PACKAGES = ["./cmd/featureproof", "./internal/environment/kubernetes", "./internal/validationworkflow"]
# PROFILE_BLOCK decodes Go cover-profile blocks without losing column identity.
PROFILE_BLOCK = re.compile(
    r".*?/(cmd/.*?\.go|internal/.*?\.go):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)$"
)
# GO_DIRECTIVE extracts the repository-owned toolchain version from go.mod.
GO_DIRECTIVE = re.compile(r"^go\s+(\d+\.\d+(?:\.\d+)?)$", re.MULTILINE)


def git(*args: str) -> str:
    """Return one repository query as decoded text."""
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True, errors="replace")


def changed_lines() -> dict[str, set[int]]:
    """Return added production Go lines, including untracked feature files."""
    tracked = git("diff", "--name-only", "--diff-filter=ACMR", BASE, "--", "*.go").splitlines()
    untracked = git("ls-files", "--others", "--exclude-standard", "--", "*.go").splitlines()
    changed: dict[str, set[int]] = {}
    for name in sorted(set(tracked + untracked)):
        if name.endswith("_test.go"):
            continue
        if name in untracked:
            changed[name] = set(range(1, len((ROOT / name).read_text(encoding="utf-8").splitlines()) + 1))
            continue
        lines: set[int] = set()
        diff = git("diff", "--unified=0", BASE, "--", name)
        for match in re.finditer(r"^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@", diff, re.MULTILINE):
            start, count = int(match.group(1)), int(match.group(2) or 1)
            lines.update(range(start, start + count))
        changed[name] = lines
    return changed


def main() -> int:
    """Generate a pinned fresh profile and enforce 85 percent without rounding."""
    match = GO_DIRECTIVE.search((ROOT / "go.mod").read_text(encoding="utf-8"))
    if not match:
        raise RuntimeError("go.mod has no exact Go directive")
    toolchain = "go" + match.group(1)
    environment = {**os.environ, "GOTOOLCHAIN": toolchain}
    identity = subprocess.check_output(["go", "env", "GOVERSION", "GOOS", "GOARCH"], cwd=ROOT, env=environment, text=True).splitlines()
    if len(identity) != 3 or identity[0] != toolchain:
        raise RuntimeError(f"required toolchain {toolchain} is unavailable")
    changed = changed_lines()
    with tempfile.TemporaryDirectory(prefix="feature32-coverage-") as temporary:
        profile = pathlib.Path(temporary) / "coverage.out"
        subprocess.run(["go", "test", "-count=1", "-coverpkg=./...", f"-coverprofile={profile}", *PACKAGES], cwd=ROOT, env=environment, check=True)
        blocks: dict[tuple[str, int, int, int, int, int], int] = {}
        for line in profile.read_text(encoding="utf-8").splitlines()[1:]:
            block = PROFILE_BLOCK.match(line)
            if not block:
                continue
            name = block.group(1)
            start_line, start_column, end_line, end_column, statements, count = map(int, block.groups()[1:])
            if name not in changed or not changed[name].intersection(range(start_line, end_line + 1)):
                continue
            key = (name, start_line, start_column, end_line, end_column, statements)
            blocks[key] = max(blocks.get(key, 0), count)
    by_file: dict[str, tuple[int, int]] = {}
    for (name, _start_line, _start_column, _end_line, _end_column, statements), count in blocks.items():
        covered, total = by_file.get(name, (0, 0))
        by_file[name] = (covered + (statements if count > 0 else 0), total + statements)
    missing = sorted(name for name, lines in changed.items() if lines and name not in by_file)
    if missing:
        raise RuntimeError("profile omitted changed production sources: " + ", ".join(missing))
    covered = sum(value[0] for value in by_file.values())
    total = sum(value[1] for value in by_file.values())
    percent = 100 * covered / total if total else 0
    print(f"host {identity[0]} {identity[1]}/{identity[2]}")
    print(f"feature32 changed production statements: {covered}/{total} = {percent:.6f}%")
    for name, (file_covered, file_total) in sorted(by_file.items()):
        print(f"{100 * file_covered / file_total:5.1f}% {file_covered}/{file_total} {name}")
    return 0 if percent >= 85 else 1


if __name__ == "__main__":
    sys.exit(main())
