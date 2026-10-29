package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/asierhr/featureProof/internal/githubpr"
)

// TestImportRelationPathRejectsUntrustedRelations proves that only bounded,
// relative paths minted by the frozen exporter are eligible for materialization.
func TestImportRelationPathRejectsUntrustedRelations(t *testing.T) {
	t.Parallel()

	valid := "p" + hexBytes("cleanup/store.json")
	if path, ok := importRelationPath(valid); !ok || path != "cleanup/store.json" {
		t.Fatalf("valid relation decoded as path=%q ok=%v", path, ok)
	}

	for _, relation := range []string{"", "typed-cleanup", "pzz", "p" + hexBytes("../escape"), "p" + hexBytes("workflows/../escape.state.json"), "p" + hexBytes(`workflows\\run.state.json`), "p" + hexBytes("workflows//run.state.json"), "p" + hexBytes("a\x00b"), "p" + hexBytes(string(make([]byte, 64)))} {
		if path, ok := importRelationPath(relation); ok || path != "" {
			t.Fatalf("untrusted relation %q decoded as path=%q ok=%v", relation, path, ok)
		}
	}
}

// TestImportRelationAllowlistRejectsEveryUnknownBranch proves each supported
// stream has a closed relation grammar rather than a successful no-op branch.
func TestImportRelationAllowlistRejectsEveryUnknownBranch(t *testing.T) {
	t.Parallel()

	pathRelation := func(path string) string { return "p" + hexBytes(path) }
	valid := []controlplane.ImportDocument{
		{Stream: "workflow", RelationKey: pathRelation("workflows/run.state.json")},
		{Stream: "artifacts", RelationKey: pathRelation("workflows/plans/run.plan.json")},
		{Stream: "executables", RelationKey: pathRelation("workflows/private/run.private.json")},
		{Stream: "functional", RelationKey: pathRelation("workflows/functional-flow/run.flow.json")},
		{Stream: "approval", RelationKey: pathRelation("workflows/approval/run.jsonl")},
		{Stream: "approval", RelationKey: pathRelation("workflows/approval/run.request.1.json")},
		{Stream: "environment", RelationKey: pathRelation("environment/records/run/1.json")},
		{Stream: "environment", RelationKey: pathRelation("environment/events/run/1.json")},
		{Stream: "cleanup", RelationKey: "typed-cleanup"},
		{Stream: "capacity", RelationKey: "typed-capacity"},
		{Stream: "correction", RelationKey: "typed-correction"},
		{Stream: "github", RelationKey: "typed-github"},
		{Stream: "packages", RelationKey: "typed-packages"},
	}
	for _, document := range valid {
		if err := validateImportRelation(document); err != nil {
			t.Fatalf("valid relation rejected for %s: %v", document.Stream, err)
		}
	}

	unknown := []controlplane.ImportDocument{
		{Stream: "workflow", RelationKey: pathRelation("workflows/run.unknown.json")},
		{Stream: "artifacts", RelationKey: pathRelation("workflows/plans/run.unknown.json")},
		{Stream: "executables", RelationKey: pathRelation("workflows/private/run.unknown.json")},
		{Stream: "functional", RelationKey: pathRelation("workflows/functional-flow/run.unknown.json")},
		{Stream: "approval", RelationKey: pathRelation("workflows/approval/run.request.x.json")},
		{Stream: "environment", RelationKey: pathRelation("environment/records/run/head.json")},
		{Stream: "cleanup", RelationKey: "typed-cleanup-unknown"},
		{Stream: "capacity", RelationKey: "typed-capacity-unknown"},
		{Stream: "correction", RelationKey: "typed-correction-unknown"},
		{Stream: "github", RelationKey: "typed-github-unknown"},
		{Stream: "packages", RelationKey: "typed-packages-unknown"},
	}
	for _, document := range unknown {
		if err := validateImportRelation(document); !errors.Is(err, controlplane.ErrIntegrity) {
			t.Fatalf("unknown relation accepted for %s: %v", document.Stream, err)
		}
	}

	pathStreams := []string{"workflow", "artifacts", "executables", "functional", "approval", "environment"}
	for _, document := range valid[:8] {
		for _, wrongStream := range pathStreams {
			if wrongStream == document.Stream {
				continue
			}
			crossStream := document
			crossStream.Stream = wrongStream
			if err := validateImportRelation(crossStream); !errors.Is(err, controlplane.ErrIntegrity) {
				t.Fatalf("relation for %s accepted by %s", document.Stream, wrongStream)
			}
		}
	}

	for _, hostilePath := range []string{
		"secrets/run.state.json",
		"workflows/../run.state.json",
		`workflows\\run.state.json`,
		"workflows/nested/run.state.json",
		"environment/records/run/0.json",
		"environment/records/run/01.json",
		"environment/records/run/head.json",
		"environment/other/run/1.json",
	} {
		for _, stream := range pathStreams {
			if err := validateImportRelation(controlplane.ImportDocument{Stream: stream, RelationKey: pathRelation(hostilePath)}); !errors.Is(err, controlplane.ErrIntegrity) {
				t.Fatalf("hostile path %q accepted by %s", hostilePath, stream)
			}
		}
	}
}

// TestImportDocumentOrderingHonorsConsumerDependencies proves that verification
// materializes parent projections before their dependent journals and requests.
func TestImportDocumentOrderingHonorsConsumerDependencies(t *testing.T) {
	t.Parallel()

	document := func(stream, path string, ordinal uint64) controlplane.ImportDocument {
		return controlplane.ImportDocument{Stream: stream, RelationKey: "p" + hexBytes(path), Ordinal: ordinal}
	}
	ordered := orderImportDocuments(map[string][]controlplane.ImportDocument{
		"github":      {document("github", "github/events.json", 1)},
		"environment": {document("environment", "environment/events/1.json", 1), document("environment", "environment/records/1.json", 2)},
		"approval":    {document("approval", "approval/run.request.1.json", 1), document("approval", "approval/run.jsonl", 2)},
		"workflow":    {document("workflow", "workflow/run.state.json", 1)},
	})
	if len(ordered) != 6 {
		t.Fatalf("ordered documents = %d, want 6", len(ordered))
	}
	if ordered[0].Stream != "workflow" || ordered[1].Stream != "approval" || ordered[2].Stream != "approval" || ordered[3].Stream != "environment" || ordered[4].Stream != "environment" || ordered[5].Stream != "github" {
		t.Fatalf("unexpected stream order: %#v", ordered)
	}
	if path, _ := importRelationPath(ordered[1].RelationKey); path != "approval/run.jsonl" {
		t.Fatalf("approval event journal was not ordered before request: %q", path)
	}
	if path, _ := importRelationPath(ordered[3].RelationKey); path != "environment/records/1.json" {
		t.Fatalf("environment record was not ordered before event: %q", path)
	}
}

// TestGitHubImportEventValidationRejectsPartialAuditEntries covers the closed
// audit-event union accepted by the PostgreSQL import materializer.
func TestGitHubImportEventValidationRejectsPartialAuditEntries(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	valid := githubpr.StoreEvent{
		Sequence: 1,
		Kind:     "delivery",
		At:       now,
		Delivery: &githubpr.DeliveryReceipt{Version: githubpr.SchemaVersion, DeliveryID: "delivery-1", PayloadSHA256: string(make([]byte, 64))},
	}
	if !validGitHubImportEvent(valid) {
		t.Fatal("valid delivery event rejected")
	}

	invalid := []githubpr.StoreEvent{
		{},
		{Sequence: 1, Kind: "empty", At: now},
		{Sequence: 1, Kind: "delivery", At: now, Delivery: &githubpr.DeliveryReceipt{Version: githubpr.SchemaVersion - 1, DeliveryID: "delivery-1", PayloadSHA256: string(make([]byte, 64))}},
		{Sequence: 1, Kind: "delivery", At: now, Delivery: &githubpr.DeliveryReceipt{Version: githubpr.SchemaVersion, DeliveryID: "", PayloadSHA256: string(make([]byte, 64))}},
		{Sequence: 1, Kind: "delivery", At: now, Delivery: &githubpr.DeliveryReceipt{Version: githubpr.SchemaVersion, DeliveryID: "delivery-1", PayloadSHA256: "short"}},
	}
	for index, event := range invalid {
		if validGitHubImportEvent(event) {
			t.Fatalf("invalid event %d accepted: %#v", index, event)
		}
	}
}

// TestApprovalImportDecoderIsStrict proves malformed or schema-extended JSONL
// cannot enter the approval projector during offline verification.
func TestApprovalImportDecoderIsStrict(t *testing.T) {
	t.Parallel()

	events, err := decodeApprovalImportEvents([]byte(" \n\t"))
	if err != nil || len(events) != 0 {
		t.Fatalf("empty journal decoded as events=%#v err=%v", events, err)
	}
	if _, err = decodeApprovalImportEvents([]byte(`{"unknown":true}`)); !errors.Is(err, controlplane.ErrIntegrity) {
		t.Fatalf("schema-extended event error = %v, want integrity", err)
	}
	if _, err = decodeApprovalImportEvents([]byte("{")); !errors.Is(err, controlplane.ErrIntegrity) {
		t.Fatalf("malformed event error = %v, want integrity", err)
	}
}

// TestImportNullableValuesPreserveAbsence verifies frozen lease metadata maps
// zero values to SQL NULL without altering present values.
func TestImportNullableValuesPreserveAbsence(t *testing.T) {
	t.Parallel()

	if nullableImportOwner("") != nil || nullableImportLease(time.Time{}) != nil {
		t.Fatal("zero owner or lease did not map to nil")
	}
	if owner := nullableImportOwner("worker-1"); owner != "worker-1" {
		t.Fatalf("owner = %#v", owner)
	}
	lease := time.Date(2026, 10, 28, 12, 30, 0, 123, time.FixedZone("test", 3600))
	if got := nullableImportLease(lease); got != lease.UTC() {
		t.Fatalf("lease = %#v, want %#v", got, lease.UTC())
	}
}

// hexBytes encodes arbitrary relation paths for deterministic import test documents.
func hexBytes(value string) string {
	const digits = "0123456789abcdef"
	encoded := make([]byte, len(value)*2)
	for index := range value {
		encoded[index*2] = digits[value[index]>>4]
		encoded[index*2+1] = digits[value[index]&0xf]
	}
	return string(encoded)
}
