package contractreview

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// CreateArtifact verifies both catalogs and computes an all-or-nothing canonical review artifact.
func CreateArtifact(ctx context.Context, candidate CandidateSnapshot, baseline BaselineSnapshot, limits Limits) (ReviewArtifact, error) {
	if err := limits.Validate(); err != nil {
		return ReviewArtifact{}, err
	}
	if err := cancellation(ctx, "create"); err != nil {
		return ReviewArtifact{}, err
	}
	if !validIdentity(candidate.Identity) || candidate.Identity.Digest != candidate.Catalog.Digest() || candidate.Identity.Version != candidate.Catalog.Version() {
		return ReviewArtifact{}, typedError(CategoryInvalidInput, "candidate", nil)
	}
	if (baseline.Kind != "openapi_authoritative" && baseline.Kind != "approved_projection") || baseline.Kind == "approved_projection" && baseline.ApprovedReference == nil || !validIdentity(baseline.Identity) || baseline.Identity.Digest != baseline.Catalog.Digest() || baseline.Identity.Version != baseline.Catalog.Version() {
		return ReviewArtifact{}, typedError(CategoryIncompatibleBaseline, "baseline", nil)
	}
	if candidate.Identity.RepositoryID != baseline.Identity.RepositoryID || candidate.Identity.Scope != baseline.Identity.Scope || candidate.Identity.Version != baseline.Identity.Version {
		return ReviewArtifact{}, typedError(CategoryIncompatibleBaseline, "baseline.binding", nil)
	}
	analyzers, err := normalizeAnalyzers(candidate.Analyzers)
	if err != nil {
		return ReviewArtifact{}, err
	}
	if err := validateCatalogBounds(ctx, []contractcatalog.OperationCatalog{candidate.Catalog, baseline.Catalog}, limits); err != nil {
		return ReviewArtifact{}, err
	}
	sources := candidate.Catalog.Sources()
	manifests := SourceManifests{Candidate: sources, Baseline: baseline.Catalog.Sources()}
	items, counts, err := structuralDiff(ctx, candidate.Catalog, baseline.Catalog, limits)
	if err != nil {
		return ReviewArtifact{}, err
	}
	seed := struct {
		Version       string
		Algorithm     string
		Candidate     CatalogIdentity
		Baseline      CatalogIdentity
		Sources       []contractcatalog.SourceSummary
		Manifests     SourceManifests
		Analyzers     []AnalyzerIdentity
		Limits        Limits
		Items         []ReviewItem
		Counts        ReviewCounts
		GitHub        any
		GitHubActorID int64
	}{VersionV1, AlgorithmV1, candidate.Identity, baseline.Identity, sources, manifests, analyzers, limits, items, counts, candidate.GitHub, candidate.GitHubActorID}
	digest, err := digestValue(ctx, seed, limits.MaxBytes)
	if err != nil {
		return ReviewArtifact{}, err
	}
	artifact := ReviewArtifact{Version: VersionV1, Algorithm: AlgorithmV1, ID: digest[:24], Digest: digest, Candidate: candidate.Identity, Baseline: baseline.Identity, Sources: sources, Manifests: manifests, Analyzers: analyzers, Limits: limits, Items: items, Counts: counts, GitHub: deepClone(candidate.GitHub), GitHubActorID: candidate.GitHubActorID}
	return artifact.Clone(), nil
}

// structuralDiff compares canonical operation dimensions and exposes uncertainty instead of inferring absence.
func structuralDiff(ctx context.Context, candidate, baseline contractcatalog.OperationCatalog, limits Limits) ([]ReviewItem, ReviewCounts, error) {
	candidateOps, baselineOps := operationMap(candidate.Operations()), operationMap(baseline.Operations())
	keys := make([]contractcatalog.OperationKey, 0, len(candidateOps)+len(baselineOps))
	seen := map[contractcatalog.OperationKey]bool{}
	for key := range candidateOps {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range baselineOps {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return operationKeyString(keys[i]) < operationKeyString(keys[j]) })
	if len(keys) > limits.MaxOperations {
		return nil, ReviewCounts{}, typedError(CategoryLimitExceeded, "operations", nil)
	}
	items := []ReviewItem{}
	counts := ReviewCounts{}
	for _, key := range keys {
		if err := cancellation(ctx, "diff.operations"); err != nil {
			return nil, ReviewCounts{}, err
		}
		before, hasBefore := baselineOps[key]
		after, hasAfter := candidateOps[key]
		causes := uncertaintyCauses(key, candidate, baseline, before, after, hasBefore, hasAfter)
		switch {
		case !hasBefore && hasAfter:
			kind := ChangeAdded
			if len(causes) > 0 {
				kind = ChangeUncertain
			}
			item, err := makeItem(ctx, kind, key, "operation", nil, &after, causes, limits)
			if err != nil {
				return nil, ReviewCounts{}, err
			}
			items = append(items, item)
			incrementCount(&counts, kind)
		case hasBefore && !hasAfter:
			kind := ChangeRemoved
			if len(causes) > 0 {
				kind = ChangeUncertain
			}
			item, err := makeItem(ctx, kind, key, "operation", &before, nil, causes, limits)
			if err != nil {
				return nil, ReviewCounts{}, err
			}
			items = append(items, item)
			incrementCount(&counts, kind)
		default:
			dimensions := operationDimensions(before, after)
			changed := 0
			for _, dimension := range dimensions {
				if err := cancellation(ctx, "diff.items"); err != nil {
					return nil, ReviewCounts{}, err
				}
				if dimension.Before == dimension.After {
					continue
				}
				kind := ChangeModified
				if len(causes) > 0 {
					kind = ChangeUncertain
				}
				item, err := makeItem(ctx, kind, key, dimension.Path, &before, &after, causes, limits)
				if err != nil {
					return nil, ReviewCounts{}, err
				}
				items = append(items, item)
				incrementCount(&counts, kind)
				changed++
			}
			if changed == 0 {
				counts.Unchanged++
			}
		}
		if len(items) > limits.MaxItems {
			return nil, ReviewCounts{}, typedError(CategoryLimitExceeded, "items", nil)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	wireDependencies(items)
	return items, counts, nil
}

// operationDimension holds fingerprints for one independently reviewable operation field.
type operationDimension struct{ Path, Before, After string }

// operationDimensions creates stable fingerprints for each structural contract dimension.
func operationDimensions(before, after contractcatalog.OperationDescriptor) []operationDimension {
	dimensions := []operationDimension{
		fingerprintDimension("logical_id", before.LogicalID, after.LogicalID),
		fingerprintDimension("security", canonicalSecurity(before.Security), canonicalSecurity(after.Security)),
		fingerprintDimension("declarative_constraints", canonicalConstraints(before.DeclarativeConstraints), canonicalConstraints(after.DeclarativeConstraints)),
		fingerprintDimension("metadata", canonicalMetadata(before), canonicalMetadata(after)),
	}
	beforeParameters, afterParameters := parameterMap(before.Parameters), parameterMap(after.Parameters)
	for _, key := range unionStringKeys(beforeParameters, afterParameters) {
		left, leftOK := beforeParameters[key]
		right, rightOK := afterParameters[key]
		prefix := "parameters/" + encodePathSegment(key)
		if !leftOK || !rightOK {
			dimensions = append(dimensions, fingerprintDimension(prefix, optionalParameter(left, leftOK), optionalParameter(right, rightOK)))
			continue
		}
		dimensions = append(dimensions, fingerprintDimension(prefix+"/metadata", parameterMetadata(left), parameterMetadata(right)))
		dimensions = append(dimensions, schemaDimensions(prefix+"/schema", left.Schema, right.Schema)...)
		for _, media := range unionStringKeys(left.Content, right.Content) {
			lm, lok := left.Content[media]
			rm, rok := right.Content[media]
			mediaPrefix := prefix + "/content/" + encodePathSegment(media)
			if !lok || !rok {
				dimensions = append(dimensions, fingerprintDimension(mediaPrefix, optionalMedia(lm, lok), optionalMedia(rm, rok)))
			} else {
				dimensions = append(dimensions, schemaDimensions(mediaPrefix+"/schema", lm.Schema, rm.Schema)...)
			}
		}
	}
	dimensions = append(dimensions, fingerprintDimension("request_body/required", bodyRequired(before.RequestBody), bodyRequired(after.RequestBody)))
	for _, media := range unionStringKeys(bodyContent(before.RequestBody), bodyContent(after.RequestBody)) {
		left, lok := bodyContent(before.RequestBody)[media]
		right, rok := bodyContent(after.RequestBody)[media]
		prefix := "request_body/content/" + encodePathSegment(media)
		if !lok || !rok {
			dimensions = append(dimensions, fingerprintDimension(prefix, optionalMedia(left, lok), optionalMedia(right, rok)))
		} else {
			dimensions = append(dimensions, schemaDimensions(prefix+"/schema", left.Schema, right.Schema)...)
		}
	}
	for _, selector := range unionStringKeys(before.Responses, after.Responses) {
		left, leftOK := before.Responses[selector]
		right, rightOK := after.Responses[selector]
		if !leftOK || !rightOK {
			dimensions = append(dimensions, fingerprintDimension("responses/"+encodePathSegment(selector), optionalResponse(left, leftOK), optionalResponse(right, rightOK)))
			continue
		}
		for _, header := range unionStringKeys(left.Headers, right.Headers) {
			lh, lok := left.Headers[header]
			rh, rok := right.Headers[header]
			prefix := "responses/" + encodePathSegment(selector) + "/headers/" + encodePathSegment(header)
			if !lok || !rok {
				dimensions = append(dimensions, fingerprintDimension(prefix, optionalHeader(lh, lok), optionalHeader(rh, rok)))
			} else {
				dimensions = append(dimensions, fingerprintDimension(prefix+"/metadata", headerMetadata(lh), headerMetadata(rh)))
				dimensions = append(dimensions, schemaDimensions(prefix+"/schema", lh.Schema, rh.Schema)...)
			}
		}
		for _, media := range unionStringKeys(left.Content, right.Content) {
			lm, lok := left.Content[media]
			rm, rok := right.Content[media]
			prefix := "responses/" + encodePathSegment(selector) + "/content/" + encodePathSegment(media)
			if !lok || !rok {
				dimensions = append(dimensions, fingerprintDimension(prefix, optionalMedia(lm, lok), optionalMedia(rm, rok)))
			} else {
				dimensions = append(dimensions, schemaDimensions(prefix+"/schema", lm.Schema, rm.Schema)...)
			}
		}
	}
	for _, name := range unionStringKeys(before.SecuritySchemes, after.SecuritySchemes) {
		dimensions = append(dimensions, fingerprintDimension("security_schemes/"+encodePathSegment(name), before.SecuritySchemes[name], after.SecuritySchemes[name]))
	}
	return dimensions
}

// schemaDimensions recursively emits independently reviewable schema nodes and scalar attributes.
func schemaDimensions(prefix string, before, after *contractcatalog.Schema) []operationDimension {
	if before == nil || after == nil {
		return []operationDimension{fingerprintDimension(prefix, before, after)}
	}
	out := []operationDimension{}
	for _, field := range []struct {
		name        string
		left, right any
	}{{"id", before.ID, after.ID}, {"type", before.Type, after.Type}, {"format", before.Format, after.Format}, {"required", before.Required, after.Required}, {"enum", before.Enum, after.Enum}, {"min_properties", before.MinProperties, after.MinProperties}, {"nullable", before.Nullable, after.Nullable}, {"read_only", before.ReadOnly, after.ReadOnly}, {"write_only", before.WriteOnly, after.WriteOnly}} {
		out = append(out, fingerprintDimension(prefix+"/"+field.name, field.left, field.right))
	}
	for _, name := range unionStringKeys(before.Properties, after.Properties) {
		out = append(out, schemaDimensions(prefix+"/properties/"+encodePathSegment(name), before.Properties[name], after.Properties[name])...)
	}
	out = append(out, schemaDimensions(prefix+"/items", before.Items, after.Items)...)
	maximum := len(before.AllOf)
	if len(after.AllOf) > maximum {
		maximum = len(after.AllOf)
	}
	for index := 0; index < maximum; index++ {
		var left, right *contractcatalog.Schema
		if index < len(before.AllOf) {
			left = before.AllOf[index]
		}
		if index < len(after.AllOf) {
			right = after.AllOf[index]
		}
		out = append(out, schemaDimensions(fmt.Sprintf("%s/allOf/%d", prefix, index), left, right)...)
	}
	return out
}

// optionalParameter preserves absence independently from a zero-value parameter.
func optionalParameter(value contractcatalog.Parameter, ok bool) any {
	if !ok {
		return nil
	}
	return value
}

// optionalMedia preserves absence independently from a media entry with no schema.
func optionalMedia(value contractcatalog.MediaType, ok bool) any {
	if !ok {
		return nil
	}
	return value
}

// optionalHeader preserves absence independently from a zero-value response header.
func optionalHeader(value contractcatalog.Header, ok bool) any {
	if !ok {
		return nil
	}
	return value
}

// parameterMetadata excludes nested schemas so sibling decisions cannot apply them implicitly.
func parameterMetadata(value contractcatalog.Parameter) any {
	return struct {
		Name, In string
		Required bool
	}{value.Name, value.In, value.Required}
}

// headerMetadata excludes nested schemas so header flags and schema nodes remain independent.
func headerMetadata(value contractcatalog.Header) any { return struct{ Required bool }{value.Required} }

// wireDependencies creates a bidirectional atomic group between one operation's security choice and only its referenced changed schemes.
func wireDependencies(items []ReviewItem) {
	for securityIndex := range items {
		security := &items[securityIndex]
		if security.Path != "security" {
			continue
		}
		referenced := referencedSecurityNames(security.Before, security.After)
		for schemeIndex := range items {
			scheme := &items[schemeIndex]
			if scheme.Operation != security.Operation || !strings.HasPrefix(scheme.Path, "security_schemes/") {
				continue
			}
			name := decodePathSegment(strings.TrimPrefix(scheme.Path, "security_schemes/"))
			if referenced[name] {
				security.Dependencies = append(security.Dependencies, scheme.ID)
				scheme.Dependencies = append(scheme.Dependencies, security.ID)
			}
		}
		security.Dependencies = sortedUnique(security.Dependencies)
	}
}

// referencedSecurityNames returns the exact union referenced by the before and after operation security expressions.
func referencedSecurityNames(before, after *contractcatalog.OperationDescriptor) map[string]bool {
	out := map[string]bool{}
	for _, operation := range []*contractcatalog.OperationDescriptor{before, after} {
		if operation == nil {
			continue
		}
		for _, requirement := range operation.Security {
			for name := range requirement.Schemes {
				out[name] = true
			}
		}
	}
	return out
}

// parameterMap indexes parameters by their structural location and name.
func parameterMap(values []contractcatalog.Parameter) map[string]contractcatalog.Parameter {
	out := map[string]contractcatalog.Parameter{}
	for _, value := range values {
		out[value.In+"\x00"+value.Name] = value
	}
	return out
}

// bodyContent returns an empty map for an absent request body.
func bodyContent(value *contractcatalog.RequestBody) map[string]contractcatalog.MediaType {
	if value == nil {
		return map[string]contractcatalog.MediaType{}
	}
	return value.Content
}

// bodyRequired returns nil for an absent body so presence changes remain reviewable.
func bodyRequired(value *contractcatalog.RequestBody) any {
	if value == nil {
		return nil
	}
	return value.Required
}

// optionalResponse preserves response absence independently from an empty response object.
func optionalResponse(value contractcatalog.Response, ok bool) any {
	if !ok {
		return nil
	}
	return value
}

// unionStringKeys returns the sorted key union of two structural maps.
func unionStringKeys[A any](left, right map[string]A) []string {
	seen := map[string]bool{}
	out := []string{}
	for key := range left {
		seen[key] = true
		out = append(out, key)
	}
	for key := range right {
		if !seen[key] {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// encodePathSegment makes arbitrary media and selector keys unambiguous in review paths.
func encodePathSegment(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

// decodePathSegment reverses an internally generated structural path segment.
func decodePathSegment(value string) string {
	raw, _ := base64.RawURLEncoding.DecodeString(value)
	return string(raw)
}

// fingerprintDimension reduces one structural value to its exact deterministic JSON identity.
func fingerprintDimension(path string, before, after any) operationDimension {
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	return operationDimension{Path: path, Before: digestBytes(b), After: digestBytes(a)}
}

// makeItem binds fingerprints and payload copies to a content-derived stable identity.
func makeItem(ctx context.Context, kind ChangeKind, key contractcatalog.OperationKey, path string, before, after *contractcatalog.OperationDescriptor, causes []string, limits Limits) (ReviewItem, error) {
	item := ReviewItem{Kind: kind, Operation: key, Path: path, Causes: sortedUnique(causes), Before: cloneOperationPointer(before), After: cloneOperationPointer(after)}
	if before != nil {
		raw, _ := json.Marshal(dimensionValue(*before, path))
		item.BeforeFingerprint = digestBytes(raw)
		item.Provenance = append(item.Provenance, before.Provenance...)
	}
	if after != nil {
		raw, _ := json.Marshal(dimensionValue(*after, path))
		item.AfterFingerprint = digestBytes(raw)
		item.Provenance = append(item.Provenance, after.Provenance...)
	}
	item.Provenance = canonicalProvenance(item.Provenance, len(item.Provenance))
	seed := struct {
		Algorithm           string
		Kind                ChangeKind
		Operation           contractcatalog.OperationKey
		Path, Before, After string
		Causes              []string
	}{AlgorithmV1, kind, key, path, item.BeforeFingerprint, item.AfterFingerprint, item.Causes}
	digest, err := digestValue(ctx, seed, limits.MaxBytes)
	if err != nil {
		return ReviewItem{}, err
	}
	item.ID = digest[:24]
	return item, nil
}

// uncertaintyCauses derives only closed structural reasons that block proof of presence, absence or value.
func uncertaintyCauses(key contractcatalog.OperationKey, candidate, baseline contractcatalog.OperationCatalog, before, after contractcatalog.OperationDescriptor, hasBefore, hasAfter bool) []string {
	causes := []string{}
	for _, catalog := range []contractcatalog.OperationCatalog{candidate, baseline} {
		for _, conflict := range catalog.Conflicts() {
			for _, affected := range conflict.OperationKeys {
				if affected == key {
					causes = append(causes, "conflict:"+string(conflict.Kind))
				}
			}
		}
		if len(catalog.Diagnostics()) > 0 {
			causes = append(causes, "diagnostic")
		}
		omissions := catalog.Omissions()
		if omissions.SizeLimit+omissions.Unselected > 0 {
			causes = append(causes, "omission")
		}
	}
	if hasBefore && before.Completeness != contractcatalog.CompletenessComplete {
		causes = append(causes, "baseline_"+string(before.Completeness))
	}
	if hasAfter && after.Completeness != contractcatalog.CompletenessComplete {
		causes = append(causes, "candidate_"+string(after.Completeness))
	}
	return sortedUnique(causes)
}

// operationMap creates a unique exact-key index over an already canonical catalog.
func operationMap(values []contractcatalog.OperationDescriptor) map[contractcatalog.OperationKey]contractcatalog.OperationDescriptor {
	out := map[contractcatalog.OperationKey]contractcatalog.OperationDescriptor{}
	for _, value := range values {
		value.Key.Method = strings.ToUpper(value.Key.Method)
		out[value.Key] = value
	}
	return out
}

// operationKeyString stabilizes ordering without relying on optional logical IDs.
func operationKeyString(key contractcatalog.OperationKey) string {
	return strings.ToUpper(key.Method) + "\x00" + key.Path
}

// dimensionValue selects exactly the unit whose fingerprints and decisions are recorded.
func dimensionValue(operation contractcatalog.OperationDescriptor, path string) any {
	parts := strings.Split(path, "/")
	if len(parts) == 2 && parts[0] == "parameters" {
		return parameterMap(operation.Parameters)[decodePathSegment(parts[1])]
	}
	if len(parts) >= 3 && parts[0] == "parameters" {
		parameter, ok := parameterMap(operation.Parameters)[decodePathSegment(parts[1])]
		if !ok {
			return nil
		}
		if parts[2] == "metadata" {
			return parameterMetadata(parameter)
		}
		if parts[2] == "schema" {
			return schemaPathValue(parameter.Schema, parts[3:])
		}
		if len(parts) >= 5 && parts[2] == "content" {
			media, ok := parameter.Content[decodePathSegment(parts[3])]
			if !ok {
				return nil
			}
			if parts[4] == "schema" {
				return schemaPathValue(media.Schema, parts[5:])
			}
		}
	}
	if len(parts) >= 2 && parts[0] == "request_body" {
		if parts[1] == "required" {
			return bodyRequired(operation.RequestBody)
		}
		if len(parts) == 3 && parts[1] == "content" {
			return bodyContent(operation.RequestBody)[decodePathSegment(parts[2])]
		}
		if len(parts) >= 4 && parts[1] == "content" {
			media, ok := bodyContent(operation.RequestBody)[decodePathSegment(parts[2])]
			if !ok {
				return nil
			}
			if parts[3] == "schema" {
				return schemaPathValue(media.Schema, parts[4:])
			}
		}
	}
	if len(parts) >= 2 && parts[0] == "responses" {
		response, ok := operation.Responses[decodePathSegment(parts[1])]
		if len(parts) == 2 {
			return optionalResponse(response, ok)
		}
		if !ok {
			return nil
		}
		if len(parts) == 4 && parts[2] == "headers" {
			return response.Headers[decodePathSegment(parts[3])]
		}
		if len(parts) == 4 && parts[2] == "content" {
			return response.Content[decodePathSegment(parts[3])]
		}
		if len(parts) >= 5 && parts[2] == "headers" {
			header, ok := response.Headers[decodePathSegment(parts[3])]
			if !ok {
				return nil
			}
			if parts[4] == "metadata" {
				return headerMetadata(header)
			}
			if parts[4] == "schema" {
				return schemaPathValue(header.Schema, parts[5:])
			}
		}
		if len(parts) >= 5 && parts[2] == "content" {
			media, ok := response.Content[decodePathSegment(parts[3])]
			if !ok {
				return nil
			}
			if parts[4] == "schema" {
				return schemaPathValue(media.Schema, parts[5:])
			}
		}
	}
	if len(parts) == 2 && parts[0] == "security_schemes" {
		return operation.SecuritySchemes[decodePathSegment(parts[1])]
	}
	switch path {
	case "logical_id":
		return operation.LogicalID
	case "security":
		return canonicalSecurity(operation.Security)
	case "declarative_constraints":
		return canonicalConstraints(operation.DeclarativeConstraints)
	case "metadata":
		return canonicalMetadata(operation)
	default:
		return operation
	}
}

// schemaPathValue selects one recursive schema node or scalar attribute for stable item fingerprints.
func schemaPathValue(schema *contractcatalog.Schema, parts []string) any {
	if schema == nil {
		return nil
	}
	if len(parts) == 0 {
		return schema
	}
	switch parts[0] {
	case "id":
		return schema.ID
	case "type":
		return schema.Type
	case "format":
		return schema.Format
	case "required":
		return schema.Required
	case "enum":
		return schema.Enum
	case "min_properties":
		return schema.MinProperties
	case "nullable":
		return schema.Nullable
	case "read_only":
		return schema.ReadOnly
	case "write_only":
		return schema.WriteOnly
	case "items":
		return schemaPathValue(schema.Items, parts[1:])
	case "properties":
		if len(parts) < 2 {
			return nil
		}
		return schemaPathValue(schema.Properties[decodePathSegment(parts[1])], parts[2:])
	case "allOf":
		if len(parts) < 2 {
			return nil
		}
		index := 0
		_, _ = fmt.Sscanf(parts[1], "%d", &index)
		if index >= len(schema.AllOf) {
			return nil
		}
		return schemaPathValue(schema.AllOf[index], parts[2:])
	}
	return nil
}

// canonicalParameters sorts identity-bearing parameters while retaining full schema structure.
func canonicalParameters(values []contractcatalog.Parameter) []contractcatalog.Parameter {
	out := deepClone(values)
	sort.Slice(out, func(i, j int) bool { return out[i].In+"\x00"+out[i].Name < out[j].In+"\x00"+out[j].Name })
	return out
}

// canonicalSecurity treats OR alternatives and AND scopes as semantic sets.
func canonicalSecurity(values []contractcatalog.SecurityRequirement) []contractcatalog.SecurityRequirement {
	out := deepClone(values)
	for i := range out {
		for name, scopes := range out[i].Schemes {
			sort.Strings(scopes)
			out[i].Schemes[name] = scopes
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := json.Marshal(out[i])
		b, _ := json.Marshal(out[j])
		return string(a) < string(b)
	})
	return out
}

// canonicalConstraints treats declarative security metadata as inert ordered evidence.
func canonicalConstraints(values []contractcatalog.DeclarativeConstraint) []contractcatalog.DeclarativeConstraint {
	out := deepClone(values)
	for i := range out {
		sort.Strings(out[i].Values)
		out[i].Provenance = canonicalProvenance(out[i].Provenance, len(out[i].Provenance))
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := json.Marshal(out[i])
		b, _ := json.Marshal(out[j])
		return string(a) < string(b)
	})
	return out
}

// canonicalMetadata captures trust metadata independently of executable contract fields.
func canonicalMetadata(value contractcatalog.OperationDescriptor) any {
	return struct {
		Provenance   []contractcatalog.Provenance
		Confidence   contractcatalog.Confidence
		Completeness contractcatalog.Completeness
		Classes      []contractcatalog.SourceClass
		Claims       []contractcatalog.Claim
	}{canonicalProvenance(value.Provenance, len(value.Provenance)), value.Confidence, value.Completeness, sortedClasses(value.Classes), canonicalClaims(value.Claims)}
}

// canonicalProvenance sorts and bounds exact source identities without merging non-identical lineage.
func canonicalProvenance(values []contractcatalog.Provenance, maximum int) []contractcatalog.Provenance {
	out := append([]contractcatalog.Provenance(nil), values...)
	sort.Slice(out, func(i, j int) bool {
		a, _ := json.Marshal(out[i])
		b, _ := json.Marshal(out[j])
		return string(a) < string(b)
	})
	if maximum >= 0 && len(out) > maximum {
		out = out[:maximum]
	}
	return out
}

// canonicalClaims treats claim order as irrelevant while preserving each fact and lineage tuple.
func canonicalClaims(values []contractcatalog.Claim) []contractcatalog.Claim {
	out := deepClone(values)
	for i := range out {
		out[i].Provenance = canonicalProvenance(out[i].Provenance, len(out[i].Provenance))
	}
	sort.Slice(out, func(i, j int) bool {
		return fmt.Sprint(out[i].Kind, "\x00", out[i].Field, "\x00", out[i].Fingerprint) < fmt.Sprint(out[j].Kind, "\x00", out[j].Field, "\x00", out[j].Fingerprint)
	})
	return out
}

// sortedClasses canonicalizes source epistemic roles as a set.
func sortedClasses(values []contractcatalog.SourceClass) []contractcatalog.SourceClass {
	out := append([]contractcatalog.SourceClass(nil), values...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// sortedUnique canonicalizes closed cause and dependency code collections.
func sortedUnique(values []string) []string {
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

// cloneOperationPointer isolates nested schemas through the JSON-domain representation.
func cloneOperationPointer(value *contractcatalog.OperationDescriptor) *contractcatalog.OperationDescriptor {
	if value == nil {
		return nil
	}
	cloned := deepClone(*value)
	return &cloned
}

// incrementCount updates the immutable artifact summary for one emitted item.
func incrementCount(counts *ReviewCounts, kind ChangeKind) {
	switch kind {
	case ChangeAdded:
		counts.Added++
	case ChangeModified:
		counts.Modified++
	case ChangeRemoved:
		counts.Removed++
	case ChangeUncertain:
		counts.Uncertain++
	}
}
