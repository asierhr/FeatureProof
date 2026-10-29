package githubpr

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
)

// ImportSnapshot is the replayable allowlisted GitHub coordination journal;
// webhook bodies, installation credentials and store locks are never included.
type ImportSnapshot struct {
	Events []StoreEvent `json:"events"`
}

// ExportImportSnapshot replays the consumer-owned journal and emits its safe
// events only after all delivery, association and intent invariants validate.
func ExportImportSnapshot(ctx context.Context, root string) ([]byte, error) {
	store, err := OpenLocalStore(root)
	if err != nil {
		return nil, err
	}
	events, err := store.Events(ctx)
	if err != nil {
		return nil, err
	}
	return json.Marshal(ImportSnapshot{Events: events})
}

// ImportClaimedPaths replaces the complete private local GitHub store with one
// typed journal DTO in the frozen manifest.
func ImportClaimedPaths(root string) []string { return []string{root} }

// ImportStoreExists distinguishes an optional absent GitHub store from an
// unreadable selected source.
func ImportStoreExists(root string) (bool, error) {
	_, err := os.Stat(filepath.Join(root, "events.jsonl"))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
