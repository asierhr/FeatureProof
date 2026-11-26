// Command featureproof exposes local on-demand validation.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/asierhr/featureProof/internal/agenteval"
	"github.com/asierhr/featureProof/internal/contractreview"
	"github.com/asierhr/featureProof/internal/controlplane"
	controlplanepg "github.com/asierhr/featureProof/internal/controlplane/postgres"
	"github.com/asierhr/featureProof/internal/correction"
	"github.com/asierhr/featureProof/internal/environment"
	environmentcleanup "github.com/asierhr/featureProof/internal/environment/cleanup"
	"github.com/asierhr/featureProof/internal/externalagent"
	"github.com/asierhr/featureProof/internal/githubpr"
	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/model"
	"github.com/asierhr/featureProof/internal/model/openai"
	"github.com/asierhr/featureProof/internal/model/openai/oauth"
	"github.com/asierhr/featureProof/internal/previewenv"
	"github.com/asierhr/featureProof/internal/projectconfig"
	"github.com/asierhr/featureProof/internal/projectmemory"
	"github.com/asierhr/featureProof/internal/tenantsecurity"
	"github.com/asierhr/featureProof/internal/validationworkflow"
)

// productionEvalComposer resolves OAuth only after agenteval has completed local preflight.
type productionEvalComposer struct{}

// evalClock supplies live feature-9 budget time.
type evalClock struct{}

// Now returns current UTC wall time for live model budgets.
func (evalClock) Now() time.Time { return time.Now().UTC() }

// evalObserver accepts only the runtime's safe attempt record and stores no provider payload.
type evalObserver struct{}

// controlPlaneObserver emits only the closed content-free operational projection selected by the control plane.
type controlPlaneObserver struct{ logger *slog.Logger }

// operatorUIDAuthorizer permits an offline controller only for the exact OS
// identity selected by composition and never derives project membership.
type operatorUIDAuthorizer struct {
	actual, allowed string
}

// Authorize compares immutable process identity before any local freeze or
// PostgreSQL candidate access occurs.
func (authorizer operatorUIDAuthorizer) Authorize(ctx context.Context, operation string, scope controlplane.ProjectScope) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	allowedOperation := operation == "offline_import" || operation == "query_jobs" || operation == "retention"
	if !allowedOperation || scope.Validate() != nil || authorizer.allowed == "" || authorizer.actual != authorizer.allowed {
		return controlplane.ErrUnauthorized
	}
	return nil
}

// Observe writes operation metadata without run content, SQL text, credentials or authority-bearing values.
func (observer controlPlaneObserver) Observe(_ context.Context, value controlplane.Observation) error {
	if observer.logger == nil {
		return nil
	}
	observer.logger.Info("control_plane", "operation", value.Operation, "category", value.Category, "attempt", value.Attempt, "count", value.Count, "duration_ms", value.Duration.Milliseconds())
	return nil
}

// Observe confirms terminal accounting without exposing authentication or response text.
func (evalObserver) Observe(context.Context, model.AttemptRecord) error { return nil }

// Compose builds the explicit OpenAI OAuth transport and returns a neutral source without profile data.
func (productionEvalComposer) Compose(_ context.Context, o agenteval.CommandOptions) (agenteval.CandidateSource, error) {
	if o.Provider != "openai" {
		return nil, errors.New("unsupported eval provider")
	}
	store, err := oauth.NewLocalStore(o.ProfileStore)
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
	resolver, err := oauth.NewResolver(gateway, oauth.WallClock{}, validator, store)
	if err != nil {
		return nil, err
	}
	credentials, err := oauth.NewProfileSource(resolver, o.Profile)
	if err != nil {
		return nil, err
	}
	transport, err := openai.NewAuthenticatedTransport(credentials, client)
	if err != nil {
		return nil, err
	}
	adapter, err := openai.New(transport, openai.Config{MaxRequestBytes: o.MaxRequestBytes, MaxHeaderBytes: o.MaxHeaderBytes, MaxStreamBytes: o.MaxStreamBytes, MaxOutputBytes: o.MaxOutputBytes})
	if err != nil {
		return nil, err
	}
	session, err := model.NewSession(model.Config{ProviderID: o.Provider, ModelID: o.Model, AuthProfileID: o.Profile, CallTimeout: o.CallTimeout, MaxRequestBytes: o.MaxRequestBytes, MaxStreamBytes: o.MaxStreamBytes, MaxOutputBytes: o.MaxOutputBytes, Budget: model.BudgetLimits{MaxCalls: o.MaxCalls, MaxTokens: o.MaxTokens, MaxDuration: o.MaxDuration, MaxReplans: o.MaxReplans}}, adapter, evalClock{}, evalObserver{})
	if err != nil {
		return nil, err
	}
	return agenteval.NewLiveSource(session, agenteval.SourceBudget{Kind: "live", Provider: o.Provider, Model: o.Model, CallTimeoutMillis: o.CallTimeout.Milliseconds(), MaxCalls: o.MaxCalls, MaxTokens: o.MaxTokens, MaxDurationMillis: o.MaxDuration.Milliseconds(), MaxReplans: o.MaxReplans, MaxRequestBytes: o.MaxRequestBytes, MaxHeaderBytes: o.MaxHeaderBytes, MaxStreamBytes: o.MaxStreamBytes, MaxOutputBytes: o.MaxOutputBytes})
}

// commandArgs isolates process arguments so main remains verifiable without spawning a child process.
func commandArgs() []string { return os.Args[1:] }

// processArgs provides the injectable process argument source used by main.
var processArgs = commandArgs

// processInput provides the injectable standard-input boundary used by main.
var processInput io.Reader = os.Stdin

// processOutput provides the injectable standard-output boundary used by main.
var processOutput io.Writer = os.Stdout

// processError provides the injectable diagnostic-output boundary used by main.
var processError io.Writer = os.Stderr

// processExit provides the injectable process termination boundary used by main.
var processExit = os.Exit

// run installs the production signal context around the otherwise injectable command boundary.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runWithContext(ctx, args, stdin, stdout, stderr)
}

// runWithContext dispatches every product entrypoint under one caller-owned lifecycle for deterministic server shutdown.
func runWithContext(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) >= 2 && args[0] == "project-config" {
		security, err := authorizeProductSurface(ctx, tenantsecurity.SurfaceCLI, tenantsecurity.ActionTenantAdmin, tenantsecurity.ResourceTenant)
		if err != nil {
			return 3
		}
		defer security.Close()
		return runProjectConfigurationCommand(ctx, args[1:], stdin, stdout)
	}
	if len(args) == 2 && args[0] == "control-plane" && args[1] == "import" {
		return runControlPlaneImport(ctx, stdout, stderr)
	}
	if len(args) == 2 && args[0] == "control-plane" && args[1] == "jobs" {
		return runControlPlaneJobs(ctx, stdout, stderr)
	}
	if len(args) == 2 && args[0] == "control-plane" && args[1] == "retention" {
		return runControlPlaneRetention(ctx, stdout, stderr)
	}
	if len(args) == 3 && args[0] == "memory" && args[1] == "mcp" && args[2] == "serve" {
		security, err := composeTenantSecurity(ctx)
		if err != nil {
			return 3
		}
		defer security.Close()
		return runProjectMemoryMCPWithTenantSecurity(ctx, stdin, stdout, security)
	}
	if len(args) == 3 && args[0] == "memory" && args[1] == "api" && args[2] == "serve" {
		security, err := composeTenantSecurity(ctx)
		if err != nil {
			return 3
		}
		defer security.Close()
		return runProjectMemoryAPIWithTenantSecurity(ctx, security)
	}
	if len(args) >= 2 && args[0] == "memory" {
		security, err := authorizeProductSurface(ctx, tenantsecurity.SurfaceCLI, tenantsecurity.ActionConfigRead, tenantsecurity.ResourceConfig)
		if err != nil {
			return 3
		}
		defer security.Close()
		composition, err := composeProjectMemory()
		if err != nil {
			return 3
		}
		raw, err := io.ReadAll(io.LimitReader(stdin, int64(projectmemory.DefaultLimits().MaxDocumentBytes)+1))
		if err != nil {
			return 2
		}
		return composition.CLI.Run(ctx, projectmemory.SurfaceOperation(args[1]), raw, stdout)
	}
	if len(args) >= 2 && args[0] == "eval" && args[1] == "agent" {
		return agenteval.RunCommand(ctx, args[2:], stdout, stderr, productionEvalComposer{})
	}
	if len(args) == 2 && args[0] == "github" && args[1] == "serve" {
		security, err := composeTenantSecurity(ctx)
		if err != nil {
			return 3
		}
		defer security.Close()
		return runGitHubServerWithTenantSecurity(ctx, stderr, security)
	}
	if len(args) > 1 && args[0] == "workflow" {
		security, err := composeTenantSecurity(ctx)
		if err != nil {
			return 3
		}
		defer security.Close()
		surface := tenantsecurity.SurfaceCLI
		workflowContext := ctx
		if len(args) == 3 && args[1] == "mcp" && args[2] == "serve" {
			surface = tenantsecurity.SurfaceMCP
			current, currentErr := user.Current()
			if currentErr != nil || current.Uid == "" {
				return 3
			}
			workflowContext, err = security.AuthenticateSurface(ctx, surface, current.Uid)
			if err != nil {
				return 3
			}
		}
		cli, err := composeWorkflowCLIWithTenantSecurity(workflowContext, stdout, stderr, surface, security)
		if err != nil {
			return 3
		}
		var runtime validationworkflow.RuntimeHandle
		if cli.Runtime != nil {
			runtime, err = cli.Runtime.Start(ctx)
			if err != nil {
				return 3
			}
			defer func() { runtime.Cancel(); _ = runtime.Join() }()
		}
		if len(args) == 3 && args[1] == "mcp" && args[2] == "serve" {
			authenticator, authErr := loadTenantMCPAuthenticator(security, tenantsecurity.SurfaceMCP, "FEATUREPROOF_WORKFLOW_MCP_CREDENTIALS")
			if authErr != nil {
				return 3
			}
			server := validationworkflow.MCPServer{Service: cli.Service, Authenticator: authenticator, AuthProfiles: cli.AuthProfiles, DirectTargets: cli.DirectTargets, PreviewTargets: cli.PreviewTargets, FlowAliases: cli.FlowAliases}
			if server.Serve(ctx, stdin, stdout) != nil {
				return 3
			}
			return 0
		}
		return cli.Run(ctx, args[1:], stdout)
	}
	if len(args) > 1 && args[0] == "contract-review" {
		if len(args) == 3 && args[1] == "mcp" && args[2] == "serve" {
			security, err := composeTenantSecurity(ctx)
			if err != nil {
				return 3
			}
			defer security.Close()
			return runContractReviewMCPWithTenantSecurity(ctx, stdin, stdout, security)
		}
		if len(args) == 3 && args[1] == "api" && args[2] == "serve" {
			security, err := composeTenantSecurity(ctx)
			if err != nil {
				return 3
			}
			defer security.Close()
			return runContractReviewAPIWithTenantSecurity(ctx, security)
		}
		security, err := authorizeProductSurface(ctx, tenantsecurity.SurfaceCLI, tenantsecurity.ActionConfigRead, tenantsecurity.ResourceConfig)
		if err != nil {
			return 3
		}
		defer security.Close()
		return runContractReviewCommand(ctx, args[1:], stdout)
	}
	if isExternalAgentCommand(args) {
		security, securityErr := composeTenantSecurity(ctx)
		if securityErr != nil {
			return writeExternalCompositionError(stdout)
		}
		defer security.Close()
		service, err := composeExternalAgentServiceWithTenantSecurity(security)
		if err != nil {
			return writeExternalCompositionError(stdout)
		}
		defer service.Close(context.WithoutCancel(ctx))
		if len(args) == 2 && args[0] == "mcp" && args[1] == "serve" {
			authenticator, authErr := loadTenantMCPAuthenticator(security, tenantsecurity.SurfaceExternalAgent, "FEATUREPROOF_EXTERNAL_AGENT_MCP_CREDENTIALS")
			if authErr != nil {
				return 3
			}
			server, err := externalagent.NewAuthenticatedMCPServer(service, authenticator)
			if err != nil || server.Serve(ctx, stdin, stdout) != nil {
				return 3
			}
			return 0
		}
		return externalagent.RunCLI(ctx, args, stdout, service)
	}
	security, err := authorizeProductSurface(ctx, tenantsecurity.SurfaceCLI, tenantsecurity.ActionRunAdmit, tenantsecurity.ResourceRun)
	if err != nil {
		return 3
	}
	defer security.Close()
	return localvalidation.RunCommand(localvalidation.TerminalSignalContext(ctx), args, stdin, stdout, stderr)
}

// runProjectConfigurationCommand routes the local administrative CLI through the same authoritative gateway used by admission.
func runProjectConfigurationCommand(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) int {
	if len(args) == 0 {
		return 2
	}
	current, err := user.Current()
	if err != nil {
		return 3
	}
	repositoryID := os.Getenv("FEATUREPROOF_PROJECT_CONFIG_REPOSITORY_ID")
	composition, err := composeWorkflowProjectConfiguration(ctx, "", current.Uid, repositoryID, nil, false)
	if err != nil {
		return 3
	}
	request := projectconfig.GatewayRequest{Authority: composition.Authority, ProjectKey: composition.Key, Operation: projectconfig.Operation(args[0])}
	if request.Operation == projectconfig.OperationValidate || request.Operation == projectconfig.OperationPublish {
		request.Document, err = io.ReadAll(io.LimitReader(stdin, projectconfig.MaxCanonicalBytes+1))
		if err != nil || len(request.Document) > projectconfig.MaxCanonicalBytes {
			return 2
		}
	}
	switch request.Operation {
	case projectconfig.OperationPublish:
		if len(args) != 3 {
			return 2
		}
		request.OperationID = args[1]
		request.ExpectedGeneration, err = strconv.ParseUint(args[2], 10, 64)
	case projectconfig.OperationRollback:
		if len(args) != 4 {
			return 2
		}
		request.OperationID = args[1]
		request.Version, err = strconv.ParseUint(args[2], 10, 64)
		if err == nil {
			request.ExpectedGeneration, err = strconv.ParseUint(args[3], 10, 64)
		}
	case projectconfig.OperationShowVersion, projectconfig.OperationExport:
		if len(args) != 2 {
			return 2
		}
		request.Version, err = strconv.ParseUint(args[1], 10, 64)
	case projectconfig.OperationHistory:
		if len(args) != 3 {
			return 2
		}
		request.Cursor = args[1]
		request.Limit, err = strconv.Atoi(args[2])
	case projectconfig.OperationValidate, projectconfig.OperationShowActive:
		if len(args) != 1 {
			return 2
		}
	default:
		return 2
	}
	if err != nil {
		return 2
	}
	result, err := composition.Gateway.Execute(ctx, request)
	if err != nil {
		return 3
	}
	if json.NewEncoder(stdout).Encode(result) != nil {
		return 3
	}
	return 0
}

// currentOperatorAuthorizer binds operational commands to the exact current
// OS identity selected by trusted process composition.
func currentOperatorAuthorizer() (operatorUIDAuthorizer, error) {
	current, err := user.Current()
	if err != nil {
		return operatorUIDAuthorizer{}, err
	}
	return operatorUIDAuthorizer{actual: current.Uid, allowed: os.Getenv("FEATUREPROOF_CONTROL_PLANE_OPERATOR_UID")}, nil
}

// openOperationalControlPlane opens only the explicitly selected PostgreSQL
// backend and completes migrate/readiness before an operator controller runs.
func openOperationalControlPlane(ctx context.Context, stderr io.Writer) (*controlplanepg.Store, error) {
	observer := controlPlaneObserver{logger: slog.New(slog.NewJSONHandler(stderr, nil))}
	store, err := controlplanepg.Open(ctx, controlplanepg.Config{DSN: os.Getenv("FEATUREPROOF_CONTROL_PLANE_POSTGRES_DSN"), BackendIdentity: requiredEnv("FEATUREPROOF_CONTROL_PLANE_IDENTITY"), MaxConnections: 8, StatementTimeout: 5 * time.Second, MaxLease: 5 * time.Minute, Observer: observer, IsolatedLoopbackTest: isolatedPostgresGateAuthorized()})
	if err != nil {
		return nil, err
	}
	if err = store.Migrate(ctx); err != nil {
		store.Close()
		return nil, err
	}
	if err = store.Ready(ctx); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

// runControlPlaneJobs exposes one authenticated, project-scoped keyset page
// and never accepts an unsigned cursor.
func runControlPlaneJobs(ctx context.Context, stdout, stderr io.Writer) int {
	authorizer, err := currentOperatorAuthorizer()
	if err != nil {
		return 3
	}
	store, err := openOperationalControlPlane(ctx, stderr)
	if err != nil {
		return 3
	}
	defer store.Close()
	codec, err := controlplane.NewCursorCodec([]byte(os.Getenv("FEATUREPROOF_CONTROL_PLANE_CURSOR_SECRET")))
	if err != nil {
		return 3
	}
	limit, err := strconv.Atoi(os.Getenv("FEATUREPROOF_CONTROL_PLANE_QUERY_LIMIT"))
	if err != nil {
		return 2
	}
	query := controlplane.JobQuery{OrganizationID: os.Getenv("FEATUREPROOF_CONTROL_PLANE_QUERY_ORGANIZATION_ID"), ProjectID: os.Getenv("FEATUREPROOF_CONTROL_PLANE_QUERY_PROJECT_ID"), State: controlplane.JobState(os.Getenv("FEATUREPROOF_CONTROL_PLANE_QUERY_STATE")), Cursor: os.Getenv("FEATUREPROOF_CONTROL_PLANE_QUERY_CURSOR"), Limit: limit, Now: time.Now().UTC(), CursorTTL: 5 * time.Minute}
	page, err := (controlplane.QueryController{Store: store, Authorizer: authorizer, Cursors: codec}).ListJobs(ctx, query)
	if err != nil || json.NewEncoder(stdout).Encode(page) != nil {
		return 3
	}
	return 0
}

// runControlPlaneRetention executes exactly one authenticated explicit policy
// batch; absence or malformed policy never falls back to deletion defaults.
func runControlPlaneRetention(ctx context.Context, stdout, stderr io.Writer) int {
	authorizer, err := currentOperatorAuthorizer()
	if err != nil {
		return 3
	}
	store, err := openOperationalControlPlane(ctx, stderr)
	if err != nil {
		return 3
	}
	defer store.Close()
	before, beforeErr := strconv.ParseInt(os.Getenv("FEATUREPROOF_CONTROL_PLANE_RETENTION_BEFORE_UNIX"), 10, 64)
	batch, batchErr := strconv.Atoi(os.Getenv("FEATUREPROOF_CONTROL_PLANE_RETENTION_BATCH_SIZE"))
	if beforeErr != nil || batchErr != nil {
		return 2
	}
	scope := controlplane.ProjectScope{OrganizationID: os.Getenv("FEATUREPROOF_CONTROL_PLANE_RETENTION_ORGANIZATION_ID"), ProjectID: os.Getenv("FEATUREPROOF_CONTROL_PLANE_RETENTION_PROJECT_ID")}
	count, err := (controlplane.RetentionController{Store: store, Authorizer: authorizer}).Run(ctx, scope, &controlplane.RetentionPolicy{BeforeUnix: before, BatchSize: batch})
	if err != nil || json.NewEncoder(stdout).Encode(map[string]int{"deleted": count}) != nil {
		return 3
	}
	return 0
}

// runControlPlaneImport composes the explicit offline source and PostgreSQL
// target, runs the authenticated resumable protocol, and prints only its safe
// cutover receipt.
func runControlPlaneImport(ctx context.Context, stdout, stderr io.Writer) int {
	authorizer, err := currentOperatorAuthorizer()
	if err != nil {
		return 3
	}
	epoch, err := strconv.ParseUint(os.Getenv("FEATUREPROOF_CONTROL_PLANE_IMPORT_EXPECTED_EPOCH"), 10, 64)
	if err != nil || epoch == 0 {
		return 2
	}
	pageSize := 100
	if configured := os.Getenv("FEATUREPROOF_CONTROL_PLANE_IMPORT_PAGE_SIZE"); configured != "" {
		pageSize, err = strconv.Atoi(configured)
		if err != nil || pageSize <= 0 || pageSize > 1000 {
			return 2
		}
	}
	target, err := openOperationalControlPlane(ctx, stderr)
	if err != nil {
		return 3
	}
	defer target.Close()
	source, err := controlplane.OpenLocalImportStore(os.Getenv("FEATUREPROOF_CONTROL_PLANE_LOCAL_ROOT"), os.Getenv("FEATUREPROOF_CONTROL_PLANE_LOCAL_IDENTITY"))
	if err != nil {
		return 3
	}
	if err = registerLocalImportExporters(source, os.Getenv("FEATUREPROOF_CONTROL_PLANE_LOCAL_ROOT")); err != nil {
		return 3
	}
	request := controlplane.OfflineImportRequest{OrganizationID: os.Getenv("FEATUREPROOF_CONTROL_PLANE_IMPORT_ORGANIZATION_ID"), ImportID: os.Getenv("FEATUREPROOF_CONTROL_PLANE_IMPORT_ID"), RunID: os.Getenv("FEATUREPROOF_CONTROL_PLANE_IMPORT_RUN_ID"), ProjectID: os.Getenv("FEATUREPROOF_CONTROL_PLANE_IMPORT_PROJECT_ID"), ExpectedHead: os.Getenv("FEATUREPROOF_CONTROL_PLANE_IMPORT_EXPECTED_HEAD"), ExpectedEpoch: epoch, PageSize: pageSize, Now: time.Now().UTC(), ReconcileTimeout: 30 * time.Second}
	controller := controlplane.OfflineImportController{Source: source, Target: target, Authorizer: authorizer}
	receipt, err := controller.Run(ctx, request)
	if err != nil {
		return 3
	}
	if err = json.NewEncoder(stdout).Encode(receipt); err != nil {
		return 3
	}
	return 0
}

// registerLocalImportExporters composes each consumer-owned verifier into the
// global freeze so private journals, anchors and locks never cross staging.
func registerLocalImportExporters(source *controlplane.LocalImportStore, root string) error {
	featureRoot := root
	if filepath.Base(filepath.Clean(root)) != ".featureproof" {
		featureRoot = filepath.Join(root, ".featureproof")
	}
	registrations := []controlplane.LocalSnapshotExporter{
		func(ctx context.Context, _ string) ([]controlplane.LocalSnapshotExport, error) {
			path := filepath.Join(featureRoot, "cleanup", "items.json")
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				return nil, nil
			} else if err != nil {
				return nil, err
			}
			document, err := environmentcleanup.ExportImportSnapshot(ctx, path)
			return []controlplane.LocalSnapshotExport{{Stream: "cleanup", RelationKey: "typed-cleanup", Document: document, ClaimedPaths: environmentcleanup.ImportClaimedPaths(path)}}, err
		},
		func(ctx context.Context, _ string) ([]controlplane.LocalSnapshotExport, error) {
			path := filepath.Join(featureRoot, "cleanup", "capacity.json")
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				return nil, nil
			} else if err != nil {
				return nil, err
			}
			document, err := environmentcleanup.ExportCapacityImportSnapshot(ctx, path)
			return []controlplane.LocalSnapshotExport{{Stream: "capacity", RelationKey: "typed-capacity", Document: document, ClaimedPaths: environmentcleanup.ImportClaimedPaths(path)}}, err
		},
		func(ctx context.Context, _ string) ([]controlplane.LocalSnapshotExport, error) {
			path := filepath.Join(featureRoot, "corrections")
			exists, err := correction.ImportStoreExists(path)
			if err != nil || !exists {
				return nil, err
			}
			document, err := correction.ExportImportSnapshot(ctx, path)
			return []controlplane.LocalSnapshotExport{{Stream: "correction", RelationKey: "typed-correction", Document: document, ClaimedPaths: correction.ImportClaimedPaths(path)}}, err
		},
		func(ctx context.Context, _ string) ([]controlplane.LocalSnapshotExport, error) {
			path := filepath.Join(featureRoot, "github-pr")
			exists, err := githubpr.ImportStoreExists(path)
			if err != nil || !exists {
				return nil, err
			}
			document, err := githubpr.ExportImportSnapshot(ctx, path)
			return []controlplane.LocalSnapshotExport{{Stream: "github", RelationKey: "typed-github", Document: document, ClaimedPaths: githubpr.ImportClaimedPaths(path)}}, err
		},
		func(ctx context.Context, _ string) ([]controlplane.LocalSnapshotExport, error) {
			path := filepath.Join(featureRoot, "workflows", "functional-flow", "packages")
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				return nil, nil
			} else if err != nil {
				return nil, err
			}
			document, err := validationworkflow.ExportPackageImportSnapshot(ctx, path)
			return []controlplane.LocalSnapshotExport{{Stream: "packages", RelationKey: "typed-packages", Document: document, ClaimedPaths: validationworkflow.PackageImportClaimedPaths(path)}}, err
		},
	}
	for _, registration := range registrations {
		if err := source.RegisterSnapshotExporter(registration); err != nil {
			return err
		}
	}
	return nil
}

// runGitHubServer composes secrets at the executable boundary and owns bounded HTTP shutdown.
func runGitHubServer(ctx context.Context, stderr io.Writer) int {
	return runGitHubServerWithReadiness(ctx, stderr, nil)
}

// runGitHubServerWithTenantSecurity preserves the authorized composition for
// worker quota decisions throughout the server lifecycle.
func runGitHubServerWithTenantSecurity(ctx context.Context, stderr io.Writer, security *tenantSecurityComposition) int {
	runtime, address, err := composeGitHubRuntimeWithTenantSecurity(ctx, security)
	if err != nil {
		return 3
	}
	return serveGitHubRuntime(ctx, stderr, nil, runtime, address)
}

// runGitHubServerWithReadiness exposes listener ownership to integration tests without changing production shutdown semantics.
func runGitHubServerWithReadiness(ctx context.Context, stderr io.Writer, ready chan<- struct{}) int {
	runtime, address, err := composeGitHubRuntime(ctx)
	if err != nil {
		return 3
	}
	return serveGitHubRuntime(ctx, stderr, ready, runtime, address)
}

// serveGitHubRuntime owns bounded listener startup and shutdown after all
// security composition has completed.
func serveGitHubRuntime(ctx context.Context, stderr io.Writer, ready chan<- struct{}, runtime *githubpr.Runtime, address string) int {
	var err error
	if err = runtime.Start(ctx); err != nil {
		_ = runtime.Close(context.WithoutCancel(ctx))
		return 3
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		_ = runtime.Close(context.WithoutCancel(ctx))
		return 3
	}
	server := &http.Server{Addr: address, Handler: runtime.Handler(), ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	if ready != nil {
		close(ready)
	}
	select {
	case err = <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			_ = runtime.Close(context.WithoutCancel(ctx))
			return 3
		}
		return 0
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		if runtime.Close(shutdownCtx) != nil {
			return 3
		}
		return 0
	}
}

// composeGitHubRuntime resolves GitHub App secrets and exact allowlists without passing them to the harness.
func composeGitHubRuntime(ctx context.Context) (*githubpr.Runtime, string, error) {
	return composeGitHubRuntimeWithTenantSecurity(ctx, nil)
}

// composeGitHubRuntimeWithTenantSecurity installs worker claim quotas when the
// productive entrypoint supplies its mandatory tenant composition.
func composeGitHubRuntimeWithTenantSecurity(ctx context.Context, security *tenantSecurityComposition) (*githubpr.Runtime, string, error) {
	installation, err := requiredPositiveEnv("FEATUREPROOF_GITHUB_INSTALLATION_ID")
	if err != nil {
		return nil, "", err
	}
	repository, err := requiredPositiveEnv("FEATUREPROOF_GITHUB_REPOSITORY_ID")
	if err != nil {
		return nil, "", err
	}
	actor, err := requiredPositiveEnv("FEATUREPROOF_GITHUB_ACTOR_ID")
	if err != nil {
		return nil, "", err
	}
	appID, err := requiredPositiveEnv("FEATUREPROOF_GITHUB_APP_ID")
	if err != nil {
		return nil, "", err
	}
	botID, err := requiredPositiveEnv("FEATUREPROOF_GITHUB_BOT_ID")
	if err != nil {
		return nil, "", err
	}
	secret := []byte(os.Getenv("FEATUREPROOF_GITHUB_WEBHOOK_SECRET"))
	privateKey := []byte(os.Getenv("FEATUREPROOF_GITHUB_PRIVATE_KEY_PEM"))
	if len(secret) < 16 || len(privateKey) == 0 {
		return nil, "", errors.New("github secrets unavailable")
	}
	clock := githubpr.WallClock{}
	httpClient := &http.Client{Timeout: 2 * time.Minute}
	apiOrigin := os.Getenv("FEATUREPROOF_GITHUB_API_ORIGIN")
	if apiOrigin == "" {
		apiOrigin = "https://api.github.com"
	}
	postgresMode := os.Getenv("FEATUREPROOF_CONTROL_PLANE_BACKEND") == string(controlplane.BackendPostgres)
	if postgresMode && (os.Getenv("FEATUREPROOF_PREVIEW_CONFIG") != "" || projectMemoryConfigured() || os.Getenv("FEATUREPROOF_CONTRACT_REVIEW_CONFIG") != "") {
		return nil, "", errors.New("postgres composition requires PostgreSQL-backed optional workflow integrations")
	}
	var tokenSource githubpr.CredentialSource
	var authorityAdapter *githubpr.RESTAdapter
	if !postgresMode {
		tokenSource, err = githubpr.NewGitHubAppTokenSource(githubpr.GitHubAppTokenConfig{AppID: appID, BaseURL: apiOrigin, APIVersion: "2022-11-28", PrivateKeyPEM: privateKey, HTTP: httpClient, Clock: clock, MaxResponse: 1 << 20})
		if err != nil {
			return nil, "", err
		}
		authorityAdapter, err = githubpr.NewRESTAdapter(githubpr.RESTConfig{BaseURL: apiOrigin, APIVersion: "2022-11-28", AppID: appID, Tokens: tokenSource, HTTP: httpClient, Clock: clock, MaxResponse: 1 << 20, MaxPages: 10, MaxChangedPaths: 1000})
		if err != nil {
			return nil, "", err
		}
	}
	root, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}
	validation := githubpr.ValidationGateway(githubpr.FailClosedValidation{})
	selection := githubpr.Selection{Alias: "default", Secretless: false}
	if preview, configured, previewErr := composeConfiguredPreview(ctx, root); previewErr != nil {
		return nil, "", previewErr
	} else if configured {
		gateway, gatewayErr := previewenv.NewGitHubGateway(preview.Coordinator)
		if gatewayErr != nil {
			return nil, "", gatewayErr
		}
		recovery, recoveryErr := previewenv.StartRecoverySupervisor(preview.Coordinator)
		if recoveryErr != nil {
			return nil, "", recoveryErr
		}
		preview.Recovery = recovery
		validation = gateway
		gateway.AttachRecovery(preview.Recovery)
		selection = githubpr.Selection{Alias: preview.Request.Selection.Alias, Secretless: preview.Request.Selection.Secretless, Value: preview.Request}
	}
	var configuredWorkflow *workflowConfig
	if os.Getenv("FEATUREPROOF_WORKFLOW_CONFIG") != "" {
		workflowConfig, workflowErr := loadWorkflowConfig()
		if workflowErr != nil {
			return nil, "", workflowErr
		}
		configuredWorkflow = &workflowConfig
		if !postgresMode {
			if workflowErr = bindProductWorkflowConfiguration(ctx, configuredWorkflow, nil); workflowErr != nil {
				return nil, "", workflowErr
			}
			selection = githubpr.Selection{Alias: configuredWorkflow.TargetAlias, Secretless: configuredWorkflow.TargetKind == "preview"}
			var workflowCLI *validationworkflow.CLI
			var composeErr error
			if security != nil {
				workflowCLI, composeErr = composeWorkflowCLIWithGitHubTenantSecurity(ctx, io.Discard, io.Discard, authorityAdapter, nil, security)
			} else {
				workflowCLI, composeErr = composeWorkflowCLIWithGitHubAuthority(ctx, io.Discard, io.Discard, authorityAdapter)
			}
			if composeErr != nil {
				return nil, "", composeErr
			}
			validation, composeErr = githubWorkflowValidation(workflowCLI, workflowConfig)
			if composeErr != nil {
				return nil, "", composeErr
			}
		}
	}
	var reviewHandler func(context.Context, githubpr.WebhookCommand) (string, error)
	var reviewOwner interface{ Close() error }
	var memoryHandler func(context.Context, githubpr.WebhookCommand) (string, error)
	if projectMemoryConfigured() {
		memoryComposition, memoryErr := composeProjectMemory()
		if memoryErr != nil {
			return nil, "", memoryErr
		}
		memoryHandler = projectMemoryGitHubHandler(memoryComposition)
	}
	var contractHandler func(context.Context, githubpr.WebhookCommand) (string, error)
	if os.Getenv("FEATUREPROOF_CONTRACT_REVIEW_CONFIG") != "" {
		if postgresMode {
			return nil, "", errors.New("postgres contract review effect composition unavailable")
		}
		reviewCLI, reviewErr := composeContractReviewCLI(ctx)
		if reviewErr != nil {
			return nil, "", reviewErr
		}
		reviewService, ok := reviewCLI.API.Service.(contractreview.Service)
		if !ok {
			_ = reviewCLI.Close()
			return nil, "", errors.New("invalid contract review composition")
		}
		reviewService.GitHub = authorityAdapter
		reviewCLI.API.Service = reviewService
		candidateRef := os.Getenv("FEATUREPROOF_CONTRACT_REVIEW_CANDIDATE")
		baselineRef := os.Getenv("FEATUREPROOF_CONTRACT_REVIEW_BASELINE")
		if candidateRef == "" {
			candidateRef = "candidate"
		}
		if baselineRef == "" {
			baselineRef = "baseline"
		}
		gateway := contractreview.GitHubRuntimeGateway{API: reviewCLI.API, Authority: authorityAdapter, Principals: contractreview.StaticGitHubPrincipalAuthority{Principals: map[int64]string{actor: reviewCLI.API.Config.PrincipalID}}, CandidateRef: candidateRef, BaselineRef: baselineRef, Summaries: contractreview.DurableGitHubSummary{GitHub: authorityAdapter, BotID: botID, MaxPages: 10}}
		contractHandler = gateway.HandleReview
		reviewOwner = reviewCLI
	}
	if memoryHandler != nil || contractHandler != nil {
		reviewHandler = func(handlerContext context.Context, command githubpr.WebhookCommand) (string, error) {
			if command.MemoryOperation != "" && memoryHandler != nil {
				return memoryHandler(handlerContext, command)
			}
			if command.ReviewOperation != "" && contractHandler != nil {
				return contractHandler(handlerContext, command)
			}
			return "", &githubpr.Error{Code: githubpr.CodeUnauthorized, Operation: "command_unavailable"}
		}
	}
	config := githubpr.ProductionConfig{AppID: appID, BotID: botID, Permissions: githubpr.RequiredPermissions(), APIOrigin: apiOrigin, APIVersion: "2022-11-28", EvidenceOrigin: requiredEnv("FEATUREPROOF_EVIDENCE_ORIGIN"), StoreDir: filepath.Join(root, ".featureproof", "github-pr"), MaxWebhookBytes: 1 << 20, MaxResponseBytes: 1 << 20, MaxPages: 10, MaxChangedPaths: 1000, MaxAttempts: 4, RunTimeout: 30 * time.Minute, CloseTimeout: 10 * time.Second, LeaseTTL: 32 * time.Minute, PollInterval: 2 * time.Second, RetryMaxDuration: 2 * time.Minute, RetryMaxWait: time.Minute, RetryInitialBackoff: time.Second, RetryMaxBackoff: 30 * time.Second, Authorization: githubpr.AuthorizationConfig{Installations: map[int64]map[int64]bool{installation: {repository: true}}, Actors: map[int64]bool{actor: true}, Selections: map[string]githubpr.Selection{selection.Alias: selection}, Forks: map[int64]map[int64]bool{}}}
	controlPlaneProject := "repository-" + strconv.FormatInt(repository, 10)
	if configuredWorkflow != nil {
		// Product workflow state is tenant-scoped by the immutable repository
		// project carried by ProjectKey, not by a second GitHub-derived alias.
		controlPlaneProject = configuredWorkflow.RepositoryID
	}
	controlPlane, admission, controlPlaneErr := composeGitHubControlPlane(ctx, clock, config.RunTimeout, controlPlaneProject)
	if controlPlaneErr != nil {
		return nil, "", controlPlaneErr
	}
	var controlPlaneQueue controlplane.JobQueue
	if controlPlane != nil {
		controlPlaneQueue, _ = controlPlane.(controlplane.JobQueue)
		if security != nil && controlPlaneQueue != nil {
			workerContext, bindErr := security.AuthenticateSurface(ctx, tenantsecurity.SurfaceWorker, "github-worker")
			if bindErr != nil {
				return nil, "", bindErr
			}
			gate, gateErr := newTenantQuotaGate(workerContext, security, tenantsecurity.SurfaceWorker)
			if gateErr != nil {
				return nil, "", gateErr
			}
			controlPlaneQueue, err = controlplane.NewQuotaJobQueue(controlPlaneQueue, tenantClaimQuotaGate{tenantQuotaGate: gate})
			if err != nil {
				return nil, "", err
			}
		}
	}
	var controlPlaneAuthority controlplane.AuthorityEnvelopeStore
	var controlPlaneOutbox controlplane.EffectOutbox
	var postgresDomains *controlplanepg.DomainStores
	var controlPlaneDomains interface {
		Ready(context.Context) error
	}
	if controlPlane != nil {
		controlPlaneAuthority, _ = controlPlane.(controlplane.AuthorityEnvelopeStore)
		controlPlaneOutbox, _ = controlPlane.(controlplane.EffectOutbox)
		postgresStore, ok := controlPlane.(*controlplanepg.Store)
		if !ok {
			return nil, "", errors.New("postgres domain adapters unavailable")
		}
		limits := map[string]environment.CapacityLimits{controlPlaneProject: {Environments: 64, CPUMilli: 64000, MemoryBytes: 1 << 40, ObjectCount: 100000, MaxTTL: config.RunTimeout}}
		if configuredWorkflow != nil {
			workflowSelection, selectionErr := configuredWorkflow.EnvironmentCatalog.Resolve(configuredWorkflow.TargetAlias)
			if selectionErr != nil {
				postgresStore.Close()
				return nil, "", selectionErr
			}
			limits[workflowSelection.ProjectID] = environment.CapacityLimits{Environments: 64, CPUMilli: 64000, MemoryBytes: 1 << 40, ObjectCount: 100000, MaxTTL: config.RunTimeout}
		}
		postgresDomains, err = postgresStore.DomainStores(controlplanepg.DomainStoreConfig{Clock: clock, FunctionalTTL: 2 * time.Minute, CapacityLimits: limits})
		if err != nil {
			postgresStore.Close()
			return nil, "", err
		}
		controlPlaneDomains = postgresDomains
		if configuredWorkflow != nil {
			configurationPersistence := &workflowPersistence{projectConfig: postgresDomains.ProjectConfig}
			if bindErr := bindProductWorkflowConfiguration(ctx, configuredWorkflow, configurationPersistence); bindErr != nil {
				postgresStore.Close()
				return nil, "", bindErr
			}
			selection = githubpr.Selection{Alias: configuredWorkflow.TargetAlias, Secretless: configuredWorkflow.TargetKind == "preview"}
			workflowPersistence := &workflowPersistence{workflows: postgresDomains.Workflow, artifacts: postgresDomains.PlanArtifacts, executables: postgresDomains.ExecutablePlan, approvals: postgresDomains.Approval, environments: postgresDomains.Environment, capacity: postgresDomains.Capacity, cleanupLedger: postgresDomains.CleanupLedger, cleanup: postgresDomains.Cleanup, functional: postgresDomains.Functional, journal: postgresDomains.Functional, packages: postgresDomains.Packages, corrections: postgresDomains.Correction, fixtures: postgresDomains.Fixtures, context: postgresDomains.Context, projectConfig: postgresDomains.ProjectConfig, resourceArtifacts: postgresDomains.ResourceArtifacts, functionalRuntime: postgresStore}
			var workflowCLI *validationworkflow.CLI
			var composeErr error
			if security != nil {
				subject := "installation:" + strconv.FormatInt(installation, 10) + ":actor:" + strconv.FormatInt(actor, 10)
				workflowContext, bindErr := security.AuthenticateSurface(ctx, tenantsecurity.SurfaceGitHub, subject)
				if bindErr != nil {
					postgresStore.Close()
					return nil, "", bindErr
				}
				workflowCLI, composeErr = composeWorkflowCLIWithGitHubTenantSecurity(workflowContext, io.Discard, io.Discard, authorityAdapter, workflowPersistence, security)
			} else {
				workflowCLI, composeErr = composeWorkflowCLIWithPersistence(ctx, io.Discard, io.Discard, workflowPersistence)
			}
			if composeErr != nil {
				postgresStore.Close()
				return nil, "", composeErr
			}
			validation, composeErr = githubWorkflowValidation(workflowCLI, *configuredWorkflow)
			if composeErr != nil {
				postgresStore.Close()
				return nil, "", composeErr
			}
		}
	}
	secretResolver := controlPlaneSecretResolver{values: map[string]string{"github-app-private-key": string(privateKey)}}
	productEffect := &githubRuntimeEffect{appID: appID, apiOrigin: apiOrigin, authorization: config.Authorization, validation: validation, http: httpClient, clock: clock}
	var productAuthorize func(context.Context, githubpr.ProductIdentity) error
	var productWorkerAuthorize func(context.Context, githubpr.WorkerIdentity) error
	if security != nil {
		productAuthorize = func(authorizeContext context.Context, identity githubpr.ProductIdentity) error {
			subject := "installation:" + strconv.FormatInt(identity.InstallationID, 10) + ":actor:" + strconv.FormatInt(identity.ActorID, 10)
			authenticated, bindErr := security.AuthenticateSurface(authorizeContext, tenantsecurity.SurfaceGitHub, subject)
			if bindErr != nil {
				return bindErr
			}
			_, authorizeErr := security.Gateway.Authorize(authenticated, tenantsecurity.SurfaceGitHub, tenantsecurity.ActionRunAdmit, tenantsecurity.ResourceRun)
			return authorizeErr
		}
		productWorkerAuthorize = func(authorizeContext context.Context, identity githubpr.WorkerIdentity) error {
			authenticated, bindErr := security.AuthenticateSurface(authorizeContext, tenantsecurity.SurfaceWorker, identity.WorkerID)
			if bindErr != nil {
				return bindErr
			}
			capability, authorizeErr := security.Gateway.Authorize(authenticated, tenantsecurity.SurfaceWorker, tenantsecurity.ActionRunRead, tenantsecurity.ResourceRun)
			if authorizeErr != nil {
				return authorizeErr
			}
			if capability.Scope.OrganizationID != identity.OrganizationID || capability.Scope.ProjectID != identity.ProjectID || identity.Fence == 0 {
				return errors.New("worker tenant scope unavailable")
			}
			return nil
		}
	}
	runtime, err := githubpr.ComposeRuntime(ctx, config, githubpr.CompositionSecrets{WebhookSecret: secret}, githubpr.RuntimeDependencies{Credentials: tokenSource, HTTP: httpClient, Packages: externalagent.VerifiedPackageReader{}, Validation: validation, Clock: clock, Jitter: githubpr.RandomJitter{}, IDs: githubpr.RandomIDs{}, ProductAuthorize: productAuthorize, ProductWorkerAuthorize: productWorkerAuthorize, Review: reviewHandler, ReviewOwner: reviewOwner, ControlPlane: controlPlane, ControlPlaneAdmission: admission, ControlPlaneQueue: controlPlaneQueue, ControlPlaneAuthority: controlPlaneAuthority, ControlPlaneOutbox: controlPlaneOutbox, ControlPlaneSecrets: secretResolver, ControlPlaneEffect: productEffect, ControlPlaneDomains: controlPlaneDomains, ControlPlaneOrganizationID: os.Getenv("FEATUREPROOF_ORGANIZATION_ID"), ControlPlaneProjectID: controlPlaneProject, ControlPlaneWorkerID: "github-worker"})
	if err != nil {
		if closer, ok := controlPlane.(interface{ Close() }); ok {
			closer.Close()
		}
		if closer, ok := validation.(interface{ Close(context.Context) error }); ok {
			_ = closer.Close(context.Background())
		}
		return nil, "", err
	}
	address := os.Getenv("FEATUREPROOF_GITHUB_LISTEN")
	if address == "" {
		address = "127.0.0.1:8081"
	}
	return runtime, address, nil
}

// bindProductWorkflowConfiguration materializes project-owned selectors from the authoritative backend before any ingress advertises them.
func bindProductWorkflowConfiguration(ctx context.Context, config *workflowConfig, persistence *workflowPersistence) error {
	if config == nil {
		return errors.New("workflow configuration unavailable")
	}
	current, err := user.Current()
	if err != nil || current.Uid == "" {
		return errors.New("local identity unavailable")
	}
	composition, err := composeWorkflowProjectConfiguration(ctx, "", current.Uid, config.RepositoryID, persistence, true)
	if err != nil {
		return err
	}
	active, err := composition.Gateway.Execute(ctx, projectconfig.GatewayRequest{Operation: projectconfig.OperationShowActive, Authority: composition.Authority, ProjectKey: composition.Key})
	if err != nil || active.Effective == nil {
		return errors.New("active project configuration unavailable")
	}
	if err = applyBoundWorkflowSelections(config, *active.Effective); err != nil {
		return err
	}
	return validateBoundWorkflowConfig(*config)
}

// githubWorkflowValidation exposes one composed workflow through the GitHub gateway without changing its persistence backend.
func githubWorkflowValidation(workflowCLI *validationworkflow.CLI, config workflowConfig) (githubpr.ValidationGateway, error) {
	if workflowCLI == nil || workflowCLI.Service == nil {
		return nil, errors.New("workflow service unavailable")
	}
	approvedContract, err := config.approvedContractReference()
	if err != nil {
		return nil, err
	}
	target := validationworkflow.TargetSelection{}
	if configuredTarget, ok := workflowCLI.DirectTargets[config.TargetAlias]; ok {
		target.Direct = &configuredTarget
	} else if configuredPreview, ok := workflowCLI.PreviewTargets[config.TargetAlias]; ok {
		target.Preview = &configuredPreview
	} else {
		return nil, errors.New("workflow target unavailable")
	}
	command := validationworkflow.StartCommand{Principal: workflowCLI.Principal, IdempotencyKey: "github-template", SpecificationRef: config.SpecificationPath, ContractRef: config.ContractPath, Provider: config.Provider, Model: config.Model, CompositionRef: workflowCLI.AuthProfiles[config.AuthProfile], RuntimeProfile: config.RuntimeProfile, RepositoryID: config.RepositoryID, Revision: config.CommitOID, Approval: validationworkflow.ApprovalRequired, Target: target, ApprovedContract: approvedContract}
	gateway := validationworkflow.GitHubGateway{Service: workflowCLI.Service, Principal: workflowCLI.Principal, Commands: map[string]validationworkflow.StartCommand{config.TargetAlias: command}}
	return workflowGitHubGateway{GitHubGateway: gateway, service: workflowCLI.Service, principal: workflowCLI.Principal}, nil
}

// GitHub worker/outbox route instead of exposing Service.Decide to the harness.
type workflowGitHubGateway struct {
	validationworkflow.GitHubGateway
	service   *validationworkflow.Service
	principal string
}

// Approve applies an exact durable approval revision and projects the resulting
// workflow through the same GitHub state adapter used by validation admission.
func (gateway workflowGitHubGateway) Approve(ctx context.Context, command githubpr.WebhookCommand) (githubpr.RunState, error) {
	if gateway.service == nil || command.ReviewOperation != "decide" || command.ReviewID == "" || command.ReviewRevision == 0 || command.ReviewAction != "accept_all" {
		return githubpr.RunState{}, controlplane.ErrUnauthorized
	}
	if _, err := gateway.service.Decide(ctx, validationworkflow.DecisionCommand{Principal: gateway.principal, Handle: command.ReviewID, DecisionID: command.DeliveryID, Revision: command.ReviewRevision, Action: validationworkflow.DecisionApprove}); err != nil {
		return githubpr.RunState{}, err
	}
	return gateway.Status(ctx, command.ReviewID)
}

// composeGitHubControlPlane selects existing local stores explicitly or one migrated/readiness-checked PostgreSQL authority without fallback.
func composeGitHubControlPlane(ctx context.Context, clock githubpr.Clock, runTimeout time.Duration, productProjectID string) (controlplane.IngressStore, func(context.Context, githubpr.WebhookCommand) (controlplane.IngressAdmission, error), error) {
	mode := os.Getenv("FEATUREPROOF_CONTROL_PLANE_BACKEND")
	if mode == "" || mode == string(controlplane.BackendLocal) {
		return nil, nil, nil
	}
	if mode != string(controlplane.BackendPostgres) {
		return nil, nil, errors.New("control plane backend unavailable")
	}
	organizationID := os.Getenv("FEATUREPROOF_ORGANIZATION_ID")
	if organizationID == "" {
		return nil, nil, errors.New("control plane organization unavailable")
	}
	identity := os.Getenv("FEATUREPROOF_CONTROL_PLANE_IDENTITY")
	if identity == "" {
		identity = "github-postgres"
	}
	observer := controlPlaneObserver{logger: slog.New(slog.NewJSONHandler(os.Stderr, nil))}
	store, err := controlplanepg.Open(ctx, controlplanepg.Config{DSN: os.Getenv("FEATUREPROOF_CONTROL_PLANE_POSTGRES_DSN"), BackendIdentity: identity, MaxConnections: 16, StatementTimeout: 5 * time.Second, MaxLease: 5 * time.Minute, Observer: observer, IsolatedLoopbackTest: isolatedPostgresGateAuthorized()})
	if err != nil {
		return nil, nil, err
	}
	if err = store.Migrate(ctx); err != nil {
		store.Close()
		return nil, nil, err
	}
	if err = store.Ready(ctx); err != nil {
		store.Close()
		return nil, nil, err
	}
	build := func(_ context.Context, command githubpr.WebhookCommand) (controlplane.IngressAdmission, error) {
		now := clock.Now()
		sum := sha256.Sum256([]byte(command.DeliveryID))
		identityHash := hex.EncodeToString(sum[:])
		runID, jobID := "github-"+identityHash, "ingress-"+identityHash
		projectID := productProjectID
		if projectID == "" {
			projectID = "repository-" + strconv.FormatInt(command.RepositoryID, 10)
		}
		epoch, sealErr := controlplane.SealBackendEpoch(controlplane.BackendEpoch{OrganizationID: organizationID, RunID: runID, ProjectID: projectID, Epoch: 1, Mode: controlplane.BackendPostgres, BackendIdentity: identity, SchemaVersion: 1, CreatedAt: now})
		if sealErr != nil {
			return controlplane.IngressAdmission{}, sealErr
		}
		job, sealErr := controlplane.SealJob(controlplane.Job{OrganizationID: organizationID, JobID: jobID, ProjectID: projectID, RunID: runID, BackendEpoch: 1, BindingDigest: epoch.Digest, Phase: controlplane.PhaseExecute, State: controlplane.JobQueued, InputDigest: command.PayloadDigest, CreatedAt: now, AvailableAt: now, Deadline: now.Add(runTimeout)})
		if sealErr != nil {
			return controlplane.IngressAdmission{}, sealErr
		}
		grant, sealErr := controlplane.SealAuthorityGrant(controlplane.AuthorityGrant{OrganizationID: organizationID, ProjectID: projectID, RunID: runID, BackendEpoch: epoch.Epoch, BindingDigest: epoch.Digest, Phase: controlplane.PhaseExecute, InputReferences: []string{"ingress"}, AllowedCapabilities: []string{"http.execute"}, TargetBindingRef: command.SelectionAlias, TargetBindingDigest: command.PayloadDigest, SecretReferenceIDs: []string{"github-app-private-key"}})
		if sealErr != nil {
			return controlplane.IngressAdmission{}, sealErr
		}
		intent := controlplane.EffectIntent{OrganizationID: organizationID, OperationID: jobID + "-dispatch", IdempotencyKey: jobID, ProjectID: projectID, RunID: runID, Phase: job.Phase, BindingDigest: epoch.Digest, InputDigest: command.PayloadDigest, PayloadRef: "ingress:" + runID, PayloadDigest: command.PayloadDigest, ReconcileMethod: "reload_ingress"}
		fields := map[string]string{"delivery_id": command.DeliveryID, "event": command.Event, "installation_id": strconv.FormatInt(command.InstallationID, 10), "repository_id": strconv.FormatInt(command.RepositoryID, 10), "pull_request_number": strconv.FormatInt(command.PullRequestNumber, 10), "actor_id": strconv.FormatInt(command.ActorID, 10), "selection_alias": command.SelectionAlias, "payload_digest": command.PayloadDigest}
		if command.ReviewOperation != "" {
			fields["review_operation"] = command.ReviewOperation
			fields["review_revision"] = strconv.FormatUint(command.ReviewRevision, 10)
			if command.ReviewID != "" {
				fields["review_id"] = command.ReviewID
			}
			if command.ReviewAction != "" {
				fields["review_action"] = command.ReviewAction
			}
		}
		if command.MemoryOperation != "" {
			fields["memory_operation"] = command.MemoryOperation
			fields["memory_expected_sequence"] = strconv.FormatUint(command.MemoryExpectedSequence, 10)
			fields["memory_expected_version"] = strconv.FormatUint(command.MemoryExpectedVersion, 10)
			if command.MemoryID != "" {
				fields["memory_id"] = command.MemoryID
			}
			if command.MemoryDecisionID != "" {
				fields["memory_decision_id"] = command.MemoryDecisionID
			}
		}
		return controlplane.IngressAdmission{Envelope: controlplane.InboxEnvelope{OrganizationID: organizationID, Source: "github", ExternalID: command.DeliveryID, IdempotencyKey: command.DeliveryID, ProjectID: projectID, RequestDigest: command.PayloadDigest, CommandKind: "validate", CommandFields: fields}, Epoch: epoch, Job: job, Grant: grant, Intent: intent}, nil
	}
	return store, build, nil
}

// controlPlaneSecretResolver is the executable-only JIT boundary; it never
// persists or logs credential values and returns only explicitly granted refs.
type controlPlaneSecretResolver struct{ values map[string]string }

// githubRuntimeEffect owns no credential and constructs a short-lived GitHub capability only from one authorized JIT request.
type githubRuntimeEffect struct {
	appID         int64
	apiOrigin     string
	authorization githubpr.AuthorizationConfig
	validation    githubpr.ValidationGateway
	http          githubpr.HTTPDoer
	clock         githubpr.Clock
}

// Reconcile resolves the existing workflow idempotency receipt before any fresh GitHub mutation.
func (effect *githubRuntimeEffect) Reconcile(ctx context.Context, request githubpr.ControlPlaneEffectRequest) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if effect == nil || request.Intent.IdempotencyKey == "" || request.Claim.Fence == 0 {
		return "", false, controlplane.ErrUnauthorized
	}
	reconciler, ok := effect.validation.(githubpr.ValidationIdempotencyReconciler)
	if !ok {
		return "", false, controlplane.ErrUnavailable
	}
	run, found, err := reconciler.ResolveIdempotency(ctx, "github:"+request.Intent.IdempotencyKey)
	if err != nil || !found {
		return "", false, err
	}
	receipt := sha256.Sum256([]byte(request.Intent.OperationID + "\x00" + run.RunHandle))
	return hex.EncodeToString(receipt[:]), true, nil
}

// Execute uses the resolved private key to build one ephemeral adapter and submits the exact idempotent validation command.
func (effect *githubRuntimeEffect) Execute(ctx context.Context, request githubpr.ControlPlaneEffectRequest) (string, error) {
	privateKey := []byte(request.Secrets["github-app-private-key"])
	if effect == nil || len(privateKey) == 0 || effect.validation == nil || effect.http == nil || effect.clock == nil || request.Intent.OperationID != request.Claim.OperationID || request.Intent.IdempotencyKey == "" {
		return "", controlplane.ErrUnauthorized
	}
	apiOrigin := effect.apiOrigin
	if apiOrigin == "" {
		apiOrigin = "https://api.github.com"
	}
	tokens, err := githubpr.NewGitHubAppTokenSource(githubpr.GitHubAppTokenConfig{AppID: effect.appID, BaseURL: apiOrigin, APIVersion: "2022-11-28", PrivateKeyPEM: privateKey, HTTP: effect.http, Clock: effect.clock, MaxResponse: 1 << 20})
	for index := range privateKey {
		privateKey[index] = 0
	}
	if err != nil {
		return "", err
	}
	adapter, err := githubpr.NewRESTAdapter(githubpr.RESTConfig{BaseURL: apiOrigin, APIVersion: "2022-11-28", AppID: effect.appID, Tokens: tokens, HTTP: effect.http, Clock: effect.clock, MaxResponse: 1 << 20, MaxPages: 10, MaxChangedPaths: 1000})
	if err != nil {
		return "", err
	}
	command := request.Command
	snapshot, err := adapter.PullRequest(ctx, command.InstallationID, command.RepositoryID, command.PullRequestNumber)
	if err != nil {
		return "", err
	}
	selection, err := (githubpr.StaticAuthorizer{Config: effect.authorization}).Authorize(ctx, snapshot, command.ActorID, command.SelectionAlias)
	if err != nil {
		return "", err
	}
	var run githubpr.RunState
	if command.ReviewOperation != "" {
		approver, ok := effect.validation.(interface {
			Approve(context.Context, githubpr.WebhookCommand) (githubpr.RunState, error)
		})
		if !ok {
			return "", controlplane.ErrUnauthorized
		}
		run, err = approver.Approve(ctx, command)
	} else {
		run, err = effect.validation.Submit(ctx, selection, snapshot, "github:"+request.Intent.IdempotencyKey)
	}
	if err != nil {
		return "", err
	}
	receipt := sha256.Sum256([]byte(request.Intent.OperationID + "\x00" + run.RunHandle))
	return hex.EncodeToString(receipt[:]), nil
}

// Resolve copies the requested composition-owned credentials after the worker
// has revalidated its persisted epoch, lease, fence and phase grant.
func (resolver controlPlaneSecretResolver) Resolve(ctx context.Context, references []string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolved := make(map[string]string, len(references))
	for _, reference := range references {
		value, ok := resolver.values[reference]
		if !ok || value == "" {
			return nil, controlplane.ErrUnauthorized
		}
		resolved[reference] = value
	}
	return resolved, nil
}

// requiredPositiveEnv parses one mandatory positive numeric GitHub identity.
func requiredPositiveEnv(name string) (int64, error) {
	value, err := strconv.ParseInt(os.Getenv(name), 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("github identity unavailable")
	}
	return value, nil
}

// requiredEnv returns one mandatory non-secret configuration value.
func requiredEnv(name string) string { return os.Getenv(name) }

// isolatedPostgresGateAuthorized exposes plaintext loopback only inside the explicit hermetic gate.
func isolatedPostgresGateAuthorized() bool {
	return os.Getenv("FEATUREPROOF_POSTGRES_GATE_AUTHORIZED") == "true"
}

func isExternalAgentCommand(args []string) bool {
	return len(args) > 0 && (args[0] == "capabilities" || args[0] == "run" || args[0] == "correction" || args[0] == "mcp")
}

// externalProjectResourceMaterializer derives every external-agent admission consumer and limit from its sealed run binding.
type externalProjectResourceMaterializer struct {
	principalIDs []string
	principalID  string
	selection    externalagent.Selection
	executor     externalagent.ApprovedExecutor
	artifacts    interface {
		GetArtifact(context.Context, projectconfig.ResourceDescriptor) ([]byte, error)
	}
}

// trustedPrincipalIDs preserves the historical single-host adapter while
// allowing per-request MCP subjects to share the same sealed resource set.
func (materializer externalProjectResourceMaterializer) trustedPrincipalIDs() []string {
	if len(materializer.principalIDs) > 0 {
		return materializer.principalIDs
	}
	if materializer.principalID != "" {
		return []string{materializer.principalID}
	}
	return nil
}

// PrepareAdmission validates current backing and builds consumers without persisting before the causal registry transaction.
func (materializer externalProjectResourceMaterializer) PrepareAdmission(_ context.Context, binding projectconfig.RunConfigurationBinding) (externalagent.ProjectResourceSnapshot, externalagent.ProjectBoundResources, error) {
	if projectconfig.ValidateRunBinding(binding) != nil || len(materializer.trustedPrincipalIDs()) == 0 || materializer.selection.Alias == "" || materializer.executor == nil || materializer.artifacts == nil {
		return externalagent.ProjectResourceSnapshot{}, externalagent.ProjectBoundResources{}, errors.New("external project resources unavailable")
	}
	descriptor := externalEnvironmentDescriptor(binding)
	if descriptor == nil || descriptor.Selection != materializer.selection.Alias {
		return externalagent.ProjectResourceSnapshot{}, externalagent.ProjectBoundResources{}, errors.New("external project selection unavailable")
	}
	payload, err := json.Marshal(materializer.selection.Value)
	if err != nil {
		return externalagent.ProjectResourceSnapshot{}, externalagent.ProjectBoundResources{}, errors.New("external project artifact unavailable")
	}
	resources, err := materializer.resourcesFromPayload(binding, *descriptor, payload)
	if err != nil {
		return externalagent.ProjectResourceSnapshot{}, externalagent.ProjectBoundResources{}, err
	}
	return externalagent.ProjectResourceSnapshot{Descriptor: *descriptor, Payload: payload}, resources, nil
}

// externalEnvironmentDescriptor returns the single environment identity carried by the sealed binding.
func externalEnvironmentDescriptor(binding projectconfig.RunConfigurationBinding) *projectconfig.ResourceDescriptor {
	for index := range binding.Effective.Descriptors {
		candidate := binding.Effective.Descriptors[index]
		if candidate.Kind == projectconfig.ResourceEnvironment && candidate.Selection != "" {
			copy := candidate
			return &copy
		}
	}
	return nil
}

// Materialize rejects descriptor drift and returns only artifacts authorized by the historical binding without republishing backing.
func (materializer externalProjectResourceMaterializer) Materialize(ctx context.Context, binding projectconfig.RunConfigurationBinding) (externalagent.ProjectBoundResources, error) {
	if projectconfig.ValidateRunBinding(binding) != nil || len(materializer.trustedPrincipalIDs()) == 0 || materializer.executor == nil || materializer.artifacts == nil {
		return externalagent.ProjectBoundResources{}, errors.New("external project resources unavailable")
	}
	descriptor := externalEnvironmentDescriptor(binding)
	if descriptor == nil {
		return externalagent.ProjectBoundResources{}, errors.New("external project selection unavailable")
	}
	payload, err := materializer.artifacts.GetArtifact(ctx, *descriptor)
	if err != nil {
		return externalagent.ProjectBoundResources{}, errors.New("external project artifact unavailable")
	}
	return materializer.resourcesFromPayload(binding, *descriptor, payload)
}

// resourcesFromPayload authenticates the selected alias and derives limits before consumers can be admitted or recovered.
func (materializer externalProjectResourceMaterializer) resourcesFromPayload(binding projectconfig.RunConfigurationBinding, descriptor projectconfig.ResourceDescriptor, payload []byte) (externalagent.ProjectBoundResources, error) {
	if len(materializer.trustedPrincipalIDs()) == 0 {
		return externalagent.ProjectBoundResources{}, errors.New("external project principal unavailable")
	}
	var request previewenv.ConsumerRequest
	if json.Unmarshal(payload, &request) != nil || request.Selection.Alias != descriptor.Selection {
		return externalagent.ProjectBoundResources{}, errors.New("external project artifact corrupt")
	}
	selection := externalagent.Selection{Alias: descriptor.Selection, Value: request}
	principalIDs := materializer.trustedPrincipalIDs()
	entries := make(map[string]map[string]externalagent.Selection, len(principalIDs))
	for _, principalID := range principalIDs {
		entries[principalID] = map[string]externalagent.Selection{selection.Alias: selection}
	}
	catalog, err := externalagent.NewAliasCatalog(entries)
	retention := time.Duration(binding.Effective.Constraints.RetentionDays) * 24 * time.Hour
	timeout := time.Duration(binding.Effective.Constraints.Limits.DurationSeconds) * time.Second
	if err != nil || retention <= 0 || timeout <= 0 {
		return externalagent.ProjectBoundResources{}, errors.New("external project limits unavailable")
	}
	return externalagent.ProjectBoundResources{Catalog: catalog, Executor: materializer.executor, Retention: retention, RunTimeout: timeout}, nil
}

// scopedExternalRegistry seals the composition-owned project capability onto
// every durable external-agent call, including supervisor recovery contexts.
type scopedExternalRegistry struct {
	registry externalagent.Registry
	scope    controlplane.ProjectScope
}

// externalContext attaches the trusted tenant scope without granting a run identity.
func (registry scopedExternalRegistry) externalContext(ctx context.Context) context.Context {
	return controlplane.WithBackendCoordinate(ctx, controlplane.BackendCoordinate{OrganizationID: registry.scope.OrganizationID, ProjectID: registry.scope.ProjectID})
}

// ClaimSubmit scopes idempotency serialization to the composed tenant project.
func (registry scopedExternalRegistry) ClaimSubmit(ctx context.Context, principalID, keyHash, requestHash string) (*externalagent.SubmissionClaim, error) {
	return registry.registry.(externalagent.ConfiguredAdmissionRegistry).ClaimSubmit(registry.externalContext(ctx), principalID, keyHash, requestHash)
}

// Reserve prevents an unconfigured run from escaping the composed tenant project.
func (registry scopedExternalRegistry) Reserve(ctx context.Context, record externalagent.RunRecord) (externalagent.ReserveResult, error) {
	return registry.registry.Reserve(registry.externalContext(ctx), record)
}

// ReserveConfigured binds the run and captured artifact to the same tenant project.
func (registry scopedExternalRegistry) ReserveConfigured(ctx context.Context, record externalagent.RunRecord, snapshot externalagent.ProjectResourceSnapshot) (externalagent.ReserveResult, error) {
	return registry.registry.(externalagent.ConfiguredAdmissionRegistry).ReserveConfigured(registry.externalContext(ctx), record, snapshot)
}

// Get hides equal handles owned by a different tenant project.
func (registry scopedExternalRegistry) Get(ctx context.Context, handle string) (externalagent.RunRecord, error) {
	return registry.registry.Get(registry.externalContext(ctx), handle)
}

// Transition applies lifecycle CAS only inside the composed tenant project.
func (registry scopedExternalRegistry) Transition(ctx context.Context, handle string, from, to externalagent.Phase, status externalagent.Outcome, packageDir, owner string, now time.Time) (externalagent.RunRecord, error) {
	return registry.registry.Transition(registry.externalContext(ctx), handle, from, to, status, packageDir, owner, now)
}

// ReserveCancel scopes cancellation idempotency to the composed tenant project.
func (registry scopedExternalRegistry) ReserveCancel(ctx context.Context, handle, keyHash, requestHash string) (externalagent.RunRecord, bool, error) {
	return registry.registry.ReserveCancel(registry.externalContext(ctx), handle, keyHash, requestHash)
}

// Recover terminalizes abandoned runs only inside the composed tenant project.
func (registry scopedExternalRegistry) Recover(ctx context.Context, owner string, now time.Time) error {
	return registry.registry.Recover(registry.externalContext(ctx), owner, now)
}

// RecoverTerminal returns exact terminalized records under the same trusted
// tenant coordinate for resource reconciliation.
func (registry scopedExternalRegistry) RecoverTerminal(ctx context.Context, owner string, now time.Time) ([]externalagent.RunRecord, error) {
	reporter, ok := registry.registry.(externalagent.RecoveryReportingRegistry)
	if !ok {
		return nil, externalagent.ErrConflict
	}
	return reporter.RecoverTerminal(registry.externalContext(ctx), owner, now)
}

// Events reads the tenant-scoped immutable external-agent journal.
func (registry scopedExternalRegistry) Events(ctx context.Context, handle string) ([]externalagent.RunEvent, error) {
	return registry.registry.Events(registry.externalContext(ctx), handle)
}

// MarkPublicationFailure records the safe failure cause under the tenant fence.
func (registry scopedExternalRegistry) MarkPublicationFailure(ctx context.Context, handle string, now time.Time) error {
	return registry.registry.MarkPublicationFailure(registry.externalContext(ctx), handle, now)
}

// ReleaseOwner preserves the registry lifecycle boundary after scoped execution.
func (registry scopedExternalRegistry) ReleaseOwner(handle string) {
	registry.registry.ReleaseOwner(handle)
}

// composeExternalAgentService enables preview scopes only after complete trusted composition.
func composeExternalAgentService() (*externalagent.Service, error) {
	return composeExternalAgentServiceWithTenantSecurity(nil)
}

// composeExternalAgentServiceWithTenantSecurity installs pre-effect quota
// admission for the productive external executor.
func composeExternalAgentServiceWithTenantSecurity(security *tenantSecurityComposition) (*externalagent.Service, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	current, err := user.Current()
	if err != nil || current.Uid == "" {
		return nil, errors.New("local identity unavailable")
	}
	var registry externalagent.Registry
	var configurationPersistence *workflowPersistence
	var postgresOwner *controlplanepg.Store
	keepPostgresOwner := false
	defer func() {
		if postgresOwner != nil && !keepPostgresOwner {
			postgresOwner.Close()
		}
	}()
	backend := os.Getenv("FEATUREPROOF_CONTROL_PLANE_BACKEND")
	switch backend {
	case "", string(controlplane.BackendLocal):
		registry, err = externalagent.NewLocalRegistry(filepath.Join(root, ".featureproof", "external-agent"))
	case string(controlplane.BackendPostgres):
		identity := os.Getenv("FEATUREPROOF_CONTROL_PLANE_IDENTITY")
		if identity == "" {
			identity = "external-agent-postgres"
		}
		observer := controlPlaneObserver{logger: slog.New(slog.NewJSONHandler(os.Stderr, nil))}
		postgresOwner, err = controlplanepg.Open(context.Background(), controlplanepg.Config{DSN: os.Getenv("FEATUREPROOF_CONTROL_PLANE_POSTGRES_DSN"), BackendIdentity: identity, MaxConnections: 8, StatementTimeout: 5 * time.Second, MaxLease: 5 * time.Minute, Observer: observer, IsolatedLoopbackTest: isolatedPostgresGateAuthorized()})
		if err == nil {
			err = postgresOwner.Migrate(context.Background())
		}
		if err == nil {
			err = postgresOwner.Ready(context.Background())
		}
		if err == nil {
			registry = postgresOwner.ExternalAgentRegistry()
			configurationPersistence = &workflowPersistence{projectConfig: postgresOwner.ProjectConfiguration()}
		}
	default:
		return nil, errors.New("control plane backend unavailable")
	}
	if err != nil || registry == nil {
		return nil, errors.New("external registry unavailable")
	}
	artifactRegistry, ok := registry.(interface {
		GetArtifact(context.Context, projectconfig.ResourceDescriptor) ([]byte, error)
	})
	if !ok {
		return nil, errors.New("external artifact registry unavailable")
	}
	repositoryID := os.Getenv("FEATUREPROOF_PROJECT_CONFIG_REPOSITORY_ID")
	var projectConfiguration workflowProjectConfiguration
	if security != nil {
		authenticated, bindErr := security.AuthenticateSurface(context.Background(), tenantsecurity.SurfaceExternalAgent, current.Uid)
		if bindErr != nil {
			return nil, bindErr
		}
		configCapability, authorizeErr := security.Gateway.Authorize(authenticated, tenantsecurity.SurfaceExternalAgent, tenantsecurity.ActionConfigRead, tenantsecurity.ResourceConfig)
		if authorizeErr != nil {
			return nil, authorizeErr
		}
		projectConfiguration, err = composeWorkflowProjectConfigurationWithTenant(authenticated, current.Uid, repositoryID, configurationPersistence, true, security.Store, configCapability)
	} else {
		projectConfiguration, err = composeWorkflowProjectConfiguration(context.Background(), root, current.Uid, repositoryID, configurationPersistence, true)
	}
	if err != nil {
		return nil, err
	}
	active, err := projectConfiguration.Gateway.Execute(context.Background(), projectconfig.GatewayRequest{Operation: projectconfig.OperationShowActive, Authority: projectConfiguration.Authority, ProjectKey: projectConfiguration.Key})
	if err != nil || active.Effective == nil {
		return nil, errors.New("active project configuration unavailable")
	}
	if _, ok = registry.(externalagent.ConfiguredAdmissionRegistry); !ok {
		return nil, errors.New("external configured registry unavailable")
	}
	registry = scopedExternalRegistry{registry: registry, scope: controlplane.ProjectScope{OrganizationID: projectConfiguration.Key.OrganizationID, ProjectID: projectConfiguration.Key.ProjectID}}
	preview, configured, err := composeConfiguredPreview(context.Background(), root)
	if err != nil {
		return nil, err
	}
	scopes := map[string]bool{}
	selections := map[string]externalagent.Selection{}
	executor := externalagent.ApprovedExecutor(externalagent.FailClosedExecutor{})
	if configured {
		if !active.Effective.AllowsReference(projectconfig.ResourceEnvironment, preview.Request.Selection.Alias) {
			return nil, errors.New("external selection outside project configuration")
		}
		scopes = map[string]bool{externalagent.ScopeCreate: true, externalagent.ScopeRead: true, externalagent.ScopeCancel: true}
		selections[preview.Request.Selection.Alias] = externalagent.Selection{Alias: preview.Request.Selection.Alias, Value: preview.Request}
		executor = previewenv.ExternalExecutor{Coordinator: preview.Coordinator, Recovery: preview.Recovery}
	}
	localIdentity, err := externalagent.NewLocalIdentity(externalagent.Principal{ID: current.Uid, Scopes: scopes})
	if err != nil {
		return nil, err
	}
	var identity externalagent.IdentitySource = localIdentity
	principalIDs := []string{current.Uid}
	if security != nil {
		if authenticated, authErr := security.AuthenticateSurface(context.Background(), tenantsecurity.SurfaceExternalAgent, current.Uid); authErr == nil {
			if capability, authorizeErr := security.Gateway.Authorize(authenticated, tenantsecurity.SurfaceExternalAgent, tenantsecurity.ActionRunRead, tenantsecurity.ResourceRun); authorizeErr == nil {
				principalIDs = []string{capability.Scope.PrincipalID}
			}
		}
		if authenticator, authErr := loadTenantMCPAuthenticator(security, tenantsecurity.SurfaceExternalAgent, "FEATUREPROOF_EXTERNAL_AGENT_MCP_CREDENTIALS"); authErr == nil {
			for credential := range authenticator.profiles {
				_, principalID, _, authorizeErr := authenticator.authorize(context.Background(), credential, "submit")
				if authorizeErr != nil {
					return nil, authorizeErr
				}
				if !slices.Contains(principalIDs, principalID) {
					principalIDs = append(principalIDs, principalID)
				}
			}
		}
	}
	if security != nil {
		identity = tenantExternalIdentity{next: externalagent.RequestIdentity{Fallback: localIdentity}, security: security}
	}
	catalogEntries := make(map[string]map[string]externalagent.Selection, len(principalIDs))
	for _, principalID := range principalIDs {
		catalogEntries[principalID] = selections
	}
	catalog, err := externalagent.NewAliasCatalog(catalogEntries)
	if err != nil {
		return nil, err
	}
	owner, err := (externalagent.RandomIDs{}).Next()
	if err != nil {
		return nil, err
	}
	if configured {
		recovery, recoveryErr := previewenv.StartRecoverySupervisor(preview.Coordinator)
		if recoveryErr != nil {
			return nil, recoveryErr
		}
		preview.Recovery = recovery
		executor = previewenv.ExternalExecutor{Coordinator: preview.Coordinator, Recovery: recovery}
	}
	if security != nil {
		authenticated, bindErr := security.AuthenticateSurface(context.Background(), tenantsecurity.SurfaceExternalAgent, current.Uid)
		if bindErr != nil {
			return nil, bindErr
		}
		gate, gateErr := newTenantQuotaGate(authenticated, security, tenantsecurity.SurfaceExternalAgent)
		if gateErr != nil {
			return nil, gateErr
		}
		quotaExecutor, quotaErr := newQuotaExternalExecutor(executor, gate)
		err = quotaErr
		if err != nil {
			return nil, err
		}
		executor = quotaExecutor
		quotaRegistry, registryErr := newQuotaExternalRegistry(registry, gate)
		err = registryErr
		if err != nil {
			return nil, err
		}
		quotaRegistry.reconciler = quotaExecutor
		registry = quotaRegistry
	}
	retention := time.Duration(active.Effective.Constraints.RetentionDays) * 24 * time.Hour
	runTimeout := time.Duration(active.Effective.Constraints.Limits.DurationSeconds) * time.Second
	if retention <= 0 || runTimeout <= 0 {
		return nil, errors.New("project configuration limits unavailable")
	}
	selection := externalagent.Selection{Alias: "unavailable"}
	if configured {
		selection = selections[preview.Request.Selection.Alias]
	}
	service, serviceErr := externalagent.NewService(externalagent.ServiceDependencies{Identity: identity, Catalog: catalog, Registry: registry, Executor: executor, Packages: externalagent.VerifiedPackageReader{}, Clock: externalagent.WallClock{}, IDs: externalagent.RandomIDs{}, Owner: owner, Retention: retention, RunTimeout: runTimeout, RepositoryID: repositoryID, ProjectConfig: projectConfiguration.Resolver, ProjectResources: externalProjectResourceMaterializer{principalIDs: principalIDs, selection: selection, executor: executor, artifacts: artifactRegistry}})
	if serviceErr != nil && configured {
		_ = preview.Recovery.Close(context.Background())
	}
	if serviceErr == nil && postgresOwner != nil {
		keepPostgresOwner = true
	}
	return service, serviceErr
}

// writeExternalCompositionError emits the same closed envelope if composition itself fails.
func writeExternalCompositionError(output io.Writer) int {
	raw, _ := externalagent.CanonicalJSON(externalagent.Envelope{ContractVersion: externalagent.ContractVersion, Error: &externalagent.AppError{Code: externalagent.CodeInternal}})
	_, _ = output.Write(append(raw, '\n'))
	return 3
}

// main delegates process termination to the stable command exit code.
func main() { processExit(run(processArgs(), processInput, processOutput, processError)) }
