"""Generate and measure feature 28 coverage in one closed ephemeral operation."""

from __future__ import annotations

import argparse
import hashlib
import os
import pathlib
import re
import shutil
import stat
import subprocess
import sys
import tempfile
from collections.abc import Callable
from dataclasses import dataclass

# ROOT anchors source discovery and all hardcoded recipes to this repository.
ROOT = pathlib.Path(__file__).resolve().parents[1]
# DEFAULT_BASE is the human-approved feature 28 specification commit.
DEFAULT_BASE = "9c8aa34"
# PROFILE_BLOCK parses one canonical Go coverprofile statement block.
PROFILE_BLOCK = re.compile(r"^(.*\.go):(\d+)\.\d+,(\d+)\.\d+ (\d+) (\d+)$")
# DECLARATION_ONLY_DIFFS records reviewed changed files whose changed lines add no statements.
DECLARATION_ONLY_DIFFS = {"internal/validationworkflow/model.go"}
# WINDOWS_RECIPES are the only host profile commands the closed gate may execute.
WINDOWS_RECIPES = (
    (
        "core.out",
        (
            "go",
            "test",
            "./cmd/featureproof",
            "./internal/agentplanning",
            "./internal/githubpr",
            "./internal/projectmemory",
            "-count=1",
            "-coverpkg=./...",
        ),
    ),
    (
        "validation.out",
        (
            "go",
            "test",
            "./internal/validationworkflow",
            "-count=1",
            "-coverpkg=./...",
        ),
    ),
    (
        "cmd.out",
        (
            "go",
            "test",
            "./cmd/featureproof",
            "-count=1",
            "-coverpkg=./...",
        ),
    ),
)
# BoundaryHook supports deterministic gate probes without exposing CLI customization.
BoundaryHook = Callable[[str, pathlib.Path], None]
# MAX_PROFILE_BYTES bounds every generated coverage input held in immutable memory.
MAX_PROFILE_BYTES = 16 * 1024 * 1024
# MAX_BINARY_BYTES bounds the Linux test binary transferred from its verified buffer.
MAX_BINARY_BYTES = 64 * 1024 * 1024


@dataclass(frozen=True)
class ProfileBuffer:
    """Carry one verified profile identity and the only bytes parser/merge may consume."""

    name: str
    digest: str
    data: bytes


def git(*args: str) -> str:
    """Read repository metadata without leaking line-ending warnings into gate output."""
    return subprocess.check_output(
        ["git", *args],
        cwd=ROOT,
        text=True,
        errors="replace",
        stderr=subprocess.PIPE,
    )


def changed_lines(base: str) -> dict[str, set[int]]:
    """Return every added or modified production Go line since the approved base."""
    tracked = git(
        "diff", "--name-only", "--diff-filter=ACMR", base, "--", "*.go"
    ).splitlines()
    untracked = git(
        "ls-files", "--others", "--exclude-standard", "--", "*.go"
    ).splitlines()
    result: dict[str, set[int]] = {}
    for name in sorted(set(tracked + untracked)):
        normalized = name.replace("\\", "/")
        if normalized.endswith("_test.go"):
            continue
        if name in untracked:
            source = ROOT / name
            result[normalized] = set(
                range(1, len(source.read_text(encoding="utf-8").splitlines()) + 1)
            )
            continue
        lines: set[int] = set()
        patch = git("diff", "--unified=0", base, "--", name)
        for match in re.finditer(
            r"^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@", patch, re.MULTILINE
        ):
            start = int(match.group(1))
            count = int(match.group(2) or 1)
            lines.update(range(start, start + count))
        result[normalized] = lines
    return result


def read_once(path: pathlib.Path, maximum: int, purpose: str) -> bytes:
    """Read one regular single-link artifact through one no-follow identity-checked handle."""
    flags = os.O_RDONLY | getattr(os, "O_BINARY", 0) | getattr(os, "O_CLOEXEC", 0)
    flags |= getattr(os, "O_NOFOLLOW", 0)
    descriptor = os.open(path, flags)
    try:
        opened = os.fstat(descriptor)
        linked = os.lstat(path)
        if not stat.S_ISREG(opened.st_mode) or not stat.S_ISREG(linked.st_mode):
            raise RuntimeError(f"{purpose} is not a regular no-follow file")
        if opened.st_nlink != 1 or linked.st_nlink != 1:
            raise RuntimeError(f"{purpose} has an aliased link identity")
        if (opened.st_dev, opened.st_ino) != (linked.st_dev, linked.st_ino):
            raise RuntimeError(f"{purpose} path identity changed while opening")
        if opened.st_size <= 0 or opened.st_size > maximum:
            raise RuntimeError(f"{purpose} exceeds its closed size limit")
        chunks: list[bytes] = []
        remaining = maximum + 1
        while remaining > 0:
            chunk = os.read(descriptor, min(1024 * 1024, remaining))
            if not chunk:
                break
            chunks.append(chunk)
            remaining -= len(chunk)
        data = b"".join(chunks)
        after = os.fstat(descriptor)
        if len(data) != opened.st_size or (
            after.st_dev,
            after.st_ino,
            after.st_size,
        ) != (opened.st_dev, opened.st_ino, opened.st_size):
            raise RuntimeError(f"{purpose} changed or truncated during its single read")
        if len(data) > maximum:
            raise RuntimeError(f"{purpose} exceeds its closed size limit")
        return data
    finally:
        os.close(descriptor)


def source_hash(base: str) -> str:
    """Bind one verification run to every changed production source byte."""
    digest = hashlib.sha256()
    for name in sorted(changed_lines(base)):
        path = ROOT / name
        digest.update(name.encode("utf-8"))
        digest.update(b"\0")
        digest.update(path.read_bytes())
        digest.update(b"\0")
    return digest.hexdigest()


def run_checked(
    command: list[str],
    *,
    environment: dict[str, str] | None = None,
    input_bytes: bytes | None = None,
) -> subprocess.CompletedProcess[bytes]:
    """Execute one hardcoded recipe silently and retain diagnostics only for failure."""
    completed = subprocess.run(
        command,
        cwd=ROOT,
        env=environment,
        input=input_bytes,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if completed.returncode != 0:
        detail = (completed.stdout + completed.stderr)[-4096:].decode(
            "utf-8", errors="replace"
        )
        raise RuntimeError(f"verification command failed ({completed.returncode}): {detail}")
    return completed


def generate_windows_profiles(directory: pathlib.Path) -> list[pathlib.Path]:
    """Generate the three allowlisted Windows profiles inside the private run root."""
    profiles: list[pathlib.Path] = []
    for name, recipe in WINDOWS_RECIPES:
        profile = directory / name
        run_checked([*recipe, f"-coverprofile={profile}"])
        if not profile.is_file():
            raise RuntimeError(f"allowlisted recipe omitted profile {name}")
        profiles.append(profile)
    return profiles


def linux_runner_script() -> bytes:
    """Return the exact POSIX shell bytes that verify and execute one focal binary."""
    return b"""#!/bin/sh
set -eu
remote_directory=$1
expected_hash=$2
test_boundary=${3:-}
remote_binary=$remote_directory/projectmemory.test
remote_profile=$remote_directory/coverage.out
trap 'rm -rf -- "$remote_directory"' EXIT HUP INT TERM
exec 3< "$remote_binary"
exec 4<&3
exec 5<&3
if [ -n "$test_boundary" ]; then
    : > "$test_boundary.opened"
    while [ ! -f "$test_boundary.continue" ]; do sleep 0.01; done
fi
rm -- "$remote_binary"
before=$(sha256sum "/proc/$$/fd/3" | awk '{print $1}')
test "$before" = "$expected_hash"
"/proc/$$/fd/4" -test.count=1 -test.coverprofile="$remote_profile" >/dev/null
after=$(sha256sum "/proc/$$/fd/5" | awk '{print $1}')
test "$after" = "$expected_hash"
exec 6< "$remote_profile"
rm -- "$remote_profile"
cat "/proc/$$/fd/6"
"""


def generate_linux_profile(
    directory: pathlib.Path, boundary: BoundaryHook | None = None
) -> pathlib.Path:
    """Build, transfer, execute, and retrieve Linux evidence without accepting inputs."""
    binary = directory / "projectmemory-linux.test"
    profile = directory / "linux.out"
    environment = os.environ.copy()
    environment.update({"GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0"})
    run_checked(
        [
            "go",
            "test",
            "-c",
            "./internal/projectmemory",
            "-coverpkg=./internal/projectmemory",
            f"-o={binary}",
        ],
        environment=environment,
    )
    binary_bytes = read_once(binary, MAX_BINARY_BYTES, "generated Linux binary")
    binary_hash = hashlib.sha256(binary_bytes).hexdigest()
    if boundary is not None:
        boundary("linux.after_build", directory)
    token = directory.name.replace("feature28-coverage-", "")
    if not re.fullmatch(r"[A-Za-z0-9_-]+", token):
        raise RuntimeError("unsafe private coverage run identity")
    remote_directory = f"/tmp/feature28-{token}"
    remote_binary = f"{remote_directory}/projectmemory.test"
    try:
        run_checked(
            ["wsl.exe", "-d", "docker-desktop", "mkdir", "-m", "700", "--", remote_directory]
        )
        run_checked(
            ["wsl.exe", "-d", "docker-desktop", "dd", f"of={remote_binary}", "status=none"],
            input_bytes=binary_bytes,
        )
        run_checked(
            ["wsl.exe", "-d", "docker-desktop", "chmod", "500", "--", remote_binary]
        )
        profile.write_bytes(
            run_checked(
                [
                    "wsl.exe",
                    "-d",
                    "docker-desktop",
                    "sh",
                    "-s",
                    "--",
                    remote_directory,
                    binary_hash,
                ],
                input_bytes=linux_runner_script(),
            ).stdout
        )
        if boundary is not None:
            boundary("linux.after_profile", directory)
        return profile
    finally:
        subprocess.run(
            ["wsl.exe", "-d", "docker-desktop", "rm", "-rf", "--", remote_directory],
            cwd=ROOT,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )


def repository_name(profile_name: str) -> str | None:
    """Normalize one module-qualified coverage filename to a repository path."""
    normalized = profile_name.replace("\\", "/")
    for marker in ("/cmd/", "/internal/"):
        position = normalized.find(marker)
        if position >= 0:
            return normalized[position + 1 :]
    if normalized.startswith(("cmd/", "internal/")):
        return normalized
    return None


def profile_lines(profile: ProfileBuffer) -> list[str]:
    """Decode and structurally validate one immutable profile buffer."""
    try:
        lines = profile.data.decode("utf-8").splitlines()
    except UnicodeDecodeError as error:
        raise RuntimeError(f"invalid generated profile encoding: {profile.name}") from error
    if not lines or lines[0] not in {"mode: set", "mode: count", "mode: atomic"}:
        raise RuntimeError(f"invalid generated Go coverprofile: {profile.name}")
    for line in lines[1:]:
        if PROFILE_BLOCK.match(line) is None:
            raise RuntimeError(f"invalid generated coverage block in {profile.name}")
    return lines


def load_profiles(paths: list[pathlib.Path]) -> list[ProfileBuffer]:
    """Open every generated profile once and reject truncation, oversize, or duplicates."""
    profiles: list[ProfileBuffer] = []
    digests: set[str] = set()
    for path in paths:
        data = read_once(path, MAX_PROFILE_BYTES, f"generated profile {path.name}")
        digest = hashlib.sha256(data).hexdigest()
        if digest in digests:
            raise RuntimeError("generated profiles contain duplicate bytes")
        profile = ProfileBuffer(name=path.name, digest=digest, data=data)
        profile_lines(profile)
        profiles.append(profile)
        digests.add(digest)
    return profiles


def merge_profiles(profiles: list[ProfileBuffer]) -> dict[tuple[str, int, int, int], int]:
    """Union immutable profile buffers and count common statement blocks once."""
    blocks: dict[tuple[str, int, int, int], int] = {}
    for profile in profiles:
        for line in profile_lines(profile)[1:]:
            match = PROFILE_BLOCK.match(line)
            if match is None:  # profile_lines already establishes this invariant.
                raise RuntimeError(f"invalid generated coverage block in {profile.name}")
            name = repository_name(match.group(1))
            if name is None:
                continue
            start, end, statements, count = map(int, match.groups()[1:])
            key = (name, start, end, statements)
            blocks[key] = max(blocks.get(key, 0), count)
    return blocks


def source_has_executable_code(path: pathlib.Path) -> bool:
    """Distinguish declaration-only model files from sources requiring profile blocks."""
    source = path.read_text(encoding="utf-8")
    return bool(re.search(r"(?m)^func\s+(?:\([^)]*\)\s*)?[A-Za-z_]", source))


def measure(
    changed: dict[str, set[int]],
    blocks: dict[tuple[str, int, int, int], int],
) -> tuple[int, int, dict[str, tuple[int, int]]]:
    """Intersect fresh statement blocks with changed lines and reject source omissions."""
    by_file: dict[str, tuple[int, int]] = {}
    for (name, start, end, statements), count in blocks.items():
        if name not in changed or not changed[name].intersection(range(start, end + 1)):
            continue
        covered, total = by_file.get(name, (0, 0))
        by_file[name] = (
            covered + (statements if count > 0 else 0),
            total + statements,
        )
    missing = sorted(
        name
        for name, lines in changed.items()
        if lines
        and name not in by_file
        and name not in DECLARATION_ONLY_DIFFS
        and source_has_executable_code(ROOT / name)
    )
    if missing:
        raise RuntimeError(
            "coverage profiles omitted changed production sources: " + ", ".join(missing)
        )
    return (
        sum(covered for covered, _ in by_file.values()),
        sum(total for _, total in by_file.values()),
        by_file,
    )


def verify_gate(
    *,
    boundary: BoundaryHook | None = None,
    temporary_parent: pathlib.Path | None = None,
) -> tuple[int, int, dict[str, tuple[int, int]]]:
    """Run every allowlisted recipe, measure immediately, and always erase evidence."""
    before = source_hash(DEFAULT_BASE)
    directory = pathlib.Path(
        tempfile.mkdtemp(
            prefix="feature28-coverage-",
            dir=str(temporary_parent) if temporary_parent is not None else None,
        )
    )
    try:
        paths = generate_windows_profiles(directory)
        paths.append(generate_linux_profile(directory, boundary))
        profiles = load_profiles(paths)
        loaded_source = source_hash(DEFAULT_BASE)
        if before != loaded_source:
            raise RuntimeError("production source changed while generating coverage buffers")
        if boundary is not None:
            boundary("profiles.after_load", directory)
        blocks = merge_profiles(profiles)
        if boundary is not None:
            boundary("measure.before", directory)
        result = measure(changed_lines(DEFAULT_BASE), blocks)
        if boundary is not None:
            boundary("measure.after", directory)
        if before != source_hash(DEFAULT_BASE):
            raise RuntimeError("production source changed during coverage measurement")
        return result
    finally:
        shutil.rmtree(directory, ignore_errors=True)


def parse_args(arguments: list[str]) -> argparse.Namespace:
    """Accept only the closed verify operation and no evidence or command options."""
    parser = argparse.ArgumentParser()
    parser.add_argument("operation", choices=("verify",))
    return parser.parse_args(arguments)


def main(arguments: list[str]) -> int:
    """Print only the reproducible result of the closed ephemeral verification."""
    parse_args(arguments)
    covered, total, by_file = verify_gate()
    percent = 100 * covered / total if total else 0.0
    print(f"feature28 changed production statements: {covered}/{total} = {percent:.6f}%")
    for name, (file_covered, file_total) in sorted(by_file.items()):
        print(f"{100 * file_covered / file_total:7.3f}% {file_covered}/{file_total} {name}")
    return 0 if total and percent >= 85.0 else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
