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
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
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
	"github.com/asierhr/featureProof/internal/projectmemory"
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
func (authorizer operatorUIDAuthorizer) Authorize(ctx context.Context, operation, projectID string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	allowedOperation := operation == "offline_import" || operation == "query_jobs" || operation == "retention"
	if !allowedOperation || projectID == "" || authorizer.allowed == "" || authorizer.actual != authorizer.allowed {
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
		return runProjectMemoryMCP(ctx, stdin, stdout)
	}
	if len(args) == 3 && args[0] == "memory" && args[1] == "api" && args[2] == "serve" {
		return runProjectMemoryAPI(ctx)
	}
	if len(args) >= 2 && args[0] == "memory" {
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
		return runGitHubServer(ctx, stderr)
	}
	if len(args) > 1 && args[0] == "workflow" {
		cli, err := composeWorkflowCLI(ctx, stdout, stderr)
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
			server := validationworkflow.MCPServer{Service: cli.Service, Principal: cli.Principal, AuthProfiles: cli.AuthProfiles, DirectTargets: cli.DirectTargets, PreviewTargets: cli.PreviewTargets, FlowAliases: cli.FlowAliases}
			if server.Serve(ctx, stdin, stdout) != nil {
				return 3
			}
			return 0
		}
		return cli.Run(ctx, args[1:], stdout)
	}
	if len(args) > 1 && args[0] == "contract-review" {
		if len(args) == 3 && args[1] == "mcp" && args[2] == "serve" {
			return runContractReviewMCP(ctx, stdin, stdout)
		}
		if len(args) == 3 && args[1] == "api" && args[2] == "serve" {
			return runContractReviewAPI(ctx)
		}
		return runContractReviewCommand(ctx, args[1:], stdout)
	}
	if isExternalAgentCommand(args) {
		service, err := composeExternalAgentService()
		if err != nil {
			return writeExternalCompositionError(stdout)
		}
		defer service.Close(context.WithoutCancel(ctx))
		if len(args) == 2 && args[0] == "mcp" && args[1] == "serve" {
			server, err := externalagent.NewMCPServer(service)
			if err != nil || server.Serve(ctx, stdin, stdout) != nil {
				return 3
			}
			return 0
		}
		return externalagent.RunCLI(ctx, args, stdout, service)
	}
	return localvalidation.RunCommand(localvalidation.TerminalSignalContext(ctx), args, stdin, stdout, stderr)
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
	store, err := controlplanepg.Open(ctx, controlplanepg.Config{DSN: os.Getenv("FEATUREPROOF_CONTROL_PLANE_POSTGRES_DSN"), BackendIdentity: requiredEnv("FEATUREPROOF_CONTROL_PLANE_IDENTITY"), MaxConnections: 8, StatementTimeout: 5 * time.Second, MaxLease: 5 * time.Minute, Observer: observer})
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
	query := controlplane.JobQuery{ProjectID: os.Getenv("FEATUREPROOF_CONTROL_PLANE_QUERY_PROJECT_ID"), State: controlplane.JobState(os.Getenv("FEATUREPROOF_CONTROL_PLANE_QUERY_STATE")), Cursor: os.Getenv("FEATUREPROOF_CONTROL_PLANE_QUERY_CURSOR"), Limit: limit, Now: time.Now().UTC(), CursorTTL: 5 * time.Minute}
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
	count, err := (controlplane.RetentionController{Store: store, Authorizer: authorizer}).Run(ctx, os.Getenv("FEATUREPROOF_CONTROL_PLANE_RETENTION_PROJECT_ID"), &controlplane.RetentionPolicy{BeforeUnix: before, BatchSize: batch})
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
	request := controlplane.OfflineImportRequest{ImportID: os.Getenv("FEATUREPROOF_CONTROL_PLANE_IMPORT_ID"), RunID: os.Getenv("FEATUREPROOF_CONTROL_PLANE_IMPORT_RUN_ID"), ProjectID: os.Getenv("FEATUREPROOF_CONTROL_PLANE_IMPORT_PROJECT_ID"), ExpectedHead: os.Getenv("FEATUREPROOF_CONTROL_PLANE_IMPORT_EXPECTED_HEAD"), ExpectedEpoch: epoch, PageSize: pageSize, Now: time.Now().UTC(), ReconcileTimeout: 30 * time.Second}
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
	runtime, address, err := composeGitHubRuntime(ctx)
	if err != nil {
		return 3
	}
	if err = runtime.Start(ctx); err != nil {
		_ = runtime.Close(context.WithoutCancel(ctx))
		return 3
	}
	server := &http.Server{Addr: address, Handler: runtime.Handler(), ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
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
		selection = githubpr.Selection{Alias: workflowConfig.TargetAlias, Secretless: workflowConfig.TargetKind == "preview"}
		if !postgresMode {
			workflowCLI, composeErr := composeWorkflowCLIWithGitHubAuthority(ctx, io.Discard, io.Discard, authorityAdapter)
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
	controlPlane, admission, controlPlaneErr := composeGitHubControlPlane(ctx, clock, config.RunTimeout)
	if controlPlaneErr != nil {
		return nil, "", controlPlaneErr
	}
	var controlPlaneQueue controlplane.JobQueue
	if controlPlane != nil {
		controlPlaneQueue, _ = controlPlane.(controlplane.JobQueue)
	}
	controlPlaneProject := "repository-" + strconv.FormatInt(repository, 10)
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
			workflowCLI, composeErr := composeWorkflowCLIWithPersistence(ctx, io.Discard, io.Discard, &workflowPersistence{workflows: postgresDomains.Workflow, artifacts: postgresDomains.PlanArtifacts, executables: postgresDomains.ExecutablePlan, approvals: postgresDomains.Approval, environments: postgresDomains.Environment, capacity: postgresDomains.Capacity, cleanupLedger: postgresDomains.CleanupLedger, cleanup: postgresDomains.Cleanup, functional: postgresDomains.Functional, journal: postgresDomains.Functional, packages: postgresDomains.Packages, corrections: postgresDomains.Correction, fixtures: postgresDomains.Fixtures, context: postgresDomains.Context, functionalRuntime: postgresStore})
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
	runtime, err := githubpr.ComposeRuntime(ctx, config, githubpr.CompositionSecrets{WebhookSecret: secret}, githubpr.RuntimeDependencies{Credentials: tokenSource, HTTP: httpClient, Packages: externalagent.VerifiedPackageReader{}, Validation: validation, Clock: clock, Jitter: githubpr.RandomJitter{}, IDs: githubpr.RandomIDs{}, Review: reviewHandler, ReviewOwner: reviewOwner, ControlPlane: controlPlane, ControlPlaneAdmission: admission, ControlPlaneQueue: controlPlaneQueue, ControlPlaneAuthority: controlPlaneAuthority, ControlPlaneOutbox: controlPlaneOutbox, ControlPlaneSecrets: secretResolver, ControlPlaneEffect: productEffect, ControlPlaneDomains: controlPlaneDomains, ControlPlaneProjectID: controlPlaneProject, ControlPlaneWorkerID: "github-worker"})
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
func composeGitHubControlPlane(ctx context.Context, clock githubpr.Clock, runTimeout time.Duration) (controlplane.IngressStore, func(context.Context, githubpr.WebhookCommand) (controlplane.IngressAdmission, error), error) {
	mode := os.Getenv("FEATUREPROOF_CONTROL_PLANE_BACKEND")
	if mode == "" || mode == string(controlplane.BackendLocal) {
		return nil, nil, nil
	}
	if mode != string(controlplane.BackendPostgres) {
		return nil, nil, errors.New("control plane backend unavailable")
	}
	identity := os.Getenv("FEATUREPROOF_CONTROL_PLANE_IDENTITY")
	if identity == "" {
		identity = "github-postgres"
	}
	observer := controlPlaneObserver{logger: slog.New(slog.NewJSONHandler(os.Stderr, nil))}
	store, err := controlplanepg.Open(ctx, controlplanepg.Config{DSN: os.Getenv("FEATUREPROOF_CONTROL_PLANE_POSTGRES_DSN"), BackendIdentity: identity, MaxConnections: 16, StatementTimeout: 5 * time.Second, MaxLease: 5 * time.Minute, Observer: observer})
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
		projectID := "repository-" + strconv.FormatInt(command.RepositoryID, 10)
		epoch, sealErr := controlplane.SealBackendEpoch(controlplane.BackendEpoch{RunID: runID, ProjectID: projectID, Epoch: 1, Mode: controlplane.BackendPostgres, BackendIdentity: identity, SchemaVersion: 1, CreatedAt: now})
		if sealErr != nil {
			return controlplane.IngressAdmission{}, sealErr
		}
		job, sealErr := controlplane.SealJob(controlplane.Job{JobID: jobID, ProjectID: projectID, RunID: runID, BackendEpoch: 1, BindingDigest: epoch.Digest, Phase: controlplane.PhaseExecute, State: controlplane.JobQueued, InputDigest: command.PayloadDigest, CreatedAt: now, AvailableAt: now, Deadline: now.Add(runTimeout)})
		if sealErr != nil {
			return controlplane.IngressAdmission{}, sealErr
		}
		grant, sealErr := controlplane.SealAuthorityGrant(controlplane.AuthorityGrant{ProjectID: projectID, RunID: runID, BackendEpoch: epoch.Epoch, BindingDigest: epoch.Digest, Phase: controlplane.PhaseExecute, InputReferences: []string{"ingress"}, AllowedCapabilities: []string{"http.execute"}, TargetBindingRef: command.SelectionAlias, TargetBindingDigest: command.PayloadDigest, SecretReferenceIDs: []string{"github-app-private-key"}})
		if sealErr != nil {
			return controlplane.IngressAdmission{}, sealErr
		}
		intent := controlplane.EffectIntent{OperationID: jobID + "-dispatch", IdempotencyKey: jobID, ProjectID: projectID, RunID: runID, Phase: job.Phase, BindingDigest: epoch.Digest, InputDigest: command.PayloadDigest, PayloadRef: "ingress:" + runID, PayloadDigest: command.PayloadDigest, ReconcileMethod: "reload_ingress"}
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
		return controlplane.IngressAdmission{Envelope: controlplane.InboxEnvelope{Source: "github", ExternalID: command.DeliveryID, IdempotencyKey: command.DeliveryID, ProjectID: projectID, RequestDigest: command.PayloadDigest, CommandKind: "validate", CommandFields: fields}, Epoch: epoch, Job: job, Grant: grant, Intent: intent}, nil
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

func isExternalAgentCommand(args []string) bool {
	return len(args) > 0 && (args[0] == "capabilities" || args[0] == "run" || args[0] == "correction" || args[0] == "mcp")
}

// composeExternalAgentService enables preview scopes only after complete trusted composition.
func composeExternalAgentService() (*externalagent.Service, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	current, err := user.Current()
	if err != nil || current.Uid == "" {
		return nil, errors.New("local identity unavailable")
	}
	preview, configured, err := composeConfiguredPreview(context.Background(), root)
	if err != nil {
		return nil, err
	}
	scopes := map[string]bool{}
	selections := map[string]externalagent.Selection{}
	executor := externalagent.ApprovedExecutor(externalagent.FailClosedExecutor{})
	if configured {
		scopes = map[string]bool{externalagent.ScopeCreate: true, externalagent.ScopeRead: true, externalagent.ScopeCancel: true}
		selections[preview.Request.Selection.Alias] = externalagent.Selection{Alias: preview.Request.Selection.Alias, Value: preview.Request}
		executor = previewenv.ExternalExecutor{Coordinator: preview.Coordinator, Recovery: preview.Recovery}
	}
	identity, err := externalagent.NewLocalIdentity(externalagent.Principal{ID: current.Uid, Scopes: scopes})
	if err != nil {
		return nil, err
	}
	catalog, err := externalagent.NewAliasCatalog(map[string]map[string]externalagent.Selection{current.Uid: selections})
	if err != nil {
		return nil, err
	}
	registry, err := externalagent.NewLocalRegistry(filepath.Join(root, ".featureproof", "external-agent"))
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
	service, serviceErr := externalagent.NewService(externalagent.ServiceDependencies{Identity: identity, Catalog: catalog, Registry: registry, Executor: executor, Packages: externalagent.VerifiedPackageReader{}, Clock: externalagent.WallClock{}, IDs: externalagent.RandomIDs{}, Owner: owner, Retention: 24 * time.Hour, RunTimeout: 30 * time.Minute})
	if serviceErr != nil && configured {
		_ = preview.Recovery.Close(context.Background())
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
