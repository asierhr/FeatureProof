package correction_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane/contracttest"
	"github.com/asierhr/featureProof/internal/correction"
)

// TestLocalCorrectionStoreSharedContract executes the backend-neutral lineage
// suite against independently reopened filesystem handles.
func TestLocalCorrectionStoreSharedContract(t *testing.T) {
	root := filepath.Join(t.TempDir(), "corrections")
	factory := func() (correction.Store, error) { return correction.NewLocalStore(root) }
	if err := contracttest.RunCorrectionStore(context.Background(), factory, "local-correction-root", "local-correction-child", "contract-project", time.Now().UTC().Truncate(time.Microsecond)); err != nil {
		t.Fatal(err)
	}
}

// TestLocalCorrectionImportExportReplaysOwnedJournal proves the migration DTO
// is derived by the correction projector rather than raw file copying.
func TestLocalCorrectionImportExportReplaysOwnedJournal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "corrections")
	if err := contracttest.RunCorrectionStore(context.Background(), func() (correction.Store, error) { return correction.NewLocalStore(root) }, "export-root", "export-child", "export-project", time.Now().UTC().Truncate(time.Microsecond)); err != nil {
		t.Fatal(err)
	}
	raw, err := correction.ExportImportSnapshot(context.Background(), root)
	var snapshot correction.ImportSnapshot
	if err != nil || json.Unmarshal(raw, &snapshot) != nil || len(snapshot.Chains) != 1 || len(snapshot.Chains["export-root"]) == 0 {
		t.Fatalf("correction export chains=%d err=%v", len(snapshot.Chains), err)
	}
	claimed := correction.ImportClaimedPaths(root)
	if len(claimed) != 2 {
		t.Fatalf("claimed paths=%v", claimed)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = correction.ExportImportSnapshot(cancelled, root); err == nil {
		t.Fatal("cancelled correction export succeeded")
	}
	if exists, existsErr := correction.ImportStoreExists(root); existsErr != nil || !exists {
		t.Fatal("existing correction store not detected", existsErr)
	}
	if exists, existsErr := correction.ImportStoreExists(filepath.Join(t.TempDir(), "absent")); existsErr != nil || exists {
		t.Fatal("absent correction store detected", existsErr)
	}
}
