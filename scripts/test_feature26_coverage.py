"""Behavioral tests for symmetric platform coverage accounting."""

import unittest
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import feature26_coverage as coverage


class PlatformCoverageTests(unittest.TestCase):
    """Exercise Windows-host and Unix-host profiles without rounding or omission."""

    def test_windows_profile_adds_unix_once(self) -> None:
        """A Windows profile retains measured Windows blocks and adds Unix denominator."""
        changed = {name: {1} for name in coverage.PLATFORM_STATEMENTS}
        measured = {
            "internal/fixture/store_lock_windows.go": (10, 17),
            "internal/fixture/store_replace_windows.go": (5, 7),
        }
        coverage.apply_platform_denominator(measured, changed)
        self.assertEqual(sum(total for _, total in measured.values()), 102)

    def test_unix_profile_adds_windows_once(self) -> None:
        """A Unix profile retains measured Unix blocks and adds Windows denominator."""
        changed = {name: {1} for name in coverage.PLATFORM_STATEMENTS}
        measured = {
            "internal/fixture/store_lock_unix.go": (15, 15),
            "internal/fixture/store_replace_unix.go": (1, 1),
        }
        coverage.apply_platform_denominator(measured, changed)
        self.assertEqual(sum(total for _, total in measured.values()), 102)


if __name__ == "__main__":
    unittest.main()
