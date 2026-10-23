package projectmemory

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

// Clock supplies trusted time to validity and expiry decisions.
type Clock interface{ Now() time.Time }

// Waiter owns finite retry backoff and must return promptly when context ends.
type Waiter interface {
	Wait(context.Context, time.Duration) error
}

// EvidenceAuthority verifies lineage without exposing evidence bodies.
type EvidenceAuthority interface {
	Verify(context.Context, string, Scope, EvidenceRef) error
}

// PrincipalDecision is a trusted authorization result derived outside request payloads.
type PrincipalDecision struct {
	Principal           string
	Allowed             bool
	MaxScope            ScopeKind
	MaxSensitivity      Sensitivity
	PolicyDigest        string
	Actor               Actor
	RepositoryID        string
	CommitOID           string
	ConfigurationDigest string
	Producer            string
	ProducerVersion     string
	Reference           string
}

// PrincipalAuthority maps authenticated principals to exact memory actions.
type PrincipalAuthority interface {
	Authorize(context.Context, string, string, string) (PrincipalDecision, error)
}

// Head identifies one authenticated project stream position.
type Head struct {
	Sequence uint64 `json:"sequence"`
	Digest   string `json:"digest"`
}

// Mutation requests one CAS-protected immutable version append.
type Mutation struct {
	ProjectID        string
	ExpectedSequence uint64
	ExpectedVersion  uint64
	IdempotencyKey   string
	Transition       string
	Record           Record
	PolicyDigest     string
	RequestDigest    string
}

// CommitResult returns a detached record and its resulting durable stream head.
type CommitResult struct {
	Record      Record
	Head        Head
	Redelivered bool
}

// StructuralFilter limits store materialization to one project before service filtering.
type StructuralFilter struct {
	Classes     []Class
	States      []State
	Authorities []Authority
}

// VerifiedPage carries only records verified by a MemoryStore implementation.
type VerifiedPage struct {
	Records []Record
	Head    Head
}

// MemoryStore is the consumer-owned persistence port for authenticated memory streams.
type MemoryStore interface {
	Commit(context.Context, Mutation) (CommitResult, error)
	Current(context.Context, string, string) (Record, error)
	Version(context.Context, string, string, uint64) (Record, error)
	Scan(context.Context, string, StructuralFilter, int) (VerifiedPage, error)
	Head(context.Context, string) (Head, error)
	Recover(context.Context, string) error
}

// OperationRecord is payload-free telemetry for one completed memory boundary.
type OperationRecord struct {
	ProjectID, MemoryID, Transition, Actor, Outcome, Category string
	Version                                                   uint64
	Duration                                                  time.Duration
}

// Observer synchronously accepts only safe operation metadata.
type Observer interface {
	Observe(context.Context, OperationRecord) error
}

// SecretRules configures the fail-closed pre-persistence detector from trusted composition.
type SecretRules struct {
	Names    []string
	Values   []string
	Canaries []string
	MaxWork  int
}

// SecretGate rejects prohibited content before store or index boundaries.
type SecretGate struct{ Rules SecretRules }

// Check recursively scans names and values without returning rejected content.
func (gate SecretGate) Check(ctx context.Context, value any) error {
	work := 0
	var inspect func(string, any) error
	inspect = func(path string, current any) error {
		if err := ctx.Err(); err != nil {
			return fieldError("cancelled", "secret_gate", path, err)
		}
		work++
		if gate.Rules.MaxWork > 0 && work > gate.Rules.MaxWork {
			return fieldError("limit", "secret_gate", path, nil)
		}
		sensitive := func(text string) bool {
			lower := strings.ToLower(text)
			for _, name := range gate.Rules.Names {
				if strings.Contains(lower, strings.ToLower(name)) {
					return true
				}
			}
			for _, prohibited := range append(append([]string(nil), gate.Rules.Values...), gate.Rules.Canaries...) {
				if prohibited != "" && strings.Contains(text, prohibited) {
					return true
				}
			}
			return strings.Contains(lower, "private key") || strings.Contains(lower, "chain of thought") || strings.Contains(lower, "authorization:") || strings.Contains(lower, "cookie:")
		}
		switch item := current.(type) {
		case string:
			if sensitive(item) {
				return fieldError("secret_rejected", "secret_gate", path, nil)
			}
		case map[string]any:
			for key, child := range item {
				if sensitive(key) {
					return fieldError("secret_rejected", "secret_gate", path, nil)
				}
				if err := inspect(path+"."+key, child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range item {
				if err := inspect(path+"[]", child); err != nil {
					return err
				}
			}
		default:
			raw, err := json.Marshal(item)
			if err != nil {
				return fieldError("secret_rejected", "secret_gate", path, err)
			}
			var generic any
			if err = json.Unmarshal(raw, &generic); err != nil {
				return fieldError("secret_rejected", "secret_gate", path, err)
			}
			if _, scalar := generic.(map[string]any); scalar {
				return inspect(path, generic)
			}
			if _, array := generic.([]any); array {
				return inspect(path, generic)
			}
		}
		return nil
	}
	return inspect("$", value)
}

// ProposalCommand is the only data shape an agent adapter may submit.
type ProposalCommand struct {
	Principal, IdempotencyKey string
	ExpectedSequence          uint64
	Candidate                 Candidate
}

// ObserveCommand creates an evidence-backed deterministic observation.
type ObserveCommand struct {
	Principal, ID, IdempotencyKey     string
	ExpectedSequence, ExpectedVersion uint64
	Candidate                         Candidate
	FunctionalResultDigest            string
}

// DecisionID identifies one authenticated human decision and cannot be synthesized by observers or reconcilers.
type DecisionID string

// TransitionCommand requests an authorized human lifecycle transition.
type TransitionCommand struct {
	Principal, ProjectID, MemoryID, IdempotencyKey string
	DecisionID                                     DecisionID
	ExpectedSequence, ExpectedVersion              uint64
	Target                                         State
	Replacement                                    *ReplacementRef
}

// ReviewView is the bounded safe record projection shared by review surfaces.
type ReviewView struct {
	ContractVersion, ID, ProjectID string
	Version                        uint64
	Scope                          Scope
	Class                          Class
	State                          State
	Subject, Predicate             string
	Value                          TypedValue
	Authority                      Authority
	Confidence                     Confidence
	Sensitivity                    Sensitivity
	Provenance                     Provenance
	Evidence                       []EvidenceRef
	RecordDigest                   string
}

// AgentMemoryDescriptor labels safe untrusted memory content for downstream typed consumers.
type AgentMemoryDescriptor struct {
	ID                 string
	Version            uint64
	Class              Class
	State              State
	Authority          Authority
	Sensitivity        Sensitivity
	Subject, Predicate string
	Value              TypedValue
	Provenance         Provenance
	EvidenceIDs        []string
	RecordDigest       string
}

// AuditView exposes lifecycle identity and digests without memory content or authenticators.
type AuditView struct {
	ID, ProjectID            string
	Version                  uint64
	State                    State
	Authority                Authority
	RecordDigest, DecisionID string
}

// QueryRequest contains caller-selected restrictions that can only narrow trusted authority.
type QueryRequest struct {
	ProjectID       string
	Scope           Scope
	Classes         []Class
	States          []State
	Authorities     []Authority
	MaxSensitivity  Sensitivity
	Limit, MaxBytes int
	IncludeProposed bool
}

// QueryAuthority contains trusted effective eligibility and revision policy.
type QueryAuthority struct {
	ProjectID                   string
	Scope                       Scope
	Classes                     []Class
	States                      []State
	Authorities                 []Authority
	MaxSensitivity              Sensitivity
	ConfigurationDigest         string
	AsOf                        time.Time
	MaxItems, MaxBytes, MaxWork int
	Review                      bool
}

// Conflict reports all contradictory memory IDs without selecting a winner.
type Conflict struct {
	LogicalKey string   `json:"logical_key"`
	IDs        []string `json:"ids"`
}

// QueryResult preserves the verified head and detached stable selection.
type QueryResult struct {
	ProjectID     string
	Records       []Record
	Conflicts     []Conflict
	Head          Head
	QueryDigest   string
	requestSeal   QueryRequest
	authoritySeal QueryAuthority
	selectionSeal string
}

// BindingEntry seals one exact record version used by a run.
type BindingEntry struct {
	ID      string `json:"id"`
	Version uint64 `json:"version"`
	Digest  string `json:"digest"`
}

// MemoryBinding is an immutable content-free historical selection.
type MemoryBinding struct {
	Version             string         `json:"version"`
	ProjectID           string         `json:"project_id"`
	RepositoryID        string         `json:"repository_id"`
	RevisionID          string         `json:"revision_id,omitempty"`
	CommitOID           string         `json:"commit_oid,omitempty"`
	ConfigurationDigest string         `json:"configuration_digest,omitempty"`
	QueryDigest         string         `json:"query_digest"`
	StoreHead           Head           `json:"store_head"`
	Entries             []BindingEntry `json:"entries"`
	Query               QueryRequest   `json:"query"`
	AuthorityDigest     string         `json:"authority_digest"`
	Digest              string         `json:"digest"`
}

// BindingStatus reports whether an exact historical selection remains eligible for new work.
type BindingStatus struct {
	Valid    bool
	Category string
}

// Service coordinates authority, validation, secret detection, evidence verification, and storage.
type Service struct {
	Store                MemoryStore
	Evidence             EvidenceAuthority
	HumanPrincipals      PrincipalAuthority
	ObserverPrincipals   PrincipalAuthority
	ReconcilerPrincipals PrincipalAuthority
	Clock                Clock
	Gate                 SecretGate
	Limits               Limits
	Waiter               Waiter
	Observer             Observer
	MaxRetries           int
	RetryDelay           time.Duration
}

// Propose forces agent-owned lifecycle fields and commits only after all guards pass.
func (service Service) Propose(ctx context.Context, command ProposalCommand) (ReviewView, error) {
	decision, err := service.authorize(ctx, service.HumanPrincipals, command.Principal, command.Candidate.ProjectID, "propose", "human")
	if err != nil {
		return ReviewView{}, err
	}
	if scopeRank(command.Candidate.Scope.Kind) > scopeRank(decision.MaxScope) || sensitivityRank(command.Candidate.Sensitivity) > sensitivityRank(decision.MaxSensitivity) {
		return ReviewView{}, fieldError("policy_denied", "propose", "scope", nil)
	}
	requestDigest := digestCanonical(struct {
		Project, Idempotency string
		Expected             uint64
		Candidate            Candidate
	}{command.Candidate.ProjectID, command.IdempotencyKey, command.ExpectedSequence, command.Candidate})
	idSeed := requestDigest
	command.Candidate, err = service.serverCandidate(command.Candidate, decision, "agent_proposal", "")
	if err != nil {
		return ReviewView{}, err
	}
	if err = service.Gate.Check(ctx, command.Candidate); err != nil {
		return ReviewView{}, err
	}
	id := "memory-" + idSeed[:24]
	record, err := NormalizeRecord(command.Candidate, id, 1, StateProposed, AuthorityAgentProposal, service.Limits)
	if err != nil {
		return ReviewView{}, err
	}
	result, err := service.commit(ctx, Mutation{ProjectID: record.ProjectID, ExpectedSequence: command.ExpectedSequence, IdempotencyKey: command.IdempotencyKey, Transition: "propose", Record: record, PolicyDigest: decision.PolicyDigest, RequestDigest: requestDigest})
	if err != nil {
		return ReviewView{}, err
	}
	service.observe(ctx, OperationRecord{ProjectID: record.ProjectID, MemoryID: record.ID, Version: result.Record.Version, Transition: "propose", Actor: decision.Principal, Outcome: "committed"})
	return reviewView(result.Record), nil
}

// Observe verifies every evidence reference before creating an observed record.
func (service Service) Observe(ctx context.Context, command ObserveCommand) (ReviewView, error) {
	decision, err := service.authorize(ctx, service.ObserverPrincipals, command.Principal, command.Candidate.ProjectID, "observe", "observer")
	if err != nil {
		return ReviewView{}, err
	}
	if scopeRank(command.Candidate.Scope.Kind) > scopeRank(decision.MaxScope) || sensitivityRank(command.Candidate.Sensitivity) > sensitivityRank(decision.MaxSensitivity) || !digestPattern.MatchString(command.FunctionalResultDigest) {
		return ReviewView{}, fieldError("policy_denied", "observe", "authority", nil)
	}
	requestDigest := digestCanonical(struct {
		Project, ID, Idempotency, Functional string
		ExpectedSequence, ExpectedVersion    uint64
		Candidate                            Candidate
	}{command.Candidate.ProjectID, command.ID, command.IdempotencyKey, command.FunctionalResultDigest, command.ExpectedSequence, command.ExpectedVersion, command.Candidate})
	command.Candidate, err = service.serverCandidate(command.Candidate, decision, "functional_result", command.FunctionalResultDigest)
	if err != nil {
		return ReviewView{}, err
	}
	if err = service.Gate.Check(ctx, command.Candidate); err != nil {
		return ReviewView{}, err
	}
	for _, evidence := range command.Candidate.Evidence {
		if err = service.Evidence.Verify(ctx, command.Candidate.ProjectID, command.Candidate.Scope, evidence); err != nil {
			return ReviewView{}, fieldError("policy_denied", "evidence", "evidence", err)
		}
	}
	version := command.ExpectedVersion + 1
	if command.ExpectedVersion == 0 {
		version = 1
	}
	record, err := NormalizeRecord(command.Candidate, command.ID, version, StateObserved, AuthorityObservation, service.Limits)
	if err != nil {
		return ReviewView{}, err
	}
	result, err := service.commit(ctx, Mutation{ProjectID: record.ProjectID, ExpectedSequence: command.ExpectedSequence, ExpectedVersion: command.ExpectedVersion, IdempotencyKey: command.IdempotencyKey, Transition: "observe", Record: record, PolicyDigest: decision.PolicyDigest, RequestDigest: requestDigest})
	if err != nil {
		return ReviewView{}, err
	}
	service.observe(ctx, OperationRecord{ProjectID: record.ProjectID, MemoryID: record.ID, Version: result.Record.Version, Transition: "observe", Actor: decision.Principal, Outcome: "committed"})
	return reviewView(result.Record), nil
}

// Transition appends an authorized state version and forbids transitions from terminal records.
func (service Service) Transition(ctx context.Context, command TransitionCommand) (AuditView, error) {
	if command.Target == StateObserved || command.Target == StateExpired || !safeID.MatchString(string(command.DecisionID)) {
		return AuditView{}, fieldError("policy_denied", "transition", "authority", nil)
	}
	action := string(command.Target)
	requestDigest := digestCanonical(struct {
		Project, Memory, Idempotency, Decision, Target string
		ExpectedSequence, ExpectedVersion              uint64
		Replacement                                    *ReplacementRef
	}{command.ProjectID, command.MemoryID, command.IdempotencyKey, string(command.DecisionID), action, command.ExpectedSequence, command.ExpectedVersion, command.Replacement})
	decision, err := service.authorize(ctx, service.HumanPrincipals, command.Principal, command.ProjectID, action, "human")
	if err != nil {
		return AuditView{}, err
	}
	prior, err := service.Store.Current(ctx, command.ProjectID, command.MemoryID)
	if err != nil {
		return AuditView{}, err
	}
	if prior.Version == command.ExpectedVersion+1 && prior.State == command.Target && prior.Provenance.DecisionID == string(command.DecisionID) {
		result, replayErr := service.commit(ctx, Mutation{ProjectID: command.ProjectID, ExpectedSequence: command.ExpectedSequence, ExpectedVersion: command.ExpectedVersion, IdempotencyKey: command.IdempotencyKey, Transition: action, Record: prior, PolicyDigest: decision.PolicyDigest, RequestDigest: requestDigest})
		if replayErr != nil {
			return AuditView{}, replayErr
		}
		return auditView(result.Record), nil
	}
	if prior.Version != command.ExpectedVersion || !transitionAllowed(prior.State, command.Target) {
		return AuditView{}, fieldError("conflict", "transition", "state", nil)
	}
	if scopeRank(prior.Scope.Kind) > scopeRank(decision.MaxScope) || sensitivityRank(prior.Sensitivity) > sensitivityRank(decision.MaxSensitivity) || prior.Scope.Kind != ScopeRun && prior.Scope.RepositoryID != decision.RepositoryID {
		return AuditView{}, fieldError("policy_denied", "transition", "authority", nil)
	}
	if command.Target == StateSuperseded {
		if command.Replacement == nil {
			return AuditView{}, fieldError("invalid_input", "transition", "replacement", nil)
		}
		replacement, loadErr := service.Store.Version(ctx, command.ProjectID, command.Replacement.ID, command.Replacement.Version)
		if loadErr != nil || replacement.RecordDigest != command.Replacement.Digest || replacement.LogicalKey() != prior.LogicalKey() {
			return AuditView{}, fieldError("conflict", "transition", "replacement", loadErr)
		}
	}
	next := cloneRecord(prior)
	next.Version++
	next.State = command.Target
	next.Authority = AuthorityHuman
	next.Provenance.Actor = decision.Actor
	next.Provenance.Source = "authorized_transition"
	next.Provenance.Reference = decision.Reference
	next.Provenance.Producer = decision.Producer
	next.Provenance.ProducerVersion = decision.ProducerVersion
	next.Provenance.ObservedAt = service.Clock.Now().UTC()
	next.Provenance.RepositoryID = decision.RepositoryID
	next.Provenance.CommitOID = decision.CommitOID
	next.Provenance.ConfigurationDigest = decision.ConfigurationDigest
	next.Provenance.DecisionID = string(command.DecisionID)
	next.Provenance.PolicyDigest = decision.PolicyDigest
	next.Replacement = command.Replacement
	next.ProvenanceDigest = digestCanonical(struct {
		Provenance Provenance
		Evidence   []EvidenceRef
	}{next.Provenance, next.Evidence})
	next.RecordDigest = recordDigest(next)
	if err = service.Gate.Check(ctx, next); err != nil {
		return AuditView{}, err
	}
	result, err := service.commit(ctx, Mutation{ProjectID: command.ProjectID, ExpectedSequence: command.ExpectedSequence, ExpectedVersion: command.ExpectedVersion, IdempotencyKey: command.IdempotencyKey, Transition: action, Record: next, PolicyDigest: decision.PolicyDigest, RequestDigest: requestDigest})
	if err != nil {
		return AuditView{}, err
	}
	service.observe(ctx, OperationRecord{ProjectID: command.ProjectID, MemoryID: command.MemoryID, Version: result.Record.Version, Transition: action, Actor: decision.Principal, Outcome: "committed"})
	return auditView(result.Record), nil
}

// ReconcileExpiry explicitly persists terminal expiry for currently elapsed records without background work.
func (service Service) ReconcileExpiry(ctx context.Context, principal, project string, expectedSequence uint64) error {
	decision, err := service.authorize(ctx, service.ReconcilerPrincipals, principal, project, "expire", "reconciler")
	if err != nil {
		return err
	}
	page, err := service.Store.Scan(ctx, project, StructuralFilter{}, service.Limits.MaxRecords)
	if err != nil {
		return err
	}
	now := service.Clock.Now().UTC()
	sequence := expectedSequence
	for _, record := range page.Records {
		if record.ValidUntil != nil && !now.Before(*record.ValidUntil) && !terminal(record.State) {
			next := cloneRecord(record)
			next.Version++
			next.State = StateExpired
			next.Authority = AuthorityConfiguration
			next.Provenance.Actor = decision.Actor
			next.Provenance.Source = "trusted_expiry_reconciler"
			next.Provenance.Reference = decision.Reference
			next.Provenance.Producer = decision.Producer
			next.Provenance.ProducerVersion = decision.ProducerVersion
			next.Provenance.ObservedAt = now
			next.Provenance.DecisionID = ""
			next.Provenance.PolicyDigest = decision.PolicyDigest
			next.ProvenanceDigest = digestCanonical(struct {
				Provenance Provenance
				Evidence   []EvidenceRef
			}{next.Provenance, next.Evidence})
			next.RecordDigest = recordDigest(next)
			if err = service.Gate.Check(ctx, next); err == nil {
				idempotencyKey := "expiry-" + record.ID + "-" + record.ValidUntil.UTC().Format(time.RFC3339Nano)
				requestDigest := digestCanonical(struct {
					Project, ID, Idempotency  string
					ExpectedSequence, Version uint64
				}{project, record.ID, idempotencyKey, sequence, record.Version})
				_, err = service.commit(ctx, Mutation{ProjectID: project, ExpectedSequence: sequence, ExpectedVersion: record.Version, IdempotencyKey: idempotencyKey, Transition: "expire", Record: next, PolicyDigest: decision.PolicyDigest, RequestDigest: requestDigest})
			}
			if err != nil {
				return err
			}
			sequence++
		}
	}
	return nil
}

// Query applies trusted structural authority before ordering, conflicts, and complete-record truncation.
func (service Service) Query(ctx context.Context, request QueryRequest, authority QueryAuthority) (QueryResult, error) {
	if err := ctx.Err(); err != nil {
		return QueryResult{}, fieldError("cancelled", "query", "", err)
	}
	if request.ProjectID != authority.ProjectID || request.Scope != authority.Scope || request.Limit < 0 || request.MaxBytes < 0 {
		return QueryResult{}, fieldError("policy_denied", "query", "scope", nil)
	}
	page, err := service.Store.Scan(ctx, request.ProjectID, StructuralFilter{}, service.Limits.MaxRecords)
	if err != nil {
		return QueryResult{}, err
	}
	eligibleRecords := make([]Record, 0)
	work := 0
	for _, record := range page.Records {
		work++
		if authority.MaxWork > 0 && work > authority.MaxWork {
			return QueryResult{}, fieldError("limit", "query", "work", nil)
		}
		if !eligible(record, request, authority) {
			continue
		}
		if err = service.verifyRecordEvidence(ctx, record); err != nil {
			continue
		}
		eligibleRecords = append(eligibleRecords, cloneRecord(record))
	}
	sort.Slice(eligibleRecords, func(i, j int) bool {
		left, right := eligibleRecords[i], eligibleRecords[j]
		a := strings.Join([]string{string(left.Class), left.Subject, left.Predicate, left.ValidFrom.UTC().Format(time.RFC3339Nano), left.ID}, "\x00")
		b := strings.Join([]string{string(right.Class), right.Subject, right.Predicate, right.ValidFrom.UTC().Format(time.RFC3339Nano), right.ID}, "\x00")
		if a == b {
			return left.Version < right.Version
		}
		return a < b
	})
	conflicts := memoryConflicts(eligibleRecords)
	selected := make([]Record, 0, len(eligibleRecords))
	size := 0
	limit := authority.MaxItems
	if request.Limit > 0 && (limit == 0 || request.Limit < limit) {
		limit = request.Limit
	}
	for _, record := range eligibleRecords {
		raw, _ := json.Marshal(record)
		if (authority.MaxBytes > 0 && size+len(raw) > authority.MaxBytes) || (request.MaxBytes > 0 && size+len(raw) > request.MaxBytes) || (limit > 0 && len(selected) >= limit) {
			break
		}
		selected = append(selected, record)
		size += len(raw)
	}
	queryDigest := digestCanonical(struct {
		Request   QueryRequest
		Authority QueryAuthority
	}{request, authority})
	result := QueryResult{ProjectID: request.ProjectID, Records: selected, Conflicts: conflicts, Head: page.Head, QueryDigest: queryDigest, requestSeal: request, authoritySeal: authority}
	result.selectionSeal = querySelectionDigest(result)
	return result, nil
}

// Bind seals exact selected versions and the verified stream head without content.
func (service Service) Bind(result QueryResult, authority QueryAuthority) (MemoryBinding, error) {
	if result.selectionSeal == "" || result.selectionSeal != querySelectionDigest(result) || digestCanonical(authority) != digestCanonical(result.authoritySeal) {
		return MemoryBinding{}, fieldError("invalid_input", "binding", "query_result", nil)
	}
	entries := make([]BindingEntry, len(result.Records))
	for index, record := range result.Records {
		entries[index] = BindingEntry{ID: record.ID, Version: record.Version, Digest: record.RecordDigest}
	}
	binding := MemoryBinding{Version: ContractVersion, ProjectID: result.ProjectID, RepositoryID: authority.Scope.RepositoryID, RevisionID: authority.Scope.RevisionID, CommitOID: authority.Scope.CommitOID, ConfigurationDigest: authority.ConfigurationDigest, QueryDigest: result.QueryDigest, StoreHead: result.Head, Entries: entries, Query: result.requestSeal, AuthorityDigest: digestCanonical(authority)}
	binding.Digest = bindingDigest(binding)
	return binding, nil
}

// Revalidate checks binding integrity and each exact historical record before new approval or execution.
func (service Service) Revalidate(ctx context.Context, binding MemoryBinding, authority QueryAuthority) (BindingStatus, error) {
	if binding.Digest != bindingDigest(binding) || binding.ProjectID != authority.ProjectID || binding.ConfigurationDigest != authority.ConfigurationDigest || binding.RepositoryID != authority.Scope.RepositoryID || binding.RevisionID != authority.Scope.RevisionID || binding.CommitOID != authority.Scope.CommitOID || binding.AuthorityDigest != digestCanonical(authority) || binding.QueryDigest != digestCanonical(struct {
		Request   QueryRequest
		Authority QueryAuthority
	}{binding.Query, authority}) {
		return BindingStatus{Category: "stale"}, nil
	}
	head, err := service.Store.Head(ctx, binding.ProjectID)
	if err != nil {
		return BindingStatus{}, err
	}
	if head != binding.StoreHead {
		return BindingStatus{Category: "stale"}, nil
	}
	for _, entry := range binding.Entries {
		record, loadErr := service.Store.Version(ctx, binding.ProjectID, entry.ID, entry.Version)
		if loadErr != nil || record.RecordDigest != entry.Digest || !eligible(record, binding.Query, authority) || service.verifyRecordEvidence(ctx, record) != nil {
			return BindingStatus{Category: "stale"}, nil
		}
	}
	current, queryErr := service.Query(ctx, binding.Query, authority)
	if queryErr != nil || current.Head != binding.StoreHead || len(current.Records) != len(binding.Entries) {
		return BindingStatus{Category: "stale"}, nil
	}
	for index, record := range current.Records {
		if record.ID != binding.Entries[index].ID || record.Version != binding.Entries[index].Version || record.RecordDigest != binding.Entries[index].Digest {
			return BindingStatus{Category: "stale"}, nil
		}
	}
	return BindingStatus{Valid: true}, nil
}

// Historical retrieves one exact immutable version for audit.
func (service Service) Historical(ctx context.Context, project, id string, version uint64) (AuditView, error) {
	record, err := service.Store.Version(ctx, project, id, version)
	if err != nil {
		return AuditView{}, err
	}
	return auditView(record), nil
}

// commit retries only typed transient local unavailability under one caller-owned context and idempotency identity.
func (service Service) commit(ctx context.Context, mutation Mutation) (CommitResult, error) {
	for attempt := 0; ; attempt++ {
		result, err := service.Store.Commit(ctx, mutation)
		if err == nil {
			return result, nil
		}
		var typed *Error
		if !errors.As(err, &typed) || typed.Category != "unavailable" || attempt >= service.MaxRetries || service.Waiter == nil || service.RetryDelay <= 0 {
			return CommitResult{}, err
		}
		if waitErr := service.Waiter.Wait(ctx, service.RetryDelay); waitErr != nil {
			return CommitResult{}, fieldError("cancelled", "retry", "", waitErr)
		}
	}
}

// observe emits payload-free telemetry synchronously and never changes an already committed result.
func (service Service) observe(ctx context.Context, record OperationRecord) {
	if service.Observer != nil {
		_ = service.Observer.Observe(ctx, record)
	}
}

// authorize resolves server-side authority and rejects unknown or mismatched principals.
func (service Service) authorize(ctx context.Context, authority PrincipalAuthority, principal, project, action, actorKind string) (PrincipalDecision, error) {
	if err := ctx.Err(); err != nil {
		return PrincipalDecision{}, fieldError("cancelled", "authority", "", err)
	}
	if authority == nil {
		return PrincipalDecision{}, fieldError("unavailable", "authority", "role", nil)
	}
	decision, err := authority.Authorize(ctx, principal, project, action)
	if err != nil || !decision.Allowed || decision.Principal != principal || !digestPattern.MatchString(decision.PolicyDigest) || scopeRank(decision.MaxScope) == 99 || sensitivityRank(decision.MaxSensitivity) == 99 || !safeID.MatchString(decision.Actor.ID) || decision.Actor.Kind != actorKind || !safeID.MatchString(decision.RepositoryID) || decision.Producer == "" || decision.ProducerVersion == "" || decision.Reference == "" {
		return PrincipalDecision{}, fieldError("policy_denied", "authority", "principal", err)
	}
	return decision, nil
}

// serverCandidate replaces every caller-asserted provenance field with trusted authorization and clock data.
func (service Service) serverCandidate(candidate Candidate, decision PrincipalDecision, source, functionalResultDigest string) (Candidate, error) {
	if service.Clock == nil {
		return Candidate{}, fieldError("unavailable", "provenance", "clock", nil)
	}
	commitOID := decision.CommitOID
	if candidate.Scope.Kind == ScopeRevision {
		if commitOID == "" {
			commitOID = candidate.Scope.CommitOID
		}
		if commitOID != candidate.Scope.CommitOID {
			return Candidate{}, fieldError("policy_denied", "provenance", "commit_oid", nil)
		}
	}
	if candidate.Scope.Kind != ScopeRun && candidate.Scope.RepositoryID != decision.RepositoryID {
		return Candidate{}, fieldError("policy_denied", "provenance", "repository_id", nil)
	}
	now := service.Clock.Now().UTC()
	candidate.Provenance = Provenance{Source: source, Reference: decision.Reference, Actor: decision.Actor, Producer: decision.Producer, ProducerVersion: decision.ProducerVersion, ObservedAt: now, RepositoryID: decision.RepositoryID, CommitOID: commitOID, ConfigurationDigest: decision.ConfigurationDigest, PolicyDigest: decision.PolicyDigest, FunctionalResultDigest: functionalResultDigest}
	return candidate, nil
}

// verifyRecordEvidence rechecks every lineage reference so revocation or corruption makes committed memory stale.
func (service Service) verifyRecordEvidence(ctx context.Context, record Record) error {
	if len(record.Evidence) == 0 {
		return nil
	}
	if service.Evidence == nil {
		return fieldError("stale", "evidence", "authority", nil)
	}
	for _, evidence := range record.Evidence {
		if err := service.Evidence.Verify(ctx, record.ProjectID, record.Scope, evidence); err != nil {
			return fieldError("stale", "evidence", "evidence", err)
		}
	}
	return nil
}

// querySelectionDigest authenticates the exact internal query product accepted by Bind.
func querySelectionDigest(result QueryResult) string {
	return digestCanonical(struct {
		ProjectID   string
		Records     []Record
		Conflicts   []Conflict
		Head        Head
		QueryDigest string
		Request     QueryRequest
		Authority   QueryAuthority
	}{result.ProjectID, result.Records, result.Conflicts, result.Head, result.QueryDigest, result.requestSeal, result.authoritySeal})
}

// transitionAllowed defines the complete non-terminal lifecycle graph.
func transitionAllowed(from, to State) bool {
	if terminal(from) {
		return false
	}
	switch to {
	case StateApproved:
		return from == StateProposed || from == StateObserved
	case StateInvalidated, StateSuperseded, StateExpired:
		return from == StateProposed || from == StateObserved || from == StateApproved
	}
	return false
}

// terminal reports whether a lifecycle state rejects all future transitions.
func terminal(state State) bool {
	return state == StateInvalidated || state == StateSuperseded || state == StateExpired
}

// containsClass applies an allowlist with empty meaning unrestricted at that already-trusted layer.
func containsClass(values []Class, value Class) bool {
	if len(values) == 0 {
		return true
	}
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

// containsState applies a closed state allowlist.
func containsState(values []State, value State) bool {
	if len(values) == 0 {
		return true
	}
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

// containsAuthority applies a closed authority allowlist.
func containsAuthority(values []Authority, value Authority) bool {
	if len(values) == 0 {
		return true
	}
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

// sensitivityRank orders labels only for maximum-access comparisons.
func sensitivityRank(value Sensitivity) int {
	switch value {
	case SensitivityPublic:
		return 1
	case SensitivityInternal:
		return 2
	case SensitivityRestricted:
		return 3
	}
	return 99
}

// eligible evaluates exact scope, policy, validity, and lineage without semantic ranking.
func eligible(record Record, request QueryRequest, authority QueryAuthority) bool {
	if record.ProjectID != authority.ProjectID || record.ProjectID != request.ProjectID || record.Scope != authority.Scope || !containsClass(authority.Classes, record.Class) || !containsClass(request.Classes, record.Class) || !containsState(authority.States, record.State) || !containsState(request.States, record.State) || !containsAuthority(authority.Authorities, record.Authority) || !containsAuthority(request.Authorities, record.Authority) || sensitivityRank(record.Sensitivity) > sensitivityRank(authority.MaxSensitivity) || sensitivityRank(record.Sensitivity) > sensitivityRank(request.MaxSensitivity) {
		return false
	}
	if record.State == StateProposed && (!authority.Review || !request.IncludeProposed) {
		return false
	}
	if terminal(record.State) {
		return false
	}
	asOf := authority.AsOf.UTC()
	if asOf.IsZero() {
		return false
	}
	if asOf.Before(record.ValidFrom) || record.ValidUntil != nil && !asOf.Before(*record.ValidUntil) {
		return false
	}
	if record.Provenance.ConfigurationDigest != "" && authority.ConfigurationDigest != record.Provenance.ConfigurationDigest {
		return false
	}
	if record.Scope.Kind == ScopeRevision && record.Scope.CommitOID != authority.Scope.CommitOID {
		return false
	}
	return true
}

// memoryConflicts reports every logical key with more than one distinct content digest.
func memoryConflicts(records []Record) []Conflict {
	groups := map[string]map[string][]string{}
	for _, record := range records {
		if groups[record.LogicalKey()] == nil {
			groups[record.LogicalKey()] = map[string][]string{}
		}
		groups[record.LogicalKey()][record.ContentDigest] = append(groups[record.LogicalKey()][record.ContentDigest], record.ID)
	}
	out := []Conflict{}
	for key, variants := range groups {
		if len(variants) < 2 {
			continue
		}
		ids := []string{}
		for _, values := range variants {
			ids = append(ids, values...)
		}
		sort.Strings(ids)
		out = append(out, Conflict{LogicalKey: key, IDs: ids})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LogicalKey < out[j].LogicalKey })
	return out
}

// bindingDigest computes the tamper-evident digest without recursively including itself.
func bindingDigest(binding MemoryBinding) string {
	binding.Digest = ""
	return digestCanonical(binding)
}

// reviewView produces a detached allowlisted projection for reviewers.
func reviewView(record Record) ReviewView {
	clone := cloneRecord(record)
	return ReviewView{ContractVersion: clone.ContractVersion, ID: clone.ID, ProjectID: clone.ProjectID, Version: clone.Version, Scope: clone.Scope, Class: clone.Class, State: clone.State, Subject: clone.Subject, Predicate: clone.Predicate, Value: clone.Value, Authority: clone.Authority, Confidence: clone.Confidence, Sensitivity: clone.Sensitivity, Provenance: clone.Provenance, Evidence: clone.Evidence, RecordDigest: clone.RecordDigest}
}

// auditView produces a content-free lifecycle projection.
func auditView(record Record) AuditView {
	return AuditView{ID: record.ID, ProjectID: record.ProjectID, Version: record.Version, State: record.State, Authority: record.Authority, RecordDigest: record.RecordDigest, DecisionID: record.Provenance.DecisionID}
}
