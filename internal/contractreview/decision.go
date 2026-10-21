package contractreview

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// DecisionInput contains only the CAS and choice data supplied by an authenticated adapter.
type DecisionInput struct {
	ExpectedRevision uint64         `json:"expected_revision"`
	Whole            WholeAction    `json:"whole,omitempty"`
	Items            []ItemDecision `json:"items,omitempty"`
	IdempotencyKey   string         `json:"idempotency_key"`
	// AuthenticatedGitHubActorID is injected only by the authenticated GitHub boundary.
	AuthenticatedGitHubActorID int64 `json:"-"`
}

// ReduceDecision validates one all-or-nothing human command and derives a projection only when complete.
func ReduceDecision(ctx context.Context, artifact ReviewArtifact, currentRevision uint64, prior *DecisionRevision, input DecisionInput, actor string, now time.Time, candidate CandidateSnapshot, baseline BaselineSnapshot, policyDigest string) (DecisionRevision, ReviewState, *ApprovedProjection, error) {
	if err := cancellation(ctx, "decision"); err != nil {
		return DecisionRevision{}, "", nil, err
	}
	if input.ExpectedRevision != currentRevision {
		return DecisionRevision{}, "", nil, typedError(CategoryConflict, "decision.revision", nil)
	}
	if actor == "" || len(actor) > 128 || input.IdempotencyKey == "" || len(input.IdempotencyKey) > 128 || now.Location() != time.UTC || !validDigest(policyDigest) {
		return DecisionRevision{}, "", nil, typedError(CategoryInvalidInput, "decision", nil)
	}
	if (input.Whole == "") == (len(input.Items) == 0) {
		return DecisionRevision{}, "", nil, typedError(CategoryInvalidInput, "decision.mode", nil)
	}
	itemByID := map[string]ReviewItem{}
	for _, item := range artifact.Items {
		itemByID[item.ID] = item
	}
	effective := map[string]DecisionValue{}
	if prior != nil {
		for id, value := range prior.Effective {
			effective[id] = value
		}
	}
	if input.Whole != "" {
		if input.Whole != WholeAccept && input.Whole != WholeReject {
			return DecisionRevision{}, "", nil, typedError(CategoryInvalidInput, "decision.whole", nil)
		}
		value := DecisionAccept
		if input.Whole == WholeReject {
			value = DecisionReject
		}
		for id := range itemByID {
			effective[id] = value
		}
	} else {
		seen := map[string]bool{}
		for _, choice := range input.Items {
			if seen[choice.ItemID] || itemByID[choice.ItemID].ID == "" || choice.Value != DecisionAccept && choice.Value != DecisionReject {
				return DecisionRevision{}, "", nil, typedError(CategoryInvalidInput, "decision.items", nil)
			}
			seen[choice.ItemID] = true
			effective[choice.ItemID] = choice.Value
		}
		for id, value := range effective {
			if value != DecisionAccept {
				continue
			}
			for _, dependency := range itemByID[id].Dependencies {
				if effective[dependency] != DecisionAccept {
					return DecisionRevision{}, "", nil, typedError(CategoryInvalidInput, "decision.dependencies", nil)
				}
			}
		}
	}
	requestDigest, err := digestValue(ctx, struct {
		Artifact      string
		Input         DecisionInput
		Actor         string
		GitHubActorID int64
	}{artifact.Digest, input, actor, input.AuthenticatedGitHubActorID}, artifact.Limits.MaxBytes)
	if err != nil {
		return DecisionRevision{}, "", nil, err
	}
	revision := DecisionRevision{Revision: currentRevision + 1, ActorID: actor, GitHubActorID: input.AuthenticatedGitHubActorID, DecidedAt: now, Whole: input.Whole, Items: append([]ItemDecision(nil), input.Items...), Effective: effective, IdempotencyKey: input.IdempotencyKey, RequestDigest: requestDigest}
	sort.Slice(revision.Items, func(i, j int) bool { return revision.Items[i].ItemID < revision.Items[j].ItemID })
	revision.Digest, err = digestValue(ctx, struct {
		Revision      uint64
		Actor         string
		GitHubActorID int64
		At            time.Time
		Whole         WholeAction
		Items         []ItemDecision
		Effective     map[string]DecisionValue
		Request       string
	}{revision.Revision, actor, revision.GitHubActorID, now, revision.Whole, revision.Items, effective, requestDigest}, artifact.Limits.MaxBytes)
	if err != nil {
		return DecisionRevision{}, "", nil, err
	}
	if len(effective) < len(artifact.Items) {
		return revision, StatePartiallyDecided, nil, nil
	}
	accepted := len(artifact.Items) == 0 && input.Whole == WholeAccept
	for _, value := range effective {
		accepted = accepted || value == DecisionAccept
	}
	if input.Whole == WholeReject || !accepted {
		return revision, StateRejected, nil, nil
	}
	projection, err := projectAccepted(ctx, artifact, effective, candidate, baseline, revision, policyDigest)
	if err != nil {
		return DecisionRevision{}, "", nil, err
	}
	return revision, StateApproved, &projection, nil
}

// projectAccepted applies accepted atomic differences to an isolated baseline and preserves pending/rejected values.
func projectAccepted(ctx context.Context, artifact ReviewArtifact, choices map[string]DecisionValue, candidate CandidateSnapshot, baseline BaselineSnapshot, revision DecisionRevision, policyDigest string) (ApprovedProjection, error) {
	operations := operationMap(baseline.Catalog.Operations())
	items := projectionOrder(artifact.Items)
	for _, item := range items {
		if err := cancellation(ctx, "projection"); err != nil {
			return ApprovedProjection{}, err
		}
		if choices[item.ID] != DecisionAccept {
			continue
		}
		switch item.Path {
		case "operation":
			if item.After == nil {
				delete(operations, item.Operation)
			} else {
				operations[item.Operation] = deepClone(*item.After)
			}
		default:
			current, ok := operations[item.Operation]
			if !ok || item.After == nil {
				return ApprovedProjection{}, typedError(CategoryInvalidInput, "projection.dependencies", nil)
			}
			applyDimension(&current, *item.After, item.Path)
			operations[item.Operation] = current
		}
	}
	keys := make([]contractcatalog.OperationKey, 0, len(operations))
	for key := range operations {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return operationKeyString(keys[i]) < operationKeyString(keys[j]) })
	projected := make([]contractcatalog.OperationDescriptor, 0, len(keys))
	for _, key := range keys {
		projected = append(projected, operations[key])
	}
	schemes, schemeErr := schemesForProjectedOperations(projected)
	if schemeErr != nil {
		return ApprovedProjection{}, schemeErr
	}
	projection := ApprovedProjection{Operations: projected, SecuritySchemes: schemes, Sources: append(baseline.Catalog.Sources(), candidate.Catalog.Sources()...), Conflicts: append(baseline.Catalog.Conflicts(), candidate.Catalog.Conflicts()...), Omissions: addOmissions(baseline.Catalog.Omissions(), candidate.Catalog.Omissions()), Diagnostics: append(baseline.Catalog.Diagnostics(), candidate.Catalog.Diagnostics()...)}
	if err := validateCatalogBounds(ctx, []contractcatalog.OperationCatalog{candidate.Catalog, baseline.Catalog}, artifact.Limits); err != nil {
		return ApprovedProjection{}, err
	}
	projection = deepClone(projection)
	projectionDigest, err := digestValue(ctx, struct {
		Operations  []contractcatalog.OperationDescriptor
		Schemes     map[string]contractcatalog.SecurityScheme
		Sources     []contractcatalog.SourceSummary
		Conflicts   []contractcatalog.Conflict
		Omissions   contractcatalog.OmissionCounts
		Diagnostics []contractcatalog.SourceDiagnostic
	}{projection.Operations, projection.SecuritySchemes, projection.Sources, projection.Conflicts, projection.Omissions, projection.Diagnostics}, artifact.Limits.MaxBytes)
	if err != nil {
		return ApprovedProjection{}, err
	}
	projection.Binding = ApprovalBinding{ReviewID: artifact.ID, ReviewDigest: artifact.Digest, DecisionRevision: revision.Revision, DecisionDigest: revision.Digest, Candidate: artifact.Candidate, Baseline: artifact.Baseline, Sources: append([]contractcatalog.SourceSummary(nil), artifact.Sources...), Manifests: deepClone(artifact.Manifests), Analyzers: append([]AnalyzerIdentity(nil), artifact.Analyzers...), ProjectionDigest: projectionDigest, PolicyDigest: policyDigest, GitHub: deepClone(artifact.GitHub), GitHubActorID: revision.GitHubActorID}
	return deepClone(projection), nil
}

// projectionOrder applies allOf removals from highest index to lowest so stable review paths cannot shift mid-reduction.
func projectionOrder(values []ReviewItem) []ReviewItem {
	out := append([]ReviewItem(nil), values...)
	sort.SliceStable(out, func(i, j int) bool {
		leftParent, leftIndex, leftRemoval := allOfRemoval(out[i])
		rightParent, rightIndex, rightRemoval := allOfRemoval(out[j])
		leftGroup, rightGroup := out[i].Path, out[j].Path
		if leftRemoval {
			leftGroup = leftParent + "/allOf"
		}
		if rightRemoval {
			rightGroup = rightParent + "/allOf"
		}
		leftOperation, rightOperation := operationKeyString(out[i].Operation), operationKeyString(out[j].Operation)
		if leftOperation != rightOperation {
			return leftOperation < rightOperation
		}
		if leftGroup != rightGroup {
			return leftGroup < rightGroup
		}
		if leftRemoval && rightRemoval {
			return leftIndex > rightIndex
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// allOfRemoval identifies one candidate-side deletion and its stable parent/index for safe ordering.
func allOfRemoval(item ReviewItem) (string, int, bool) {
	marker := "/allOf/"
	position := strings.LastIndex(item.Path, marker)
	if position < 0 || item.After == nil || schemaDimensionValue(*item.After, item.Path) != nil {
		return "", 0, false
	}
	index := 0
	if _, err := fmt.Sscanf(item.Path[position+len(marker):], "%d", &index); err != nil {
		return "", 0, false
	}
	return item.Path[:position], index, true
}

// schemaDimensionValue resolves a schema path from a full operation for projection ordering decisions.
func schemaDimensionValue(operation contractcatalog.OperationDescriptor, path string) any {
	return dimensionValue(operation, path)
}

// schemesForProjectedOperations derives global schemes exclusively from operations retained by the decision.
func schemesForProjectedOperations(operations []contractcatalog.OperationDescriptor) (map[string]contractcatalog.SecurityScheme, error) {
	out := map[string]contractcatalog.SecurityScheme{}
	for _, operation := range operations {
		for name, scheme := range operation.SecuritySchemes {
			if prior, ok := out[name]; ok && !decisionJSONEqual(prior, scheme) {
				return nil, typedError(CategoryInvalidInput, "projection.security_schemes", nil)
			}
			out[name] = deepClone(scheme)
		}
	}
	return out, nil
}

// applyDimension replaces exactly one independently reviewed field on an operation.
func applyDimension(target *contractcatalog.OperationDescriptor, source contractcatalog.OperationDescriptor, path string) {
	parts := strings.Split(path, "/")
	if len(parts) == 2 && parts[0] == "parameters" {
		key := decodePathSegment(parts[1])
		values := parameterMap(source.Parameters)
		next, exists := values[key]
		filtered := target.Parameters[:0]
		for _, value := range target.Parameters {
			if value.In+"\x00"+value.Name != key {
				filtered = append(filtered, value)
			}
		}
		target.Parameters = filtered
		if exists {
			target.Parameters = append(target.Parameters, deepClone(next))
			target.Parameters = canonicalParameters(target.Parameters)
		}
		return
	}
	if len(parts) >= 3 && parts[0] == "parameters" {
		key := decodePathSegment(parts[1])
		targetValues := parameterMap(target.Parameters)
		sourceValues := parameterMap(source.Parameters)
		parameter, ok := targetValues[key]
		sourceParameter, sourceOK := sourceValues[key]
		if !ok || !sourceOK {
			return
		}
		if parts[2] == "metadata" {
			parameter.Name, parameter.In, parameter.Required = sourceParameter.Name, sourceParameter.In, sourceParameter.Required
		} else if parts[2] == "schema" {
			applySchemaPath(&parameter.Schema, sourceParameter.Schema, parts[3:])
		} else if len(parts) >= 5 && parts[2] == "content" {
			if parameter.Content == nil {
				parameter.Content = map[string]contractcatalog.MediaType{}
			}
			mediaName := decodePathSegment(parts[3])
			media := parameter.Content[mediaName]
			sourceMedia := sourceParameter.Content[mediaName]
			applySchemaPath(&media.Schema, sourceMedia.Schema, parts[5:])
			parameter.Content[mediaName] = media
		}
		for index, value := range target.Parameters {
			if value.In+"\x00"+value.Name == key {
				target.Parameters[index] = parameter
				return
			}
		}
	}
	if len(parts) >= 2 && parts[0] == "request_body" {
		if parts[1] == "required" {
			if source.RequestBody == nil {
				target.RequestBody = nil
			} else {
				if target.RequestBody == nil {
					target.RequestBody = &contractcatalog.RequestBody{}
				}
				target.RequestBody.Required = source.RequestBody.Required
			}
			return
		}
		if len(parts) == 3 && parts[1] == "content" {
			if target.RequestBody == nil {
				target.RequestBody = &contractcatalog.RequestBody{}
			}
			if target.RequestBody.Content == nil {
				target.RequestBody.Content = map[string]contractcatalog.MediaType{}
			}
			media := decodePathSegment(parts[2])
			value, ok := bodyContent(source.RequestBody)[media]
			if ok {
				target.RequestBody.Content[media] = deepClone(value)
			} else {
				delete(target.RequestBody.Content, media)
			}
			return
		}
		if len(parts) >= 4 && parts[1] == "content" && target.RequestBody != nil && source.RequestBody != nil {
			mediaName := decodePathSegment(parts[2])
			media := target.RequestBody.Content[mediaName]
			sourceMedia := source.RequestBody.Content[mediaName]
			applySchemaPath(&media.Schema, sourceMedia.Schema, parts[4:])
			target.RequestBody.Content[mediaName] = media
			return
		}
	}
	if len(parts) >= 2 && parts[0] == "responses" {
		if target.Responses == nil {
			target.Responses = map[string]contractcatalog.Response{}
		}
		selector := decodePathSegment(parts[1])
		sourceResponse, exists := source.Responses[selector]
		if len(parts) == 2 {
			if exists {
				target.Responses[selector] = deepClone(sourceResponse)
			} else {
				delete(target.Responses, selector)
			}
			return
		}
		response := target.Responses[selector]
		if len(parts) == 4 && parts[2] == "headers" {
			if response.Headers == nil {
				response.Headers = map[string]contractcatalog.Header{}
			}
			name := decodePathSegment(parts[3])
			value, ok := sourceResponse.Headers[name]
			if ok {
				response.Headers[name] = deepClone(value)
			} else {
				delete(response.Headers, name)
			}
		}
		if len(parts) == 4 && parts[2] == "content" {
			if response.Content == nil {
				response.Content = map[string]contractcatalog.MediaType{}
			}
			media := decodePathSegment(parts[3])
			value, ok := sourceResponse.Content[media]
			if ok {
				response.Content[media] = deepClone(value)
			} else {
				delete(response.Content, media)
			}
		}
		if len(parts) >= 5 && parts[2] == "headers" {
			name := decodePathSegment(parts[3])
			header := response.Headers[name]
			sourceHeader := sourceResponse.Headers[name]
			if parts[4] == "metadata" {
				header.Required = sourceHeader.Required
			} else if parts[4] == "schema" {
				applySchemaPath(&header.Schema, sourceHeader.Schema, parts[5:])
			}
			response.Headers[name] = header
		}
		if len(parts) >= 5 && parts[2] == "content" {
			mediaName := decodePathSegment(parts[3])
			media := response.Content[mediaName]
			sourceMedia := sourceResponse.Content[mediaName]
			applySchemaPath(&media.Schema, sourceMedia.Schema, parts[5:])
			response.Content[mediaName] = media
		}
		target.Responses[selector] = response
		return
	}
	if len(parts) == 2 && parts[0] == "security_schemes" {
		if target.SecuritySchemes == nil {
			target.SecuritySchemes = map[string]contractcatalog.SecurityScheme{}
		}
		name := decodePathSegment(parts[1])
		value, ok := source.SecuritySchemes[name]
		if ok {
			target.SecuritySchemes[name] = deepClone(value)
		} else {
			delete(target.SecuritySchemes, name)
		}
		return
	}
	switch path {
	case "logical_id":
		target.LogicalID = source.LogicalID
	case "security":
		target.Security = deepClone(source.Security)
	case "declarative_constraints":
		target.DeclarativeConstraints = deepClone(source.DeclarativeConstraints)
	case "metadata":
		target.Provenance = deepClone(source.Provenance)
		target.Confidence = source.Confidence
		target.Completeness = source.Completeness
		target.Classes = deepClone(source.Classes)
		target.Claims = deepClone(source.Claims)
	}
}

// applySchemaPath replaces exactly one recursive schema node or scalar attribute.
func applySchemaPath(target **contractcatalog.Schema, source *contractcatalog.Schema, parts []string) {
	if len(parts) == 0 {
		*target = deepClone(source)
		return
	}
	if source == nil {
		return
	}
	if *target == nil {
		*target = &contractcatalog.Schema{}
	}
	value := *target
	switch parts[0] {
	case "id":
		value.ID = source.ID
	case "type":
		value.Type = source.Type
	case "format":
		value.Format = source.Format
	case "required":
		value.Required = deepClone(source.Required)
	case "enum":
		value.Enum = deepClone(source.Enum)
	case "min_properties":
		value.MinProperties = deepClone(source.MinProperties)
	case "nullable":
		value.Nullable = source.Nullable
	case "read_only":
		value.ReadOnly = source.ReadOnly
	case "write_only":
		value.WriteOnly = source.WriteOnly
	case "items":
		applySchemaPath(&value.Items, source.Items, parts[1:])
	case "properties":
		if len(parts) < 2 {
			return
		}
		if value.Properties == nil {
			value.Properties = map[string]*contractcatalog.Schema{}
		}
		name := decodePathSegment(parts[1])
		sourceChild := source.Properties[name]
		if sourceChild == nil && len(parts) == 2 {
			delete(value.Properties, name)
		} else {
			child := value.Properties[name]
			applySchemaPath(&child, sourceChild, parts[2:])
			value.Properties[name] = child
		}
	case "allOf":
		if len(parts) < 2 {
			return
		}
		index := 0
		_, _ = fmt.Sscanf(parts[1], "%d", &index)
		if index >= len(source.AllOf) {
			if index < len(value.AllOf) {
				value.AllOf = append(value.AllOf[:index], value.AllOf[index+1:]...)
			}
			break
		}
		for len(value.AllOf) <= index {
			value.AllOf = append(value.AllOf, nil)
		}
		applySchemaPath(&value.AllOf[index], source.AllOf[index], parts[2:])
	}
}

// addOmissions sums explicit loss counters without converting uncertainty into evidence.
func addOmissions(a, b contractcatalog.OmissionCounts) contractcatalog.OmissionCounts {
	return contractcatalog.OmissionCounts{Examples: a.Examples + b.Examples, Extensions: a.Extensions + b.Extensions, Documentation: a.Documentation + b.Documentation, Unselected: a.Unselected + b.Unselected, SizeLimit: a.SizeLimit + b.SizeLimit}
}

// projectionDigest recalculates the exact immutable content identity before use or export.
func projectionDigest(ctx context.Context, projection ApprovedProjection, limit int) (string, error) {
	return digestValue(ctx, struct {
		Operations  []contractcatalog.OperationDescriptor
		Schemes     map[string]contractcatalog.SecurityScheme
		Sources     []contractcatalog.SourceSummary
		Conflicts   []contractcatalog.Conflict
		Omissions   contractcatalog.OmissionCounts
		Diagnostics []contractcatalog.SourceDiagnostic
	}{projection.Operations, projection.SecuritySchemes, projection.Sources, projection.Conflicts, projection.Omissions, projection.Diagnostics}, limit)
}

// decisionJSONEqual compares canonical decision values without text parsing.
func decisionJSONEqual(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}
