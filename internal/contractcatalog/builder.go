package contractcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Build combines validated snapshots deterministically and retains disagreements as explicit conflicts.
func Build(ctx context.Context, loaded LoadedSources, limits Limits) (OperationCatalog, error) {
	if err := validLimits(limits); err != nil {
		return OperationCatalog{}, err
	}
	snapshots := loaded.snapshots
	if len(snapshots) == 0 || len(snapshots) > limits.MaxSources {
		return OperationCatalog{}, typedError(CategoryLimitExceeded, "sources", nil)
	}
	owned := make([]SourceSnapshot, len(snapshots))
	for i, v := range snapshots {
		if err := ctx.Err(); err != nil {
			return OperationCatalog{}, typedError(CategoryCancelled, "merge", err)
		}
		depth, err := structuralDepthContext(ctx, reflect.ValueOf(v), map[visit]bool{})
		if err != nil {
			return OperationCatalog{}, typedError(CategoryCancelled, "merge", err)
		}
		if depth > limits.MaxDepth {
			return OperationCatalog{}, typedError(CategoryLimitExceeded, "merge.depth", nil)
		}
		owned[i] = cloneSnapshot(v)
	}
	sort.Slice(owned, func(i, j int) bool { return sourceIdentityKey(owned[i].Source) < sourceIdentityKey(owned[j].Source) })
	type grouped struct {
		operation OperationDescriptor
		variants  []OperationDescriptor
	}
	byKey := map[OperationKey]*grouped{}
	logical := map[string][]OperationKey{}
	sources := make([]SourceSummary, 0, len(owned))
	omissions := OmissionCounts{}
	securitySchemes := map[string]SecurityScheme{}
	securitySchemeKeys := map[string][]OperationKey{}
	securitySchemeLocations := map[string][]string{}
	securitySchemeConflicts := map[string]bool{}
	claims := 0
	diagnostics := []SourceDiagnostic{}
	affected := []OperationKey{}
	for _, snapshot := range owned {
		sources = append(sources, snapshot.Source)
		omissions = addOmissions(omissions, snapshot.Omissions)
		diagnostics = append(diagnostics, snapshot.Diagnostics...)
		affected = append(affected, snapshot.AffectedOperations...)
		for _, op := range snapshot.Operations {
			if err := ctx.Err(); err != nil {
				return OperationCatalog{}, typedError(CategoryCancelled, "merge", err)
			}
			op = canonicalOperation(op)
			key := OperationKey{Method: strings.ToUpper(op.Key.Method), Path: op.Key.Path}
			op.Key = key
			for name, scheme := range op.SecuritySchemes {
				canonical := canonicalSecurityScheme(scheme)
				if prior, exists := securitySchemes[name]; exists && !jsonEqual(prior, canonical) {
					securitySchemeConflicts[name] = true
				} else if !exists {
					securitySchemes[name] = canonical
				}
				securitySchemeKeys[name] = append(securitySchemeKeys[name], key)
				securitySchemeLocations[name] = append(securitySchemeLocations[name], locations(op)...)
			}
			claims += len(op.Claims)
			if claims > limits.MaxClaims {
				return OperationCatalog{}, typedError(CategoryLimitExceeded, "claims", nil)
			}
			bucket := byKey[key]
			if bucket == nil {
				bucket = &grouped{operation: cloneOperation(op)}
				byKey[key] = bucket
			}
			bucket.variants = append(bucket.variants, cloneOperation(op))
			if op.LogicalID != "" {
				for _, provenance := range op.Provenance {
					logical[provenance.SourceID+"\x00"+op.LogicalID] = append(logical[provenance.SourceID+"\x00"+op.LogicalID], key)
				}
			}
		}
	}
	keys := make([]OperationKey, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sortKeys(keys)
	if len(keys) > limits.MaxOperations {
		return OperationCatalog{}, typedError(CategoryLimitExceeded, "operations", nil)
	}
	operations := make([]OperationDescriptor, 0, len(keys))
	conflicts := []Conflict{}
	for _, key := range keys {
		bucket := byKey[key]
		merged, found := mergeVariants(bucket.variants)
		operations = append(operations, merged)
		conflicts = append(conflicts, found...)
	}
	for id, values := range logical {
		unique := uniqueKeys(values)
		if len(unique) > 1 {
			conflicts = append(conflicts, newConflict(ConflictIdentity, unique, []string{"operations.logical_id"}, id))
		}
	}
	for name := range securitySchemeConflicts {
		conflicts = append(conflicts, newConflict(ConflictSecurity, uniqueKeys(securitySchemeKeys[name]), securitySchemeLocations[name], "security_scheme/"+name))
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].ID < conflicts[j].ID })
	sort.Slice(diagnostics, func(i, j int) bool {
		return diagnostics[i].Code+"\x00"+diagnostics[i].SafeLocation < diagnostics[j].Code+"\x00"+diagnostics[j].SafeLocation
	})
	if len(diagnostics) > 1 {
		unique := diagnostics[:1]
		for _, item := range diagnostics[1:] {
			if item != unique[len(unique)-1] {
				unique = append(unique, item)
			}
		}
		diagnostics = unique
	}
	affected = uniqueKeys(affected)
	if len(conflicts)+len(diagnostics)+omissionDiagnostics(omissions) > limits.MaxDiagnostics {
		return OperationCatalog{}, typedError(CategoryLimitExceeded, "conflicts", nil)
	}
	projection := catalogProjection(operations, securitySchemes, sources, conflicts, omissions, diagnostics, affected)
	raw, err := json.Marshal(projection)
	if err != nil {
		return OperationCatalog{}, typedError(CategoryInternal, "catalog", nil)
	}
	if len(raw) > limits.MaxBytes {
		return OperationCatalog{}, typedError(CategoryLimitExceeded, "catalog", nil)
	}
	return OperationCatalog{version: VersionV1, digest: digestBytes(raw), operations: cloneOperations(operations), securitySchemes: cloneSecuritySchemes(securitySchemes), sources: append([]SourceSummary(nil), sources...), conflicts: cloneConflicts(conflicts), omissions: omissions, diagnostics: append([]SourceDiagnostic(nil), diagnostics...), affectedOperations: append([]OperationKey(nil), affected...)}, nil
}

// sourceIdentityKey orders sources by their complete trusted identity tuple.
func sourceIdentityKey(source SourceSummary) string {
	raw, _ := json.Marshal(source)
	return string(raw)
}

// mergeVariants coalesces equal operations and reports every incompatible executable dimension.
func mergeVariants(variants []OperationDescriptor) (OperationDescriptor, []Conflict) {
	sort.Slice(variants, func(i, j int) bool { return provenanceSortKey(variants[i]) < provenanceSortKey(variants[j]) })
	base := cloneOperation(variants[0])
	allProv := append([]Provenance(nil), base.Provenance...)
	classes := append([]SourceClass(nil), base.Classes...)
	minConfidence, minCompleteness := base.Confidence, base.Completeness
	conflicts := []Conflict{}
	for _, candidate := range variants[1:] {
		allProv = append(allProv, candidate.Provenance...)
		classes = append(classes, candidate.Classes...)
		if base.LogicalID != candidate.LogicalID {
			conflicts = append(conflicts, newConflict(ConflictIdentity, []OperationKey{base.Key}, locations(base, candidate), "identity"))
		}
		if confidenceRank(candidate.Confidence) < confidenceRank(minConfidence) {
			minConfidence = candidate.Confidence
		}
		if completenessRank(candidate.Completeness) < completenessRank(minCompleteness) {
			minCompleteness = candidate.Completeness
		}
		if !jsonEqual(base.Parameters, candidate.Parameters) || !jsonEqual(base.RequestBody, candidate.RequestBody) {
			conflicts = append(conflicts, newConflict(ConflictSchema, []OperationKey{base.Key}, locations(base, candidate), "schema"))
		}
		if !jsonEqual(base.Security, candidate.Security) || !jsonEqual(base.SecuritySchemes, candidate.SecuritySchemes) {
			conflicts = append(conflicts, newConflict(ConflictSecurity, []OperationKey{base.Key}, locations(base, candidate), "security"))
		}
		if !jsonEqual(base.Responses, candidate.Responses) {
			conflicts = append(conflicts, newConflict(ConflictResponse, []OperationKey{base.Key}, locations(base, candidate), "response"))
		}
	}
	base.Provenance = uniqueProvenance(allProv)
	base.Claims = mergeClaims(variants)
	base.Classes = uniqueClasses(classes)
	base.Confidence = minConfidence
	base.Completeness = minCompleteness
	return base, conflicts
}

// mergeClaims coalesces structurally identical facts while retaining every exact contributing provenance tuple.
func mergeClaims(variants []OperationDescriptor) []Claim {
	byIdentity := map[string]Claim{}
	for _, operation := range variants {
		for _, claim := range operation.Claims {
			key := string(claim.Kind) + "\x00" + claim.Field + "\x00" + claim.Fingerprint
			if existing, ok := byIdentity[key]; ok {
				existing.Provenance = uniqueProvenance(append(existing.Provenance, claim.Provenance...))
				if confidenceRank(claim.Confidence) < confidenceRank(existing.Confidence) {
					existing.Confidence = claim.Confidence
				}
				if completenessRank(claim.Completeness) < completenessRank(existing.Completeness) {
					existing.Completeness = claim.Completeness
				}
				byIdentity[key] = existing
			} else {
				claim.Provenance = uniqueProvenance(claim.Provenance)
				byIdentity[key] = claim
			}
		}
	}
	keys := make([]string, 0, len(byIdentity))
	for key := range byIdentity {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]Claim, 0, len(keys))
	for _, key := range keys {
		out = append(out, byIdentity[key])
	}
	return out
}

// newConflict derives a stable identifier exclusively from safe structural values.
func newConflict(kind ConflictKind, keys []OperationKey, locations []string, field string) Conflict {
	sortKeys(keys)
	locations = sortedUniqueStrings(locations)
	raw, _ := json.Marshal(struct {
		Kind  ConflictKind
		Keys  []OperationKey
		Field string
	}{kind, keys, field})
	sum := sha256.Sum256(raw)
	return Conflict{ID: hex.EncodeToString(sum[:12]), Kind: kind, OperationKeys: keys, Locations: locations}
}

// locations collects already validated structural provenance locations from incompatible claims.
func locations(values ...OperationDescriptor) []string {
	out := []string{}
	for _, v := range values {
		for _, p := range v.Provenance {
			out = append(out, p.SafeLocation)
		}
	}
	return out
}

// provenanceSortKey stabilizes merge base selection without assigning authority priority.
func provenanceSortKey(v OperationDescriptor) string {
	if len(v.Provenance) == 0 {
		return fmt.Sprintf("%s %s", v.Key.Method, v.Key.Path)
	}
	p := v.Provenance[0]
	return p.SourceID + "/" + p.RepositoryID + "/" + p.Revision + "/" + p.SafeLocation
}

// uniqueProvenance sorts and coalesces exact provenance tuples while preserving all contributing sources.
func uniqueProvenance(values []Provenance) []Provenance {
	sort.Slice(values, func(i, j int) bool {
		a, b := values[i], values[j]
		return a.SourceID+a.RepositoryID+a.Revision+a.SafeLocation+a.SourceDigest < b.SourceID+b.RepositoryID+b.Revision+b.SafeLocation+b.SourceDigest
	})
	out := values[:0]
	for _, v := range values {
		if len(out) == 0 || !jsonEqual(out[len(out)-1], v) {
			out = append(out, v)
		}
	}
	return append([]Provenance(nil), out...)
}

// uniqueClasses sorts effective source labels without relabeling non-authoritative facts.
func uniqueClasses(values []SourceClass) []SourceClass {
	seen := map[SourceClass]bool{}
	for _, v := range values {
		seen[v] = true
	}
	out := make([]SourceClass, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// uniqueKeys removes duplicate operation identities and returns canonical method/path order.
func uniqueKeys(values []OperationKey) []OperationKey {
	seen := map[OperationKey]bool{}
	for _, v := range values {
		seen[v] = true
	}
	out := make([]OperationKey, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sortKeys(out)
	return out
}

// sortKeys defines canonical operation ordering independent from source and map iteration.
func sortKeys(values []OperationKey) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].Path != values[j].Path {
			return values[i].Path < values[j].Path
		}
		return values[i].Method < values[j].Method
	})
}

// jsonEqual compares normalized structural values rather than addresses or map order.
func jsonEqual(a, b any) bool {
	ar, _ := json.Marshal(a)
	br, _ := json.Marshal(b)
	return string(ar) == string(br)
}

// addOmissions aggregates explicit loss counters without changing their meaning.
func addOmissions(a, b OmissionCounts) OmissionCounts {
	return OmissionCounts{a.Examples + b.Examples, a.Extensions + b.Extensions, a.Documentation + b.Documentation, a.Unselected + b.Unselected, a.SizeLimit + b.SizeLimit}
}
