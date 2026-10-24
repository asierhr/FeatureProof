package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/functionalflow"
	"github.com/asierhr/featureProof/internal/githubpr"
	"github.com/asierhr/featureProof/internal/projectmemory"
	"github.com/asierhr/featureProof/internal/validationworkflow"
)

// projectMemoryWitnessClient isolates authority traffic from model and target transports replaced by workflow composition.
var projectMemoryWitnessClient = &http.Client{Transport: http.DefaultTransport, Timeout: 10 * time.Second}

// projectMemoryGitHubHandler translates one authenticated webhook command into the shared memory DTO.
func projectMemoryGitHubHandler(composition projectMemoryComposition) func(context.Context, githubpr.WebhookCommand) (string, error) {
	return func(ctx context.Context, command githubpr.WebhookCommand) (string, error) {
		actorID, repositoryID := strconv.FormatInt(command.ActorID, 10), strconv.FormatInt(command.RepositoryID, 10)
		actor := projectmemory.VerifiedGitHubActor{ActorID: actorID, RepositoryID: repositoryID}
		var operation projectmemory.SurfaceOperation
		var payload []byte
		switch command.MemoryOperation {
		case "query":
			operation, actor.Action = projectmemory.OperationQuery, "memory.review"
			request := projectmemory.QueryRequest{ProjectID: composition.HumanAuthority.project, Scope: projectmemory.Scope{Kind: projectmemory.ScopeRevision, RepositoryID: composition.HumanAuthority.repository, CommitOID: composition.HumanAuthority.commit}, States: []projectmemory.State{projectmemory.StateObserved, projectmemory.StateApproved}, MaxSensitivity: projectmemory.SensitivityInternal, Limit: 100, MaxBytes: 256 << 10}
			payload, _ = json.Marshal(request)
		case "approve", "invalidate":
			operation = projectmemory.OperationTransition
			target := projectmemory.StateApproved
			actor.Action = "memory.approve"
			if command.MemoryOperation == "invalidate" {
				target, actor.Action = projectmemory.StateInvalidated, "memory.invalidate"
			}
			request := projectmemory.TransitionCommand{ProjectID: composition.HumanAuthority.project, MemoryID: command.MemoryID, IdempotencyKey: command.DeliveryID, DecisionID: projectmemory.DecisionID(command.MemoryDecisionID), ExpectedSequence: command.MemoryExpectedSequence, ExpectedVersion: command.MemoryExpectedVersion, Target: target}
			payload, _ = json.Marshal(request)
		default:
			return "", &githubpr.Error{Code: githubpr.CodeInvalid, Operation: "memory_command"}
		}
		response := composition.GitHub.Submit(ctx, actor, operation, payload)
		if response.Error != nil {
			code := githubpr.CodeInvalid
			if response.Error.Category == "policy_denied" {
				code = githubpr.CodeUnauthorized
			} else if response.Error.Category == "conflict" || response.Error.Category == "integrity" {
				code = githubpr.CodeConflict
			}
			return "", &githubpr.Error{Code: code, Operation: "memory_" + command.MemoryOperation}
		}
		if response.Query != nil {
			return response.Query.QueryDigest, nil
		}
		if response.Audit != nil {
			return response.Audit.ID, nil
		}
		return "", &githubpr.Error{Code: githubpr.CodeInvalid, Operation: "memory_response"}
	}
}

// runProjectMemoryMCP composes the shared durable service and owns the productive MCP stream.
func runProjectMemoryMCP(ctx context.Context, input io.Reader, output io.Writer) int {
	composition, err := composeProjectMemory()
	if err != nil {
		return 3
	}
	if err = (projectmemory.MCPServer{Adapter: composition.MCP, MaxLine: projectmemory.DefaultLimits().MaxDocumentBytes}).Serve(ctx, input, output); err != nil {
		return 3
	}
	return 0
}

// runProjectMemoryAPI binds only a configured loopback address and joins shutdown.
func runProjectMemoryAPI(ctx context.Context) int {
	composition, err := composeProjectMemory()
	if err != nil {
		return 3
	}
	address := os.Getenv("FEATUREPROOF_MEMORY_ADDRESS")
	if address == "" {
		address = "127.0.0.1:8082"
	}
	host, _, splitErr := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	if splitErr != nil || ip == nil || !ip.IsLoopback() {
		return 3
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return 3
	}
	return serveProjectMemoryAPI(ctx, composition.API, listener)
}

// serveProjectMemoryAPI serves the composed adapter on an already-bound loopback listener.
func serveProjectMemoryAPI(ctx context.Context, handler projectmemory.APIHandler, listener net.Listener) int {
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || address.IP == nil || !address.IP.IsLoopback() {
		_ = listener.Close()
		return 3
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return 3
		}
		return 0
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			return 3
		}
		return 0
	}
}

// projectMemoryClock supplies trusted UTC time to the production memory composition.
type projectMemoryClock struct{}

// Now returns the current UTC wall-clock instant.
func (projectMemoryClock) Now() time.Time { return time.Now().UTC() }

// projectMemoryQueryPolicy is the server-owned eligibility ceiling for one consumer mode.
type projectMemoryQueryPolicy struct {
	classes        []projectmemory.Class
	states         []projectmemory.State
	authorities    []projectmemory.Authority
	maxSensitivity projectmemory.Sensitivity
	review         bool
}

// projectMemoryAuthority is one immutable role policy resolved from process configuration.
type projectMemoryAuthority struct {
	principal, credential, project, repository, revision, commit, configuration string
	role, actorKind                                                             string
	actions                                                                     map[string]bool
	query                                                                       projectMemoryQueryPolicy
}

// Authorize derives action authority from authenticated composition and never from payload claims.
func (authority projectMemoryAuthority) Authorize(_ context.Context, principal, project, action string) (projectmemory.PrincipalDecision, error) {
	if principal != authority.principal || project != authority.project || !authority.actions[action] {
		return projectmemory.PrincipalDecision{}, errors.New("denied")
	}
	return projectmemory.PrincipalDecision{Principal: principal, Allowed: true, MaxScope: projectmemory.ScopeProject, MaxSensitivity: projectmemory.SensitivityRestricted, PolicyDigest: authority.configuration, Actor: projectmemory.Actor{Kind: authority.actorKind, ID: principal}, RepositoryID: authority.repository, CommitOID: authority.commit, ConfigurationDigest: authority.configuration, Producer: "featureproof", ProducerVersion: projectmemory.ContractVersion, Reference: "production-" + authority.role}, nil
}

// Resolve maps the authenticated principal to the exact configured project and revision query authority.
func (authority projectMemoryAuthority) Resolve(_ context.Context, principal string, request projectmemory.QueryRequest) (projectmemory.QueryAuthority, error) {
	if authority.role != "human" || principal != authority.principal || request.ProjectID != authority.project {
		return projectmemory.QueryAuthority{}, errors.New("denied")
	}
	configuredScope := projectmemory.Scope{Kind: projectmemory.ScopeRevision, RepositoryID: authority.repository, CommitOID: authority.commit}
	if request.Scope != configuredScope {
		return projectmemory.QueryAuthority{}, errors.New("scope denied")
	}
	if !queryClassesAllowed(request.Classes, authority.query.classes) || !queryStatesAllowed(request.States, authority.query.states) || !queryAuthoritiesAllowed(request.Authorities, authority.query.authorities) || sensitivityExceeds(request.MaxSensitivity, authority.query.maxSensitivity) || request.IncludeProposed && !authority.query.review {
		return projectmemory.QueryAuthority{}, errors.New("query policy denied")
	}
	return projectmemory.QueryAuthority{ProjectID: authority.project, Scope: configuredScope, Classes: append([]projectmemory.Class(nil), authority.query.classes...), States: append([]projectmemory.State(nil), authority.query.states...), Authorities: append([]projectmemory.Authority(nil), authority.query.authorities...), MaxSensitivity: authority.query.maxSensitivity, ConfigurationDigest: authority.configuration, AsOf: time.Now().UTC(), MaxItems: 100, MaxBytes: 256 << 10, MaxWork: 10000, Review: authority.query.review}, nil
}

// queryClassesAllowed requires every caller class restriction to exist in the configured consumer allowlist.
func queryClassesAllowed(requested, allowed []projectmemory.Class) bool {
	for _, value := range requested {
		found := false
		for _, candidate := range allowed {
			found = found || value == candidate
		}
		if !found {
			return false
		}
	}
	return true
}

// queryStatesAllowed requires every caller state restriction to exist in the configured consumer allowlist.
func queryStatesAllowed(requested, allowed []projectmemory.State) bool {
	for _, value := range requested {
		found := false
		for _, candidate := range allowed {
			found = found || value == candidate
		}
		if !found {
			return false
		}
	}
	return true
}

// queryAuthoritiesAllowed requires every caller authority restriction to exist in the configured consumer allowlist.
func queryAuthoritiesAllowed(requested, allowed []projectmemory.Authority) bool {
	for _, value := range requested {
		found := false
		for _, candidate := range allowed {
			found = found || value == candidate
		}
		if !found {
			return false
		}
	}
	return true
}

// sensitivityExceeds compares the closed sensitivity lattice without trusting lexical ordering.
func sensitivityExceeds(requested, allowed projectmemory.Sensitivity) bool {
	rank := map[projectmemory.Sensitivity]int{projectmemory.SensitivityPublic: 0, projectmemory.SensitivityInternal: 1, projectmemory.SensitivityRestricted: 2}
	requestedRank, requestOK := rank[requested]
	allowedRank, allowedOK := rank[allowed]
	return !requestOK || !allowedOK || requestedRank > allowedRank
}

// ResolvePrincipal authenticates the opaque API or MCP credential against composition state.
func (authority projectMemoryAuthority) ResolvePrincipal(_ context.Context, credential string) (string, error) {
	if authority.credential == "" || credential != authority.credential {
		return "", errors.New("denied")
	}
	return authority.principal, nil
}

// ResolveGitHubPrincipal maps only an allowlisted verified repository action to the configured principal.
func (authority projectMemoryAuthority) ResolveGitHubPrincipal(_ context.Context, actor projectmemory.VerifiedGitHubActor, operation projectmemory.SurfaceOperation, target projectmemory.State) (string, error) {
	expected := map[projectmemory.SurfaceOperation]string{projectmemory.OperationPropose: "memory.propose", projectmemory.OperationQuery: "memory.review"}[operation]
	if operation == projectmemory.OperationTransition {
		expected = map[projectmemory.State]string{projectmemory.StateApproved: "memory.approve", projectmemory.StateInvalidated: "memory.invalidate", projectmemory.StateSuperseded: "memory.supersede"}[target]
	}
	if expected == "" || actor.RepositoryID != authority.repository || actor.ActorID != authority.principal || actor.Action != expected {
		return "", errors.New("denied")
	}
	return authority.principal, nil
}

// projectMemoryEvidence fails closed because production evidence adapters must be configured by their owning functional flow.
type projectMemoryEvidence struct{}

// Verify rejects unconfigured evidence lookup rather than trusting reference metadata.
func (projectMemoryEvidence) Verify(context.Context, string, projectmemory.Scope, projectmemory.EvidenceRef) error {
	return errors.New("evidence authority unavailable")
}

// workflowMemoryEvidence revalidates a memory evidence reference against the verified terminal functional package.
type workflowMemoryEvidence struct {
	project, repository, packageRoot string
}

// Verify reloads the immutable package and matches the exact evidence record without exposing its body.
func (authority workflowMemoryEvidence) Verify(_ context.Context, project string, scope projectmemory.Scope, reference projectmemory.EvidenceRef) error {
	if project != authority.project || reference.Project != authority.project || scope.Kind != projectmemory.ScopeRevision || scope.RepositoryID != authority.repository || reference.RunID == "" {
		return errors.New("evidence denied")
	}
	raw, err := os.ReadFile(filepath.Join(authority.packageRoot, reference.RunID, "functional-flow-package.json"))
	if err != nil {
		return errors.New("evidence missing")
	}
	var pkg functionalflow.Package
	if json.Unmarshal(raw, &pkg) != nil || functionalflow.VerifyPackage(pkg) != nil || pkg.RunID != reference.RunID {
		return errors.New("evidence corrupt")
	}
	for _, record := range pkg.Evidence {
		if record.ID == reference.ID && record.RecordDigest == reference.Digest && record.RunID == reference.RunID {
			return nil
		}
	}
	return errors.New("evidence revoked")
}

// workflowProjectMemory composes planning selection, drift checks, and terminal observation over one service.
type workflowProjectMemory struct {
	service           *projectmemory.Service
	authority         projectMemoryAuthority
	observerPrincipal string
	packages          string
}

// Prepare derives query authority from composition and seals the exact selection for planning and later checkpoints.
func (integration workflowProjectMemory) Prepare(ctx context.Context, repository, revision string) (validationworkflow.ProjectMemorySelection, error) {
	request := projectmemory.QueryRequest{ProjectID: integration.authority.project, Scope: projectmemory.Scope{Kind: projectmemory.ScopeRevision, RepositoryID: repository, CommitOID: revision}, States: []projectmemory.State{projectmemory.StateObserved, projectmemory.StateApproved}, MaxSensitivity: projectmemory.SensitivityInternal, Limit: 100, MaxBytes: 256 << 10}
	authority, err := integration.authority.Resolve(ctx, integration.authority.principal, request)
	if err != nil {
		return validationworkflow.ProjectMemorySelection{}, err
	}
	validated, err := agentplanning.LoadValidatedMemory(ctx, integration.service, request, authority)
	if err != nil {
		return validationworkflow.ProjectMemorySelection{}, err
	}
	return validationworkflow.ProjectMemorySelection{Descriptors: validated.Descriptors, Binding: validated.Binding, Authority: authority}, nil
}

// Revalidate delegates every privileged workflow checkpoint to the shared authenticated memory service.
func (integration workflowProjectMemory) Revalidate(ctx context.Context, binding projectmemory.MemoryBinding, authority projectmemory.QueryAuthority) (projectmemory.BindingStatus, error) {
	return integration.service.Revalidate(ctx, binding, authority)
}

// ObserveTerminal verifies the functional package and publishes an idempotent evidence-backed observation.
func (integration workflowProjectMemory) ObserveTerminal(ctx context.Context, projection validationworkflow.PackageProjection, repository, revision, _ string) error {
	if projection.Outcome != validationworkflow.OutcomePassed && projection.Outcome != validationworkflow.OutcomeFailed {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(projection.PackageDir, "functional-flow-package.json"))
	if err != nil {
		return err
	}
	var pkg functionalflow.Package
	if json.Unmarshal(raw, &pkg) != nil || functionalflow.VerifyPackage(pkg) != nil || pkg.RunID != projection.RunID {
		return errors.New("invalid terminal package")
	}
	head, err := integration.service.Store.Head(ctx, integration.authority.project)
	if err != nil {
		return err
	}
	primaryRaw, _ := json.Marshal(pkg.Primary)
	primaryDigest := sha256.Sum256(primaryRaw)
	now := time.Now().UTC()
	input := projectmemory.ObservationInput{Principal: integration.observerPrincipal, ID: "validation-" + pkg.PackageDigest[:32], IdempotencyKey: "functional-result-" + pkg.PackageDigest, ExpectedSequence: head.Sequence, Candidate: projectmemory.Candidate{ContractVersion: projectmemory.ContractVersion, ProjectID: integration.authority.project, Scope: projectmemory.Scope{Kind: projectmemory.ScopeRevision, RepositoryID: repository, CommitOID: revision}, Class: projectmemory.ClassDiagnostic, Subject: "validation." + pkg.RunID, Predicate: "outcome", Value: projectmemory.TypedValue{Kind: projectmemory.ValueString, Data: string(projection.Outcome)}, Confidence: projectmemory.ConfidenceHigh, Sensitivity: projectmemory.SensitivityInternal, ValidFrom: now}, FunctionalResultDigest: hex.EncodeToString(primaryDigest[:])}
	_, err = functionalflow.ObserveProjectMemory(ctx, projectmemory.ObservationAdapter{Service: integration.service}, pkg, input)
	return err
}

// projectMemoryComposition exposes four real adapters backed by one service and trusted authority mapping.
type projectMemoryComposition struct {
	Service           *projectmemory.Service
	HumanAuthority    projectMemoryAuthority
	ObserverPrincipal string
	CLI               projectmemory.CLIAdapter
	API               projectmemory.APIHandler
	MCP               projectmemory.MCPAdapter
	GitHub            projectmemory.GitHubAdapter
	Witness           projectmemory.MonotonicWitness
	ProjectID         string
}

// composeProjectMemory builds the local authenticated store and all memory transport adapters from process configuration.
func composeProjectMemory() (projectMemoryComposition, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return projectMemoryComposition{}, err
	}
	root := os.Getenv("FEATUREPROOF_MEMORY_ROOT")
	if root == "" {
		root = filepath.Join(workingDirectory, ".featureproof", "memory")
	}
	trustRoot := os.Getenv("FEATUREPROOF_MEMORY_TRUST_ROOT")
	if trustRoot == "" {
		trustRoot = filepath.Join(workingDirectory, ".featureproof-trust", "memory-registry")
	}
	witnessURL := os.Getenv("FEATUREPROOF_MEMORY_WITNESS_URL")
	witnessCredential := os.Getenv("FEATUREPROOF_MEMORY_WITNESS_CREDENTIAL")
	witnessGuarantee := os.Getenv("FEATUREPROOF_MEMORY_WITNESS_GUARANTEE")
	key := []byte(os.Getenv("FEATUREPROOF_MEMORY_INTEGRITY_KEY"))
	principal, credential := os.Getenv("FEATUREPROOF_MEMORY_PRINCIPAL"), os.Getenv("FEATUREPROOF_MEMORY_CREDENTIAL")
	project, repository := os.Getenv("FEATUREPROOF_MEMORY_PROJECT"), os.Getenv("FEATUREPROOF_MEMORY_REPOSITORY")
	commit, configuration := strings.ToLower(os.Getenv("FEATUREPROOF_MEMORY_COMMIT")), strings.ToLower(os.Getenv("FEATUREPROOF_MEMORY_CONFIGURATION_DIGEST"))
	if len(key) < 32 || principal == "" || credential == "" || project == "" || repository == "" || commit == "" || configuration == "" || witnessURL == "" || witnessCredential == "" || witnessGuarantee != "linearizable-durable-nonreplayable-v1" {
		return projectMemoryComposition{}, errors.New("memory composition unavailable")
	}
	integrity, err := projectmemory.NewHMACAuthority("memory-key", key)
	if err != nil {
		return projectMemoryComposition{}, err
	}
	witness, err := projectmemory.NewHTTPMonotonicWitness(witnessURL, witnessCredential, projectMemoryWitnessClient)
	if err != nil {
		return projectMemoryComposition{}, err
	}
	store, err := projectmemory.NewLocalStoreWithAuthorities(root, trustRoot, integrity, witness, projectmemory.DefaultLimits())
	if err != nil {
		return projectMemoryComposition{}, err
	}
	if os.Getenv("FEATUREPROOF_MEMORY_WITNESS_BOOTSTRAP") == "true" {
		if err = store.BootstrapProject(context.Background(), project); err != nil {
			return projectMemoryComposition{}, err
		}
	} else if _, err = witness.Read(context.Background(), project); err != nil {
		return projectMemoryComposition{}, errors.New("memory witness unavailable")
	}
	observerPrincipal, reconcilerPrincipal := principal+".observer", principal+".reconciler"
	queryPolicy := projectMemoryQueryPolicy{classes: []projectmemory.Class{projectmemory.ClassFact, projectmemory.ClassDecision, projectmemory.ClassConvention, projectmemory.ClassDiagnostic}, states: []projectmemory.State{projectmemory.StateObserved, projectmemory.StateApproved}, authorities: []projectmemory.Authority{projectmemory.AuthorityObservation, projectmemory.AuthorityHuman, projectmemory.AuthorityConfiguration}, maxSensitivity: projectmemory.SensitivityInternal}
	human := projectMemoryAuthority{principal: principal, credential: credential, project: project, repository: repository, commit: commit, configuration: configuration, role: "human", actorKind: "human", actions: map[string]bool{"propose": true, string(projectmemory.StateApproved): true, string(projectmemory.StateInvalidated): true, string(projectmemory.StateSuperseded): true}, query: queryPolicy}
	observer := projectMemoryAuthority{principal: observerPrincipal, project: project, repository: repository, commit: commit, configuration: configuration, role: "observer", actorKind: "observer", actions: map[string]bool{"observe": true}}
	reconciler := projectMemoryAuthority{principal: reconcilerPrincipal, project: project, repository: repository, commit: commit, configuration: configuration, role: "reconciler", actorKind: "reconciler", actions: map[string]bool{"expire": true}}
	service := &projectmemory.Service{Store: store, Evidence: projectMemoryEvidence{}, HumanPrincipals: human, ObserverPrincipals: observer, ReconcilerPrincipals: reconciler, Clock: projectMemoryClock{}, Gate: projectmemory.SecretGate{Rules: projectmemory.SecretRules{Names: []string{"password", "token", "secret"}, MaxWork: 10000}}, Limits: projectmemory.DefaultLimits()}
	surface := projectmemory.Surface{Service: service, Authority: human}
	return projectMemoryComposition{Service: service, HumanAuthority: human, ObserverPrincipal: observerPrincipal, CLI: projectmemory.CLIAdapter{Surface: surface, Principal: principal}, API: projectmemory.APIHandler{Surface: surface, Auth: human}, MCP: projectmemory.MCPAdapter{Surface: surface, Auth: human}, GitHub: projectmemory.GitHubAdapter{Surface: surface, Auth: human}, Witness: witness, ProjectID: project}, nil
}
