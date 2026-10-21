package contractcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// SourceDescriptor supplies caller-owned trusted metadata and bounded opaque input for sealing before registry use.
type SourceDescriptor struct {
	Kind           string
	ID             string
	Class          SourceClass
	Confidence     Confidence
	Completeness   Completeness
	ExpectedDigest string
	AdapterVersion string
	Input          any
	Limits         Limits
	Projection     ProjectionPolicy
}

// SourceRequest is an immutable sealed descriptor whose authority cannot alias caller or adapter slices.
type SourceRequest struct{ authority sourceAuthority }

// sourceAuthority owns the private descriptor copy used for prevalidation and post-adapter verification.
type sourceAuthority struct {
	Kind           string
	ID             string
	Class          SourceClass
	Confidence     Confidence
	Completeness   Completeness
	ExpectedDigest string
	AdapterVersion string
	Input          any
	Limits         Limits
	Projection     ProjectionPolicy
}

// NewSourceRequest freezes every authority-bearing collection before a descriptor can enter the registry.
func NewSourceRequest(descriptor SourceDescriptor) SourceRequest {
	return SourceRequest{authority: sourceAuthority{Kind: descriptor.Kind, ID: descriptor.ID, Class: descriptor.Class, Confidence: descriptor.Confidence, Completeness: descriptor.Completeness, ExpectedDigest: descriptor.ExpectedDigest, AdapterVersion: descriptor.AdapterVersion, Input: descriptor.Input, Limits: descriptor.Limits, Projection: cloneProjectionPolicy(descriptor.Projection)}}
}

// Kind returns the exact sealed adapter route.
func (r SourceRequest) Kind() string { return r.authority.Kind }

// ID returns the sealed source instance identity.
func (r SourceRequest) ID() string { return r.authority.ID }

// Class returns the epistemic class assigned by trusted composition.
func (r SourceRequest) Class() SourceClass { return r.authority.Class }

// Confidence returns the sealed confidence assigned by trusted composition.
func (r SourceRequest) Confidence() Confidence { return r.authority.Confidence }

// Completeness returns the sealed exhaustiveness assigned by trusted composition.
func (r SourceRequest) Completeness() Completeness { return r.authority.Completeness }

// ExpectedDigest returns the sealed content identity expected from the adapter.
func (r SourceRequest) ExpectedDigest() string { return r.authority.ExpectedDigest }

// AdapterVersion returns the sealed adapter implementation identity.
func (r SourceRequest) AdapterVersion() string { return r.authority.AdapterVersion }

// Input returns the opaque already-authorized adapter input without granting metadata authority.
func (r SourceRequest) Input() any { return r.authority.Input }

// Limits returns the scalar sealed per-source budget.
func (r SourceRequest) Limits() Limits { return r.authority.Limits }

// Projection returns an isolated policy copy so an adapter cannot mutate registry authority.
func (r SourceRequest) Projection() ProjectionPolicy {
	return cloneProjectionPolicy(r.authority.Projection)
}

// cloneProjectionPolicy owns every policy collection independently from caller and adapter mutations.
func cloneProjectionPolicy(policy ProjectionPolicy) ProjectionPolicy {
	return ProjectionPolicy{MaxStringBytes: policy.MaxStringBytes, AllowedURLSchemes: clonePolicyStrings(policy.AllowedURLSchemes), SensitiveNames: clonePolicyStrings(policy.SensitiveNames), SensitivePaths: clonePolicyStrings(policy.SensitivePaths), SensitiveHeaderNames: clonePolicyStrings(policy.SensitiveHeaderNames), ForbiddenValues: clonePolicyStrings(policy.ForbiddenValues)}
}

// clonePolicyStrings preserves the distinction between an explicit empty policy list and an absent list.
func clonePolicyStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

// adapterRequest returns a second sealed copy so even package-local hostile adapters cannot alter validation authority.
func (r SourceRequest) adapterRequest() SourceRequest {
	authority := r.authority
	authority.Projection = cloneProjectionPolicy(authority.Projection)
	return SourceRequest{authority: authority}
}

// ContractSource translates already authorized input without owning network, credentials, tools or processes.
type ContractSource interface {
	Load(context.Context, SourceRequest) (SourceSnapshot, error)
}

// Registry is an immutable exact-kind routing table for passive contract adapters.
type Registry struct{ sources map[string]ContractSource }

// LoadedSources is an opaque sealed batch whose trust metadata was supplied and verified by Registry.
type LoadedSources struct{ snapshots []SourceSnapshot }

// NewRegistry validates and copies explicit source routes so concurrent loads share no mutable caller map.
func NewRegistry(sources map[string]ContractSource) (Registry, error) {
	if len(sources) == 0 {
		return Registry{}, typedError(CategoryInvalidSource, "registry", nil)
	}
	out := map[string]ContractSource{}
	for k, v := range sources {
		if safeLocation(k, 64) != k || v == nil {
			return Registry{}, typedError(CategoryInvalidSource, "registry.kind", nil)
		}
		if _, ok := out[k]; ok {
			return Registry{}, typedError(CategoryInvalidSource, "registry.duplicate", nil)
		}
		out[k] = v
	}
	return Registry{sources: out}, nil
}

// Load invokes sources sequentially with prevalidated trusted metadata and returns no partial collection on failure.
func (r Registry) Load(ctx context.Context, requests []SourceRequest, limits Limits) (LoadedSources, error) {
	if err := validLimits(limits); err != nil {
		return LoadedSources{}, err
	}
	if len(requests) == 0 {
		return LoadedSources{}, typedError(CategoryLimitExceeded, "sources", nil)
	}
	seenRequests := map[string]SourceRequest{}
	digestOwners := map[string]string{}
	unique := make([]SourceRequest, 0, len(requests))
	for i, req := range requests {
		if !validRequest(req, limits) || r.sources[req.Kind()] == nil {
			return LoadedSources{}, typedError(CategoryInvalidSource, fmt.Sprintf("sources[%d]", i), nil)
		}
		if prior, ok := seenRequests[req.ID()]; ok {
			if !sameTrustedRequest(prior, req) {
				return LoadedSources{}, typedError(CategoryInvalidSource, "sources.duplicate", nil)
			}
			continue
		}
		if owner, ok := digestOwners[req.ExpectedDigest()]; ok && owner != req.ID() {
			return LoadedSources{}, typedError(CategoryInvalidSource, "sources.digest", nil)
		}
		seenRequests[req.ID()], digestOwners[req.ExpectedDigest()] = req, req.ID()
		unique = append(unique, req)
	}
	if len(unique) > limits.MaxSources {
		return LoadedSources{}, typedError(CategoryLimitExceeded, "sources", nil)
	}
	out := make([]SourceSnapshot, 0, len(requests))
	aggregate := sourceMetrics{}
	for _, req := range unique {
		if err := ctx.Err(); err != nil {
			return LoadedSources{}, typedError(CategoryCancelled, "sources", err)
		}
		if !aggregate.canReserve(req.Limits(), limits) {
			return LoadedSources{}, typedError(CategoryLimitExceeded, "sources.aggregate", nil)
		}
		adapter := r.sources[req.Kind()]
		snap, err := adapter.Load(ctx, req.adapterRequest())
		if err != nil {
			if ctx.Err() != nil {
				return LoadedSources{}, typedError(CategoryCancelled, "sources", ctx.Err())
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return LoadedSources{}, typedError(CategoryCancelled, "sources.adapter", err)
			}
			var sourceError *Error
			if errors.As(err, &sourceError) {
				return LoadedSources{}, typedError(sourceError.Category, "sources.adapter", sourceError.Cause)
			}
			return LoadedSources{}, typedError(CategoryInvalidSource, "sources.adapter", nil)
		}
		if err = validateSnapshot(ctx, snap, req); err != nil {
			return LoadedSources{}, err
		}
		metrics, metricErr := measureSnapshot(ctx, snap)
		if metricErr != nil {
			return LoadedSources{}, metricErr
		}
		aggregate = aggregate.add(metrics)
		if aggregate.exceeds(limits) {
			return LoadedSources{}, typedError(CategoryLimitExceeded, "sources.aggregate", nil)
		}
		out = append(out, cloneSnapshot(snap))
	}
	return LoadedSources{snapshots: out}, nil
}

// sameTrustedRequest compares every authority-bearing descriptor while deliberately ignoring opaque input identity.
func sameTrustedRequest(a, b SourceRequest) bool {
	return a.Kind() == b.Kind() && a.ID() == b.ID() && a.Class() == b.Class() && a.Confidence() == b.Confidence() && a.Completeness() == b.Completeness() && a.ExpectedDigest() == b.ExpectedDigest() && a.AdapterVersion() == b.AdapterVersion() && reflect.DeepEqual(a.Limits(), b.Limits()) && reflect.DeepEqual(a.authority.Projection, b.authority.Projection)
}

// validLimits rejects every unbounded ingestion dimension before an adapter can run.
func validLimits(v Limits) error {
	if v.MaxSources <= 0 || v.MaxOperations <= 0 || v.MaxClaims <= 0 || v.MaxBytes <= 0 || v.MaxDepth <= 0 || v.MaxDiagnostics <= 0 || v.MaxLocationBytes <= 0 {
		return typedError(CategoryInvalidSource, "limits", nil)
	}
	return nil
}

// validRequest ensures source content cannot choose its own epistemic authority.
func validRequest(v SourceRequest, aggregate Limits) bool {
	return safeLocation(v.Kind(), 64) == v.Kind() && safeLocation(v.ID(), 128) == v.ID() && validSourceClass(v.Class()) && confidenceRank(v.Confidence()) > 0 && completenessRank(v.Completeness()) > 0 && validDigest(v.ExpectedDigest()) && safeLocation(v.AdapterVersion(), 64) == v.AdapterVersion() && validLimits(v.Limits()) == nil && limitsWithin(v.Limits(), aggregate) && validProjectionPolicy(v.authority.Projection)
}

// validateSnapshot binds an adapter result back to trusted request metadata and limits.
func validateSnapshot(ctx context.Context, v SourceSnapshot, req SourceRequest) error {
	if v.Version != VersionV1 || v.Source.ID != req.ID() || v.Source.Kind != req.Kind() || v.Source.Class != req.Class() || v.Source.Confidence != req.Confidence() || v.Source.Completeness != req.Completeness() || v.Source.Digest != req.ExpectedDigest() || v.Source.AdapterVersion != req.AdapterVersion() {
		return typedError(CategoryInvalidSource, "snapshot", nil)
	}
	metrics, metricErr := measureSnapshot(ctx, v)
	if metricErr != nil {
		return metricErr
	}
	if metrics.exceeds(req.Limits()) {
		return typedError(CategoryLimitExceeded, "snapshot", nil)
	}
	if req.Kind() == "review-approved-v1" {
		return validateApprovedReviewSnapshot(ctx, v, req)
	}
	reachableSchemes := map[string]SecurityScheme{}
	operationKeys := map[OperationKey]bool{}
	for i, op := range v.Operations {
		operationKeys[op.Key] = true
		if err := validateProjection(op, req.authority.Projection); err != nil {
			return err
		}
		if op.Key.Method == "" || op.Key.Path == "" || len(op.Responses) == 0 || len(op.Provenance) == 0 || op.Confidence != req.Confidence() || op.Completeness != req.Completeness() || len(op.Classes) != 1 || op.Classes[0] != req.Class() {
			return typedError(CategoryInvalidSource, fmt.Sprintf("operations[%d]", i), nil)
		}
		if len(op.Claims) == 0 {
			return typedError(CategoryInvalidSource, "claims", nil)
		}
		expectedClaims := ClaimsForOperation(op, op.Provenance[0])
		if len(op.Claims) != len(expectedClaims) {
			return typedError(CategoryInvalidSource, "claims", nil)
		}
		expectedClaimCounts := claimIdentityCounts(expectedClaims)
		for _, claim := range op.Claims {
			if claim.Kind == "" || safeLocation(claim.Field, req.Limits().MaxLocationBytes) != claim.Field || claim.Fingerprint == "" || len(claim.Provenance) == 0 || claim.Confidence != req.Confidence() || claim.Completeness != req.Completeness() {
				return typedError(CategoryInvalidSource, "claims", nil)
			}
			for _, provenance := range claim.Provenance {
				if provenance.SourceID != req.ID() || provenance.RepositoryID != v.Source.RepositoryID || provenance.Revision != v.Source.Revision || provenance.Class != req.Class() || provenance.Confidence != req.Confidence() || provenance.Completeness != req.Completeness() || provenance.SourceDigest != req.ExpectedDigest() || provenance.AdapterVersion != req.AdapterVersion() || safeLocation(provenance.SafeLocation, req.Limits().MaxLocationBytes) != provenance.SafeLocation {
					return typedError(CategoryInvalidSource, "claims.provenance", nil)
				}
			}
			identity := claimIdentity(claim)
			if expectedClaimCounts[identity] == 0 {
				return typedError(CategoryInvalidSource, "claims.fingerprint."+claim.Field, nil)
			}
			expectedClaimCounts[identity]--
		}
		for _, remaining := range expectedClaimCounts {
			if remaining != 0 {
				return typedError(CategoryInvalidSource, "claims.missing", nil)
			}
		}
		for _, p := range op.Provenance {
			if p.SourceID != req.ID() || p.RepositoryID != v.Source.RepositoryID || p.Revision != v.Source.Revision || p.Class != req.Class() || p.Confidence != req.Confidence() || p.Completeness != req.Completeness() || p.SourceDigest != req.ExpectedDigest() || p.AdapterVersion != req.AdapterVersion() || safeLocation(p.SafeLocation, req.Limits().MaxLocationBytes) != p.SafeLocation {
				return typedError(CategoryInvalidSource, "provenance", nil)
			}
		}
		if err := collectReachableSecuritySchemes(op, reachableSchemes); err != nil {
			return err
		}
	}
	for _, diagnostic := range v.Diagnostics {
		if safeLocation(diagnostic.Code, 64) != diagnostic.Code || (diagnostic.SafeLocation != "" && safeLocation(diagnostic.SafeLocation, req.Limits().MaxLocationBytes) != diagnostic.SafeLocation) {
			return typedError(CategoryInvalidSource, "diagnostics", nil)
		}
	}
	for _, key := range v.AffectedOperations {
		if !operationKeys[key] {
			return typedError(CategoryInvalidSource, "affected_operations", nil)
		}
	}
	declaredSchemes := map[string]SecurityScheme{}
	for _, scheme := range v.SecuritySchemes {
		if scheme.Name == "" {
			return typedError(CategoryInvalidSource, "security_schemes.name", nil)
		}
		if prior, exists := declaredSchemes[scheme.Name]; exists && !jsonEqual(canonicalSecurityScheme(prior), canonicalSecurityScheme(scheme)) {
			return typedError(CategoryInvalidSource, "security_schemes.duplicate", nil)
		}
		declaredSchemes[scheme.Name] = canonicalSecurityScheme(scheme)
	}
	if !jsonEqual(cloneSecuritySchemes(reachableSchemes), declaredSchemes) {
		return typedError(CategoryInvalidSource, "security_schemes.reachability", nil)
	}
	return nil
}

// validateApprovedReviewSnapshot admits human approval lineage while preserving the original derived confidence and completeness.
func validateApprovedReviewSnapshot(ctx context.Context, snapshot SourceSnapshot, req SourceRequest) error {
	reachableSchemes := map[string]SecurityScheme{}
	operationKeys := map[OperationKey]bool{}
	for index, operation := range snapshot.Operations {
		if err := ctx.Err(); err != nil {
			return typedError(CategoryCancelled, "review_snapshot", err)
		}
		if err := validateProjection(operation, req.authority.Projection); err != nil {
			return err
		}
		operationKeys[operation.Key] = true
		if operation.Key.Method == "" || operation.Key.Path == "" || len(operation.Responses) == 0 || confidenceRank(operation.Confidence) == 0 || completenessRank(operation.Completeness) == 0 || !containsSourceClass(operation.Classes, ClassAuthoritative) || !containsReviewProvenance(operation.Provenance, snapshot.Source, operation.Confidence, operation.Completeness) || !containsOriginalProvenance(operation.Provenance, snapshot.Source) {
			return typedError(CategoryInvalidSource, fmt.Sprintf("operations[%d]", index), nil)
		}
		expected := ClaimsForOperation(operation, operation.Provenance[0])
		fingerprints := map[string]int{}
		for _, claim := range expected {
			fingerprints[string(claim.Kind)+"\x00"+claim.Field+"\x00"+claim.Fingerprint]++
		}
		if len(operation.Claims) != len(expected) {
			return typedError(CategoryInvalidSource, "review_snapshot.claims", nil)
		}
		for _, claim := range operation.Claims {
			identity := string(claim.Kind) + "\x00" + claim.Field + "\x00" + claim.Fingerprint
			if fingerprints[identity] == 0 || confidenceRank(claim.Confidence) == 0 || completenessRank(claim.Completeness) == 0 || safeLocation(claim.Field, req.Limits().MaxLocationBytes) != claim.Field || !containsReviewProvenance(claim.Provenance, snapshot.Source, claim.Confidence, claim.Completeness) || !containsOriginalProvenance(claim.Provenance, snapshot.Source) {
				return typedError(CategoryInvalidSource, "review_snapshot.claims", nil)
			}
			fingerprints[identity]--
			if !validReviewProvenance(claim.Provenance, req.Limits().MaxLocationBytes) {
				return typedError(CategoryInvalidSource, "review_snapshot.claims.provenance", nil)
			}
		}
		if !validReviewProvenance(operation.Provenance, req.Limits().MaxLocationBytes) {
			return typedError(CategoryInvalidSource, "review_snapshot.provenance", nil)
		}
		if err := collectReachableSecuritySchemes(operation, reachableSchemes); err != nil {
			return err
		}
	}
	for _, diagnostic := range snapshot.Diagnostics {
		if safeLocation(diagnostic.Code, 64) != diagnostic.Code || diagnostic.SafeLocation != "" && safeLocation(diagnostic.SafeLocation, req.Limits().MaxLocationBytes) != diagnostic.SafeLocation {
			return typedError(CategoryInvalidSource, "review_snapshot.diagnostics", nil)
		}
	}
	for _, key := range snapshot.AffectedOperations {
		if !operationKeys[key] {
			return typedError(CategoryInvalidSource, "review_snapshot.affected_operations", nil)
		}
	}
	declaredSchemes := map[string]SecurityScheme{}
	for _, scheme := range snapshot.SecuritySchemes {
		if scheme.Name == "" {
			return typedError(CategoryInvalidSource, "review_snapshot.security_schemes.name", nil)
		}
		canonical := canonicalSecurityScheme(scheme)
		if prior, exists := declaredSchemes[scheme.Name]; exists && !jsonEqual(prior, canonical) {
			return typedError(CategoryInvalidSource, "review_snapshot.security_schemes.duplicate", nil)
		}
		declaredSchemes[scheme.Name] = canonical
	}
	if !jsonEqual(cloneSecuritySchemes(reachableSchemes), declaredSchemes) {
		return typedError(CategoryInvalidSource, "review_snapshot.security_schemes.reachability", nil)
	}
	return nil
}

// containsSourceClass verifies that review authority is additive and never rewrites all source classes.
func containsSourceClass(values []SourceClass, expected SourceClass) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

// containsReviewProvenance binds additive human authority to the exact approved projection identity without elevating epistemic fields.
func containsReviewProvenance(values []Provenance, source SourceSummary, confidence Confidence, completeness Completeness) bool {
	for _, value := range values {
		if value.SourceID == source.ID && value.RepositoryID == source.RepositoryID && value.Revision == source.Revision && value.Class == source.Class && value.SourceDigest == source.Digest && value.AdapterVersion == source.AdapterVersion && value.Confidence == confidence && value.Completeness == completeness {
			return true
		}
	}
	return false
}

// containsOriginalProvenance proves approval authority augments rather than replaces the evidence lineage.
func containsOriginalProvenance(values []Provenance, source SourceSummary) bool {
	for _, value := range values {
		if value.SourceID != source.ID {
			return true
		}
	}
	return false
}

// validReviewProvenance validates every retained original and approval lineage entry under the same closed safety rules.
func validReviewProvenance(values []Provenance, maximumLocation int) bool {
	for _, value := range values {
		if value.SourceID == "" || value.RepositoryID == "" || value.Revision == "" || !validSourceClass(value.Class) || !validDigest(value.SourceDigest) || value.AdapterVersion == "" || confidenceRank(value.Confidence) == 0 || completenessRank(value.Completeness) == 0 || safeLocation(value.SafeLocation, maximumLocation) != value.SafeLocation {
			return false
		}
	}
	return len(values) > 0
}

// claimIdentity returns the exact material identity used to compare claimed and recomputed facts as a multiset.
func claimIdentity(claim Claim) string {
	return string(claim.Kind) + "\x00" + claim.Field + "\x00" + claim.Fingerprint
}

// claimIdentityCounts records multiplicity so a duplicated claim cannot conceal a missing sibling claim.
func claimIdentityCounts(claims []Claim) map[string]int {
	counts := make(map[string]int, len(claims))
	for _, claim := range claims {
		counts[claimIdentity(claim)]++
	}
	return counts
}

// collectReachableSecuritySchemes verifies that each operation carries exactly the schemes named by its effective requirements.
func collectReachableSecuritySchemes(operation OperationDescriptor, aggregate map[string]SecurityScheme) error {
	required := map[string]struct{}{}
	for _, alternative := range operation.Security {
		for name := range alternative.Schemes {
			required[name] = struct{}{}
			if _, exists := operation.SecuritySchemes[name]; !exists {
				return typedError(CategoryInvalidSource, "security_schemes.missing", nil)
			}
		}
	}
	if len(required) != len(operation.SecuritySchemes) {
		return typedError(CategoryInvalidSource, "security_schemes.unreachable", nil)
	}
	for name, scheme := range operation.SecuritySchemes {
		canonical := canonicalSecurityScheme(scheme)
		if canonical.Name == "" {
			canonical.Name = name
		}
		if canonical.Name != name {
			return typedError(CategoryInvalidSource, "security_schemes.identity", nil)
		}
		if prior, exists := aggregate[name]; exists && !jsonEqual(prior, canonical) {
			return typedError(CategoryInvalidSource, "security_schemes.conflict", nil)
		}
		aggregate[name] = canonical
	}
	return nil
}

// sourceMetrics records every bounded ingestion dimension before another adapter may start.
type sourceMetrics struct{ sources, operations, claims, bytes, depth, diagnostics int }

// add combines source metrics without retaining partial source state.
func (m sourceMetrics) add(other sourceMetrics) sourceMetrics {
	return sourceMetrics{m.sources + other.sources, m.operations + other.operations, m.claims + other.claims, m.bytes + other.bytes, m.depth + other.depth, m.diagnostics + other.diagnostics}
}

// exceeds checks all aggregate and per-source dimensions.
func (m sourceMetrics) exceeds(l Limits) bool {
	return m.sources > l.MaxSources || m.operations > l.MaxOperations || m.claims > l.MaxClaims || m.bytes > l.MaxBytes || m.depth > l.MaxDepth || m.diagnostics > l.MaxDiagnostics
}

// canReserve prevents starting another adapter when its declared worst-case budget cannot fit the aggregate remainder.
func (m sourceMetrics) canReserve(perSource, aggregate Limits) bool {
	return m.sources+1 <= aggregate.MaxSources && m.operations+perSource.MaxOperations <= aggregate.MaxOperations && m.claims+perSource.MaxClaims <= aggregate.MaxClaims && m.bytes+perSource.MaxBytes <= aggregate.MaxBytes && m.depth+perSource.MaxDepth <= aggregate.MaxDepth && m.diagnostics+perSource.MaxDiagnostics <= aggregate.MaxDiagnostics
}

// measureSnapshot traverses only inert values to determine bounded source cost.

func measureSnapshot(ctx context.Context, v SourceSnapshot) (sourceMetrics, error) {
	depth, err := structuralDepthContext(ctx, reflect.ValueOf(v), map[visit]bool{})
	if err != nil {
		return sourceMetrics{}, typedError(CategoryCancelled, "snapshot", err)
	}
	raw, _ := canonicalJSON(v)
	claims := 0
	for _, op := range v.Operations {
		claims += len(op.Claims)
	}
	return sourceMetrics{sources: 1, operations: len(v.Operations), claims: claims, bytes: len(raw), depth: depth, diagnostics: omissionDiagnostics(v.Omissions) + len(v.Diagnostics)}, nil
}

// structuralDepthContext computes depth while observing cancellation at every collection and field boundary.
func structuralDepthContext(ctx context.Context, value reflect.Value, active map[visit]bool) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !value.IsValid() {
		return 1, nil
	}
	if value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return 1, nil
		}
		depth, err := structuralDepthContext(ctx, value.Elem(), active)
		return 1 + depth, err
	}
	key := visit{kind: value.Kind()}
	if value.Kind() == reflect.Map || value.Kind() == reflect.Slice {
		key.pointer = value.Pointer()
		if active[key] {
			return int(^uint(0) >> 1), nil
		}
		active[key] = true
		defer delete(active, key)
	}
	maximum := 1
	visitChild := func(child reflect.Value) error {
		depth, err := structuralDepthContext(ctx, child, active)
		if err != nil {
			return err
		}
		if 1+depth > maximum {
			maximum = 1 + depth
		}
		return nil
	}
	switch value.Kind() {
	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() {
			if err := visitChild(iterator.Value()); err != nil {
				return 0, err
			}
		}
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := visitChild(value.Index(index)); err != nil {
				return 0, err
			}
		}
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if value.Type().Field(index).IsExported() {
				if err := visitChild(value.Field(index)); err != nil {
					return 0, err
				}
			}
		}
	}
	return maximum, nil
}

// omissionDiagnostics counts safe source diagnostic units without copying collaborator text.
func omissionDiagnostics(v OmissionCounts) int {
	return v.Examples + v.Extensions + v.Documentation + v.Unselected + v.SizeLimit
}

// limitsWithin ensures no per-source dimension can exceed the aggregate budget.
func limitsWithin(a, b Limits) bool {
	return a.MaxSources <= b.MaxSources && a.MaxOperations <= b.MaxOperations && a.MaxClaims <= b.MaxClaims && a.MaxBytes <= b.MaxBytes && a.MaxDepth <= b.MaxDepth && a.MaxDiagnostics <= b.MaxDiagnostics && a.MaxLocationBytes <= b.MaxLocationBytes
}

// validDigest accepts only lowercase or uppercase hexadecimal SHA-256 identities supplied by composition.
func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f' || character >= 'A' && character <= 'F') {
			return false
		}
	}
	return true
}

// visit identifies active reference-like values while bounded source depth is inspected.
type visit struct {
	kind    reflect.Kind
	pointer uintptr
}

// cloneSnapshot prevents adapters and callers from sharing mutable schema graphs.
func cloneSnapshot(v SourceSnapshot) SourceSnapshot {
	raw, _ := json.Marshal(v)
	var out SourceSnapshot
	_ = json.Unmarshal(raw, &out)
	return out
}

// StaticSource is a passive fixture/adapter for already materialized derived, observed or declared facts.
type StaticSource struct {
	Snapshot SourceSnapshot
	mu       sync.Mutex
}

// Load returns an isolated pre-materialized snapshot and never interprets or executes its input.
func (s *StaticSource) Load(ctx context.Context, _ SourceRequest) (SourceSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return SourceSnapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSnapshot(s.Snapshot), nil
}
