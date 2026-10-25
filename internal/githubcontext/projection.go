package githubcontext

import (
	"encoding/json"
	"strconv"

	"github.com/asierhr/featureProof/internal/contextassembly"
)

// DiffProducer converts one verified GitHub context into the neutral SourceDiff contract.
func DiffProducer(value Context, authority contextassembly.AuthorityBinding) (*contextassembly.StaticProducer, error) {
	if VerifyContext(value) != nil || authority.RepositoryID != strconv.FormatInt(value.Authority.RepositoryID, 10) || authority.Revision != value.Authority.HeadOID {
		return nil, &Error{Category: CategoryUnauthorized, Stage: "projection_authority"}
	}
	items := make([]contextassembly.Item, 0, len(value.Files)+1)
	metadata, _ := json.Marshal(map[string]any{"value": map[string]any{"title": value.Title, "description": value.Description, "head_oid": value.Authority.HeadOID, "base_oid": value.Authority.BaseOID, "completeness": value.Completeness, "constraints": value.Constraints, "affected_contracts": value.Contracts, "omissions": value.Omissions}})
	items = append(items, projectionItem(authority, "github:pr:metadata", "github-metadata", metadata))
	for _, file := range value.Files {
		payload, _ := json.Marshal(map[string]any{"path": file.Path, "status": file.Status, "hunks": file.Hunks, "value": map[string]any{"previous_path": file.PreviousPath, "digest": file.Digest}})
		items = append(items, projectionItem(authority, "github:file:"+file.Digest[:16], file.ID, payload))
	}
	completeness := contextassembly.CompletenessComplete
	if value.Completeness == CompletenessPartial {
		completeness = contextassembly.CompletenessPartial
	}
	if value.Completeness == CompletenessInsufficient {
		completeness = contextassembly.CompletenessInsufficient
	}
	projection, err := contextassembly.NewProjection(contextassembly.SourceDiff, "githubcontext.acquirer", "v1-"+value.ObservationDigest[:12], completeness, items)
	if err != nil {
		return nil, err
	}
	return contextassembly.NeutralDiffProducer(projection), nil
}

// projectionItem assigns exact revision scope and untrusted-data trust to a GitHub unit.
func projectionItem(authority contextassembly.AuthorityBinding, reference, id string, payload []byte) contextassembly.Item {
	return contextassembly.Item{ID: id, Kind: contextassembly.SourceDiff, Trust: contextassembly.TrustUntrusted, Redaction: contextassembly.RedactionVerified, Authority: contextassembly.AuthorityObserved, SafeReference: reference, ProjectID: authority.ProjectID, RepositoryID: authority.RepositoryID, Revision: authority.Revision, Scope: "revision", State: "active", Payload: append(json.RawMessage(nil), payload...)}
}
