"""Generate exact feature 37 coverage, including Unix-only durable stores."""

from __future__ import annotations

import pathlib
import subprocess

import feature35_coverage as coverage


# MANIFEST_PATH closes the complete feature-owned Go source inventory.
coverage.MANIFEST_PATH = coverage.ROOT / "scripts" / "feature37-coverage-manifest.json"


def wsl_path(path: pathlib.Path) -> str:
    """Convert one absolute drive path to its fixed WSL mount representation."""
    absolute = path.resolve()
    drive = absolute.drive.rstrip(":").lower()
    return f"/mnt/{drive}/" + absolute.as_posix().split(":/", 1)[1]


def feature37_profiles(directory: pathlib.Path, manifest: dict[str, object], environment: dict[str, str]) -> list[pathlib.Path]:
    """Generate repeat-stabilized profiles plus both Unix durability implementations."""
    profiles = coverage.generate_profiles_base(directory, manifest, environment)
    distro = environment.get("FEATUREPROOF_COVERAGE_WSL_DISTRO", "")
    if not distro:
        raise RuntimeError("FEATUREPROOF_COVERAGE_WSL_DISTRO is required inside the isolated gate")
    linux_environment = {**environment, "GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0"}
    for name, package in (
        ("projectconfig-local-linux", "./internal/projectconfig/local"),
        ("tenantsecurity-local-linux", "./internal/tenantsecurity/local"),
    ):
        binary = directory / name
        profile = directory / f"{name}.cover"
        subprocess.run(
            ["go", "test", "-c", "-cover", f"-coverpkg={package}", "-o", str(binary), package],
            cwd=coverage.ROOT,
            env=linux_environment,
            check=True,
        )
        subprocess.run(
            ["wsl.exe", "-d", distro, "--", wsl_path(binary), "-test.count=1", f"-test.coverprofile={wsl_path(profile)}"],
            cwd=coverage.ROOT,
            env=environment,
            check=True,
        )
        profiles.append(profile)
    return profiles


# Keep an immutable reference before replacing the shared generator.
coverage.generate_profiles_base = coverage.generate_profiles
coverage.generate_profiles = feature37_profiles


if __name__ == "__main__":
    raise SystemExit(coverage.main())
