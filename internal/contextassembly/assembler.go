package contextassembly

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"
)

// Profile is one composition-sealed least-privilege projection policy.
type Profile struct {
	Role               Role
	Version            string
	InstructionVersion string
	Instructions       json.RawMessage
	AllowedSources     map[SourceKind]bool
	AllowedFields      map[SourceKind]map[string]bool
	MandatorySources   map[SourceKind]bool
	RequiredComplete   map[SourceKind]bool
	ConflictBlocks     map[SourceKind]bool
	MaxSensitivity     int
	Budgets            Budgets
	AllowRankFallback  bool
}

// ProfileCatalog resolves immutable trusted profiles by closed role.
type ProfileCatalog interface{ Profile(Role) (Profile, bool) }

// Producer returns one already-authorized neutral source projection.
type Producer interface {
	Kind() SourceKind
	TrustClass() TrustClass
	Identity() (string, string)
	Produce(context.Context, Request) (Projection, error)
	Revalidate(context.Context, SourceBinding, AuthorityBinding) (bool, error)
}

// ProducerRegistry resolves sealed producer identities against the current composition-owned source authorities.
type ProducerRegistry interface {
	Resolve(context.Context, string, RegistryDescriptor, AuthorityBinding) ([]Producer, error)
}

// SecretGate verifies that a pre-redacted item contains no forbidden material.
type SecretGate interface {
	Check(context.Context, Item) error
}

// RankInput exposes only eligible safe identities and bounded metadata.
type RankInput struct {
	ID            string
	Kind          SourceKind
	SafeReference string
	Digest        string
}

// RankedID contains one derived score and cannot carry content or authority.
type RankedID struct {
	ID    string
	Score float64
}

// SemanticRanker optionally reorders already eligible identities.
type SemanticRanker interface {
	Rank(context.Context, Role, []RankInput, int) ([]RankedID, error)
}

// RankUnavailableError reports a missing, stale or corrupt derived index that a profile may safely bypass.
type RankUnavailableError struct{ State string }

// Error returns only the controlled derived-index state.
func (e *RankUnavailableError) Error() string { return "semantic ranker unavailable: " + e.State }

// TokenCounter counts the exact canonical model envelope for one selected provider/model.
type TokenCounter interface {
	Count(context.Context, string, string, []byte) (int, error)
}

// Store persists immutable snapshots and bindings through idempotent receipts.
type Store interface {
	CommitSnapshot(context.Context, string, Request, Snapshot) (Snapshot, bool, error)
	LoadSnapshot(context.Context, string) (Snapshot, error)
	CommitBinding(context.Context, string, ContextBinding) (ContextBinding, bool, error)
	LoadBinding(context.Context, string) (ContextBinding, error)
}

// Record is payload-free terminal telemetry for assembly, binding and revalidation.
type Record struct {
	RunID, Digest string
	Role          Role
	Outcome       Outcome
	Counts        Counts
	Duration      time.Duration
}

// Observer confirms one terminal content-free record.
type Observer interface {
	Observe(context.Context, Record) error
}

// Clock supplies deterministic duration accounting.
type Clock interface{ Now() time.Time }

// Config bounds independent terminal observation.
type Config struct{ ObserverTimeout time.Duration }

// Dependencies contains every explicit context assembly boundary.
type Dependencies struct {
	Profiles  ProfileCatalog
	Producers []Producer
	Secrets   SecretGate
	Ranker    SemanticRanker
	Tokens    TokenCounter
	Store     Store
	Observer  Observer
	Clock     Clock
}

// Assembler owns deterministic filtering, packing, sealing and persistence.
type Assembler struct {
	config    Config
	deps      Dependencies
	producers map[SourceKind]Producer
}

// NewAssembler seals a producer registry and rejects partial composition.
func NewAssembler(config Config, deps Dependencies) (*Assembler, error) {
	if config.ObserverTimeout <= 0 || deps.Profiles == nil || deps.Secrets == nil || deps.Tokens == nil || deps.Store == nil || deps.Observer == nil || deps.Clock == nil || len(deps.Producers) == 0 {
		return nil, &Error{Category: CategoryInvalid, Stage: "compose", Path: "dependencies"}
	}
	registry := make(map[SourceKind]Producer, len(deps.Producers))
	for _, producer := range deps.Producers {
		if producer == nil || !validSourceKind(producer.Kind()) || producer.TrustClass() != expectedSourceTrust(producer.Kind()) || registry[producer.Kind()] != nil {
			return nil, &Error{Category: CategoryInvalid, Stage: "compose", Path: "producers"}
		}
		registry[producer.Kind()] = producer
	}
	return &Assembler{config: config, deps: deps, producers: registry}, nil
}

// Assemble produces or redelivers one immutable context snapshot.
func (a *Assembler) Assemble(ctx context.Context, request Request) (snapshot Snapshot, err error) {
	started := a.deps.Clock.Now()
	record := Record{RunID: request.RunID, Role: request.Role, Outcome: OutcomeFailed}
	emit := true
	defer func() {
		if !emit {
			return
		}
		record.Digest, record.Counts = snapshot.Digest, snapshot.Counts
		record.Duration = a.deps.Clock.Now().Sub(started)
		if record.Duration < 0 {
			record.Duration = 0
		}
		if err == nil {
			if snapshot.Completeness == CompletenessInsufficient {
				record.Outcome = OutcomeInsufficient
			} else {
				record.Outcome = OutcomeAssembled
			}
		} else if IsCategory(err, CategoryCancelled) || IsCategory(err, CategoryTimeout) {
			record.Outcome = OutcomeCancelled
		}
		observeCtx, cancel := context.WithTimeout(context.Background(), a.config.ObserverTimeout)
		defer cancel()
		if observeErr := a.deps.Observer.Observe(observeCtx, record); observeErr != nil && err == nil {
			snapshot = Snapshot{}
			err = contextFailure("observe", observeErr)
		}
	}()
	if ctx.Err() != nil {
		return Snapshot{}, contextFailureFromContext("preflight", ctx)
	}
	request = cloneRequest(request)
	if validateErr := validateRequest(request); validateErr != nil {
		return Snapshot{}, validateErr
	}
	profile, ok := a.deps.Profiles.Profile(request.Role)
	if !ok || profile.Role != request.Role || profile.Version != request.Authority.ProfileVersion || profile.InstructionVersion != request.Authority.InstructionVersion || !validBudgets(profile.Budgets) {
		return Snapshot{}, &Error{Category: CategoryUnauthorized, Stage: "profile", Path: "role"}
	}
	effective, budgetErr := effectiveBudgets(request.Limits, profile.Budgets)
	if budgetErr != nil || request.MaxSensitivity > profile.MaxSensitivity {
		return Snapshot{}, &Error{Category: CategoryUnauthorized, Stage: "profile", Path: "limits"}
	}
	request.Limits = effective
	workCtx, workCancel := context.WithTimeout(ctx, time.Duration(effective.MaxDurationMS)*time.Millisecond)
	defer workCancel()
	ctx = workCtx
	wanted, sourceErr := requestedSources(request.SourceKinds, profile)
	if sourceErr != nil {
		return Snapshot{}, sourceErr
	}
	if len(wanted) > effective.MaxSources {
		return Snapshot{}, &Error{Category: CategoryUnauthorized, Stage: "profile", Path: "source_count"}
	}
	if fieldErr := validateFieldNarrowing(request.Fields, wanted, profile); fieldErr != nil {
		return Snapshot{}, fieldErr
	}
	projections := make([]Projection, 0, len(wanted))
	omissions := make([]Omission, 0)
	bindings := make([]SourceBinding, 0, len(wanted))
	candidateCount := 0
	for _, kind := range wanted {
		if ctx.Err() != nil {
			return Snapshot{}, contextFailureFromContext("source", ctx)
		}
		producer := a.producers[kind]
		if producer == nil {
			if profile.MandatorySources[kind] {
				omissions = append(omissions, Omission{ID: string(kind), Kind: kind, Reason: OmissionSourceMissing})
			}
			continue
		}
		projection, produceErr := producer.Produce(ctx, request)
		if produceErr != nil {
			return Snapshot{}, contextFailure("source", produceErr)
		}
		if ctx.Err() != nil {
			return Snapshot{}, contextFailureFromContext("source", ctx)
		}
		detached, projectionErr := validateProjection(projection, request, profile, kind)
		if projectionErr != nil {
			return Snapshot{}, projectionErr
		}
		projections = append(projections, detached)
		bindings = append(bindings, detached.Binding)
		candidateCount += len(detached.Items)
		if candidateCount > effective.MaxCandidates {
			return Snapshot{}, &Error{Category: CategoryInvalid, Stage: "source", Path: "candidate_count"}
		}
	}
	candidates, conflicts, filterOmissions, filterErr := a.filter(ctx, request, profile, projections, started.UTC())
	if filterErr != nil {
		return Snapshot{}, filterErr
	}
	omissions = append(omissions, filterOmissions...)
	if ctx.Err() != nil {
		return Snapshot{}, contextFailureFromContext("filter", ctx)
	}
	ordered, rankErr := a.rank(ctx, request.Role, candidates, effective.MaxRankingWork, profile.AllowRankFallback)
	if rankErr != nil {
		return Snapshot{}, rankErr
	}
	if ctx.Err() != nil {
		return Snapshot{}, contextFailureFromContext("rank", ctx)
	}
	snapshot, err = a.pack(ctx, request, profile, effective, ordered, bindings, conflicts, omissions)
	if err != nil {
		return Snapshot{}, err
	}
	if ctx.Err() != nil {
		return Snapshot{}, contextFailureFromContext("pack", ctx)
	}
	committed, created, commitErr := a.deps.Store.CommitSnapshot(ctx, request.IdempotencyKey, request, snapshot)
	if commitErr != nil {
		if ctx.Err() != nil || errors.Is(commitErr, context.Canceled) || errors.Is(commitErr, context.DeadlineExceeded) {
			return Snapshot{}, contextFailure("store", commitErr)
		}
		return Snapshot{}, commitErr
	}
	emit = created
	return committed, nil
}

// validateFieldNarrowing rejects caller fields outside both selected sources and sealed profile authority.
func validateFieldNarrowing(requested map[SourceKind][]string, wanted []SourceKind, profile Profile) error {
	selected := make(map[SourceKind]bool, len(wanted))
	for _, kind := range wanted {
		selected[kind] = true
	}
	for kind, fields := range requested {
		if !selected[kind] {
			return &Error{Category: CategoryUnauthorized, Stage: "profile", Path: "fields.source"}
		}
		for _, field := range fields {
			if !profile.AllowedFields[kind][field] {
				return &Error{Category: CategoryUnauthorized, Stage: "profile", Path: "fields." + string(kind)}
			}
		}
	}
	return nil
}

// effectiveBudgets enforces caller narrowing across every dimension.
func effectiveBudgets(requested, trusted Budgets) (Budgets, error) {
	if requested.MaxSources == 0 {
		requested.MaxSources = trusted.MaxSources
	}
	if requested.MaxCandidates == 0 {
		requested.MaxCandidates = trusted.MaxCandidates
	}
	if requested.MaxPayloadBytes == 0 {
		requested.MaxPayloadBytes = trusted.MaxPayloadBytes
	}
	if requested.MaxDurationMS == 0 {
		requested.MaxDurationMS = trusted.MaxDurationMS
	}
	if !validBudgets(requested) || requested.MaxItems > trusted.MaxItems || requested.MaxBytes > trusted.MaxBytes || requested.MaxTokens > trusted.MaxTokens || requested.MaxRankingWork > trusted.MaxRankingWork || requested.MaxSources > trusted.MaxSources || requested.MaxCandidates > trusted.MaxCandidates || requested.MaxPayloadBytes > trusted.MaxPayloadBytes || requested.MaxDurationMS > trusted.MaxDurationMS {
		return Budgets{}, &Error{Category: CategoryUnauthorized, Stage: "profile", Path: "budgets"}
	}
	return requested, nil
}

// validBudgets ensures no work dimension is unbounded.
func validBudgets(value Budgets) bool {
	return value.MaxItems > 0 && value.MaxBytes > 0 && value.MaxTokens > 0 && value.MaxRankingWork > 0 && value.MaxSources > 0 && value.MaxCandidates > 0 && value.MaxPayloadBytes > 0 && value.MaxDurationMS > 0
}

// requestedSources intersects caller narrowing with the sealed role profile.
func requestedSources(requested []SourceKind, profile Profile) ([]SourceKind, error) {
	if len(requested) == 0 {
		for kind := range profile.AllowedSources {
			requested = append(requested, kind)
		}
	}
	result := append([]SourceKind(nil), requested...)
	for _, kind := range result {
		if !profile.AllowedSources[kind] {
			return nil, &Error{Category: CategoryUnauthorized, Stage: "profile", Path: "source_kinds"}
		}
	}
	for kind := range profile.MandatorySources {
		found := false
		for _, selected := range result {
			found = found || selected == kind
		}
		if !found {
			return nil, &Error{Category: CategoryUnauthorized, Stage: "profile", Path: "mandatory_sources"}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

// validateProjection confirms producer identity, source digest and detached item payloads.
func validateProjection(value Projection, request Request, profile Profile, kind SourceKind) (Projection, error) {
	if len(value.Items) > request.Limits.MaxCandidates && request.Limits.MaxCandidates > 0 {
		return Projection{}, &Error{Category: CategoryInvalid, Stage: "source", Path: "projection.candidates"}
	}
	if value.Version != SnapshotVersion || value.Binding.Kind != kind || !safeIdentifier.MatchString(value.Binding.Producer) || !safeIdentifier.MatchString(value.Binding.ProducerVersion) || !digestPattern.MatchString(value.Binding.Digest) || (value.Binding.Completeness != CompletenessComplete && value.Binding.Completeness != CompletenessPartial && value.Binding.Completeness != CompletenessInsufficient) {
		return Projection{}, &Error{Category: CategoryInvalid, Stage: "source", Path: "projection.binding"}
	}
	detached := Projection{Version: value.Version, Binding: value.Binding, Conflicts: append([]Conflict(nil), value.Conflicts...)}
	if value.Items != nil {
		detached.Items = make([]Item, len(value.Items))
	}
	seen := map[string]bool{}
	for index, item := range value.Items {
		if item.Kind != kind || item.Trust != expectedSourceTrust(kind) || item.Redaction != RedactionVerified || !safeIdentifier.MatchString(item.ID) || seen[item.ID] || !safeReference(item.SafeReference) || !digestPattern.MatchString(item.Digest) || !validAuthority(item.Authority) || !safeIdentifier.MatchString(item.ProjectID) || !safeIdentifier.MatchString(item.RepositoryID) || item.Sensitivity < 0 || (request.Limits.MaxPayloadBytes > 0 && len(item.Payload) > request.Limits.MaxPayloadBytes) {
			return Projection{}, &Error{Category: CategoryInvalid, Stage: "source", Path: "projection.items"}
		}
		payload, payloadErr := canonicalPayload(item.Payload)
		if payloadErr != nil {
			return Projection{}, payloadErr
		}
		item.Payload = append(json.RawMessage(nil), payload...)
		if fieldErr := validatePayloadFields(item.Payload, kind, request.Fields[kind], profile.AllowedFields[kind]); fieldErr != nil {
			return Projection{}, fieldErr
		}
		want, digestErr := digestItem(item)
		if digestErr != nil || want != item.Digest {
			return Projection{}, &Error{Category: CategoryInvalid, Stage: "source", Path: "projection.items.digest"}
		}
		detached.Items[index], seen[item.ID] = item, true
	}
	want, digestErr := digestProjection(detached)
	if digestErr != nil || want != value.Binding.Digest {
		return Projection{}, &Error{Category: CategoryInvalid, Stage: "source", Path: "projection.binding.digest"}
	}
	return detached, nil
}

// validatePayloadFields enforces composition allowlists and caller narrowing on top-level projected fields.
func validatePayloadFields(payload json.RawMessage, kind SourceKind, requested []string, allowed map[string]bool) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil || object == nil || len(allowed) == 0 {
		return &Error{Category: CategoryUnauthorized, Stage: "source", Path: "projection.fields"}
	}
	narrowed := allowed
	if len(requested) > 0 {
		narrowed = make(map[string]bool, len(requested))
		for _, field := range requested {
			if !allowed[field] {
				return &Error{Category: CategoryUnauthorized, Stage: "profile", Path: "fields." + string(kind)}
			}
			narrowed[field] = true
		}
	}
	for field := range object {
		if !narrowed[field] {
			return &Error{Category: CategoryUnauthorized, Stage: "source", Path: "projection.fields"}
		}
	}
	return nil
}

// digestItem derives an item digest without trusting its declared identity.
func digestItem(item Item) (string, error) { item.Digest = ""; return digestValue(item) }

// digestProjection derives a source digest without trusting its declared digest.
func digestProjection(value Projection) (string, error) {
	value.Binding.Digest = ""
	sort.Slice(value.Items, func(i, j int) bool { return sourceTuple(value.Items[i]) < sourceTuple(value.Items[j]) })
	return digestValue(value)
}

// filter applies every structural authority gate before ranking or tokenization.
func (a *Assembler) filter(ctx context.Context, request Request, profile Profile, projections []Projection, now time.Time) ([]Item, []Conflict, []Omission, error) {
	var eligible []Item
	var omissions []Omission
	logical := map[string]map[string][]string{}
	digests := map[string]bool{}
	declared := map[string][]string{}
	allIDs := map[string]bool{}
	for _, projection := range projections {
		for _, conflict := range projection.Conflicts {
			if !safeIdentifier.MatchString(conflict.LogicalKey) || len(conflict.ItemIDs) < 2 {
				return nil, nil, nil, &Error{Category: CategoryInvalid, Stage: "source", Path: "projection.conflicts"}
			}
			declared[conflict.LogicalKey] = append(declared[conflict.LogicalKey], conflict.ItemIDs...)
		}
		for _, item := range projection.Items {
			allIDs[item.ID] = true
			if ctx.Err() != nil {
				return nil, nil, nil, contextFailureFromContext("filter", ctx)
			}
			reason := eligibilityReason(item, request, profile, now)
			if reason == "" {
				if secretErr := a.deps.Secrets.Check(ctx, item); secretErr != nil {
					if ctx.Err() != nil || errors.Is(secretErr, context.Canceled) || errors.Is(secretErr, context.DeadlineExceeded) {
						return nil, nil, nil, contextFailure("secret_gate", secretErr)
					}
					var gateErr *Error
					if errors.As(secretErr, &gateErr) && gateErr.Category == CategorySecret {
						return nil, nil, nil, &Error{Category: CategorySecret, Stage: "secret_gate", Path: gateErr.Path}
					}
					return nil, nil, nil, &Error{Category: CategorySecret, Stage: "secret_gate", Path: "item." + string(item.Kind)}
				}
				if ctx.Err() != nil {
					return nil, nil, nil, contextFailureFromContext("secret_gate", ctx)
				}
				if digests[item.Digest] {
					reason = OmissionDuplicate
				} else {
					digests[item.Digest] = true
				}
			}
			if reason != "" {
				omissions = append(omissions, Omission{ID: item.ID, Kind: item.Kind, Reason: reason})
				continue
			}
			eligible = append(eligible, item)
			if item.LogicalKey != "" {
				if logical[item.LogicalKey] == nil {
					logical[item.LogicalKey] = map[string][]string{}
				}
				logical[item.LogicalKey][item.ValueDigest] = append(logical[item.LogicalKey][item.ValueDigest], item.ID)
			}
		}
	}
	var conflicts []Conflict
	conflicted := map[string]bool{}
	for key, ids := range declared {
		ids = sortedUniqueStrings(ids)
		for _, id := range ids {
			if !allIDs[id] {
				return nil, nil, nil, &Error{Category: CategoryInvalid, Stage: "source", Path: "projection.conflicts.item_ids"}
			}
		}
		conflicts = append(conflicts, Conflict{LogicalKey: key, ItemIDs: ids})
		for _, id := range ids {
			conflicted[id] = true
		}
	}
	for key, values := range logical {
		if len(values) < 2 {
			continue
		}
		var ids []string
		for _, set := range values {
			ids = append(ids, set...)
		}
		ids = sortedUniqueStrings(ids)
		if _, exists := declared[key]; !exists {
			conflicts = append(conflicts, Conflict{LogicalKey: key, ItemIDs: ids})
		}
		for _, id := range ids {
			conflicted[id] = true
		}
	}
	if len(conflicted) > 0 {
		kept := eligible[:0]
		for _, item := range eligible {
			if conflicted[item.ID] {
				omissions = append(omissions, Omission{ID: item.ID, Kind: item.Kind, Reason: OmissionConflict})
			} else {
				kept = append(kept, item)
			}
		}
		eligible = kept
	}
	sort.Slice(eligible, func(i, j int) bool { return sourceTuple(eligible[i]) < sourceTuple(eligible[j]) })
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].LogicalKey < conflicts[j].LogicalKey })
	return eligible, conflicts, omissions, nil
}

// eligibilityReason returns the first stable structural exclusion reason.
func eligibilityReason(item Item, request Request, profile Profile, now time.Time) OmissionReason {
	if !profile.AllowedSources[item.Kind] {
		return OmissionRole
	}
	if item.ProjectID != request.Authority.ProjectID || item.RepositoryID != request.Authority.RepositoryID || (item.RunID != "" && item.RunID != request.RunID) {
		return OmissionScope
	}
	if item.Revision != "" && item.Revision != request.Authority.Revision {
		return OmissionRevision
	}
	if item.State != "active" && item.State != "approved" && item.State != "verified" {
		return OmissionState
	}
	if item.Sensitivity > request.MaxSensitivity || item.Sensitivity > profile.MaxSensitivity {
		return OmissionSensitivity
	}
	if authorityLevel(item.Authority) < requiredAuthority(item.Kind) {
		return OmissionAuthority
	}
	if item.ValidUntil != nil && !item.ValidUntil.After(now) {
		return OmissionValidity
	}
	if item.Trust != expectedSourceTrust(item.Kind) || item.Redaction != RedactionVerified {
		return OmissionAuthority
	}
	return ""
}

// authorityLevel orders domain provenance without allowing ranking or recency to grant authority.
func authorityLevel(value Authority) int {
	switch value {
	case AuthorityComposition:
		return 4
	case AuthorityApproved:
		return 3
	case AuthorityVerified:
		return 2
	case AuthorityObserved:
		return 1
	}
	return 0
}

// requiredAuthority returns the minimum sealed provenance for each neutral source kind.
func requiredAuthority(kind SourceKind) int {
	switch kind {
	case SourceCapability:
		return 4
	case SourceConfig, SourceMemory:
		return 3
	case SourceConstraints, SourceContract, SourceDiff, SourcePlan, SourceEvidence:
		return 2
	case SourceHistory, SourceDiagnostic:
		return 1
	}
	return 5
}

// rank validates that optional derived ranking is a permutation of eligible candidates.
func (a *Assembler) rank(ctx context.Context, role Role, candidates []Item, work int, fallback bool) ([]Item, error) {
	if a.deps.Ranker == nil {
		return append([]Item(nil), candidates...), nil
	}
	inputs := make([]RankInput, len(candidates))
	byID := make(map[string]Item, len(candidates))
	for index, item := range candidates {
		inputs[index] = RankInput{ID: item.ID, Kind: item.Kind, SafeReference: item.SafeReference, Digest: item.Digest}
		byID[item.ID] = item
	}
	ranked, err := a.deps.Ranker.Rank(ctx, role, inputs, work)
	if err != nil {
		var unavailable *RankUnavailableError
		if fallback && errors.As(err, &unavailable) && (unavailable.State == "missing" || unavailable.State == "stale" || unavailable.State == "corrupt") {
			return append([]Item(nil), candidates...), nil
		}
		return nil, contextFailure("rank", err)
	}
	if ctx.Err() != nil {
		return nil, contextFailureFromContext("rank", ctx)
	}
	if len(ranked) != len(candidates) {
		return nil, &Error{Category: CategoryInvalid, Stage: "rank", Path: "cardinality"}
	}
	seen := map[string]bool{}
	for _, value := range ranked {
		if seen[value.ID] || byID[value.ID].ID == "" || math.IsNaN(value.Score) || math.IsInf(value.Score, 0) {
			return nil, &Error{Category: CategoryInvalid, Stage: "rank", Path: "result"}
		}
		seen[value.ID] = true
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score == ranked[j].Score {
			return sourceTuple(byID[ranked[i].ID]) < sourceTuple(byID[ranked[j].ID])
		}
		return ranked[i].Score > ranked[j].Score
	})
	result := make([]Item, len(ranked))
	for index, value := range ranked {
		result[index] = byID[value.ID]
	}
	return result, nil
}

// pack reserves trusted guards and adds only complete items while confirming exact byte and token counts.
func (a *Assembler) pack(ctx context.Context, request Request, profile Profile, budgets Budgets, candidates []Item, bindings []SourceBinding, conflicts []Conflict, omissions []Omission) (Snapshot, error) {
	instruction := Item{ID: "instructions", Kind: SourceConfig, Trust: TrustInstructions, Redaction: RedactionVerified, Authority: AuthorityComposition, SafeReference: "composition:instructions", ProjectID: request.Authority.ProjectID, RepositoryID: request.Authority.RepositoryID, Revision: request.Authority.Revision, Scope: "project", State: "active", Payload: append(json.RawMessage(nil), profile.Instructions...)}
	instructionDigest, err := digestItem(instruction)
	if err != nil {
		return Snapshot{}, &Error{Category: CategoryInvalid, Stage: "pack", Path: "instructions"}
	}
	instruction.Digest = instructionDigest
	selected := []Item{instruction}
	remaining := make([]Item, 0, len(candidates))
	for _, item := range candidates {
		if item.Kind == SourceConfig {
			selected = append(selected, item)
		} else {
			remaining = append(remaining, item)
		}
	}
	for {
		if ctx.Err() != nil {
			return Snapshot{}, contextFailureFromContext("pack", ctx)
		}
		candidate := buildSnapshot(request, profile, budgets, selected, bindings, conflicts, omissions)
		measured, measureErr := a.measureSnapshot(ctx, candidate)
		if measureErr != nil {
			return Snapshot{}, measureErr
		}
		fits := len(selected) <= budgets.MaxItems && measured.Counts.Bytes <= budgets.MaxBytes && measured.Counts.Tokens <= budgets.MaxTokens
		if !fits {
			if len(selected) == 1 {
				candidate.Completeness = CompletenessInsufficient
				return a.measureSnapshot(ctx, candidate)
			}
			last := selected[len(selected)-1]
			selected = selected[:len(selected)-1]
			omissions = append(omissions, Omission{ID: last.ID, Kind: last.Kind, Reason: OmissionBudget})
			continue
		}
		if len(remaining) == 0 {
			return measured, nil
		}
		next := remaining[0]
		remaining = remaining[1:]
		if len(selected)+1 > budgets.MaxItems {
			omissions = append(omissions, Omission{ID: next.ID, Kind: next.Kind, Reason: OmissionBudget})
			continue
		}
		selected = append(selected, next)
	}
}

// measureSnapshot converges self-described byte/token counts before sealing the final canonical digest.
func (a *Assembler) measureSnapshot(ctx context.Context, snapshot Snapshot) (Snapshot, error) {
	for attempt := 0; attempt < 8; attempt++ {
		snapshot.Digest = ""
		digest, err := digestValue(snapshot)
		if err != nil {
			return Snapshot{}, &Error{Category: CategoryInvalid, Stage: "canonicalize", Path: "snapshot"}
		}
		snapshot.Digest = digest
		envelope, envelopeErr := Envelope(snapshot)
		if envelopeErr != nil {
			return Snapshot{}, envelopeErr
		}
		untrusted, err := canonicalJSON(envelope)
		if err != nil {
			return Snapshot{}, &Error{Category: CategoryInvalid, Stage: "canonicalize", Path: "snapshot"}
		}
		modelFacing, err := canonicalJSON(struct {
			Instructions string          `json:"instructions"`
			Untrusted    json.RawMessage `json:"untrusted"`
		}{Instructions: envelope.Instructions, Untrusted: untrusted})
		if err != nil {
			return Snapshot{}, &Error{Category: CategoryInvalid, Stage: "canonicalize", Path: "model_facing"}
		}
		tokens, err := a.deps.Tokens.Count(ctx, snapshot.Authority.Provider, snapshot.Authority.Model, modelFacing)
		if err != nil {
			return Snapshot{}, contextFailure("token_count", err)
		}
		if ctx.Err() != nil {
			return Snapshot{}, contextFailureFromContext("token_count", ctx)
		}
		if snapshot.Counts.Bytes == len(modelFacing) && snapshot.Counts.Tokens == tokens {
			return cloneSnapshot(snapshot), nil
		}
		snapshot.Counts.Bytes, snapshot.Counts.Tokens = len(modelFacing), tokens
	}
	return Snapshot{}, &Error{Category: CategoryInvalid, Stage: "canonicalize", Path: "counts"}
}

// buildSnapshot creates canonical sections and content-free manifests for one tentative packing set.
func buildSnapshot(request Request, profile Profile, budgets Budgets, selected []Item, bindings []SourceBinding, conflicts []Conflict, omissions []Omission) Snapshot {
	sectionItems := map[TrustClass][]Item{TrustInstructions: {}, TrustConfiguration: {}, TrustUntrusted: {}}
	included := make([]ManifestEntry, 0, len(selected))
	for _, item := range selected {
		copyItem := item
		copyItem.Payload = append(json.RawMessage(nil), item.Payload...)
		sectionItems[item.Trust] = append(sectionItems[item.Trust], copyItem)
		included = append(included, ManifestEntry{ID: item.ID, Digest: item.Digest, SafeReference: item.SafeReference, Kind: item.Kind, Authority: item.Authority})
	}
	sections := make([]Section, 0, 3)
	for _, trust := range []TrustClass{TrustInstructions, TrustConfiguration, TrustUntrusted} {
		items := sectionItems[trust]
		sort.Slice(items, func(i, j int) bool { return sourceTuple(items[i]) < sourceTuple(items[j]) })
		sections = append(sections, Section{Trust: trust, Items: items})
	}
	sort.Slice(included, func(i, j int) bool {
		if included[i].Kind == included[j].Kind {
			return included[i].ID < included[j].ID
		}
		return included[i].Kind < included[j].Kind
	})
	sort.Slice(omissions, func(i, j int) bool {
		if omissions[i].Kind == omissions[j].Kind {
			if omissions[i].ID == omissions[j].ID {
				return omissions[i].Reason < omissions[j].Reason
			}
			return omissions[i].ID < omissions[j].ID
		}
		return omissions[i].Kind < omissions[j].Kind
	})
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].Kind < bindings[j].Kind })
	completeness := CompletenessComplete
	for _, binding := range bindings {
		if binding.Completeness == CompletenessPartial {
			completeness = CompletenessPartial
		}
		if binding.Completeness == CompletenessInsufficient {
			completeness = CompletenessInsufficient
		}
	}
	includedKinds := map[SourceKind]bool{}
	for _, item := range selected {
		if item.Trust != TrustInstructions {
			includedKinds[item.Kind] = true
		}
	}
	for kind := range profile.MandatorySources {
		if !includedKinds[kind] {
			completeness = CompletenessInsufficient
		}
	}
	for _, omission := range omissions {
		if omission.Reason == OmissionSourceMissing || (omission.Reason == OmissionConflict && profile.ConflictBlocks[omission.Kind]) || profile.RequiredComplete[omission.Kind] {
			completeness = CompletenessInsufficient
		} else if completeness == CompletenessComplete {
			completeness = CompletenessPartial
		}
	}
	registry, _ := registryDescriptor(request.Authority, bindings)
	return Snapshot{Version: SnapshotVersion, RunID: request.RunID, PlanID: request.PlanID, Role: request.Role, Authority: request.Authority, Budgets: budgets, Sections: sections, Included: included, Omitted: append([]Omission(nil), omissions...), Conflicts: append([]Conflict(nil), conflicts...), SourceBindings: append([]SourceBinding(nil), bindings...), Registry: registry, Completeness: completeness, Counts: Counts{Candidates: len(selected) + len(omissions), Eligible: len(selected), Included: len(selected), Omitted: len(omissions), Conflicts: len(conflicts)}}
}

// registryDescriptor seals profile, instruction, authority and producer identities for stateless restart revalidation.
func registryDescriptor(authority AuthorityBinding, bindings []SourceBinding) (RegistryDescriptor, error) {
	authorityID, err := digestValue(authority)
	if err != nil {
		return RegistryDescriptor{}, err
	}
	descriptor := RegistryDescriptor{Version: RegistryVersion, ProfileVersion: authority.ProfileVersion, InstructionVersion: authority.InstructionVersion, AuthorityID: authorityID, Sources: make([]RegistrySource, len(bindings))}
	for index, binding := range bindings {
		descriptor.Sources[index] = RegistrySource{Kind: binding.Kind, Producer: binding.Producer, ProducerVersion: binding.ProducerVersion, AuthorityID: authorityID}
	}
	sort.Slice(descriptor.Sources, func(i, j int) bool { return descriptor.Sources[i].Kind < descriptor.Sources[j].Kind })
	descriptor.Digest, err = registryDescriptorDigest(descriptor)
	return descriptor, err
}

// registryDescriptorDigest hashes the canonical descriptor without trusting its embedded digest.
func registryDescriptorDigest(descriptor RegistryDescriptor) (string, error) {
	descriptor.Digest = ""
	return digestValue(descriptor)
}

// RevalidationProducers validates the durable descriptor and resolves each producer from current composition authority.
func RevalidationProducers(ctx context.Context, snapshot Snapshot, registry ProducerRegistry) ([]Producer, error) {
	want, err := registryDescriptor(snapshot.Authority, snapshot.SourceBindings)
	if registry == nil || err != nil || want.Digest != snapshot.Registry.Digest || want.Version != snapshot.Registry.Version || want.ProfileVersion != snapshot.Registry.ProfileVersion || want.InstructionVersion != snapshot.Registry.InstructionVersion || want.AuthorityID != snapshot.Registry.AuthorityID || len(want.Sources) != len(snapshot.Registry.Sources) {
		return nil, &Error{Category: CategoryPersistence, Stage: "registry", Path: "descriptor"}
	}
	for index := range want.Sources {
		if want.Sources[index] != snapshot.Registry.Sources[index] {
			return nil, &Error{Category: CategoryPersistence, Stage: "registry", Path: "source"}
		}
	}
	producers, err := registry.Resolve(ctx, snapshot.RunID, cloneRegistryDescriptor(snapshot.Registry), snapshot.Authority)
	if err != nil {
		return nil, err
	}
	resolved := make(map[SourceKind]Producer, len(producers))
	for _, producer := range producers {
		if producer == nil || resolved[producer.Kind()] != nil || producer.TrustClass() != expectedSourceTrust(producer.Kind()) {
			return nil, &Error{Category: CategoryConflict, Stage: "registry", Path: "producer"}
		}
		resolved[producer.Kind()] = producer
	}
	if len(resolved) != len(snapshot.Registry.Sources) {
		return nil, &Error{Category: CategoryConflict, Stage: "registry", Path: "producer_count"}
	}
	for _, source := range snapshot.Registry.Sources {
		producer := resolved[source.Kind]
		if producer == nil {
			return nil, &Error{Category: CategoryConflict, Stage: "registry", Path: "producer_missing"}
		}
		name, version := producer.Identity()
		if name != source.Producer || version != source.ProducerVersion || source.AuthorityID != snapshot.Registry.AuthorityID {
			return nil, &Error{Category: CategoryConflict, Stage: "registry", Path: "producer_identity"}
		}
	}
	return producers, nil
}

// expectedSourceTrust assigns trust exclusively from composition and never from producer-supplied authority.
func expectedSourceTrust(kind SourceKind) TrustClass {
	if kind == SourceConfig {
		return TrustConfiguration
	}
	return TrustUntrusted
}

// cloneSnapshot detaches every mutable collection before crossing package boundaries.
func cloneSnapshot(value Snapshot) Snapshot {
	result := value
	result.Sections = make([]Section, len(value.Sections))
	for index, section := range value.Sections {
		result.Sections[index].Trust = section.Trust
		result.Sections[index].Items = make([]Item, len(section.Items))
		for itemIndex, item := range section.Items {
			item.Payload = append(json.RawMessage(nil), item.Payload...)
			result.Sections[index].Items[itemIndex] = item
		}
	}
	result.Included = append([]ManifestEntry(nil), value.Included...)
	result.Omitted = append([]Omission(nil), value.Omitted...)
	result.SourceBindings = append([]SourceBinding(nil), value.SourceBindings...)
	result.Registry.Sources = append([]RegistrySource(nil), value.Registry.Sources...)
	if value.Conflicts != nil {
		result.Conflicts = make([]Conflict, len(value.Conflicts))
		for index, conflict := range value.Conflicts {
			result.Conflicts[index] = Conflict{LogicalKey: conflict.LogicalKey, ItemIDs: append([]string(nil), conflict.ItemIDs...)}
		}
	}
	return result
}

// Bind seals a persisted snapshot to one exact prepared plan identity.
func (a *Assembler) Bind(ctx context.Context, key, planID, preparedPlanDigest, contextDigest string) (result ContextBinding, err error) {
	started := a.deps.Clock.Now()
	emit := true
	defer func() {
		if !emit {
			return
		}
		outcome := OutcomeFailed
		if err == nil {
			outcome = OutcomeAssembled
		}
		record := Record{RunID: result.RunID, Role: result.Role, Digest: result.Digest, Outcome: outcome, Duration: a.deps.Clock.Now().Sub(started)}
		observeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.config.ObserverTimeout)
		defer cancel()
		if observeErr := a.deps.Observer.Observe(observeCtx, record); observeErr != nil && err == nil {
			result = ContextBinding{}
			err = contextFailure("observe", observeErr)
		}
	}()
	if !safeIdentifier.MatchString(key) || !safeIdentifier.MatchString(planID) || !digestPattern.MatchString(preparedPlanDigest) || !digestPattern.MatchString(contextDigest) {
		return ContextBinding{}, &Error{Category: CategoryInvalid, Stage: "bind", Path: "identity"}
	}
	snapshot, err := a.deps.Store.LoadSnapshot(ctx, contextDigest)
	if err != nil || snapshot.Digest != contextDigest {
		return ContextBinding{}, &Error{Category: CategoryPersistence, Stage: "bind", Path: "snapshot"}
	}
	binding := ContextBinding{Version: BindingVersion, RunID: snapshot.RunID, Role: snapshot.Role, PlanID: planID, PreparedPlanDigest: preparedPlanDigest, ContextDigest: contextDigest, Authority: snapshot.Authority, SourceBindings: append([]SourceBinding(nil), snapshot.SourceBindings...), Registry: cloneRegistryDescriptor(snapshot.Registry)}
	binding.Digest, err = bindingDigest(binding)
	if err != nil {
		return ContextBinding{}, &Error{Category: CategoryInvalid, Stage: "bind", Path: "digest"}
	}
	committed, created, commitErr := a.deps.Store.CommitBinding(ctx, key, binding)
	emit, result, err = created, committed, commitErr
	return result, err
}

// bindingDigest hashes a content-free plan/context seal without trusting its self digest.
func bindingDigest(value ContextBinding) (string, error) {
	value.Digest = ""
	value.SourceBindings = append([]SourceBinding(nil), value.SourceBindings...)
	value.Registry = cloneRegistryDescriptor(value.Registry)
	sort.Slice(value.SourceBindings, func(i, j int) bool { return value.SourceBindings[i].Kind < value.SourceBindings[j].Kind })
	sort.Slice(value.Registry.Sources, func(i, j int) bool { return value.Registry.Sources[i].Kind < value.Registry.Sources[j].Kind })
	return digestValue(value)
}

// cloneRegistryDescriptor detaches the durable source registry from caller-owned slices.
func cloneRegistryDescriptor(value RegistryDescriptor) RegistryDescriptor {
	value.Sources = append([]RegistrySource(nil), value.Sources...)
	return value
}

// Revalidate verifies identity, digest and every current producer authority before approval or execution.
func (a *Assembler) Revalidate(ctx context.Context, binding ContextBinding) (result RevalidationResult, err error) {
	started := a.deps.Clock.Now()
	defer func() {
		outcome := OutcomeFailed
		if err == nil {
			outcome = result.Outcome
		}
		record := Record{RunID: binding.RunID, Role: binding.Role, Digest: binding.Digest, Outcome: outcome, Duration: a.deps.Clock.Now().Sub(started)}
		observeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.config.ObserverTimeout)
		defer cancel()
		if observeErr := a.deps.Observer.Observe(observeCtx, record); observeErr != nil && err == nil {
			result = RevalidationResult{}
			err = contextFailure("observe", observeErr)
		}
	}()
	want, err := bindingDigest(binding)
	registry, registryErr := registryDescriptor(binding.Authority, binding.SourceBindings)
	if err != nil || registryErr != nil || binding.Version != BindingVersion || want != binding.Digest || !validAuthorityBinding(binding.Authority) || registry.Digest != binding.Registry.Digest {
		return RevalidationResult{}, &Error{Category: CategoryInvalid, Stage: "revalidate", Path: "binding"}
	}
	var reasons []string
	for _, source := range binding.SourceBindings {
		if ctx.Err() != nil {
			return RevalidationResult{}, contextFailureFromContext("revalidate", ctx)
		}
		producer := a.producers[source.Kind]
		if producer == nil {
			reasons = append(reasons, "source_missing:"+string(source.Kind))
			continue
		}
		valid, revalidateErr := producer.Revalidate(ctx, source, binding.Authority)
		if revalidateErr != nil {
			return RevalidationResult{}, contextFailure("revalidate", revalidateErr)
		}
		if !valid {
			reasons = append(reasons, "source_stale:"+string(source.Kind))
		}
	}
	sort.Strings(reasons)
	if len(reasons) > 0 {
		return RevalidationResult{Outcome: OutcomeStale, Reasons: reasons}, nil
	}
	return RevalidationResult{Outcome: OutcomeValid}, nil
}

// Consume gives a role-specific detached snapshot to a controlled non-executable consumer.
func (a *Assembler) Consume(ctx context.Context, request Request, consumer Consumer) error {
	if consumer == nil {
		return &Error{Category: CategoryInvalid, Stage: "consume", Path: "consumer"}
	}
	snapshot, err := a.Assemble(ctx, request)
	if err != nil {
		return err
	}
	if snapshot.Completeness == CompletenessInsufficient {
		return &Error{Category: CategoryConflict, Stage: "consume", Path: "insufficient_context"}
	}
	return consumer.Consume(ctx, cloneSnapshot(snapshot))
}

// Consumer receives only a detached role snapshot and no store, producer or executable handle.
type Consumer interface {
	Consume(context.Context, Snapshot) error
}
