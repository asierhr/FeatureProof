package correction

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
)

// ImportSnapshot is the secret-free replayable correction journal exported by
// the package that owns its chain and index invariants.
type ImportSnapshot struct {
	Chains map[string][]Event `json:"chains"`
}

// ExportImportSnapshot verifies every local chain with the normal projector
// and returns only append-only domain events, never locks or derived indexes.
func ExportImportSnapshot(ctx context.Context, root string) ([]byte, error) {
	store, err := NewLocalStore(root)
	if err != nil {
		return nil, err
	}
	roots, err := store.roots(ctx)
	if err != nil {
		return nil, err
	}
	snapshot := ImportSnapshot{Chains: make(map[string][]Event, len(roots))}
	for _, rootID := range roots {
		if _, err = store.readChainState(ctx, rootID); err != nil {
			return nil, err
		}
		events, readErr := readEvents(filepath.Join(store.chainDir(rootID), "events.jsonl"))
		if readErr != nil {
			return nil, readErr
		}
		snapshot.Chains[rootID] = events
	}
	return json.Marshal(snapshot)
}

// ImportClaimedPaths returns the private journal and derived-index subtrees
// replaced by ExportImportSnapshot in an offline manifest.
func ImportClaimedPaths(root string) []string {
	return []string{filepath.Join(root, "chains"), filepath.Join(root, "indexes")}
}

// ImportStoreExists distinguishes an absent optional correction store from a
// selected but unreadable source that must fail the offline import.
func ImportStoreExists(root string) (bool, error) {
	_, err := os.Stat(filepath.Join(root, "chains"))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
