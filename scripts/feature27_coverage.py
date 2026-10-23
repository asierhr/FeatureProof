"""Measure statement-weighted coverage for feature 27 production changes from b81cee0."""

from __future__ import annotations

import hashlib
import pathlib
import re
import subprocess
import sys
import tempfile


ROOT = pathlib.Path(__file__).resolve().parents[1]
BASE = "b81cee0"
PACKAGES = ["./cmd/featureproof", "./internal/fixture", "./internal/functionalflow", "./internal/validationworkflow"]

# Go runs only the host implementation. These verified counts retain the
# opposite platform implementation in the changed-statement denominator.
PLATFORM_STATEMENTS = {
    "internal/functionalflow/store_lock_unix.go": (20, "33ee94676161f64b8798c698896033c2155229728aca7c7856cebcee7429ea04"),
    "internal/functionalflow/store_replace_unix.go": (1, "99d4a7d1d1c17db93d303113c0503f3c5b5da55040a4544335f5f8421ff63d87"),
    "internal/functionalflow/store_lock_windows.go": (22, "885e6f19ff03b3c0144f3579499cf21f7afaf07b76467fd43c4c85d3e7139602"),
    "internal/functionalflow/store_replace_windows.go": (7, "b676f9bf73171ebcc6634f5e32d0ffc5c17d1085a97e55f60fb82245f1e52e7e"),
}

# These changed declarations contain no executable coverage blocks.
ZERO_STATEMENT_SOURCES = {
    "internal/fixture/model.go",
    "internal/planapproval/model.go",
    "internal/testidentity/model.go",
    "internal/validationworkflow/model.go",
}


def git(*args: str) -> str:
    """Return one repository query as decoded text without interpreting its content."""
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True, errors="replace")


def changed_lines() -> dict[str, set[int]]:
    """Return added or modified production lines, including every untracked feature source."""
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


def run_coverage(profile: pathlib.Path) -> None:
    """Exercise all affected test packages with one shared all-package instrumentation profile."""
    subprocess.run(["go", "test", "-count=1", "-coverpkg=./...", f"-coverprofile={profile}", *PACKAGES], cwd=ROOT, check=True)


def apply_platform_denominator(by_file: dict[str, tuple[int, int]], changed: dict[str, set[int]]) -> None:
    """Authenticate platform source metadata and count each host-specific implementation once."""
    for name, (statements, expected_hash) in PLATFORM_STATEMENTS.items():
        if name not in changed:
            continue
        source = ROOT / name
        if not source.is_file() or hashlib.sha256(source.read_bytes()).hexdigest() != expected_hash:
            raise RuntimeError(f"stale or missing platform coverage metadata: {name}")
        if name in by_file:
            if by_file[name][1] != statements:
                raise RuntimeError(f"platform statement metadata mismatch: {name}")
        else:
            by_file[name] = (0, statements)


def measure(profile: pathlib.Path, changed: dict[str, set[int]]) -> tuple[int, int, dict[str, tuple[int, int]]]:
    """Intersect canonical Go coverage blocks with the exact changed production line set."""
    blocks: dict[tuple[str, int, int, int], int] = {}
    pattern = re.compile(r".*?/(cmd/.*?\.go|internal/.*?\.go):(\d+)\.\d+,(\d+)\.\d+ (\d+) (\d+)$")
    for line in profile.read_text(encoding="utf-8").splitlines()[1:]:
        match = pattern.match(line)
        if not match:
            continue
        name = match.group(1)
        start, end, statements, count = map(int, match.groups()[1:])
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


def main() -> int:
    """Print reproducible per-file evidence and reject feature coverage below 85 percent."""
    with tempfile.TemporaryDirectory(prefix="feature27-coverage-") as temporary:
        profile = pathlib.Path(temporary) / "coverage.out"
        run_coverage(profile)
        covered, total, by_file = measure(profile, changed_lines())
    percent = 100 * covered / total if total else 0
    print(f"feature27 changed production statements: {covered}/{total} = {percent:.4f}%")
    for name, (file_covered, file_total) in sorted(by_file.items()):
        print(f"{100 * file_covered / file_total:5.1f}% {file_covered}/{file_total} {name}")
    return 0 if percent >= 85 else 1


if __name__ == "__main__":
    sys.exit(main())
