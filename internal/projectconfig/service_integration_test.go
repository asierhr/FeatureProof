package projectconfig_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	pc "github.com/asierhr/featureProof/internal/projectconfig"
	localconfig "github.com/asierhr/featureProof/internal/projectconfig/local"
)

// catalogLimits supplies explicit finite budgets for service integration fixtures.
func catalogLimits() pc.CatalogLimits {
	return pc.CatalogLimits{MaxDescriptors: 16, MaxOperationsPerDescriptor: 16, MaxCapabilitiesPerDescriptor: 16, MaxDependenciesPerDescriptor: 16, MaxDependencyEdges: 64, MaxGraphWork: 80}
}

// serviceFixture composes the real service and local durable adapter with deterministic snapshots.
func serviceFixture(t *testing.T) (*pc.Service, pc.ProjectKey, []byte, pc.ConfigurationAuthority) {
	t.Helper()
	key, _ := pc.NewProjectKey("service-organization", "service-project", "service-repo")
	digest := digestFixture()
	document := pc.Document{Schema: pc.SchemaV1, OrganizationID: key.OrganizationID, ProjectID: key.ProjectID, RepositoryID: key.RepositoryID, References: []pc.ResourceReference{{Kind: pc.ResourceContract, ID: "contract", Version: 1, Digest: digest}}, Constraints: pc.Constraints{Limits: pc.Limits{DurationSeconds: 5, ToolCalls: 2, ModelTokens: 50, Replans: 1}, Approval: "required", RetentionDays: 7, Capabilities: []string{"read"}}}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := pc.NewSnapshotCatalog([]pc.ResourceCatalogSnapshot{{Limits: catalogLimits(), ProjectKey: key, Digest: digest, Descriptors: []pc.ResourceDescriptor{{Kind: pc.ResourceContract, ID: "contract", Version: 1, Digest: digest, ProjectKey: key}}}})
	if err != nil {
		t.Fatal(err)
	}
	policies, err := pc.NewSnapshotPolicies([]pc.PolicyForProject{{ProjectKey: key, Snapshot: pc.OrganizationPolicySnapshot{Version: 1, Digest: digest, MaximumLimits: pc.Limits{DurationSeconds: 10, ToolCalls: 3, ModelTokens: 100, Replans: 2}, ApprovalRequired: true, MinimumRetentionDays: 1, MaximumRetentionDays: 30, AllowedCapabilities: []string{"read"}}}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := localconfig.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := pc.NewService(store, catalog, policies, nil, pc.ServiceConfig{CursorKey: []byte("01234567890123456789012345678901"), CursorTTL: time.Hour, Now: func() time.Time { return time.Unix(100, 0).UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := pc.IssueAuthority("actor", key, pc.ActionValidate, pc.ActionPublish, pc.ActionRollback, pc.ActionRead, pc.ActionExport, pc.ActionAdmit)
	if err != nil {
		t.Fatal(err)
	}
	return service, key, raw, authority
}

// digestFixture supplies a canonical digest for catalog and policy fixtures.
func digestFixture() string {
	return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
}

// TestServiceLifecycle exercises every public command through one authority and store.
func TestServiceLifecycle(t *testing.T) {
	t.Parallel()
	service, key, raw, authority := serviceFixture(t)
	ctx := context.Background()
	validated, err := service.Validate(ctx, authority, key, raw)
	if err != nil || validated.Digest == "" {
		t.Fatalf("validate: %v", err)
	}
	receipt, err := service.PublishAndActivate(ctx, authority, key, "publish-one", 0, raw)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.PublishAndActivate(ctx, authority, key, "publish-one", 0, raw)
	if err != nil || replay != receipt {
		t.Fatalf("publish replay: %+v %v", replay, err)
	}
	head, version, effective, err := service.Active(ctx, authority, key)
	if err != nil || head.Activation.Generation != 1 || version.Version != 1 || effective.Digest != validated.Digest {
		t.Fatalf("active mismatch: %v", err)
	}
	loaded, loadedEffective, err := service.Version(ctx, authority, key, 1)
	if err != nil || loaded.DocumentDigest != version.DocumentDigest || loadedEffective.Digest != effective.Digest {
		t.Fatalf("version mismatch: %v", err)
	}
	exported, err := service.Export(ctx, authority, key, 1)
	if err != nil || !bytes.Equal(exported, version.Canonical) {
		t.Fatalf("export mismatch: %v", err)
	}
	page, err := service.History(ctx, authority, key, "", 1)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("history mismatch: %v", err)
	}
	binding, err := service.ResolveForAdmission(ctx, authority, key, "run-one")
	if err != nil || pc.ValidateRunBinding(binding) != nil {
		t.Fatalf("binding mismatch: %v", err)
	}
	rollback, err := service.Rollback(ctx, authority, key, "rollback-one", 1, 1)
	if err != nil || rollback.Generation != 2 {
		t.Fatalf("rollback mismatch: %+v %v", rollback, err)
	}
	reloaded, err := service.Binding(ctx, authority, key, "run-one")
	if err != nil || reloaded.BindingDigest != binding.BindingDigest {
		t.Fatalf("binding changed: %v", err)
	}
	head, _, _, err = service.Active(ctx, authority, key)
	if err != nil || head.Activation.Reason != "rollback" {
		t.Fatalf("rollback head: %v", err)
	}
}

// TestServiceRejectsStaleScopeAndMalformedOperations covers fail-closed command paths.
func TestServiceRejectsStaleScopeAndMalformedOperations(t *testing.T) {
	t.Parallel()
	service, key, raw, authority := serviceFixture(t)
	ctx := context.Background()
	if _, err := service.PublishAndActivate(ctx, authority, key, "bad/id", 0, raw); !pc.IsCategory(err, pc.CategoryInvalid) {
		t.Fatalf("operation got %v", err)
	}
	if _, err := service.PublishAndActivate(ctx, authority, key, "publish", 1, raw); !pc.IsCategory(err, pc.CategoryStaleHead) {
		t.Fatalf("stale got %v", err)
	}
	other, _ := pc.NewProjectKey("other-organization", "other", "service-repo")
	if _, err := service.Validate(ctx, authority, other, raw); !pc.IsCategory(err, pc.CategoryUnauthorized) {
		t.Fatalf("scope got %v", err)
	}
	if _, err := service.Rollback(ctx, authority, key, "rollback", 9, 0); !pc.IsCategory(err, pc.CategoryNotFound) {
		t.Fatalf("missing rollback got %v", err)
	}
	if _, err := service.ResolveForAdmission(ctx, authority, key, "run"); !pc.IsCategory(err, pc.CategoryNotFound) {
		t.Fatalf("headless admit got %v", err)
	}
	if _, err := service.History(ctx, authority, key, "", 0); !pc.IsCategory(err, pc.CategoryInvalid) {
		t.Fatalf("invalid history got %v", err)
	}
	if _, err := service.Export(ctx, authority, key, 1); !pc.IsCategory(err, pc.CategoryNotFound) {
		t.Fatalf("missing export got %v", err)
	}
}

// TestGatewayConvergesAdministrativeOperations proves every transport command uses the same service.
func TestGatewayConvergesAdministrativeOperations(t *testing.T) {
	t.Parallel()
	service, key, raw, authority := serviceFixture(t)
	gateway := pc.Gateway{Service: service}
	ctx := context.Background()
	validated, err := gateway.Execute(ctx, pc.GatewayRequest{Operation: pc.OperationValidate, Authority: authority, ProjectKey: key, Document: raw})
	if err != nil || validated.Effective == nil {
		t.Fatalf("validate %v", err)
	}
	published, err := gateway.Execute(ctx, pc.GatewayRequest{Operation: pc.OperationPublish, Authority: authority, ProjectKey: key, OperationID: "gateway-publish", Document: raw})
	if err != nil || published.Receipt == nil {
		t.Fatalf("publish %v", err)
	}
	requests := []pc.GatewayRequest{{Operation: pc.OperationShowActive, Authority: authority, ProjectKey: key}, {Operation: pc.OperationShowVersion, Authority: authority, ProjectKey: key, Version: 1}, {Operation: pc.OperationHistory, Authority: authority, ProjectKey: key, Limit: 10}, {Operation: pc.OperationExport, Authority: authority, ProjectKey: key, Version: 1}, {Operation: pc.OperationRollback, Authority: authority, ProjectKey: key, OperationID: "gateway-rollback", Version: 1, ExpectedGeneration: 1}}
	for _, request := range requests {
		if _, err = gateway.Execute(ctx, request); err != nil {
			t.Fatalf("%s: %v", request.Operation, err)
		}
	}
	if _, err = gateway.Execute(ctx, pc.GatewayRequest{Operation: "unknown"}); !pc.IsCategory(err, pc.CategoryInvalid) {
		t.Fatalf("unknown %v", err)
	}
}

// TestAdmissionResolverUsesOneTrustedMapping covers both atomic and standalone admission paths.
func TestAdmissionResolverUsesOneTrustedMapping(t *testing.T) {
	t.Parallel()
	service, key, raw, authority := serviceFixture(t)
	if _, err := service.PublishAndActivate(context.Background(), authority, key, "admission-publish", 0, raw); err != nil {
		t.Fatal(err)
	}
	resolver, err := pc.NewAdmissionResolver(service, []pc.RepositoryAdmission{{RepositoryID: key.RepositoryID, ProjectKey: key, Authority: authority}})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := resolver.PrepareForWorkflow(context.Background(), key.RepositoryID, "prepared-run")
	if err != nil || prepared.Binding.BindingDigest == "" {
		t.Fatalf("prepare: %+v %v", prepared, err)
	}
	bound, err := resolver.BindForWorkflow(context.Background(), key.RepositoryID, "bound-run")
	if err != nil || bound.BindingDigest == "" {
		t.Fatalf("bind: %+v %v", bound, err)
	}
	if _, err = resolver.PrepareForWorkflow(context.Background(), "unknown", "run"); !pc.IsCategory(err, pc.CategoryUnauthorized) {
		t.Fatalf("unknown prepare: %v", err)
	}
	if _, err = (*pc.AdmissionResolver)(nil).BindForWorkflow(context.Background(), key.RepositoryID, "run"); !pc.IsCategory(err, pc.CategoryUnavailable) {
		t.Fatalf("nil bind: %v", err)
	}
	if _, err = (*pc.AdmissionResolver)(nil).PrepareForWorkflow(context.Background(), key.RepositoryID, "run"); !pc.IsCategory(err, pc.CategoryUnavailable) {
		t.Fatalf("nil prepare: %v", err)
	}
}

// TestAdmissionResolverRejectsInvalidComposition proves mappings are immutable and uniquely scoped.
func TestAdmissionResolverRejectsInvalidComposition(t *testing.T) {
	t.Parallel()
	service, key, _, authority := serviceFixture(t)
	valid := pc.RepositoryAdmission{RepositoryID: key.RepositoryID, ProjectKey: key, Authority: authority}
	tests := [][]pc.RepositoryAdmission{nil, {valid, valid}, {{RepositoryID: "wrong", ProjectKey: key, Authority: authority}}}
	for _, values := range tests {
		if _, err := pc.NewAdmissionResolver(service, values); err == nil {
			t.Fatal("invalid admission composition accepted")
		}
	}
	if _, err := pc.NewAdmissionResolver(nil, []pc.RepositoryAdmission{valid}); err == nil {
		t.Fatal("nil service accepted")
	}
}

// recordingObserver captures allowlisted facts and deliberately has no authority over commits.
type recordingObserver struct{ events []pc.Event }

// Observe records one post-commit fact for the service assertion.
func (observer *recordingObserver) Observe(_ context.Context, event pc.Event) {
	observer.events = append(observer.events, event)
}

// panicObserver simulates a failed best-effort telemetry sink.
type panicObserver struct{}

// Observe fails after receiving an allowlisted fact and must not affect the authoritative operation.
func (panicObserver) Observe(context.Context, pc.Event) { panic("sink unavailable") }

// TestObserverCoversRejectionAndCannotChangeResults proves telemetry is non-authoritative and secret-free.
func TestObserverCoversRejectionAndCannotChangeResults(t *testing.T) {
	t.Parallel()
	_, key, raw, authority := serviceFixture(t)
	observer := &recordingObserver{}
	service, err := pc.NewService(serviceStore(t), serviceCatalog(t, key), servicePolicies(t, key), observer, pc.ServiceConfig{CursorKey: []byte("01234567890123456789012345678901"), CursorTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := pc.NewProjectKey("other-organization", "other", "repo")
	if _, err = service.Validate(context.Background(), authority, other, []byte(`{"secret_value":"CANARY"}`)); !pc.IsCategory(err, pc.CategoryUnauthorized) {
		t.Fatalf("authorization result changed: %v", err)
	}
	if len(observer.events) != 1 || observer.events[0].Action != "validate" || observer.events[0].Reason != string(pc.CategoryUnauthorized) {
		t.Fatalf("rejection telemetry: %+v", observer.events)
	}
	encoded, _ := json.Marshal(observer.events)
	if bytes.Contains(encoded, []byte("CANARY")) {
		t.Fatal("telemetry leaked request content")
	}
	service, err = pc.NewService(serviceStore(t), serviceCatalog(t, key), servicePolicies(t, key), panicObserver{}, pc.ServiceConfig{CursorKey: []byte("01234567890123456789012345678901"), CursorTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.PublishAndActivate(context.Background(), authority, key, "sink-failure", 0, raw); err != nil {
		t.Fatalf("sink changed commit result: %v", err)
	}
}

// TestObserverCoversEveryServiceBoundary proves all administrative, query and ingress outcomes use the closed telemetry vocabulary.
func TestObserverCoversEveryServiceBoundary(t *testing.T) {
	t.Parallel()
	_, key, raw, authority := serviceFixture(t)
	observer := &recordingObserver{}
	service, err := pc.NewService(serviceStore(t), serviceCatalog(t, key), servicePolicies(t, key), observer, pc.ServiceConfig{CursorKey: []byte("01234567890123456789012345678901"), CursorTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = service.Validate(ctx, authority, key, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = service.PublishAndActivate(ctx, authority, key, "observe-publish", 0, raw); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = service.Active(ctx, authority, key); err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.Version(ctx, authority, key, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = service.History(ctx, authority, key, "", 10); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Export(ctx, authority, key, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Rollback(ctx, authority, key, "observe-rollback", 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = service.PrepareForAdmission(ctx, authority, key, "observe-run"); err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, event := range observer.events {
		if event.Reason != "success" {
			t.Fatalf("unexpected result: %+v", event)
		}
		actions[event.Action] = true
	}
	for _, action := range []string{"validate", "publish", "active", "version", "history", "export", "rollback", "ingress"} {
		if !actions[action] {
			t.Fatalf("missing %s telemetry in %+v", action, observer.events)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = service.Validate(cancelled, authority, key, raw); !pc.IsCategory(err, pc.CategoryCancelled) {
		t.Fatalf("cancelled validation: %v", err)
	}
	last := observer.events[len(observer.events)-1]
	if last.Action != "validate" || last.Reason != string(pc.CategoryCancelled) {
		t.Fatalf("cancel telemetry: %+v", last)
	}
}

// TestEveryServiceBoundaryRejectsWrongAuthorityBeforeState covers the complete read, write, export and ingress authorization matrix.
func TestEveryServiceBoundaryRejectsWrongAuthorityBeforeState(t *testing.T) {
	t.Parallel()
	service, key, raw, _ := serviceFixture(t)
	other, _ := pc.NewProjectKey("other-organization", "other-project", key.RepositoryID)
	wrong, _ := pc.IssueAuthority("actor", other, pc.ActionValidate, pc.ActionPublish, pc.ActionRollback, pc.ActionRead, pc.ActionExport, pc.ActionAdmit)
	ctx := context.Background()
	assertUnauthorized := func(err error) {
		t.Helper()
		if !pc.IsCategory(err, pc.CategoryUnauthorized) {
			t.Fatalf("got %v", err)
		}
	}
	_, err := service.Validate(ctx, wrong, key, raw)
	assertUnauthorized(err)
	_, err = service.PublishAndActivate(ctx, wrong, key, "wrong-publish", 0, raw)
	assertUnauthorized(err)
	_, err = service.Rollback(ctx, wrong, key, "wrong-rollback", 1, 0)
	assertUnauthorized(err)
	_, _, _, err = service.Active(ctx, wrong, key)
	assertUnauthorized(err)
	_, _, err = service.Version(ctx, wrong, key, 1)
	assertUnauthorized(err)
	_, err = service.History(ctx, wrong, key, "", 1)
	assertUnauthorized(err)
	_, err = service.Export(ctx, wrong, key, 1)
	assertUnauthorized(err)
	_, err = service.Binding(ctx, wrong, key, "run")
	assertUnauthorized(err)
	_, err = service.PrepareForAdmission(ctx, wrong, key, "run")
	assertUnauthorized(err)
}

// TestServiceRejectsAuthorizedCrossDocumentScopeAndMalformedWrite proves an authority cannot make candidate bytes authoritative.
func TestServiceRejectsAuthorizedCrossDocumentScopeAndMalformedWrite(t *testing.T) {
	t.Parallel()
	service, key, raw, _ := serviceFixture(t)
	other, _ := pc.NewProjectKey("other-organization", "other-project", key.RepositoryID)
	authority, _ := pc.IssueAuthority("actor", other, pc.ActionValidate, pc.ActionPublish)
	if _, err := service.Validate(context.Background(), authority, other, raw); !pc.IsCategory(err, pc.CategoryUnauthorized) {
		t.Fatalf("validate scope: %v", err)
	}
	if _, err := service.PublishAndActivate(context.Background(), authority, other, "cross-scope", 0, raw); !pc.IsCategory(err, pc.CategoryUnauthorized) {
		t.Fatalf("publish scope: %v", err)
	}
	if _, err := service.PublishAndActivate(context.Background(), authority, other, "malformed", 0, []byte("{")); !pc.IsCategory(err, pc.CategoryInvalid) {
		t.Fatalf("malformed publish: %v", err)
	}
}

// TestPublishObservesOnlyAfterCommitAndHistoryCursorPaginates exercises safe post-commit/query branches.
func TestPublishObservesOnlyAfterCommitAndHistoryCursorPaginates(t *testing.T) {
	t.Parallel()
	_, key, raw, authority := serviceFixture(t)
	observer := &recordingObserver{}
	serviceWithObserver, err := pc.NewService(serviceStore(t), serviceCatalog(t, key), servicePolicies(t, key), observer, pc.ServiceConfig{CursorKey: []byte("01234567890123456789012345678901"), CursorTTL: time.Hour, Now: func() time.Time { return time.Unix(100, 0).UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = serviceWithObserver.PublishAndActivate(context.Background(), authority, key, "first", 0, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = serviceWithObserver.PublishAndActivate(context.Background(), authority, key, "second", 1, raw); err != nil {
		t.Fatal(err)
	}
	if len(observer.events) != 2 || observer.events[0].Action != "publish" || observer.events[0].Reason != "success" {
		t.Fatalf("events: %+v", observer.events)
	}
	page, err := serviceWithObserver.History(context.Background(), authority, key, "", 1)
	if err != nil || page.NextCursor == "" || len(page.Items) != 1 {
		t.Fatalf("first page: %+v %v", page, err)
	}
	page, err = serviceWithObserver.History(context.Background(), authority, key, page.NextCursor, 1)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("second page: %+v %v", page, err)
	}
	if _, err = (pc.Gateway{}).Execute(context.Background(), pc.GatewayRequest{}); !pc.IsCategory(err, pc.CategoryUnavailable) {
		t.Fatalf("nil gateway: %v", err)
	}
}

// serviceStore returns an isolated durable store for observer tests.
func serviceStore(t *testing.T) pc.Store {
	t.Helper()
	store, err := localconfig.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// serviceCatalog returns the canonical non-secret catalog fixture.
func serviceCatalog(t *testing.T, key pc.ProjectKey) pc.ResourceCatalog {
	t.Helper()
	catalog, err := pc.NewSnapshotCatalog([]pc.ResourceCatalogSnapshot{{Limits: catalogLimits(), ProjectKey: key, Digest: digestFixture(), Descriptors: []pc.ResourceDescriptor{{Kind: pc.ResourceContract, ID: "contract", Version: 1, Digest: digestFixture(), ProjectKey: key}}}})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// servicePolicies returns the monotonic organization policy fixture.
func servicePolicies(t *testing.T, key pc.ProjectKey) pc.OrganizationPolicySource {
	t.Helper()
	policies, err := pc.NewSnapshotPolicies([]pc.PolicyForProject{{ProjectKey: key, Snapshot: pc.OrganizationPolicySnapshot{Version: 1, Digest: digestFixture(), MaximumLimits: pc.Limits{DurationSeconds: 10, ToolCalls: 3, ModelTokens: 100, Replans: 2}, ApprovalRequired: true, MinimumRetentionDays: 1, MaximumRetentionDays: 30, AllowedCapabilities: []string{"read"}}}})
	if err != nil {
		t.Fatal(err)
	}
	return policies
}

// boundaryStore exposes deterministic query failures for application-boundary classification tests.
type boundaryStore struct {
	head         pc.ConfigurationHead
	version      pc.ConfigurationVersion
	effective    pc.EffectiveConfiguration
	headErr      error
	versionErr   error
	effectiveErr error
	bindErr      error
}

// Publish is unused by the read/admission boundary matrix.
func (*boundaryStore) Publish(context.Context, pc.PublishCommand) (pc.PublishReceipt, error) {
	return pc.PublishReceipt{}, errors.New("unused")
}

// Rollback is unused by the read/admission boundary matrix.
func (*boundaryStore) Rollback(context.Context, pc.RollbackCommand) (pc.PublishReceipt, error) {
	return pc.PublishReceipt{}, errors.New("unused")
}

// Head returns the configured head or safe test failure.
func (store *boundaryStore) Head(context.Context, pc.ProjectKey) (pc.ConfigurationHead, error) {
	return store.head, store.headErr
}

// Version returns the configured immutable version or safe test failure.
func (store *boundaryStore) Version(context.Context, pc.ProjectKey, uint64) (pc.ConfigurationVersion, pc.EffectiveConfiguration, error) {
	return store.version, store.effective, store.versionErr
}

// Effective returns the configured snapshot or safe test failure.
func (store *boundaryStore) Effective(context.Context, pc.ProjectKey, uint64) (pc.EffectiveConfiguration, error) {
	return store.effective, store.effectiveErr
}

// History is unused by the read/admission boundary matrix.
func (*boundaryStore) History(context.Context, pc.ProjectKey, uint64, int) (pc.HistoryPage, error) {
	return pc.HistoryPage{}, errors.New("unused")
}

// Bind returns the configured failure after candidate preparation.
func (store *boundaryStore) Bind(_ context.Context, command pc.BindCommand) (pc.RunConfigurationBinding, error) {
	return command.Candidate.Binding, store.bindErr
}

// Binding is unused by the read/admission boundary matrix.
func (*boundaryStore) Binding(context.Context, pc.ProjectKey, string) (pc.RunConfigurationBinding, error) {
	return pc.RunConfigurationBinding{}, errors.New("unused")
}

// boundaryService creates a complete service around one scripted store.
func boundaryService(t *testing.T, store *boundaryStore) (*pc.Service, pc.ProjectKey, pc.ConfigurationAuthority) {
	t.Helper()
	key, _ := pc.NewProjectKey("boundary-organization", "boundary-project", "boundary-repo")
	rawDocument := pc.Document{Schema: pc.SchemaV1, OrganizationID: key.OrganizationID, ProjectID: key.ProjectID, RepositoryID: key.RepositoryID, References: []pc.ResourceReference{{Kind: pc.ResourceContract, ID: "contract", Version: 1, Digest: digestFixture()}}, Constraints: pc.Constraints{Limits: pc.Limits{DurationSeconds: 1}, Approval: "required", RetentionDays: 1}}
	canonical, err := pc.Canonicalize(rawDocument)
	if err != nil {
		t.Fatal(err)
	}
	catalog := serviceCatalog(t, key)
	policies := servicePolicies(t, key)
	effective, err := pc.ValidateAndMerge(canonical, pc.OrganizationPolicySnapshot{Version: 1, Digest: digestFixture(), MaximumLimits: pc.Limits{DurationSeconds: 10}, ApprovalRequired: true, MinimumRetentionDays: 1, MaximumRetentionDays: 30}, pc.ResourceCatalogSnapshot{Limits: catalogLimits(), ProjectKey: key, Digest: digestFixture(), Descriptors: []pc.ResourceDescriptor{{Kind: pc.ResourceContract, ID: "contract", Version: 1, Digest: digestFixture(), ProjectKey: key}}})
	if err != nil {
		t.Fatal(err)
	}
	store.head = pc.ConfigurationHead{ProjectKey: key, Activation: pc.Activation{ProjectKey: key, Generation: 1, Version: 1, VersionDigest: canonical.Digest, Digest: digestFixture()}}
	store.version = pc.ConfigurationVersion{ProjectKey: key, Version: 1, Schema: pc.SchemaV1, Canonical: canonical.Bytes, DocumentDigest: canonical.Digest}
	store.effective = effective
	service, err := pc.NewService(store, catalog, policies, nil, pc.ServiceConfig{CursorKey: []byte("01234567890123456789012345678901"), CursorTTL: time.Hour, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	authority, _ := pc.IssueAuthority("actor", key, pc.ActionRead, pc.ActionAdmit)
	return service, key, authority
}

// TestServiceClassifiesQueryAndAdmissionBoundaries covers fail-closed collaborator branches.
func TestServiceClassifiesQueryAndAdmissionBoundaries(t *testing.T) {
	t.Parallel()
	store := &boundaryStore{}
	service, key, authority := boundaryService(t, store)
	store.headErr = context.Canceled
	if _, _, _, err := service.Active(context.Background(), authority, key); !pc.IsCategory(err, pc.CategoryCancelled) {
		t.Fatalf("head cancel: %v", err)
	}
	store.headErr = nil
	store.versionErr = errors.New("provider canary")
	if _, _, _, err := service.Active(context.Background(), authority, key); !pc.IsCategory(err, pc.CategoryUnavailable) {
		t.Fatalf("version unavailable: %v", err)
	}
	store.versionErr = nil
	store.effectiveErr = context.DeadlineExceeded
	if _, _, _, err := service.Active(context.Background(), authority, key); !pc.IsCategory(err, pc.CategoryDeadline) {
		t.Fatalf("effective deadline: %v", err)
	}
	store.effectiveErr = nil
	if _, err := service.PrepareForAdmission(context.Background(), authority, key, "bad/run"); !pc.IsCategory(err, pc.CategoryInvalid) {
		t.Fatalf("run id: %v", err)
	}
	store.headErr = errors.New("head provider canary")
	if _, err := service.PrepareForAdmission(context.Background(), authority, key, "run-head"); !pc.IsCategory(err, pc.CategoryUnavailable) {
		t.Fatalf("admission head: %v", err)
	}
	store.headErr = nil
	store.versionErr = context.DeadlineExceeded
	if _, err := service.PrepareForAdmission(context.Background(), authority, key, "run-version"); !pc.IsCategory(err, pc.CategoryDeadline) {
		t.Fatalf("admission version: %v", err)
	}
	store.versionErr = nil
	store.effectiveErr = context.Canceled
	if _, err := service.PrepareForAdmission(context.Background(), authority, key, "run-effective"); !pc.IsCategory(err, pc.CategoryCancelled) {
		t.Fatalf("admission effective: %v", err)
	}
	store.effectiveErr = nil
	store.version.Canonical = []byte("{")
	if _, err := service.PrepareForAdmission(context.Background(), authority, key, "run-one"); !pc.IsCategory(err, pc.CategoryIntegrity) {
		t.Fatalf("corrupt version: %v", err)
	}
	service, key, authority = boundaryService(t, store)
	store.effective.Digest = digestFixture()
	if _, err := service.PrepareForAdmission(context.Background(), authority, key, "run-two"); !pc.IsCategory(err, pc.CategoryIncompatibleReference) {
		t.Fatalf("drift: %v", err)
	}
	service, key, authority = boundaryService(t, store)
	store.bindErr = context.Canceled
	if _, err := service.ResolveForAdmission(context.Background(), authority, key, "run-three"); !errors.Is(err, context.Canceled) {
		t.Fatalf("bind: %v", err)
	}
}

// failingPolicySource returns an opaque collaborator failure without catalog access.
type failingPolicySource struct{}

// Snapshot simulates an unavailable organization policy authority.
func (failingPolicySource) Snapshot(context.Context, pc.ProjectKey) (pc.OrganizationPolicySnapshot, error) {
	return pc.OrganizationPolicySnapshot{}, errors.New("policy provider canary")
}

// failingCatalogSource returns an opaque collaborator failure after policy resolution.
type failingCatalogSource struct{}

// Snapshot simulates an unavailable resource catalog authority.
func (failingCatalogSource) Snapshot(context.Context, pc.ProjectKey) (pc.ResourceCatalogSnapshot, error) {
	return pc.ResourceCatalogSnapshot{}, errors.New("catalog provider canary")
}

// TestServiceClassifiesSnapshotFailures proves raw collaborator errors never cross the boundary.
func TestServiceClassifiesSnapshotFailures(t *testing.T) {
	t.Parallel()
	key, _ := pc.NewProjectKey("snapshot-organization", "snapshot-project", "snapshot-repo")
	document := pc.Document{Schema: pc.SchemaV1, OrganizationID: key.OrganizationID, ProjectID: key.ProjectID, RepositoryID: key.RepositoryID, References: []pc.ResourceReference{{Kind: pc.ResourceContract, ID: "contract", Version: 1, Digest: digestFixture()}}, Constraints: pc.Constraints{Limits: pc.Limits{DurationSeconds: 1}, Approval: "required", RetentionDays: 1}}
	raw, _ := json.Marshal(document)
	store := &boundaryStore{}
	authority, _ := pc.IssueAuthority("actor", key, pc.ActionValidate)
	validCatalog := serviceCatalog(t, key)
	validPolicies := servicePolicies(t, key)
	policyFailure, err := pc.NewService(store, validCatalog, failingPolicySource{}, nil, pc.ServiceConfig{CursorKey: []byte("01234567890123456789012345678901"), CursorTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = policyFailure.Validate(context.Background(), authority, key, raw); !pc.IsCategory(err, pc.CategoryUnavailable) || strings.Contains(err.Error(), "canary") {
		t.Fatalf("policy failure: %v", err)
	}
	catalogFailure, err := pc.NewService(store, failingCatalogSource{}, validPolicies, nil, pc.ServiceConfig{CursorKey: []byte("01234567890123456789012345678901"), CursorTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = catalogFailure.Validate(context.Background(), authority, key, raw); !pc.IsCategory(err, pc.CategoryUnavailable) || strings.Contains(err.Error(), "canary") {
		t.Fatalf("catalog failure: %v", err)
	}
}
