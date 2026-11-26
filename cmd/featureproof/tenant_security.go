package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
	controlplanepg "github.com/asierhr/featureProof/internal/controlplane/postgres"
	"github.com/asierhr/featureProof/internal/environment"
	"github.com/asierhr/featureProof/internal/externalagent"
	"github.com/asierhr/featureProof/internal/projectconfig"
	"github.com/asierhr/featureProof/internal/tenantsecurity"
	tenantlocal "github.com/asierhr/featureProof/internal/tenantsecurity/local"
	"github.com/asierhr/featureProof/internal/validationworkflow"
)

// tenantSecurityConfig is the non-secret composition document that selects a
// trusted authenticated subject/resource provider for every product surface.
type tenantSecurityConfig struct {
	CapabilityTTLSeconds uint64                                 `json:"capability_ttl_seconds"`
	StoreRoot            string                                 `json:"store_root"`
	Surfaces             map[string]tenantSecuritySurfaceConfig `json:"surfaces"`
	Quota                tenantsecurity.QuotaPolicySnapshot     `json:"quota"`
}

// tenantSecuritySurfaceConfig contains only authenticated profile identities
// and trusted resource coordinates; memberships remain provisioned in store.
type tenantSecuritySurfaceConfig struct {
	Provider       string `json:"provider"`
	Issuer         string `json:"issuer"`
	ProfileSubject string `json:"profile_subject,omitempty"`
	OrganizationID string `json:"organization_id"`
	ProjectID      string `json:"project_id"`
	RepositoryID   string `json:"repository_id"`
}

// tenantSecurityComposition owns common authorization and quota services plus
// the selected durable backend lifecycle.
type tenantSecurityComposition struct {
	Gateway *tenantsecurity.SurfaceGateway
	Quotas  *tenantsecurity.DurableQuotaService
	Policy  tenantsecurity.QuotaPolicySnapshot
	Store   tenantsecurity.DurableStore
	issuers map[tenantsecurity.Surface]string
	close   func()
}

// tenantMCPAuthenticator resolves opaque bearer credentials supplied by the
// trusted MCP transport and reauthorizes their subject against live tenant
// state on every request.
type tenantMCPAuthenticator struct {
	security *tenantSecurityComposition
	surface  tenantsecurity.Surface
	profiles map[string]string
}

// loadTenantMCPAuthenticator loads an environment-owned credential map. Keys
// are opaque secrets and values are authenticated provider subjects.
func loadTenantMCPAuthenticator(security *tenantSecurityComposition, surface tenantsecurity.Surface, environment string) (*tenantMCPAuthenticator, error) {
	raw := os.Getenv(environment)
	if security == nil || raw == "" || len(raw) > tenantsecurity.MaxPayloadBytes {
		return nil, errors.New("MCP authentication unavailable")
	}
	profiles := map[string]string{}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&profiles); err != nil || !errors.Is(decodeEOF(decoder), io.EOF) || len(profiles) == 0 || len(profiles) > tenantsecurity.MaxItems {
		return nil, errors.New("MCP authentication unavailable")
	}
	for credential, subject := range profiles {
		if len(credential) < 16 || len(credential) > 512 || subject == "" || len(subject) > 256 {
			return nil, errors.New("MCP authentication unavailable")
		}
	}
	return &tenantMCPAuthenticator{security: security, surface: surface, profiles: profiles}, nil
}

// subject compares every configured credential in constant time and never
// trusts principal-like fields carried inside operation arguments.
func (auth *tenantMCPAuthenticator) subject(credential string) (string, error) {
	if auth == nil || len(credential) < 16 || len(credential) > 512 {
		return "", errors.New("MCP authentication failed")
	}
	subject := ""
	for candidate, configuredSubject := range auth.profiles {
		if len(candidate) == len(credential) && subtle.ConstantTimeCompare([]byte(candidate), []byte(credential)) == 1 {
			subject = configuredSubject
		}
	}
	if subject == "" {
		return "", errors.New("MCP authentication failed")
	}
	return subject, nil
}

// authorize resolves the current membership and policy head for one request.
func (auth *tenantMCPAuthenticator) authorize(ctx context.Context, credential, operation string) (context.Context, string, tenantsecurity.Action, error) {
	subject, err := auth.subject(credential)
	if err != nil {
		return ctx, "", "", err
	}
	authenticated, err := auth.security.AuthenticateSurface(ctx, auth.surface, subject)
	if err != nil {
		return ctx, "", "", err
	}
	action := tenantsecurity.ActionRunRead
	switch operation {
	case "workflow.start", "workflow.decide", "submit", "correction_submit":
		action = tenantsecurity.ActionRunAdmit
	case "workflow.cancel", "cancel", "correction_cancel":
		action = tenantsecurity.ActionRunCancel
	}
	capability, err := auth.security.Gateway.Authorize(authenticated, auth.surface, action, tenantsecurity.ResourceRun)
	if err != nil {
		return ctx, "", "", err
	}
	return authenticated, capability.Scope.PrincipalID, action, nil
}

// AuthenticateWorkflowMCP implements workflow per-request transport auth.
func (auth *tenantMCPAuthenticator) AuthenticateWorkflowMCP(ctx context.Context, credential, operation string) (context.Context, string, error) {
	authenticated, principalID, _, err := auth.authorize(ctx, credential, operation)
	return authenticated, principalID, err
}

// AuthenticateMCP implements external-agent per-request transport auth and
// grants only the application scope required by this exact operation.
func (auth *tenantMCPAuthenticator) AuthenticateMCP(ctx context.Context, credential, operation string) (context.Context, externalagent.Principal, error) {
	authenticated, principalID, action, err := auth.authorize(ctx, credential, operation)
	if err != nil {
		return ctx, externalagent.Principal{}, err
	}
	scope := externalagent.ScopeRead
	switch action {
	case tenantsecurity.ActionRunAdmit:
		if strings.HasPrefix(operation, "correction_") {
			scope = externalagent.ScopeCorrectionCreate
		} else {
			scope = externalagent.ScopeCreate
		}
	case tenantsecurity.ActionRunCancel:
		if strings.HasPrefix(operation, "correction_") {
			scope = externalagent.ScopeCorrectionCancel
		} else {
			scope = externalagent.ScopeCancel
		}
	case tenantsecurity.ActionRunRead:
		if strings.HasPrefix(operation, "correction_") {
			scope = externalagent.ScopeCorrectionRead
		}
	}
	return authenticated, externalagent.Principal{ID: principalID, Scopes: map[string]bool{scope: true}}, nil
}

// AuthenticateSurface adds one adapter-authenticated opaque subject to a
// request context using the issuer selected for that exact surface.
func (composition *tenantSecurityComposition) AuthenticateSurface(ctx context.Context, surface tenantsecurity.Surface, subject string) (context.Context, error) {
	if composition == nil || subject == "" {
		return nil, errors.New("tenant security subject unavailable")
	}
	issuer := composition.issuers[surface]
	if issuer == "" {
		return nil, errors.New("tenant security issuer unavailable")
	}
	return tenantsecurity.WithAuthenticatedSurface(ctx, surface, tenantsecurity.AuthenticatedSubject{Issuer: issuer, Subject: subject})
}

// authorizeProductSurface composes and authorizes one ingress while returning
// the owner so callers retain PostgreSQL resources for the full operation.
func authorizeProductSurface(ctx context.Context, surface tenantsecurity.Surface, action tenantsecurity.Action, resource tenantsecurity.ResourceClass) (*tenantSecurityComposition, error) {
	composition, err := composeTenantSecurity(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = composition.Gateway.Authorize(ctx, surface, action, resource); err != nil {
		composition.Close()
		return nil, err
	}
	return composition, nil
}

// Close releases only composition-owned PostgreSQL resources.
func (composition *tenantSecurityComposition) Close() {
	if composition != nil && composition.close != nil {
		composition.close()
	}
}

// composeTenantSecurity loads mandatory non-secret providers, selects exactly
// one durable backend and rejects missing/partial security before product I/O.
func composeTenantSecurity(ctx context.Context) (*tenantSecurityComposition, error) {
	path := os.Getenv("FEATUREPROOF_TENANT_SECURITY_CONFIG")
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("tenant security composition unavailable")
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil || len(raw) == 0 || len(raw) > tenantsecurity.MaxPayloadBytes {
		return nil, errors.New("tenant security composition unavailable")
	}
	var config tenantSecurityConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&config); err != nil || !errors.Is(decodeEOF(decoder), io.EOF) || config.CapabilityTTLSeconds == 0 {
		return nil, errors.New("tenant security composition unavailable")
	}
	limits := tenantsecurity.DefaultLimits()
	limits.CapabilityTTL = time.Duration(config.CapabilityTTLSeconds) * time.Second
	if tenantsecurity.ValidateLimits(limits) != nil {
		return nil, errors.New("tenant security composition unavailable")
	}
	var store tenantsecurity.DurableStore
	closeStore := func() {}
	backend := os.Getenv("FEATUREPROOF_CONTROL_PLANE_BACKEND")
	switch backend {
	case "", string(controlplane.BackendLocal):
		if !filepath.IsAbs(config.StoreRoot) {
			return nil, errors.New("tenant security store unavailable")
		}
		store, err = tenantlocal.New(filepath.Clean(config.StoreRoot))
	case string(controlplane.BackendPostgres):
		observer := controlPlaneObserver{logger: slog.New(slog.NewJSONHandler(os.Stderr, nil))}
		identity := os.Getenv("FEATUREPROOF_CONTROL_PLANE_IDENTITY")
		if identity == "" {
			identity = "tenant-security-postgres"
		}
		owner, openErr := controlplanepg.Open(ctx, controlplanepg.Config{DSN: os.Getenv("FEATUREPROOF_CONTROL_PLANE_POSTGRES_DSN"), BackendIdentity: identity, MaxConnections: 8, StatementTimeout: 5 * time.Second, MaxLease: 5 * time.Minute, Observer: observer, IsolatedLoopbackTest: isolatedPostgresGateAuthorized()})
		if openErr != nil {
			return nil, openErr
		}
		closeStore = owner.Close
		if err = owner.Migrate(ctx); err == nil {
			err = owner.Ready(ctx)
		}
		if err == nil {
			store = owner.TenantSecurity()
		}
	default:
		return nil, errors.New("tenant security backend unavailable")
	}
	if err != nil || store == nil {
		closeStore()
		return nil, errors.New("tenant security store unavailable")
	}
	authority, err := tenantsecurity.NewDurableAuthority(store, tenantsecurity.RealClock{}, limits, nil)
	if err != nil {
		closeStore()
		return nil, err
	}
	providers := make(map[tenantsecurity.Surface]tenantsecurity.SurfaceBindingProvider, len(config.Surfaces))
	issuers := make(map[tenantsecurity.Surface]string, len(config.Surfaces))
	for name, surfaceConfig := range config.Surfaces {
		surface := tenantsecurity.Surface(name)
		if !validTenantSurfaceProvider(surface, surfaceConfig.Provider) || surface == tenantsecurity.SurfaceCLI && surfaceConfig.ProfileSubject == "" || surface != tenantsecurity.SurfaceCLI && surfaceConfig.ProfileSubject != "" {
			closeStore()
			return nil, errors.New("tenant security provider unavailable")
		}
		resource := tenantsecurity.TrustedResourceIdentity{OrganizationID: surfaceConfig.OrganizationID, ProjectID: surfaceConfig.ProjectID, RepositoryID: surfaceConfig.RepositoryID}
		issuers[surface] = surfaceConfig.Issuer
		if surface == tenantsecurity.SurfaceCLI {
			providers[surface] = tenantsecurity.StaticSurfaceBindingProvider{Value: tenantsecurity.SurfaceBinding{Subject: tenantsecurity.AuthenticatedSubject{Issuer: surfaceConfig.Issuer, Subject: surfaceConfig.ProfileSubject}, Resource: resource}}
		} else {
			providers[surface] = tenantsecurity.RequestSurfaceBindingProvider{Surface: surface, Issuer: surfaceConfig.Issuer, Resource: resource}
		}
	}
	gateway, err := tenantsecurity.NewProviderSurfaceGateway(authority, providers)
	if err != nil {
		closeStore()
		return nil, err
	}
	policy, err := tenantsecurity.CompileQuotaPolicy(config.Quota)
	if err != nil {
		closeStore()
		return nil, err
	}
	quotas, err := tenantsecurity.NewDurableQuotaService(store, tenantsecurity.RealClock{})
	if err != nil {
		closeStore()
		return nil, err
	}
	return &tenantSecurityComposition{Gateway: gateway, Quotas: quotas, Policy: policy, Store: store, issuers: issuers, close: closeStore}, nil
}

// decodeEOF confirms one and only one strict JSON document.
func decodeEOF(decoder *json.Decoder) error { return decoder.Decode(&struct{}{}) }

// validTenantSurfaceProvider binds each surface to its closed trusted adapter
// kind so a config cannot substitute a payload-derived generic provider.
func validTenantSurfaceProvider(surface tenantsecurity.Surface, provider string) bool {
	expected := map[tenantsecurity.Surface]string{tenantsecurity.SurfaceCLI: "cli_profile", tenantsecurity.SurfaceAPI: "api_identity", tenantsecurity.SurfaceMCP: "mcp_identity", tenantsecurity.SurfaceGitHub: "github_installation", tenantsecurity.SurfaceExternalAgent: "external_profile", tenantsecurity.SurfaceWorker: "sealed_worker_envelope"}
	return expected[surface] == provider
}

// tenantQuotaGate adapts current tenant authority to workflow and environment
// consumer-owned quota ports without exposing policy to either consumer.
type tenantQuotaGate struct {
	service    *tenantsecurity.DurableQuotaService
	capability tenantsecurity.Capability
	policy     tenantsecurity.QuotaPolicySnapshot
}

// tenantClaimQuotaGate adapts the same durable quota service to scheduler
// claims made by a sealed worker surface.
type tenantClaimQuotaGate struct{ *tenantQuotaGate }

// tenantWorkflowAdmission adapts the workflow's actual durable handle and
// historical project binding to the tenant aggregate admission boundary.
type tenantWorkflowAdmission struct {
	service    *tenantsecurity.Admission
	capability tenantsecurity.Capability
	security   *tenantSecurityComposition
	surface    tenantsecurity.Surface
}

// requestCapability reauthorizes remotely authenticated requests against the
// live security head; startup recovery alone uses the sealed bootstrap grant.
func (admission tenantWorkflowAdmission) requestCapability(ctx context.Context) (tenantsecurity.Capability, error) {
	if admission.security != nil && tenantsecurity.HasAuthenticatedSurface(ctx, admission.surface) {
		return admission.security.Gateway.Authorize(ctx, admission.surface, tenantsecurity.ActionRunAdmit, tenantsecurity.ResourceRun)
	}
	return admission.capability, nil
}

// encodeHistoricalCapability stores only the secret-free minimal capability
// required to finish the already prepared tenant intent after restart.
func encodeHistoricalCapability(capability tenantsecurity.Capability) (string, error) {
	encoded, err := json.Marshal(capability)
	if err != nil || capability.Scope.PrincipalID == "" || capability.Digest == "" {
		return "", errors.New("tenant workflow authority unavailable")
	}
	return string(encoded), nil
}

// decodeHistoricalCapability rejects malformed or broadened recovery grants.
func decodeHistoricalCapability(encoded string) (tenantsecurity.Capability, error) {
	var capability tenantsecurity.Capability
	if encoded == "" || json.Unmarshal([]byte(encoded), &capability) != nil || capability.Action != tenantsecurity.ActionRunAdmit || capability.Resource != tenantsecurity.ResourceRun {
		return tenantsecurity.Capability{}, errors.New("tenant workflow authority unavailable")
	}
	expected, err := tenantsecurity.HistoricalCapability(capability.Scope, capability.Action, capability.Resource)
	if err != nil || expected.Digest != capability.Digest {
		return tenantsecurity.Capability{}, errors.New("tenant workflow authority unavailable")
	}
	return capability, nil
}

// Prepare seals a non-runnable tenant authority/configuration intent before
// the workflow store publishes its run and first event.
func (admission tenantWorkflowAdmission) Prepare(ctx context.Context, runID string, binding projectconfig.RunConfigurationBinding) error {
	if binding.RunID != runID {
		return errors.New("tenant workflow binding mismatch")
	}
	coordinate := tenantsecurity.ConfigurationCoordinate{Generation: binding.ActivationGeneration, VersionDigest: binding.VersionDigest, PolicyDigest: binding.PolicyDigest, CatalogDigest: binding.CatalogDigest, EffectiveDigest: binding.EffectiveDigest}
	capability, err := admission.requestCapability(ctx)
	if err != nil {
		return err
	}
	_, _, err = admission.service.Prepare(ctx, capability, tenantQuotaOperation("workflow-prepare", runID, binding.BindingDigest), runID, "admit", coordinate)
	return err
}

// PrepareHistorical persists the intent and returns the exact request grant
// that must be used for confirmation and recovery.
func (admission tenantWorkflowAdmission) PrepareHistorical(ctx context.Context, runID string, binding projectconfig.RunConfigurationBinding) (string, error) {
	if binding.RunID != runID {
		return "", errors.New("tenant workflow binding mismatch")
	}
	capability, err := admission.requestCapability(ctx)
	if err != nil {
		return "", err
	}
	coordinate := tenantsecurity.ConfigurationCoordinate{Generation: binding.ActivationGeneration, VersionDigest: binding.VersionDigest, PolicyDigest: binding.PolicyDigest, CatalogDigest: binding.CatalogDigest, EffectiveDigest: binding.EffectiveDigest}
	if _, _, err = admission.service.Prepare(ctx, capability, tenantQuotaOperation("workflow-prepare", runID, binding.BindingDigest), runID, "admit", coordinate); err != nil {
		return "", err
	}
	return encodeHistoricalCapability(capability)
}

// Confirm makes the prepared first job runnable after workflow durability;
// retrying the same confirmation reconciles an interrupted composition.
func (admission tenantWorkflowAdmission) Confirm(ctx context.Context, runID string) error {
	capability, err := admission.requestCapability(ctx)
	if err != nil {
		return err
	}
	return admission.service.Confirm(ctx, capability, tenantQuotaOperation("workflow-confirm", runID), runID)
}

// ConfirmHistorical uses the request capability sealed beside the workflow,
// never the process bootstrap grant.
func (admission tenantWorkflowAdmission) ConfirmHistorical(ctx context.Context, runID, authority string) error {
	capability, err := decodeHistoricalCapability(authority)
	if err != nil {
		return err
	}
	return admission.service.Confirm(ctx, capability, tenantQuotaOperation("workflow-confirm", runID), runID)
}

// Pending lists only this tenant/project's non-runnable admission intents for
// workflow crash recovery.
func (admission tenantWorkflowAdmission) Pending(ctx context.Context) ([]string, error) {
	return admission.service.Pending(ctx, admission.capability)
}

// PendingHistorical enumerates pending bindings and reconstructs each exact
// principal authority independently of the recovery process identity.
func (admission tenantWorkflowAdmission) PendingHistorical(ctx context.Context) ([]validationworkflow.TenantAdmissionIntent, error) {
	bindings, err := admission.service.PendingBindings(ctx, admission.capability)
	if err != nil {
		return nil, err
	}
	intents := make([]validationworkflow.TenantAdmissionIntent, 0, len(bindings))
	for _, binding := range bindings {
		capability, capabilityErr := tenantsecurity.HistoricalCapability(binding.Scope, tenantsecurity.ActionRunAdmit, tenantsecurity.ResourceRun)
		if capabilityErr != nil {
			return nil, capabilityErr
		}
		authority, encodeErr := encodeHistoricalCapability(capability)
		if encodeErr != nil {
			return nil, encodeErr
		}
		intents = append(intents, validationworkflow.TenantAdmissionIntent{RunID: binding.RunID, Authority: authority})
	}
	return intents, nil
}

// Abort terminalizes one prepared intent whose workflow record never committed.
func (admission tenantWorkflowAdmission) Abort(ctx context.Context, runID string) error {
	return admission.service.Abort(ctx, admission.capability, tenantQuotaOperation("workflow-abort", runID), runID)
}

// AbortHistorical cancels an orphan with the same authority that prepared it.
func (admission tenantWorkflowAdmission) AbortHistorical(ctx context.Context, runID, authority string) error {
	capability, err := decodeHistoricalCapability(authority)
	if err != nil {
		return err
	}
	return admission.service.Abort(ctx, capability, tenantQuotaOperation("workflow-abort", runID), runID)
}

// ReserveClaim meters the bounded claim batch before the queue grants leases.
func (gate tenantClaimQuotaGate) ReserveClaim(ctx context.Context, request controlplane.ClaimRequest) (controlplane.ClaimQuotaReceipt, error) {
	if request.Scope.OrganizationID != gate.capability.Scope.OrganizationID || request.Scope.ProjectID != gate.capability.Scope.ProjectID || request.Limit <= 0 {
		return controlplane.ClaimQuotaReceipt{}, controlplane.ErrUnauthorized
	}
	id := tenantQuotaOperation("claim", request.WorkerID, request.Scope.OrganizationID, request.Scope.ProjectID, request.Now.UTC().Format(time.RFC3339Nano))
	reservation, err := gate.service.Reserve(ctx, tenantsecurity.QuotaRequest{Capability: gate.capability, OperationID: id, Dimension: tenantsecurity.QuotaQueueClaim, Units: uint64(request.Limit), Policy: gate.policy})
	return controlplane.ClaimQuotaReceipt{ID: reservation.ID, Digest: reservation.Digest}, err
}

// ConfirmClaim releases an empty scan; leased batches remain reserved until
// the owning worker reconciliation proves terminal state.
func (gate tenantClaimQuotaGate) ConfirmClaim(ctx context.Context, receipt controlplane.ClaimQuotaReceipt, jobs []controlplane.Job) error {
	if len(jobs) != 0 {
		ids := make([]string, len(jobs))
		for index, job := range jobs {
			if job.OrganizationID != gate.capability.Scope.OrganizationID || job.ProjectID != gate.capability.Scope.ProjectID {
				return controlplane.ErrUnauthorized
			}
			ids[index] = job.JobID
		}
		return gate.service.BindResources(ctx, gate.capability, receipt.ID, tenantQuotaOperation("claim-bind", receipt.ID, receipt.Digest), ids)
	}
	return gate.service.Release(ctx, gate.capability, receipt.ID, tenantQuotaOperation("claim-empty", receipt.ID, receipt.Digest))
}

// RejectClaim preserves capacity when the queue result may have committed and
// releases only a known pre-effect rejection.
func (gate tenantClaimQuotaGate) RejectClaim(ctx context.Context, receipt controlplane.ClaimQuotaReceipt, uncertain bool) error {
	operation := tenantQuotaOperation("claim-reject", receipt.ID, receipt.Digest)
	if uncertain {
		return gate.service.MarkUncertain(ctx, gate.capability, receipt.ID, operation)
	}
	return gate.service.Release(ctx, gate.capability, receipt.ID, operation)
}

// ReleaseClaim releases worker capacity only after the queue proves terminal lifecycle or recovery.
func (gate tenantClaimQuotaGate) ReleaseClaim(ctx context.Context, scope controlplane.ProjectScope, jobID string, uncertain bool) error {
	if scope.OrganizationID != gate.capability.Scope.OrganizationID || scope.ProjectID != gate.capability.Scope.ProjectID {
		return controlplane.ErrUnauthorized
	}
	operation := tenantQuotaOperation("claim-terminal", scope.OrganizationID, scope.ProjectID, jobID)
	return gate.service.ReleaseResource(ctx, gate.capability, jobID, operation, uncertain)
}

// newTenantQuotaGate resolves the current security head before composing any
// model/provider wrapper.
func newTenantQuotaGate(ctx context.Context, composition *tenantSecurityComposition, surface tenantsecurity.Surface) (*tenantQuotaGate, error) {
	if composition == nil || composition.Gateway == nil || composition.Quotas == nil {
		return nil, errors.New("tenant quota composition unavailable")
	}
	capability, err := composition.Gateway.Authorize(ctx, surface, tenantsecurity.ActionQuotaReserve, tenantsecurity.ResourceQuota)
	if err != nil {
		return nil, err
	}
	return &tenantQuotaGate{service: composition.Quotas, capability: capability, policy: composition.Policy}, nil
}

// ReserveGeneration confirms one model-call reservation before provider I/O.
func (gate *tenantQuotaGate) ReserveGeneration(ctx context.Context, snapshot validationworkflow.AdmissionSnapshot) (validationworkflow.GenerationQuotaReceipt, error) {
	workflowID, ok := validationworkflow.GenerationQuotaIdentity(ctx)
	if !ok {
		return validationworkflow.GenerationQuotaReceipt{}, errors.New("generation quota identity unavailable")
	}
	id := tenantQuotaOperation("model", workflowID, snapshot.SpecificationDigest, snapshot.RevisionDigest, snapshot.TargetDigest)
	reservation, err := gate.service.Reserve(ctx, tenantsecurity.QuotaRequest{Capability: gate.capability, OperationID: id, Dimension: tenantsecurity.QuotaModelCall, Units: 1, Policy: gate.policy})
	return validationworkflow.GenerationQuotaReceipt{ID: reservation.ID, Digest: reservation.Digest}, err
}

// ResolveGeneration retains uncertain calls and releases a known terminal one.
func (gate *tenantQuotaGate) ResolveGeneration(ctx context.Context, receipt validationworkflow.GenerationQuotaReceipt, _ validationworkflow.ModelUsage, uncertain bool) error {
	operation := tenantQuotaOperation("model-resolve", receipt.ID, receipt.Digest)
	if uncertain {
		return gate.service.MarkUncertain(ctx, gate.capability, receipt.ID, operation)
	}
	return gate.service.Release(ctx, gate.capability, receipt.ID, operation)
}

// ReserveAcquire confirms environment capacity before provider I/O.
func (gate *tenantQuotaGate) ReserveAcquire(ctx context.Context, request environment.AcquisitionRequest) (environment.AcquireQuotaReceipt, error) {
	id := tenantQuotaOperation("environment", request.RunID, request.IdempotencyKey, request.Selection.Digest)
	reservation, err := gate.service.Reserve(ctx, tenantsecurity.QuotaRequest{Capability: gate.capability, OperationID: id, Dimension: tenantsecurity.QuotaEnvironment, Units: 1, Policy: gate.policy})
	return environment.AcquireQuotaReceipt{ID: reservation.ID, Digest: reservation.Digest}, err
}

// ResolveAcquire retains unknown provider effects while successful acquisition remains charged until cleanup.
func (gate *tenantQuotaGate) ResolveAcquire(ctx context.Context, receipt environment.AcquireQuotaReceipt, _ environment.Observation, uncertain bool) error {
	if uncertain {
		return gate.service.MarkUncertain(ctx, gate.capability, receipt.ID, tenantQuotaOperation("environment-uncertain", receipt.ID, receipt.Digest))
	}
	return nil
}

// ReleaseAcquire reconciles the durable acquisition reservation from the stable acquisition key after provider cleanup.
func (gate *tenantQuotaGate) ReleaseAcquire(ctx context.Context, request environment.ReleaseRequest, uncertain bool) error {
	reservationID := tenantQuotaOperation("environment", request.RunID, request.AcquisitionKey, request.Selection.Digest)
	operation := tenantQuotaOperation("environment-release", reservationID, request.LifecycleDigest)
	if uncertain {
		return gate.service.MarkUncertain(ctx, gate.capability, reservationID, operation)
	}
	return gate.service.Release(ctx, gate.capability, reservationID, operation)
}

// tenantQuotaOperation derives a bounded stable operation from sealed product digests.
func tenantQuotaOperation(kind string, values ...string) string {
	digest := sha256.Sum256([]byte(kind + "\x00" + strings.Join(values, "\x00")))
	return kind + "-" + hex.EncodeToString(digest[:20])
}

// quotaExternalExecutor reserves environment capacity before an external-agent
// executor may create or contact a preview lifecycle.
type quotaExternalExecutor struct {
	next       externalagent.ApprovedExecutor
	guard      *tenantsecurity.QuotaGuard
	capability tenantsecurity.Capability
	policy     tenantsecurity.QuotaPolicySnapshot
}

// tenantExternalIdentity revalidates all externally exposed run operations on
// every MCP/CLI call so a live membership or security-head change takes effect.
type tenantExternalIdentity struct {
	next     externalagent.IdentitySource
	security *tenantSecurityComposition
}

// Resolve reauthorizes only the exact application scopes sealed by the MCP
// boundary so a live membership or policy change takes effect per operation.
func (identity tenantExternalIdentity) Resolve(ctx context.Context) (externalagent.Principal, error) {
	principal, err := identity.next.Resolve(ctx)
	if err != nil {
		return externalagent.Principal{}, err
	}
	authenticated := ctx
	if !tenantsecurity.HasAuthenticatedSurface(ctx, tenantsecurity.SurfaceExternalAgent) {
		authenticated, err = identity.security.AuthenticateSurface(ctx, tenantsecurity.SurfaceExternalAgent, principal.ID)
		if err != nil {
			return externalagent.Principal{}, err
		}
	}
	actions := map[string]tenantsecurity.Action{
		externalagent.ScopeCreate:           tenantsecurity.ActionRunAdmit,
		externalagent.ScopeCorrectionCreate: tenantsecurity.ActionRunAdmit,
		externalagent.ScopeRead:             tenantsecurity.ActionRunRead,
		externalagent.ScopeCorrectionRead:   tenantsecurity.ActionRunRead,
		externalagent.ScopeCancel:           tenantsecurity.ActionRunCancel,
		externalagent.ScopeCorrectionCancel: tenantsecurity.ActionRunCancel,
	}
	for scope, action := range actions {
		if !principal.Scopes[scope] {
			continue
		}
		capability, authorizeErr := identity.security.Gateway.Authorize(authenticated, tenantsecurity.SurfaceExternalAgent, action, tenantsecurity.ResourceRun)
		if authorizeErr != nil {
			return externalagent.Principal{}, authorizeErr
		}
		if principal.ID != "" && tenantsecurity.HasAuthenticatedSurface(ctx, tenantsecurity.SurfaceExternalAgent) && principal.ID != capability.Scope.PrincipalID {
			return externalagent.Principal{}, errors.New("external principal attribution mismatch")
		}
		principal.ID = capability.Scope.PrincipalID
	}
	return principal, nil
}

// newQuotaExternalExecutor rejects productive external-agent composition when
// either executor or tenant quota authority is missing.
func newQuotaExternalExecutor(next externalagent.ApprovedExecutor, gate *tenantQuotaGate) (*quotaExternalExecutor, error) {
	if next == nil || gate == nil {
		return nil, errors.New("external quota composition unavailable")
	}
	guard, err := tenantsecurity.NewQuotaGuard(gate.service)
	if err != nil {
		return nil, err
	}
	return &quotaExternalExecutor{next: next, guard: guard, capability: gate.capability, policy: gate.policy}, nil
}

// Execute confirms quota before the real executor and retains uncertainty on
// any provider failure that cannot prove absence of an external effect.
func (executor *quotaExternalExecutor) Execute(ctx context.Context, selection externalagent.Selection, runID string) (externalagent.ExecutionResult, error) {
	effect := &externalQuotaEffect{next: executor.next, selection: selection, runID: runID}
	operation := tenantQuotaOperation("external-environment", selection.Alias, runID)
	_, err := executor.guard.Execute(ctx, tenantsecurity.QuotaRequest{Capability: executor.capability, OperationID: operation, Dimension: tenantsecurity.QuotaEnvironment, Units: 1, Policy: executor.policy}, effect)
	return effect.result, err
}

// ReconcileUnknown delegates to the provider-owned durable recovery boundary;
// absence of that boundary fails closed and retains quota capacity.
func (executor *quotaExternalExecutor) ReconcileUnknown(ctx context.Context, runID string) error {
	reconciler, ok := executor.next.(externalagent.UnknownEffectReconciler)
	if !ok {
		return errors.New("external effect reconciliation unavailable")
	}
	return reconciler.ReconcileUnknown(ctx, runID)
}

// externalQuotaEffect adapts one preview execution to the generic causal quota protocol.
type externalQuotaEffect struct {
	next      externalagent.ApprovedExecutor
	selection externalagent.Selection
	runID     string
	result    externalagent.ExecutionResult
}

// Execute invokes the preview consumer only after reservation commit.
func (effect *externalQuotaEffect) Execute(ctx context.Context, _ tenantsecurity.Reservation) (tenantsecurity.EffectStatus, error) {
	result, err := effect.next.Execute(ctx, effect.selection, effect.runID)
	effect.result = result
	if err != nil {
		return tenantsecurity.EffectUnknown, err
	}
	return tenantsecurity.EffectCommitted, nil
}

// quotaExternalRegistry meters object and storage capacity before the causal
// configured run+artifact commit.
type quotaExternalRegistry struct {
	next       externalagent.Registry
	configured externalagent.ConfiguredAdmissionRegistry
	guard      *tenantsecurity.QuotaGuard
	quotas     *tenantsecurity.DurableQuotaService
	capability tenantsecurity.Capability
	policy     tenantsecurity.QuotaPolicySnapshot
	recovery   externalagent.RecoveryReportingRegistry
	reconciler externalagent.UnknownEffectReconciler
}

// newQuotaExternalRegistry requires the configured atomic admission port so
// quota cannot guard a weaker non-causal fallback.
func newQuotaExternalRegistry(next externalagent.Registry, gate *tenantQuotaGate) (*quotaExternalRegistry, error) {
	configured, ok := next.(externalagent.ConfiguredAdmissionRegistry)
	recovery, recoveryOK := next.(externalagent.RecoveryReportingRegistry)
	if !ok || !recoveryOK || gate == nil {
		return nil, errors.New("external storage quota composition unavailable")
	}
	guard, err := tenantsecurity.NewQuotaGuard(gate.service)
	if err != nil {
		return nil, err
	}
	return &quotaExternalRegistry{next: next, configured: configured, guard: guard, quotas: gate.service, capability: gate.capability, policy: gate.policy, recovery: recovery}, nil
}

// ClaimSubmit delegates the pre-admission idempotency lease unchanged.
func (registry *quotaExternalRegistry) ClaimSubmit(ctx context.Context, principal, key, hash string) (*externalagent.SubmissionClaim, error) {
	return registry.configured.ClaimSubmit(ctx, principal, key, hash)
}

// ReserveConfigured reserves bytes and object count before the atomic run/artifact commit.
func (registry *quotaExternalRegistry) ReserveConfigured(ctx context.Context, record externalagent.RunRecord, snapshot externalagent.ProjectResourceSnapshot) (externalagent.ReserveResult, error) {
	effect := &externalRegistryEffect{next: registry.configured, record: record, snapshot: snapshot}
	objectRequest := tenantsecurity.QuotaRequest{Capability: registry.capability, OperationID: tenantQuotaOperation("external-object", record.InternalID), Dimension: tenantsecurity.QuotaObject, Units: 1, Policy: registry.policy}
	nested := &externalRegistryNestedEffect{guard: registry.guard, request: objectRequest, next: effect}
	bytes := uint64(len(snapshot.Payload))
	if bytes == 0 {
		bytes = 1
	}
	storageRequest := tenantsecurity.QuotaRequest{Capability: registry.capability, OperationID: tenantQuotaOperation("external-storage", record.InternalID), Dimension: tenantsecurity.QuotaStorageByte, Units: bytes, Policy: registry.policy}
	_, err := registry.guard.Execute(ctx, storageRequest, nested)
	return effect.result, err
}

// Reserve delegates legacy admission; productive configured composition never selects it.
func (registry *quotaExternalRegistry) Reserve(ctx context.Context, record externalagent.RunRecord) (externalagent.ReserveResult, error) {
	return registry.next.Reserve(ctx, record)
}

// Get reloads one already admitted run without creating capacity.
func (registry *quotaExternalRegistry) Get(ctx context.Context, handle string) (externalagent.RunRecord, error) {
	return registry.next.Get(ctx, handle)
}

// Transition delegates a fenced lifecycle mutation for already reserved work.
func (registry *quotaExternalRegistry) Transition(ctx context.Context, handle string, from, to externalagent.Phase, outcome externalagent.Outcome, owner, digest string, at time.Time) (externalagent.RunRecord, error) {
	record, err := registry.next.Transition(ctx, handle, from, to, outcome, owner, digest, at)
	if err != nil && to == externalagent.PhaseTerminal {
		observed, observeErr := registry.next.Get(ctx, handle)
		if observeErr == nil && observed.Phase == externalagent.PhaseTerminal {
			record, err = observed, nil
		}
	}
	if err == nil && record.Phase == externalagent.PhaseTerminal {
		err = registry.releaseEnvironment(ctx, record)
	}
	return record, err
}

// ReserveCancel delegates cancellation without allocating new capacity.
func (registry *quotaExternalRegistry) ReserveCancel(ctx context.Context, handle, key, hash string) (externalagent.RunRecord, bool, error) {
	return registry.next.ReserveCancel(ctx, handle, key, hash)
}

// Recover delegates historical convergence without a new external effect.
func (registry *quotaExternalRegistry) Recover(ctx context.Context, owner string, now time.Time) error {
	records, err := registry.recovery.RecoverTerminal(ctx, owner, now)
	if err != nil {
		return err
	}
	for _, record := range records {
		if err = registry.releaseEnvironment(ctx, record); err != nil {
			return err
		}
	}
	return nil
}

// releaseEnvironment resolves one deterministic execution reservation after a
// terminal record is durably observable; replay cannot decrement twice.
func (registry *quotaExternalRegistry) releaseEnvironment(ctx context.Context, record externalagent.RunRecord) error {
	if record.InternalID == "" || record.SelectionAlias == "" || record.Phase != externalagent.PhaseTerminal {
		return errors.New("external environment terminal identity unavailable")
	}
	reservationID := tenantQuotaOperation("external-environment", record.SelectionAlias, record.InternalID)
	reservation, err := registry.quotas.ReservationState(ctx, registry.capability, reservationID)
	if err != nil {
		return err
	}
	if reservation.Released {
		return nil
	}
	if reservation.Uncertain {
		if registry.reconciler == nil {
			return errors.New("external effect reconciliation unavailable")
		}
		if err = registry.reconciler.ReconcileUnknown(ctx, record.InternalID); err != nil {
			return err
		}
	}
	return registry.quotas.Release(ctx, registry.capability, reservationID, tenantQuotaOperation("external-environment-release", reservationID))
}

// Events delegates immutable journal reads.
func (registry *quotaExternalRegistry) Events(ctx context.Context, handle string) ([]externalagent.RunEvent, error) {
	return registry.next.Events(ctx, handle)
}

// MarkPublicationFailure delegates terminal safe failure publication.
func (registry *quotaExternalRegistry) MarkPublicationFailure(ctx context.Context, handle string, now time.Time) error {
	return registry.next.MarkPublicationFailure(ctx, handle, now)
}

// ReleaseOwner delegates process-local owner cleanup.
func (registry *quotaExternalRegistry) ReleaseOwner(handle string) {
	registry.next.ReleaseOwner(handle)
}

// externalRegistryEffect adapts the atomic configured commit to quota protocol.
type externalRegistryEffect struct {
	next     externalagent.ConfiguredAdmissionRegistry
	record   externalagent.RunRecord
	snapshot externalagent.ProjectResourceSnapshot
	result   externalagent.ReserveResult
}

// Execute performs the real atomic run/artifact commit after both reservations.
func (effect *externalRegistryEffect) Execute(ctx context.Context, _ tenantsecurity.Reservation) (tenantsecurity.EffectStatus, error) {
	result, err := effect.next.ReserveConfigured(ctx, effect.record, effect.snapshot)
	effect.result = result
	if err != nil {
		return tenantsecurity.EffectUnknown, err
	}
	return tenantsecurity.EffectCommitted, nil
}

// externalRegistryNestedEffect reserves object count before the storage effect.
type externalRegistryNestedEffect struct {
	guard   *tenantsecurity.QuotaGuard
	request tenantsecurity.QuotaRequest
	next    *externalRegistryEffect
}

// Execute applies the inner reservation and propagates conservative uncertainty.
func (effect *externalRegistryNestedEffect) Execute(ctx context.Context, _ tenantsecurity.Reservation) (tenantsecurity.EffectStatus, error) {
	_, err := effect.guard.Execute(ctx, effect.request, effect.next)
	if err != nil {
		return tenantsecurity.EffectUnknown, err
	}
	return tenantsecurity.EffectCommitted, nil
}

var _ externalagent.ConfiguredAdmissionRegistry = (*quotaExternalRegistry)(nil)
