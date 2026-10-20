package contractcatalog

import (
	"context"
	"testing"
)

// TestSourceDiagnosticsAndAffectedOperationsAreValidatedAndRetained proves neutral analysis metadata is bounded and catalog-bound.
func TestSourceDiagnosticsAndAffectedOperationsAreValidatedAndRetained(t *testing.T) {
	snapshot := testSnapshot("derived-meta", "fake", ClassDerived, ConfidenceMedium, CompletenessPartial, OperationKey{"GET", "/pets"})
	snapshot.Diagnostics = []SourceDiagnostic{{Code: "dynamic_route", SafeLocation: "app/routes.go/line-4"}}
	snapshot.AffectedOperations = []OperationKey{{"GET", "/pets"}}
	registry, err := NewRegistry(map[string]ContractSource{"fake": &StaticSource{Snapshot: snapshot}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := registry.Load(context.Background(), []SourceRequest{requestFor(snapshot)}, catalogLimits())
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Build(context.Background(), loaded, catalogLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Diagnostics()) != 1 || len(catalog.AffectedOperations()) != 1 {
		t.Fatalf("metadata lost: %+v %+v", catalog.Diagnostics(), catalog.AffectedOperations())
	}
	hostile := snapshot
	hostile.Diagnostics = []SourceDiagnostic{{Code: "dynamic_route", SafeLocation: `C:\secret\token`}}
	registry, _ = NewRegistry(map[string]ContractSource{"fake": &StaticSource{Snapshot: hostile}})
	if _, err = registry.Load(context.Background(), []SourceRequest{requestFor(hostile)}, catalogLimits()); err == nil {
		t.Fatal("unsafe diagnostic accepted")
	}
	unknown := snapshot
	unknown.AffectedOperations = []OperationKey{{"DELETE", "/missing"}}
	registry, _ = NewRegistry(map[string]ContractSource{"fake": &StaticSource{Snapshot: unknown}})
	if _, err = registry.Load(context.Background(), []SourceRequest{requestFor(unknown)}, catalogLimits()); err == nil {
		t.Fatal("unknown affected operation accepted")
	}
}
