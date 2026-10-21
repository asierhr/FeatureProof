package contractreview

import (
	"context"
	"strconv"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/githubpr"
)

// Action is the closed authorization vocabulary enforced by the shared application service.
type Action string

const (
	// ActionCreate authorizes submitting exact candidate and baseline references.
	ActionCreate Action = "review.create"
	// ActionRead authorizes reading a bounded review view.
	ActionRead Action = "review.read"
	// ActionDecide authorizes changing the human decision set.
	ActionDecide Action = "review.decide"
	// ActionExport authorizes a local contract publication.
	ActionExport Action = "review.export"
	// ActionUse authorizes consuming an approved projection as contract context.
	ActionUse Action = "review.use"
)

// Authorizer grants one authenticated principal an exact action on a repository and review.
type Authorizer interface {
	Authorize(context.Context, string, Action, string, string) error
}

// Resolver resolves already-authorized opaque references into sealed catalogs without credentials in core.
type Resolver interface {
	ResolveCandidate(context.Context, string) (CandidateSnapshot, error)
	ResolveBaseline(context.Context, string) (BaselineSnapshot, error)
}

// PolicyAuthority resolves the current composition-owned policy identity at each authority checkpoint.
type PolicyAuthority interface {
	CurrentPolicyDigest(context.Context, string, string) (string, error)
}

// FixedPolicyAuthority supplies one immutable policy digest for local composition.
type FixedPolicyAuthority string

// CurrentPolicyDigest returns the configured digest without consulting request data.
func (p FixedPolicyAuthority) CurrentPolicyDigest(context.Context, string, string) (string, error) {
	return string(p), nil
}

// Clock supplies trusted UTC audit instants independent of request payloads.
type Clock interface{ Now() time.Time }

// Observer receives bounded operational events and is never a source of durable state.
type Observer interface {
	Observe(context.Context, AuditEvent)
}

// CreateCommand carries only authenticated identity, opaque exact references and idempotency data.
type CreateCommand struct {
	PrincipalID, CandidateRef, BaselineRef, IdempotencyKey string
	Limits                                                 Limits
	PolicyDigest                                           string
	GitHub                                                 *githubpr.PullRequestSnapshot
	GitHubActorID                                          int64
}

// DecisionCommand combines trusted principal identity with one explicit CAS decision.
type DecisionCommand struct {
	PrincipalID, ReviewID string
	Decision              DecisionInput
}

// ExportCommand requests one explicit guarded local publication of a current approval.
type ExportCommand struct {
	PrincipalID, ReviewID, IdempotencyKey string
	ExpectedRevision                      uint64
	Target                                PublishRequest
	Info                                  ExportInfo
}

// ApprovedReference identifies one exact review and approval binding for downstream consumption.
type ApprovedReference struct {
	PrincipalID, ReviewID string
	ExpectedRevision      uint64
	ProjectionDigest      string
}

// Service coordinates authority, durable CAS, drift checks, export and approved-source projection.
type Service struct {
	Authorizer Authorizer
	Resolver   Resolver
	Policy     PolicyAuthority
	Clock      Clock
	Store      Store
	Exporter   Exporter
	Publisher  Publisher
	Observer   Observer
	GitHub     GitHubPullRequestAuthority
}

// Close releases filesystem descriptors owned by durable store and publisher adapters.
func (s Service) Close() error {
	var first error
	if closer, ok := s.Store.(interface{ Close() error }); ok {
		first = closer.Close()
	}
	if closer, ok := s.Publisher.(interface{ Close() error }); ok {
		if err := closer.Close(); first == nil {
			first = err
		}
	}
	return first
}

// Validate rejects incomplete composition before any resolver, store or filesystem callback.
func (s Service) Validate() error {
	if s.Authorizer == nil || s.Resolver == nil || s.Policy == nil || s.Clock == nil || s.Store == nil || s.Publisher == nil {
		return typedError(CategoryInvalidInput, "service", nil)
	}
	return nil
}

// Create authorizes, resolves, diffs and durably publishes one immutable artifact exactly once.
func (s Service) Create(ctx context.Context, command CreateCommand) (ReviewView, error) {
	if err := s.Validate(); err != nil {
		return ReviewView{}, err
	}
	if !safeReference(command.CandidateRef) || !safeReference(command.BaselineRef) || !safeReference(command.IdempotencyKey) || !validDigest(command.PolicyDigest) {
		return ReviewView{}, typedError(CategoryInvalidInput, "create", nil)
	}
	candidate, err := s.Resolver.ResolveCandidate(ctx, command.CandidateRef)
	if err != nil {
		return ReviewView{}, translateCollaborator(ctx, "candidate", err)
	}
	if command.GitHub != nil {
		if err = s.validateGitHub(ctx, *command.GitHub, candidate.Identity); err != nil {
			return ReviewView{}, err
		}
		candidate.GitHub = deepClone(command.GitHub)
		candidate.GitHubActorID = command.GitHubActorID
	}
	if err = s.Authorizer.Authorize(ctx, command.PrincipalID, ActionCreate, candidate.Identity.RepositoryID, ""); err != nil {
		return ReviewView{}, typedError(CategoryUnauthorized, "authorize", nil)
	}
	baseline, err := s.Resolver.ResolveBaseline(ctx, command.BaselineRef)
	if err != nil {
		return ReviewView{}, translateCollaborator(ctx, "baseline", err)
	}
	if err = s.validateDerivedBaseline(ctx, command.PrincipalID, baseline); err != nil {
		return ReviewView{}, err
	}
	artifact, err := CreateArtifact(ctx, candidate, baseline, command.Limits)
	if err != nil {
		return ReviewView{}, err
	}
	currentPolicy, err := s.Policy.CurrentPolicyDigest(ctx, candidate.Identity.RepositoryID, candidate.Identity.Scope)
	if err != nil || currentPolicy != command.PolicyDigest {
		return ReviewView{}, typedError(CategoryInvalidated, "binding.policy", err)
	}
	requestDigest, err := digestValue(ctx, struct {
		Principal, Candidate, Baseline, Policy string
		Limits                                 Limits
		GitHub                                 *githubpr.PullRequestSnapshot
		GitHubActorID                          int64
	}{command.PrincipalID, command.CandidateRef, command.BaselineRef, command.PolicyDigest, command.Limits, command.GitHub, command.GitHubActorID}, command.Limits.MaxBytes)
	if err != nil {
		return ReviewView{}, err
	}
	event := AuditEvent{Kind: "review_created", Revision: 0, At: s.Clock.Now().UTC(), ActorID: command.PrincipalID, ReviewID: artifact.ID, Digest: artifact.Digest}
	record := ReviewRecord{Artifact: artifact, CandidateRef: command.CandidateRef, BaselineRef: command.BaselineRef, BaselineApproval: deepClone(baseline.ApprovedReference), PolicyDigest: command.PolicyDigest, State: StateCreated, Revision: 0, Events: []AuditEvent{event}}
	stored, err := s.Store.Create(ctx, command.PrincipalID, command.IdempotencyKey, requestDigest, record)
	if err != nil {
		return ReviewView{}, err
	}
	s.observe(ctx, event)
	return viewOf(stored), nil
}

// Get authorizes and returns the same bounded view used by every external surface.
func (s Service) Get(ctx context.Context, principal, reviewID string) (ReviewView, error) {
	record, err := s.Store.Load(ctx, reviewID)
	if err != nil {
		return ReviewView{}, err
	}
	if err = s.Authorizer.Authorize(ctx, principal, ActionRead, record.Artifact.Candidate.RepositoryID, reviewID); err != nil {
		return ReviewView{}, typedError(CategoryUnauthorized, "authorize", nil)
	}
	return viewOf(record), nil
}

// VerifyGitHubReview binds an authenticated command snapshot to the exact PR authority persisted in a review.
func (s Service) VerifyGitHubReview(ctx context.Context, principal, reviewID string, snapshot githubpr.PullRequestSnapshot) error {
	record, err := s.Store.Load(ctx, reviewID)
	if err != nil {
		return err
	}
	if err = s.Authorizer.Authorize(ctx, principal, ActionRead, record.Artifact.Candidate.RepositoryID, reviewID); err != nil {
		return typedError(CategoryUnauthorized, "authorize", nil)
	}
	if record.Artifact.GitHub == nil || githubpr.VerifySnapshot(snapshot) != nil || !decisionJSONEqual(*record.Artifact.GitHub, snapshot) {
		return typedError(CategoryUnauthorized, "github.review_binding", nil)
	}
	return nil
}

// Decide re-resolves every bound identity before committing one complete human decision revision.
func (s Service) Decide(ctx context.Context, command DecisionCommand) (ReviewView, error) {
	record, err := s.Store.Load(ctx, command.ReviewID)
	if err != nil {
		return ReviewView{}, err
	}
	if err = s.Authorizer.Authorize(ctx, command.PrincipalID, ActionDecide, record.Artifact.Candidate.RepositoryID, command.ReviewID); err != nil {
		return ReviewView{}, typedError(CategoryUnauthorized, "authorize", nil)
	}
	candidate, baseline, err := s.revalidate(ctx, record)
	if err != nil {
		return ReviewView{}, s.invalidate(ctx, command.PrincipalID, record, err)
	}
	decision, state, projection, err := ReduceDecision(ctx, record.Artifact, record.Revision, record.Decision, command.Decision, command.PrincipalID, s.Clock.Now().UTC(), candidate, baseline, record.PolicyDigest)
	if err != nil {
		return ReviewView{}, err
	}
	// Re-resolve immediately before CAS so reducer work cannot open a drift window.
	if _, _, err = s.revalidate(ctx, record); err != nil {
		return ReviewView{}, s.invalidate(ctx, command.PrincipalID, record, err)
	}
	next := record.Clone()
	next.Revision = decision.Revision
	next.Decision = &decision
	next.State = state
	next.Projection = projection
	kind := "decision_recorded"
	if state == StateApproved {
		kind = "approval_created"
	}
	event := AuditEvent{Kind: kind, Revision: next.Revision, At: decision.DecidedAt, ActorID: command.PrincipalID, GitHubActorID: decision.GitHubActorID, ReviewID: record.Artifact.ID, Digest: decision.Digest}
	next.Events = append(next.Events, event)
	stored, err := s.Store.CompareAndSwap(ctx, command.PrincipalID, record.Revision, "decide", command.Decision.IdempotencyKey, decision.RequestDigest, next)
	if err != nil {
		return ReviewView{}, err
	}
	s.observe(ctx, event)
	return viewOf(stored), nil
}

// Export revalidates before serialization and again immediately before filesystem I/O.
func (s Service) Export(ctx context.Context, command ExportCommand) (ExportReceipt, error) {
	record, err := s.Store.Load(ctx, command.ReviewID)
	if err != nil {
		return ExportReceipt{}, err
	}
	if err = s.Authorizer.Authorize(ctx, command.PrincipalID, ActionExport, record.Artifact.Candidate.RepositoryID, command.ReviewID); err != nil {
		return ExportReceipt{}, typedError(CategoryUnauthorized, "authorize", nil)
	}
	if record.State != StateApproved || record.Projection == nil {
		return ExportReceipt{}, typedError(CategoryInvalidated, "approval", nil)
	}
	if _, _, err = s.revalidate(ctx, record); err != nil {
		return ExportReceipt{}, s.invalidate(ctx, command.PrincipalID, record, err)
	}
	raw, outputDigest, err := s.Exporter.Export(ctx, *record.Projection, command.Info, record.Artifact.Limits)
	if err != nil {
		return ExportReceipt{}, err
	}
	if err = validateExportShape(raw); err != nil {
		return ExportReceipt{}, err
	}
	requestDigest, err := digestValue(ctx, struct {
		Principal, Review, Key string
		Revision               uint64
		Target                 PublishRequest
		Info                   ExportInfo
		Output                 string
	}{command.PrincipalID, record.Artifact.ID, command.IdempotencyKey, command.ExpectedRevision, command.Target, command.Info, outputDigest}, record.Artifact.Limits.MaxBytes)
	if err != nil {
		return ExportReceipt{}, err
	}
	command.Target.AttemptID = requestDigest
	for _, receipt := range record.Receipts {
		if receipt.IdempotencyKey == command.IdempotencyKey {
			if receipt.RequestDigest != requestDigest {
				return ExportReceipt{}, typedError(CategoryConflict, "idempotency", nil)
			}
			return receipt, nil
		}
	}
	if record.PendingExport == nil {
		if record.Revision != command.ExpectedRevision {
			return ExportReceipt{}, typedError(CategoryConflict, "revision", nil)
		}
		prepared := record.Clone()
		prepared.Revision++
		prepared.PendingExport = &PendingExport{PrincipalID: command.PrincipalID, BaseRevision: record.Revision, IdempotencyKey: command.IdempotencyKey, RequestDigest: requestDigest, OutputDigest: outputDigest, Bytes: len(raw), Target: command.Target, Info: command.Info}
		prepareEvent := AuditEvent{Kind: "export_prepared", Revision: prepared.Revision, At: s.Clock.Now().UTC(), ActorID: command.PrincipalID, ReviewID: record.Artifact.ID, Digest: requestDigest}
		prepared.Events = append(prepared.Events, prepareEvent)
		record, err = s.Store.CompareAndSwap(ctx, command.PrincipalID, record.Revision, "export.prepare", command.IdempotencyKey, requestDigest, prepared)
		if err != nil {
			return ExportReceipt{}, err
		}
		s.observe(ctx, prepareEvent)
	} else if record.PendingExport.RequestDigest != requestDigest || record.PendingExport.IdempotencyKey != command.IdempotencyKey || record.PendingExport.BaseRevision != command.ExpectedRevision {
		return ExportReceipt{}, typedError(CategoryConflict, "export.pending", nil)
	}
	if _, _, err = s.revalidate(ctx, record); err != nil {
		return ExportReceipt{}, s.invalidate(ctx, command.PrincipalID, record, err)
	}
	target, publishErr := s.Publisher.Publish(ctx, command.Target, raw, outputDigest)
	if publishErr != nil {
		inspected, actual, inspectErr := s.Publisher.Inspect(ctx, command.Target.Target, requestDigest)
		if inspectErr != nil || actual != outputDigest {
			return ExportReceipt{}, publishErr
		}
		target = inspected
	}
	receipt := ExportReceipt{ReviewID: record.Artifact.ID, Revision: record.Revision + 1, Target: target, OutputDigest: outputDigest, Bytes: len(raw), PublishedAt: s.Clock.Now().UTC(), IdempotencyKey: command.IdempotencyKey, RequestDigest: requestDigest}
	next := record.Clone()
	next.Revision++
	next.PendingExport = nil
	next.Receipts = append(next.Receipts, receipt)
	event := AuditEvent{Kind: "export_committed", Revision: next.Revision, At: receipt.PublishedAt, ActorID: command.PrincipalID, ReviewID: record.Artifact.ID, Digest: outputDigest}
	next.Events = append(next.Events, event)
	stored, err := s.Store.CompareAndSwap(ctx, command.PrincipalID, record.Revision, "export.commit", command.IdempotencyKey, requestDigest, next)
	if err != nil {
		return ExportReceipt{}, err
	}
	s.observe(ctx, event)
	for _, value := range stored.Receipts {
		if value.IdempotencyKey == command.IdempotencyKey {
			return value, nil
		}
	}
	return ExportReceipt{}, typedError(CategoryInternal, "export.receipt", nil)
}

// ResolveApproved revalidates current authority and projects review provenance plus original lineage into a neutral snapshot.
func (s Service) ResolveApproved(ctx context.Context, reference ApprovedReference) (contractcatalog.SourceSnapshot, error) {
	return s.resolveApprovedRecursive(ctx, reference)
}

// baselineValidationPath bounds recursive approved baselines and rejects cycles before loading another aggregate.
type baselineValidationPath []string

// resolveApprovedRecursive revalidates an approval and every prior approval in its baseline lineage.
func (s Service) resolveApprovedRecursive(ctx context.Context, reference ApprovedReference) (contractcatalog.SourceSnapshot, error) {
	path, _ := ctx.Value(baselineValidationKey{}).(baselineValidationPath)
	if len(path) >= 32 {
		return contractcatalog.SourceSnapshot{}, typedError(CategoryLimitExceeded, "baseline.depth", nil)
	}
	for _, reviewID := range path {
		if reviewID == reference.ReviewID {
			return contractcatalog.SourceSnapshot{}, typedError(CategoryIncompatibleBaseline, "baseline.cycle", nil)
		}
	}
	nextPath := append(append(baselineValidationPath(nil), path...), reference.ReviewID)
	ctx = context.WithValue(ctx, baselineValidationKey{}, nextPath)
	record, err := s.Store.Load(ctx, reference.ReviewID)
	if err != nil {
		return contractcatalog.SourceSnapshot{}, err
	}
	if err = s.Authorizer.Authorize(ctx, reference.PrincipalID, ActionUse, record.Artifact.Candidate.RepositoryID, reference.ReviewID); err != nil {
		return contractcatalog.SourceSnapshot{}, typedError(CategoryUnauthorized, "authorize", nil)
	}
	if record.State != StateApproved || record.Projection == nil || record.Revision != reference.ExpectedRevision || record.Projection.Binding.ProjectionDigest != reference.ProjectionDigest {
		return contractcatalog.SourceSnapshot{}, typedError(CategoryInvalidated, "approval", nil)
	}
	if _, _, err = s.revalidate(ctx, record); err != nil {
		return contractcatalog.SourceSnapshot{}, s.invalidate(ctx, reference.PrincipalID, record, err)
	}
	return approvedSnapshot(*record.Projection, record.Artifact), nil
}

// baselineValidationKey owns recursion state exclusively inside one approval resolution call tree.
type baselineValidationKey struct{}

// revalidate compares freshly resolved catalogs and every analyzer/source identity to the immutable artifact binding.
func (s Service) revalidate(ctx context.Context, record ReviewRecord) (CandidateSnapshot, BaselineSnapshot, error) {
	candidate, err := s.Resolver.ResolveCandidate(ctx, record.CandidateRef)
	if err != nil {
		return CandidateSnapshot{}, BaselineSnapshot{}, translateCollaborator(ctx, "candidate", err)
	}
	baseline, err := s.Resolver.ResolveBaseline(ctx, record.BaselineRef)
	if err != nil {
		return CandidateSnapshot{}, BaselineSnapshot{}, translateCollaborator(ctx, "baseline", err)
	}
	if err := s.validateDerivedBaseline(ctx, "", baseline); err != nil {
		return CandidateSnapshot{}, BaselineSnapshot{}, err
	}
	if !validIdentity(candidate.Identity) || candidate.Identity.Digest != candidate.Catalog.Digest() || candidate.Identity.Version != candidate.Catalog.Version() {
		return CandidateSnapshot{}, BaselineSnapshot{}, typedError(CategoryInvalidated, "binding.candidate.catalog", nil)
	}
	if record.Artifact.GitHub != nil {
		if err := s.validateGitHub(ctx, *record.Artifact.GitHub, candidate.Identity); err != nil {
			return CandidateSnapshot{}, BaselineSnapshot{}, err
		}
		candidate.GitHub = deepClone(record.Artifact.GitHub)
	}
	if !validIdentity(baseline.Identity) || baseline.Identity.Digest != baseline.Catalog.Digest() || baseline.Identity.Version != baseline.Catalog.Version() || baseline.Kind == "approved_projection" && baseline.ApprovedReference == nil {
		return CandidateSnapshot{}, BaselineSnapshot{}, typedError(CategoryInvalidated, "binding.baseline.catalog", nil)
	}
	if err := validateCatalogBounds(ctx, []contractcatalog.OperationCatalog{candidate.Catalog, baseline.Catalog}, record.Artifact.Limits); err != nil {
		return CandidateSnapshot{}, BaselineSnapshot{}, err
	}
	analyzers, err := normalizeAnalyzers(candidate.Analyzers)
	if err != nil {
		return CandidateSnapshot{}, BaselineSnapshot{}, err
	}
	if !decisionJSONEqual(candidate.Identity, record.Artifact.Candidate) {
		return CandidateSnapshot{}, BaselineSnapshot{}, typedError(CategoryInvalidated, "binding.candidate", nil)
	}
	if !decisionJSONEqual(baseline.Identity, record.Artifact.Baseline) {
		return CandidateSnapshot{}, BaselineSnapshot{}, typedError(CategoryInvalidated, "binding.baseline", nil)
	}
	if !decisionJSONEqual(baseline.ApprovedReference, record.BaselineApproval) {
		return CandidateSnapshot{}, BaselineSnapshot{}, typedError(CategoryInvalidated, "binding.baseline.approval", nil)
	}
	if !decisionJSONEqual(candidate.Catalog.Sources(), record.Artifact.Sources) {
		return CandidateSnapshot{}, BaselineSnapshot{}, typedError(CategoryInvalidated, "binding.sources", nil)
	}
	if !decisionJSONEqual(candidate.Catalog.Sources(), record.Artifact.Manifests.Candidate) || !decisionJSONEqual(baseline.Catalog.Sources(), record.Artifact.Manifests.Baseline) {
		return CandidateSnapshot{}, BaselineSnapshot{}, typedError(CategoryInvalidated, "binding.manifests", nil)
	}
	if !decisionJSONEqual(analyzers, record.Artifact.Analyzers) {
		return CandidateSnapshot{}, BaselineSnapshot{}, typedError(CategoryInvalidated, "binding.analyzers", nil)
	}
	currentPolicy, policyErr := s.Policy.CurrentPolicyDigest(ctx, record.Artifact.Candidate.RepositoryID, record.Artifact.Candidate.Scope)
	if policyErr != nil || currentPolicy != record.PolicyDigest {
		return CandidateSnapshot{}, BaselineSnapshot{}, typedError(CategoryInvalidated, "binding.policy", policyErr)
	}
	if record.Projection != nil {
		digest, digestErr := projectionDigest(ctx, *record.Projection, record.Artifact.Limits.MaxBytes)
		if digestErr != nil || digest != record.Projection.Binding.ProjectionDigest {
			return CandidateSnapshot{}, BaselineSnapshot{}, typedError(CategoryInvalidated, "projection", digestErr)
		}
	}
	return candidate, baseline, nil
}

// validateGitHub refreshes the authenticated PR and binds its full fingerprint to the candidate commit and repository.
func (s Service) validateGitHub(ctx context.Context, expected githubpr.PullRequestSnapshot, identity CatalogIdentity) error {
	if s.GitHub == nil || githubpr.VerifySnapshot(expected) != nil || strconv.FormatInt(expected.RepositoryID, 10) != identity.RepositoryID || expected.HeadOID != identity.Commit {
		return typedError(CategoryInvalidated, "github.binding", nil)
	}
	fresh, err := s.GitHub.PullRequest(ctx, expected.InstallationID, expected.RepositoryID, expected.Number)
	if err != nil || githubpr.VerifySnapshot(fresh) != nil || !decisionJSONEqual(fresh, expected) {
		return typedError(CategoryInvalidated, "github.binding", err)
	}
	return nil
}

// validateDerivedBaseline proves a derived baseline exclusively through a still-current durable approval.
func (s Service) validateDerivedBaseline(ctx context.Context, principal string, baseline BaselineSnapshot) error {
	if baseline.Kind != "approved_projection" {
		return nil
	}
	if baseline.ApprovedReference == nil {
		return typedError(CategoryIncompatibleBaseline, "baseline.approval", nil)
	}
	reference := *baseline.ApprovedReference
	if principal != "" && reference.PrincipalID != principal {
		return typedError(CategoryUnauthorized, "baseline.approval", nil)
	}
	if principal == "" {
		principal = reference.PrincipalID
	}
	reference.PrincipalID = principal
	authorized, err := s.resolveApprovedRecursive(ctx, reference)
	if err != nil {
		return typedError(CategoryIncompatibleBaseline, "baseline.approval", err)
	}
	if !decisionJSONEqual(authorized.Operations, baseline.Catalog.Operations()) || !equivalentSchemeSet(authorized.SecuritySchemes, baseline.Catalog.SecuritySchemes()) {
		return typedError(CategoryIncompatibleBaseline, "baseline.projection", nil)
	}
	return nil
}

// equivalentSchemeSet compares the snapshot list with the catalog's name-keyed closed scheme map.
func equivalentSchemeSet(left []contractcatalog.SecurityScheme, right map[string]contractcatalog.SecurityScheme) bool {
	if len(left) != len(right) {
		return false
	}
	for _, scheme := range left {
		if other, ok := right[scheme.Name]; !ok || !decisionJSONEqual(scheme, other) {
			return false
		}
	}
	return true
}

// invalidate persists drift as an append-only terminal transition before returning its safe category.
func (s Service) invalidate(ctx context.Context, principal string, record ReviewRecord, cause error) error {
	if record.State == StateInvalidated {
		return typedError(CategoryInvalidated, "binding", nil)
	}
	next := record.Clone()
	next.State = StateInvalidated
	next.Revision++
	event := AuditEvent{Kind: "review_invalidated", Revision: next.Revision, At: s.Clock.Now().UTC(), ActorID: principal, ReviewID: record.Artifact.ID, Category: CategoryInvalidated}
	next.Events = append(next.Events, event)
	requestDigest, _ := digestValue(context.Background(), struct {
		Review   string
		Revision uint64
	}{record.Artifact.ID, record.Revision}, record.Artifact.Limits.MaxBytes)
	if _, err := s.Store.CompareAndSwap(context.WithoutCancel(ctx), principal, record.Revision, "invalidate", requestDigest[:24], requestDigest, next); err != nil {
		return err
	}
	s.observe(context.WithoutCancel(ctx), event)
	return typedError(CategoryInvalidated, "binding", cause)
}

// approvedSnapshot attaches explicit human-review provenance while retaining every original operation lineage entry.
func approvedSnapshot(projection ApprovedProjection, artifact ReviewArtifact) contractcatalog.SourceSnapshot {
	operations := deepClone(projection.Operations)
	confidence, completeness := contractcatalog.ConfidenceHigh, contractcatalog.CompletenessComplete
	if len(operations) == 0 {
		completeness = contractcatalog.CompletenessUnknown
	}
	for i := range operations {
		confidence = weakerConfidence(confidence, operations[i].Confidence)
		completeness = weakerCompleteness(completeness, operations[i].Completeness)
		reviewProvenance := approvalProvenance(artifact, projection.Binding.ProjectionDigest, operations[i].Confidence, operations[i].Completeness)
		operations[i].Provenance = append(operations[i].Provenance, reviewProvenance)
		operations[i].Classes = append(operations[i].Classes, contractcatalog.ClassAuthoritative)
		for claimIndex := range operations[i].Claims {
			claim := &operations[i].Claims[claimIndex]
			claim.Provenance = append(claim.Provenance, approvalProvenance(artifact, projection.Binding.ProjectionDigest, claim.Confidence, claim.Completeness))
		}
		operations[i].Provenance = canonicalProvenance(operations[i].Provenance, len(operations[i].Provenance))
		operations[i].Classes = sortedClasses(operations[i].Classes)
		operations[i].Claims = canonicalClaims(operations[i].Claims)
	}
	if projection.Omissions != (contractcatalog.OmissionCounts{}) || len(projection.Diagnostics) > 0 || len(projection.Conflicts) > 0 {
		completeness = weakerCompleteness(completeness, contractcatalog.CompletenessPartial)
	}
	authority := approvalProvenance(artifact, projection.Binding.ProjectionDigest, confidence, completeness)
	return contractcatalog.SourceSnapshot{Version: contractcatalog.VersionV1, Source: contractcatalog.SourceSummary{ID: authority.SourceID, RepositoryID: authority.RepositoryID, Revision: authority.Revision, Kind: "review-approved-v1", Class: contractcatalog.ClassAuthoritative, Digest: authority.SourceDigest, AdapterVersion: authority.AdapterVersion, Confidence: confidence, Completeness: completeness}, Operations: operations, SecuritySchemes: mapSchemes(projection.SecuritySchemes), Omissions: projection.Omissions, Diagnostics: deepClone(projection.Diagnostics)}
}

// approvalProvenance records human authority while preserving the evidence's confidence and completeness.
func approvalProvenance(artifact ReviewArtifact, digest string, confidence contractcatalog.Confidence, completeness contractcatalog.Completeness) contractcatalog.Provenance {
	return contractcatalog.Provenance{SourceID: "review-" + artifact.ID, RepositoryID: artifact.Candidate.RepositoryID, Revision: artifact.Candidate.Commit, Class: contractcatalog.ClassAuthoritative, SourceDigest: digest, AdapterVersion: "contract-review-v1", SafeLocation: "contract-review/" + artifact.ID, Confidence: confidence, Completeness: completeness}
}

// weakerConfidence returns the least confident of two evidence classifications.
func weakerConfidence(a, b contractcatalog.Confidence) contractcatalog.Confidence {
	rank := map[contractcatalog.Confidence]int{contractcatalog.ConfidenceLow: 0, contractcatalog.ConfidenceMedium: 1, contractcatalog.ConfidenceHigh: 2}
	if rank[b] < rank[a] {
		return b
	}
	return a
}

// weakerCompleteness returns the least complete of two evidence classifications.
func weakerCompleteness(a, b contractcatalog.Completeness) contractcatalog.Completeness {
	rank := map[contractcatalog.Completeness]int{contractcatalog.CompletenessUnknown: 0, contractcatalog.CompletenessPartial: 1, contractcatalog.CompletenessComplete: 2}
	if rank[b] < rank[a] {
		return b
	}
	return a
}

// mapSchemes converts the canonical scheme map into the neutral snapshot list.
func mapSchemes(values map[string]contractcatalog.SecurityScheme) []contractcatalog.SecurityScheme {
	out := make([]contractcatalog.SecurityScheme, 0, len(values))
	for name, value := range values {
		value.Name = name
		out = append(out, value)
	}
	return out
}

// viewOf projects only bounded identifiers, counts and digests from a durable aggregate.
func viewOf(record ReviewRecord) ReviewView {
	view := ReviewView{Version: VersionV1, ID: record.Artifact.ID, Digest: record.Artifact.Digest, State: record.State, Revision: record.Revision, Counts: record.Artifact.Counts}
	if record.Decision != nil {
		view.DecisionDigest = record.Decision.Digest
	}
	if record.Projection != nil {
		view.ProjectionDigest = record.Projection.Binding.ProjectionDigest
	}
	return view
}

// safeReference limits opaque aliases and idempotency values before any collaborator receives them.
func safeReference(value string) bool {
	return value != "" && len(value) <= 128 && safeLocation(value) == value
}

// translateCollaborator preserves cancellation while collapsing all other resolver details.
func translateCollaborator(ctx context.Context, location string, err error) error {
	if ctx.Err() != nil {
		return typedError(CategoryCancelled, location, ctx.Err())
	}
	if isCancellation(err) {
		return typedError(CategoryCancelled, location, err)
	}
	return typedError(CategoryUnavailable, location, nil)
}

// observe emits a bounded best-effort signal after durable state confirmation.
func (s Service) observe(ctx context.Context, event AuditEvent) {
	if s.Observer != nil {
		s.Observer.Observe(ctx, event)
	}
}
