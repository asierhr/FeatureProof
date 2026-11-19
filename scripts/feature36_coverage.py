"""Generate exact feature 36 coverage, including Unix-only local durability blocks."""

from __future__ import annotations

import io
import os
import pathlib
import subprocess
import sys
from contextlib import redirect_stdout

import feature35_coverage as coverage


# MANIFEST_PATH closes the feature-owned source and test inventory at review time.
coverage.MANIFEST_PATH = coverage.ROOT / "scripts" / "feature36-coverage-manifest.json"
def wsl_path(path: pathlib.Path) -> str:
    """Convert one absolute drive path to the fixed WSL mount representation."""
    absolute = path.resolve()
    drive = absolute.drive.rstrip(":").lower()
    return f"/mnt/{drive}/" + absolute.as_posix().split(":/", 1)[1]


def feature36_profiles(directory: pathlib.Path, manifest: dict[str, object], environment: dict[str, str]) -> list[pathlib.Path]:
    """Add a Linux local-store profile so build-tagged durability code stays in the denominator."""
    packages = [str(value) for value in manifest["unit_packages"]]
    profiles: list[pathlib.Path] = []
    for index, package in enumerate(packages):
        profile = directory / f"unit-{index}.cover"
        subprocess.run(["go", "test", "-p=1", "-count=3", f"-coverpkg={package}", f"-coverprofile={profile}", package], cwd=coverage.ROOT, env=environment, check=True)
        profiles.append(profile)
    postgres_cover_packages = ",".join(packages)
    postgres_profile = directory / "postgres.cover"
    # Tagged executable composition tests are part of the changed-test inventory and must contribute to the same PostgreSQL profile as their adapters.
    subprocess.run(["go", "test", "-p=1", "-tags=postgres_integration", "-count=3", f"-coverpkg={postgres_cover_packages}", f"-coverprofile={postgres_profile}", "./cmd/featureproof", "./internal/controlplane/postgres"], cwd=coverage.ROOT, env=environment, check=True)
    profiles.append(postgres_profile)
    distro = environment.get("FEATUREPROOF_COVERAGE_WSL_DISTRO", "")
    if not distro:
        raise RuntimeError("FEATUREPROOF_COVERAGE_WSL_DISTRO is required inside the isolated gate")
    binary = directory / "projectconfig-local-linux"
    profile = directory / "projectconfig-local-linux.cover"
    linux_environment = {**environment, "GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0"}
    subprocess.run(["go", "test", "-c", "-cover", "-coverpkg=./internal/projectconfig/local", "-o", str(binary), "./internal/projectconfig/local"], cwd=coverage.ROOT, env=linux_environment, check=True)
    subprocess.run(["wsl.exe", "-d", distro, "--", wsl_path(binary), "-test.count=1", f"-test.coverprofile={wsl_path(profile)}"], cwd=coverage.ROOT, env=environment, check=True)
    profiles.append(profile)
    return profiles


# generate_profiles keeps the exact feature35 algorithm while adding platform evidence.
coverage.generate_profiles = feature36_profiles


def main() -> int:
    """Run the exact gate and optionally persist its aggregate line for review evidence."""
    output = io.StringIO()
    with redirect_stdout(output):
        result = coverage.main()
    rendered = output.getvalue()
    print(rendered, end="")
    result_path = os.environ.get("FEATUREPROOF_COVERAGE_RESULT_PATH", "")
    if result_path:
        aggregate = next((line for line in rendered.splitlines() if line.startswith("feature35 changed production statements:")), "")
        if not aggregate:
            raise RuntimeError("coverage aggregate line missing")
        pathlib.Path(result_path).write_text(aggregate + "\n", encoding="utf-8")
    return result


if __name__ == "__main__":
    sys.exit(main())
