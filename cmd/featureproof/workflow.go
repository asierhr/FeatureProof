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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/assertion"
	"github.com/asierhr/featureProof/internal/contextassembly"
	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/contractreview"
	"github.com/asierhr/featureProof/internal/correction"
	"github.com/asierhr/featureProof/internal/environment"
	environmentcleanup "github.com/asierhr/featureProof/internal/environment/cleanup"
	kubernetesenvironment "github.com/asierhr/featureProof/internal/environment/kubernetes"
	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/externalagent"
	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/functionalflow"
	"github.com/asierhr/featureProof/internal/githubcontext"
	"github.com/asierhr/featureProof/internal/githubpr"
	"github.com/asierhr/featureProof/internal/goinspector"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/model"
	"github.com/asierhr/featureProof/internal/model/openai"
	"github.com/asierhr/featureProof/internal/model/openai/oauth"
	openapipkg "github.com/asierhr/featureProof/internal/openapi"
	"github.com/asierhr/featureProof/internal/planapproval"
	"github.com/asierhr/featureProof/internal/planmanifest"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/previewenv"
	"github.com/asierhr/featureProof/internal/projectmemory"
	"github.com/asierhr/featureProof/internal/revisionbinding"
	"github.com/asierhr/featureProof/internal/specification"
	specyaml "github.com/asierhr/featureProof/internal/specification/yaml"
	"github.com/asierhr/featureProof/internal/springinspector"
	"github.com/asierhr/featureProof/internal/testidentity"
	"github.com/asierhr/featureProof/internal/validationworkflow"
	kubernetesclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// workflowProvisionalPlanningTarget is a non-routable validation placeholder replaced by acquired authority before model generation.
const workflowProvisionalPlanningTarget = "https://composition.invalid/"

// workflowConfig is the trusted local composition record; it contains references but no credential bytes.
type workflowConfig struct {
	Provider                      string                                  `json:"provider"`
	Model                         string                                  `json:"model"`
	AuthProfile                   string                                  `json:"auth_profile"`
	ProfileStore                  string                                  `json:"profile_store"`
	RuntimeProfile                string                                  `json:"runtime_profile"`
	SpecificationPath             string                                  `json:"specification_path"`
	ContractPath                  string                                  `json:"contract_path"`
	ContractSource                string                                  `json:"contract_source,omitempty"`
	ContractSources               []string                                `json:"contract_sources,omitempty"`
	OpenAPIContractPath           string                                  `json:"openapi_contract_path,omitempty"`
	RepositoryRoot                string                                  `json:"repository_root,omitempty"`
	GoInspector                   *workflowGoInspectorConfig              `json:"go_inspector,omitempty"`
	SpringInspector               *workflowSpringInspectorConfig          `json:"spring_inspector,omitempty"`
	RuntimeManifestPath           string                                  `json:"runtime_manifest_path"`
	RepositoryID                  string                                  `json:"repository_id"`
	CommitOID                     string                                  `json:"commit_oid"`
	EnvironmentID                 string                                  `json:"environment_id"`
	AttestationRef                string                                  `json:"attestation_ref"`
	TargetAlias                   string                                  `json:"target_alias"`
	TargetURL                     string                                  `json:"target_url"`
	TargetKind                    string                                  `json:"target_kind"`
	PolicyID                      string                                  `json:"policy_id"`
	AttestationValidUntil         time.Time                               `json:"attestation_valid_until"`
	AllowDisabledApproval         bool                                    `json:"allow_disabled_approval"`
	ContractSelections            map[string][]workflowOperationSelection `json:"contract_selections"`
	ContractProjection            workflowProjectionConfig                `json:"contract_projection"`
	ContractReviewID              string                                  `json:"contract_review_id,omitempty"`
	ContractReviewRevision        uint64                                  `json:"contract_review_revision,omitempty"`
	ContractReviewDigest          string                                  `json:"contract_review_digest,omitempty"`
	IdentityCredentialEnvironment map[string]string                       `json:"identity_credential_environment,omitempty"`
	IdentityObservationTimeoutMS  int                                     `json:"identity_observation_timeout_ms,omitempty"`
	FixtureRuntime                *workflowFixtureRuntimeConfig           `json:"fixture_runtime,omitempty"`
	FunctionalFlowAliases         map[string]string                       `json:"functional_flow_aliases,omitempty"`
	GitHubContext                 *githubcontext.Policy                   `json:"github_context,omitempty"`
	EnvironmentCatalog            environment.Catalog                     `json:"environment_catalog"`
	ManagedCapacity               *workflowManagedCapacityConfig          `json:"managed_capacity,omitempty"`
	EnvironmentCleanup            *workflowCleanupRuntimeConfig           `json:"environment_cleanup,omitempty"`
	Kubernetes                    *workflowKubernetesRuntimeConfig        `json:"kubernetes,omitempty"`
}

// workflowManagedCapacityConfig carries sealed project policy and per-selection demand without request/model defaults.
type workflowManagedCapacityConfig struct {
	Policy  environmentcleanup.CapacityPolicy     `json:"policy"`
	Demands map[string]environment.ResourceDemand `json:"demands"`
	TTLs    map[string]time.Duration              `json:"ttls"`
}

// workflowFixtureRuntimeConfig is the trusted operator-owned authority for every finite fixture effect and wait.
type workflowFixtureRuntimeConfig struct {
	MaxCaptureBytes  int `json:"max_capture_bytes"`
	MaxToolCalls     int `json:"max_tool_calls"`
	MaxRetries       int `json:"max_retries"`
	CreateRetries    int `json:"create_retries"`
	CleanupTimeoutMS int `json:"cleanup_timeout_ms"`
	CleanupMaxCalls  int `json:"cleanup_max_calls"`
	CleanupRetries   int `json:"cleanup_retries"`
	RetryBackoffMS   int `json:"retry_backoff_ms"`
	LeaseTTLMS       int `json:"lease_ttl_ms"`
}

// validate rejects absent, non-positive, or internally inconsistent fixture authority without consulting the specification.
func (c *workflowFixtureRuntimeConfig) validate() error {
	if c == nil || c.MaxCaptureBytes <= 0 || c.MaxToolCalls <= 0 || c.MaxRetries <= 0 || c.CreateRetries <= 0 || c.CleanupTimeoutMS <= 0 || c.CleanupMaxCalls <= 0 || c.CleanupRetries <= 0 || c.RetryBackoffMS <= 0 || c.LeaseTTLMS <= 0 {
		return errors.New("fixture runtime limits unavailable")
	}
	if c.CreateRetries > c.MaxRetries || c.CleanupRetries > c.MaxRetries || c.CleanupMaxCalls > c.MaxToolCalls || c.RetryBackoffMS >= c.CleanupTimeoutMS || c.LeaseTTLMS <= c.CleanupTimeoutMS {
		return errors.New("fixture runtime limits inconsistent")
	}
	return nil
}

// workflowEnvironmentCredentialResolver resolves one composition-mapped environment entry only at the terminal authenticated attempt.
type workflowEnvironmentCredentialResolver struct {
	reference, environment string
}

// workflowGitHubContextObserver accepts only the already content-free terminal
// record when PostgreSQL composition must not create a filesystem journal.
type workflowGitHubContextObserver struct{}

// Observe completes the PostgreSQL-mode observation boundary without retaining
// request content, credentials, or a second durable source of truth.
func (workflowGitHubContextObserver) Observe(context.Context, githubcontext.Record) error { return nil }

// workflowCredentialResolverFactory is the composition-owned port that creates one late resolver for each trusted reference mapping.
type workflowCredentialResolverFactory func(reference, environment string) testidentity.CredentialResolver

// workflowCompositionOptions carries productive ports and startup recovery behavior without exposing them to workflow input.
type workflowCompositionOptions struct {
	githubAuthority           validationworkflow.GitHubPullRequestAuthority
	credentialResolverFactory workflowCredentialResolverFactory
	environmentProviders      map[string]environment.EnvironmentProvider
	kubernetes                *workflowKubernetesComposition
	recoverPending            bool
	persistence               *workflowPersistence
}

// workflowPersistence is the backend-homogeneous set of consumer-owned ports used by the productive workflow.
type workflowPersistence struct {
	workflows     validationworkflow.WorkflowStore
	artifacts     validationworkflow.PlanArtifactStore
	executables   validationworkflow.ExecutablePlanStore
	approvals     planapproval.ApprovalStore
	environments  environment.CleanupLifecycleStore
	capacity      environment.CapacityAdmission
	cleanupLedger workflowCapacityLedger
	cleanup       environmentcleanup.Store
	functional    functionalflow.IntentStore
	journal       functionalflow.EventJournal
	packages      validationworkflow.PackageStore
	corrections   correction.Store
	fixtures      interface {
		fixture.Store
		fixture.EvidenceSink
		fixture.Observer
	}
	context           contextassembly.Store
	functionalRuntime interface {
		FunctionalRuntimeStores(map[string]string, functionalflow.Limits, functionalflow.EventJournal) (validationworkflow.FunctionalFlowArtifactStore, functionalflow.EvidenceStore, validationworkflow.FunctionalFlowPublisher, validationworkflow.FunctionalFlowCatalog, validationworkflow.FunctionalIdentityAttemptSink, error)
	}
}

// workflowKubernetesComposition carries explicit cluster transport, sealed catalog, secrets, and safety bounds outside workflow input.
type workflowKubernetesComposition struct {
	RESTConfig       *rest.Config
	Catalog          kubernetesenvironment.Catalog
	Secrets          map[string][]byte
	Owner, ClusterID string
	MaxList          int64
}

// workflowKubernetesRuntimeConfig names every product-owned file, credential location, cluster identity, and safety bound needed to build Kubernetes authority.
type workflowKubernetesRuntimeConfig struct {
	APIServer              string            `json:"api_server"`
	BearerTokenEnvironment string            `json:"bearer_token_environment"`
	CAFile                 string            `json:"ca_file"`
	ServerName             string            `json:"server_name"`
	CatalogPath            string            `json:"catalog_path"`
	Owner                  string            `json:"owner"`
	ClusterID              string            `json:"cluster_id"`
	SecretEnvironments     map[string]string `json:"secret_environments"`
	MaxList                int64             `json:"max_list"`
}

// workflowTargetPlanningBinder derives the planner's HTTP validator from the acquired provider target at the final planning boundary.
type workflowTargetPlanningBinder struct {
	spec     specification.Specification
	manifest planmanifest.Manifest
	deps     localvalidation.RuntimeDependencies
}

// workflowTargetHTTPTool routes functional-flow calls only through tools rebuilt from acquired per-run targets.
type workflowTargetHTTPTool struct {
	mu       sync.RWMutex
	spec     specification.Specification
	manifest planmanifest.Manifest
	deps     localvalidation.RuntimeDependencies
	tools    map[string]harness.Tool
	digests  map[string]string
}

// workflowFunctionalFlowTargetPort binds acquired target authority and releases it only when the application service confirms rollback or terminal durability.
type workflowFunctionalFlowTargetPort struct {
	inner validationworkflow.FunctionalFlowExecutionPort
	tool  *workflowTargetHTTPTool
}

// workflowTargetFunctionalFlowComposer seals functional artifacts to the acquired target digest instead of composition-time coordinates.
type workflowTargetFunctionalFlowComposer struct {
	inner         validationworkflow.CanonicalFunctionalFlowComposer
	runtimeDigest string
}

// Bind rejects an invalid target projection and builds the sole HTTP capability against its endpoint.
func (b workflowTargetPlanningBinder) Bind(ctx context.Context, target validationworkflow.TargetContext) ([]agentplanning.CapabilityDescriptor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := validationworkflow.TargetContextDigest(target); err != nil {
		return nil, err
	}
	capability, err := localvalidation.BuildHTTPPlanningCapability(b.spec, b.manifest, target.URL, b.deps)
	if err != nil {
		return nil, err
	}
	return []agentplanning.CapabilityDescriptor{capability}, nil
}

// Bind replaces one run's functional HTTP tool with a capability built from its exact acquired target.
func (t *workflowTargetHTTPTool) Bind(ctx context.Context, runID string, target validationworkflow.TargetContext) error {
	if t == nil || runID == "" {
		return errors.New("functional target unavailable")
	}
	digest, err := validationworkflow.TargetContextDigest(target)
	if err != nil {
		return errors.New("functional target unavailable")
	}
	t.mu.RLock()
	current := t.digests[runID]
	t.mu.RUnlock()
	if current == digest {
		return nil
	}
	spec := t.spec
	spec.Target.BaseURL = target.URL
	tool, _, err := localvalidation.BuildFixtureHTTPRuntime(spec, t.manifest, t.deps)
	if err != nil || ctx.Err() != nil {
		return errors.New("functional target unavailable")
	}
	t.mu.Lock()
	t.tools[runID], t.digests[runID] = tool, digest
	t.mu.Unlock()
	return nil
}

// Execute rejects unbound runs and delegates only to the acquired-target tool selected for the call's workflow identity.
func (t *workflowTargetHTTPTool) Execute(ctx context.Context, call harness.ToolCall) (harness.ToolResult, error) {
	if t == nil {
		return harness.ToolResult{}, errors.New("functional target unavailable")
	}
	t.mu.RLock()
	tool := t.tools[call.RunID]
	t.mu.RUnlock()
	if tool == nil {
		return harness.ToolResult{}, errors.New("functional target unavailable")
	}
	return tool.Execute(ctx, call)
}

// Unbind idempotently releases both endpoint tool and digest after a run reaches a durable terminal.
func (t *workflowTargetHTTPTool) Unbind(runID string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	delete(t.tools, runID)
	delete(t.digests, runID)
	t.mu.Unlock()
}

// Compose rebuilds target-dependent artifact bindings from the latest acquired workflow snapshot.
func (c workflowTargetFunctionalFlowComposer) Compose(ctx context.Context, request validationworkflow.ExecutionRequest, snapshot agentplanning.PreparedSnapshot) (functionalflow.ValidatedFlow, functionalflow.Artifact, error) {
	digest, err := validationworkflow.TargetContextDigest(request.Snapshot.Binding.Target)
	if err != nil || digest != request.Snapshot.TargetDigest {
		return functionalflow.ValidatedFlow{}, functionalflow.Artifact{}, errors.New("functional target unavailable")
	}
	inner := c.inner
	inner.Binding.Bindings.Target = digest
	inner.Generated.ContextDigest = workflowDigest("functional-context", c.runtimeDigest, digest)
	return inner.Compose(ctx, request, snapshot)
}

// InspectProvided validates an operator-selected flow without retaining an executable target tool before admission.
func (p workflowFunctionalFlowTargetPort) InspectProvided(ctx context.Context, request validationworkflow.ExecutionRequest) (functionalflow.Flow, error) {
	return p.inner.InspectProvided(ctx, request)
}

// Admit binds target authority before sealing the functional-flow artifact.
func (p workflowFunctionalFlowTargetPort) Admit(ctx context.Context, request validationworkflow.ExecutionRequest, snapshot agentplanning.PreparedSnapshot) (planapproval.FunctionalFlowSnapshot, error) {
	if err := p.tool.Bind(ctx, request.Handle, request.Snapshot.Binding.Target); err != nil {
		return planapproval.FunctionalFlowSnapshot{}, err
	}
	admitted, err := p.inner.Admit(ctx, request, snapshot)
	if err != nil {
		p.tool.Unbind(request.Handle)
	}
	return admitted, err
}

// RecordPreparation delegates the durable fixture terminal to the already target-bound runtime.
func (p workflowFunctionalFlowTargetPort) RecordPreparation(ctx context.Context, handle string, status fixture.PreparationStatus) (fixture.PreparationStatus, error) {
	return p.inner.RecordPreparation(ctx, handle, status)
}

// Execute rebinds from the latest revalidated snapshot immediately before any functional network call.
func (p workflowFunctionalFlowTargetPort) Execute(ctx context.Context, request validationworkflow.ExecutionRequest, snapshot agentplanning.PreparedSnapshot) (validationworkflow.FunctionalFlowPrimary, error) {
	if err := p.tool.Bind(ctx, request.Handle, request.Snapshot.Binding.Target); err != nil {
		return validationworkflow.FunctionalFlowPrimary{}, err
	}
	return p.inner.Execute(ctx, request, snapshot)
}

// Promote delegates publication after Service has revalidated the same acquired target.
func (p workflowFunctionalFlowTargetPort) Promote(ctx context.Context, primary validationworkflow.FunctionalFlowPrimary) error {
	return p.inner.Promote(ctx, primary)
}

// Finalize delegates packaging without releasing target authority before the application persists its terminal state.
func (p workflowFunctionalFlowTargetPort) Finalize(ctx context.Context, handle string, outcome validationworkflow.Outcome, summary fixture.FixtureSummary) (validationworkflow.PackageProjection, error) {
	return p.inner.Finalize(ctx, handle, outcome, summary)
}

// RollbackAdmission releases target authority whose successful admission could not be made durable by the workflow store.
func (p workflowFunctionalFlowTargetPort) RollbackAdmission(handle string) {
	p.tool.Unbind(handle)
}

// ConfirmTerminal releases target authority only after the workflow service confirms the terminal SaveOwned succeeded.
func (p workflowFunctionalFlowTargetPort) ConfirmTerminal(handle string) {
	p.tool.Unbind(handle)
}

// validate rejects partial runtime authority without reading credentials or contacting the cluster.
func (c *workflowKubernetesRuntimeConfig) validate() error {
	if c == nil {
		return errors.New("invalid kubernetes runtime configuration")
	}
	endpoint, endpointErr := url.Parse(c.APIServer)
	if endpointErr != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || c.BearerTokenEnvironment == "" || c.CAFile == "" || c.ServerName == "" || c.CatalogPath == "" || c.Owner == "" || c.ClusterID == "" || c.SecretEnvironments == nil || c.MaxList <= 0 {
		return errors.New("invalid kubernetes runtime configuration")
	}
	for reference, environmentName := range c.SecretEnvironments {
		if reference == "" || environmentName == "" {
			return errors.New("invalid kubernetes runtime configuration")
		}
	}
	return nil
}

// composeWorkflowKubernetesRuntime resolves only explicitly named product authority during composition and performs no cluster I/O.
func composeWorkflowKubernetesRuntime(config *workflowKubernetesRuntimeConfig) (*workflowKubernetesComposition, error) {
	if config == nil || config.validate() != nil {
		return nil, errors.New("kubernetes environment composition unavailable")
	}
	catalogRaw, catalogErr := os.ReadFile(config.CatalogPath)
	caData, caErr := os.ReadFile(config.CAFile)
	token, tokenPresent := os.LookupEnv(config.BearerTokenEnvironment)
	if catalogErr != nil || caErr != nil || len(catalogRaw) == 0 || len(catalogRaw) > 1<<20 || len(caData) == 0 || len(caData) > 1<<20 || !tokenPresent || token == "" {
		return nil, errors.New("kubernetes environment composition unavailable")
	}
	catalog, err := kubernetesenvironment.DecodeCatalog(catalogRaw)
	if err != nil {
		return nil, errors.New("kubernetes environment composition unavailable")
	}
	secrets := make(map[string][]byte, len(config.SecretEnvironments))
	for reference, environmentName := range config.SecretEnvironments {
		value, present := os.LookupEnv(environmentName)
		if !present || value == "" {
			return nil, errors.New("kubernetes environment composition unavailable")
		}
		secrets[reference] = []byte(value)
	}
	restConfig := &rest.Config{Host: config.APIServer, BearerToken: token, TLSClientConfig: rest.TLSClientConfig{CAData: append([]byte(nil), caData...), ServerName: config.ServerName}}
	return &workflowKubernetesComposition{RESTConfig: restConfig, Catalog: catalog, Secrets: secrets, Owner: config.Owner, ClusterID: config.ClusterID, MaxList: config.MaxList}, nil
}

// composeWorkflowKubernetesProvider builds the real Kubernetes adapter without kubeconfig, environment, or fallback discovery.
func composeWorkflowKubernetesProvider(config *workflowKubernetesComposition) (environment.EnvironmentProvider, error) {
	if config == nil {
		return nil, errors.New("kubernetes environment composition unavailable")
	}
	client, err := kubernetesenvironment.NewAPIClient(config.RESTConfig)
	if err != nil {
		return nil, err
	}
	secrets, err := kubernetesenvironment.NewPrivateSecretBundle(config.Secrets)
	if err != nil {
		return nil, err
	}
	return kubernetesenvironment.NewProvider(kubernetesenvironment.Dependencies{Catalog: config.Catalog, Client: client, Clock: environment.WallClock{}, Secrets: secrets, Owner: config.Owner, ClusterID: config.ClusterID, MaxList: config.MaxList})
}

// Resolve copies late-bound material into an attempt-owned lease without exposing its environment location outside composition.
func (r workflowEnvironmentCredentialResolver) Resolve(ctx context.Context, request testidentity.ResolutionRequest) (testidentity.CredentialLease, error) {
	if err := ctx.Err(); err != nil {
		return testidentity.CredentialLease{}, err
	}
	if request.CredentialReference != r.reference {
		return testidentity.CredentialLease{}, errors.New("credential resolver mismatch")
	}
	value, ok := os.LookupEnv(r.environment)
	if !ok || value == "" {
		return testidentity.CredentialLease{}, errors.New("credential unavailable")
	}
	return testidentity.NewCredentialLease([]byte(value), request.Mechanism, time.Time{}, nil), nil
}

// newWorkflowEnvironmentCredentialResolver creates the default resolver that reads its trusted environment location only during Resolve.
func newWorkflowEnvironmentCredentialResolver(reference, environment string) testidentity.CredentialResolver {
	return workflowEnvironmentCredentialResolver{reference: reference, environment: environment}
}

// workflowIdentityObserver confirms terminal observation synchronously without publishing credential-adjacent data.
type workflowIdentityObserver struct{}

// Observe honors the composition-owned bound and leaves durable safe attempt facts to the existing runner journal.
func (workflowIdentityObserver) Observe(ctx context.Context, _ testidentity.AttemptRecord) error {
	return ctx.Err()
}

// composeWorkflowIdentities validates every opaque reference against one trusted late environment resolver before model construction.
func composeWorkflowIdentities(spec specification.Specification, config workflowConfig) (map[string]testidentity.CredentialResolver, testidentity.Observer, time.Duration, error) {
	return composeWorkflowIdentitiesWithFactory(spec, config, newWorkflowEnvironmentCredentialResolver)
}

// composeWorkflowIdentitiesWithFactory validates the exact reference set and delegates resolver construction to the trusted composition port.
func composeWorkflowIdentitiesWithFactory(spec specification.Specification, config workflowConfig, factory workflowCredentialResolverFactory) (map[string]testidentity.CredentialResolver, testidentity.Observer, time.Duration, error) {
	if len(spec.Identities) == 0 {
		if len(config.IdentityCredentialEnvironment) != 0 || config.IdentityObservationTimeoutMS != 0 {
			return nil, nil, 0, errors.New("unexpected identity composition")
		}
		return nil, nil, 0, nil
	}
	references := make(map[string]struct{}, len(spec.Identities))
	for _, identity := range spec.Identities {
		references[identity.CredentialRef] = struct{}{}
	}
	if factory == nil || config.IdentityObservationTimeoutMS <= 0 || len(config.IdentityCredentialEnvironment) != len(references) {
		return nil, nil, 0, errors.New("identity composition unavailable")
	}
	resolvers := make(map[string]testidentity.CredentialResolver, len(references))
	for reference := range references {
		environment := config.IdentityCredentialEnvironment[reference]
		if environment == "" || strings.ContainsAny(environment, "=\x00") {
			return nil, nil, 0, errors.New("identity composition unavailable")
		}
		resolvers[reference] = factory(reference, environment)
		if resolvers[reference] == nil {
			return nil, nil, 0, errors.New("identity composition unavailable")
		}
	}
	return resolvers, workflowIdentityObserver{}, time.Duration(config.IdentityObservationTimeoutMS) * time.Millisecond, nil
}

// workflowIdentityCompositionDigest binds safe resolver mappings and observation bounds without reading or hashing credential material.
func workflowIdentityCompositionDigest(config workflowConfig) string {
	keys := make([]string, 0, len(config.IdentityCredentialEnvironment))
	for reference := range config.IdentityCredentialEnvironment {
		keys = append(keys, reference)
	}
	sort.Strings(keys)
	values := []string{strconv.Itoa(config.IdentityObservationTimeoutMS)}
	for _, reference := range keys {
		values = append(values, reference, config.IdentityCredentialEnvironment[reference])
	}
	return workflowDigest(values...)
}

// workflowEffectiveRedaction seals one identity-augmented policy and manifest configuration shared by planning, approval, and execution.
func workflowEffectiveRedaction(spec specification.Specification, manifest *planmanifest.Manifest) (*evidence.Policy, error) {
	if manifest == nil {
		return nil, errors.New("runtime manifest unavailable")
	}
	manifest.Redaction = evidence.AugmentAuthenticationConfig(manifest.Redaction, spec.Identities)
	return evidence.NewPolicy(manifest.Redaction)
}

// workflowGoInspectorConfig carries the explicit trusted roots, PR impact, build selector, auth allowlist, and budgets for Go analysis.
type workflowGoInspectorConfig struct {
	Roots            []string                  `json:"roots"`
	ChangedPaths     []string                  `json:"changed_paths"`
	GOOS             string                    `json:"goos"`
	GOARCH           string                    `json:"goarch"`
	BuildTags        []string                  `json:"build_tags"`
	Auth             []goinspector.AuthMatcher `json:"auth_matchers"`
	Limits           goinspector.Limits        `json:"limits"`
	IncludeGenerated bool                      `json:"include_generated"`
	EntrySymbols     []string                  `json:"entry_symbols"`
}

// workflowSpringInspectorConfig carries explicit Java source sets, release, generated policy and bounded inspection authority.
type workflowSpringInspectorConfig struct {
	RootIdentity        string                          `json:"root_identity"`
	FrameworkIdentities []string                        `json:"framework_identities"`
	SourceSets          []springinspector.SourceSet     `json:"source_sets"`
	ChangedPaths        []string                        `json:"changed_paths"`
	JavaRelease         int                             `json:"java_release"`
	Generated           springinspector.GeneratedPolicy `json:"generated_policy"`
	Limits              springinspector.Limits          `json:"limits"`
}

// workflowOperationSelection is one trusted method/path pair assigned to a specification constraint.
type workflowOperationSelection struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// workflowProjectionConfig is the required trusted redaction and structural allowlist record for contract ingestion.
type workflowProjectionConfig struct {
	MaxStringBytes       int      `json:"max_string_bytes"`
	AllowedURLSchemes    []string `json:"allowed_url_schemes"`
	SensitiveNames       []string `json:"sensitive_names"`
	SensitivePaths       []string `json:"sensitive_paths"`
	SensitiveHeaderNames []string `json:"sensitive_header_names"`
	ForbiddenValues      []string `json:"forbidden_values"`
}

// policy validates and seals configuration plus manifest-owned sensitive header names without introducing defaults.
func (c workflowProjectionConfig) policy(additionalHeaders []string) (contractcatalog.ProjectionPolicy, error) {
	if c.AllowedURLSchemes == nil || c.SensitiveNames == nil || c.SensitivePaths == nil || c.SensitiveHeaderNames == nil || c.ForbiddenValues == nil {
		return contractcatalog.ProjectionPolicy{}, errors.New("incomplete contract projection configuration")
	}
	headers := append(append([]string{}, c.SensitiveHeaderNames...), additionalHeaders...)
	return contractcatalog.NewProjectionPolicy(contractcatalog.ProjectionPolicy{MaxStringBytes: c.MaxStringBytes, AllowedURLSchemes: append([]string{}, c.AllowedURLSchemes...), SensitiveNames: append([]string{}, c.SensitiveNames...), SensitivePaths: append([]string{}, c.SensitivePaths...), SensitiveHeaderNames: headers, ForbiddenValues: append([]string{}, c.ForbiddenValues...)})
}

// loadWorkflowConfig strictly decodes one bounded trusted configuration selected outside workflow input.
// feature22:coverage loadWorkflowConfig
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
	sealedEnvironmentCatalog, environmentCatalogErr := environment.SealCatalog(config.EnvironmentCatalog)
	selection, selectionErr := sealedEnvironmentCatalog.Resolve(config.TargetAlias)
	managedKubernetes := selection.Kind == environment.KindManaged && selection.ProviderID == kubernetesenvironment.ProviderID && selection.ProviderVersion == kubernetesenvironment.ProviderVersion
	if environmentCatalogErr != nil || selectionErr != nil || (selection.Kind != environment.KindExistingURL && selection.Kind != environment.KindManaged) || selection.ProjectID != config.RepositoryID || selection.RepositoryID != config.RepositoryID || selection.NetworkPolicyID != config.PolicyID || (selection.Kind == environment.KindManaged && (config.TargetKind != "direct" || !managedKubernetes || config.Kubernetes == nil || config.Kubernetes.validate() != nil)) || (selection.Kind != environment.KindManaged && config.Kubernetes != nil) {
		return workflowConfig{}, errors.New("invalid environment catalog configuration")
	}
	config.EnvironmentCatalog = sealedEnvironmentCatalog
	if config.ContractSource != "" && config.ContractSource != "openapi" && config.ContractSource != goinspector.SourceKind && config.ContractSource != springinspector.SourceKind && config.ContractSource != "combined" {
		return workflowConfig{}, errors.New("invalid workflow configuration")
	}
	if len(config.ContractSources) > 0 && !validContractSources(config.ContractSources) {
		return workflowConfig{}, errors.New("invalid workflow configuration")
	}
	if len(config.ContractSources) > 0 && config.ContractSource != "combined" {
		return workflowConfig{}, errors.New("invalid workflow configuration")
	}
	if config.ContractSource == goinspector.SourceKind && (config.GoInspector == nil || len(config.GoInspector.Roots) == 0 || len(config.GoInspector.EntrySymbols) == 0 || config.GoInspector.GOOS == "" || config.GoInspector.GOARCH == "") {
		return workflowConfig{}, errors.New("invalid workflow configuration")
	}
	if config.ContractSource == springinspector.SourceKind && (config.SpringInspector == nil || config.SpringInspector.RootIdentity == "" || len(config.SpringInspector.FrameworkIdentities) == 0 || len(config.SpringInspector.SourceSets) == 0 || config.SpringInspector.JavaRelease == 0 || config.RepositoryRoot == "") {
		return workflowConfig{}, errors.New("invalid workflow configuration")
	}
	if config.ContractSource == "combined" && (config.RepositoryRoot == "" || len(config.ContractSources) < 2) {
		return workflowConfig{}, errors.New("invalid workflow configuration")
	}
	combinedSources := config.ContractSources
	if config.ContractSource == "combined" && len(combinedSources) == 0 {
		combinedSources = []string{"openapi", goinspector.SourceKind}
	}
	if containsContractSource(combinedSources, "openapi") && config.OpenAPIContractPath == "" {
		return workflowConfig{}, errors.New("invalid workflow configuration")
	}
	if containsContractSource(combinedSources, goinspector.SourceKind) && (config.GoInspector == nil || len(config.GoInspector.Roots) == 0 || len(config.GoInspector.EntrySymbols) == 0 || config.GoInspector.GOOS == "" || config.GoInspector.GOARCH == "") {
		return workflowConfig{}, errors.New("invalid workflow configuration")
	}
	if containsContractSource(config.ContractSources, springinspector.SourceKind) && (config.SpringInspector == nil || config.SpringInspector.RootIdentity == "" || len(config.SpringInspector.FrameworkIdentities) == 0 || len(config.SpringInspector.SourceSets) == 0) {
		return workflowConfig{}, errors.New("invalid workflow configuration")
	}
	if _, err := config.ContractProjection.policy(nil); err != nil {
		return workflowConfig{}, errors.New("invalid workflow configuration")
	}
	if _, err := config.approvedContractReference(); err != nil {
		return workflowConfig{}, errors.New("invalid workflow configuration")
	}
	if config.GitHubContext != nil {
		sealed, policyErr := githubcontext.SealPolicy(*config.GitHubContext)
		if policyErr != nil || config.GitHubContext.Digest != "" && config.GitHubContext.Digest != sealed.Digest {
			return workflowConfig{}, errors.New("invalid github context configuration")
		}
		config.GitHubContext = &sealed
	}
	return config, nil
}

// approvedContractReference validates the trusted all-or-none durable review selector used by CLI, MCP and GitHub starts.
func (c workflowConfig) approvedContractReference() (*validationworkflow.ApprovedContractReference, error) {
	absent := c.ContractReviewID == "" && c.ContractReviewRevision == 0 && c.ContractReviewDigest == ""
	if absent {
		return nil, nil
	}
	if len(c.ContractReviewID) != 24 || c.ContractReviewRevision == 0 || len(c.ContractReviewDigest) != 64 {
		return nil, errors.New("invalid approved contract reference")
	}
	if _, err := hex.DecodeString(c.ContractReviewID); err != nil {
		return nil, errors.New("invalid approved contract reference")
	}
	if _, err := hex.DecodeString(c.ContractReviewDigest); err != nil {
		return nil, errors.New("invalid approved contract reference")
	}
	return &validationworkflow.ApprovedContractReference{ReviewID: c.ContractReviewID, ExpectedRevision: c.ContractReviewRevision, ProjectionDigest: c.ContractReviewDigest}, nil
}

// validContractSources requires a duplicate-free explicit list from the closed adapter registry.
func validContractSources(values []string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] || (value != "openapi" && value != goinspector.SourceKind && value != springinspector.SourceKind) {
			return false
		}
		seen[value] = true
	}
	return len(seen) > 0
}

// containsContractSource checks one exact adapter identity in a sealed source list.
func containsContractSource(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

// composeWorkflowCLI resolves OAuth and filesystem references only in the executable composition root.
// feature22:coverage composeWorkflowCLI
func composeWorkflowCLI(ctx context.Context, stdout, stderr io.Writer) (*validationworkflow.CLI, error) {
	return composeWorkflowCLIWithOptions(ctx, stdout, stderr, workflowCompositionOptions{credentialResolverFactory: newWorkflowEnvironmentCredentialResolver, recoverPending: true})
}

// composeWorkflowCLIWithGitHubAuthority additionally binds a composition-owned PR refresher for durable GitHub decisions.
func composeWorkflowCLIWithGitHubAuthority(ctx context.Context, stdout, stderr io.Writer, githubAuthority validationworkflow.GitHubPullRequestAuthority) (*validationworkflow.CLI, error) {
	return composeWorkflowCLIWithOptions(ctx, stdout, stderr, workflowCompositionOptions{githubAuthority: githubAuthority, credentialResolverFactory: newWorkflowEnvironmentCredentialResolver, recoverPending: true})
}

// composeWorkflowCLIWithPersistence builds the productive workflow exclusively from the selected PostgreSQL consumer ports.
func composeWorkflowCLIWithPersistence(ctx context.Context, stdout, stderr io.Writer, persistence *workflowPersistence) (*validationworkflow.CLI, error) {
	if persistence == nil {
		return nil, errors.New("postgres workflow persistence unavailable")
	}
	return composeWorkflowCLIWithOptions(ctx, stdout, stderr, workflowCompositionOptions{credentialResolverFactory: newWorkflowEnvironmentCredentialResolver, recoverPending: true, persistence: persistence})
}

// composeWorkflowCLIWithOptions constructs the shared productive service graph from trusted ports and optionally runs startup recovery.
func composeWorkflowCLIWithOptions(ctx context.Context, stdout, stderr io.Writer, options workflowCompositionOptions) (*validationworkflow.CLI, error) {
	config, err := loadWorkflowConfig()
	if err != nil {
		return nil, err
	}
	selection, selectionErr := config.EnvironmentCatalog.Resolve(config.TargetAlias)
	if selectionErr != nil {
		return nil, selectionErr
	}
	if selection.Kind == environment.KindManaged {
		if len(options.environmentProviders) != 0 {
			return nil, errors.New("duplicate managed environment composition")
		}
		if options.kubernetes == nil {
			options.kubernetes, err = composeWorkflowKubernetesRuntime(config.Kubernetes)
			if err != nil {
				return nil, err
			}
		}
		provider, providerErr := composeWorkflowKubernetesProvider(options.kubernetes)
		if providerErr != nil {
			return nil, providerErr
		}
		options.environmentProviders = map[string]environment.EnvironmentProvider{selection.ProviderID + "@" + selection.ProviderVersion: provider}
	} else if options.kubernetes != nil {
		return nil, errors.New("unexpected kubernetes environment composition")
	}
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	var workflowPreview *workflowPreviewPort
	if config.TargetKind == "preview" {
		configured, present, previewErr := composeConfiguredPreview(ctx, root)
		if previewErr != nil || !present || configured.Request.Selection.Alias != config.TargetAlias || configured.Config.RepositoryID != config.RepositoryID || configured.Config.CommitOID != config.CommitOID || configured.Config.EnvironmentID != config.EnvironmentID || configured.Config.Target != config.TargetURL || configured.Config.AttestationRef != config.AttestationRef || !configured.Config.AttestationValidUntil.Equal(config.AttestationValidUntil) {
			return nil, errors.New("preview workflow configuration unavailable")
		}
		workflowPreview = &workflowPreviewPort{configured: configured, target: config.TargetURL, attestors: map[string]revisionbinding.EnvironmentAttestor{}}
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
	identityResolvers, identityObserver, identityObservationTimeout, err := composeWorkflowIdentitiesWithFactory(spec, config, options.credentialResolverFactory)
	if err != nil {
		return nil, err
	}
	redaction, err := workflowEffectiveRedaction(spec, &manifest)
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
	runtimeDeps.CredentialResolvers, runtimeDeps.IdentityObserver, runtimeDeps.IdentityObservationTimeout = identityResolvers, identityObserver, identityObservationTimeout
	capability, err := localvalidation.BuildHTTPPlanningCapability(spec, manifest, workflowProvisionalPlanningTarget, runtimeDeps)
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
	environmentSelection, err := config.EnvironmentCatalog.Resolve(config.TargetAlias)
	if err != nil {
		return nil, errors.New("environment selection unavailable")
	}
	targetContext := validationworkflow.TargetContext{Kind: config.TargetKind, Alias: config.TargetAlias, URL: config.TargetURL, SelectionDigest: environmentSelection.Digest, ProviderID: environmentSelection.ProviderID, ProviderVersion: environmentSelection.ProviderVersion, EnvironmentRef: environmentSelection.TemplateReference, ProfileID: environmentSelection.ProfileID, EnvironmentID: config.EnvironmentID, Capabilities: append([]string(nil), environmentSelection.Capabilities...), Attestation: validationworkflow.AttestationContext{EnvironmentID: config.EnvironmentID, RepositoryID: config.RepositoryID, CommitOID: config.CommitOID, Reference: config.AttestationRef, ValidUntil: config.AttestationValidUntil, Verified: true}, Network: network}
	if workflowPreview != nil {
		if targetContext.ProviderID != workflowPreview.configured.Config.ProviderID || targetContext.EnvironmentRef != workflowPreview.configured.Config.EnvironmentRef || targetContext.ProfileID != workflowPreview.configured.Config.ProfileID {
			return nil, errors.New("preview environment selection mismatch")
		}
	}
	if targetContext.ObservedVersion == "" {
		targetContext.ObservedVersion = "configured-v1"
	}
	if targetContext.Fingerprint == "" {
		targetContext.Fingerprint = workflowDigest("environment", targetContext.EnvironmentID, targetContext.URL, targetContext.Attestation.Reference)
	}
	environmentPort, environmentRuntime, err := composeWorkflowEnvironmentWithRuntime(root, config, targetContext, workflowPreview, options.environmentProviders, options.persistence)
	if err != nil {
		return nil, err
	}
	if environmentRuntime != nil && environmentSelection.ProviderID == "kubernetes" {
		if options.kubernetes == nil {
			return nil, errors.New("cleanup inventory composition unavailable")
		}
		clientset, clientErr := kubernetesclient.NewForConfig(options.kubernetes.RESTConfig)
		if clientErr != nil {
			return nil, clientErr
		}
		inventory, inventoryErr := environmentcleanup.NewKubernetesInventory(environmentcleanup.KubernetesInventoryConfig{ProviderID: environmentSelection.ProviderID, ProviderVersion: environmentSelection.ProviderVersion, Owner: options.kubernetes.Owner, IdentityPath: filepath.Join(root, ".featureproof", "cleanup", "kubernetes-identities.json"), Client: clientset, MaxPages: environmentRuntime.config.MaxPages, PageSize: int(options.kubernetes.MaxList)})
		if inventoryErr != nil {
			return nil, inventoryErr
		}
		environmentRuntime.inventories = map[string]environmentcleanup.ManagedInventory{environmentSelection.ProviderID + "@" + environmentSelection.ProviderVersion: inventory}
	}
	revisionContext := validationworkflow.RevisionContext{RepositoryID: config.RepositoryID, CommitOID: config.CommitOID}
	if config.GoInspector != nil {
		revisionContext.Roots = append([]string{}, config.GoInspector.Roots...)
		revisionContext.ChangedPaths = append([]string{}, config.GoInspector.ChangedPaths...)
	}
	bindingContext := validationworkflow.BindingContext{Revision: revisionContext, Target: targetContext}
	runtimeContext := workflowRuntimeContext(config, manifest, network)
	targetDigest, err := validationworkflow.TargetContextDigest(targetContext)
	if err != nil {
		return nil, err
	}
	runtimeDigest, err := validationworkflow.RuntimeContextDigest(runtimeContext)
	if err != nil {
		return nil, err
	}
	compositionRef := workflowDigest("composition", config.Provider, config.Model, config.AuthProfile, workflowIdentityCompositionDigest(config))
	projection, err := config.ContractProjection.policy(manifest.Redaction.AdditionalHeaders)
	if err != nil {
		return nil, errors.New("invalid contract projection configuration")
	}
	contractReducer := workflowContractReducerFor(config, projection)
	var approvedAuthority validationworkflow.ApprovedContractAuthority
	if os.Getenv("FEATUREPROOF_CONTRACT_REVIEW_CONFIG") != "" {
		reviewCLI, reviewErr := composeContractReviewCLI(ctx)
		if reviewErr != nil {
			return nil, reviewErr
		}
		reviewService, ok := reviewCLI.API.Service.(contractreview.Service)
		if !ok {
			return nil, errors.New("invalid contract review composition")
		}
		if options.githubAuthority != nil {
			reviewService.GitHub = options.githubAuthority
		}
		authority := contractreview.WorkflowAuthority{Service: reviewService}
		approvedAuthority = authority
		rules := map[contractcatalog.ClaimKind]contractcatalog.ClaimRule{}
		for _, kind := range []contractcatalog.ClaimKind{contractcatalog.ClaimOperationIdentity, contractcatalog.ClaimSchema, contractcatalog.ClaimSecurity, contractcatalog.ClaimResponse} {
			rules[kind] = contractcatalog.ClaimRule{AllowedClasses: []contractcatalog.SourceClass{contractcatalog.ClassAuthoritative}, MinimumConfidence: map[contractcatalog.SourceClass]contractcatalog.Confidence{contractcatalog.ClassAuthoritative: contractcatalog.ConfidenceHigh}, RequiredCompleteness: contractcatalog.CompletenessComplete, Required: true}
		}
		policy, policyErr := contractcatalog.NewPolicy(contractcatalog.SufficiencyPolicy{ID: "workflow-approved-review", Version: contractcatalog.VersionV1, Claims: rules, ConflictTreatment: contractcatalog.ConflictDeny})
		if policyErr != nil {
			return nil, policyErr
		}
		selections := map[string][]contractcatalog.OperationKey{}
		for constraint, values := range config.ContractSelections {
			for _, value := range values {
				selections[constraint] = append(selections[constraint], contractcatalog.OperationKey{Method: strings.ToUpper(value.Method), Path: value.Path})
			}
		}
		contractReducer = validationworkflow.ApprovedReviewReducer{Authority: authority, Fallback: contractReducer, Policy: policy, CatalogLimits: contractcatalog.Limits{MaxSources: 32, MaxOperations: 4096, MaxClaims: 65536, MaxBytes: 2 << 20, MaxDepth: 64, MaxDiagnostics: 4096, MaxLocationBytes: 512}, ReductionLimits: contractcatalog.ReductionLimits{MaxBytes: 1 << 20, MaxOperations: 4096, MaxClaims: 65536, MaxDepth: 64, MaxDiagnostics: 4096}, ProjectionPolicy: projection, Selections: selections}
	}
	var fixtureReduced contractcatalog.ReducedContext
	if len(spec.Fixtures) > 0 {
		contractContext := validationworkflow.ContextWithContractAuthority(ctx, revisionContext)
		approvedRef, approvedErr := config.approvedContractReference()
		if approvedErr != nil {
			return nil, approvedErr
		}
		if approvedRef != nil {
			approvedReducer, ok := contractReducer.(validationworkflow.ApprovedContractReducer)
			if !ok {
				return nil, errors.New("fixture approved contract authority unavailable")
			}
			fixtureReduced, err = approvedReducer.ReduceApproved(contractContext, validationworkflow.StartCommand{Principal: current.Uid, ApprovedContract: approvedRef}, spec)
		} else {
			fixtureReduced, err = contractReducer.Reduce(contractContext, config.ContractPath, spec)
		}
		if err != nil {
			return nil, err
		}
	}
	trustedAdmission := validationworkflow.TrustedAdmissionBuilder{Specifications: workflowSpecLoader{path: config.SpecificationPath}, Contracts: contractReducer, Bindings: workflowBindingVerifier{config: config, binding: bindingContext}, Catalog: workflowCatalog{profile: config.RuntimeProfile, runtime: runtimeContext, descriptors: []agentplanning.CapabilityDescriptor{capability}}, Config: validationworkflow.AdmissionConfig{Provider: config.Provider, Model: config.Model, CompositionRef: compositionRef, Limits: agentplanning.Limits{MaxInputBytes: 1 << 20, MaxOutputBytes: 1 << 20, MaxConstraints: 128, MaxCapabilities: 16, MaxChecks: 384, MaxStepsPerCheck: 64, MaxAssertionsPerStep: 64, MaxMissingItems: 32, MaxValidationWork: 100000}}}
	var admission validationworkflow.AdmissionBuilder = trustedAdmission
	if options.githubAuthority != nil {
		admission = validationworkflow.GitHubRefreshingAdmission{Next: trustedAdmission, Authority: options.githubAuthority}
	}
	storeRoot := filepath.Join(root, ".featureproof", "workflows")
	var store validationworkflow.WorkflowStore
	var artifacts validationworkflow.PlanArtifactStore
	var executables validationworkflow.ExecutablePlanStore
	var approvalStore planapproval.ApprovalStore
	if options.persistence != nil {
		store, artifacts, executables, approvalStore = options.persistence.workflows, options.persistence.artifacts, options.persistence.executables, options.persistence.approvals
		if store == nil || artifacts == nil || executables == nil || approvalStore == nil {
			return nil, errors.New("postgres workflow persistence incomplete")
		}
	} else {
		localStore, openErr := validationworkflow.OpenLocalStore(storeRoot)
		if openErr != nil {
			return nil, openErr
		}
		localArtifacts, openErr := validationworkflow.OpenLocalArtifactStore(filepath.Join(storeRoot, "plans"))
		if openErr != nil {
			return nil, openErr
		}
		localExecutables, openErr := validationworkflow.OpenLocalExecutableStore(filepath.Join(storeRoot, "private"))
		if openErr != nil {
			return nil, openErr
		}
		localApprovals, openErr := planapproval.NewLocalStore(filepath.Join(storeRoot, "approval"))
		if openErr != nil {
			return nil, openErr
		}
		store, artifacts, executables, approvalStore = localStore, localArtifacts, localExecutables, localApprovals
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
	if environmentRuntime != nil {
		if err = environmentRuntime.attachWorkflow(store, wallClock); err != nil {
			return nil, err
		}
	}
	var fixturePort validationworkflow.FixtureLifecyclePort
	var fixtureState fixture.Store
	var auditSigner localvalidation.FixtureAuditSigner
	var auditTrust localvalidation.FixtureAuditTrust
	if len(spec.Fixtures) > 0 {
		var fixturePersistence interface {
			fixture.Store
			fixture.EvidenceSink
			fixture.Observer
		}
		if options.persistence != nil {
			fixturePersistence = options.persistence.fixtures
			if fixturePersistence == nil {
				return nil, errors.New("postgres fixture persistence unavailable")
			}
		}
		fixturePort, fixtureState, err = composeWorkflowFixtures(ctx, root, owner, spec, manifest, runtimeDeps, fixtureReduced, config, redaction, fixturePersistence)
		if err != nil {
			return nil, err
		}
		if options.persistence == nil {
			auditSigner, auditTrust, err = localvalidation.OpenFixtureAuditAuthority(filepath.Join(storeRoot, "private", "fixture-audit.ed25519"), filepath.Join(storeRoot, "trust", "fixture-audit.pin"))
			if err != nil {
				return nil, err
			}
		}
	}
	functionalPort, flowCatalog, err := composeWorkflowFunctionalFlow(root, storeRoot, owner, spec, manifest, config, runtimeDeps, redaction, targetDigest, runtimeDigest, fixtureState, options.persistence)
	if err != nil {
		return nil, err
	}
	var flowAuthority validationworkflow.FunctionalFlowAuthorityLifecyclePort
	if functionalPort != nil {
		var ok bool
		flowAuthority, ok = functionalPort.(validationworkflow.FunctionalFlowAuthorityLifecyclePort)
		if !ok {
			return nil, errors.New("functional target lifecycle unavailable")
		}
	}
	var memoryPort validationworkflow.ProjectMemoryPort
	var memoryObserver validationworkflow.ProjectMemoryObservationPort
	var contextWitness projectmemory.MonotonicWitness
	var contextWitnessStream string
	if projectMemoryConfigured() {
		if options.persistence != nil {
			return nil, errors.New("postgres project memory persistence unavailable")
		}
		memoryComposition, memoryErr := composeProjectMemory()
		if memoryErr != nil {
			return nil, memoryErr
		}
		memoryPackages := filepath.Join(storeRoot, "functional-flow", "packages")
		memoryComposition.Service.Evidence = workflowMemoryEvidence{project: os.Getenv("FEATUREPROOF_MEMORY_PROJECT"), repository: config.RepositoryID, packageRoot: memoryPackages}
		integration := workflowProjectMemory{service: memoryComposition.Service, authority: memoryComposition.HumanAuthority, observerPrincipal: memoryComposition.ObserverPrincipal, packages: memoryPackages}
		memoryPort, memoryObserver = integration, integration
		contextWitness, contextWitnessStream = memoryComposition.Witness, memoryComposition.ProjectID+"-context"
	}
	contextProfiles, err := contextassembly.DefaultProfiles()
	if err != nil {
		return nil, err
	}
	var contextStore contextassembly.Store
	if contextWitness == nil && os.Getenv("FEATUREPROOF_CONTEXT_WITNESS_STREAM") != "" {
		if os.Getenv("FEATUREPROOF_MEMORY_WITNESS_GUARANTEE") != "linearizable-durable-nonreplayable-v1" {
			return nil, errors.New("context witness guarantee unavailable")
		}
		contextWitness, err = projectmemory.NewHTTPMonotonicWitness(os.Getenv("FEATUREPROOF_MEMORY_WITNESS_URL"), os.Getenv("FEATUREPROOF_MEMORY_WITNESS_CREDENTIAL"), projectMemoryWitnessClient)
		if err != nil {
			return nil, err
		}
		contextWitnessStream = os.Getenv("FEATUREPROOF_CONTEXT_WITNESS_STREAM")
	}
	if contextWitness == nil && options.persistence == nil {
		return nil, errors.New("durable context witness required")
	}
	if contextWitness != nil {
		_, witnessErr := contextWitness.Read(ctx, contextWitnessStream)
		if errors.Is(witnessErr, projectmemory.ErrWitnessMissing) {
			if _, witnessErr = contextWitness.Bootstrap(ctx, contextWitnessStream); witnessErr != nil {
				if ctx.Err() != nil {
					return nil, workflowContextFailure(ctx)
				}
				return nil, errors.New("context witness bootstrap unavailable")
			}
		} else if witnessErr != nil {
			if ctx.Err() != nil {
				return nil, workflowContextFailure(ctx)
			}
			return nil, errors.New("context witness unavailable")
		}
	}
	if options.persistence != nil {
		contextStore = options.persistence.context
		if contextStore == nil {
			return nil, errors.New("postgres context persistence incomplete")
		}
	} else {
		contextStore, err = contextassembly.NewLocalStore(ctx, filepath.Join(storeRoot, "context"), contextWitnessStream, contextWitness)
		if err != nil {
			return nil, err
		}
	}
	githubContextSource, err := composeWorkflowGitHubContext(storeRoot, config.RepositoryID, config.GitHubContext, options.githubAuthority, wallClock, options.persistence != nil)
	if err != nil {
		return nil, err
	}
	contextRegistry := validationworkflow.CurrentContextProducerRegistry{Workflows: store, Admission: admission, GitHub: githubContextSource}
	contextPort := &validationworkflow.ContextAssemblyAdapter{Profiles: contextProfiles, Registry: contextRegistry, Secrets: contextassembly.NewCanaryGate(nil), Tokens: contextassembly.ByteTokenCounter{Selections: map[string]bool{config.Provider + "/" + config.Model: true}}, Store: contextStore, Observer: validationworkflow.ContextObserver{}, Clock: wallClock, GitHub: githubContextSource}
	service, err := validationworkflow.NewService(validationworkflow.Config{LeaseTTL: 10 * time.Minute, HeartbeatInterval: time.Minute, FinalizeTimeout: 10 * time.Second, ApprovalTTL: 24 * time.Hour}, validationworkflow.Dependencies{Admission: admission, Generation: validationworkflow.GeneratorAdapter{Generator: generator}, Preparation: validationworkflow.PreparationAdapter{Assertions: assertions, Policy: generationPolicy}, Approval: approval, Execution: execution, Packages: validationworkflow.LocalPackageReader{Artifacts: artifacts, AuditTrust: auditTrust}, FixturePackages: validationworkflow.LocalFixturePackageWriter{AuditSigner: auditSigner}, Authorization: workflowAutoPolicy{allowed: config.AllowDisabledApproval}, Finalizer: workflowFinalizer{}, Environment: environmentPort, Store: store, Artifacts: artifacts, Executables: executables, Clock: wallClock, Waiter: wallClock, IDs: externalagent.RandomIDs{}, Owner: owner, ContractReview: approvedAuthority, Fixtures: fixturePort, FunctionalFlow: functionalPort, FlowAuthority: flowAuthority, Memory: memoryPort, MemoryObserver: memoryObserver, Context: contextPort, TargetPlanning: workflowTargetPlanningBinder{spec: spec, manifest: manifest, deps: runtimeDeps}})
	if err != nil {
		return nil, err
	}
	if options.recoverPending {
		if err = service.Recover(ctx); err != nil {
			return nil, err
		}
	}
	var callerRuntime validationworkflow.CallerOwnedRuntime
	if environmentRuntime != nil {
		callerRuntime = environmentRuntime
	}
	cli := &validationworkflow.CLI{Service: service, Principal: current.Uid, AuthProfiles: map[string]string{config.AuthProfile: compositionRef}, DirectTargets: map[string]validationworkflow.DirectTarget{}, PreviewTargets: map[string]validationworkflow.PreviewTarget{}, FlowManifests: flowCatalog, FlowAliases: cloneWorkflowAliases(config.FunctionalFlowAliases), Runtime: callerRuntime}
	if config.TargetKind == "direct" {
		cli.DirectTargets[config.TargetAlias] = validationworkflow.DirectTarget{Alias: config.TargetAlias, Digest: targetDigest}
	} else {
		cli.PreviewTargets[config.TargetAlias] = validationworkflow.PreviewTarget{Alias: config.TargetAlias, Digest: targetDigest}
	}
	return cli, nil
}

// composeWorkflowGitHubContext confines trusted policy, read-only gateway and durable store to product composition.
func composeWorkflowGitHubContext(storeRoot, repository string, policy *githubcontext.Policy, authority validationworkflow.GitHubPullRequestAuthority, clock githubcontext.Clock, postgres ...bool) (validationworkflow.GitHubContextSource, error) {
	if policy == nil {
		return nil, nil
	}
	githubGateway, ok := authority.(githubcontext.Gateway)
	repositoryID, err := strconv.ParseInt(repository, 10, 64)
	if !ok || err != nil || repositoryID <= 0 || clock == nil {
		return nil, errors.New("github context composition unavailable")
	}
	sealed, err := githubcontext.SealPolicy(*policy)
	if err != nil || policy.Digest != sealed.Digest {
		return nil, errors.New("github context policy unavailable")
	}
	var store githubcontext.Store
	var observer githubcontext.Observer
	if len(postgres) > 0 && postgres[0] {
		store = githubcontext.NewMemoryStore()
		observer = workflowGitHubContextObserver{}
	} else {
		store, err = githubcontext.OpenLocalStore(filepath.Join(storeRoot, "github-context"))
		if err != nil {
			return nil, err
		}
		observer, err = githubcontext.OpenLocalRecordObserver(filepath.Join(storeRoot, "github-context-records"))
		if err != nil {
			return nil, err
		}
	}
	acquirer, err := githubcontext.NewAcquirer(githubcontext.Dependencies{Gateway: githubGateway, Secrets: githubcontext.NewCanaryGate(nil), Clock: clock, Store: store, Observer: observer})
	if err != nil {
		return nil, err
	}
	return validationworkflow.GitHubContextAdapter{Acquirer: acquirer, Policies: validationworkflow.StaticGitHubContextPolicies{repositoryID: sealed}}, nil
}

// workflowContextFailure preserves an owner context's sentinel cause while exposing a stable cancellation category.
func workflowContextFailure(ctx context.Context) error {
	category := contextassembly.CategoryCancelled
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		category = contextassembly.CategoryTimeout
	}
	cause := context.Cause(ctx)
	if cause == nil {
		cause = ctx.Err()
	}
	return &contextassembly.Error{Category: category, Stage: "composition", Path: "context", Cause: cause}
}

// projectMemoryConfigured enables workflow memory only when composition configuration is intentionally present.
func projectMemoryConfigured() bool {
	for _, name := range []string{"FEATUREPROOF_MEMORY_ROOT", "FEATUREPROOF_MEMORY_INTEGRITY_KEY", "FEATUREPROOF_MEMORY_PROJECT", "FEATUREPROOF_MEMORY_PRINCIPAL"} {
		if os.Getenv(name) != "" {
			return true
		}
	}
	return false
}

var _ agentplanning.ProjectMemoryReader = (*projectmemory.Service)(nil)

// cloneWorkflowAliases detaches trusted remote selections before exposing them to adapter composition.
func cloneWorkflowAliases(source map[string]string) map[string]string {
	out := make(map[string]string, len(source))
	for alias, artifactID := range source {
		out[alias] = artifactID
	}
	return out
}

// composeWorkflowFunctionalFlow derives exact local runtime authority from the same specification, contract selections, target, policy, and HTTP boundaries as the shared workflow.
func composeWorkflowFunctionalFlow(root, storeRoot, owner string, spec specification.Specification, manifest planmanifest.Manifest, config workflowConfig, deps localvalidation.RuntimeDependencies, redaction *evidence.Policy, targetDigest, runtimeDigest string, fixtureState fixture.Store, persistences ...*workflowPersistence) (validationworkflow.FunctionalFlowExecutionPort, validationworkflow.FunctionalFlowCatalog, error) {
	var persistence *workflowPersistence
	if len(persistences) > 0 {
		persistence = persistences[0]
	}
	contractRaw, err := json.Marshal(config.ContractSelections)
	if err != nil {
		return nil, nil, err
	}
	contractDigest := workflowDigest("functional-contract", string(contractRaw))
	operationEffects := map[string]bool{}
	operations := []functionalflow.OperationAuthority{}
	identityOperations := []testidentity.Operation{}
	seen := map[functionalflow.OperationReference]bool{}
	for _, selections := range config.ContractSelections {
		for _, selection := range selections {
			method := strings.ToUpper(selection.Method)
			reference := functionalflow.OperationReference{Capability: "http.request", TargetID: spec.Target.ID, Method: method, Path: selection.Path, ContractDigest: contractDigest}
			if seen[reference] {
				continue
			}
			seen[reference] = true
			effectful := method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
			idempotent := !effectful || method == http.MethodPut || method == http.MethodDelete
			operations = append(operations, functionalflow.OperationAuthority{Reference: reference, Effectful: effectful, Idempotent: idempotent})
			operationEffects[method+"\x00"+selection.Path] = effectful
			identityOperations = append(identityOperations, testidentity.Operation{Capability: reference.Capability, TargetID: reference.TargetID, Method: reference.Method, Path: reference.Path})
		}
	}
	if len(operations) == 0 {
		return nil, nil, nil
	}
	identitySpec := spec
	identitySpec.Target.BaseURL = workflowProvisionalPlanningTarget
	_, identityCatalog, err := localvalidation.BuildFixtureHTTPRuntime(identitySpec, manifest, deps)
	if err != nil {
		return nil, nil, err
	}
	descriptors := identityCatalog.DescriptorsFor(identityOperations)
	const publicIdentity = "featureproof-public"
	descriptors = append(descriptors, testidentity.Descriptor{ID: publicIdentity, Mechanism: testidentity.MechanismAnonymous, Operations: append([]testidentity.Operation(nil), identityOperations...)})
	identitiesDigest, err := testidentity.DescriptorDigest(descriptors)
	if err != nil {
		return nil, nil, err
	}
	constraints := map[string]bool{}
	for _, constraint := range spec.Constraints {
		constraints[constraint.ID] = true
	}
	captures := map[string]planning.ValueType{}
	for _, declaration := range spec.Fixtures {
		for _, capture := range declaration.Captures {
			captures[declaration.ID+"\x00"+capture.Name] = planning.ValueType(capture.ValueType)
		}
	}
	limits := functionalflow.Limits{MaxDocumentBytes: 1 << 20, MaxSteps: max(1, spec.Limits.MaxSteps), MaxAssertions: max(1, spec.Limits.MaxSteps*64), MaxComparisons: max(1, spec.Limits.MaxSteps), MaxReferences: max(1, spec.Limits.MaxSteps*8), MaxDepth: 64, MaxResolvedBytes: 1 << 20, MaxToolCalls: max(1, spec.Limits.MaxToolCalls), MaxRetries: max(0, spec.Limits.MaxRetries), MaxWork: max(100, spec.Limits.MaxSteps*32), MaxRevisions: 3, StepTimeout: manifest.HTTP.Timeout() + time.Second, RunTimeout: time.Duration(max(1, spec.Limits.MaxSteps))*(manifest.HTTP.Timeout()+time.Second) + 30*time.Second}
	bindings := functionalflow.ArtifactBindings{Specification: workflowDigest("specification", config.SpecificationPath, config.CommitOID), Revision: workflowDigest("revision", config.RepositoryID, config.CommitOID), Target: targetDigest, Contract: contractDigest, Identities: identitiesDigest, Policy: workflowDigest("policy", config.PolicyID), Fixtures: workflowDigest("fixtures", config.RepositoryID, config.EnvironmentID), Capabilities: runtimeDigest, Limits: workflowDigest("functional-limits", strconv.Itoa(limits.MaxSteps), strconv.Itoa(limits.MaxToolCalls), strconv.Itoa(limits.MaxRetries))}
	fixtureResolver := functionalflow.FixtureCaptureStore(nil)
	if fixtureState != nil {
		fixtureResolver = validationworkflow.FunctionalFixtureCaptureAdapter{Store: fixtureState}
	}
	confirmation := 10 * time.Second
	executionConfig := functionalflow.ExecutionConfig{Owner: owner, MaxCalls: limits.MaxToolCalls, MaxRetries: limits.MaxRetries, MaxWork: limits.MaxWork, StepTimeout: limits.StepTimeout, RunTimeout: limits.RunTimeout, ConfirmationTimeout: confirmation, RetryDelay: 100 * time.Millisecond}
	leaseTTL := time.Duration(executionConfig.MaxRetries+1)*executionConfig.StepTimeout + time.Duration(executionConfig.MaxRetries)*executionConfig.RetryDelay + executionConfig.ConfirmationTimeout + time.Minute
	targetTool := &workflowTargetHTTPTool{spec: spec, manifest: manifest, deps: deps, tools: map[string]harness.Tool{}, digests: map[string]string{}}
	functionalConfig := validationworkflow.LocalFunctionalFlowRuntimeConfig{Root: filepath.Join(storeRoot, "functional-flow"), PublicIdentity: publicIdentity, AttemptJournalRoot: filepath.Join(root, ".featureproof", "runs"), AllowedManifestRoots: []string{root}, Aliases: config.FunctionalFlowAliases, Limits: limits, Validation: functionalflow.ValidationAuthority{Constraints: constraints, FixtureCaptures: captures}, Binding: functionalflow.BindingAuthority{Operations: operations, Identities: descriptors, Bindings: bindings}, Generated: functionalflow.GeneratedAuthority{ContextDigest: workflowDigest("functional-context", runtimeDigest, targetDigest), TargetID: spec.Target.ID, ContractDigest: contractDigest, Effects: map[string]bool{"http.request": true}, OperationEffects: operationEffects}, Execution: executionConfig, LeaseTTL: leaseTTL, Tool: targetTool, PolicyFactory: deps.PolicyFactory, Sanitizer: evidence.NewSanitizer(redaction), Evaluator: deps.Evaluator, Identities: identityCatalog, Clock: deps.Clock, IDs: deps.IDs, Waiter: deps.Waiter, Fixtures: fixtureResolver}
	if persistence != nil {
		functionalConfig.State, functionalConfig.Journal = persistence.functional, persistence.journal
		if persistence.functionalRuntime == nil {
			return nil, nil, errors.New("functional runtime persistence unavailable")
		}
		functionalConfig.Artifacts, functionalConfig.Evidence, functionalConfig.Publisher, functionalConfig.Catalog, functionalConfig.AttemptSink, err = persistence.functionalRuntime.FunctionalRuntimeStores(config.FunctionalFlowAliases, limits, persistence.journal)
		if err != nil {
			return nil, nil, err
		}
		manifestSource, ok := functionalConfig.Evidence.(validationworkflow.FunctionalFlowEvidenceManifestSource)
		if !ok {
			return nil, nil, errors.New("functional evidence manifest persistence unavailable")
		}
		functionalConfig.EvidenceManifest = manifestSource
	}
	runtime, catalog, err := validationworkflow.ComposeLocalFunctionalFlowRuntime(functionalConfig)
	if err != nil {
		return nil, nil, err
	}
	canonicalComposer, ok := runtime.Composer.(validationworkflow.CanonicalFunctionalFlowComposer)
	if !ok {
		return nil, nil, errors.New("functional target composition unavailable")
	}
	runtime.Composer = workflowTargetFunctionalFlowComposer{inner: canonicalComposer, runtimeDigest: runtimeDigest}
	return workflowFunctionalFlowTargetPort{inner: runtime, tool: targetTool}, catalog, nil
}

// composeWorkflowFixtures builds one consumer-owned lifecycle shared by every productive workflow surface.
func composeWorkflowFixtures(shutdown context.Context, root, owner string, spec specification.Specification, manifest planmanifest.Manifest, deps localvalidation.RuntimeDependencies, reduced contractcatalog.ReducedContext, config workflowConfig, policy *evidence.Policy, persistences ...interface {
	fixture.Store
	fixture.EvidenceSink
	fixture.Observer
}) (validationworkflow.FixtureLifecyclePort, fixture.Store, error) {
	if shutdown == nil {
		return nil, nil, errors.New("fixture shutdown scope unavailable")
	}
	if err := config.FixtureRuntime.validate(); err != nil {
		return nil, nil, err
	}
	runtimeLimits := *config.FixtureRuntime
	set, err := fixture.ValidateSet(spec.Fixtures)
	if err != nil {
		return nil, nil, err
	}
	httpTool, identities, err := localvalidation.BuildFixtureHTTPRuntime(spec, manifest, deps)
	if err != nil {
		return nil, nil, err
	}
	redactor := evidence.NewSanitizer(policy)
	for _, declaration := range spec.Fixtures {
		for _, capture := range declaration.Captures {
			selector := "/body"
			for _, segment := range capture.Path {
				if segment.Field != nil {
					selector += "/" + strings.ReplaceAll(strings.ReplaceAll(*segment.Field, "~", "~0"), "/", "~1")
				} else if segment.Index != nil {
					selector += "/" + strconv.Itoa(*segment.Index)
				}
			}
			if policy.OverlapsSensitive(selector) {
				return nil, nil, errors.New("fixture capture overlaps sensitive evidence")
			}
		}
	}
	sanitizer := fixture.SanitizerFunc(func(ctx context.Context, value any) (any, error) {
		safe, sanitizeErr := redactor.Sanitize(ctx, value)
		if sanitizeErr != nil {
			return nil, sanitizeErr
		}
		return safe.Value(), nil
	})
	fixtureRoot := filepath.Join(root, ".featureproof", "workflows", "fixtures")
	var stateStore fixture.Store
	var evidenceSink fixture.EvidenceSink
	var observer fixture.Observer
	if len(persistences) > 0 && persistences[0] != nil {
		stateStore, evidenceSink, observer = persistences[0], workflowSanitizedFixtureEvidence{next: persistences[0], sanitizer: sanitizer}, persistences[0]
	} else {
		stateStore, err = fixture.NewFileStore(filepath.Join(fixtureRoot, "state"))
		if err != nil {
			return nil, nil, err
		}
		evidenceSink = fixture.FileEvidenceSink{Root: filepath.Join(fixtureRoot, "evidence"), Sanitizer: sanitizer}
		observer = &fixture.FileObserver{Path: filepath.Join(fixtureRoot, "journal.jsonl")}
	}
	executor, err := fixture.NewHTTPAdapter(fixture.HTTPAdapterConfig{Tool: httpTool, Evidence: evidenceSink, IdempotencyHeader: workflowFixtureIdempotencyHeader(manifest)})
	if err != nil {
		return nil, nil, err
	}
	guard := &fixture.ProductRevalidator{PolicyFactory: deps.PolicyFactory, Limits: harness.Limits{MaxSteps: 0, MaxToolCalls: runtimeLimits.MaxToolCalls, MaxRetries: runtimeLimits.MaxRetries}}
	lifecycle, err := fixture.NewLifecycle(stateStore, executor, guard, sanitizer, observer, fixture.ContextWaiter{}, fixture.DigestKeys{Namespace: config.RepositoryID + ":" + config.EnvironmentID}, fixture.FiniteScopes{Parent: shutdown, Timeout: time.Duration(runtimeLimits.CleanupTimeoutMS) * time.Millisecond, MaxCalls: runtimeLimits.CleanupMaxCalls, MaxRetries: runtimeLimits.CleanupRetries}, fixture.Config{MaxCaptureBytes: runtimeLimits.MaxCaptureBytes, MaxToolCalls: runtimeLimits.MaxToolCalls, MaxRetries: runtimeLimits.MaxRetries, CreateRetries: runtimeLimits.CreateRetries, RetryBackoff: time.Duration(runtimeLimits.RetryBackoffMS) * time.Millisecond, Owner: owner, LeaseTTL: time.Duration(runtimeLimits.LeaseTTLMS) * time.Millisecond, Clock: fixture.WallClock{}})
	if err != nil {
		return nil, nil, err
	}
	authority := fixture.ReducedAuthority{Set: set, TargetID: spec.Target.ID, RepositoryID: config.RepositoryID, Revision: config.CommitOID, Contract: reduced, Identities: identities, Idempotency: fixture.ContractIdempotencyPolicy{}}
	return fixture.WorkflowAdapter{Lifecycle: lifecycle, Authority: authority}, stateStore, nil
}

// workflowSanitizedFixtureEvidence applies composition-owned redaction before
// PostgreSQL receives bounded fixture response fields.
type workflowSanitizedFixtureEvidence struct {
	next      fixture.EvidenceSink
	sanitizer fixture.Sanitizer
}

// Confirm sanitizes one fixture response and delegates its durable confirmation.
func (sink workflowSanitizedFixtureEvidence) Confirm(ctx context.Context, runID, binding string, value map[string]any) (map[string]any, string, error) {
	safe, err := sink.sanitizer.Sanitize(ctx, value)
	if err != nil {
		return nil, "", err
	}
	projected, ok := safe.(map[string]any)
	if !ok {
		return nil, "", errors.New("fixture evidence projection unavailable")
	}
	return sink.next.Confirm(ctx, runID, binding, projected)
}

// workflowFixtureIdempotencyHeader selects the system-owned key header only when trusted HTTP policy explicitly permits it.
func workflowFixtureIdempotencyHeader(manifest planmanifest.Manifest) string {
	for _, header := range manifest.HTTP.AllowedRequestHeaders {
		if strings.EqualFold(header, "Idempotency-Key") {
			return "Idempotency-Key"
		}
	}
	return ""
}

// workflowContractReducerFor is the neutral composition point reused by CLI, API, MCP and GitHub through the shared service.
// feature22:coverage workflowContractReducerFor
func workflowContractReducerFor(config workflowConfig, projection contractcatalog.ProjectionPolicy) validationworkflow.ContractReducer {
	if config.ContractSource == goinspector.SourceKind {
		return workflowGoContractReducer{root: config.ContractPath, repositoryID: config.RepositoryID, revision: config.CommitOID, config: *config.GoInspector, selections: config.ContractSelections, projection: projection}
	}
	if config.ContractSource == springinspector.SourceKind {
		return workflowSpringContractReducer{root: config.RepositoryRoot, authorizedRef: config.ContractPath, repositoryID: config.RepositoryID, revision: config.CommitOID, config: *config.SpringInspector, selections: config.ContractSelections, projection: projection}
	}
	if config.ContractSource == "combined" {
		goConfig := workflowGoInspectorConfig{}
		if config.GoInspector != nil {
			goConfig = *config.GoInspector
		}
		return workflowCombinedContractReducer{openAPIPath: config.OpenAPIContractPath, root: config.RepositoryRoot, authorizedRef: config.ContractPath, repositoryID: config.RepositoryID, revision: config.CommitOID, config: goConfig, spring: config.SpringInspector, sources: append([]string(nil), config.ContractSources...), selections: config.ContractSelections, projection: projection}
	}
	return workflowContractReducer{path: config.ContractPath, selections: config.ContractSelections, projection: projection}
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
type workflowContractReducer struct {
	path       string
	selections map[string][]workflowOperationSelection
	projection contractcatalog.ProjectionPolicy
}

// Reduce builds a bounded per-constraint operation context from the configured contract.
func (r workflowContractReducer) Reduce(ctx context.Context, ref string, spec specification.Specification) (contractcatalog.ReducedContext, error) {
	if ref != r.path {
		return contractcatalog.ReducedContext{}, errors.New("unauthorized contract")
	}
	root := filepath.Dir(r.path)
	loader, err := openapipkg.NewLoader(ctx, openapipkg.Config{AuthorizedRoot: root, MaxDocumentBytes: 2 << 20, MaxTotalBytes: 4 << 20, MaxDocuments: 32, MaxReferences: 512, MaxReferenceDepth: 16, MaxObservedBodyBytes: 1 << 20, MaxContextBytes: 1 << 20}, openapipkg.LocalFS{})
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	contract, err := loader.Load(ctx, filepath.Base(r.path))
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	openAPICatalog := contract.Catalog()
	rawCatalog, err := json.Marshal(openAPICatalog)
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	sum := sha256.Sum256(rawCatalog)
	sourceDigest := hex.EncodeToString(sum[:])
	limits := contractcatalog.Limits{MaxSources: 1, MaxOperations: 4096, MaxClaims: 65536, MaxBytes: 2 << 20, MaxDepth: 64, MaxDiagnostics: 4096, MaxLocationBytes: 256}
	registry, err := contractcatalog.NewRegistry(map[string]contractcatalog.ContractSource{"openapi": openapipkg.ContractSourceAdapter{AdapterVersion: "openapi-v1"}})
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	loaded, err := registry.Load(ctx, []contractcatalog.SourceRequest{contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: "openapi", ID: "configured-openapi", Class: contractcatalog.ClassAuthoritative, Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete, ExpectedDigest: sourceDigest, AdapterVersion: "openapi-v1", Input: contract, Limits: limits, Projection: r.projection})}, limits)
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	catalog, err := contractcatalog.Build(ctx, loaded, limits)
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	rules := map[contractcatalog.ClaimKind]contractcatalog.ClaimRule{}
	for _, kind := range []contractcatalog.ClaimKind{contractcatalog.ClaimOperationIdentity, contractcatalog.ClaimSchema, contractcatalog.ClaimSecurity, contractcatalog.ClaimResponse} {
		rules[kind] = contractcatalog.ClaimRule{AllowedClasses: []contractcatalog.SourceClass{contractcatalog.ClassAuthoritative}, MinimumConfidence: map[contractcatalog.SourceClass]contractcatalog.Confidence{contractcatalog.ClassAuthoritative: contractcatalog.ConfidenceHigh}, RequiredCompleteness: contractcatalog.CompletenessComplete, Required: true}
	}
	policy, err := contractcatalog.NewPolicy(contractcatalog.SufficiencyPolicy{ID: "workflow-openapi", Version: contractcatalog.VersionV1, Claims: rules, ConflictTreatment: contractcatalog.ConflictDeny})
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	selections := make([]contractcatalog.ConstraintSelection, len(spec.Constraints))
	for i, constraint := range spec.Constraints {
		selections[i].ConstraintID = constraint.ID
		for _, operation := range r.selections[constraint.ID] {
			selections[i].Operations = append(selections[i].Operations, contractcatalog.OperationKey{Method: strings.ToUpper(operation.Method), Path: operation.Path})
		}
	}
	return contractcatalog.BuildContext(ctx, catalog, policy, selections, contractcatalog.ReductionLimits{MaxBytes: 1 << 20, MaxOperations: 4096, MaxClaims: 65536, MaxDepth: 64, MaxDiagnostics: 4096})
}

// workflowGoContractReducer freezes one authorized checkout and admits only policy-eligible derived operations.
type workflowGoContractReducer struct {
	root         string
	repositoryID string
	revision     string
	config       workflowGoInspectorConfig
	selections   map[string][]workflowOperationSelection
	projection   contractcatalog.ProjectionPolicy
}

// workflowCombinedContractReducer admits authoritative OpenAPI and derived Go facts into one conflict-aware catalog.
type workflowCombinedContractReducer struct {
	openAPIPath, root, authorizedRef, repositoryID, revision string
	config                                                   workflowGoInspectorConfig
	spring                                                   *workflowSpringInspectorConfig
	sources                                                  []string
	selections                                               map[string][]workflowOperationSelection
	projection                                               contractcatalog.ProjectionPolicy
}

// Reduce freezes both authorized sources before a single registry, catalog, policy, and reduction pass.
// feature22:coverage workflowCombinedContractReducer.Reduce
func (r workflowCombinedContractReducer) Reduce(ctx context.Context, ref string, spec specification.Specification) (contractcatalog.ReducedContext, error) {
	if ref != r.authorizedRef {
		return contractcatalog.ReducedContext{}, errors.New("unauthorized contract")
	}
	sources := append([]string(nil), r.sources...)
	if len(sources) == 0 {
		sources = []string{"openapi", goinspector.SourceKind}
		if r.spring != nil {
			sources = append(sources, springinspector.SourceKind)
		}
	}
	if !validContractSources(sources) || (containsContractSource(sources, "openapi") && r.openAPIPath == "") || (containsContractSource(sources, springinspector.SourceKind) && r.spring == nil) {
		return contractcatalog.ReducedContext{}, errors.New("contract source drift")
	}
	duration := r.config.Limits.MaxDuration
	if !containsContractSource(sources, goinspector.SourceKind) && r.spring != nil {
		duration = r.spring.Limits.MaxDuration
	}
	if duration <= 0 {
		return contractcatalog.ReducedContext{}, errors.New("contract source limits unavailable")
	}
	bounded, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	effective := r.config
	if authority, ok := validationworkflow.ContractAuthority(ctx); ok {
		if authority.RepositoryID != r.repositoryID || authority.CommitOID != r.revision {
			return contractcatalog.ReducedContext{}, errors.New("contract authority drift")
		}
		effective.Roots = append([]string{}, authority.Roots...)
		effective.ChangedPaths = append([]string{}, authority.ChangedPaths...)
	}
	maxSources := len(sources)
	adapters := map[string]contractcatalog.ContractSource{}
	requests := []contractcatalog.SourceRequest{}
	limits := contractcatalog.Limits{MaxSources: maxSources, MaxOperations: maxInt(r.config.Limits.MaxOperations, 4096), MaxClaims: maxInt(r.config.Limits.MaxOperations*128, 65536), MaxBytes: maxInt(r.config.Limits.MaxBytes, 2<<20), MaxDepth: maxInt(r.config.Limits.MaxDepth, 64), MaxDiagnostics: maxInt(r.config.Limits.MaxDiagnostics, 4096), MaxLocationBytes: 1024}
	perSource := contractcatalog.Limits{MaxSources: 1, MaxOperations: limits.MaxOperations / maxSources, MaxClaims: limits.MaxClaims / maxSources, MaxBytes: limits.MaxBytes / maxSources, MaxDepth: limits.MaxDepth / maxSources, MaxDiagnostics: limits.MaxDiagnostics / maxSources, MaxLocationBytes: limits.MaxLocationBytes}
	if containsContractSource(sources, "openapi") {
		loader, err := openapipkg.NewLoader(bounded, openapipkg.Config{AuthorizedRoot: filepath.Dir(r.openAPIPath), MaxDocumentBytes: 2 << 20, MaxTotalBytes: 4 << 20, MaxDocuments: 32, MaxReferences: 512, MaxReferenceDepth: 16, MaxObservedBodyBytes: 1 << 20, MaxContextBytes: 1 << 20}, openapipkg.LocalFS{})
		if err != nil {
			return contractcatalog.ReducedContext{}, err
		}
		contract, err := loader.Load(bounded, filepath.Base(r.openAPIPath))
		if err != nil {
			return contractcatalog.ReducedContext{}, err
		}
		raw, err := json.Marshal(contract.Catalog())
		if err != nil {
			return contractcatalog.ReducedContext{}, err
		}
		digest := sha256.Sum256(raw)
		adapters["openapi"] = openapipkg.ContractSourceAdapter{AdapterVersion: "openapi-v1"}
		requests = append(requests, contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: "openapi", ID: "configured-openapi", Class: contractcatalog.ClassAuthoritative, Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete, ExpectedDigest: hex.EncodeToString(digest[:]), AdapterVersion: "openapi-v1", Input: contract, Limits: perSource, Projection: r.projection}))
	}
	if containsContractSource(sources, goinspector.SourceKind) {
		input, err := goinspector.FreezeConfiguredRepository(bounded, goinspector.LocalRepositoryFS{}, r.root, goinspector.Configuration{RepositoryID: r.repositoryID, Revision: r.revision, Roots: append([]string{}, effective.Roots...), ChangedPaths: append([]string{}, effective.ChangedPaths...), Build: goinspector.BuildContext{GOOS: r.config.GOOS, GOARCH: r.config.GOARCH, Tags: append([]string{}, r.config.BuildTags...)}, Auth: append([]goinspector.AuthMatcher{}, r.config.Auth...), Limits: r.config.Limits, Confidence: contractcatalog.ConfidenceMedium, Completeness: contractcatalog.CompletenessPartial, IncludeGenerated: r.config.IncludeGenerated, EntrySymbols: append([]string{}, r.config.EntrySymbols...), Projection: r.projection})
		if err != nil {
			return contractcatalog.ReducedContext{}, err
		}
		adapters[goinspector.SourceKind] = goinspector.DerivedSource{}
		requests = append(requests, goinspector.SourceRequest("configured-go", input, perSource, r.projection))
	}
	if containsContractSource(sources, springinspector.SourceKind) {
		springConfig := *r.spring
		changed := append([]string(nil), springConfig.ChangedPaths...)
		if authority, ok := validationworkflow.ContractAuthority(ctx); ok {
			changed = changed[:0]
			for _, candidate := range authority.ChangedPaths {
				for _, set := range springConfig.SourceSets {
					if candidate == set.Root || strings.HasPrefix(candidate, strings.TrimSuffix(set.Root, "/")+"/") {
						changed = append(changed, candidate)
						break
					}
				}
			}
		}
		springInput, err := springinspector.FreezeConfiguredRepository(bounded, workflowSpringRepository{}, r.root, springinspector.Configuration{RepositoryID: r.repositoryID, Revision: r.revision, RootIdentity: springConfig.RootIdentity, FrameworkIdentities: append([]string(nil), springConfig.FrameworkIdentities...), SourceSets: append([]springinspector.SourceSet(nil), springConfig.SourceSets...), ChangedPaths: changed, JavaRelease: springConfig.JavaRelease, Generated: springConfig.Generated, Limits: springConfig.Limits, Confidence: contractcatalog.ConfidenceMedium, Completeness: contractcatalog.CompletenessPartial, Projection: r.projection})
		if err != nil {
			return contractcatalog.ReducedContext{}, err
		}
		adapters[springinspector.SourceKind] = springinspector.DerivedSource{}
		requests = append(requests, springinspector.SourceRequest("configured-spring", springInput, perSource, r.projection))
	}
	registry, err := contractcatalog.NewRegistry(adapters)
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	loaded, err := registry.Load(bounded, requests, limits)
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	catalog, err := contractcatalog.Build(bounded, loaded, limits)
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	rules := map[contractcatalog.ClaimKind]contractcatalog.ClaimRule{}
	for _, kind := range []contractcatalog.ClaimKind{contractcatalog.ClaimOperationIdentity, contractcatalog.ClaimSchema, contractcatalog.ClaimSecurity, contractcatalog.ClaimResponse} {
		rules[kind] = contractcatalog.ClaimRule{AllowedClasses: []contractcatalog.SourceClass{contractcatalog.ClassAuthoritative, contractcatalog.ClassDerived}, MinimumConfidence: map[contractcatalog.SourceClass]contractcatalog.Confidence{contractcatalog.ClassAuthoritative: contractcatalog.ConfidenceHigh, contractcatalog.ClassDerived: contractcatalog.ConfidenceMedium}, RequiredCompleteness: contractcatalog.CompletenessPartial, Required: true}
	}
	policy, err := contractcatalog.NewPolicy(contractcatalog.SufficiencyPolicy{ID: "workflow-combined-v1", Version: contractcatalog.VersionV1, Claims: rules, ConflictTreatment: contractcatalog.ConflictDeny})
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	selections := workflowSelections(spec, r.selections)
	return contractcatalog.BuildContext(bounded, catalog, policy, selections, contractcatalog.ReductionLimits{MaxBytes: 1 << 20, MaxOperations: limits.MaxOperations, MaxClaims: limits.MaxClaims, MaxDepth: limits.MaxDepth, MaxDiagnostics: limits.MaxDiagnostics})
}

// maxInt selects a finite aggregate budget without introducing an implicit source configuration.
func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

// workflowSelections converts trusted constraint mappings into source-neutral operation keys.
// feature22:coverage workflowSelections
func workflowSelections(spec specification.Specification, configured map[string][]workflowOperationSelection) []contractcatalog.ConstraintSelection {
	selections := make([]contractcatalog.ConstraintSelection, len(spec.Constraints))
	for index, constraint := range spec.Constraints {
		selections[index].ConstraintID = constraint.ID
		for _, operation := range configured[constraint.ID] {
			selections[index].Operations = append(selections[index].Operations, contractcatalog.OperationKey{Method: strings.ToUpper(operation.Method), Path: operation.Path})
		}
	}
	return selections
}

// Reduce binds repository bytes, revision, inspection settings, catalog, and sufficiency policy before model context exists.
// feature22:coverage workflowGoContractReducer.Reduce
func (r workflowGoContractReducer) Reduce(ctx context.Context, ref string, spec specification.Specification) (contractcatalog.ReducedContext, error) {
	if ref != r.root {
		return contractcatalog.ReducedContext{}, errors.New("unauthorized contract")
	}
	bounded, cancel := context.WithTimeout(ctx, r.config.Limits.MaxDuration)
	defer cancel()
	effective := r.config
	if authority, ok := validationworkflow.ContractAuthority(ctx); ok {
		if authority.RepositoryID != r.repositoryID || authority.CommitOID != r.revision {
			return contractcatalog.ReducedContext{}, errors.New("contract authority drift")
		}
		effective.Roots = append([]string{}, authority.Roots...)
		effective.ChangedPaths = append([]string{}, authority.ChangedPaths...)
	}
	input, err := goinspector.FreezeConfiguredRepository(bounded, goinspector.LocalRepositoryFS{}, r.root, goinspector.Configuration{RepositoryID: r.repositoryID, Revision: r.revision, Roots: append([]string{}, effective.Roots...), ChangedPaths: append([]string{}, effective.ChangedPaths...), Build: goinspector.BuildContext{GOOS: r.config.GOOS, GOARCH: r.config.GOARCH, Tags: append([]string{}, r.config.BuildTags...)}, Auth: append([]goinspector.AuthMatcher{}, r.config.Auth...), Limits: r.config.Limits, Confidence: contractcatalog.ConfidenceMedium, Completeness: contractcatalog.CompletenessPartial, IncludeGenerated: r.config.IncludeGenerated, EntrySymbols: append([]string{}, r.config.EntrySymbols...), Projection: r.projection})
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	limits := contractcatalog.Limits{MaxSources: 1, MaxOperations: r.config.Limits.MaxOperations, MaxClaims: r.config.Limits.MaxOperations * 128, MaxBytes: r.config.Limits.MaxBytes, MaxDepth: r.config.Limits.MaxDepth + 16, MaxDiagnostics: r.config.Limits.MaxDiagnostics, MaxLocationBytes: 512}
	registry, err := contractcatalog.NewRegistry(map[string]contractcatalog.ContractSource{goinspector.SourceKind: goinspector.DerivedSource{}})
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	loaded, err := registry.Load(bounded, []contractcatalog.SourceRequest{goinspector.SourceRequest("configured-go", input, limits, r.projection)}, limits)
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	catalog, err := contractcatalog.Build(bounded, loaded, limits)
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	rules := map[contractcatalog.ClaimKind]contractcatalog.ClaimRule{}
	for _, kind := range []contractcatalog.ClaimKind{contractcatalog.ClaimOperationIdentity, contractcatalog.ClaimSchema, contractcatalog.ClaimSecurity, contractcatalog.ClaimResponse} {
		rules[kind] = contractcatalog.ClaimRule{AllowedClasses: []contractcatalog.SourceClass{contractcatalog.ClassDerived}, MinimumConfidence: map[contractcatalog.SourceClass]contractcatalog.Confidence{contractcatalog.ClassDerived: contractcatalog.ConfidenceMedium}, RequiredCompleteness: contractcatalog.CompletenessPartial, Required: true}
	}
	policy, err := contractcatalog.NewPolicy(contractcatalog.SufficiencyPolicy{ID: "workflow-go-static-v1", Version: contractcatalog.VersionV1, Claims: rules, ConflictTreatment: contractcatalog.ConflictDeny})
	if err != nil {
		return contractcatalog.ReducedContext{}, err
	}
	selections := make([]contractcatalog.ConstraintSelection, len(spec.Constraints))
	for index, constraint := range spec.Constraints {
		selections[index].ConstraintID = constraint.ID
		for _, operation := range r.selections[constraint.ID] {
			selections[index].Operations = append(selections[index].Operations, contractcatalog.OperationKey{Method: strings.ToUpper(operation.Method), Path: operation.Path})
		}
	}
	return contractcatalog.BuildContext(bounded, catalog, policy, selections, contractcatalog.ReductionLimits{MaxBytes: 1 << 20, MaxOperations: r.config.Limits.MaxOperations, MaxClaims: limits.MaxClaims, MaxDepth: limits.MaxDepth, MaxDiagnostics: r.config.Limits.MaxDiagnostics})
}

// workflowBindingVerifier checks exact configured source and deployment identity without exposing credentials.
type workflowBindingVerifier struct {
	config  workflowConfig
	binding validationworkflow.BindingContext
}

// ContractAuthority returns roots and changed paths only from trusted workflow composition before contract loading.
// feature22:coverage workflowBindingVerifier.ContractAuthority
func (v workflowBindingVerifier) ContractAuthority(ctx context.Context, command validationworkflow.StartCommand) (validationworkflow.RevisionContext, error) {
	githubAuthority, github := validationworkflow.GitHubContractAuthority(ctx)
	return contractAuthorityFor(v.config, v.binding, command, githubAuthority, github)
}

// contractAuthorityFor validates either sealed GitHub authority or the configured local revision and always supplies trusted roots.
// feature22:coverage contractAuthorityFor
func contractAuthorityFor(config workflowConfig, binding validationworkflow.BindingContext, command validationworkflow.StartCommand, githubAuthority validationworkflow.RevisionContext, github bool) (validationworkflow.RevisionContext, error) {
	if github {
		if githubAuthority.RepositoryID != command.RepositoryID || githubAuthority.CommitOID != command.Revision {
			return validationworkflow.RevisionContext{}, errors.New("revision mismatch")
		}
		githubAuthority.Roots = append([]string{}, binding.Revision.Roots...)
		return githubAuthority, nil
	}
	if command.RepositoryID != config.RepositoryID || command.Revision != config.CommitOID {
		return validationworkflow.RevisionContext{}, errors.New("revision mismatch")
	}
	return binding.Revision, nil
}

// Verify rejects any source, revision or target drift and returns the effective safe objects.
// feature22:coverage workflowBindingVerifier.Verify
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
// feature22:coverage workflowCatalog.Descriptors
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

// Sanitize normalizes typed approval values into JSON data and returns only the treated projection while keeping truncation metadata private to composition.
func (s workflowApprovalSanitizer) Sanitize(ctx context.Context, value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var compatible any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err = decoder.Decode(&compatible); err != nil {
		return nil, err
	}
	treated, err := s.inner.Sanitize(ctx, compatible)
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

// composeWorkflowEnvironment builds the sealed catalog, durable lifecycle and sole workflow environment port.
func composeWorkflowEnvironment(root string, config workflowConfig, target validationworkflow.TargetContext, preview *workflowPreviewPort, managedProviders map[string]environment.EnvironmentProvider) (validationworkflow.EnvironmentPort, error) {
	port, _, err := composeWorkflowEnvironmentWithRuntime(root, config, target, preview, managedProviders, nil)
	return port, err
}

// composeWorkflowEnvironmentWithRuntime opens one shared durable capacity authority for lifecycle and cleanup composition.
func composeWorkflowEnvironmentWithRuntime(root string, config workflowConfig, target validationworkflow.TargetContext, preview *workflowPreviewPort, managedProviders map[string]environment.EnvironmentProvider, persistences ...*workflowPersistence) (validationworkflow.EnvironmentPort, *workflowEnvironmentRuntime, error) {
	var persistence *workflowPersistence
	if len(persistences) > 0 {
		persistence = persistences[0]
	}
	selection, err := config.EnvironmentCatalog.Resolve(config.TargetAlias)
	if err != nil {
		return nil, nil, err
	}
	if selection.ProviderID != target.ProviderID || selection.TemplateReference != target.EnvironmentRef || selection.ProfileID != target.ProfileID || selection.NetworkPolicyID != target.Network.PolicyID {
		return nil, nil, errors.New("environment target authority mismatch")
	}
	providerKey := selection.ProviderID + "@" + selection.ProviderVersion
	var provider environment.EnvironmentProvider
	if selection.Kind == environment.KindExistingURL {
		if len(managedProviders) != 0 {
			return nil, nil, errors.New("unexpected managed environment composition")
		}
		provider, err = environment.NewExistingURLProvider(&workflowExistingLifecycle{target: target, preview: preview})
	} else {
		provider = managedProviders[providerKey]
		if provider == nil || preview != nil || len(managedProviders) != 1 {
			return nil, nil, errors.New("managed environment composition unavailable")
		}
	}
	if err != nil {
		return nil, nil, err
	}
	registry, err := environment.NewRegistry(map[string]environment.EnvironmentProvider{providerKey: provider})
	if err != nil {
		return nil, nil, err
	}
	var store environment.CleanupLifecycleStore
	if persistence != nil {
		store = persistence.environments
		if store == nil {
			return nil, nil, errors.New("postgres environment persistence incomplete")
		}
	} else {
		localStore, openErr := environment.OpenLocalStore(filepath.Join(root, ".featureproof", "environment"))
		if openErr != nil {
			return nil, nil, openErr
		}
		store = localStore
	}
	clock := environment.WallClock{}
	var demand environment.DemandResolver
	var capacity environment.CapacityAdmission
	var runtime *workflowEnvironmentRuntime
	runtimeKey := ""
	if selection.Kind == environment.KindManaged {
		if config.ManagedCapacity == nil || config.EnvironmentCleanup == nil {
			return nil, nil, errors.New("managed capacity composition unavailable")
		}
		policy, policyErr := environmentcleanup.SealCapacityPolicy(config.ManagedCapacity.Policy)
		demandValue, demandOK := config.ManagedCapacity.Demands[selection.Digest]
		ttl := config.ManagedCapacity.TTLs[selection.Digest]
		if policyErr != nil || policy.Digest != config.ManagedCapacity.Policy.Digest || policy.ProjectID != selection.ProjectID || !demandOK || demandValue.Environments != 1 || ttl <= 0 || ttl > policy.MaxTTL {
			return nil, nil, errors.New("managed capacity authority invalid")
		}
		runtimeKey, err = workflowEnvironmentRuntimeKey(root, selection.ProjectID)
		if err != nil {
			return nil, nil, err
		}
		workflowEnvironmentRuntimes.Lock()
		existingRuntime := workflowEnvironmentRuntimes.values[runtimeKey]
		workflowEnvironmentRuntimes.Unlock()
		if existingRuntime != nil {
			return existingRuntime.port, existingRuntime, nil
		}
		demand = environment.StaticDemandResolver{Demands: map[string]environment.ResourceDemand{selection.Digest: demandValue}, TTLs: map[string]time.Duration{selection.Digest: ttl}, PolicyDigests: map[string]string{selection.Digest: policy.Digest}}
		if persistence != nil {
			ledger := persistence.cleanupLedger
			if ledger == nil || persistence.capacity == nil {
				return nil, nil, errors.New("postgres capacity persistence incomplete")
			}
			capacity = persistence.capacity
			if persistence.cleanup == nil {
				return nil, nil, errors.New("postgres cleanup persistence incomplete")
			}
			runtime = &workflowEnvironmentRuntime{root: root, project: selection.ProjectID, config: *config.EnvironmentCleanup, ledger: ledger, cleanup: persistence.cleanup}
		} else {
			ledger, ledgerErr := environmentcleanup.OpenCapacityLedger(filepath.Join(root, ".featureproof", "cleanup", "capacity.json"))
			if ledgerErr != nil {
				return nil, nil, ledgerErr
			}
			durableCapacity := &workflowDurableCapacity{ledger: ledger, policies: map[string]environmentcleanup.CapacityPolicy{policy.Digest: policy}}
			capacity = durableCapacity
			runtime = &workflowEnvironmentRuntime{root: root, project: selection.ProjectID, config: *config.EnvironmentCleanup, ledger: ledger, capacity: durableCapacity}
		}
	}
	lifecycle, err := environment.NewLifecycleService(environment.Dependencies{Catalog: config.EnvironmentCatalog, Providers: registry, Store: store, Network: environment.AllowlistedNetworkAuthorizer{}, Clock: clock, Waiter: clock, Observer: &environment.MemoryObserver{}, Owner: "workflow-composition", Demand: demand, Capacity: capacity, Limits: environment.Limits{AcquireTimeout: 30 * time.Second, ObserveTimeout: 10 * time.Second, ReleaseTimeout: 10 * time.Second, NetworkTimeout: 10 * time.Second, ObserverTimeout: 5 * time.Second, LeaseTTL: 10 * time.Minute, MaxAttempts: 3, MaxReadiness: 10, MaxElapsed: 2 * time.Minute, MaxCumulativeWait: 30 * time.Second, InitialBackoff: time.Second}})
	if err != nil {
		return nil, nil, err
	}
	if runtime != nil {
		runtime.bridge = &environment.CleanupBridge{Store: store, Service: lifecycle, Owner: config.EnvironmentCleanup.Owner}
	}
	port := validationworkflow.LifecycleEnvironmentPort{Lifecycle: lifecycle, Projects: map[string]string{config.TargetAlias: selection.ProjectID}}
	if runtime != nil {
		runtime.port = port
		workflowEnvironmentRuntimes.Lock()
		if existingRuntime := workflowEnvironmentRuntimes.values[runtimeKey]; existingRuntime != nil {
			workflowEnvironmentRuntimes.Unlock()
			return existingRuntime.port, existingRuntime, nil
		}
		workflowEnvironmentRuntimes.values[runtimeKey] = runtime
		workflowEnvironmentRuntimes.Unlock()
	}
	return port, runtime, nil
}

// workflowExistingLifecycle adapts configured direct targets and feature 19 previews without provisioning effects.
type workflowExistingLifecycle struct {
	target  validationworkflow.TargetContext
	preview *workflowPreviewPort
}

// AcquireExisting returns current trusted direct authority or delegates one preview reservation.
func (l *workflowExistingLifecycle) AcquireExisting(ctx context.Context, runID string, selection environment.Selection, repositoryID, commitOID string) (environment.ExistingBinding, error) {
	if l.preview == nil {
		return l.directBinding(selection, repositoryID, commitOID)
	}
	binding, err := l.preview.Acquire(ctx, runID, validationworkflow.StartCommand{RepositoryID: repositoryID, Revision: commitOID, Target: validationworkflow.TargetSelection{Preview: &validationworkflow.PreviewTarget{Alias: selection.Alias, Digest: selection.Digest}}})
	if err != nil {
		return environment.ExistingBinding{}, err
	}
	return existingBindingFromWorkflow(binding.Target, selection), nil
}

// RestoreExisting reconstructs preview authority or rechecks the immutable configured direct binding.
func (l *workflowExistingLifecycle) RestoreExisting(ctx context.Context, runID string, selection environment.Selection, repositoryID, commitOID string) (environment.ExistingBinding, error) {
	if l.preview == nil {
		return l.directBinding(selection, repositoryID, commitOID)
	}
	binding, err := l.preview.Restore(ctx, runID, validationworkflow.StartCommand{RepositoryID: repositoryID, Revision: commitOID, Target: validationworkflow.TargetSelection{Preview: &validationworkflow.PreviewTarget{Alias: selection.Alias, Digest: selection.Digest}}})
	if err != nil {
		return environment.ExistingBinding{}, err
	}
	return existingBindingFromWorkflow(binding.Target, selection), nil
}

// ReleaseExisting performs the feature 19 logical release once and leaves direct infrastructure untouched.
func (l *workflowExistingLifecycle) ReleaseExisting(ctx context.Context, runID string) error {
	if l.preview == nil {
		return nil
	}
	return l.preview.Release(ctx, runID)
}

// directBinding rejects cross-revision reuse before projecting the configured existing URL.
func (l *workflowExistingLifecycle) directBinding(selection environment.Selection, repositoryID, commitOID string) (environment.ExistingBinding, error) {
	if l.target.Attestation.RepositoryID != repositoryID || l.target.Attestation.CommitOID != commitOID {
		return environment.ExistingBinding{}, errors.New("configured target revision mismatch")
	}
	return existingBindingFromWorkflow(l.target, selection), nil
}

// existingBindingFromWorkflow translates the complete approved target without exposing credentials.
func existingBindingFromWorkflow(target validationworkflow.TargetContext, selection environment.Selection) environment.ExistingBinding {
	return environment.ExistingBinding{ProviderID: selection.ProviderID, ProviderVersion: selection.ProviderVersion, EnvironmentID: target.EnvironmentID, EnvironmentRef: target.EnvironmentRef, ProfileID: target.ProfileID, ReservationID: target.ReservationID, Fingerprint: target.Fingerprint, LifecycleHandle: workflowDigest("lifecycle", selection.Digest, target.EnvironmentID, target.ReservationID), Endpoint: target.URL, ObservedVersion: target.ObservedVersion, ObservationDigest: workflowDigest("observation", target.EnvironmentID, target.ObservedVersion, target.Fingerprint), Availability: environment.AvailabilityReady, Attestation: environment.Attestation{EnvironmentID: target.Attestation.EnvironmentID, RepositoryID: target.Attestation.RepositoryID, CommitOID: target.Attestation.CommitOID, Reference: target.Attestation.Reference, ValidUntil: target.Attestation.ValidUntil, Verified: target.Attestation.Verified}, Network: environment.NetworkPolicy{PolicyID: target.Network.PolicyID, Schemes: append([]string(nil), target.Network.Schemes...), Hosts: append([]string(nil), target.Network.Hosts...), Ports: append([]int(nil), target.Network.Ports...), BasePaths: append([]string(nil), target.Network.BasePaths...), MaxURLBytes: target.Network.MaxURLBytes}, Capabilities: append([]string(nil), selection.Capabilities...)}
}

// workflowPreviewPort connects workflow preview selections to the existing durable preview coordinator.
type workflowPreviewPort struct {
	configured *configuredPreview
	target     string
	mu         sync.Mutex
	attestors  map[string]revisionbinding.EnvironmentAttestor
}

// Acquire reserves and verifies the configured preview and returns its complete safe target object.
func (p *workflowPreviewPort) Acquire(ctx context.Context, handle string, command validationworkflow.StartCommand) (validationworkflow.EnvironmentBinding, error) {
	if p.configured == nil || command.Target.Preview == nil || command.Target.Preview.Alias != p.configured.Request.Selection.Alias {
		return validationworkflow.EnvironmentBinding{}, errors.New("preview selection unavailable")
	}
	authorized, err := p.configured.Coordinator.Acquire(ctx, handle, p.configured.Request.Selection, p.configured.Request.Expected)
	if err != nil || authorized.Target.BaseURL != p.target {
		return validationworkflow.EnvironmentBinding{}, errors.New("preview target drift")
	}
	p.mu.Lock()
	p.attestors[authorized.Snapshot.EnvironmentID] = authorized.Attestor
	p.mu.Unlock()
	return p.previewBinding(authorized), nil
}

// Restore reconstructs the acquired preview attestor from the coordinator's sealed durable snapshot.
func (p *workflowPreviewPort) Restore(ctx context.Context, handle string, command validationworkflow.StartCommand) (validationworkflow.EnvironmentBinding, error) {
	if p.configured == nil || command.Target.Preview == nil || command.Target.Preview.Alias != p.configured.Request.Selection.Alias {
		return validationworkflow.EnvironmentBinding{}, errors.New("preview selection unavailable")
	}
	authorized, err := p.configured.Coordinator.RestoreAcquired(ctx, handle, p.configured.Request.Selection, p.configured.Request.Expected)
	if err != nil || authorized.Target.BaseURL != p.target {
		return validationworkflow.EnvironmentBinding{}, errors.New("preview target drift")
	}
	p.mu.Lock()
	p.attestors[authorized.Snapshot.EnvironmentID] = authorized.Attestor
	p.mu.Unlock()
	return p.previewBinding(authorized), nil
}

// previewBinding projects the sealed provider observation and effective network rules without credentials.
func (p *workflowPreviewPort) previewBinding(authorized previewenv.AuthorizedExecution) validationworkflow.EnvironmentBinding {
	configured := p.configured.Config
	snapshot := authorized.Snapshot
	return validationworkflow.EnvironmentBinding{Target: validationworkflow.TargetContext{Kind: "preview", Alias: configured.Alias, URL: authorized.Target.BaseURL, ProviderID: configured.ProviderID, EnvironmentRef: configured.EnvironmentRef, ProfileID: configured.ProfileID, EnvironmentID: snapshot.EnvironmentID, ReservationID: snapshot.ReservationID, ObservedVersion: snapshot.ObservedVersion, Fingerprint: snapshot.Fingerprint, Attestation: validationworkflow.AttestationContext{EnvironmentID: snapshot.EnvironmentID, RepositoryID: snapshot.RepositoryID, CommitOID: snapshot.CommitOID, Reference: snapshot.AttestationRef, ValidUntil: snapshot.AttestationValidUntil, Verified: true}, Network: validationworkflow.NetworkContext{PolicyID: configured.NetworkPolicyID, Schemes: []string{configured.Scheme}, Hosts: []string{configured.Host}, Ports: []int{configured.Port}, BasePaths: []string{configured.BasePath}, MaxURLBytes: 2048}}}
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
