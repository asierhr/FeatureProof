package environment_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/asierhr/featureProof/internal/controlplane/contracttest"
	"github.com/asierhr/featureProof/internal/environment"
)

// cleanupLifecycleScanProbe isolates bridge pagination and projection failures
// without granting any lifecycle mutation authority.
type cleanupLifecycleScanProbe struct {
	environment.Store
	pages   [][]environment.Record
	next    []string
	failure error
	calls   int
}

// ScanCleanupLifecycle returns the configured page sequence or one storage failure.
func (probe *cleanupLifecycleScanProbe) ScanCleanupLifecycle(context.Context, string, int) ([]environment.Record, string, error) {
	if probe.failure != nil {
		return nil, "", probe.failure
	}
	index := probe.calls
	probe.calls++
	if index >= len(probe.pages) {
		return nil, "", nil
	}
	cursor := ""
	if index < len(probe.next) {
		cursor = probe.next[index]
	}
	return append([]environment.Record(nil), probe.pages[index]...), cursor, nil
}

// TestMemoryCapacityAdmissionSharedContract executes the backend-neutral quota
// suite against the local single-process implementation.
func TestMemoryCapacityAdmissionSharedContract(t *testing.T) {
	admission, err := environment.NewMemoryCapacityAdmission(map[string]environment.CapacityLimits{"contract-project": {Environments: 1, CPUMilli: 100, MemoryBytes: 100, ObjectCount: 10, MaxTTL: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	if err = contracttest.RunCapacityAdmission(context.Background(), admission, "contract-project", "local-capacity-a", "local-capacity-b"); err != nil {
		t.Fatal(err)
	}
}

// TestExportedStoreAndCleanupContractsFailClosed proves persistence adapters
// share the domain transition grammar and cleanup lookup validation.
func TestExportedStoreAndCleanupContractsFailClosed(t *testing.T) {
	if environment.ValidateStoreRecord(environment.Record{}, "run", 1) == nil {
		t.Fatal("invalid lifecycle record accepted")
	}
	if environment.AllowedStoreTransition(environment.StateSelected, environment.StateReleased) {
		t.Fatal("disallowed lifecycle transition accepted")
	}
	event := environment.Event{ID: strings.Repeat("b", 64), SelectionDigest: strings.Repeat("c", 64), ProviderID: "provider", EnvironmentDigest: strings.Repeat("d", 64), State: environment.StateSelected, OccurredAt: time.Unix(1, 0).UTC()}
	sealed, err := environment.SealStoreEvent(event, "run-one")
	if err != nil || sealed.RunID != "run-one" || sealed.Digest == "" {
		t.Fatalf("sealed event=%#v err=%v", sealed, err)
	}
	drifted := event
	drifted.RunID = "run-two"
	if _, err = environment.SealStoreEvent(drifted, "run-one"); err == nil {
		t.Fatal("cross-run lifecycle event accepted")
	}
	incomplete := event
	incomplete.ID = ""
	if _, err = environment.SealStoreEvent(incomplete, "run-one"); err == nil {
		t.Fatal("incomplete lifecycle event accepted")
	}
	bridge := &environment.CleanupBridge{}
	if _, err := bridge.HasLifecycleReservation(context.Background(), "project", "run", strings.Repeat("a", 64)); err == nil {
		t.Fatal("incomplete cleanup bridge accepted reservation lookup")
	}
	root := filepath.Join(t.TempDir(), "environment-store")
	lifecycle, err := environment.OpenLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = lifecycle.ScanCleanupLifecycle(context.Background(), "", 0); !environment.IsCategory(err, environment.CategoryInvalid) {
		t.Fatalf("invalid cleanup scan limit err=%v", err)
	}
	if err = os.RemoveAll(filepath.Join(root, "records")); err != nil {
		t.Fatal(err)
	}
	brokenBridge := &environment.CleanupBridge{Store: lifecycle}
	if _, _, err = brokenBridge.ScanLifecycleCleanup(context.Background(), "", 1); err == nil {
		t.Fatal("missing lifecycle projection was treated as an empty cleanup page")
	}
	lifecycle, err = environment.OpenLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	importer, err := controlplane.OpenLocalImportStore(root, "environment-freeze")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = importer.Freeze(context.Background(), "environment-import", "environment-run", "project", strings.Repeat("a", 64), 1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, _, err = lifecycle.Reserve(context.Background(), environment.Record{RunID: "frozen-run"}); !environment.IsCategory(err, environment.CategoryConflict) {
		t.Fatalf("frozen lifecycle store write err=%v", err)
	}
	filteredBridge := &environment.CleanupBridge{Store: &cleanupLifecycleScanProbe{pages: [][]environment.Record{{{Selection: environment.Selection{Kind: environment.KindExistingURL}, LifecycleHandle: "existing"}}}}}
	if records, _, scanErr := filteredBridge.ScanLifecycleCleanup(context.Background(), "", 1); scanErr != nil || len(records) != 0 {
		t.Fatalf("existing target entered cleanup discovery: %#v %v", records, scanErr)
	}
	invalidBridge := &environment.CleanupBridge{Store: &cleanupLifecycleScanProbe{pages: [][]environment.Record{{{RunID: "invalid-managed", Selection: environment.Selection{Kind: environment.KindManaged}, LifecycleHandle: "opaque"}}}}}
	if _, _, err = invalidBridge.ScanLifecycleCleanup(context.Background(), "", 1); err == nil {
		t.Fatal("invalid managed cleanup authority was projected")
	}
	failedLookup := &environment.CleanupBridge{Store: &cleanupLifecycleScanProbe{failure: errors.New("scan unavailable")}}
	if _, err = failedLookup.LoadCleanupBinding(context.Background(), strings.Repeat("f", 64)); err == nil {
		t.Fatal("cleanup binding scan failure was hidden")
	}
	pagedLookup := &environment.CleanupBridge{Store: &cleanupLifecycleScanProbe{pages: [][]environment.Record{nil, nil}, next: []string{"next", ""}}}
	if _, err = pagedLookup.LoadCleanupBinding(context.Background(), strings.Repeat("f", 64)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("paged cleanup absence err=%v", err)
	}
}
