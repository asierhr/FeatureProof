package cleanup_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane/contracttest"
	cleanup "github.com/asierhr/featureProof/internal/environment/cleanup"
)

// TestLocalCleanupStoreSharedContract executes the backend-neutral scheduling
// and cursor suite against independently reopened filesystem handles.
func TestLocalCleanupStoreSharedContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cleanup.json")
	factory := func() (contracttest.CleanupStore, error) { return cleanup.OpenLocalStore(path) }
	if err := contracttest.RunCleanupStore(context.Background(), factory, "local-cleanup-contract", "contract-project", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

// TestLocalCleanupImportExportVerifiesOwnedState proves the offline DTO is
// produced only by reopening the consumer-owned item and capacity stores.
func TestLocalCleanupImportExportVerifiesOwnedState(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	itemPath := filepath.Join(root, "items.json")
	if err := contracttest.RunCleanupStore(ctx, func() (contracttest.CleanupStore, error) { return cleanup.OpenLocalStore(itemPath) }, "export-cleanup", "export-project", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	raw, err := cleanup.ExportImportSnapshot(ctx, itemPath)
	var items cleanup.ImportSnapshot
	if err != nil || json.Unmarshal(raw, &items) != nil || len(items.Items) != 1 || len(items.Events) != 1 || len(items.Cursors) != 1 {
		t.Fatalf("cleanup export items=%d events=%d cursors=%d err=%v", len(items.Items), len(items.Events), len(items.Cursors), err)
	}
	for source, cursor := range items.Cursors {
		if cursor.Source != source || cursor.Epoch != 1 || cursor.Digest == "" {
			t.Fatalf("cleanup cursor was not normalized for PostgreSQL: source=%q cursor=%#v", source, cursor)
		}
	}
	claimed := cleanup.ImportClaimedPaths(itemPath)
	if len(claimed) != 4 || claimed[0] != itemPath || claimed[3] != itemPath+".receipts" {
		t.Fatalf("cleanup durable chain claims = %#v", claimed)
	}
	capacityPath := filepath.Join(root, "capacity.json")
	ledger, err := cleanup.OpenCapacityLedger(capacityPath)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := cleanup.SealCapacityPolicy(cleanup.CapacityPolicy{ProjectID: "export-project", MaxEnvironments: 2, MaxCPUMilli: 1000, MaxMemoryBytes: 1 << 20, MaxObjectCount: 100, MaxTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, reserveErr := ledger.Reserve(ctx, cleanup.CapacityRequest{ProjectID: "export-project", RunID: "export-capacity", AcquisitionKey: "export-acquisition", SelectionDigest: testDigest("selection"), Demand: cleanup.ResourceDemand{Environments: 1, CPUMilli: 1, MemoryBytes: 1, ObjectCount: 1}, TTL: time.Minute, Policy: policy}); reserveErr != nil || !created {
		t.Fatal("capacity reserve", created, reserveErr)
	}
	raw, err = cleanup.ExportCapacityImportSnapshot(ctx, capacityPath)
	var capacity cleanup.ImportCapacitySnapshot
	if err != nil || json.Unmarshal(raw, &capacity) != nil || len(capacity.Receipts) != 1 || len(capacity.Events) != 1 {
		t.Fatalf("capacity export receipts=%d events=%d err=%v", len(capacity.Receipts), len(capacity.Events), err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = cleanup.ExportImportSnapshot(cancelled, itemPath); err == nil {
		t.Fatal("cancelled cleanup export accepted")
	}
	if _, err = cleanup.ExportCapacityImportSnapshot(cancelled, capacityPath); err == nil {
		t.Fatal("cancelled capacity export accepted")
	}
}

// testDigest supplies a valid opaque SHA-256 identity to the public cleanup
// contract without exposing store internals.
func testDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
