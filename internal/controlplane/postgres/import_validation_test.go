package postgres

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/asierhr/featureProof/internal/githubpr"
)

// TestImportRelationValidationClosesEveryConsumerGrammar exercises the
// filesystem-independent allowlist used before imported rows reach SQL.
func TestImportRelationValidationClosesEveryConsumerGrammar(t *testing.T) {
	relation := func(path string) string { return "p" + hex.EncodeToString([]byte(path)) }
	valid := []controlplane.ImportDocument{
		{Stream: "capacity", RelationKey: "typed-capacity"},
		{Stream: "cleanup", RelationKey: "typed-cleanup"},
		{Stream: "correction", RelationKey: "typed-correction"},
		{Stream: "github", RelationKey: "typed-github"},
		{Stream: "packages", RelationKey: "typed-packages"},
		{Stream: "workflow", RelationKey: relation("workflows/run.state.json")},
		{Stream: "artifacts", RelationKey: relation("workflows/plans/run.plan.json")},
		{Stream: "executables", RelationKey: relation("workflows/private/run.private.json")},
		{Stream: "functional", RelationKey: relation("workflows/functional-flow/run.flow.json")},
		{Stream: "approval", RelationKey: relation("workflows/approval/run.jsonl")},
		{Stream: "approval", RelationKey: relation("workflows/approval/run.request.2.json")},
		{Stream: "environment", RelationKey: relation("environment/records/run/1.json")},
		{Stream: "environment", RelationKey: relation("environment/events/run/2.json")},
	}
	for _, document := range valid {
		if err := validateImportRelation(document); err != nil {
			t.Fatalf("valid relation %+v: %v", document, err)
		}
	}
	invalid := []controlplane.ImportDocument{
		{Stream: "capacity", RelationKey: "typed-cleanup"},
		{Stream: "workflow", RelationKey: "opaque"},
		{Stream: "workflow", RelationKey: relation("../run.state.json")},
		{Stream: "approval", RelationKey: relation("workflows/approval/run.request.0.json")},
		{Stream: "environment", RelationKey: relation("environment/records/run/01.json")},
		{Stream: "unknown", RelationKey: relation("workflows/run.state.json")},
	}
	for _, document := range invalid {
		if err := validateImportRelation(document); err == nil {
			t.Fatalf("invalid relation accepted: %+v", document)
		}
	}
}

// TestImportValidationHelpersRejectAliasesAndMalformedEvents covers canonical
// path, nullable lease and strict journal decoding edge cases.
func TestImportValidationHelpersRejectAliasesAndMalformedEvents(t *testing.T) {
	for _, relation := range []string{"", "p", "pzz", "p" + hex.EncodeToString([]byte("one")), "p" + hex.EncodeToString([]byte("a/../b")), "p" + hex.EncodeToString([]byte("a\\b"))} {
		if _, ok := importRelationPath(relation); ok {
			t.Fatalf("invalid relation decoded: %q", relation)
		}
	}
	if value, ok := importRelationPath("p" + hex.EncodeToString([]byte("safe/path"))); !ok || value != "safe/path" {
		t.Fatalf("decoded=%q ok=%v", value, ok)
	}
	for _, value := range []string{"", "0", "01", "+1", "1x"} {
		if validPositiveDecimal(value) {
			t.Fatalf("invalid decimal accepted: %q", value)
		}
	}
	if !validPositiveDecimal("123") || validImportFilePath("wrong/run.json", "expected", ".json") || validEnvironmentImportPath("environment/records/run/0.json") || validApprovalRequestRelation("workflows/approval/.request.1.json") {
		t.Fatal("canonical helper accepted or rejected the wrong shape")
	}
	if nullableImportOwner("") != nil || nullableImportOwner("owner") != "owner" || nullableImportLease(time.Time{}) != nil {
		t.Fatal("nullable import projection mismatch")
	}
	now := time.Unix(10, 0).UTC()
	if nullableImportLease(now) != now {
		t.Fatal("lease instant changed")
	}
	if events, err := decodeApprovalImportEvents([]byte("{}\n\n{}\n")); err != nil || len(events) != 2 {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
	if _, err := decodeApprovalImportEvents([]byte(`{"unknown":true}`)); err == nil {
		t.Fatal("unknown approval field accepted")
	}
	if validGitHubImportEvent(githubpr.StoreEvent{}) {
		t.Fatal("empty GitHub event accepted")
	}
	if !validGitHubImportEvent(githubpr.StoreEvent{Sequence: 1, Kind: "claim", At: now, Claims: &githubpr.ClaimBatch{}}) {
		t.Fatal("bounded claim event rejected")
	}
}
