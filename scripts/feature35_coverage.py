"""Generate and enforce exact changed-statement coverage for feature 35."""

from __future__ import annotations

import json
import os
import pathlib
import re
import subprocess
import sys
import tempfile
from decimal import Decimal, getcontext


# ROOT anchors Git, manifest and Go commands to the reviewed checkout.
ROOT = pathlib.Path(__file__).resolve().parents[1]
# MANIFEST_PATH enumerates every feature-owned production and test source.
MANIFEST_PATH = ROOT / "scripts" / "feature35-coverage-manifest.json"
# PROFILE_BLOCK decodes Go statement coordinates independent of absolute checkout paths.
PROFILE_BLOCK = re.compile(
    r".*?/(cmd/.*?\.go|internal/.*?\.go):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)$"
)
# GO_DIRECTIVE pins coverage block layout to the repository toolchain.
GO_DIRECTIVE = re.compile(r"^go\s+(\d+\.\d+(?:\.\d+)?)$", re.MULTILINE)


def git(*arguments: str) -> str:
    """Return one repository query without interpreting paths as commands."""
    return subprocess.check_output(["git", *arguments], cwd=ROOT, text=True, errors="replace")


def load_manifest() -> dict[str, object]:
    """Load the closed source/test/package inventory and reject unknown fields."""
    value = json.loads(MANIFEST_PATH.read_text(encoding="utf-8"))
    if set(value) != {"version", "base", "production", "tests", "zero_statement_sources", "unit_packages"} or value["version"] != 1:
        raise RuntimeError("invalid feature35 coverage manifest")
    for key in ("production", "tests", "zero_statement_sources", "unit_packages"):
        if not isinstance(value[key], list) or not value[key] or len(value[key]) != len(set(value[key])):
            raise RuntimeError(f"invalid manifest {key}")
    return value


def changed_go_inventory(base: str) -> tuple[set[str], set[str]]:
    """Return every changed/untracked production and test Go path from the approved feature base."""
    tracked = git("diff", "--name-only", "--diff-filter=ACMR", base, "--", "*.go").splitlines()
    untracked = git("ls-files", "--others", "--exclude-standard", "--", "*.go").splitlines()
    names = {name.replace("\\", "/") for name in tracked + untracked}
    return ({name for name in names if not name.endswith("_test.go")}, {name for name in names if name.endswith("_test.go")})


def changed_lines(base: str, production: set[str]) -> dict[str, set[int]]:
    """Return exact added lines for tracked files and all lines for untracked feature sources."""
    untracked = set(git("ls-files", "--others", "--exclude-standard", "--", "*.go").splitlines())
    result: dict[str, set[int]] = {}
    for name in sorted(production):
        if name in untracked:
            result[name] = set(range(1, len((ROOT / name).read_text(encoding="utf-8").splitlines()) + 1))
            continue
        lines: set[int] = set()
        diff = git("diff", "--unified=0", base, "--", name)
        for match in re.finditer(r"^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@", diff, re.MULTILINE):
            start, count = int(match.group(1)), int(match.group(2) or 1)
            lines.update(range(start, start + count))
        result[name] = lines
    return result


def toolchain_environment() -> dict[str, str]:
    """Return an environment pinned to the exact go.mod toolchain and current explicit DSN."""
    match = GO_DIRECTIVE.search((ROOT / "go.mod").read_text(encoding="utf-8"))
    if not match:
        raise RuntimeError("go.mod lacks an exact Go directive")
    toolchain = "go" + match.group(1)
    environment = {**os.environ, "GOTOOLCHAIN": toolchain}
    actual = subprocess.check_output(["go", "env", "GOVERSION"], cwd=ROOT, env=environment, text=True).strip()
    if actual != toolchain:
        raise RuntimeError(f"required toolchain {toolchain}, got {actual}")
    return environment


def generate_profiles(directory: pathlib.Path, manifest: dict[str, object], environment: dict[str, str]) -> list[pathlib.Path]:
    """Generate repeat-stabilized unit and fresh-database PostgreSQL profiles."""
    if not environment.get("FEATUREPROOF_POSTGRES_TEST_DSN"):
        raise RuntimeError("FEATUREPROOF_POSTGRES_TEST_DSN is required; run inside feature35-postgres-gate.ps1")
    packages = [str(value) for value in manifest["unit_packages"]]
    profiles: list[pathlib.Path] = []
    for index, package in enumerate(packages):
        profile = directory / f"unit-{index}.cover"
        subprocess.run(["go", "test", "-p=1", "-count=3", f"-coverpkg={package}", f"-coverprofile={profile}", package], cwd=ROOT, env=environment, check=True)
        profiles.append(profile)
    postgres_cover_packages = ",".join(package for package in packages if package.startswith("./internal/"))
    postgres_profile = directory / "postgres.cover"
    subprocess.run(["go", "test", "-p=1", "-tags=postgres_integration", "-count=3", f"-coverpkg={postgres_cover_packages},./cmd/featureproof,./cmd/featureproof-postgres-compat,./cmd/featureproof-postgres-gate", f"-coverprofile={postgres_profile}", "./cmd/featureproof-postgres-compat", "./cmd/featureproof-postgres-gate", "./cmd/featureproof", "./internal/controlplane/postgres", "./internal/planapproval"], cwd=ROOT, env=environment, check=True)
    profiles.append(postgres_profile)
    return profiles


def merged_blocks(profiles: list[pathlib.Path], changed: dict[str, set[int]]) -> dict[tuple[str, int, int, int, int, int], int]:
    """Merge identical blocks by maximum execution count without inflating their denominator."""
    blocks: dict[tuple[str, int, int, int, int, int], int] = {}
    for profile in profiles:
        for line in profile.read_text(encoding="utf-8").splitlines()[1:]:
            match = PROFILE_BLOCK.match(line)
            if not match:
                continue
            name = match.group(1)
            start_line, start_column, end_line, end_column, statements, count = map(int, match.groups()[1:])
            if name not in changed or not changed[name].intersection(range(start_line, end_line + 1)):
                continue
            key = (name, start_line, start_column, end_line, end_column, statements)
            blocks[key] = max(blocks.get(key, 0), count)
    return blocks


def main() -> int:
    """Audit inventory, generate profiles, print exact coverage and fail below 85 percent without rounding."""
    manifest = load_manifest()
    base = str(manifest["base"])
    production, tests = changed_go_inventory(base)
    expected_production = {str(value) for value in manifest["production"]}
    expected_tests = {str(value) for value in manifest["tests"]}
    if production != expected_production:
        raise RuntimeError(f"production inventory mismatch missing={sorted(production-expected_production)} stale={sorted(expected_production-production)}")
    if tests != expected_tests:
        raise RuntimeError(f"test inventory mismatch missing={sorted(tests-expected_tests)} stale={sorted(expected_tests-tests)}")
    changed = changed_lines(base, production)
    environment = toolchain_environment()
    with tempfile.TemporaryDirectory(prefix="feature35-coverage-") as temporary:
        first_directory = pathlib.Path(temporary) / "first"
        second_directory = pathlib.Path(temporary) / "second"
        first_directory.mkdir()
        second_directory.mkdir()
        first_blocks = merged_blocks(generate_profiles(first_directory, manifest, environment), changed)
        second_blocks = merged_blocks(generate_profiles(second_directory, manifest, environment), changed)
        first_coverage = {key: count > 0 for key, count in first_blocks.items()}
        second_coverage = {key: count > 0 for key, count in second_blocks.items()}
        if first_coverage != second_coverage:
            unstable = sorted(key for key in first_coverage.keys() | second_coverage.keys() if first_coverage.get(key) != second_coverage.get(key))
            raise RuntimeError("non-deterministic changed-statement coverage: " + ", ".join(f"{key[0]}:{key[1]}" for key in unstable))
        blocks = first_blocks
    by_file: dict[str, tuple[int, int]] = {}
    for (name, _sl, _sc, _el, _ec, statements), count in blocks.items():
        covered, total = by_file.get(name, (0, 0))
        by_file[name] = (covered + (statements if count > 0 else 0), total + statements)
    zero_statement_sources = {str(value) for value in manifest["zero_statement_sources"]}
    if not zero_statement_sources.issubset(production):
        raise RuntimeError("zero-statement inventory is not production-owned")
    if zero_statement_sources.intersection(by_file):
        raise RuntimeError("zero-statement inventory contains executable changed blocks")
    missing = sorted(name for name, lines in changed.items() if lines and name not in by_file and name not in zero_statement_sources)
    if missing:
        raise RuntimeError("coverage profiles omitted changed production sources: " + ", ".join(missing))
    covered = sum(value[0] for value in by_file.values())
    total = sum(value[1] for value in by_file.values())
    if total == 0:
        raise RuntimeError("no changed statements found")
    getcontext().prec = 40
    percent = Decimal(covered) * Decimal(100) / Decimal(total)
    # signature_path lets the outer gate compare independent database runs rather than trusting one mutated database.
    signature_path = os.environ.get("FEATUREPROOF_COVERAGE_SIGNATURE_PATH", "")
    if signature_path:
        # signature records block identities and booleans so a mismatch names coverage drift, not only its aggregate count.
        signature = {
            "blocks": [list(key) + [count > 0] for key, count in sorted(blocks.items())],
            "covered": covered,
            "total": total,
        }
        pathlib.Path(signature_path).write_text(json.dumps(signature, sort_keys=True, separators=(",", ":")), encoding="utf-8")
    print(f"feature35 changed production statements: {covered}/{total} = {percent}%")
    for name, (file_covered, file_total) in sorted(by_file.items()):
        print(f"{file_covered}/{file_total} {name}")
    return 0 if covered * 100 >= total * 85 else 1


if __name__ == "__main__":
    sys.exit(main())
