package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contractreview"
	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/asierhr/featureProof/internal/environment"
	"github.com/asierhr/featureProof/internal/externalagent"
	"github.com/asierhr/featureProof/internal/projectconfig"
	"github.com/asierhr/featureProof/internal/tenantsecurity"
	"github.com/asierhr/featureProof/internal/validationworkflow"
)

// countingExternalExecutor records whether quota denial leaked to the real preview consumer.
type countingExternalExecutor struct {
	calls          int
	err            error
	reconcileCalls int
	reconcileErr   error
}

// Execute records one forbidden downstream call for the quota-order assertion.
func (executor *countingExternalExecutor) Execute(context.Context, externalagent.Selection, string) (externalagent.ExecutionResult, error) {
	executor.calls++
	return externalagent.ExecutionResult{}, executor.err
}

// ReconcileUnknown records provider-owned reconciliation and can preserve an
// unresolved effect across terminal application state and process restart.
func (executor *countingExternalExecutor) ReconcileUnknown(context.Context, string) error {
	executor.reconcileCalls++
	return executor.reconcileErr
}

// staticExternalIdentity exposes one trusted downstream principal.
type staticExternalIdentity struct {
	calls int
	id    string
	err   error
}

// reportingExternalRegistry injects recovery observations while preserving the
// complete configured registry behavior of its embedded local adapter.
type reportingExternalRegistry struct {
	*externalagent.LocalRegistry
	records []externalagent.RunRecord
	err     error
}

// RecoverTerminal returns the exact injected recovery outcome.
func (registry reportingExternalRegistry) RecoverTerminal(context.Context, string, time.Time) ([]externalagent.RunRecord, error) {
	return registry.records, registry.err
}

// externalIdentityFunc adapts one request-specific identity result.
type externalIdentityFunc func(context.Context) (externalagent.Principal, error)

// Resolve invokes the injected identity boundary.
func (resolve externalIdentityFunc) Resolve(ctx context.Context) (externalagent.Principal, error) {
	return resolve(ctx)
}

// mappedMCPAuthenticator simulates a transport verifier returning two
// different principals without reading tool arguments.
type mappedMCPAuthenticator map[string]string

// AuthenticateMCP resolves only preconfigured opaque credentials.
func (authenticator mappedMCPAuthenticator) AuthenticateMCP(_ context.Context, credential string) (string, error) {
	principal, ok := authenticator[credential]
	if !ok {
		return "", errors.New("denied")
	}
	return principal, nil
}

// Resolve records identity delegation after tenant authorization.
func (identity *staticExternalIdentity) Resolve(context.Context) (externalagent.Principal, error) {
	identity.calls++
	if identity.err != nil {
		return externalagent.Principal{}, identity.err
	}
	id := identity.id
	if id == "" {
		id = "operator"
	}
	return externalagent.Principal{ID: id, Scopes: map[string]bool{externalagent.ScopeCreate: true, externalagent.ScopeRead: true, externalagent.ScopeCancel: true}}, nil
}

// TestEveryProductIngressFailsClosedWithoutTenantComposition proves each CLI,
// API, MCP, GitHub, workflow and external-agent route rejects before product I/O.
func TestEveryProductIngressFailsClosedWithoutTenantComposition(t *testing.T) {
	t.Setenv("FEATUREPROOF_TENANT_SECURITY_CONFIG", "")
	for name, args := range map[string][]string{
		"project configuration": {"project-config", "show"},
		"memory mcp":            {"memory", "mcp", "serve"},
		"memory api":            {"memory", "api", "serve"},
		"memory cli":            {"memory", "query"},
		"github":                {"github", "serve"},
		"workflow cli":          {"workflow", "run"},
		"workflow mcp":          {"workflow", "mcp", "serve"},
		"contract mcp":          {"contract-review", "mcp", "serve"},
		"contract api":          {"contract-review", "api", "serve"},
		"contract cli":          {"contract-review", "show"},
		"default validation":    {"run"},
	} {
		t.Run(name, func(t *testing.T) {
			if code := runWithContext(context.Background(), args, bytes.NewReader(nil), &bytes.Buffer{}, &bytes.Buffer{}); code != 3 {
				t.Fatalf("args=%v code=%d", args, code)
			}
		})
	}
	var output bytes.Buffer
	if code := runWithContext(context.Background(), []string{"capabilities", "--json"}, bytes.NewReader(nil), &output, &bytes.Buffer{}); code == 0 || output.Len() == 0 {
		t.Fatalf("external-agent fail-closed code=%d output=%q", code, output.String())
	}
}

// TestComposeTenantSecurityFailsBeforeStoreWithoutConfig proves product
// surfaces cannot silently use legacy identity or a nil authorizer.
func TestComposeTenantSecurityFailsBeforeStoreWithoutConfig(t *testing.T) {
	t.Setenv("FEATUREPROOF_TENANT_SECURITY_CONFIG", "")
	if composition, err := composeTenantSecurity(context.Background()); err == nil || composition != nil {
		t.Fatalf("expected missing composition fail closed, got %+v %v", composition, err)
	}
}

// TestProductMCPAuthenticatorUsesPerRequestLiveTenantAuthority proves two
// transport credentials resolve distinct principals and membership revocation
// takes effect without restarting the long-lived MCP process.
func TestProductMCPAuthenticatorUsesPerRequestLiveTenantAuthority(t *testing.T) {
	root := t.TempDir()
	writeTenantSecurityCompositionFixture(t, root)
	composition, err := composeTenantSecurity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer composition.Close()
	snapshot, err := composition.Store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	digest, err := tenantsecurity.DigestSubject(tenantsecurity.AuthenticatedSubject{Issuer: "fixture", Subject: "second-subject"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Principals["organization/second-principal"] = tenantsecurity.Principal{ID: "second-principal", Kind: tenantsecurity.PrincipalHuman, SubjectDigest: digest, State: tenantsecurity.StateActive, Version: 1}
	snapshot.Subjects["organization/"+digest] = "second-principal"
	snapshot.Memberships["organization/second-principal"] = tenantsecurity.Membership{OrganizationID: "organization", PrincipalID: "second-principal", RoleIDs: []string{"operator"}, ProjectIDs: []string{"project"}, State: tenantsecurity.StateActive, ValidFrom: time.Now().Add(-time.Hour), Version: 1}
	expectedEpoch := snapshot.Epoch
	snapshot.Epoch++
	if _, err = composition.Store.Commit(context.Background(), tenantsecurity.StoreCommand{OrganizationID: "organization", OperationID: "add-mcp-principal", CommandDigest: strings.Repeat("e", 64), ExpectedEpoch: expectedEpoch, Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FEATUREPROOF_WORKFLOW_MCP_CREDENTIALS", `{"credential-operator-long":"operator","credential-second-long":"second-subject"}`)
	authenticator, err := loadTenantMCPAuthenticator(composition, tenantsecurity.SurfaceMCP, "FEATUREPROOF_WORKFLOW_MCP_CREDENTIALS")
	if err != nil {
		t.Fatal(err)
	}
	_, first, err := authenticator.AuthenticateWorkflowMCP(context.Background(), "credential-operator-long", "workflow.status")
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := authenticator.AuthenticateWorkflowMCP(context.Background(), "credential-second-long", "workflow.status")
	if err != nil {
		t.Fatal(err)
	}
	if first != "operator" || second != "second-principal" || first == second {
		t.Fatalf("per-request principals first=%q second=%q", first, second)
	}
	if _, _, err = authenticator.AuthenticateWorkflowMCP(context.Background(), "credential-spoofed-long", "workflow.status"); err == nil {
		t.Fatal("untrusted request credential accepted")
	}
	t.Setenv("FEATUREPROOF_EXTERNAL_AGENT_MCP_CREDENTIALS", `{"credential-operator-long":"operator","credential-second-long":"second-subject"}`)
	externalAuthenticator, err := loadTenantMCPAuthenticator(composition, tenantsecurity.SurfaceExternalAgent, "FEATUREPROOF_EXTERNAL_AGENT_MCP_CREDENTIALS")
	if err != nil {
		t.Fatal(err)
	}
	for operation, expectedScope := range map[string]string{
		"submit":            externalagent.ScopeCreate,
		"status":            externalagent.ScopeRead,
		"cancel":            externalagent.ScopeCancel,
		"correction_submit": externalagent.ScopeCorrectionCreate,
		"correction_status": externalagent.ScopeCorrectionRead,
		"correction_cancel": externalagent.ScopeCorrectionCancel,
	} {
		_, principal, authErr := externalAuthenticator.AuthenticateMCP(context.Background(), "credential-operator-long", operation)
		if authErr != nil || principal.ID != "operator" || !principal.Scopes[expectedScope] || len(principal.Scopes) != 1 {
			t.Fatalf("external operation=%s principal=%+v err=%v", operation, principal, authErr)
		}
	}
	_, secondExternal, err := externalAuthenticator.AuthenticateMCP(context.Background(), "credential-second-long", "submit")
	if err != nil || secondExternal.ID != "second-principal" {
		t.Fatalf("external internal principal=%+v err=%v", secondExternal, err)
	}
	bootstrap, err := composition.Gateway.Authorize(context.Background(), tenantsecurity.SurfaceCLI, tenantsecurity.ActionRunAdmit, tenantsecurity.ResourceRun)
	if err != nil {
		t.Fatal(err)
	}
	admissionService, err := tenantsecurity.NewAdmission(composition.Store, tenantsecurity.RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	remoteContext, err := composition.AuthenticateSurface(context.Background(), tenantsecurity.SurfaceMCP, "second-subject")
	if err != nil {
		t.Fatal(err)
	}
	digestValue := strings.Repeat("a", 64)
	binding := projectconfig.RunConfigurationBinding{RunID: "workflow-second-principal", ActivationGeneration: 1, VersionDigest: digestValue, PolicyDigest: bootstrap.Scope.PolicyDigest, CatalogDigest: strings.Repeat("b", 64), EffectiveDigest: strings.Repeat("c", 64), BindingDigest: strings.Repeat("d", 64)}
	remoteAdmission := tenantWorkflowAdmission{service: admissionService, capability: bootstrap, security: composition, surface: tenantsecurity.SurfaceMCP}
	authority, err := remoteAdmission.PrepareHistorical(remoteContext, binding.RunID, binding)
	if err != nil {
		t.Fatal(err)
	}
	historical, err := decodeHistoricalCapability(authority)
	if err != nil || historical.Scope.PrincipalID != "second-principal" {
		t.Fatalf("historical principal=%+v err=%v", historical.Scope, err)
	}
	restarted := tenantWorkflowAdmission{service: admissionService, capability: bootstrap, security: composition, surface: tenantsecurity.SurfaceMCP}
	intents, err := restarted.PendingHistorical(context.Background())
	if err != nil || len(intents) != 1 || intents[0].RunID != binding.RunID || intents[0].Authority != authority {
		t.Fatalf("restarted intents=%+v err=%v", intents, err)
	}
	if err = restarted.ConfirmHistorical(context.Background(), binding.RunID, intents[0].Authority); err != nil {
		t.Fatal(err)
	}
	if err = restarted.ConfirmHistorical(context.Background(), binding.RunID, intents[0].Authority); err != nil {
		t.Fatalf("historical replay changed digest: %v", err)
	}
	snapshot, err = composition.Store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	storedBinding := snapshot.Bindings["organization/project/"+binding.RunID]
	if storedBinding.Scope.PrincipalID != "second-principal" || snapshot.Jobs["organization/project/"+binding.RunID+"-first"].State != "queued" {
		t.Fatalf("historical attribution binding=%+v job=%+v", storedBinding, snapshot.Jobs["organization/project/"+binding.RunID+"-first"])
	}
	snapshot, err = composition.Store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	membership := snapshot.Memberships["organization/second-principal"]
	membership.State, membership.Version = tenantsecurity.StateRevoked, membership.Version+1
	snapshot.Memberships["organization/second-principal"] = membership
	expectedEpoch = snapshot.Epoch
	snapshot.Epoch++
	if _, err = composition.Store.Commit(context.Background(), tenantsecurity.StoreCommand{OrganizationID: "organization", OperationID: "revoke-mcp-principal", CommandDigest: strings.Repeat("f", 64), ExpectedEpoch: expectedEpoch, Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = authenticator.AuthenticateWorkflowMCP(context.Background(), "credential-second-long", "workflow.status"); err == nil {
		t.Fatal("live tenant revocation ignored")
	}
	if _, _, err = externalAuthenticator.AuthenticateMCP(context.Background(), "credential-second-long", "status"); err == nil {
		t.Fatal("external live tenant revocation ignored")
	}
}

// TestProductMCPAuthenticationFailureBoundaries covers malformed trusted
// credential configuration, invalid transport credentials, and unavailable
// issuer composition without crossing into application operations.
func TestProductMCPAuthenticationFailureBoundaries(t *testing.T) {
	root := t.TempDir()
	writeTenantSecurityCompositionFixture(t, root)
	composition, err := composeTenantSecurity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer composition.Close()
	for name, raw := range map[string]string{
		"malformed":        `{`,
		"short credential": `{"short":"operator"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("FEATUREPROOF_TEST_MCP_CREDENTIALS", raw)
			if _, loadErr := loadTenantMCPAuthenticator(composition, tenantsecurity.SurfaceMCP, "FEATUREPROOF_TEST_MCP_CREDENTIALS"); loadErr == nil {
				t.Fatal("invalid credential configuration accepted")
			}
		})
	}
	if _, subjectErr := (*tenantMCPAuthenticator)(nil).subject("credential-long-enough"); subjectErr == nil {
		t.Fatal("nil authenticator accepted")
	}
	auth := &tenantMCPAuthenticator{security: composition, surface: tenantsecurity.Surface("unknown"), profiles: map[string]string{"credential-long-enough": "operator"}}
	if _, _, _, err = auth.authorize(context.Background(), "credential-long-enough", "workflow.status"); err == nil {
		t.Fatal("surface without trusted issuer accepted")
	}
}

// TestTenantSecurityCompositionRejectsInvalidQuota proves invalid trusted
// quota documents fail during their exact construction boundary.
func TestTenantSecurityCompositionRejectsInvalidQuota(t *testing.T) {
	for name, mutate := range map[string]func(*tenantSecurityConfig){
		"quota policy": func(config *tenantSecurityConfig) { config.Quota.Version = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeTenantSecurityCompositionFixture(t, root)
			path := os.Getenv("FEATUREPROOF_TENANT_SECURITY_CONFIG")
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			var config tenantSecurityConfig
			if json.Unmarshal(raw, &config) != nil {
				t.Fatal("fixture config unavailable")
			}
			mutate(&config)
			raw, _ = json.Marshal(config)
			if writeErr := os.WriteFile(path, raw, 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
			if composition, composeErr := composeTenantSecurity(context.Background()); composeErr == nil || composition != nil {
				t.Fatalf("invalid %s composed", name)
			}
		})
	}
}

// TestComposeTenantSecurityRejectsMissingSurfaceProvider proves all six
// productive adapters are mandatory before authorization/store lookup.
func TestComposeTenantSecurityRejectsMissingSurfaceProvider(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "tenant-security.json")
	config := tenantSecurityConfig{CapabilityTTLSeconds: 60, StoreRoot: filepath.Join(root, "tenant-store"), Surfaces: map[string]tenantSecuritySurfaceConfig{}, Quota: tenantsecurity.QuotaPolicySnapshot{Version: 1, Tenant: map[string]tenantsecurity.QuotaLimit{tenantsecurity.QuotaModelCall: {Limit: 1, Window: time.Hour}}, Project: map[string]tenantsecurity.QuotaLimit{tenantsecurity.QuotaModelCall: {Limit: 1, Window: time.Hour}}, TenantOutstanding: 1, ProjectOutstanding: 1, ReservationTTL: time.Minute}}
	for surface, provider := range map[tenantsecurity.Surface]string{tenantsecurity.SurfaceCLI: "cli_profile", tenantsecurity.SurfaceAPI: "api_identity", tenantsecurity.SurfaceMCP: "mcp_identity", tenantsecurity.SurfaceGitHub: "github_installation", tenantsecurity.SurfaceExternalAgent: "external_profile"} {
		entry := tenantSecuritySurfaceConfig{Provider: provider, Issuer: "trusted", OrganizationID: "organization", ProjectID: "project", RepositoryID: "repository"}
		if surface == tenantsecurity.SurfaceCLI {
			entry.ProfileSubject = "subject"
		}
		config.Surfaces[string(surface)] = entry
	}
	payload, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(configPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FEATUREPROOF_TENANT_SECURITY_CONFIG", configPath)
	t.Setenv("FEATUREPROOF_CONTROL_PLANE_BACKEND", "local")
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(working) })
	if composition, composeErr := composeTenantSecurity(context.Background()); composeErr == nil || composition != nil {
		t.Fatalf("expected missing worker provider rejected, got %+v %v", composition, composeErr)
	}
}

// TestComposeTenantSecurityRejectsEveryIncompleteTrustBoundary proves startup
// never falls back to a legacy identity/store when its strict document,
// backend, provider set, or quota authority is incomplete.
func TestComposeTenantSecurityRejectsEveryIncompleteTrustBoundary(t *testing.T) {
	validConfig := func(root string) tenantSecurityConfig {
		surfaces := map[string]tenantSecuritySurfaceConfig{}
		for surface, provider := range map[tenantsecurity.Surface]string{
			tenantsecurity.SurfaceCLI:           "cli_profile",
			tenantsecurity.SurfaceAPI:           "api_identity",
			tenantsecurity.SurfaceMCP:           "mcp_identity",
			tenantsecurity.SurfaceGitHub:        "github_installation",
			tenantsecurity.SurfaceExternalAgent: "external_profile",
			tenantsecurity.SurfaceWorker:        "sealed_worker_envelope",
		} {
			entry := tenantSecuritySurfaceConfig{Provider: provider, Issuer: "trusted", OrganizationID: "organization", ProjectID: "project", RepositoryID: "repository"}
			if surface == tenantsecurity.SurfaceCLI {
				entry.ProfileSubject = "operator"
			}
			surfaces[string(surface)] = entry
		}
		limit := tenantsecurity.QuotaLimit{Limit: 10, Window: time.Hour}
		return tenantSecurityConfig{
			CapabilityTTLSeconds: 60,
			StoreRoot:            filepath.Join(root, "tenant-store"),
			Surfaces:             surfaces,
			Quota: tenantsecurity.QuotaPolicySnapshot{
				Version: 1, Tenant: map[string]tenantsecurity.QuotaLimit{tenantsecurity.QuotaModelCall: limit},
				Project:           map[string]tenantsecurity.QuotaLimit{tenantsecurity.QuotaModelCall: limit},
				TenantOutstanding: 2, ProjectOutstanding: 2, ReservationTTL: time.Minute,
			},
		}
	}
	tests := map[string]struct {
		backend string
		prepare func(*testing.T, string, *tenantSecurityConfig) string
	}{
		"relative config path": {prepare: func(_ *testing.T, _ string, _ *tenantSecurityConfig) string { return "tenant-security.json" }},
		"missing config file": {prepare: func(_ *testing.T, root string, _ *tenantSecurityConfig) string {
			return filepath.Join(root, "missing.json")
		}},
		"malformed document": {prepare: func(t *testing.T, root string, _ *tenantSecurityConfig) string {
			path := filepath.Join(root, "malformed.json")
			if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		"trailing document": {prepare: func(t *testing.T, root string, config *tenantSecurityConfig) string {
			payload, _ := json.Marshal(config)
			path := filepath.Join(root, "trailing.json")
			if err := os.WriteFile(path, append(payload, []byte("{}")...), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		"overflowed ttl": {prepare: func(t *testing.T, root string, config *tenantSecurityConfig) string {
			config.CapabilityTTLSeconds = ^uint64(0)
			return writeTenantSecurityConfigCase(t, root, config)
		}},
		"relative store root": {prepare: func(t *testing.T, root string, config *tenantSecurityConfig) string {
			config.StoreRoot = "relative-store"
			return writeTenantSecurityConfigCase(t, root, config)
		}},
		"store root is file": {prepare: func(t *testing.T, root string, config *tenantSecurityConfig) string {
			config.StoreRoot = filepath.Join(root, "not-a-directory")
			if err := os.WriteFile(config.StoreRoot, []byte("occupied"), 0o600); err != nil {
				t.Fatal(err)
			}
			return writeTenantSecurityConfigCase(t, root, config)
		}},
		"unsupported backend": {backend: "unsupported", prepare: func(t *testing.T, root string, config *tenantSecurityConfig) string {
			return writeTenantSecurityConfigCase(t, root, config)
		}},
		"postgres without authority": {backend: "postgres", prepare: func(t *testing.T, root string, config *tenantSecurityConfig) string {
			return writeTenantSecurityConfigCase(t, root, config)
		}},
		"spoofable remote profile": {prepare: func(t *testing.T, root string, config *tenantSecurityConfig) string {
			entry := config.Surfaces[string(tenantsecurity.SurfaceAPI)]
			entry.ProfileSubject = "payload-subject"
			config.Surfaces[string(tenantsecurity.SurfaceAPI)] = entry
			return writeTenantSecurityConfigCase(t, root, config)
		}},
		"missing cli profile": {prepare: func(t *testing.T, root string, config *tenantSecurityConfig) string {
			entry := config.Surfaces[string(tenantsecurity.SurfaceCLI)]
			entry.ProfileSubject = ""
			config.Surfaces[string(tenantsecurity.SurfaceCLI)] = entry
			return writeTenantSecurityConfigCase(t, root, config)
		}},
		"missing worker provider": {prepare: func(t *testing.T, root string, config *tenantSecurityConfig) string {
			delete(config.Surfaces, string(tenantsecurity.SurfaceWorker))
			return writeTenantSecurityConfigCase(t, root, config)
		}},
		"invalid quota policy": {prepare: func(t *testing.T, root string, config *tenantSecurityConfig) string {
			config.Quota.Version = 0
			return writeTenantSecurityConfigCase(t, root, config)
		}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			config := validConfig(root)
			path := test.prepare(t, root, &config)
			t.Setenv("FEATUREPROOF_TENANT_SECURITY_CONFIG", path)
			t.Setenv("FEATUREPROOF_CONTROL_PLANE_BACKEND", test.backend)
			t.Setenv("FEATUREPROOF_CONTROL_PLANE_POSTGRES_DSN", "")
			composition, err := composeTenantSecurity(t.Context())
			if composition != nil {
				composition.Close()
			}
			if err == nil || composition != nil {
				t.Fatalf("incomplete trust boundary started: composition=%+v err=%v", composition, err)
			}
		})
	}
}

// TestProductSurfaceHelpersFailClosedWithoutRequestIdentity proves helper
// composition cannot authorize an unknown surface or a remote request that
// omitted its transport-authenticated subject.
func TestProductSurfaceHelpersFailClosedWithoutRequestIdentity(t *testing.T) {
	root := t.TempDir()
	writeTenantSecurityCompositionFixture(t, root)
	composition, err := composeTenantSecurity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer composition.Close()
	if _, err = composition.AuthenticateSurface(t.Context(), tenantsecurity.Surface("unknown"), "subject"); err == nil {
		t.Fatal("unknown surface acquired an authenticated context")
	}
	if _, err = newTenantQuotaGate(t.Context(), composition, tenantsecurity.SurfaceAPI); err == nil {
		t.Fatal("quota capability issued without API request identity")
	}
	if authorized, authorizeErr := authorizeProductSurface(t.Context(), tenantsecurity.SurfaceAPI, tenantsecurity.ActionRunRead, tenantsecurity.ResourceRun); authorizeErr == nil || authorized != nil {
		if authorized != nil {
			authorized.Close()
		}
		t.Fatalf("remote surface authorized without identity: composition=%+v err=%v", authorized, authorizeErr)
	}
}

// writeTenantSecurityConfigCase writes one deliberately altered, non-secret
// composition document for fail-closed startup tests.
func writeTenantSecurityConfigCase(t *testing.T, root string, config *tenantSecurityConfig) string {
	t.Helper()
	payload, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "tenant-security-case.json")
	if err = os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestContractMCPAuthenticatesEveryRequestSubject proves two connections do
// not inherit a process-wide principal and spoofed credentials fail before tools.
func TestContractMCPAuthenticatesEveryRequestSubject(t *testing.T) {
	root := t.TempDir()
	writeTenantSecurityCompositionFixture(t, root)
	composition, err := composeTenantSecurity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer composition.Close()
	authenticator := tenantContractMCPAuthenticator{next: mappedMCPAuthenticator{"credential-a": "actor", "credential-b": "operator"}, security: composition}
	for credential, expected := range map[string]string{"credential-a": "actor", "credential-b": "operator"} {
		principal, authErr := authenticator.AuthenticateMCP(context.Background(), credential)
		if authErr != nil || principal != expected {
			t.Fatalf("credential=%s principal=%s err=%v", credential, principal, authErr)
		}
	}
	if _, err = authenticator.AuthenticateMCP(context.Background(), "credential-spoof"); err == nil {
		t.Fatal("spoofed MCP credential accepted")
	}
	var _ contractreview.MCPAuthenticator = authenticator
}

// TestProductTenantAdmissionPersistsPendingIntentBeforeConfirmation proves the
// command composition adapter uses the real workflow handle and reconciles
// the prepared first job without rebinding its historical configuration.
func TestProductTenantAdmissionPersistsPendingIntentBeforeConfirmation(t *testing.T) {
	root := t.TempDir()
	writeTenantSecurityCompositionFixture(t, root)
	composition, err := composeTenantSecurity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer composition.Close()
	capability, err := composition.Gateway.Authorize(context.Background(), tenantsecurity.SurfaceCLI, tenantsecurity.ActionRunAdmit, tenantsecurity.ResourceRun)
	if err != nil {
		t.Fatal(err)
	}
	service, err := tenantsecurity.NewAdmission(composition.Store, tenantsecurity.RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	admission := tenantWorkflowAdmission{service: service, capability: capability}
	digest := strings.Repeat("a", 64)
	binding := projectconfig.RunConfigurationBinding{RunID: "workflow-product", ActivationGeneration: 1, VersionDigest: digest, PolicyDigest: capability.Scope.PolicyDigest, CatalogDigest: strings.Repeat("b", 64), EffectiveDigest: strings.Repeat("c", 64), BindingDigest: strings.Repeat("d", 64)}
	wrong := binding
	wrong.RunID = "workflow-other"
	if err = admission.Prepare(context.Background(), binding.RunID, wrong); err == nil {
		t.Fatal("workflow handle substitution accepted")
	}
	if err = admission.Prepare(context.Background(), binding.RunID, binding); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := composition.Store.Snapshot(context.Background())
	job := snapshot.Jobs[capability.Scope.OrganizationID+"/"+capability.Scope.ProjectID+"/"+binding.RunID+"-first"]
	if job.State != "pending" {
		t.Fatalf("prepared job=%+v", job)
	}
	if err = admission.Confirm(context.Background(), binding.RunID); err != nil {
		t.Fatal(err)
	}
	if err = admission.Confirm(context.Background(), binding.RunID); err != nil {
		t.Fatalf("confirmation replay=%v", err)
	}
	snapshot, _ = composition.Store.Snapshot(context.Background())
	job = snapshot.Jobs[capability.Scope.OrganizationID+"/"+capability.Scope.ProjectID+"/"+binding.RunID+"-first"]
	if job.State != "queued" {
		t.Fatalf("confirmed job=%+v", job)
	}
	remoteAdmission := tenantWorkflowAdmission{service: service, capability: capability, security: composition, surface: tenantsecurity.SurfaceMCP}
	remoteContext, err := composition.AuthenticateSurface(context.Background(), tenantsecurity.SurfaceMCP, "operator")
	if err != nil {
		t.Fatal(err)
	}
	remote := binding
	remote.RunID, remote.BindingDigest = "workflow-remote", strings.Repeat("e", 64)
	if err = remoteAdmission.Prepare(remoteContext, remote.RunID, remote); err != nil {
		t.Fatal(err)
	}
	if err = remoteAdmission.Abort(context.Background(), remote.RunID); err != nil {
		t.Fatal(err)
	}
	deniedContext, err := composition.AuthenticateSurface(context.Background(), tenantsecurity.SurfaceMCP, "unknown-subject")
	if err != nil {
		t.Fatal(err)
	}
	denied := binding
	denied.RunID, denied.BindingDigest = "workflow-denied", strings.Repeat("f", 64)
	if err = remoteAdmission.Prepare(deniedContext, denied.RunID, denied); err == nil {
		t.Fatal("unknown remote workflow principal prepared")
	}
	if err = remoteAdmission.Confirm(deniedContext, denied.RunID); err == nil {
		t.Fatal("unknown remote workflow principal confirmed")
	}
}

// TestProductExternalQuotaDenialDoesNotCallExecutor proves the productive
// adapter, not only the generic T20 wrapper, reserves before preview effects.
func TestProductExternalQuotaDenialDoesNotCallExecutor(t *testing.T) {
	root := t.TempDir()
	writeTenantSecurityCompositionFixture(t, root)
	composition, err := composeTenantSecurity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer composition.Close()
	authenticated, err := composition.AuthenticateSurface(context.Background(), tenantsecurity.SurfaceExternalAgent, "operator")
	if err != nil {
		t.Fatal(err)
	}
	gate, err := newTenantQuotaGate(authenticated, composition, tenantsecurity.SurfaceExternalAgent)
	if err != nil {
		t.Fatal(err)
	}
	limit := composition.Policy.Project[tenantsecurity.QuotaEnvironment].Limit
	if _, err = composition.Quotas.Reserve(context.Background(), tenantsecurity.QuotaRequest{Capability: gate.capability, OperationID: "prefill-environment", Dimension: tenantsecurity.QuotaEnvironment, Units: limit, Policy: composition.Policy}); err != nil {
		t.Fatal(err)
	}
	next := &countingExternalExecutor{}
	executor, err := newQuotaExternalExecutor(next, gate)
	if err != nil {
		t.Fatal(err)
	}
	invalidGate := *gate
	invalidGate.service = nil
	if _, constructorErr := newQuotaExternalExecutor(next, &invalidGate); constructorErr == nil {
		t.Fatal("external executor accepted missing durable quota service")
	}
	if _, err = newQuotaExternalExecutor(next, nil); err == nil {
		t.Fatal("external executor composed without quota guard")
	}
	if _, err = newQuotaExternalRegistry(nil, gate); err == nil {
		t.Fatal("external storage composed without configured registry")
	}
	if _, err = executor.Execute(context.Background(), externalagent.Selection{Alias: "preview"}, "run-quota-denied"); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryQuotaExhausted) {
		t.Fatalf("expected quota denial, got %v", err)
	}
	if next.calls != 0 {
		t.Fatalf("executor called %d times after denial", next.calls)
	}
}

// TestProductExternalQuotaWrappersResolveEffectsAndDelegateLifecycle covers
// successful and unknown provider effects plus all non-allocating registry operations.
func TestProductExternalQuotaWrappersResolveEffectsAndDelegateLifecycle(t *testing.T) {
	root := t.TempDir()
	writeTenantSecurityCompositionFixture(t, root)
	composition, err := composeTenantSecurity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer composition.Close()
	authenticated, err := composition.AuthenticateSurface(context.Background(), tenantsecurity.SurfaceExternalAgent, "operator")
	if err != nil {
		t.Fatal(err)
	}
	gate, err := newTenantQuotaGate(authenticated, composition, tenantsecurity.SurfaceExternalAgent)
	if err != nil {
		t.Fatal(err)
	}

	identityNext := &staticExternalIdentity{}
	identity := tenantExternalIdentity{next: identityNext, security: composition}
	if principal, resolveErr := identity.Resolve(context.Background()); resolveErr != nil || principal.ID == "" || identityNext.calls != 1 {
		t.Fatalf("principal=%+v calls=%d err=%v", principal, identityNext.calls, resolveErr)
	}
	identityFailure := errors.New("identity unavailable")
	identityNext.err = identityFailure
	if _, resolveErr := identity.Resolve(context.Background()); !errors.Is(resolveErr, identityFailure) {
		t.Fatalf("identity failure=%v", resolveErr)
	}
	identityNext.err, identityNext.id = nil, "attacker"
	if _, resolveErr := identity.Resolve(context.Background()); resolveErr == nil {
		t.Fatal("unmapped external principal accepted")
	}
	identityNext.id = ""

	next := &countingExternalExecutor{}
	executor, err := newQuotaExternalExecutor(next, gate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = executor.Execute(context.Background(), externalagent.Selection{Alias: "preview"}, "run-success"); err != nil || next.calls != 1 {
		t.Fatalf("success calls=%d err=%v", next.calls, err)
	}
	providerFailure := errors.New("provider outcome unknown")
	next.err = providerFailure
	if _, err = executor.Execute(context.Background(), externalagent.Selection{Alias: "preview"}, "run-unknown"); !errors.Is(err, providerFailure) || next.calls != 2 {
		t.Fatalf("unknown calls=%d err=%v", next.calls, err)
	}

	local, err := externalagent.NewLocalRegistry(filepath.Join(root, "external-registry"))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := newQuotaExternalRegistry(local, gate)
	if err != nil {
		t.Fatal(err)
	}
	invalidGate := *gate
	invalidGate.service = nil
	if _, constructorErr := newQuotaExternalRegistry(local, &invalidGate); constructorErr == nil {
		t.Fatal("external registry accepted missing durable quota service")
	}
	if releaseErr := registry.releaseEnvironment(context.Background(), externalagent.RunRecord{}); releaseErr == nil {
		t.Fatal("environment release accepted missing durable terminal identity")
	}
	recoveryFailure := errors.New("recovery unavailable")
	failingRecovery, err := newQuotaExternalRegistry(reportingExternalRegistry{LocalRegistry: local, err: recoveryFailure}, gate)
	if err != nil || !errors.Is(failingRecovery.Recover(context.Background(), "owner", time.Now().UTC()), recoveryFailure) {
		t.Fatalf("recovery failure not preserved: %v", err)
	}
	invalidRecovery, err := newQuotaExternalRegistry(reportingExternalRegistry{LocalRegistry: local, records: []externalagent.RunRecord{{Phase: externalagent.PhaseTerminal}}}, gate)
	if err != nil || invalidRecovery.Recover(context.Background(), "owner", time.Now().UTC()) == nil {
		t.Fatalf("invalid recovery identity accepted: %v", err)
	}
	identityWithoutSubject := tenantExternalIdentity{next: externalIdentityFunc(func(context.Context) (externalagent.Principal, error) {
		return externalagent.Principal{Scopes: map[string]bool{externalagent.ScopeRead: true}}, nil
	}), security: composition}
	if _, resolveErr := identityWithoutSubject.Resolve(context.Background()); resolveErr == nil {
		t.Fatal("external identity without subject accepted")
	}
	claim, _ := registry.ClaimSubmit(context.Background(), "principal", "key", "hash")
	if claim != nil {
		claim.Release()
	}
	record := externalagent.RunRecord{InternalID: "run-storage"}
	if _, err = registry.ReserveConfigured(context.Background(), record, externalagent.ProjectResourceSnapshot{}); err == nil {
		t.Fatal("invalid configured admission unexpectedly committed")
	}
	_, _ = registry.Reserve(context.Background(), record)
	_, _ = registry.Get(context.Background(), "missing")
	_, _ = registry.Transition(context.Background(), "missing", externalagent.PhaseQueued, externalagent.PhaseRunning, "", "", "owner", time.Now().UTC())
	_, _, _ = registry.ReserveCancel(context.Background(), "missing", "key", "hash")
	_ = registry.Recover(context.Background(), "owner", time.Now().UTC())
	_, _ = registry.Events(context.Background(), "missing")
	_ = registry.MarkPublicationFailure(context.Background(), "missing", time.Now().UTC())
	registry.ReleaseOwner("missing")
}

// TestScopedExternalRegistryPreservesTenantLifecycle proves that the productive
// tenant adapter carries one trusted project coordinate across submission,
// lifecycle, journal, cancellation, and recovery operations.
func TestScopedExternalRegistryPreservesTenantLifecycle(t *testing.T) {
	local, err := externalagent.NewLocalRegistry(filepath.Join(t.TempDir(), "scoped-external"))
	if err != nil {
		t.Fatal(err)
	}
	registry := scopedExternalRegistry{
		registry: local,
		scope: controlplane.ProjectScope{
			OrganizationID: "organization-scoped",
			ProjectID:      "project-scoped",
		},
	}
	ctx := context.Background()
	claim, err := registry.ClaimSubmit(ctx, "alice", strings.Repeat("c", 64), strings.Repeat("d", 64))
	if err != nil || claim == nil {
		t.Fatalf("claim=%v err=%v", claim, err)
	}
	claim.Release()

	now := time.Unix(2000, 0).UTC()
	record := externalagent.RunRecord{
		Handle:         "opaque_scoped_handle_00000000001",
		InternalID:     "opaque_scoped_internal_000000001",
		PrincipalID:    "alice",
		SelectionAlias: "preview",
		RequestHash:    strings.Repeat("a", 64),
		SubmitKeyHash:  strings.Repeat("b", 64),
		Phase:          externalagent.PhaseQueued,
		Owner:          "owner-scoped",
		UpdatedAt:      now,
	}
	reserved, err := registry.Reserve(ctx, record)
	if err != nil || !reserved.New {
		t.Fatalf("reserve=%+v err=%v", reserved, err)
	}
	defer registry.ReleaseOwner(record.Handle)
	loaded, err := registry.Get(ctx, record.Handle)
	if err != nil || loaded.InternalID != record.InternalID {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	if _, err = registry.Transition(ctx, record.Handle, externalagent.PhaseQueued, externalagent.PhaseRunning, "", "", record.Owner, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = registry.MarkPublicationFailure(ctx, record.Handle, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if events, eventsErr := registry.Events(ctx, record.Handle); eventsErr != nil || len(events) < 2 {
		t.Fatalf("events=%d err=%v", len(events), eventsErr)
	}
	if _, _, err = registry.ReserveCancel(ctx, record.Handle, strings.Repeat("e", 64), strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	if recovered, recoverErr := registry.RecoverTerminal(ctx, "new-owner", now.Add(10*time.Minute)); recoverErr != nil {
		t.Fatalf("recovered=%+v err=%v", recovered, recoverErr)
	}
}

// TestProductExternalEnvironmentQuotaFollowsDurableRunLifecycle proves that
// committed environment capacity remains outstanding until a durable terminal
// fact and is released exactly once for success, cancellation, error, and
// restart recovery.
func TestProductExternalEnvironmentQuotaFollowsDurableRunLifecycle(t *testing.T) {
	root := t.TempDir()
	writeTenantSecurityCompositionFixture(t, root)
	composition, err := composeTenantSecurity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer composition.Close()
	authenticated, err := composition.AuthenticateSurface(context.Background(), tenantsecurity.SurfaceExternalAgent, "operator")
	if err != nil {
		t.Fatal(err)
	}
	gate, err := newTenantQuotaGate(authenticated, composition, tenantsecurity.SurfaceExternalAgent)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := newQuotaExternalExecutor(&countingExternalExecutor{}, gate)
	if err != nil {
		t.Fatal(err)
	}
	registryRoot := filepath.Join(root, "external-lifecycle-registry")
	local, err := externalagent.NewLocalRegistry(registryRoot)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := newQuotaExternalRegistry(local, gate)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	newRecord := func(suffix string) externalagent.RunRecord {
		return externalagent.RunRecord{
			Handle:         "external-handle-" + suffix,
			InternalID:     "external-run-" + suffix,
			PrincipalID:    "principal-operator",
			SelectionAlias: "preview",
			RequestHash:    strings.Repeat("a", 64),
			SubmitKeyHash:  strings.Repeat(suffix[:1], 64),
			Phase:          externalagent.PhaseQueued,
			UpdatedAt:      now,
		}
	}
	assertReleased := func(runID string, want bool) {
		t.Helper()
		snapshot, snapshotErr := composition.Store.Snapshot(context.Background())
		if snapshotErr != nil {
			t.Fatal(snapshotErr)
		}
		reservationID := tenantQuotaOperation("external-environment", "preview", runID)
		key := gate.capability.Scope.OrganizationID + "/" + gate.capability.Scope.ProjectID + "/" + reservationID
		reservation, ok := snapshot.Reservations[key]
		if !ok || reservation.Released != want {
			t.Fatalf("reservation %s=%+v exists=%v want released=%v", runID, reservation, ok, want)
		}
	}
	reserveEnvironment := func(record externalagent.RunRecord) {
		t.Helper()
		if _, executeErr := executor.Execute(context.Background(), externalagent.Selection{Alias: record.SelectionAlias}, record.InternalID); executeErr != nil {
			t.Fatal(executeErr)
		}
		if _, reserveErr := registry.Reserve(context.Background(), record); reserveErr != nil {
			t.Fatal(reserveErr)
		}
		assertReleased(record.InternalID, false)
	}

	passed := newRecord("passed")
	reserveEnvironment(passed)
	if _, err = registry.Transition(context.Background(), passed.Handle, externalagent.PhaseQueued, externalagent.PhaseRunning, "", "", "owner-live", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Transition(context.Background(), passed.Handle, externalagent.PhaseRunning, externalagent.PhaseFinalizing, "", "", "owner-live", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Transition(context.Background(), passed.Handle, externalagent.PhaseFinalizing, externalagent.PhaseTerminal, externalagent.OutcomePassed, "package", "owner-live", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	// A repeated terminal observation must not decrement counters twice.
	if _, err = registry.Transition(context.Background(), passed.Handle, externalagent.PhaseFinalizing, externalagent.PhaseTerminal, externalagent.OutcomePassed, "package", "owner-live", now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	assertReleased(passed.InternalID, true)

	cancelled := newRecord("cancelled")
	reserveEnvironment(cancelled)
	if _, _, err = registry.ReserveCancel(context.Background(), cancelled.Handle, strings.Repeat("c", 64), strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Transition(context.Background(), cancelled.Handle, externalagent.PhaseQueued, externalagent.PhaseTerminal, externalagent.OutcomeCancelled, "", "", now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	assertReleased(cancelled.InternalID, true)

	failed := newRecord("error")
	reserveEnvironment(failed)
	if _, err = registry.Transition(context.Background(), failed.Handle, externalagent.PhaseQueued, externalagent.PhaseRunning, "", "", "owner-error", now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Transition(context.Background(), failed.Handle, externalagent.PhaseRunning, externalagent.PhaseTerminal, externalagent.OutcomeError, "", "owner-error", now.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	assertReleased(failed.InternalID, true)

	interrupted := newRecord("restart")
	reserveEnvironment(interrupted)
	if _, err = registry.Transition(context.Background(), interrupted.Handle, externalagent.PhaseQueued, externalagent.PhaseRunning, "", "", "owner-old", now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	local.ReleaseOwner(interrupted.Handle)
	reopened, err := externalagent.NewLocalRegistry(registryRoot)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := newQuotaExternalRegistry(reopened, gate)
	if err != nil {
		t.Fatal(err)
	}
	if err = recovery.Recover(context.Background(), "owner-new", now.Add(9*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = recovery.Recover(context.Background(), "owner-new", now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	assertReleased(interrupted.InternalID, true)

	outstanding := newRecord("outstanding")
	reserveEnvironment(outstanding)
	assertReleased(outstanding.InternalID, false)
	for _, record := range []externalagent.RunRecord{passed, cancelled, failed, interrupted, outstanding} {
		local.ReleaseOwner(record.Handle)
		reopened.ReleaseOwner(record.Handle)
	}
}

// TestProductExternalUnknownEffectRequiresProviderReconciliation proves an
// application terminal/cancel fact cannot release uncertain provider capacity;
// a fresh process releases it only after durable provider reconciliation and
// repeated terminal delivery remains exactly once.
func TestProductExternalUnknownEffectRequiresProviderReconciliation(t *testing.T) {
	root := t.TempDir()
	writeTenantSecurityCompositionFixture(t, root)
	composition, err := composeTenantSecurity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer composition.Close()
	authenticated, err := composition.AuthenticateSurface(context.Background(), tenantsecurity.SurfaceExternalAgent, "operator")
	if err != nil {
		t.Fatal(err)
	}
	gate, err := newTenantQuotaGate(authenticated, composition, tenantsecurity.SurfaceExternalAgent)
	if err != nil {
		t.Fatal(err)
	}
	providerFailure := errors.New("provider response lost")
	providerStillUnknown := errors.New("provider reconciliation unavailable")
	provider := &countingExternalExecutor{err: providerFailure, reconcileErr: providerStillUnknown}
	executor, err := newQuotaExternalExecutor(provider, gate)
	if err != nil {
		t.Fatal(err)
	}
	registryRoot := filepath.Join(root, "external-unknown-registry")
	local, err := externalagent.NewLocalRegistry(registryRoot)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := newQuotaExternalRegistry(local, gate)
	if err != nil {
		t.Fatal(err)
	}
	registry.reconciler = executor
	record := externalagent.RunRecord{Handle: "external-handle-unknown", InternalID: "external-run-unknown", PrincipalID: "principal-operator", SelectionAlias: "preview", RequestHash: strings.Repeat("a", 64), SubmitKeyHash: strings.Repeat("b", 64), Phase: externalagent.PhaseQueued, UpdatedAt: time.Now().UTC()}
	if _, err = executor.Execute(context.Background(), externalagent.Selection{Alias: record.SelectionAlias}, record.InternalID); !errors.Is(err, providerFailure) {
		t.Fatalf("provider unknown=%v", err)
	}
	if _, err = registry.Reserve(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	defer local.ReleaseOwner(record.Handle)
	if _, err = registry.Transition(context.Background(), record.Handle, externalagent.PhaseQueued, externalagent.PhaseTerminal, externalagent.OutcomeError, "", "", time.Now().UTC()); !errors.Is(err, providerStillUnknown) {
		t.Fatalf("terminal released unknown effect: %v", err)
	}
	reservationID := tenantQuotaOperation("external-environment", record.SelectionAlias, record.InternalID)
	reservation, err := gate.service.ReservationState(context.Background(), gate.capability, reservationID)
	if err != nil || reservation.Released || !reservation.Uncertain || provider.reconcileCalls != 1 {
		t.Fatalf("uncertain reservation=%+v reconciles=%d err=%v", reservation, provider.reconcileCalls, err)
	}

	local.ReleaseOwner(record.Handle)
	reopened, err := externalagent.NewLocalRegistry(registryRoot)
	if err != nil {
		t.Fatal(err)
	}
	restartedProvider := &countingExternalExecutor{}
	restartedExecutor, err := newQuotaExternalExecutor(restartedProvider, gate)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := newQuotaExternalRegistry(reopened, gate)
	if err != nil {
		t.Fatal(err)
	}
	restarted.reconciler = restartedExecutor
	if _, err = restarted.Transition(context.Background(), record.Handle, externalagent.PhaseQueued, externalagent.PhaseTerminal, externalagent.OutcomeError, "", "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Transition(context.Background(), record.Handle, externalagent.PhaseQueued, externalagent.PhaseTerminal, externalagent.OutcomeError, "", "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	reservation, err = gate.service.ReservationState(context.Background(), gate.capability, reservationID)
	if err != nil || !reservation.Released || reservation.Uncertain || restartedProvider.reconcileCalls != 1 {
		t.Fatalf("reconciled reservation=%+v reconciles=%d err=%v", reservation, restartedProvider.reconcileCalls, err)
	}
	reopened.ReleaseOwner(record.Handle)
}

// TestTenantQuotaGateCoversProductReservationLifecycles proves model,
// environment and worker claims all reserve durably and resolve explicitly.
func TestTenantQuotaGateCoversProductReservationLifecycles(t *testing.T) {
	root := t.TempDir()
	writeTenantSecurityCompositionFixture(t, root)
	composition, err := composeTenantSecurity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer composition.Close()
	gate, err := newTenantQuotaGate(context.Background(), composition, tenantsecurity.SurfaceCLI)
	if err != nil {
		t.Fatal(err)
	}

	generationContext := validationworkflow.WithGenerationQuotaIdentity(context.Background(), "workflow-quota")
	generation, err := gate.ReserveGeneration(generationContext, validationworkflow.AdmissionSnapshot{SpecificationDigest: "specification", RevisionDigest: "revision", TargetDigest: "target"})
	if err != nil || generation.ID == "" {
		t.Fatalf("generation=%+v err=%v", generation, err)
	}
	if err = gate.ResolveGeneration(context.Background(), generation, validationworkflow.ModelUsage{}, false); err != nil {
		t.Fatal(err)
	}
	uncertainGeneration, err := gate.ReserveGeneration(validationworkflow.WithGenerationQuotaIdentity(context.Background(), "workflow-uncertain"), validationworkflow.AdmissionSnapshot{SpecificationDigest: "specification", RevisionDigest: "revision", TargetDigest: "target"})
	if err != nil || gate.ResolveGeneration(context.Background(), uncertainGeneration, validationworkflow.ModelUsage{}, true) != nil {
		t.Fatalf("uncertain generation=%+v err=%v", uncertainGeneration, err)
	}
	if _, err = gate.ReserveGeneration(context.Background(), validationworkflow.AdmissionSnapshot{}); err == nil {
		t.Fatal("generation without workflow identity was accepted")
	}

	acquire, err := gate.ReserveAcquire(context.Background(), environment.AcquisitionRequest{RunID: "run-quota", IdempotencyKey: "acquire-quota", Selection: environment.Selection{Digest: "selection"}})
	if err != nil || acquire.ID == "" {
		t.Fatalf("acquire=%+v err=%v", acquire, err)
	}
	if err = gate.ResolveAcquire(context.Background(), acquire, environment.Observation{}, true); err != nil {
		t.Fatal(err)
	}
	releasedAcquire, err := gate.ReserveAcquire(context.Background(), environment.AcquisitionRequest{RunID: "run-release", IdempotencyKey: "acquire-release", Selection: environment.Selection{Digest: "selection"}})
	if err != nil || gate.ResolveAcquire(context.Background(), releasedAcquire, environment.Observation{}, false) != nil {
		t.Fatalf("released acquire=%+v err=%v", releasedAcquire, err)
	}
	secondAcquire, err := gate.ReserveAcquire(context.Background(), environment.AcquisitionRequest{RunID: "run-second", IdempotencyKey: "acquire-second", Selection: environment.Selection{Digest: "selection"}})
	if err != nil || gate.ResolveAcquire(context.Background(), secondAcquire, environment.Observation{}, false) != nil {
		t.Fatalf("second acquire=%+v err=%v", secondAcquire, err)
	}
	snapshot, err := composition.Store.Snapshot(context.Background())
	if err != nil || snapshot.Reservations["organization/project/"+releasedAcquire.ID].Released || snapshot.Reservations["organization/project/"+secondAcquire.ID].Released {
		t.Fatalf("active environment reservations were not retained: err=%v reservations=%+v", err, snapshot.Reservations)
	}
	releaseRequest := environment.ReleaseRequest{RunID: "run-release", AcquisitionKey: "acquire-release", LifecycleDigest: "lifecycle", Selection: environment.Selection{Digest: "selection"}}
	if err = gate.ReleaseAcquire(context.Background(), releaseRequest, false); err != nil {
		t.Fatal(err)
	}
	uncertainReleaseRequest := environment.ReleaseRequest{RunID: "run-unknown-release", AcquisitionKey: "acquire-unknown-release", LifecycleDigest: "lifecycle", Selection: environment.Selection{Digest: "selection"}}
	uncertainAcquire, err := gate.ReserveAcquire(context.Background(), environment.AcquisitionRequest{RunID: uncertainReleaseRequest.RunID, IdempotencyKey: uncertainReleaseRequest.AcquisitionKey, Selection: uncertainReleaseRequest.Selection})
	if err != nil || uncertainAcquire.ID == "" {
		t.Fatalf("uncertain acquire=%+v err=%v", uncertainAcquire, err)
	}
	if err = gate.ReleaseAcquire(context.Background(), uncertainReleaseRequest, true); err != nil {
		t.Fatal(err)
	}

	claimGate := tenantClaimQuotaGate{tenantQuotaGate: gate}
	request := controlplane.ClaimRequest{Scope: controlplane.ProjectScope{OrganizationID: gate.capability.Scope.OrganizationID, ProjectID: gate.capability.Scope.ProjectID}, WorkerID: "worker-quota", Now: time.Unix(1, 0).UTC(), Lease: time.Minute, Limit: 2}
	claim, err := claimGate.ReserveClaim(context.Background(), request)
	if err != nil || claim.ID == "" {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	claimedJob := controlplane.Job{OrganizationID: request.Scope.OrganizationID, ProjectID: request.Scope.ProjectID, JobID: "job-quota"}
	if err = claimGate.ConfirmClaim(context.Background(), claim, []controlplane.Job{claimedJob}); err != nil {
		t.Fatal(err)
	}
	restartedClaimGate := tenantClaimQuotaGate{tenantQuotaGate: &tenantQuotaGate{service: composition.Quotas, capability: gate.capability, policy: gate.policy}}
	if err = restartedClaimGate.ReleaseClaim(context.Background(), request.Scope, claimedJob.JobID, false); err != nil {
		t.Fatal(err)
	}
	foreignScope := request.Scope
	foreignScope.OrganizationID = "other-organization"
	if err = restartedClaimGate.ReleaseClaim(context.Background(), foreignScope, claimedJob.JobID, false); err == nil {
		t.Fatal("cross-tenant terminal release accepted")
	}
	if err = restartedClaimGate.ReleaseClaim(context.Background(), request.Scope, claimedJob.JobID, false); err != nil {
		t.Fatalf("terminal replay=%v", err)
	}
	request.WorkerID = "worker-empty"
	emptyClaim, err := claimGate.ReserveClaim(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err = claimGate.ConfirmClaim(context.Background(), emptyClaim, nil); err != nil {
		t.Fatal(err)
	}
	request.WorkerID = "worker-rejected"
	rejectedClaim, err := claimGate.ReserveClaim(context.Background(), request)
	if err != nil || claimGate.RejectClaim(context.Background(), rejectedClaim, false) != nil {
		t.Fatalf("rejected claim=%+v err=%v", rejectedClaim, err)
	}
	request.WorkerID = "worker-uncertain"
	uncertainClaim, err := claimGate.ReserveClaim(context.Background(), request)
	if err != nil || claimGate.RejectClaim(context.Background(), uncertainClaim, true) != nil {
		t.Fatalf("uncertain claim=%+v err=%v", uncertainClaim, err)
	}
	request.Scope.OrganizationID = "other-organization"
	if _, err = claimGate.ReserveClaim(context.Background(), request); err == nil {
		t.Fatal("cross-tenant claim quota was accepted")
	}
	request.Scope = controlplane.ProjectScope{OrganizationID: gate.capability.Scope.OrganizationID, ProjectID: gate.capability.Scope.ProjectID}
	foreignJob := claimedJob
	foreignJob.OrganizationID = "other-organization"
	claim, err = claimGate.ReserveClaim(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err = claimGate.ConfirmClaim(context.Background(), claim, []controlplane.Job{foreignJob}); err == nil {
		t.Fatal("cross-tenant claimed job accepted")
	}
	if _, err = newTenantQuotaGate(context.Background(), nil, tenantsecurity.SurfaceCLI); err == nil {
		t.Fatal("nil quota composition was accepted")
	}
}
