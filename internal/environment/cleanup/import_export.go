package cleanup

import (
	"context"
	"encoding/json"
)

// ImportCursor is the safe continuation identity exported after the local
// receipt chain has been verified by the cleanup package that owns it.
type ImportCursor struct {
	Version string `json:"version"`
	Source  string `json:"source"`
	Value   string `json:"value"`
	Digest  string `json:"digest"`
	Epoch   uint64 `json:"epoch"`
}

// ImportSnapshot is the canonical secret-free cleanup projection transferred
// to a backend candidate after consumer-owned anchor validation.
type ImportSnapshot struct {
	Items   map[string]Item         `json:"items"`
	Events  map[string][]Event      `json:"events"`
	Cursors map[string]ImportCursor `json:"cursors,omitempty"`
}

// ExportImportSnapshot reopens and verifies the complete local durable chain,
// then emits only typed cleanup state without filesystem receipt internals.
func ExportImportSnapshot(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store, err := OpenLocalStore(path)
	if err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	snapshot := ImportSnapshot{Items: map[string]Item{}, Events: map[string][]Event{}, Cursors: map[string]ImportCursor{}}
	for id, item := range store.data.Items {
		snapshot.Items[id] = item
	}
	for id, events := range store.data.Events {
		snapshot.Events[id] = append([]Event(nil), events...)
	}
	for source, cursor := range store.data.Cursors {
		epoch := cursor.Epoch
		if epoch == 0 {
			epoch = 1
		}
		snapshot.Cursors[source] = ImportCursor{Version: cursor.Version, Source: cursor.Source, Value: cursor.Value, Digest: cursor.Digest, Epoch: epoch}
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "import_export", Cause: err}
	}
	return data, nil
}

// ImportCapacitySnapshot is the typed quota state exported only after the
// capacity ledger's consumer-owned anchor and receipts have been verified.
type ImportCapacitySnapshot struct {
	Receipts map[string]CapacityReceipt `json:"receipts"`
	Events   map[string][]CapacityEvent `json:"events"`
}

// ExportCapacityImportSnapshot verifies and projects one local capacity ledger
// without exposing its filesystem rollback anchor.
func ExportCapacityImportSnapshot(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ledger, err := OpenCapacityLedger(path)
	if err != nil {
		return nil, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	snapshot := ImportCapacitySnapshot{Receipts: map[string]CapacityReceipt{}, Events: map[string][]CapacityEvent{}}
	for id, receipt := range ledger.receipts {
		snapshot.Receipts[id] = receipt
	}
	for project, events := range ledger.events {
		snapshot.Events[project] = append([]CapacityEvent(nil), events...)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, &Error{Category: CategoryIntegrity, Operation: "capacity_import_export", Cause: err}
	}
	return data, nil
}

// ImportClaimedPaths replaces the complete durable snapshot chain for one
// cleanup or capacity store while leaving sibling consumer stores unclaimed.
func ImportClaimedPaths(path string) []string {
	return []string{path, path + ".head.json", path + ".anchor.json", path + ".receipts"}
}
