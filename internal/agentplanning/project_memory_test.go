package agentplanning

import (
	"context"
	"testing"

	"github.com/asierhr/featureProof/internal/projectmemory"
)

// planningMemoryReader returns one already verified selection and records both query and binding calls.
type planningMemoryReader struct{ queries, binds int }

// Query returns one safe record projection through the planning-owned port.
func (reader *planningMemoryReader) Query(context.Context, projectmemory.QueryRequest, projectmemory.QueryAuthority) (projectmemory.QueryResult, error) {
	reader.queries++
	return projectmemory.QueryResult{ProjectID: "project-1", Records: []projectmemory.Record{{ID: "memory-1", Version: 1, Class: projectmemory.ClassFact, State: projectmemory.StateApproved, Authority: projectmemory.AuthorityHuman, Sensitivity: projectmemory.SensitivityPublic, Subject: "api.pet", Predicate: "supports", Evidence: []projectmemory.EvidenceRef{{ID: "evidence-1"}}, RecordDigest: "digest"}}, Conflicts: []projectmemory.Conflict{{LogicalKey: "fact", IDs: []string{"memory-1", "memory-2"}}}}, nil
}

// Bind returns the exact content-free historical selection associated with Query.
func (reader *planningMemoryReader) Bind(projectmemory.QueryResult, projectmemory.QueryAuthority) (projectmemory.MemoryBinding, error) {
	reader.binds++
	return projectmemory.MemoryBinding{Version: projectmemory.ContractVersion, ProjectID: "project-1", Digest: "binding"}, nil
}

// TestLoadValidatedMemoryUsesServicePortsWithoutContextAssembly proves planning receives descriptors and binding as separate typed data.
func TestLoadValidatedMemoryUsesServicePortsWithoutContextAssembly(t *testing.T) {
	reader := &planningMemoryReader{}
	result, err := LoadValidatedMemory(context.Background(), reader, projectmemory.QueryRequest{ProjectID: "project-1"}, projectmemory.QueryAuthority{ProjectID: "project-1"})
	if err != nil || reader.queries != 1 || reader.binds != 1 || len(result.Descriptors) != 1 || result.Descriptors[0].EvidenceIDs[0] != "evidence-1" || len(result.Conflicts) != 1 || result.Binding.Digest != "binding" {
		t.Fatalf("validated memory=%#v query=%d bind=%d err=%v", result, reader.queries, reader.binds, err)
	}
}
