//go:build !windows

package fixture

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestUnixStorePrimitives exercises the real advisory lock and atomic rename implementations selected on Unix.
func TestUnixStorePrimitives(t *testing.T) {
	dir := t.TempDir()
	release, err := acquireProcessLock(context.Background(), filepath.Join(dir, "state.lock"))
	if err != nil {
		t.Fatal(err)
	}
	release()
	source, target := filepath.Join(dir, "source"), filepath.Join(dir, "target")
	if err := os.WriteFile(source, []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(source, target); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(target); err != nil || string(raw) != "state" {
		t.Fatalf("raw=%q err=%v", raw, err)
	}
}
