package agentplanning

import (
	"context"

	"github.com/asierhr/featureProof/internal/projectmemory"
)

// ProjectMemoryReader is the narrow planning-owned port for an already authorized structural memory query.
type ProjectMemoryReader interface {
	Query(context.Context, projectmemory.QueryRequest, projectmemory.QueryAuthority) (projectmemory.QueryResult, error)
	Bind(projectmemory.QueryResult, projectmemory.QueryAuthority) (projectmemory.MemoryBinding, error)
}

// ValidatedMemory is an explicit untrusted planning input plus its immutable historical binding.
type ValidatedMemory struct {
	Descriptors []projectmemory.AgentMemoryDescriptor
	Conflicts   []projectmemory.Conflict
	Binding     projectmemory.MemoryBinding
}

// LoadValidatedMemory obtains eligible records through the service and seals the exact selection without assembling model context.
func LoadValidatedMemory(ctx context.Context, reader ProjectMemoryReader, request projectmemory.QueryRequest, authority projectmemory.QueryAuthority) (ValidatedMemory, error) {
	result, err := reader.Query(ctx, request, authority)
	if err != nil {
		return ValidatedMemory{}, err
	}
	binding, err := reader.Bind(result, authority)
	if err != nil {
		return ValidatedMemory{}, err
	}
	descriptors := make([]projectmemory.AgentMemoryDescriptor, len(result.Records))
	for index, record := range result.Records {
		evidenceIDs := make([]string, len(record.Evidence))
		for evidenceIndex, evidence := range record.Evidence {
			evidenceIDs[evidenceIndex] = evidence.ID
		}
		descriptors[index] = projectmemory.AgentMemoryDescriptor{ID: record.ID, Version: record.Version, Class: record.Class, State: record.State, Authority: record.Authority, Sensitivity: record.Sensitivity, Subject: record.Subject, Predicate: record.Predicate, Value: record.Value, Provenance: record.Provenance, EvidenceIDs: evidenceIDs, RecordDigest: record.RecordDigest}
	}
	return ValidatedMemory{Descriptors: descriptors, Conflicts: append([]projectmemory.Conflict(nil), result.Conflicts...), Binding: binding}, nil
}
