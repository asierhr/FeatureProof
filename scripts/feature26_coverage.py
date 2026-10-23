"""Measure statement-weighted coverage for feature 26 production diff from 1daa391."""

from __future__ import annotations

import pathlib
import hashlib
import re
import subprocess
import sys
import tempfile


ROOT = pathlib.Path(__file__).resolve().parents[1]
BASE = "1daa391"
PACKAGES = [
    "./cmd/featureproof",
    "./internal/fixture",
    "./internal/localvalidation",
    "./internal/specification",
    "./internal/specification/yaml",
    "./internal/validationworkflow",
    "./internal/planmanifest",
]

# Go cannot execute the opposite OS implementation in one process. These counts
# come from `go tool cover` blocks for the two Unix-only files; they remain in
# the denominator on Windows and are exercised by store_unix_test.go on Unix.
PLATFORM_STATEMENTS = {
    "internal/fixture/store_lock_unix.go": (15, "6769a8c1776f5aadd074b52022af2c6ea1dbda7dde07797cee7587599a5bdac2"),
    "internal/fixture/store_replace_unix.go": (1, "358f84068708a219be56379328ff2b459d38c4f236c7b6f5eaf37ae3aa7fc0b0"),
    "internal/fixture/store_lock_windows.go": (17, "d0f8e722a422aa15f959f832fda51521b45202f6d1463f7c3a638fca11d44637"),
    "internal/fixture/store_replace_windows.go": (7, "4f3d16f7cd5799950e3aed7c22e8ccb46f314979999948428dd1ed176a295ab5"),
    "internal/localvalidation/fixture_authority_owner_unix.go": (3, "783dfae11a109b3e9de32c07ac9337e89ecd782e455e49d443ec581d0af9fdbd"),
    "internal/localvalidation/fixture_authority_owner_windows.go": (54, "81ca0c9171f4df48e97070095a7e8220b882ad7a90c03b53eaab285d5365b813"),
    "internal/localvalidation/fixture_authority_sync_unix.go": (4, "53b3ae00a374059dd6f31e21597e7c8f9cc3a2726e4bc6babe82e64ab59e0992"),
    "internal/localvalidation/fixture_authority_sync_windows.go": (1, "578d4f87e81c296244b1e7d3e930bdc950f14061357e4c1026b06ac0c16f22c1"),
}

# These changed files contain declarations only and therefore have no executable
# Go cover blocks; listing them makes that zero-statement fact explicit.
ZERO_STATEMENT_SOURCES = {
    "internal/localvalidation/package_model.go",
    "internal/localvalidation/publisher.go",
    "internal/specification/model.go",
    "internal/validationworkflow/model.go",
}


def git(*args: str) -> str:
    """Return one trusted repository query as UTF-8 text."""
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True, errors="replace")


def changed_lines() -> dict[str, set[int]]:
    """Build the exact added/modified production line set, including untracked feature files."""
    tracked = git("diff", "--name-only", "--diff-filter=ACMR", BASE, "--", "*.go").splitlines()
    untracked = git("ls-files", "--others", "--exclude-standard", "--", "*.go").splitlines()
    result: dict[str, set[int]] = {}
    for name in sorted(set(tracked + untracked)):
        if name.endswith("_test.go"):
            continue
        if name in untracked:
            result[name] = set(range(1, len((ROOT / name).read_text(encoding="utf-8").splitlines()) + 1))
            continue
        diff = git("diff", "--unified=0", BASE, "--", name)
        lines: set[int] = set()
        for match in re.finditer(r"^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@", diff, re.MULTILINE):
            start, count = int(match.group(1)), int(match.group(2) or 1)
            lines.update(range(start, start + count))
        result[name] = lines
    return result


def run_coverage(profile: pathlib.Path) -> None:
    """Run every affected product test package with one shared all-package instrumentation profile."""
    subprocess.run(
        ["go", "test", "-count=1", "-coverpkg=./...", f"-coverprofile={profile}", *PACKAGES],
        cwd=ROOT,
        check=True,
    )


def measure(profile: pathlib.Path, changed: dict[str, set[int]]) -> tuple[int, int, dict[str, tuple[int, int]]]:
    """Intersect Go statement blocks with changed lines and count each canonical block once."""
    blocks: dict[tuple[str, int, int, int], int] = {}
    pattern = re.compile(r".*?/(cmd/.*?\.go|internal/.*?\.go):(\d+)\.\d+,(\d+)\.\d+ (\d+) (\d+)$")
    for line in profile.read_text(encoding="utf-8").splitlines()[1:]:
        match = pattern.match(line)
        if not match:
            continue
        name, start, end, statements, count = match.group(1), int(match.group(2)), int(match.group(3)), int(match.group(4)), int(match.group(5))
        if name not in changed or not changed[name].intersection(range(start, end + 1)):
            continue
        key = (name, start, end, statements)
        blocks[key] = max(blocks.get(key, 0), count)
    by_file: dict[str, tuple[int, int]] = {}
    for (name, _start, _end, statements), count in blocks.items():
        covered, total = by_file.get(name, (0, 0))
        by_file[name] = (covered + (statements if count > 0 else 0), total + statements)
    apply_platform_denominator(by_file, changed)
    missing = sorted(name for name, lines in changed.items() if lines and name not in by_file and name not in ZERO_STATEMENT_SOURCES)
    if missing:
        raise RuntimeError("coverage profile omitted changed production sources: " + ", ".join(missing))
    return sum(value[0] for value in by_file.values()), sum(value[1] for value in by_file.values()), by_file


def apply_platform_denominator(by_file: dict[str, tuple[int, int]], changed: dict[str, set[int]]) -> None:
    """Verify symmetric OS metadata and count each Windows/Unix implementation exactly once."""
    for name, (statements, expected_hash) in PLATFORM_STATEMENTS.items():
        if name not in changed:
            continue
        source = ROOT / name
        if not source.is_file() or hashlib.sha256(source.read_bytes()).hexdigest() != expected_hash:
            raise RuntimeError(f"stale or missing executable platform metadata: {name}")
        if name in by_file:
            if by_file[name][1] != statements:
                raise RuntimeError(f"platform statement metadata mismatch: {name}")
        else:
            by_file[name] = (0, statements)


def main() -> int:
    """Print reproducible per-file evidence and fail unless the weighted feature diff reaches 85 percent."""
    with tempfile.TemporaryDirectory(prefix="feature26-coverage-") as temporary:
        profile = pathlib.Path(temporary) / "coverage.out"
        run_coverage(profile)
        covered, total, by_file = measure(profile, changed_lines())
    percent = 100 * covered / total if total else 0
    print(f"feature26 changed production statements: {covered}/{total} = {percent:.4f}%")
    for name, (file_covered, file_total) in sorted(by_file.items()):
        print(f"{100 * file_covered / file_total:5.1f}% {file_covered}/{file_total} {name}")
    return 0 if percent >= 85 else 1


if __name__ == "__main__":
    sys.exit(main())
