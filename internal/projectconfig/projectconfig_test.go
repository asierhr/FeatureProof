package projectconfig

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// testDigest is a syntactically valid deterministic snapshot identity.
const testDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// testCatalogLimits supplies explicit finite budgets for unit catalog fixtures.
func testCatalogLimits() CatalogLimits {
	return CatalogLimits{MaxDescriptors: 16, MaxOperationsPerDescriptor: 16, MaxCapabilitiesPerDescriptor: 16, MaxDependenciesPerDescriptor: 16, MaxDependencyEdges: 64, MaxGraphWork: 80}
}

// validDocumentJSON returns the smallest semantically complete v1 document.
func validDocumentJSON() []byte {
	return []byte(`{"schema":"featureproof.project-config/v1","organization_id":"organization","project_id":"project","repository_id":"repo","references":[{"kind":"contract","id":"contract","version":1,"digest":"` + testDigest + `"}],"constraints":{"limits":{"duration_seconds":10,"tool_calls":2,"model_tokens":100,"replans":1},"approval":"required","retention_days":7,"capabilities":["read"]}}`)
}

// TestLoadStrictRejectsAmbiguityAndSecrets proves the public decoder fails closed.
func TestLoadStrictRejectsAmbiguityAndSecrets(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"unknown":   strings.Replace(string(validDocumentJSON()), `"schema":`, `"extra":1,"schema":`, 1),
		"duplicate": strings.Replace(string(validDocumentJSON()), `"schema":`, `"schema":"x","schema":`, 1),
		"trailing":  string(validDocumentJSON()) + `{}`,
		"secret":    strings.Replace(string(validDocumentJSON()), `"schema":`, `"token":"canary","schema":`, 1),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadStrict(strings.NewReader(raw), MaxCanonicalBytes); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

// TestCanonicalizeIsDeterministic proves order-only differences have one identity.
func TestCanonicalizeIsDeterministic(t *testing.T) {
	t.Parallel()
	first, err := LoadStrict(bytes.NewReader(validDocumentJSON()), MaxCanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	reordered := bytes.Replace(validDocumentJSON(), []byte(`"capabilities":["read"]`), []byte(`"capabilities":["read","read"]`), 1)
	second, err := LoadStrict(bytes.NewReader(reordered), MaxCanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest || !bytes.Equal(first.Bytes, second.Bytes) {
		t.Fatal("semantic equivalents diverged")
	}
}

// TestAuthorityRejectsScopeBeforeCollaborators proves authorization is the first boundary.
func TestAuthorityRejectsScopeBeforeCollaborators(t *testing.T) {
	t.Parallel()
	key, _ := NewProjectKey("organization", "project", "repo")
	other, _ := NewProjectKey("other-organization", "other", "repo")
	authority, _ := IssueAuthority("actor", key, ActionValidate)
	catalog := &countingCatalog{}
	policy := &countingPolicy{}
	service, _ := NewService(unusedStore{}, catalog, policy, nil, ServiceConfig{CursorKey: []byte("01234567890123456789012345678901"), CursorTTL: time.Minute})
	if _, err := service.Validate(context.Background(), authority, other, validDocumentJSON()); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("got %v", err)
	}
	if catalog.calls != 0 || policy.calls != 0 {
		t.Fatal("collaborator called before authorization")
	}
}

// TestValidateAndMergeAppliesMonotonicPolicy proves defaults cannot widen ceilings.
func TestValidateAndMergeAppliesMonotonicPolicy(t *testing.T) {
	t.Parallel()
	canonical, err := LoadStrict(bytes.NewReader(validDocumentJSON()), MaxCanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := NewProjectKey("organization", "project", "repo")
	policy := OrganizationPolicySnapshot{Version: 1, Digest: testDigest, MaximumLimits: Limits{DurationSeconds: 10, ToolCalls: 2, ModelTokens: 100, Replans: 1}, ApprovalRequired: true, MinimumRetentionDays: 1, MaximumRetentionDays: 10, AllowedCapabilities: []string{"read"}}
	catalog := ResourceCatalogSnapshot{Limits: testCatalogLimits(), ProjectKey: key, Digest: testDigest, Descriptors: []ResourceDescriptor{{Kind: ResourceContract, ID: "contract", Version: 1, Digest: testDigest, ProjectKey: key}}}
	effective, err := ValidateAndMerge(canonical, policy, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if effective.Digest == "" || effective.Constraints.Approval != "required" {
		t.Fatal("invalid effective snapshot")
	}
	canonical.Document.Constraints.Limits.ToolCalls++
	canonical, _ = Canonicalize(canonical.Document)
	if _, err := ValidateAndMerge(canonical, policy, catalog); !IsCategory(err, CategoryIncompatibleReference) {
		t.Fatalf("got %v", err)
	}
}

// TestValidateAndMergeRejectsCatalogCyclesAndProviderMismatch covers cross-resource authority.
func TestValidateAndMergeRejectsCatalogCyclesAndProviderMismatch(t *testing.T) {
	t.Parallel()
	key, _ := NewProjectKey("organization", "project", "repo")
	policy := OrganizationPolicySnapshot{Version: 1, Digest: testDigest, MaximumLimits: Limits{DurationSeconds: 10, ToolCalls: 2, ModelTokens: 100, Replans: 1}, MinimumRetentionDays: 1, MaximumRetentionDays: 10, AllowedCapabilities: []string{"read"}}
	document := Document{Schema: SchemaV1, OrganizationID: key.OrganizationID, ProjectID: key.ProjectID, RepositoryID: key.RepositoryID, References: []ResourceReference{{Kind: ResourceFixture, ID: "a", Version: 1, Digest: testDigest}, {Kind: ResourceFixture, ID: "b", Version: 1, Digest: testDigest}}, Constraints: Constraints{Limits: Limits{DurationSeconds: 1}, Approval: "required", RetentionDays: 1}}
	canonical, err := Canonicalize(document)
	if err != nil {
		t.Fatal(err)
	}
	catalog := ResourceCatalogSnapshot{Limits: testCatalogLimits(), ProjectKey: key, Digest: testDigest, Descriptors: []ResourceDescriptor{{Kind: ResourceFixture, ID: "a", Version: 1, Digest: testDigest, ProjectKey: key, Dependencies: []ResourceReference{{Kind: ResourceFixture, ID: "b", Version: 1, Digest: testDigest}}}, {Kind: ResourceFixture, ID: "b", Version: 1, Digest: testDigest, ProjectKey: key, Dependencies: []ResourceReference{{Kind: ResourceFixture, ID: "a", Version: 1, Digest: testDigest}}}}}
	if _, err = ValidateAndMerge(canonical, policy, catalog); !IsCategory(err, CategoryIncompatibleReference) {
		t.Fatalf("cycle got %v", err)
	}
	document.References = []ResourceReference{{Kind: ResourceEnvironment, ID: "target", Version: 1, Digest: testDigest}, {Kind: ResourceRuntimeProfile, ID: "runtime", Version: 1, Digest: testDigest}}
	canonical, _ = Canonicalize(document)
	catalog.Descriptors = []ResourceDescriptor{{Kind: ResourceEnvironment, ID: "target", Version: 1, Digest: testDigest, ProjectKey: key, ProviderKind: "kubernetes"}, {Kind: ResourceRuntimeProfile, ID: "runtime", Version: 1, Digest: testDigest, ProjectKey: key, ProviderKind: "direct"}}
	if _, err = ValidateAndMerge(canonical, policy, catalog); !IsCategory(err, CategoryIncompatibleReference) {
		t.Fatalf("provider got %v", err)
	}
}

// TestCatalogStructuralBudgetsRejectEveryLimitPlusOne proves trusted snapshots cannot bypass bounded copy or graph work.
func TestCatalogStructuralBudgetsRejectEveryLimitPlusOne(t *testing.T) {
	t.Parallel()
	key, _ := NewProjectKey("organization", "project", "repo")
	base := ResourceCatalogSnapshot{ProjectKey: key, Digest: testDigest, Limits: CatalogLimits{MaxDescriptors: 2, MaxOperationsPerDescriptor: 2, MaxCapabilitiesPerDescriptor: 2, MaxDependenciesPerDescriptor: 2, MaxDependencyEdges: 2, MaxGraphWork: 4}, Descriptors: []ResourceDescriptor{{Kind: ResourceContract, ID: "one", Version: 1, Digest: testDigest, ProjectKey: key}}}
	values := func(prefix string, count int) []string {
		result := make([]string, count)
		for index := range result {
			result[index] = fmt.Sprintf("%s%d", prefix, index)
		}
		return result
	}
	references := func(count int) []ResourceReference {
		result := make([]ResourceReference, count)
		for index := range result {
			result[index] = ResourceReference{Kind: ResourceContract, ID: "one", Version: 1, Digest: testDigest}
		}
		return result
	}
	tests := map[string]func(*ResourceCatalogSnapshot){
		"descriptors": func(snapshot *ResourceCatalogSnapshot) {
			snapshot.Descriptors = append(snapshot.Descriptors, snapshot.Descriptors[0], snapshot.Descriptors[0])
		},
		"operations":   func(snapshot *ResourceCatalogSnapshot) { snapshot.Descriptors[0].Operations = values("op", 3) },
		"capabilities": func(snapshot *ResourceCatalogSnapshot) { snapshot.Descriptors[0].Capabilities = values("cap", 3) },
		"dependencies": func(snapshot *ResourceCatalogSnapshot) { snapshot.Descriptors[0].Dependencies = references(3) },
		"edges": func(snapshot *ResourceCatalogSnapshot) {
			snapshot.Limits.MaxDependenciesPerDescriptor = 3
			snapshot.Descriptors[0].Dependencies = references(3)
		},
		"graph_work": func(snapshot *ResourceCatalogSnapshot) {
			snapshot.Limits.MaxGraphWork = 2
			snapshot.Descriptors[0].Dependencies = references(2)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			snapshot := base.Clone()
			mutate(&snapshot)
			if _, err := NewSnapshotCatalog([]ResourceCatalogSnapshot{snapshot}); err == nil {
				t.Fatal("expected bounded rejection")
			}
		})
	}
	atLimit := base.Clone()
	atLimit.Descriptors[0].Operations = values("op", 2)
	atLimit.Descriptors[0].Capabilities = values("cap", 2)
	atLimit.Descriptors[0].Dependencies = references(2)
	if _, err := NewSnapshotCatalog([]ResourceCatalogSnapshot{atLimit}); err != nil {
		t.Fatalf("exact limits rejected: %v", err)
	}
}

// TestDocumentCapabilitiesRejectLimitPlusOne accepts the exact process ceiling and rejects one additional entry.
func TestDocumentCapabilitiesRejectLimitPlusOne(t *testing.T) {
	t.Parallel()
	document, err := LoadStrict(bytes.NewReader(validDocumentJSON()), MaxCanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	capabilities := make([]string, MaxDocumentCapabilities)
	for index := range capabilities {
		capabilities[index] = fmt.Sprintf("cap%d", index)
	}
	document.Document.Constraints.Capabilities = capabilities
	if _, err = Canonicalize(document.Document); err != nil {
		t.Fatalf("exact limit rejected: %v", err)
	}
	document.Document.Constraints.Capabilities = append(capabilities, "overflow")
	if _, err = Canonicalize(document.Document); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("limit+1 got %v", err)
	}
}

// TestHistoryCursorRejectsTamperScopeAndExpiry proves pagination state is private and bounded.
func TestHistoryCursorRejectsTamperScopeAndExpiry(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0).UTC()
	service := &Service{cursorKey: []byte("01234567890123456789012345678901"), cursorTTL: time.Minute, now: func() time.Time { return now }}
	key, _ := NewProjectKey("organization", "project", "repo")
	cursor := service.encodeCursor(key, 42)
	if value, err := service.decodeCursor(key, cursor); err != nil || value != 42 {
		t.Fatalf("round trip: %d %v", value, err)
	}
	other, _ := NewProjectKey("other-organization", "other", "repo")
	if _, err := service.decodeCursor(other, cursor); err == nil {
		t.Fatal("cross-project cursor accepted")
	}
	tamperedByte := byte('A')
	if cursor[len(cursor)-1] == tamperedByte {
		tamperedByte = 'B'
	}
	tampered := cursor[:len(cursor)-1] + string(tamperedByte)
	if _, err := service.decodeCursor(key, tampered); err == nil {
		t.Fatal("tampered cursor accepted")
	}
	service.now = func() time.Time { return now.Add(time.Minute) }
	if _, err := service.decodeCursor(key, cursor); err == nil {
		t.Fatal("expired cursor accepted")
	}
}

// TestSnapshotSourcesAreScopedAndDefensive proves composition cannot cross project authorities.
func TestSnapshotSourcesAreScopedAndDefensive(t *testing.T) {
	t.Parallel()
	key, _ := NewProjectKey("organization", "project", "repo")
	other, _ := NewProjectKey("other-organization", "other", "repo")
	catalog, err := NewSnapshotCatalog([]ResourceCatalogSnapshot{{Limits: testCatalogLimits(), ProjectKey: key, Digest: testDigest, Descriptors: []ResourceDescriptor{{Kind: ResourceContract, ID: "contract", Version: 1, Digest: testDigest, ProjectKey: key, Operations: []string{"read"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := catalog.Snapshot(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	first.Descriptors[0].Operations[0] = "mutated"
	second, _ := catalog.Snapshot(context.Background(), key)
	if second.Descriptors[0].Operations[0] != "read" {
		t.Fatal("catalog shared mutable state")
	}
	if _, err = catalog.Snapshot(context.Background(), other); !IsCategory(err, CategoryNotFound) {
		t.Fatalf("cross scope got %v", err)
	}
	policies, err := NewSnapshotPolicies([]PolicyForProject{{ProjectKey: key, Snapshot: OrganizationPolicySnapshot{Version: 1, Digest: testDigest, AllowedCapabilities: []string{"read"}}}})
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := policies.Snapshot(context.Background(), key)
	policy.AllowedCapabilities[0] = "mutated"
	again, _ := policies.Snapshot(context.Background(), key)
	if again.AllowedCapabilities[0] != "read" {
		t.Fatal("policy shared mutable state")
	}
}

// TestAdmissionResolverDerivesTrustedProjectScope proves surfaces cannot override project identity.
func TestAdmissionResolverDerivesTrustedProjectScope(t *testing.T) {
	t.Parallel()
	key, _ := NewProjectKey("organization", "project", "repo")
	authority, _ := IssueAuthority("actor", key, ActionAdmit)
	store := &bindingStore{}
	service, _ := NewService(store, &countingCatalog{}, &countingPolicy{}, nil, ServiceConfig{CursorKey: []byte("01234567890123456789012345678901"), CursorTTL: time.Minute})
	resolver, err := NewAdmissionResolver(service, []RepositoryAdmission{{RepositoryID: "repo", ProjectKey: key, Authority: authority}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = resolver.BindForWorkflow(context.Background(), "other", "run"); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("unknown repo got %v", err)
	}
	if store.calls != 0 {
		t.Fatal("store called for unknown repository")
	}
}

// TestClosedBoundaryErrors covers invalid construction, cancellation and integrity categories.
func TestClosedBoundaryErrors(t *testing.T) {
	t.Parallel()
	if _, err := LoadStrict(nil, 1); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nil loader %v", err)
	}
	if _, err := LoadStrict(strings.NewReader(`{"schema":`), MaxCanonicalBytes); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("syntax %v", err)
	}
	key, _ := NewProjectKey("organization", "project", "repo")
	if _, err := IssueAuthority("bad/token", key, ActionRead); !IsCategory(err, CategoryUnauthorized) {
		t.Fatalf("actor %v", err)
	}
	if _, err := NewService(nil, &countingCatalog{}, &countingPolicy{}, nil, ServiceConfig{}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("service %v", err)
	}
	authority, _ := IssueAuthority("actor", key, ActionValidate)
	service, _ := NewService(unusedStore{}, cancelledCatalog{}, &countingPolicy{}, nil, ServiceConfig{CursorKey: []byte("01234567890123456789012345678901"), CursorTTL: time.Minute})
	if _, err := service.Validate(context.Background(), authority, key, validDocumentJSON()); !IsCategory(err, CategoryCancelled) {
		t.Fatalf("cancel mapping %v", err)
	}
}

// TestSharedActivationAndEffectiveIntegrityContract covers the adapter-neutral corruption checks used by local and PostgreSQL reads.
func TestSharedActivationAndEffectiveIntegrityContract(t *testing.T) {
	t.Parallel()
	key, _ := NewProjectKey("organization", "project", "repo")
	activation, err := SealActivation(Activation{ProjectKey: key, Generation: 1, Version: 1, VersionDigest: testDigest, Reason: "publish", Actor: "actor", CreatedAt: time.Unix(100, 123456789)})
	if err != nil || activation.CreatedAt.Nanosecond()%1000 != 0 || ValidateActivation(activation, testDigest) != nil {
		t.Fatalf("valid activation: %+v %v", activation, err)
	}
	corrupt := activation
	corrupt.VersionDigest = strings.Repeat("b", 64)
	if err = ValidateActivation(corrupt, testDigest); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("corrupt activation: %v", err)
	}
	if _, err = SealActivation(Activation{}); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("invalid activation: %v", err)
	}
	canonical, err := LoadStrict(bytes.NewReader(validDocumentJSON()), MaxCanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	policy := OrganizationPolicySnapshot{Version: 1, Digest: testDigest, MaximumLimits: Limits{DurationSeconds: 10, ToolCalls: 2, ModelTokens: 100, Replans: 1}, ApprovalRequired: true, MinimumRetentionDays: 1, MaximumRetentionDays: 10, AllowedCapabilities: []string{"read"}}
	catalog := ResourceCatalogSnapshot{Limits: testCatalogLimits(), ProjectKey: key, Digest: testDigest, Descriptors: []ResourceDescriptor{{Kind: ResourceContract, ID: "contract", Version: 1, Digest: testDigest, ProjectKey: key}}}
	effective, err := ValidateAndMerge(canonical, policy, catalog)
	if err != nil || ValidateEffectiveSnapshot(key, effective) != nil {
		t.Fatalf("valid effective: %v", err)
	}
	if err = ValidateDurableEffectiveSnapshot(key, effective, effective.Digest); err != nil {
		t.Fatalf("durable effective: %v", err)
	}
	if err = ValidateDurableEffectiveSnapshot(key, effective, strings.Repeat("c", 64)); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("durable effective checksum: %v", err)
	}
	binding, err := SealRunBinding(RunConfigurationBinding{RunID: "run", ProjectKey: key, Version: 1, VersionDigest: canonical.Digest, ActivationGeneration: 1, ActivationDigest: testDigest, PolicyVersion: effective.PolicyVersion, PolicyDigest: effective.PolicyDigest, CatalogDigest: effective.CatalogDigest, EffectiveDigest: effective.Digest, Effective: effective})
	if err != nil || ValidateDurableRunBinding(binding, binding.BindingDigest) != nil {
		t.Fatalf("durable binding: %v", err)
	}
	if err = ValidateDurableRunBinding(binding, strings.Repeat("c", 64)); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("durable binding checksum: %v", err)
	}
	effective.Digest = strings.Repeat("b", 64)
	if err = ValidateEffectiveSnapshot(key, effective); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("corrupt effective: %v", err)
	}
}

// TestStoreCommandValidationRejectsMutableCanonicalState proves adapters recheck trusted callers.
func TestStoreCommandValidationRejectsMutableCanonicalState(t *testing.T) {
	t.Parallel()
	canonical, err := LoadStrict(bytes.NewReader(validDocumentJSON()), MaxCanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := NewProjectKey("organization", "project", "repo")
	effective, err := ValidateAndMerge(canonical, OrganizationPolicySnapshot{Version: 1, Digest: testDigest, MaximumLimits: Limits{DurationSeconds: 10, ToolCalls: 2, ModelTokens: 100, Replans: 1}, MinimumRetentionDays: 1, MaximumRetentionDays: 10, AllowedCapabilities: []string{"read"}}, ResourceCatalogSnapshot{Limits: testCatalogLimits(), ProjectKey: key, Digest: testDigest, Descriptors: []ResourceDescriptor{{Kind: ResourceContract, ID: "contract", Version: 1, Digest: testDigest, ProjectKey: key}}})
	if err != nil {
		t.Fatal(err)
	}
	command := PublishCommand{OperationID: "operation", CommandDigest: testDigest, Actor: "actor", Document: canonical, Effective: effective, CreatedAt: time.Unix(1, 0)}
	command.Document.Bytes[0] = '['
	if _, err = ValidatePublishCommand(command); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("canonical mutation %v", err)
	}
	command.Document, err = LoadStrict(bytes.NewReader(validDocumentJSON()), MaxCanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	command.Effective.Bytes = append([]byte(nil), command.Effective.Bytes...)
	command.Effective.Bytes[0] = '['
	if _, err = ValidatePublishCommand(command); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("effective mutation %v", err)
	}
	if err = ValidateRollbackCommand(RollbackCommand{}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("rollback %v", err)
	}
	if err = ValidateBindCommand(BindCommand{}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("bind %v", err)
	}
	binding, err := SealRunBinding(RunConfigurationBinding{RunID: "run", ProjectKey: key, Version: 1, VersionDigest: testDigest, ActivationGeneration: 1, ActivationDigest: testDigest, PolicyVersion: effective.PolicyVersion, PolicyDigest: effective.PolicyDigest, CatalogDigest: effective.CatalogDigest, EffectiveDigest: effective.Digest, Effective: effective})
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateBindCommand(BindCommand{RunID: "other-run", ProjectKey: key, Candidate: &AdmissionCandidate{Binding: binding}}); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("candidate mismatch %v", err)
	}
}

// TestCanonicalizeRejectsEveryStructuralAuthorityViolation exercises closed schema invariants.
func TestCanonicalizeRejectsEveryStructuralAuthorityViolation(t *testing.T) {
	t.Parallel()
	base, err := LoadStrict(bytes.NewReader(validDocumentJSON()), MaxCanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*Document){"schema": func(value *Document) { value.Schema = "future" }, "empty_refs": func(value *Document) { value.References = nil }, "duplicate": func(value *Document) { value.References = append(value.References, value.References[0]) }, "kind": func(value *Document) { value.References[0].Kind = "unknown" }, "id": func(value *Document) { value.References[0].ID = "bad/id" }, "version": func(value *Document) { value.References[0].Version = 0 }, "digest": func(value *Document) { value.References[0].Digest = "bad" }, "secret_ref": func(value *Document) { value.References[0].SecretRef = "bad/ref" }, "approval": func(value *Document) { value.Constraints.Approval = "disabled" }, "retention": func(value *Document) { value.Constraints.RetentionDays = 0 }, "capability": func(value *Document) { value.Constraints.Capabilities = []string{"bad/value"} }}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			document := base.Document.Clone()
			mutate(&document)
			if _, err := Canonicalize(document); err == nil {
				t.Fatal("invalid document accepted")
			}
		})
	}
}

// TestValidateAndMergeRejectsMissingStaleAndPolicyExpansion covers semantic failure categories.
func TestValidateAndMergeRejectsMissingStaleAndPolicyExpansion(t *testing.T) {
	t.Parallel()
	canonical, err := LoadStrict(bytes.NewReader(validDocumentJSON()), MaxCanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := NewProjectKey("organization", "project", "repo")
	basePolicy := OrganizationPolicySnapshot{Version: 1, Digest: testDigest, MaximumLimits: Limits{DurationSeconds: 10, ToolCalls: 2, ModelTokens: 100, Replans: 1}, MinimumRetentionDays: 1, MaximumRetentionDays: 10, AllowedCapabilities: []string{"read"}}
	if _, err = ValidateAndMerge(canonical, basePolicy, ResourceCatalogSnapshot{Limits: testCatalogLimits(), ProjectKey: key, Digest: testDigest}); !IsCategory(err, CategoryMissingReference) {
		t.Fatalf("missing %v", err)
	}
	stale := ResourceCatalogSnapshot{Limits: testCatalogLimits(), ProjectKey: key, Digest: testDigest, Descriptors: []ResourceDescriptor{{Kind: ResourceContract, ID: "contract", Version: 2, Digest: testDigest, ProjectKey: key}}}
	if _, err = ValidateAndMerge(canonical, basePolicy, stale); !IsCategory(err, CategoryIncompatibleReference) {
		t.Fatalf("stale %v", err)
	}
	valid := stale
	valid.Descriptors[0].Version = 1
	policy := basePolicy.Clone()
	policy.Allowed = map[ResourceKind][]string{ResourceContract: {"other"}}
	if _, err = ValidateAndMerge(canonical, policy, valid); !IsCategory(err, CategoryIncompatibleReference) {
		t.Fatalf("allowlist %v", err)
	}
	policy = basePolicy.Clone()
	policy.ApprovalRequired = true
	document := canonical.Document.Clone()
	document.Constraints.Approval = "optional"
	optional, _ := Canonicalize(document)
	if _, err = ValidateAndMerge(optional, policy, valid); !IsCategory(err, CategoryIncompatibleReference) {
		t.Fatalf("approval %v", err)
	}
	policy = basePolicy.Clone()
	policy.AllowedCapabilities = []string{"other"}
	if _, err = ValidateAndMerge(canonical, policy, valid); !IsCategory(err, CategoryIncompatibleReference) {
		t.Fatalf("capability %v", err)
	}
}

// TestSnapshotSourceConstructionFailures covers duplicate, corrupt, nil and cancelled sources.
func TestSnapshotSourceConstructionFailures(t *testing.T) {
	t.Parallel()
	key, _ := NewProjectKey("organization", "project", "repo")
	snapshot := ResourceCatalogSnapshot{Limits: testCatalogLimits(), ProjectKey: key, Digest: testDigest, Descriptors: []ResourceDescriptor{{Kind: ResourceContract, ID: "contract", Version: 1, Digest: testDigest, ProjectKey: key}}}
	if _, err := NewSnapshotCatalog([]ResourceCatalogSnapshot{snapshot, snapshot}); err == nil {
		t.Fatal("duplicate catalog accepted")
	}
	snapshot.Digest = "bad"
	if _, err := NewSnapshotCatalog([]ResourceCatalogSnapshot{snapshot}); err == nil {
		t.Fatal("corrupt catalog accepted")
	}
	if _, err := (*SnapshotCatalog)(nil).Snapshot(context.Background(), key); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("nil catalog %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	catalog, _ := NewSnapshotCatalog([]ResourceCatalogSnapshot{{Limits: testCatalogLimits(), ProjectKey: key, Digest: testDigest, Descriptors: []ResourceDescriptor{{Kind: ResourceContract, ID: "contract", Version: 1, Digest: testDigest, ProjectKey: key}}}})
	if _, err := catalog.Snapshot(cancelled, key); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel catalog %v", err)
	}
	policy := PolicyForProject{ProjectKey: key, Snapshot: OrganizationPolicySnapshot{Version: 1, Digest: testDigest}}
	if _, err := NewSnapshotPolicies([]PolicyForProject{policy, policy}); err == nil {
		t.Fatal("duplicate policy accepted")
	}
	if _, err := (*SnapshotPolicies)(nil).Snapshot(context.Background(), key); !IsCategory(err, CategoryUnavailable) {
		t.Fatalf("nil policy %v", err)
	}
	other, _ := NewProjectKey("other-organization", "other", "repo")
	policies, _ := NewSnapshotPolicies([]PolicyForProject{policy})
	if _, err := policies.Snapshot(context.Background(), other); !IsCategory(err, CategoryNotFound) {
		t.Fatalf("missing policy %v", err)
	}
	if _, err := policies.Snapshot(cancelled, key); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel policy %v", err)
	}
	invalidPolicy := policy
	invalidPolicy.Snapshot.Version = 0
	if _, err := NewSnapshotPolicies([]PolicyForProject{invalidPolicy}); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("invalid policy %v", err)
	}
	invalidDescriptor := ResourceCatalogSnapshot{Limits: testCatalogLimits(), ProjectKey: key, Digest: testDigest, Descriptors: []ResourceDescriptor{{Kind: ResourceContract, ID: "bad/id", Version: 1, Digest: testDigest, ProjectKey: key}}}
	if _, err := NewSnapshotCatalog([]ResourceCatalogSnapshot{invalidDescriptor}); !IsCategory(err, CategoryIntegrity) {
		t.Fatalf("invalid descriptor %v", err)
	}
}

// failingReader exercises the bounded loader's opaque I/O failure branch.
type failingReader struct{}

// Read returns a provider error without exposing data.
func (failingReader) Read([]byte) (int, error) { return 0, errors.New("reader canary") }

// TestStrictLoaderCoversMalformedContainersAndBounds exercises every closed decoder edge.
func TestStrictLoaderCoversMalformedContainersAndBounds(t *testing.T) {
	t.Parallel()
	if _, err := LoadStrict(failingReader{}, MaxCanonicalBytes); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("reader: %v", err)
	}
	if _, err := LoadStrict(strings.NewReader(strings.Repeat("x", 32)), 4); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("overflow: %v", err)
	}
	for _, raw := range [][]byte{[]byte(`{`), []byte(`[`), []byte(`{"a":1,"a":2}`), []byte(`{"a":[{"b":1,"b":2}]}`)} {
		if err := rejectDuplicateKeys(raw); !IsCategory(err, CategoryInvalid) {
			t.Fatalf("duplicate walk %q: %v", raw, err)
		}
	}
	if err := rejectSecretValueFields([]byte(`[{"nested":{"password":"canary"}}]`)); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("nested secret: %v", err)
	}
	if err := rejectSecretValueFields([]byte(`{`)); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("malformed secret scan: %v", err)
	}
	base, err := LoadStrict(bytes.NewReader(validDocumentJSON()), MaxCanonicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	invalidKey := base.Document.Clone()
	invalidKey.ProjectID = "bad/project"
	if _, err = Canonicalize(invalidKey); err == nil {
		t.Fatal("invalid project key accepted")
	}
	tooMany := base.Document.Clone()
	tooMany.References = make([]ResourceReference, MaxReferences+1)
	if _, err = Canonicalize(tooMany); !IsCategory(err, CategoryInvalid) {
		t.Fatalf("reference bound: %v", err)
	}
}

// countingCatalog detects forbidden collaborator access before authorization.
type countingCatalog struct{ calls int }

// cancelledCatalog returns a typed cancellation to exercise boundary classification.
type cancelledCatalog struct{}

// Snapshot reports cancellation without returning provider data.
func (cancelledCatalog) Snapshot(context.Context, ProjectKey) (ResourceCatalogSnapshot, error) {
	return ResourceCatalogSnapshot{}, context.Canceled
}

// Snapshot records access so the authority-order assertion can inspect it.
func (catalog *countingCatalog) Snapshot(context.Context, ProjectKey) (ResourceCatalogSnapshot, error) {
	catalog.calls++
	return ResourceCatalogSnapshot{}, nil
}

// countingPolicy detects forbidden policy access before authorization.
type countingPolicy struct{ calls int }

// Snapshot records access so the authority-order assertion can inspect it.
func (policy *countingPolicy) Snapshot(context.Context, ProjectKey) (OrganizationPolicySnapshot, error) {
	policy.calls++
	return OrganizationPolicySnapshot{}, nil
}

// unusedStore panics if an authority-boundary test reaches persistence.
type unusedStore struct{}

// Publish is an unreachable mutation in authority-order tests.
func (unusedStore) Publish(context.Context, PublishCommand) (PublishReceipt, error) {
	panic("unexpected")
}

// bindingStore records admission binding while inheriting unreachable operations.
type bindingStore struct {
	unusedStore
	calls int
}

// Bind returns the fixture only for the trusted resolver path.
func (store *bindingStore) Bind(_ context.Context, command BindCommand) (RunConfigurationBinding, error) {
	store.calls++
	return RunConfigurationBinding{}, &Error{Category: CategoryUnavailable, Operation: "test.bind"}
}

// Rollback is an unreachable mutation in authority-order tests.
func (unusedStore) Rollback(context.Context, RollbackCommand) (PublishReceipt, error) {
	panic("unexpected")
}

// Head is an unreachable query in authority-order tests.
func (unusedStore) Head(context.Context, ProjectKey) (ConfigurationHead, error) { panic("unexpected") }

// Version is an unreachable query in authority-order tests.
func (unusedStore) Version(context.Context, ProjectKey, uint64) (ConfigurationVersion, EffectiveConfiguration, error) {
	panic("unexpected")
}

// Effective is an unreachable query in authority-order tests.
func (unusedStore) Effective(context.Context, ProjectKey, uint64) (EffectiveConfiguration, error) {
	panic("unexpected")
}

// History is an unreachable query in authority-order tests.
func (unusedStore) History(context.Context, ProjectKey, uint64, int) (HistoryPage, error) {
	panic("unexpected")
}

// Bind is an unreachable mutation in authority-order tests.
func (unusedStore) Bind(context.Context, BindCommand) (RunConfigurationBinding, error) {
	panic("unexpected")
}

// Binding is an unreachable query in authority-order tests.
func (unusedStore) Binding(context.Context, ProjectKey, string) (RunConfigurationBinding, error) {
	panic("unexpected")
}
