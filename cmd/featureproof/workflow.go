package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/assertion"
	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/externalagent"
	"github.com/asierhr/featureProof/internal/githubpr"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/model"
	"github.com/asierhr/featureProof/internal/model/openai"
	"github.com/asierhr/featureProof/internal/model/openai/oauth"
	openapipkg "github.com/asierhr/featureProof/internal/openapi"
	"github.com/asierhr/featureProof/internal/planapproval"
	"github.com/asierhr/featureProof/internal/planmanifest"
	"github.com/asierhr/featureProof/internal/previewenv"
	"github.com/asierhr/featureProof/internal/revisionbinding"
	"github.com/asierhr/featureProof/internal/specification"
	specyaml "github.com/asierhr/featureProof/internal/specification/yaml"
	"github.com/asierhr/featureProof/internal/validationworkflow"
)

// workflowConfig is the trusted local composition record; it contains references but no credential bytes.
type workflowConfig struct {
	Provider              string    `json:"provider"`
	Model                 string    `json:"model"`
	AuthProfile           string    `json:"auth_profile"`
	ProfileStore          string    `json:"profile_store"`
	RuntimeProfile        string    `json:"runtime_profile"`
	SpecificationPath     string    `json:"specification_path"`
	ContractPath          string    `json:"contract_path"`
	RuntimeManifestPath   string    `json:"runtime_manifest_path"`
	RepositoryID          string    `json:"repository_id"`
	CommitOID             string    `json:"commit_oid"`
	EnvironmentID         string    `json:"environment_id"`
	AttestationRef        string    `json:"attestation_ref"`
	TargetAlias           string    `json:"target_alias"`
	TargetURL             string    `json:"target_url"`
	TargetKind            string    `json:"target_kind"`
	PolicyID              string    `json:"policy_id"`
	AttestationValidUntil time.Time `json:"attestation_valid_until"`
	AllowDisabledApproval bool      `json:"allow_disabled_approval"`
}

// loadWorkflowConfig strictly decodes one bounded trusted configuration selected outside workflow input.
func loadWorkflowConfig() (workflowConfig, error) {
	path := os.Getenv("FEATUREPROOF_WORKFLOW_CONFIG")
	if path == "" {
		return workflowConfig{}, errors.New("workflow configuration unavailable")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 || len(raw) > 1<<20 {
		return workflowConfig{}, errors.New("invalid workflow configuration")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var config workflowConfig
	if decoder.Decode(&config) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return workflowConfig{}, errors.New("invalid workflow configuration")
	}
	if config.Provider != "openai" || config.Model == "" || config.AuthProfile == "" || config.ProfileStore == "" || config.RuntimeProfile == "" || config.SpecificationPath == "" || config.ContractPath == "" || config.RuntimeManifestPath == "" || !revisionbinding.ValidID(config.RepositoryID) || !revisionbinding.ValidOID(config.CommitOID) || !revisionbinding.ValidID(config.EnvironmentID) || !revisionbinding.ValidID(config.AttestationRef) || config.TargetAlias == "" || config.TargetURL == "" || config.PolicyID == "" || !config.AttestationValidUntil.After(time.Now()) || (config.TargetKind != "direct" && config.TargetKind != "preview") {
		return workflowConfig{}, errors.New("invalid workflow configuration")
	}
	return config, nil
}

// composeWorkflowCLI resolves OAuth and filesystem references only in the executable composition root.
func composeWorkflowCLI(ctx context.Context, stdout, stderr io.Writer) (*validationworkflow.CLI, error) {
	config, err := loadWorkflowConfig()
	if err != nil {
		return nil, err
	}
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	var previewPort validationworkflow.PreviewPort
	var workflowPreview *workflowPreviewPort
	if config.TargetKind == "preview" {
		configured, present, previewErr := composeConfiguredPreview(ctx, root)
		if previewErr != nil || !present || configured.Request.Selection.Alias != config.TargetAlias || configured.Config.RepositoryID != config.RepositoryID || configured.Config.CommitOID != config.CommitOID || configured.Config.EnvironmentID != config.EnvironmentID || configured.Config.Target != config.TargetURL || configured.Config.AttestationRef != config.AttestationRef || !configured.Config.AttestationValidUntil.Equal(config.AttestationValidUntil) {
			return nil, errors.New("preview workflow configuration unavailable")
		}
		workflowPreview = &workflowPreviewPort{configured: configured, target: config.TargetURL, attestors: map[string]revisionbinding.EnvironmentAttestor{}}
		previewPort = workflowPreview
	}
	current, err := user.Current()
	if err != nil || current.Uid == "" {
		return nil, errors.New("local identity unavailable")
	}
	spec, err := specyaml.Load(ctx, config.SpecificationPath)
	if err != nil {
		return nil, err
	}
	manifest, err := planmanifest.Load(ctx, config.RuntimeManifestPath, spec)
	if err != nil {
		return nil, err
	}
	redaction, err := evidence.NewPolicy(manifest.Redaction)
	if err != nil {
		return nil, err
	}
	client := openai.NewHTTPClient(http.DefaultTransport)
	gateway, err := oauth.NewHTTPGateway(client)
	if err != nil {
		return nil, err
	}
	validator, err := oauth.NewJWKSValidator(client, oauth.WallClock{})
	if err != nil {
		return nil, err
	}
	profileStore, err := oauth.NewLocalStore(config.ProfileStore)
	if err != nil {
		return nil, err
	}
	resolver, err := oauth.NewResolver(gateway, oauth.WallClock{}, validator, profileStore)
	if err != nil {
		return nil, err
	}
	credentials, err := oauth.NewProfileSource(resolver, config.AuthProfile)
	if err != nil {
		return nil, err
	}
	transport, err := openai.NewAuthenticatedTransport(credentials, client)
	if err != nil {
		return nil, err
	}
	adapter, err := openai.New(transport, openai.Config{MaxRequestBytes: 1 << 20, MaxHeaderBytes: 64 << 10, MaxStreamBytes: 2 << 20, MaxOutputBytes: 1 << 20})
	if err != nil {
		return nil, err
	}
	session, err := model.NewSession(model.Config{ProviderID: config.Provider, ModelID: config.Model, AuthProfileID: config.AuthProfile, CallTimeout: 2 * time.Minute, MaxRequestBytes: 1 << 20, MaxStreamBytes: 2 << 20, MaxOutputBytes: 1 << 20, Budget: model.BudgetLimits{MaxCalls: 1, MaxTokens: 64000, MaxDuration: 3 * time.Minute, MaxReplans: 0}}, adapter, evalClock{}, evalObserver{})
	if err != nil {
		return nil, err
	}
	assertions := assertion.DefinitionValidator{Policy: redaction}
	runtimeDeps := localvalidation.DefaultDependencies(root, stdout, stderr)
	capability, err := localvalidation.BuildHTTPPlanningCapability(spec, manifest, config.TargetURL, runtimeDeps)
	if err != nil {
		return nil, err
	}
	generationPolicy := workflowGenerationPolicy{factory: runtimeDeps.PolicyFactory}
	generator, err := agentplanning.NewGenerator(agentplanning.SessionModel{Session: session}, assertions, generationPolicy, workflowGenerationObserver{}, workflowClock{})
	if err != nil {
		return nil, err
	}
	network, err := workflowNetworkContext(config, workflowPreview)
	if err != nil {
		return nil, err
	}
	targetContext := validationworkflow.TargetContext{Kind: config.TargetKind, Alias: config.TargetAlias, URL: config.TargetURL, EnvironmentID: config.EnvironmentID, Attestation: validationworkflow.AttestationContext{EnvironmentID: config.EnvironmentID, RepositoryID: config.RepositoryID, CommitOID: config.CommitOID, Reference: config.AttestationRef, ValidUntil: config.AttestationValidUntil, Verified: true}, Network: network}
	if workflowPreview != nil {
		targetContext.ProviderID, targetContext.EnvironmentRef, targetContext.ProfileID = workflowPreview.configured.Config.ProviderID, workflowPreview.configured.Config.EnvironmentRef, workflowPreview.configured.Config.ProfileID
	}
	bindingContext := validationworkflow.BindingContext{Revision: validationworkflow.RevisionContext{RepositoryID: config.RepositoryID, CommitOID: config.CommitOID}, Target: targetContext}
	runtimeContext := workflowRuntimeContext(config, manifest, network)
	targetDigest, err := validationworkflow.TargetContextDigest(targetContext)
	if err != nil {
		return nil, err
	}
	runtimeDigest, err := validationworkflow.RuntimeContextDigest(runtimeContext)
	if err != nil {
		return nil, err
	}
	compositionRef := workflowDigest("composition", config.Provider, config.Model, config.AuthProfile)
	admission := validationworkflow.TrustedAdmissionBuilder{Specifications: workflowSpecLoader{path: config.SpecificationPath}, Contracts: workflowContractReducer{path: config.ContractPath}, Bindings: workflowBindingVerifier{config: config, binding: bindingContext}, Catalog: workflowCatalog{profile: config.RuntimeProfile, runtime: runtimeContext, descriptors: []agentplanning.CapabilityDescriptor{capability}}, Config: validationworkflow.AdmissionConfig{Provider: config.Provider, Model: config.Model, CompositionRef: compositionRef, Limits: agentplanning.Limits{MaxInputBytes: 1 << 20, MaxOutputBytes: 1 << 20, MaxConstraints: 128, MaxCapabilities: 16, MaxChecks: 384, MaxStepsPerCheck: 64, MaxAssertionsPerStep: 64, MaxMissingItems: 32, MaxValidationWork: 100000}}}
	storeRoot := filepath.Join(root, ".featureproof", "workflows")
	store, err := validationworkflow.OpenLocalStore(storeRoot)
	if err != nil {
		return nil, err
	}
	artifacts, err := validationworkflow.OpenLocalArtifactStore(filepath.Join(storeRoot, "plans"))
	if err != nil {
		return nil, err
	}
	executables, err := validationworkflow.OpenLocalExecutableStore(filepath.Join(storeRoot, "private"))
	if err != nil {
		return nil, err
	}
	approvalStore, err := planapproval.NewLocalStore(filepath.Join(storeRoot, "approval"))
	if err != nil {
		return nil, err
	}
	approval := &validationworkflow.PlanApprovalBridge{Store: approvalStore, Sanitizer: workflowApprovalSanitizer{inner: evidence.NewSanitizer(redaction)}, Clock: githubpr.WallClock{}, Limits: planapproval.Limits{MaxBytes: 1 << 20, MaxDepth: 64, MaxEvents: 10000, MaxConstraints: 128, MaxChecks: 384, MaxSteps: 24576, MaxAssertions: 24576, MaxEvidence: 24576}}
	deps := runtimeDeps
	revision := workflowRevision{repository: config.RepositoryID, commit: config.CommitOID}
	attestation := workflowAttestation{config: config}
	deps.RevisionResolver, deps.EnvironmentAttestor, deps.SafeProjector = revision, attestation, previewenv.SafeProjector{PolicyID: config.PolicyID}
	if workflowPreview != nil {
		deps.EnvironmentAttestor = workflowPreview
	}
	execution := validationworkflow.LocalExecutionPort{Configurations: map[string]validationworkflow.LocalRunnerConfiguration{runtimeDigest: {Digest: runtimeDigest, Request: localvalidation.PreparedRequest{Specification: spec, Runtime: manifest, Target: config.TargetURL, RepositoryID: config.RepositoryID, Revision: config.CommitOID, EnvironmentID: config.EnvironmentID}, Dependencies: deps}}}
	owner, err := (externalagent.RandomIDs{}).Next()
	if err != nil {
		return nil, err
	}
	wallClock := githubpr.WallClock{}
	service, err := validationworkflow.NewService(validationworkflow.Config{LeaseTTL: 10 * time.Minute, HeartbeatInterval: time.Minute, FinalizeTimeout: 10 * time.Second, ApprovalTTL: 24 * time.Hour}, validationworkflow.Dependencies{Admission: admission, Generation: validationworkflow.GeneratorAdapter{Generator: generator}, Preparation: validationworkflow.PreparationAdapter{Assertions: assertions, Policy: generationPolicy}, Approval: approval, Execution: execution, Packages: validationworkflow.LocalPackageReader{Artifacts: artifacts}, Authorization: workflowAutoPolicy{allowed: config.AllowDisabledApproval}, Finalizer: workflowFinalizer{}, Preview: previewPort, Store: store, Artifacts: artifacts, Executables: executables, Clock: wallClock, Waiter: wallClock, IDs: externalagent.RandomIDs{}, Owner: owner})
	if err != nil {
		return nil, err
	}
	if err = service.Recover(ctx); err != nil {
		return nil, err
	}
	cli := &validationworkflow.CLI{Service: service, Principal: current.Uid, AuthProfiles: map[string]string{config.AuthProfile: compositionRef}, DirectTargets: map[string]validationworkflow.DirectTarget{}, PreviewTargets: map[string]validationworkflow.PreviewTarget{}}
	if config.TargetKind == "direct" {
		cli.DirectTargets[config.TargetAlias] = validationworkflow.DirectTarget{Alias: config.TargetAlias, Digest: targetDigest}
	} else {
		cli.PreviewTargets[config.TargetAlias] = validationworkflow.PreviewTarget{Alias: config.TargetAlias, Digest: targetDigest}
	}
	return cli, nil
}

// workflowSpecLoader authorizes exactly one configured specification path.
type workflowSpecLoader struct{ path string }

// Load rejects transport-selected paths outside trusted composition.
func (l workflowSpecLoader) Load(ctx context.Context, ref string) (specification.Specification, error) {
	if ref != l.path {
		return specification.Specification{}, errors.New("unauthorized specification")
	}
	return specyaml.Load(ctx, l.path)
}

// workflowContractReducer authorizes one contract and retains every operation as untrusted planning data.
type workflowContractReducer struct{ path string }

// Reduce builds a bounded per-constraint operation context from the configured contract.
func (r workflowContractReducer) Reduce(ctx context.Context, ref string, spec specification.Specification) (openapipkg.ReducedContext, error) {
	if ref != r.path {
		return openapipkg.ReducedContext{}, errors.New("unauthorized contract")
	}
	root := filepath.Dir(r.path)
	loader, err := openapipkg.NewLoader(ctx, openapipkg.Config{AuthorizedRoot: root, MaxDocumentBytes: 2 << 20, MaxTotalBytes: 4 << 20, MaxDocuments: 32, MaxReferences: 512, MaxReferenceDepth: 16, MaxObservedBodyBytes: 1 << 20, MaxContextBytes: 1 << 20}, openapipkg.LocalFS{})
	if err != nil {
		return openapipkg.ReducedContext{}, err
	}
	contract, err := loader.Load(ctx, filepath.Base(r.path))
	if err != nil {
		return openapipkg.ReducedContext{}, err
	}
	catalog := contract.Catalog()
	selections := make([]openapipkg.ConstraintSelection, len(spec.Constraints))
	for i, constraint := range spec.Constraints {
		selections[i].ConstraintID = constraint.ID
		for _, operation := range catalog.Operations {
			selections[i].Operations = append(selections[i].Operations, operation.Key)
		}
	}
	return contract.BuildContext(ctx, openapipkg.ContextRequest{Constraints: selections})
}

// workflowBindingVerifier checks exact configured source and deployment identity without exposing credentials.
type workflowBindingVerifier struct {
	config  workflowConfig
	binding validationworkflow.BindingContext
}

// Verify rejects any source, revision or target drift and returns the effective safe objects.
func (v workflowBindingVerifier) Verify(_ context.Context, command validationworkflow.StartCommand) (validationworkflow.BindingContext, error) {
	if command.RepositoryID != v.config.RepositoryID || command.Revision != v.config.CommitOID {
		return validationworkflow.BindingContext{}, errors.New("revision mismatch")
	}
	return v.binding, nil
}

// workflowCatalog exposes only the registered HTTP descriptor and complete safe runtime context.
type workflowCatalog struct {
	profile     string
	runtime     validationworkflow.RuntimeContext
	descriptors []agentplanning.CapabilityDescriptor
}

// Descriptors rejects unknown runtime profiles and returns no executable handle or credential.
func (c workflowCatalog) Descriptors(_ context.Context, profile string, _ validationworkflow.TargetSelection) ([]agentplanning.CapabilityDescriptor, validationworkflow.RuntimeContext, error) {
	if profile != c.profile {
		return nil, validationworkflow.RuntimeContext{}, errors.New("runtime unavailable")
	}
	return append([]agentplanning.CapabilityDescriptor(nil), c.descriptors...), c.runtime, nil
}

// workflowGenerationPolicy allows only the descriptor already admitted by composition.
type workflowGenerationPolicy struct{ factory func() harness.Policy }

// Evaluate authorizes the single bounded HTTP capability.
func (p workflowGenerationPolicy) Evaluate(ctx context.Context, intent agentplanning.PlanIntent) (agentplanning.PolicyDecision, error) {
	if p.factory == nil {
		return agentplanning.PolicyDecision{}, errors.New("runtime policy unavailable")
	}
	policy := p.factory()
	if policy == nil {
		return agentplanning.PolicyDecision{}, errors.New("runtime policy unavailable")
	}
	run, err := harness.NewRun(intent.RunID, "generated validation", intent.ConstraintID, harness.Limits{MaxSteps: 1, MaxToolCalls: 1, MaxRetries: 0})
	if err != nil {
		return agentplanning.PolicyDecision{}, err
	}
	decision, err := policy.Evaluate(ctx, run, harness.ToolCall{ID: intent.CheckID, Tool: intent.Capability, ConstraintID: intent.ConstraintID, IdempotencyKey: intent.PlanID})
	return agentplanning.PolicyDecision{Allowed: decision.Allowed, Reason: decision.Reason}, err
}

// workflowGenerationObserver confirms safe planning telemetry without payload persistence.
type workflowGenerationObserver struct{}

// Observe accepts payload-free terminal records.
func (workflowGenerationObserver) Observe(context.Context, agentplanning.GenerationRecord) error {
	return nil
}

// workflowClock supplies live UTC time to planning.
type workflowClock struct{}

// Now returns current UTC time.
func (workflowClock) Now() time.Time { return time.Now().UTC() }

// workflowAutoPolicy controls the explicit approval-disabled mode.
type workflowAutoPolicy struct{ allowed bool }

// workflowApprovalSanitizer adapts the evidence sanitizer's metadata-bearing result to planapproval.
type workflowApprovalSanitizer struct{ inner evidence.Sanitizer }

// Sanitize returns only the treated value while keeping truncation metadata private to composition.
func (s workflowApprovalSanitizer) Sanitize(ctx context.Context, value any) (any, error) {
	treated, err := s.inner.Sanitize(ctx, value)
	if err != nil {
		return nil, err
	}
	return treated.Value(), nil
}

// Authorize rejects approval-disabled execution unless trusted config opted in.
func (p workflowAutoPolicy) Authorize(context.Context, validationworkflow.StartCommand, validationworkflow.PlanArtifact) error {
	if !p.allowed {
		return errors.New("approval required")
	}
	return nil
}

// workflowFinalizer owns no resources for a static configured target.
type workflowFinalizer struct{}

// workflowPreviewPort connects workflow preview selections to the existing durable preview coordinator.
type workflowPreviewPort struct {
	configured *configuredPreview
	target     string
	mu         sync.Mutex
	attestors  map[string]revisionbinding.EnvironmentAttestor
}

// Acquire reserves and verifies the configured preview and returns its complete safe target object.
func (p *workflowPreviewPort) Acquire(ctx context.Context, handle string, command validationworkflow.StartCommand) (validationworkflow.PreviewBinding, error) {
	if p.configured == nil || command.Target.Preview == nil || command.Target.Preview.Alias != p.configured.Request.Selection.Alias {
		return validationworkflow.PreviewBinding{}, errors.New("preview selection unavailable")
	}
	authorized, err := p.configured.Coordinator.Acquire(ctx, handle, p.configured.Request.Selection, p.configured.Request.Expected)
	if err != nil || authorized.Target.BaseURL != p.target {
		return validationworkflow.PreviewBinding{}, errors.New("preview target drift")
	}
	p.mu.Lock()
	p.attestors[authorized.Snapshot.EnvironmentID] = authorized.Attestor
	p.mu.Unlock()
	return p.previewBinding(authorized), nil
}

// Restore reconstructs the acquired preview attestor from the coordinator's sealed durable snapshot.
func (p *workflowPreviewPort) Restore(ctx context.Context, handle string, command validationworkflow.StartCommand) (validationworkflow.PreviewBinding, error) {
	if p.configured == nil || command.Target.Preview == nil || command.Target.Preview.Alias != p.configured.Request.Selection.Alias {
		return validationworkflow.PreviewBinding{}, errors.New("preview selection unavailable")
	}
	authorized, err := p.configured.Coordinator.RestoreAcquired(ctx, handle, p.configured.Request.Selection, p.configured.Request.Expected)
	if err != nil || authorized.Target.BaseURL != p.target {
		return validationworkflow.PreviewBinding{}, errors.New("preview target drift")
	}
	p.mu.Lock()
	p.attestors[authorized.Snapshot.EnvironmentID] = authorized.Attestor
	p.mu.Unlock()
	return p.previewBinding(authorized), nil
}

// previewBinding projects the sealed provider observation and effective network rules without credentials.
func (p *workflowPreviewPort) previewBinding(authorized previewenv.AuthorizedExecution) validationworkflow.PreviewBinding {
	configured := p.configured.Config
	snapshot := authorized.Snapshot
	return validationworkflow.PreviewBinding{Target: validationworkflow.TargetContext{Kind: "preview", Alias: configured.Alias, URL: authorized.Target.BaseURL, ProviderID: configured.ProviderID, EnvironmentRef: configured.EnvironmentRef, ProfileID: configured.ProfileID, EnvironmentID: snapshot.EnvironmentID, ReservationID: snapshot.ReservationID, ObservedVersion: snapshot.ObservedVersion, Fingerprint: snapshot.Fingerprint, Attestation: validationworkflow.AttestationContext{EnvironmentID: snapshot.EnvironmentID, RepositoryID: snapshot.RepositoryID, CommitOID: snapshot.CommitOID, Reference: snapshot.AttestationRef, ValidUntil: snapshot.AttestationValidUntil, Verified: true}, Network: validationworkflow.NetworkContext{PolicyID: configured.NetworkPolicyID, Schemes: []string{configured.Scheme}, Hosts: []string{configured.Host}, Ports: []int{configured.Port}, BasePaths: []string{configured.BasePath}, MaxURLBytes: 2048}}}
}

// Release confirms logical release through the same preview coordinator identity.
func (p *workflowPreviewPort) Release(ctx context.Context, handle string) error {
	return p.configured.Coordinator.ReleaseAcquired(ctx, handle)
}

// Attest refreshes the acquired preview snapshot before binding and between deterministic batches.
func (p *workflowPreviewPort) Attest(ctx context.Context, environment string) (revisionbinding.Attestation, error) {
	p.mu.Lock()
	attestor := p.attestors[environment]
	p.mu.Unlock()
	if attestor == nil {
		return revisionbinding.Attestation{}, errors.New("preview attestation unavailable")
	}
	return attestor.Attest(ctx, environment)
}

// Release is idempotent because static target composition reserves nothing.
func (workflowFinalizer) Release(context.Context, string) error { return nil }

// workflowRevision returns the configured immutable source identity.
type workflowRevision struct{ repository, commit string }

// Resolve rejects any repository or commit drift.
func (r workflowRevision) Resolve(_ context.Context, repository, ref string) (revisionbinding.Revision, error) {
	if repository != r.repository || ref != r.commit {
		return revisionbinding.Revision{}, errors.New("revision mismatch")
	}
	return revisionbinding.Revision{RepositoryID: repository, CommitOID: ref}, nil
}

// workflowAttestation returns the configured deployment proof.
type workflowAttestation struct{ config workflowConfig }

// Attest rejects any environment drift.
func (a workflowAttestation) Attest(_ context.Context, environment string) (revisionbinding.Attestation, error) {
	if environment != a.config.EnvironmentID {
		return revisionbinding.Attestation{}, errors.New("environment mismatch")
	}
	return revisionbinding.Attestation{EnvironmentID: environment, RepositoryID: a.config.RepositoryID, CommitOID: a.config.CommitOID, Reference: a.config.AttestationRef, ValidUntil: a.config.AttestationValidUntil, Verified: true}, nil
}

// workflowNetworkContext derives the exact effective allowlist used by direct or preview execution.
func workflowNetworkContext(config workflowConfig, preview *workflowPreviewPort) (validationworkflow.NetworkContext, error) {
	if preview != nil {
		candidate := preview.configured.Config
		return validationworkflow.NetworkContext{PolicyID: candidate.NetworkPolicyID, Schemes: []string{candidate.Scheme}, Hosts: []string{candidate.Host}, Ports: []int{candidate.Port}, BasePaths: []string{candidate.BasePath}, MaxURLBytes: 2048}, nil
	}
	parsed, err := url.Parse(config.TargetURL)
	if err != nil || !parsed.IsAbs() || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return validationworkflow.NetworkContext{}, errors.New("invalid target network configuration")
	}
	port := 80
	if parsed.Scheme == "https" {
		port = 443
	}
	if parsed.Port() != "" {
		port, err = strconv.Atoi(parsed.Port())
		if err != nil {
			return validationworkflow.NetworkContext{}, errors.New("invalid target network configuration")
		}
	}
	basePath := parsed.EscapedPath()
	if basePath == "" {
		basePath = "/"
	}
	return validationworkflow.NetworkContext{PolicyID: config.PolicyID, Schemes: []string{parsed.Scheme}, Hosts: []string{parsed.Hostname()}, Ports: []int{port}, BasePaths: []string{basePath}, MaxURLBytes: 2048}, nil
}

// workflowRuntimeContext projects the exact registry, schema, validator, manifest, policy and network selected for execution.
func workflowRuntimeContext(config workflowConfig, manifest planmanifest.Manifest, network validationworkflow.NetworkContext) validationworkflow.RuntimeContext {
	return validationworkflow.RuntimeContext{Profile: config.RuntimeProfile, ManifestVersion: manifest.Version, HTTP: manifest.HTTP, Redaction: manifest.Redaction, Registry: validationworkflow.RegistryContext{Version: "featureproof.registry/v1", Tools: []string{"http.request"}}, Schema: validationworkflow.SchemaContext{Version: "featureproof.http-input/v1", ReferenceVersion: "featureproof.output-reference/v1", Fields: []string{"method", "path", "headers", "body"}}, Validator: validationworkflow.ValidatorContext{Tool: "http.request", ToolVersion: "featureproof.http/v1", AssertionEngine: "definition", AssertionVersion: "featureproof.assertion/v1"}, Policy: validationworkflow.PolicyContext{ID: config.PolicyID, AllowedTools: []string{"http.request"}, Reason: "manifest_allowlist"}, Network: network}
}

// workflowDigest hashes safe non-secret configuration fields.
func workflowDigest(values ...string) string {
	raw, _ := json.Marshal(values)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
