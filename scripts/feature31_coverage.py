"""Measure exact multi-platform statement coverage for feature 31 from 6e8742f."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import re
import subprocess
import sys
import tempfile


ROOT = pathlib.Path(__file__).resolve().parents[1]
BASE = "6e8742f"
PACKAGES = ["./cmd/featureproof", "./internal/environment", "./internal/validationworkflow"]
ZERO_STATEMENT_SOURCES = {"internal/validationworkflow/model.go"}
PROFILE_BLOCK = re.compile(
    r".*?/(cmd/.*?\.go|internal/.*?\.go):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)$"
)
# GO_DIRECTIVE extracts the exact repository coverage toolchain authority.
GO_DIRECTIVE = re.compile(r"^go\s+(\d+\.\d+(?:\.\d+)?)$", re.MULTILINE)


def required_toolchain() -> str:
    """Return the exact Go toolchain declared by go.mod for reproducible block layouts."""
    match = GO_DIRECTIVE.search((ROOT / "go.mod").read_text(encoding="utf-8"))
    if not match:
        raise RuntimeError("go.mod has no exact go directive")
    return "go" + match.group(1)


# TOOLCHAIN pins all host coverage commands to the go.mod version.
TOOLCHAIN = required_toolchain()
# GO_ENV carries the explicit pin without depending on caller shell state.
GO_ENV = {**os.environ, "GOTOOLCHAIN": TOOLCHAIN}


def git(*args: str) -> str:
    """Return one repository query as decoded text without interpreting its content."""
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True, errors="replace")


def changed_lines() -> dict[str, set[int]]:
    """Return exact added or modified production lines, including untracked feature sources."""
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


def generate_host_profile(profile: pathlib.Path) -> None:
    """Generate a fresh host profile using the exact repository toolchain."""
    subprocess.run(
        ["go", "test", "-count=1", "-coverpkg=./...", f"-coverprofile={profile}", *PACKAGES],
        cwd=ROOT,
        check=True,
        env=GO_ENV,
    )


def go_identity() -> tuple[str, str, str]:
    """Read the pinned host toolchain identity used to generate the fresh profile."""
    values = subprocess.check_output(
        ["go", "env", "GOVERSION", "GOOS", "GOARCH"], cwd=ROOT, env=GO_ENV, text=True
    ).splitlines()
    if len(values) != 3 or values[0] != TOOLCHAIN:
        raise RuntimeError(f"required toolchain {TOOLCHAIN} is unavailable")
    return values[0], values[1], values[2]


def authenticated_manifest(path_value: str) -> list[pathlib.Path]:
    """Authenticate profile hashes and reject toolchain or platform metadata mismatches before merging."""
    manifest_path = pathlib.Path(path_value).resolve()
    raw = json.loads(manifest_path.read_text(encoding="utf-8"))
    if set(raw) != {"version", "toolchain", "profiles"} or raw["version"] != 1 or raw["toolchain"] != TOOLCHAIN or not isinstance(raw["profiles"], list):
        raise ValueError("invalid coverage manifest authority")
    profiles: list[pathlib.Path] = []
    platforms: set[tuple[str, str]] = set()
    for entry in raw["profiles"]:
        if set(entry) != {"path", "sha256", "goversion", "goos", "goarch"} or entry["goversion"] != TOOLCHAIN or entry["goos"] != "linux" or entry["goarch"] != "amd64" or not re.fullmatch(r"[0-9a-f]{64}", entry["sha256"]):
            raise ValueError("coverage manifest profile identity mismatch")
        profile = pathlib.Path(entry["path"])
        if not profile.is_absolute():
            profile = manifest_path.parent / profile
        profile = profile.resolve()
        if not profile.is_file() or hashlib.sha256(profile.read_bytes()).hexdigest() != entry["sha256"]:
            raise ValueError(f"profile authentication failed: {profile}")
        profiles.append(profile)
        platforms.add((entry["goos"], entry["goarch"]))
    if not profiles or platforms != {("linux", "amd64")}:
        raise ValueError("coverage manifest must contain Linux amd64 evidence")
    return profiles


def profile_layout(profile: pathlib.Path, changed: dict[str, set[int]]) -> dict[str, set[tuple[int, int, int, int, int]]]:
    """Return exact statement layouts so incompatible toolchains cannot silently double the denominator."""
    layout: dict[str, set[tuple[int, int, int, int, int]]] = {}
    for line in profile.read_text(encoding="utf-8").splitlines()[1:]:
        match = PROFILE_BLOCK.match(line)
        if not match or match.group(1) not in changed:
            continue
        name = match.group(1)
        coordinates = tuple(map(int, match.groups()[1:6]))
        layout.setdefault(name, set()).add(coordinates)
    return layout


def validate_layouts(profiles: list[pathlib.Path], changed: dict[str, set[int]]) -> None:
    """Reject any common source whose exact block layout differs across authenticated profiles."""
    canonical: dict[str, set[tuple[int, int, int, int, int]]] = {}
    for profile in profiles:
        for name, layout in profile_layout(profile, changed).items():
            if name in canonical and canonical[name] != layout:
                raise RuntimeError(f"incompatible coverage block layout for {name}")
            canonical[name] = layout


def merge_profiles(profiles: list[pathlib.Path], changed: dict[str, set[int]]) -> dict[tuple[str, int, int, int, int, int], int]:
    """Merge exact Go blocks by file, both coordinates, and statement count using maximum execution count."""
    blocks: dict[tuple[str, int, int, int, int, int], int] = {}
    for profile in profiles:
        lines = profile.read_text(encoding="utf-8").splitlines()
        if not lines or lines[0] not in {"mode: set", "mode: count", "mode: atomic"}:
            raise RuntimeError(f"invalid Go coverage profile: {profile}")
        for line in lines[1:]:
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


def measure(profiles: list[pathlib.Path], changed: dict[str, set[int]]) -> tuple[int, int, dict[str, tuple[int, int]]]:
    """Intersect authenticated exact blocks with changed lines and reject omitted production sources."""
    by_file: dict[str, tuple[int, int]] = {}
    for (name, _start_line, _start_column, _end_line, _end_column, statements), count in merge_profiles(profiles, changed).items():
        covered, total = by_file.get(name, (0, 0))
        by_file[name] = (covered + (statements if count > 0 else 0), total + statements)
    missing = sorted(name for name, lines in changed.items() if lines and name not in by_file and name not in ZERO_STATEMENT_SOURCES)
    if missing:
        raise RuntimeError("coverage profiles omitted changed production sources: " + ", ".join(missing))
    return sum(value[0] for value in by_file.values()), sum(value[1] for value in by_file.values()), by_file


def main() -> int:
    """Pin toolchain, authenticate platform evidence, and enforce 85 percent without rounding."""
    parser = argparse.ArgumentParser()
    parser.add_argument("--manifest", required=True, help="JSON manifest binding Linux profiles to toolchain, platform and SHA-256")
    args = parser.parse_args()
    supplied = authenticated_manifest(args.manifest)
    host_identity = go_identity()
    with tempfile.TemporaryDirectory(prefix="feature31-coverage-") as temporary:
        host = pathlib.Path(temporary) / "host.cover"
        generate_host_profile(host)
        profiles = [host, *supplied]
        changed = changed_lines()
        validate_layouts(profiles, changed)
        covered, total, by_file = measure(profiles, changed)
        print(f"host {host_identity[0]} {host_identity[1]}/{host_identity[2]}")
        for profile in profiles:
            print(f"profile {hashlib.sha256(profile.read_bytes()).hexdigest()} {profile}")
    percent = 100 * covered / total if total else 0
    print(f"feature31 changed production statements: {covered}/{total} = {percent:.6f}%")
    for name, (file_covered, file_total) in sorted(by_file.items()):
        print(f"{100 * file_covered / file_total:5.1f}% {file_covered}/{file_total} {name}")
    return 0 if percent >= 85 else 1


if __name__ == "__main__":
    sys.exit(main())
