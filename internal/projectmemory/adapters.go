package projectmemory

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
)

// HMACAuthority is a local composition adapter whose secret key remains outside persisted memory data.
type HMACAuthority struct {
	keyID string
	key   []byte
}

// NewHMACAuthority copies trusted key material and rejects weak or ambiguous configuration.
func NewHMACAuthority(keyID string, key []byte) (*HMACAuthority, error) {
	if !safeID.MatchString(keyID) || len(key) < 32 {
		return nil, fieldError("invalid_input", "integrity.configure", "key", nil)
	}
	return &HMACAuthority{keyID: keyID, key: append([]byte(nil), key...)}, nil
}

// Sign authenticates canonical bytes using the configured external trust anchor.
func (authority *HMACAuthority) Sign(ctx context.Context, payload []byte) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	mac := hmac.New(sha256.New, authority.key)
	_, _ = mac.Write(payload)
	return authority.keyID, hex.EncodeToString(mac.Sum(nil)), nil
}

// Verify accepts only the configured anchored key ID and constant-time matching authenticator.
func (authority *HMACAuthority) Verify(ctx context.Context, keyID string, payload []byte, authenticator string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if keyID != authority.keyID {
		return errors.New("unknown key")
	}
	provided, err := hex.DecodeString(authenticator)
	if err != nil {
		return errors.New("invalid authenticator")
	}
	mac := hmac.New(sha256.New, authority.key)
	_, _ = mac.Write(payload)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return errors.New("invalid authenticator")
	}
	return nil
}

// Close erases the in-memory key copy when composition releases the adapter.
func (authority *HMACAuthority) Close() {
	for index := range authority.key {
		authority.key[index] = 0
	}
	authority.key = nil
}

// AgentProposalDTO is the strict transport-only model output accepted by the proposal adapter.
type AgentProposalDTO struct {
	Version            string    `json:"version"`
	Candidate          Candidate `json:"candidate"`
	RequestedState     State     `json:"requested_state,omitempty"`
	RequestedAuthority Authority `json:"requested_authority,omitempty"`
}

// DecodeAgentProposal converts bounded model JSON only into an unprivileged proposal command.
func DecodeAgentProposal(raw []byte, principal, idempotencyKey string, expectedSequence uint64, limits Limits) (ProposalCommand, error) {
	if len(raw) == 0 || len(raw) > limits.MaxDocumentBytes {
		return ProposalCommand{}, fieldError("invalid_input", "agent.decode", "$", nil)
	}
	decoder := json.NewDecoder(bytesReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var dto AgentProposalDTO
	if decoder.Decode(&dto) != nil || dto.Version != ContractVersion || dto.RequestedState != "" || dto.RequestedAuthority != "" {
		return ProposalCommand{}, fieldError("invalid_input", "agent.decode", "$", nil)
	}
	if decoder.Decode(&struct{}{}) == nil {
		return ProposalCommand{}, fieldError("invalid_input", "agent.decode", "$", nil)
	}
	return ProposalCommand{Principal: principal, IdempotencyKey: idempotencyKey, ExpectedSequence: expectedSequence, Candidate: dto.Candidate}, nil
}

// byteReader is the minimal read/seek wrapper used by strict JSON decoders.
type byteReader struct {
	data   []byte
	offset int
}

// bytesReader copies untrusted transport bytes before decoding.
func bytesReader(data []byte) *byteReader { return &byteReader{data: append([]byte(nil), data...)} }

// Read implements streaming decode without retaining caller-owned bytes.
func (reader *byteReader) Read(target []byte) (int, error) {
	if reader.offset >= len(reader.data) {
		return 0, io.EOF
	}
	count := copy(target, reader.data[reader.offset:])
	reader.offset += count
	return count, nil
}

// SurfaceOperation identifies one versioned operation shared by CLI, API, MCP, and GitHub transports.
type SurfaceOperation string

const (
	// OperationPropose creates an untrusted proposal.
	OperationPropose SurfaceOperation = "memory.propose.v1"
	// OperationQuery returns an authorized structural projection.
	OperationQuery SurfaceOperation = "memory.query.v1"
	// OperationTransition applies an authorized lifecycle command.
	OperationTransition SurfaceOperation = "memory.transition.v1"
)

// SurfaceRequest is a transport-neutral union that never carries raw authority, paths, or integrity keys.
type SurfaceRequest struct {
	Operation  SurfaceOperation
	Principal  string
	Proposal   *ProposalCommand
	Query      *QueryRequest
	Transition *TransitionCommand
}

// SurfaceResponse is the allowlisted DTO emitted identically by every configured transport.
type SurfaceResponse struct {
	Review *ReviewView    `json:"review,omitempty"`
	Query  *SafeQueryView `json:"query,omitempty"`
	Audit  *AuditView     `json:"audit,omitempty"`
	Error  *Error         `json:"error,omitempty"`
}

// SafeQueryView carries descriptors and conflicts but no store handles or authenticators.
type SafeQueryView struct {
	Items       []AgentMemoryDescriptor `json:"items"`
	Conflicts   []Conflict              `json:"conflicts,omitempty"`
	QueryDigest string                  `json:"query_digest"`
	Sequence    uint64                  `json:"sequence"`
}

// Surface invokes the shared service after a concrete transport has mapped an authenticated principal.
type Surface struct {
	Service   *Service
	Authority QueryAuthorityResolver
	ReadOnly  bool
}

// QueryAuthorityResolver derives effective memory query authority from authenticated composition state.
type QueryAuthorityResolver interface {
	Resolve(context.Context, string, QueryRequest) (QueryAuthority, error)
}

// Invoke dispatches a closed operation grammar and normalizes errors into safe classifications.
func (surface Surface) Invoke(ctx context.Context, request SurfaceRequest) SurfaceResponse {
	if surface.Service == nil {
		return errorResponse(fieldError("unavailable", "surface", "service", nil))
	}
	switch request.Operation {
	case OperationPropose:
		if surface.ReadOnly || request.Proposal == nil {
			return errorResponse(fieldError("policy_denied", "surface", "operation", nil))
		}
		command := *request.Proposal
		command.Principal = request.Principal
		view, err := surface.Service.Propose(ctx, command)
		if err != nil {
			return errorResponse(err)
		}
		return SurfaceResponse{Review: &view}
	case OperationQuery:
		if request.Query == nil || surface.Authority == nil {
			return errorResponse(fieldError("invalid_input", "surface", "query", nil))
		}
		authority, err := surface.Authority.Resolve(ctx, request.Principal, *request.Query)
		if err != nil {
			return errorResponse(fieldError("policy_denied", "surface", "authority", err))
		}
		result, err := surface.Service.Query(ctx, *request.Query, authority)
		if err != nil {
			return errorResponse(err)
		}
		items := make([]AgentMemoryDescriptor, len(result.Records))
		for index, record := range result.Records {
			evidence := make([]string, len(record.Evidence))
			for evidenceIndex, item := range record.Evidence {
				evidence[evidenceIndex] = item.ID
			}
			items[index] = AgentMemoryDescriptor{ID: record.ID, Version: record.Version, Class: record.Class, State: record.State, Authority: record.Authority, Sensitivity: record.Sensitivity, Subject: record.Subject, Predicate: record.Predicate, Value: cloneRecord(record).Value, Provenance: record.Provenance, EvidenceIDs: evidence, RecordDigest: record.RecordDigest}
		}
		return SurfaceResponse{Query: &SafeQueryView{Items: items, Conflicts: result.Conflicts, QueryDigest: result.QueryDigest, Sequence: result.Head.Sequence}}
	case OperationTransition:
		if surface.ReadOnly || request.Transition == nil {
			return errorResponse(fieldError("policy_denied", "surface", "operation", nil))
		}
		command := *request.Transition
		command.Principal = request.Principal
		view, err := surface.Service.Transition(ctx, command)
		if err != nil {
			return errorResponse(err)
		}
		return SurfaceResponse{Audit: &view}
	default:
		return errorResponse(fieldError("invalid_input", "surface", "operation", nil))
	}
}

// errorResponse strips external causes from one safe transport response.
func errorResponse(err error) SurfaceResponse {
	var typed *Error
	if errors.As(err, &typed) {
		safe := &Error{Category: typed.Category, Stage: typed.Stage, Path: typed.Path}
		return SurfaceResponse{Error: safe}
	}
	return SurfaceResponse{Error: &Error{Category: "internal", Stage: "surface"}}
}

// WorkflowMemoryPort revalidates exact historical bindings before approval or execution.
type WorkflowMemoryPort interface {
	Revalidate(context.Context, MemoryBinding, QueryAuthority) (BindingStatus, error)
}

// WorkflowGuard is an optional additive gate that leaves memory-free runs compatible.
type WorkflowGuard struct{ Memory WorkflowMemoryPort }

// BeforePrivilegedStep blocks stale bound memory before approval or external execution.
func (guard WorkflowGuard) BeforePrivilegedStep(ctx context.Context, binding *MemoryBinding, authority QueryAuthority) error {
	if binding == nil {
		return nil
	}
	if guard.Memory == nil {
		return fieldError("unavailable", "workflow.memory", "port", nil)
	}
	status, err := guard.Memory.Revalidate(ctx, *binding, authority)
	if err != nil {
		return err
	}
	if !status.Valid {
		return fieldError("stale", "workflow.memory", "binding", nil)
	}
	return nil
}

// ObservationInput contains only verified terminal evidence metadata from a functional flow.
type ObservationInput struct {
	Principal, ID, IdempotencyKey     string
	ExpectedSequence, ExpectedVersion uint64
	Candidate                         Candidate
	FunctionalResultDigest            string
}

// ObservationAdapter proposes trusted observed memory without receiving or modifying the functional result.
type ObservationAdapter struct{ Service *Service }

// Observe delegates verified lineage to the shared service and returns only the separate memory projection.
func (adapter ObservationAdapter) Observe(ctx context.Context, input ObservationInput) (ReviewView, error) {
	if adapter.Service == nil || !digestPattern.MatchString(input.FunctionalResultDigest) {
		return ReviewView{}, fieldError("invalid_input", "observation", "result_digest", nil)
	}
	return adapter.Service.Observe(ctx, ObserveCommand{Principal: input.Principal, ID: input.ID, IdempotencyKey: input.IdempotencyKey, ExpectedSequence: input.ExpectedSequence, ExpectedVersion: input.ExpectedVersion, Candidate: input.Candidate, FunctionalResultDigest: input.FunctionalResultDigest})
}
