package externalagent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/localvalidation"
)

const (
	ScopeCreate           = "runs:create"
	ScopeRead             = "runs:read"
	ScopeCancel           = "runs:cancel"
	ScopeCorrectionCreate = "corrections:create"
	ScopeCorrectionRead   = "corrections:read"
	ScopeCorrectionCancel = "corrections:cancel"
)

// Principal is trusted host identity plus currently granted scopes.
type Principal struct {
	ID     string
	Scopes map[string]bool
}

// IdentitySource resolves authority from the host, never from request content.
type IdentitySource interface {
	Resolve(context.Context) (Principal, error)
}

// Selection is a trusted catalog entry consumed by the approved executor.
type Selection struct {
	Alias string
	Value any
}

// SelectionCatalog authorizes a local alias for the current principal.
type SelectionCatalog interface {
	Resolve(context.Context, Principal, string) (Selection, error)
}

// ExecutionResult identifies the terminal outcome and internal package location.
type ExecutionResult struct {
	Status     Outcome
	PackageDir string
}

// ApprovedExecutor traverses the existing binding, approval, policy, and budget gates.
type ApprovedExecutor interface {
	Execute(context.Context, Selection, string) (ExecutionResult, error)
}

// PackageReader verifies an evidence package before returning its semantic index.
type PackageReader interface {
	Read(context.Context, string) (localvalidation.PackageIndex, error)
}

// CorrectionUseCases is the transport-neutral correction boundary implemented by trusted composition.
type CorrectionUseCases interface {
	SubmitCorrection(context.Context, string, CorrectionSubmitInput) (CorrectionView, error)
	CorrectionStatus(context.Context, string, CorrectionHandleInput) (CorrectionView, error)
	CorrectionResult(context.Context, string, CorrectionHandleInput) (CorrectionResultView, error)
	CancelCorrection(context.Context, string, CorrectionCancelInput) (CorrectionView, error)
}

// Clock supplies auditable transition timestamps.
type Clock interface {
	Now() time.Time
}

// IDSource creates non-semantic, high-entropy handles and internal run IDs.
type IDSource interface {
	Next() (string, error)
}

// RunRecord is the durable private state behind one opaque handle.
type RunRecord struct {
	Handle         string    `json:"handle"`
	InternalID     string    `json:"internal_id"`
	PrincipalID    string    `json:"principal_id"`
	SelectionAlias string    `json:"selection_alias"`
	RequestHash    string    `json:"request_hash"`
	SubmitKeyHash  string    `json:"submit_key_hash"`
	Phase          Phase     `json:"phase"`
	Status         Outcome   `json:"status,omitempty"`
	PackageDir     string    `json:"package_dir,omitempty"`
	Owner          string    `json:"owner,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
	CancelKeyHash  string    `json:"cancel_key_hash,omitempty"`
	CancelHash     string    `json:"cancel_hash,omitempty"`
	Cause          string    `json:"cause,omitempty"`
}

// RunEvent is one immutable, ordered audit fact committed with its snapshot.
type RunEvent struct {
	Sequence  int       `json:"sequence"`
	Type      string    `json:"type"`
	RunHandle string    `json:"run_handle"`
	At        time.Time `json:"at"`
	Phase     Phase     `json:"phase"`
	Status    Outcome   `json:"status,omitempty"`
	Cause     string    `json:"cause,omitempty"`
}

// ReserveResult distinguishes a newly reserved run from an idempotent replay.
type ReserveResult struct {
	Record RunRecord
	New    bool
}

// Registry persists lifecycle, ownership, and idempotency independently of packages.
type Registry interface {
	Reserve(context.Context, RunRecord) (ReserveResult, error)
	Get(context.Context, string) (RunRecord, error)
	Transition(context.Context, string, Phase, Phase, Outcome, string, string, time.Time) (RunRecord, error)
	ReserveCancel(context.Context, string, string, string) (RunRecord, bool, error)
	Recover(context.Context, string, time.Time) error
	Events(context.Context, string) ([]RunEvent, error)
	MarkPublicationFailure(context.Context, string, time.Time) error
	ReleaseOwner(string)
}

// ServiceDependencies declares all trust, persistence, execution, and time boundaries.
type ServiceDependencies struct {
	Identity    IdentitySource
	Catalog     SelectionCatalog
	Registry    Registry
	Executor    ApprovedExecutor
	Packages    PackageReader
	Clock       Clock
	IDs         IDSource
	Owner       string
	Retention   time.Duration
	RunTimeout  time.Duration
	Corrections CorrectionUseCases
}

// Service is the single application use case shared by CLI and MCP.
type Service struct {
	deps       ServiceDependencies
	supervisor *Supervisor
}

// NewService validates closed dependencies and creates an owned worker supervisor.
func NewService(deps ServiceDependencies) (*Service, error) {
	if deps.Identity == nil || deps.Catalog == nil || deps.Registry == nil || deps.Executor == nil || deps.Packages == nil || deps.Clock == nil || deps.IDs == nil || deps.Owner == "" || deps.Retention <= 0 || deps.RunTimeout <= 0 {
		return nil, &AppError{Code: CodeInternal}
	}
	service := &Service{deps: deps}
	service.supervisor = NewSupervisor(service)
	if err := deps.Registry.Recover(context.Background(), deps.Owner, deps.Clock.Now()); err != nil {
		return nil, &AppError{Code: CodeInternal}
	}
	return service, nil
}

// Capabilities publishes only operations granted by current trusted scopes.
func (s *Service) Capabilities(ctx context.Context) (CapabilitiesResult, error) {
	principal, err := s.principal(ctx)
	if err != nil {
		return CapabilitiesResult{}, err
	}
	operations := operationsFor(principal, s.deps.Corrections != nil)
	return CapabilitiesResult{ContractVersion: ContractVersion, ResultSchemaVersion: 1, RetentionSeconds: int64(s.deps.Retention.Seconds()), Operations: operations}, nil
}

// Submit durably reserves idempotency before starting exactly one owned worker.
func (s *Service) Submit(ctx context.Context, input SubmitInput) (RunView, error) {
	if err := validateAlias(input.SelectionAlias); err != nil {
		return RunView{}, err
	}
	if err := validateKey(input.IdempotencyKey); err != nil {
		return RunView{}, err
	}
	principal, err := s.authorize(ctx, ScopeCreate)
	if err != nil {
		return RunView{}, err
	}
	selection, err := s.deps.Catalog.Resolve(ctx, principal, input.SelectionAlias)
	if err != nil {
		return RunView{}, normalizeAuthorization(err, "selection_alias")
	}
	handle, err := s.deps.IDs.Next()
	if err != nil || !validHandle(handle) {
		return RunView{}, &AppError{Code: CodeInternal}
	}
	internalID, err := s.deps.IDs.Next()
	if err != nil || !validHandle(internalID) {
		return RunView{}, &AppError{Code: CodeInternal}
	}
	requestHash := digest("submit\x00" + input.SelectionAlias)
	record := RunRecord{Handle: handle, InternalID: internalID, PrincipalID: principal.ID, SelectionAlias: input.SelectionAlias, RequestHash: requestHash, SubmitKeyHash: digest(principal.ID + "\x00submit\x00" + input.IdempotencyKey), Phase: PhaseQueued, Owner: s.deps.Owner, UpdatedAt: s.deps.Clock.Now()}
	reserved, err := s.deps.Registry.Reserve(ctx, record)
	if err != nil {
		return RunView{}, normalizeRegistry(err)
	}
	if reserved.Record.PrincipalID != principal.ID {
		return RunView{}, &AppError{Code: CodeNotFound}
	}
	if reserved.New {
		s.supervisor.Start(reserved.Record, selection)
	}
	return viewOf(reserved.Record), nil
}

// Status reauthorizes every handle lookup and reveals no foreign existence.
func (s *Service) Status(ctx context.Context, input HandleInput) (RunView, error) {
	record, err := s.ownedRecord(ctx, input.RunHandle, ScopeRead)
	if err != nil {
		return RunView{}, err
	}
	return viewOf(record), nil
}

// Result returns terminal data only after package verification and safe projection.
func (s *Service) Result(ctx context.Context, input HandleInput) (ResultView, error) {
	record, err := s.ownedRecord(ctx, input.RunHandle, ScopeRead)
	if err != nil {
		return ResultView{}, err
	}
	if record.Phase != PhaseTerminal {
		return ResultView{}, &AppError{Code: CodeInProgress, Retryable: true}
	}
	if record.Status == OutcomeCancelled || record.Status == OutcomeError {
		return ResultView{RunHandle: record.Handle, Phase: record.Phase, Status: record.Status, Constraints: []ConstraintView{}, Failures: []FailureView{}, Facts: []FactView{}, Evidence: []EvidenceView{}, Reproduction: []ReproductionView{}}, nil
	}
	if record.PackageDir == "" {
		return ResultView{}, &AppError{Code: CodeInternal}
	}
	index, err := s.deps.Packages.Read(ctx, record.PackageDir)
	if err != nil || index.RunID != record.InternalID || Outcome(index.Status) != record.Status {
		return ResultView{}, &AppError{Code: CodeInternal}
	}
	return projectResult(record, index)
}

// Cancel reserves cancellation idempotency, signals a live worker, and joins it.
func (s *Service) Cancel(ctx context.Context, input CancelInput) (RunView, error) {
	if err := validateKey(input.IdempotencyKey); err != nil {
		return RunView{}, err
	}
	record, err := s.ownedRecord(ctx, input.RunHandle, ScopeCancel)
	if err != nil {
		return RunView{}, err
	}
	requestHash := digest("cancel\x00" + input.RunHandle)
	record, _, err = s.deps.Registry.ReserveCancel(ctx, record.Handle, digest(record.PrincipalID+"\x00cancel\x00"+input.IdempotencyKey), requestHash)
	if err != nil {
		return RunView{}, normalizeRegistry(err)
	}
	if record.Phase != PhaseTerminal {
		if err := s.supervisor.Cancel(ctx, record.Handle); err != nil {
			if !errors.Is(err, ErrWorkerMissing) {
				return RunView{}, &AppError{Code: CodeInternal}
			}
			record, err = s.waitTerminal(ctx, record.Handle)
			if err != nil {
				return RunView{}, err
			}
		}
		record, err = s.deps.Registry.Get(ctx, record.Handle)
		if err != nil {
			return RunView{}, normalizeRegistry(err)
		}
	}
	return viewOf(record), nil
}

// SubmitCorrection reauthorizes correction creation and delegates only safe DTOs.
func (s *Service) SubmitCorrection(ctx context.Context, input CorrectionSubmitInput) (CorrectionView, error) {
	principal, err := s.authorize(ctx, ScopeCorrectionCreate)
	if err != nil {
		return CorrectionView{}, err
	}
	if s.deps.Corrections == nil {
		return CorrectionView{}, &AppError{Code: CodeInternal}
	}
	return s.deps.Corrections.SubmitCorrection(ctx, principal.ID, input)
}

// CorrectionStatus reauthorizes every correction lookup before delegation.
func (s *Service) CorrectionStatus(ctx context.Context, input CorrectionHandleInput) (CorrectionView, error) {
	principal, err := s.authorize(ctx, ScopeCorrectionRead)
	if err != nil {
		return CorrectionView{}, err
	}
	if s.deps.Corrections == nil {
		return CorrectionView{}, &AppError{Code: CodeInternal}
	}
	return s.deps.Corrections.CorrectionStatus(ctx, principal.ID, input)
}

// CorrectionResult reauthorizes result projection and never accepts package locations.
func (s *Service) CorrectionResult(ctx context.Context, input CorrectionHandleInput) (CorrectionResultView, error) {
	principal, err := s.authorize(ctx, ScopeCorrectionRead)
	if err != nil {
		return CorrectionResultView{}, err
	}
	if s.deps.Corrections == nil {
		return CorrectionResultView{}, &AppError{Code: CodeInternal}
	}
	return s.deps.Corrections.CorrectionResult(ctx, principal.ID, input)
}

// CancelCorrection reauthorizes mutable cancellation and preserves its idempotency key.
func (s *Service) CancelCorrection(ctx context.Context, input CorrectionCancelInput) (CorrectionView, error) {
	principal, err := s.authorize(ctx, ScopeCorrectionCancel)
	if err != nil {
		return CorrectionView{}, err
	}
	if s.deps.Corrections == nil {
		return CorrectionView{}, &AppError{Code: CodeInternal}
	}
	return s.deps.Corrections.CancelCorrection(ctx, principal.ID, input)
}

// waitTerminal joins cross-process cancellation through the durable registry signal.
func (s *Service) waitTerminal(ctx context.Context, handle string) (RunRecord, error) {
	for {
		record, err := s.deps.Registry.Get(ctx, handle)
		if err != nil {
			return RunRecord{}, &AppError{Code: CodeInternal}
		}
		if record.Phase == PhaseTerminal {
			return record, nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return RunRecord{}, &AppError{Code: CodeInternal, Retryable: true}
		case <-timer.C:
		}
	}
}

// Wait blocks until one owned worker exits and is used by the one-shot CLI lifecycle.
func (s *Service) Wait(ctx context.Context, handle string) error {
	return s.supervisor.Wait(ctx, handle)
}

// Close cancels and joins every owned worker before a transport process exits.
func (s *Service) Close(ctx context.Context) error { return s.supervisor.Close(ctx) }

// principal converts identity boundary failures to the closed authorization category.
func (s *Service) principal(ctx context.Context) (Principal, error) {
	principal, err := s.deps.Identity.Resolve(ctx)
	if err != nil || principal.ID == "" || principal.Scopes == nil {
		return Principal{}, &AppError{Code: CodeUnauthorized}
	}
	return principal, nil
}

// authorize resolves current scopes for each operation so revocation takes effect immediately.
func (s *Service) authorize(ctx context.Context, scope string) (Principal, error) {
	principal, err := s.principal(ctx)
	if err != nil || !principal.Scopes[scope] {
		return Principal{}, &AppError{Code: CodeUnauthorized}
	}
	return principal, nil
}

// ownedRecord maps malformed, missing, expired, and foreign handles to one public result.
func (s *Service) ownedRecord(ctx context.Context, handle, scope string) (RunRecord, error) {
	if !validHandle(handle) {
		return RunRecord{}, &AppError{Code: CodeNotFound}
	}
	principal, err := s.authorize(ctx, scope)
	if err != nil {
		return RunRecord{}, err
	}
	record, err := s.deps.Registry.Get(ctx, handle)
	if err != nil || record.PrincipalID != principal.ID || s.deps.Clock.Now().Sub(record.UpdatedAt) > s.deps.Retention {
		return RunRecord{}, &AppError{Code: CodeNotFound}
	}
	return record, nil
}

// operationsFor returns deterministic discovery filtered by current authority.
func operationsFor(principal Principal, hasCorrections ...bool) []Operation {
	correctionsEnabled := len(hasCorrections) > 0 && hasCorrections[0]
	operations := []Operation{{Name: "capabilities", InputSchema: objectSchema(nil, nil), OutputSchema: envelopeSchema(capabilitiesSchema())}}
	if principal.Scopes[ScopeCreate] {
		operations = append(operations, Operation{Name: "submit", InputSchema: objectSchema([]string{"selection_alias", "idempotency_key"}, map[string]string{"selection_alias": "string", "idempotency_key": "string"}), OutputSchema: envelopeSchema(runSchema())})
	}
	if principal.Scopes[ScopeRead] {
		operations = append(operations, Operation{Name: "status", InputSchema: handleSchema(), OutputSchema: envelopeSchema(runSchema())}, Operation{Name: "result", InputSchema: handleSchema(), OutputSchema: envelopeSchema(resultSchema())})
	}
	if principal.Scopes[ScopeCancel] {
		operations = append(operations, Operation{Name: "cancel", InputSchema: objectSchema([]string{"run_handle", "idempotency_key"}, map[string]string{"run_handle": "string", "idempotency_key": "string"}), OutputSchema: envelopeSchema(runSchema())})
	}
	if principal.Scopes[ScopeCorrectionCreate] && correctionsEnabled {
		operations = append(operations, Operation{Name: "correction_submit", InputSchema: objectSchema([]string{"parent_run_handle", "selection_alias", "idempotency_key"}, map[string]string{"parent_run_handle": "string", "selection_alias": "string", "idempotency_key": "string"}), OutputSchema: envelopeSchema(correctionSchema())})
	}
	if principal.Scopes[ScopeCorrectionRead] && correctionsEnabled {
		operations = append(operations, Operation{Name: "correction_status", InputSchema: correctionHandleSchema(), OutputSchema: envelopeSchema(correctionSchema())}, Operation{Name: "correction_result", InputSchema: correctionHandleSchema(), OutputSchema: envelopeSchema(correctionResultSchema())})
	}
	if principal.Scopes[ScopeCorrectionCancel] && correctionsEnabled {
		operations = append(operations, Operation{Name: "correction_cancel", InputSchema: objectSchema([]string{"correction_handle", "idempotency_key"}, map[string]string{"correction_handle": "string", "idempotency_key": "string"}), OutputSchema: envelopeSchema(correctionSchema())})
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i].Name < operations[j].Name })
	return operations
}

// correctionHandleSchema describes one opaque correction lookup.
func correctionHandleSchema() map[string]any {
	return objectSchema([]string{"correction_handle"}, map[string]string{"correction_handle": "string"})
}

// correctionSchema describes bounded lifecycle fields for correction operations.
func correctionSchema() map[string]any {
	return closedSchema([]string{"correction_handle", "parent_run_handle", "phase", "updated_at"}, map[string]any{"correction_handle": stringSchema(), "parent_run_handle": stringSchema(), "child_run_handle": stringSchema(), "phase": enumSchema("reserved", "preflight", "awaiting_approval", "failed_first", "regression", "finalizing", "terminal"), "status": enumSchema("passed", "failed", "inconclusive", "cancelled", "error"), "terminal_reason": enumSchema("limit_exhausted", "revision_not_deployed", "infrastructure_error", "integrity_failure", "cancelled"), "limit_dimension": enumSchema("attempts", "duration", "model_calls", "model_tokens"), "updated_at": map[string]any{"type": "string", "format": "date-time"}})
}

// correctionResultSchema closes comparison classes and prevents free-form adapter output.
func correctionResultSchema() map[string]any {
	scenario := closedSchema([]string{"constraint_id", "scenario", "batch_id", "step_id", "declaration_id"}, map[string]any{"constraint_id": stringSchema(), "scenario": stringSchema(), "batch_id": stringSchema(), "check_id": stringSchema(), "step_id": stringSchema(), "declaration_id": stringSchema()})
	entry := closedSchema([]string{"scenario", "class", "evidence_handles"}, map[string]any{"scenario": scenario, "class": enumSchema("corrected", "persistent", "new", "not_applicable", "inconclusive"), "evidence_handles": map[string]any{"type": "array", "items": stringSchema()}})
	properties := correctionSchema()["properties"].(map[string]any)
	for _, name := range []string{"corrected", "persistent", "new", "not_applicable", "inconclusive"} {
		properties[name] = map[string]any{"type": "array", "items": entry}
	}
	return closedSchema([]string{"correction_handle", "parent_run_handle", "phase", "updated_at", "corrected", "persistent", "new", "not_applicable", "inconclusive"}, properties)
}

// stringSchema provides the bounded scalar reused by correction schemas.
func stringSchema() map[string]any {
	return map[string]any{"type": "string", "minLength": 1, "maxLength": 256}
}

// enumSchema creates a closed JSON string vocabulary.
func enumSchema(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}

// objectSchema creates a closed JSON schema used consistently by both adapters.
func objectSchema(required []string, properties map[string]string) map[string]any {
	props := map[string]any{}
	for name, kind := range properties {
		props[name] = map[string]any{"type": kind}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": props}
}

// handleSchema describes one opaque run lookup.
func handleSchema() map[string]any {
	return objectSchema([]string{"run_handle"}, map[string]string{"run_handle": "string"})
}

// runSchema describes the common lifecycle response fields.
func runSchema() map[string]any {
	return closedSchema([]string{"run_handle", "phase", "updated_at"}, map[string]any{
		"run_handle": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{16,128}$"},
		"phase":      map[string]any{"type": "string", "enum": []string{string(PhaseQueued), string(PhaseRunning), string(PhaseFinalizing), string(PhaseTerminal)}},
		"status":     map[string]any{"type": "string", "enum": outcomeNames()},
		"updated_at": map[string]any{"type": "string", "format": "date-time"},
	})
}

// envelopeSchema describes the exact application wrapper emitted by both transports.
func envelopeSchema(data map[string]any) map[string]any {
	return map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object", "additionalProperties": false,
		"properties": map[string]any{"contract_version": map[string]any{"type": "integer", "const": ContractVersion}, "data": data, "error": appErrorSchema()},
		"required":   []string{"contract_version"}, "oneOf": []any{map[string]any{"required": []string{"data"}, "not": map[string]any{"required": []string{"error"}}}, map[string]any{"required": []string{"error"}, "not": map[string]any{"required": []string{"data"}}}},
	}
}

// appErrorSchema publishes the closed safe error vocabulary and field names.
func appErrorSchema() map[string]any {
	return closedSchema([]string{"code", "retryable"}, map[string]any{
		"code":      map[string]any{"type": "string", "enum": []string{string(CodeInvalidRequest), string(CodeUnauthorized), string(CodeNotFound), string(CodeConflict), string(CodeInProgress), string(CodeInternal)}},
		"field":     map[string]any{"type": "string", "enum": []string{"request", "selection_alias", "idempotency_key", "operation", "arguments"}},
		"retryable": map[string]any{"type": "boolean"},
	})
}

// capabilitiesSchema describes nested operations and their JSON Schema documents.
func capabilitiesSchema() map[string]any {
	schemaDocument := map[string]any{"type": "object"}
	operation := closedSchema([]string{"name", "input_schema", "output_schema"}, map[string]any{"name": map[string]any{"type": "string", "enum": []string{"capabilities", "submit", "status", "result", "cancel", "correction_submit", "correction_status", "correction_result", "correction_cancel"}}, "input_schema": schemaDocument, "output_schema": schemaDocument})
	return closedSchema([]string{"contract_version", "result_schema_version", "retention_seconds", "operations"}, map[string]any{"contract_version": map[string]any{"type": "integer", "const": ContractVersion}, "result_schema_version": map[string]any{"type": "integer", "const": 1}, "retention_seconds": map[string]any{"type": "integer", "minimum": 1}, "operations": map[string]any{"type": "array", "items": operation, "minItems": 1, "uniqueItems": true}})
}

// resultSchema describes every nested safe projection field and terminal enum.
func resultSchema() map[string]any {
	id := map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]+$"}
	evidenceHandle := map[string]any{"type": "string", "pattern": "^ev_[0-9a-f]{32}$"}
	evidenceHandles := map[string]any{"type": "array", "items": evidenceHandle}
	safeValue := closedSchema([]string{"state"}, map[string]any{"state": map[string]any{"type": "string", "enum": []string{"present", "absent", "redacted", "truncated"}}, "value": map[string]any{"type": []string{"null", "boolean", "number"}}})
	constraint := closedSchema([]string{"constraint_id", "status", "evidence_handles"}, map[string]any{"constraint_id": id, "status": map[string]any{"type": "string", "enum": []string{string(OutcomePassed), string(OutcomeFailed), string(OutcomeInconclusive)}}, "evidence_handles": evidenceHandles})
	failure := closedSchema([]string{"constraint_id", "step_id", "declaration_id", "operator", "status", "expected", "observed", "evidence_handles"}, map[string]any{"constraint_id": id, "step_id": id, "declaration_id": id, "operator": map[string]any{"type": "string", "enum": []string{"eq", "gt", "gte", "lt", "lte", "exists", "absent", "all", "any"}}, "status": map[string]any{"type": "string", "enum": []string{"passed", "failed", "inconclusive"}}, "expected": safeValue, "observed": safeValue, "evidence_handles": evidenceHandles})
	fact := closedSchema([]string{"kind", "constraint_id", "step_id", "status", "evidence_handles"}, map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"step", "assertion"}}, "constraint_id": id, "step_id": id, "declaration_id": id, "status": map[string]any{"type": "string", "enum": []string{"passed", "failed", "inconclusive", "blocked", "error"}}, "evidence_handles": evidenceHandles})
	evidence := closedSchema([]string{"evidence_handle"}, map[string]any{"evidence_handle": evidenceHandle})
	digestSchema := map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}
	reproduction := closedSchema([]string{"constraint_id", "commit_oid", "repository_id", "environment_id", "constraints_sha256", "contract_sha256", "configuration_sha256", "plan_sha256", "credential_required"}, map[string]any{"constraint_id": id, "commit_oid": map[string]any{"type": "string", "pattern": "^[0-9a-f]{40,64}$"}, "repository_id": id, "environment_id": id, "constraints_sha256": digestSchema, "contract_sha256": digestSchema, "configuration_sha256": digestSchema, "plan_sha256": map[string]any{"type": "array", "items": digestSchema}, "credential_required": map[string]any{"type": "boolean"}})
	return closedSchema([]string{"run_handle", "phase", "status", "constraints", "failures", "facts", "evidence", "reproduction"}, map[string]any{"run_handle": map[string]any{"type": "string"}, "phase": map[string]any{"type": "string", "const": string(PhaseTerminal)}, "status": map[string]any{"type": "string", "enum": outcomeNames()}, "constraints": arraySchema(constraint), "failures": arraySchema(failure), "facts": arraySchema(fact), "evidence": arraySchema(evidence), "reproduction": arraySchema(reproduction)})
}

// closedSchema builds a JSON object schema that prohibits undeclared fields.
func closedSchema(required []string, properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": properties}
}

// arraySchema builds a bounded-shape array schema for result collections.
func arraySchema(item map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": item}
}

// outcomeNames returns every terminal status advertised by v1.
func outcomeNames() []string {
	return []string{string(OutcomePassed), string(OutcomeFailed), string(OutcomeInconclusive), string(OutcomeCancelled), string(OutcomeError)}
}

// validateAlias enforces an identifier rather than accepting paths or URLs.
func validateAlias(value string) error {
	if len(value) < 1 || len(value) > 64 {
		return &AppError{Code: CodeInvalidRequest, Field: "selection_alias"}
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return &AppError{Code: CodeInvalidRequest, Field: "selection_alias"}
		}
	}
	return nil
}

// validateKey bounds idempotency material before it reaches durable hashing.
func validateKey(value string) error {
	if len(value) < 8 || len(value) > 128 || strings.TrimSpace(value) != value {
		return &AppError{Code: CodeInvalidRequest, Field: "idempotency_key"}
	}
	return nil
}

// validHandle accepts only the opaque format generated by trusted ID sources.
func validHandle(value string) bool {
	if len(value) < 16 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// digest protects idempotency keys and canonical request identities at rest.
func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// viewOf strips every internal execution and persistence field.
func viewOf(record RunRecord) RunView {
	return RunView{RunHandle: record.Handle, Phase: record.Phase, Status: record.Status, UpdatedAt: record.UpdatedAt}
}

// normalizeAuthorization prevents catalog details from crossing the boundary.
func normalizeAuthorization(err error, field string) error {
	var app *AppError
	if errors.As(err, &app) && app.Code == CodeUnauthorized {
		return app
	}
	return &AppError{Code: CodeUnauthorized, Field: field}
}

// normalizeRegistry maps private storage conditions into the closed public vocabulary.
func normalizeRegistry(err error) error {
	if errors.Is(err, ErrConflict) {
		return &AppError{Code: CodeConflict}
	}
	if errors.Is(err, ErrNotFound) {
		return &AppError{Code: CodeNotFound}
	}
	return &AppError{Code: CodeInternal}
}

// RandomIDs returns cryptographically random opaque identifiers.
type RandomIDs struct{}

// Next generates 128 bits without embedding any host or run metadata.
func (RandomIDs) Next() (string, error) {
	var value [16]byte
	_, err := rand.Read(value[:])
	return hex.EncodeToString(value[:]), err
}

// WallClock supplies UTC timestamps at production boundaries.
type WallClock struct{}

// Now returns current UTC time.
func (WallClock) Now() time.Time { return time.Now().UTC() }
