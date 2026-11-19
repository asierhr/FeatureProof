package local

import "os"

// writeTestFile replaces a test-owned snapshot to exercise integrity recovery.
func writeTestFile(path string, value []byte) error { return os.WriteFile(path, value, 0o600) }
