"""Behavioral probes for the closed ephemeral feature 28 coverage gate."""

import contextlib
import io
import pathlib
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

# SCRIPT_DIRECTORY lets unittest import the colocated gate from any working directory.
SCRIPT_DIRECTORY = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPT_DIRECTORY))

import feature28_coverage as coverage


class Feature28CoverageTests(unittest.TestCase):
    """Prove external evidence and command tampering cannot enter the closed gate."""

    def fake_windows(self, directory: pathlib.Path) -> list[pathlib.Path]:
        """Create three distinct private profiles for gate lifecycle probes."""
        profiles: list[pathlib.Path] = []
        for index, name in enumerate(("core.out", "validation.out", "cmd.out"), 1):
            profile = directory / name
            profile.write_text(
                f"mode: set\nmodule/internal/x.go:{index}.1,{index}.2 1 1\n",
                encoding="utf-8",
            )
            profiles.append(profile)
        return profiles

    def fake_linux(
        self, directory: pathlib.Path, boundary: coverage.BoundaryHook | None = None
    ) -> pathlib.Path:
        """Create one private Linux profile without accepting caller evidence."""
        profile = directory / "linux.out"
        profile.write_text(
            "mode: set\nmodule/internal/x.go:4.1,4.2 1 1\n", encoding="utf-8"
        )
        return profile

    def bash_parser(self) -> str:
        """Resolve a real Bash parser used against the exact runner byte stream."""
        candidates = (
            pathlib.Path("C:/Program Files/Git/bin/bash.exe"),
            pathlib.Path("C:/msys64/usr/bin/bash.exe"),
        )
        for candidate in candidates:
            if candidate.is_file():
                return str(candidate)
        resolved = shutil.which("bash")
        if resolved is None:
            self.fail("bash is required to validate the exact Linux runner bytes")
        return resolved

    def test_cli_accepts_only_verify_without_paths_commands_or_manifests(self) -> None:
        """Profile, command, manifest, and other post-hoc options are syntax errors."""
        self.assertEqual(coverage.parse_args(["verify"]).operation, "verify")
        for arguments in (
            ["verify", "--profile", "stale.out"],
            ["verify", "--command", "go test"],
            ["verify", "--manifest", "evidence.json"],
            ["run"],
        ):
            with contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit):
                    coverage.parse_args(arguments)

    def test_stale_posthoc_and_duplicate_files_are_ignored(self) -> None:
        """Files outside the fresh private run cannot affect measurement or naming."""
        with tempfile.TemporaryDirectory() as temporary:
            parent = pathlib.Path(temporary)
            stale = parent / "core.out"
            stale.write_text("forged stale duplicate", encoding="utf-8")
            with mock.patch.object(
                coverage, "source_hash", side_effect=("same", "same", "same")
            ), mock.patch.object(
                coverage, "generate_windows_profiles", side_effect=self.fake_windows
            ), mock.patch.object(
                coverage, "generate_linux_profile", side_effect=self.fake_linux
            ), mock.patch.object(
                coverage, "measure", return_value=(9, 10, {"internal/x.go": (9, 10)})
            ):
                result = coverage.verify_gate(temporary_parent=parent)
            self.assertEqual(result[:2], (9, 10))
            self.assertEqual(stale.read_text(encoding="utf-8"), "forged stale duplicate")
            self.assertEqual(list(parent.iterdir()), [stale])

    def test_source_drift_fails_and_interrupted_run_leaves_no_evidence(self) -> None:
        """Source A→B and command interruption both erase the private run directory."""
        with tempfile.TemporaryDirectory() as temporary:
            parent = pathlib.Path(temporary)
            with mock.patch.object(
                coverage, "source_hash", side_effect=("A", "B")
            ), mock.patch.object(
                coverage, "generate_windows_profiles", side_effect=self.fake_windows
            ), mock.patch.object(
                coverage, "generate_linux_profile", side_effect=self.fake_linux
            ):
                with self.assertRaisesRegex(RuntimeError, "source changed"):
                    coverage.verify_gate(temporary_parent=parent)
            self.assertEqual(list(parent.iterdir()), [])
            with mock.patch.object(
                coverage, "source_hash", return_value="A"
            ), mock.patch.object(
                coverage,
                "generate_windows_profiles",
                side_effect=RuntimeError("interrupted command"),
            ):
                with self.assertRaisesRegex(RuntimeError, "interrupted"):
                    coverage.verify_gate(temporary_parent=parent)
            self.assertEqual(list(parent.iterdir()), [])

    def test_profile_swap_after_single_load_cannot_change_merged_bytes(self) -> None:
        """A post-validation path swap cannot alter immutable buffers passed to merge."""
        with tempfile.TemporaryDirectory() as temporary:
            parent = pathlib.Path(temporary)

            def replace_profile(stage: str, directory: pathlib.Path) -> None:
                """Swap a profile path after its only handle has supplied the buffer."""
                if stage == "profiles.after_load":
                    (directory / "core.out").write_text("replacement", encoding="utf-8")

            with mock.patch.object(
                coverage, "source_hash", return_value="same"
            ), mock.patch.object(
                coverage, "generate_windows_profiles", side_effect=self.fake_windows
            ), mock.patch.object(
                coverage, "generate_linux_profile", side_effect=self.fake_linux
            ), mock.patch.object(
                coverage, "measure", return_value=(9, 10, {"internal/x.go": (9, 10)})
            ):
                result = coverage.verify_gate(
                    boundary=replace_profile, temporary_parent=parent
                )
            self.assertEqual(result[:2], (9, 10))
            self.assertEqual(list(parent.iterdir()), [])

    def test_linux_binary_swap_after_single_load_cannot_change_transfer_bytes(self) -> None:
        """A binary path swap after loading cannot change the bytes sent to WSL."""
        with tempfile.TemporaryDirectory() as temporary:
            directory = pathlib.Path(temporary)
            runner_invocations = 0

            def fake_run(
                command: list[str],
                *,
                environment: dict[str, str] | None = None,
                input_bytes: bytes | None = None,
            ) -> subprocess.CompletedProcess[bytes]:
                """Model build and WSL while asserting transfer uses the loaded bytes."""
                nonlocal runner_invocations
                if command[0] == "go":
                    output = next(
                        argument[3:] for argument in command if argument.startswith("-o=")
                    )
                    pathlib.Path(output).write_bytes(b"compiled")
                    return subprocess.CompletedProcess(command, 0, b"", b"")
                if command[0] == "wsl.exe" and "dd" in command:
                    self.assertEqual(input_bytes, b"compiled")
                if command[0] == "wsl.exe" and command[3:5] == ["sh", "-s"]:
                    runner_invocations += 1
                    self.assertEqual(input_bytes, coverage.linux_runner_script())
                    return subprocess.CompletedProcess(
                        command, 0,
                        b"mode: set\nmodule/internal/x.go:4.1,4.2 1 1\n", b""
                    )
                return subprocess.CompletedProcess(command, 0, b"", b"")

            def replace_binary(stage: str, root: pathlib.Path) -> None:
                """Replace the binary after its initial hash and before transfer."""
                if stage == "linux.after_build":
                    (root / "projectmemory-linux.test").write_bytes(b"replacement")

            with mock.patch.object(
                coverage, "run_checked", side_effect=fake_run
            ), mock.patch.object(
                coverage.subprocess,
                "run",
                return_value=subprocess.CompletedProcess([], 0, b"", b""),
            ):
                profile = coverage.generate_linux_profile(directory, replace_binary)
            self.assertTrue(profile.is_file())
            self.assertEqual(runner_invocations, 1)
            self.assertEqual(
                profile.read_bytes(),
                b"mode: set\nmodule/internal/x.go:4.1,4.2 1 1\n",
            )

    def test_exact_linux_runner_bytes_pass_bash_syntax(self) -> None:
        """Bash parses the exact unescaped byte stream delivered to WSL stdin."""
        script = coverage.linux_runner_script()
        self.assertIn(b"before=$(sha256sum", script)
        self.assertIn(b'"/proc/$$/fd/4"', script)
        self.assertIn(b'test "$before"', script)
        self.assertNotIn(b"\\$", script)
        self.assertEqual(script.count(b'< "$remote_binary"'), 1)
        self.assertIn(b"exec 4<&3", script)
        self.assertIn(b"exec 5<&3", script)
        parsed = subprocess.run(
            [self.bash_parser(), "-n"],
            input=script,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
        self.assertEqual(parsed.returncode, 0, parsed.stderr.decode(errors="replace"))

    def test_exact_linux_runner_executes_real_focal_binary(self) -> None:
        """The exact runner bytes execute the real Linux focal binary and return coverage."""
        with tempfile.TemporaryDirectory() as temporary:
            profile = coverage.generate_linux_profile(pathlib.Path(temporary))
            loaded = coverage.load_profiles([profile])
        lines = coverage.profile_lines(loaded[0])
        self.assertTrue(
            any("internal/projectmemory/" in line for line in lines[1:]),
            "real focal profile omitted internal/projectmemory",
        )

    def test_remote_path_swap_after_single_open_executes_original_identity(self) -> None:
        """Replacing the pathname after FD duplication cannot replace executed bytes."""
        original = b"""#!/bin/sh
profile=
for argument in "$@"; do
    case "$argument" in
        -test.coverprofile=*) profile=${argument#*=} ;;
    esac
done
printf 'mode: set\nmodule/internal/projectmemory/probe.go:1.1,1.2 1 1\n' > "$profile"
"""
        replacement = original.replace(b" 1 1\\n", b" 1 0\\n")
        expected_profile = (
            b"mode: set\nmodule/internal/projectmemory/probe.go:1.1,1.2 1 1\n"
        )
        with tempfile.TemporaryDirectory() as temporary:
            token = pathlib.Path(temporary).name.replace("_", "-")
            remote_directory = f"/tmp/feature28-fd-probe-{token}"
            remote_binary = f"{remote_directory}/projectmemory.test"
            remote_replacement = f"{remote_directory}/replacement.test"
            boundary = f"{remote_directory}/boundary"
            process: subprocess.Popen[bytes] | None = None
            try:
                coverage.run_checked(
                    [
                        "wsl.exe", "-d", "docker-desktop", "mkdir", "-m", "700",
                        "--", remote_directory,
                    ]
                )
                coverage.run_checked(
                    [
                        "wsl.exe", "-d", "docker-desktop", "dd",
                        f"of={remote_binary}", "status=none",
                    ],
                    input_bytes=original,
                )
                coverage.run_checked(
                    [
                        "wsl.exe", "-d", "docker-desktop", "chmod", "500", "--",
                        remote_binary,
                    ]
                )
                process = subprocess.Popen(
                    [
                        "wsl.exe", "-d", "docker-desktop", "sh", "-s", "--",
                        remote_directory,
                        coverage.hashlib.sha256(original).hexdigest(),
                        boundary,
                    ],
                    cwd=coverage.ROOT,
                    stdin=subprocess.PIPE,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                )
                assert process.stdin is not None
                process.stdin.write(coverage.linux_runner_script())
                process.stdin.close()
                process.stdin = None
                opened = False
                for _ in range(200):
                    observed = subprocess.run(
                        [
                            "wsl.exe", "-d", "docker-desktop", "test", "-f",
                            f"{boundary}.opened",
                        ],
                        stdout=subprocess.DEVNULL,
                        stderr=subprocess.DEVNULL,
                        check=False,
                    )
                    if observed.returncode == 0:
                        opened = True
                        break
                    time.sleep(0.01)
                self.assertTrue(opened, "runner did not expose its post-open boundary")
                coverage.run_checked(
                    [
                        "wsl.exe", "-d", "docker-desktop", "dd",
                        f"of={remote_replacement}", "status=none",
                    ],
                    input_bytes=replacement,
                )
                coverage.run_checked(
                    [
                        "wsl.exe", "-d", "docker-desktop", "chmod", "500", "--",
                        remote_replacement,
                    ]
                )
                coverage.run_checked(
                    [
                        "wsl.exe", "-d", "docker-desktop", "mv", "-f", "--",
                        remote_replacement, remote_binary,
                    ]
                )
                coverage.run_checked(
                    [
                        "wsl.exe", "-d", "docker-desktop", "touch",
                        f"{boundary}.continue",
                    ]
                )
                output, error = process.communicate(timeout=30)
                self.assertEqual(process.returncode, 0, error.decode(errors="replace"))
                self.assertEqual(output, expected_profile)
            finally:
                if process is not None and process.poll() is None:
                    process.kill()
                    process.wait(timeout=10)
                subprocess.run(
                    [
                        "wsl.exe", "-d", "docker-desktop", "rm", "-rf", "--",
                        remote_directory,
                    ],
                    cwd=coverage.ROOT,
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                    check=False,
                )

    def test_source_drift_during_measure_is_rejected(self) -> None:
        """The final source seal detects a change occurring inside measurement."""
        with tempfile.TemporaryDirectory() as temporary:
            parent = pathlib.Path(temporary)
            with mock.patch.object(
                coverage, "source_hash", side_effect=("same", "same", "changed")
            ), mock.patch.object(
                coverage, "generate_windows_profiles", side_effect=self.fake_windows
            ), mock.patch.object(
                coverage, "generate_linux_profile", side_effect=self.fake_linux
            ), mock.patch.object(
                coverage, "measure", return_value=(9, 10, {"internal/x.go": (9, 10)})
            ):
                with self.assertRaisesRegex(RuntimeError, "during coverage measurement"):
                    coverage.verify_gate(temporary_parent=parent)
            self.assertEqual(list(parent.iterdir()), [])

    def test_single_load_rejects_truncation_oversize_and_duplicate_bytes(self) -> None:
        """Malformed size and duplicate immutable inputs fail before merge."""
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            empty = root / "empty.out"
            empty.write_bytes(b"")
            with self.assertRaisesRegex(RuntimeError, "size limit"):
                coverage.load_profiles([empty])
            large = root / "large.out"
            large.write_bytes(b"mode: set\n123456")
            with mock.patch.object(coverage, "MAX_PROFILE_BYTES", 8):
                with self.assertRaisesRegex(RuntimeError, "size limit"):
                    coverage.load_profiles([large])
            one = root / "one.out"
            two = root / "two.out"
            content = b"mode: set\nmodule/internal/x.go:1.1,1.2 1 1\n"
            one.write_bytes(content)
            two.write_bytes(content)
            with self.assertRaisesRegex(RuntimeError, "duplicate bytes"):
                coverage.load_profiles([one, two])

    def test_common_blocks_are_deduplicated_only_inside_the_fresh_run(self) -> None:
        """Two fresh platform observations of one block contribute one denominator."""
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            windows = root / "windows.out"
            linux = root / "linux.out"
            windows.write_text(
                "mode: set\nmodule/internal/x.go:10.1,12.2 3 0\n", encoding="utf-8"
            )
            linux.write_text(
                "mode: set\nmodule/internal/x.go:10.1,12.2 3 1\n", encoding="utf-8"
            )
            profiles = coverage.load_profiles([windows, linux])
            blocks = coverage.merge_profiles(profiles)
        covered, total, by_file = coverage.measure(
            {"internal/x.go": {11}}, blocks
        )
        self.assertEqual((covered, total), (3, 3))
        self.assertEqual(by_file["internal/x.go"], (3, 3))


if __name__ == "__main__":
    unittest.main()
