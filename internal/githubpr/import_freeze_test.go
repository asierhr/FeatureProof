package githubpr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
)

// importFreezeDigest creates a stable test head without exposing source content.
func importFreezeDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// TestGitHubImportExportReplaysAllowlistedJournal proves offline migration
// observes the consumer-owned event projector and never webhook payload bytes.
func TestGitHubImportExportReplaysAllowlistedJournal(t *testing.T) {
	root := t.TempDir()
	store, err := OpenLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	receipt := DeliveryReceipt{Version: SchemaVersion, DeliveryID: "export-delivery", Event: "issue_comment", PayloadSHA256: importFreezeDigest("payload"), State: "received"}
	if _, created, reserveErr := store.ReserveDelivery(context.Background(), receipt, "export-owner", time.Now().UTC(), time.Minute); reserveErr != nil || !created {
		t.Fatal("reserve", created, reserveErr)
	}
	raw, err := ExportImportSnapshot(context.Background(), root)
	var snapshot ImportSnapshot
	if err != nil || json.Unmarshal(raw, &snapshot) != nil || len(snapshot.Events) != 1 {
		t.Fatalf("github export events=%d err=%v", len(snapshot.Events), err)
	}
	claimed := ImportClaimedPaths(root)
	if len(claimed) != 1 || claimed[0] != root {
		t.Fatalf("claimed paths=%v", claimed)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = ExportImportSnapshot(cancelled, root); err == nil {
		t.Fatal("cancelled github export succeeded")
	}
	if exists, existsErr := ImportStoreExists(root); existsErr != nil || !exists {
		t.Fatal("existing github store not detected", existsErr)
	}
	if exists, existsErr := ImportStoreExists(filepath.Join(t.TempDir(), "absent")); existsErr != nil || exists {
		t.Fatal("absent github store detected", existsErr)
	}
}

// TestExistingLocalStoreRejectsWritesDuringOfflineImport proves the deployed local ingress store observes the shared freeze receipt.
func TestExistingLocalStoreRejectsWritesDuringOfflineImport(t *testing.T) {
	root := t.TempDir()
	store, err := OpenLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	source, err := controlplane.OpenLocalImportStore(root, "github-local")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := source.Freeze(context.Background(), "github-import", "run-1", "project-1", importFreezeDigest("head"), 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	receipt := DeliveryReceipt{Version: 1, DeliveryID: "delivery-1", Event: "issue_comment", PayloadSHA256: importFreezeDigest("request"), State: "received"}
	if _, _, err = store.ReserveDelivery(context.Background(), receipt, "worker-1", time.Now().UTC(), time.Minute); !IsCode(err, CodeConflict) {
		t.Fatal("frozen local store accepted write", err)
	}
	if err = source.Unfreeze(context.Background(), manifest.ImportID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.ReserveDelivery(context.Background(), receipt, "worker-1", time.Now().UTC(), time.Minute); err != nil {
		t.Fatal("unfrozen local store rejected write", err)
	}
}
