package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/asierhr/featureProof/internal/environment"
	"github.com/asierhr/featureProof/internal/harness"
)

// minimumOutputBytes guarantees the normalized error envelope itself fits the configured budget.
const minimumOutputBytes = 1024

// authority is the detached runtime index derived exclusively from verified composition data.
type authority struct {
	namespace, namespaceUID, catalogDigest string
	resources                              map[string]Resource
	relationships                          map[string]Relationship
}

// Tool owns immutable observation authority while every Execute owns pagination and snapshot state.
type Tool struct {
	target                              environment.ValidationTarget
	binding                             environment.Binding
	identity                            IdentityBinding
	authority                           authority
	timeout                             time.Duration
	maxPages                            int
	pageSize                            int64
	maxItems, maxEvents, maxOutputBytes int
	allowedReasons                      map[string]struct{}
	clock                               Clock
	withTimeout                         TimeoutFactory
	reader                              Reader
}

// New validates every trusted dependency and snapshots authority before registration.
func New(config Config) (*Tool, error) {
	if config.Clock == nil || config.WithTimeout == nil || config.Reader == nil {
		return nil, &ConfigError{Field: "dependencies"}
	}
	if config.Timeout <= 0 || config.MaxPages <= 0 || config.PageSize <= 0 || config.MaxItems <= 0 || config.MaxEvents <= 0 || config.MaxOutputBytes < minimumOutputBytes {
		return nil, &ConfigError{Field: "limits"}
	}
	now := config.Clock.Now()
	if environment.VerifyTarget(config.Target, now) != nil || environment.VerifyBinding(config.Binding, config.Target, now) != nil || config.Target.ProviderID != "kubernetes" || config.Target.ReservationID == "" || !containsString(config.Target.Capabilities, ToolName) {
		return nil, &ConfigError{Field: "target_binding"}
	}
	sealed, err := SealCatalog(config.Catalog)
	if err != nil || sealed.Digest != config.Catalog.Digest || sealed.TargetDigest != config.Target.Digest || sealed.SelectionDigest != config.Target.SelectionDigest || sealed.ProviderVersion != config.Target.ProviderVersion || sealed.Namespace != config.Target.EnvironmentID || sealed.NamespaceUID != config.Target.ReservationID {
		return nil, &ConfigError{Field: "catalog_authority"}
	}
	sealedIdentity, err := SealIdentityBinding(config.Identity)
	if err != nil || sealedIdentity.Digest != config.Identity.Digest || sealedIdentity.Namespace != config.Target.EnvironmentID {
		return nil, &ConfigError{Field: "identity"}
	}
	resources := make(map[string]Resource, len(sealed.Resources))
	for _, resource := range sealed.Resources {
		resources[resource.Alias] = resource
	}
	relationships := make(map[string]Relationship, len(sealed.Relationships))
	for _, relationship := range sealed.Relationships {
		relationships[relationship.Alias] = relationship
	}
	reasons := make(map[string]struct{}, len(config.AllowedReasons))
	for _, reason := range config.AllowedReasons {
		if !boundedID.MatchString(reason) {
			return nil, &ConfigError{Field: "allowed_reasons"}
		}
		if _, duplicate := reasons[reason]; duplicate {
			return nil, &ConfigError{Field: "allowed_reasons"}
		}
		reasons[reason] = struct{}{}
	}
	return &Tool{target: cloneTarget(config.Target), binding: config.Binding, identity: sealedIdentity, authority: authority{namespace: sealed.Namespace, namespaceUID: sealed.NamespaceUID, catalogDigest: sealed.Digest, resources: resources, relationships: relationships}, timeout: config.Timeout, maxPages: config.MaxPages, pageSize: config.PageSize, maxItems: config.MaxItems, maxEvents: config.MaxEvents, maxOutputBytes: config.MaxOutputBytes, allowedReasons: reasons, clock: config.Clock, withTimeout: config.WithTimeout, reader: config.Reader}, nil
}

// Spec exposes a read-only idempotent capability through the standard registry.
func (t *Tool) Spec() harness.ToolSpec {
	return harness.ToolSpec{Name: ToolName, Validate: t.Validate, Effects: false, Idempotent: true, Tool: t}
}

// Validate accepts exactly one closed check kind and one authorized logical alias.
func (t *Tool) Validate(input map[string]any) error {
	if len(input) != 2 {
		return &InputError{Field: "$", Category: "shape"}
	}
	rawCheck, checkOK := input["check_kind"].(string)
	alias, aliasOK := input["resource_alias"].(string)
	if !checkOK {
		return &InputError{Field: "check_kind", Category: "type"}
	}
	if !aliasOK {
		return &InputError{Field: "resource_alias", Category: "type"}
	}
	check := CheckKind(rawCheck)
	if check == CheckConnectivity {
		if _, exists := t.authority.relationships[alias]; !exists {
			return &InputError{Field: "resource_alias", Category: "unauthorized"}
		}
		return nil
	}
	resource, exists := t.authority.resources[alias]
	if !exists || !containsCheck(resource.Checks, check) {
		return &InputError{Field: "resource_alias", Category: "unauthorized"}
	}
	return nil
}

// Execute revalidates authority, observes one bounded snapshot, and returns normalized evidence.
func (t *Tool) Execute(ctx context.Context, call harness.ToolCall) (harness.ToolResult, error) {
	if err := t.Validate(call.Input); err != nil {
		return harness.ToolResult{}, err
	}
	if call.RunID != t.target.RunID {
		return t.operationFailure(OutcomeIntegrity, false, nil, baseResult(CheckKind(call.Input["check_kind"].(string)), call.Input["resource_alias"].(string)))
	}
	if environment.VerifyTarget(t.target, t.clock.Now()) != nil || environment.VerifyBinding(t.binding, t.target, t.clock.Now()) != nil {
		return t.operationFailure(OutcomeIntegrity, false, nil, baseResult(CheckKind(call.Input["check_kind"].(string)), call.Input["resource_alias"].(string)))
	}
	child, cancel := t.withTimeout(ctx, t.timeout)
	defer cancel()
	check, alias := CheckKind(call.Input["check_kind"].(string)), call.Input["resource_alias"].(string)
	result := baseResult(check, alias)
	if err := child.Err(); err != nil {
		return t.mapFailure(ctx, err, result)
	}
	initial, err := t.reader.GetNamespace(child, t.authority.namespace)
	if err != nil {
		return t.mapFailure(ctx, namespaceFailure(err), result)
	}
	if err := child.Err(); err != nil {
		return t.mapFailure(ctx, err, result)
	}
	if initial.UID != t.authority.namespaceUID {
		return t.operationFailure(OutcomeIntegrity, false, nil, result)
	}
	result.Versions = append(result.Versions, version(alias+":namespace", "Namespace", initial.UID, initial.ResourceVersion))
	if err := t.observe(child, check, alias, &result); err != nil {
		var readerErr *ReaderError
		if errors.As(err, &readerErr) && readerErr.Category == OutcomeAbsent {
			if result.Observation == nil {
				return t.completeAbsence(ctx, child, initial, &result)
			}
			result.Outcome = Outcome{Category: OutcomeInsufficient, Complete: false, Drift: true}
			return t.operationFailure(OutcomeInsufficient, false, nil, result)
		}
		return t.mapFailure(ctx, err, result)
	}
	if err := child.Err(); err != nil {
		return t.mapFailure(ctx, err, result)
	}
	finalAnchor, err := t.reader.GetNamespace(child, t.authority.namespace)
	if err != nil {
		return t.mapFailure(ctx, namespaceFailure(err), result)
	}
	if err := child.Err(); err != nil {
		return t.mapFailure(ctx, err, result)
	}
	if finalAnchor.UID != initial.UID {
		return t.operationFailure(OutcomeIntegrity, false, nil, result)
	}
	if finalAnchor.ResourceVersion != initial.ResourceVersion {
		result.Outcome = Outcome{Category: OutcomeInsufficient, Complete: false, Drift: true}
		result.Observation = nil
	}
	result.Versions = append(result.Versions, version(alias+":namespace-final", "Namespace", finalAnchor.UID, finalAnchor.ResourceVersion))
	if result.Outcome.Category == OutcomeInsufficient {
		return t.operationFailure(OutcomeInsufficient, false, nil, result)
	}
	return t.bounded(result)
}

// observe dispatches only to the finite check vocabulary selected during validation.
func (t *Tool) observe(ctx context.Context, check CheckKind, alias string, result *Result) error {
	switch check {
	case CheckDeployment:
		return t.observeDeployment(ctx, t.authority.resources[alias], result)
	case CheckPodRestarts:
		return t.observePods(ctx, t.authority.resources[alias], result)
	case CheckJob:
		return t.observeJob(ctx, t.authority.resources[alias], result)
	case CheckService:
		return t.observeService(ctx, t.authority.resources[alias], result)
	case CheckEvents:
		return t.observeEvents(ctx, t.authority.resources[alias], result)
	case CheckConnectivity:
		return t.observeConnectivity(ctx, t.authority.relationships[alias], result)
	default:
		return &ReaderError{Category: OutcomeIntegrity}
	}
}

// bounded enforces the final byte budget without presenting a prefix as complete evidence.
func (t *Tool) bounded(result Result) (harness.ToolResult, error) {
	output, category, failed := t.finalize(result)
	if failed {
		class := harness.ErrorPermanent
		readerErr := &ReaderError{Category: category}
		return harness.ToolResult{Output: output, ErrorClass: class}, &harness.OperationError{Class: class, Err: readerErr}
	}
	return harness.ToolResult{Output: output}, nil
}

// completeAbsence confirms the namespace anchor before exposing exact resource absence to assertions.
func (t *Tool) completeAbsence(parent, child context.Context, initial Namespace, result *Result) (harness.ToolResult, error) {
	if err := child.Err(); err != nil {
		return t.mapFailure(parent, err, *result)
	}
	finalAnchor, err := t.reader.GetNamespace(child, t.authority.namespace)
	if err != nil {
		return t.mapFailure(parent, namespaceFailure(err), *result)
	}
	if err := child.Err(); err != nil {
		return t.mapFailure(parent, err, *result)
	}
	if finalAnchor.UID != initial.UID {
		return t.operationFailure(OutcomeIntegrity, false, nil, *result)
	}
	result.Versions = append(result.Versions, version(result.ResourceAlias+":namespace-final", "Namespace", finalAnchor.UID, finalAnchor.ResourceVersion))
	if finalAnchor.ResourceVersion != initial.ResourceVersion {
		result.Outcome = Outcome{Category: OutcomeInsufficient, Complete: false, Drift: true}
		return t.operationFailure(OutcomeInsufficient, false, nil, *result)
	}
	result.Outcome = Outcome{Category: OutcomeAbsent, Complete: true}
	result.Observation, result.Error = nil, nil
	return t.bounded(*result)
}

// mapFailure translates cancellation and adapter categories into executor retry semantics.
func (t *Tool) mapFailure(parent context.Context, err error, result Result) (harness.ToolResult, error) {
	if parent.Err() != nil {
		return harness.ToolResult{}, parent.Err()
	}
	if errors.Is(err, context.Canceled) {
		return harness.ToolResult{}, context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return t.operationFailure(OutcomeTimeout, true, context.DeadlineExceeded, result)
	}
	var readerErr *ReaderError
	if errors.As(err, &readerErr) {
		return t.operationFailure(readerErr.Category, readerErr.Transient, readerErr.Cause, result)
	}
	return t.operationFailure(OutcomeIntegrity, false, nil, result)
}

// operationFailure builds safe structured evidence and the matching harness retry contract.
func (t *Tool) operationFailure(category OutcomeCategory, transient bool, cause error, result Result) (harness.ToolResult, error) {
	result.Outcome.Category = category
	result.Outcome.Complete = false
	result.Observation = nil
	result.Error = map[string]any{"category": string(category)}
	class := harness.ErrorPermanent
	if transient {
		class = harness.ErrorTransient
	}
	readerErr := &ReaderError{Category: category, Transient: transient, Cause: cause}
	output, _, _ := t.finalize(result)
	return harness.ToolResult{Output: output, ErrorClass: class}, &harness.OperationError{Class: class, Err: readerErr}
}

// finalize canonicalizes versions, authenticates the document, and enforces the byte budget for every outcome.
func (t *Tool) finalize(result Result) (map[string]any, OutcomeCategory, bool) {
	result.Versions = canonicalVersions(result.Versions)
	result.SnapshotDigest = snapshotDigest(result)
	encoded, err := json.Marshal(result)
	if err == nil && len(encoded) <= t.maxOutputBytes {
		return resultMap(result), result.Outcome.Category, false
	}
	category := result.Outcome.Category
	if result.Error == nil {
		category = OutcomeInsufficient
	}
	result.Observation, result.Versions = nil, nil
	result.Outcome = Outcome{Category: category, Complete: false, Drift: result.Outcome.Drift, Truncated: true}
	result.Truncations = []Truncation{{Section: "output", Observed: len(encoded), Retained: 0}}
	result.Error = map[string]any{"category": string(category)}
	result.SnapshotDigest = snapshotDigest(result)
	encoded, _ = json.Marshal(result)
	if len(encoded) > t.maxOutputBytes {
		result.ResourceAlias = digest(result.ResourceAlias)[:16]
		result.SnapshotDigest = snapshotDigest(result)
	}
	return resultMap(result), category, true
}

// canonicalVersions sorts and deduplicates exact observed identities before digesting any result category.
func canonicalVersions(values []Version) []Version {
	output := append([]Version(nil), values...)
	sort.Slice(output, func(left, right int) bool {
		leftVersion, rightVersion := output[left], output[right]
		if leftVersion.ResourceAlias != rightVersion.ResourceAlias {
			return leftVersion.ResourceAlias < rightVersion.ResourceAlias
		}
		if leftVersion.Kind != rightVersion.Kind {
			return leftVersion.Kind < rightVersion.Kind
		}
		if leftVersion.UIDDigest != rightVersion.UIDDigest {
			return leftVersion.UIDDigest < rightVersion.UIDDigest
		}
		return leftVersion.ResourceVersion < rightVersion.ResourceVersion
	})
	deduplicated := output[:0]
	for _, value := range output {
		if len(deduplicated) == 0 || value != deduplicated[len(deduplicated)-1] {
			deduplicated = append(deduplicated, value)
		}
	}
	return deduplicated
}

// baseResult initializes a complete observed envelope before a check projects data.
func baseResult(check CheckKind, alias string) Result {
	return Result{Schema: SchemaVersion, CheckKind: check, ResourceAlias: alias, Outcome: Outcome{Category: OutcomeObserved, Complete: true}, Versions: []Version{}, Truncations: []Truncation{}}
}

// resultMap converts the typed envelope to the JSON-compatible representation expected by evidence.
func resultMap(result Result) map[string]any {
	encoded, _ := json.Marshal(result)
	output := map[string]any{}
	_ = json.Unmarshal(encoded, &output)
	return output
}

// snapshotDigest excludes the digest field itself while retaining canonical observation content.
func snapshotDigest(result Result) string {
	result.SnapshotDigest = ""
	projection := resultMap(result)
	delete(projection, "snapshot_digest")
	return digest(projection)
}

// version minimizes UID exposure while retaining an auditable stable identity.
func version(alias, kind, uid, resourceVersion string) Version {
	return Version{ResourceAlias: alias, Kind: kind, UIDDigest: digest(uid), ResourceVersion: resourceVersion}
}

// containsCheck tests membership in the already canonical check allowlist.
func containsCheck(values []CheckKind, wanted CheckKind) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// namespaceFailure treats loss of the authority anchor as integrity failure rather than assertable resource absence.
func namespaceFailure(err error) error {
	var readerErr *ReaderError
	if errors.As(err, &readerErr) && readerErr.Category == OutcomeAbsent {
		return &ReaderError{Category: OutcomeIntegrity, Cause: readerErr.Cause}
	}
	return err
}

// cloneTarget detaches every mutable authority slice from caller-owned composition memory.
func cloneTarget(value environment.ValidationTarget) environment.ValidationTarget {
	value.Capabilities = append([]string(nil), value.Capabilities...)
	value.Network.Schemes = append([]string(nil), value.Network.Schemes...)
	value.Network.Hosts = append([]string(nil), value.Network.Hosts...)
	value.Network.Ports = append([]int(nil), value.Network.Ports...)
	value.Network.BasePaths = append([]string(nil), value.Network.BasePaths...)
	return value
}
