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
	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/externalagent"
	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/functionalflow"
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
)

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

// workflowCredentialResolverFactory is the composition-owned port that creates one late resolver for each trusted reference mapping.
type workflowCredentialResolverFactory func(reference, environment string) testidentity.CredentialResolver

// workflowCompositionOptions carries productive ports and startup recovery behavior without exposing them to workflow input.
type workflowCompositionOptions struct {
	githubAuthority           validationworkflow.GitHubPullRequestAuthority
	credentialResolverFactory workflowCredentialResolverFactory
	recoverPending            bool
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

// composeWorkflowCLIWithOptions constructs the shared productive service graph from trusted ports and optionally runs startup recovery.
func composeWorkflowCLIWithOptions(ctx context.Context, stdout, stderr io.Writer, options workflowCompositionOptions) (*validationworkflow.CLI, error) {
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
	var fixturePort validationworkflow.FixtureLifecyclePort
	var fixtureState fixture.Store
	var auditSigner localvalidation.FixtureAuditSigner
	var auditTrust localvalidation.FixtureAuditTrust
	if len(spec.Fixtures) > 0 {
		fixturePort, fixtureState, err = composeWorkflowFixtures(ctx, root, owner, spec, manifest, runtimeDeps, fixtureReduced, config, redaction)
		if err != nil {
			return nil, err
		}
		auditSigner, auditTrust, err = localvalidation.OpenFixtureAuditAuthority(filepath.Join(storeRoot, "private", "fixture-audit.ed25519"), filepath.Join(storeRoot, "trust", "fixture-audit.pin"))
		if err != nil {
			return nil, err
		}
	}
	functionalPort, flowCatalog, err := composeWorkflowFunctionalFlow(root, storeRoot, owner, spec, manifest, config, runtimeDeps, redaction, targetDigest, runtimeDigest, fixtureState)
	if err != nil {
		return nil, err
	}
	var memoryPort validationworkflow.ProjectMemoryPort
	var memoryObserver validationworkflow.ProjectMemoryObservationPort
	var contextWitness projectmemory.MonotonicWitness
	var contextWitnessStream string
	if projectMemoryConfigured() {
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
	if contextWitness == nil {
		return nil, errors.New("durable context witness required")
	}
	if _, witnessErr := contextWitness.Read(ctx, contextWitnessStream); errors.Is(witnessErr, projectmemory.ErrWitnessMissing) {
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
	contextStore, err = contextassembly.NewLocalStore(ctx, filepath.Join(storeRoot, "context"), contextWitnessStream, contextWitness)
	if err != nil {
		return nil, err
	}
	contextPort := &validationworkflow.ContextAssemblyAdapter{Profiles: contextProfiles, Registry: validationworkflow.CurrentContextProducerRegistry{Workflows: store, Admission: admission}, Secrets: contextassembly.NewCanaryGate(nil), Tokens: contextassembly.ByteTokenCounter{Selections: map[string]bool{config.Provider + "/" + config.Model: true}}, Store: contextStore, Observer: validationworkflow.ContextObserver{}, Clock: wallClock}
	service, err := validationworkflow.NewService(validationworkflow.Config{LeaseTTL: 10 * time.Minute, HeartbeatInterval: time.Minute, FinalizeTimeout: 10 * time.Second, ApprovalTTL: 24 * time.Hour}, validationworkflow.Dependencies{Admission: admission, Generation: validationworkflow.GeneratorAdapter{Generator: generator}, Preparation: validationworkflow.PreparationAdapter{Assertions: assertions, Policy: generationPolicy}, Approval: approval, Execution: execution, Packages: validationworkflow.LocalPackageReader{Artifacts: artifacts, AuditTrust: auditTrust}, FixturePackages: validationworkflow.LocalFixturePackageWriter{AuditSigner: auditSigner}, Authorization: workflowAutoPolicy{allowed: config.AllowDisabledApproval}, Finalizer: workflowFinalizer{}, Preview: previewPort, Store: store, Artifacts: artifacts, Executables: executables, Clock: wallClock, Waiter: wallClock, IDs: externalagent.RandomIDs{}, Owner: owner, ContractReview: approvedAuthority, Fixtures: fixturePort, FunctionalFlow: functionalPort, Memory: memoryPort, MemoryObserver: memoryObserver, Context: contextPort})
	if err != nil {
		return nil, err
	}
	if options.recoverPending {
		if err = service.Recover(ctx); err != nil {
			return nil, err
		}
	}
	cli := &validationworkflow.CLI{Service: service, Principal: current.Uid, AuthProfiles: map[string]string{config.AuthProfile: compositionRef}, DirectTargets: map[string]validationworkflow.DirectTarget{}, PreviewTargets: map[string]validationworkflow.PreviewTarget{}, FlowManifests: flowCatalog, FlowAliases: cloneWorkflowAliases(config.FunctionalFlowAliases)}
	if config.TargetKind == "direct" {
		cli.DirectTargets[config.TargetAlias] = validationworkflow.DirectTarget{Alias: config.TargetAlias, Digest: targetDigest}
	} else {
		cli.PreviewTargets[config.TargetAlias] = validationworkflow.PreviewTarget{Alias: config.TargetAlias, Digest: targetDigest}
	}
	return cli, nil
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

// compile-time ownership check keeps the shared service behind the planning and workflow consumer ports.
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
func composeWorkflowFunctionalFlow(root, storeRoot, owner string, spec specification.Specification, manifest planmanifest.Manifest, config workflowConfig, deps localvalidation.RuntimeDependencies, redaction *evidence.Policy, targetDigest, runtimeDigest string, fixtureState fixture.Store) (validationworkflow.FunctionalFlowExecutionPort, *validationworkflow.LocalProvidedFlowCatalog, error) {
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
	httpTool, identityCatalog, err := localvalidation.BuildFixtureHTTPRuntime(spec, manifest, deps)
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
	runtime, catalog, err := validationworkflow.ComposeLocalFunctionalFlowRuntime(validationworkflow.LocalFunctionalFlowRuntimeConfig{Root: filepath.Join(storeRoot, "functional-flow"), PublicIdentity: publicIdentity, AttemptJournalRoot: filepath.Join(root, ".featureproof", "runs"), AllowedManifestRoots: []string{root}, Aliases: config.FunctionalFlowAliases, Limits: limits, Validation: functionalflow.ValidationAuthority{Constraints: constraints, FixtureCaptures: captures}, Binding: functionalflow.BindingAuthority{Operations: operations, Identities: descriptors, Bindings: bindings}, Generated: functionalflow.GeneratedAuthority{ContextDigest: workflowDigest("functional-context", runtimeDigest, targetDigest), TargetID: spec.Target.ID, ContractDigest: contractDigest, Effects: map[string]bool{"http.request": true}, OperationEffects: operationEffects}, Execution: executionConfig, LeaseTTL: leaseTTL, Tool: httpTool, PolicyFactory: deps.PolicyFactory, Sanitizer: evidence.NewSanitizer(redaction), Evaluator: deps.Evaluator, Identities: identityCatalog, Clock: deps.Clock, IDs: deps.IDs, Waiter: deps.Waiter, Fixtures: fixtureResolver})
	if err != nil {
		return nil, nil, err
	}
	return runtime, catalog, nil
}

// composeWorkflowFixtures builds one consumer-owned lifecycle shared by every productive workflow surface.
func composeWorkflowFixtures(shutdown context.Context, root, owner string, spec specification.Specification, manifest planmanifest.Manifest, deps localvalidation.RuntimeDependencies, reduced contractcatalog.ReducedContext, config workflowConfig, policy *evidence.Policy) (validationworkflow.FixtureLifecyclePort, fixture.Store, error) {
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
	stateStore, err := fixture.NewFileStore(filepath.Join(fixtureRoot, "state"))
	if err != nil {
		return nil, nil, err
	}
	evidenceSink := fixture.FileEvidenceSink{Root: filepath.Join(fixtureRoot, "evidence"), Sanitizer: sanitizer}
	executor, err := fixture.NewHTTPAdapter(fixture.HTTPAdapterConfig{Tool: httpTool, Evidence: evidenceSink, IdempotencyHeader: workflowFixtureIdempotencyHeader(manifest)})
	if err != nil {
		return nil, nil, err
	}
	guard := &fixture.ProductRevalidator{PolicyFactory: deps.PolicyFactory, Limits: harness.Limits{MaxSteps: 0, MaxToolCalls: runtimeLimits.MaxToolCalls, MaxRetries: runtimeLimits.MaxRetries}}
	lifecycle, err := fixture.NewLifecycle(stateStore, executor, guard, sanitizer, &fixture.FileObserver{Path: filepath.Join(fixtureRoot, "journal.jsonl")}, fixture.ContextWaiter{}, fixture.DigestKeys{Namespace: config.RepositoryID + ":" + config.EnvironmentID}, fixture.FiniteScopes{Parent: shutdown, Timeout: time.Duration(runtimeLimits.CleanupTimeoutMS) * time.Millisecond, MaxCalls: runtimeLimits.CleanupMaxCalls, MaxRetries: runtimeLimits.CleanupRetries}, fixture.Config{MaxCaptureBytes: runtimeLimits.MaxCaptureBytes, MaxToolCalls: runtimeLimits.MaxToolCalls, MaxRetries: runtimeLimits.MaxRetries, CreateRetries: runtimeLimits.CreateRetries, RetryBackoff: time.Duration(runtimeLimits.RetryBackoffMS) * time.Millisecond, Owner: owner, LeaseTTL: time.Duration(runtimeLimits.LeaseTTLMS) * time.Millisecond, Clock: fixture.WallClock{}})
	if err != nil {
		return nil, nil, err
	}
	authority := fixture.ReducedAuthority{Set: set, TargetID: spec.Target.ID, RepositoryID: config.RepositoryID, Revision: config.CommitOID, Contract: reduced, Identities: identities, Idempotency: fixture.ContractIdempotencyPolicy{}}
	return fixture.WorkflowAdapter{Lifecycle: lifecycle, Authority: authority}, stateStore, nil
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
