// Command featureproof exposes local on-demand validation.
package main

import (
	"context"
	"errors"
	"io"
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

var processArgs = commandArgs
var processInput io.Reader = os.Stdin
var processOutput io.Writer = os.Stdout
var processError io.Writer = os.Stderr
var processExit = os.Exit

// run installs the production signal context around the otherwise injectable command boundary.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runWithContext(ctx, args, stdin, stdout, stderr)
}

// runWithContext dispatches every product entrypoint under one caller-owned lifecycle for deterministic server shutdown.
func runWithContext(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
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

// runGitHubServer composes secrets at the executable boundary and owns bounded HTTP shutdown.
func runGitHubServer(ctx context.Context, stderr io.Writer) int {
	runtime, address, err := composeGitHubRuntime(ctx)
	if err != nil {
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
	tokenSource, err := githubpr.NewGitHubAppTokenSource(githubpr.GitHubAppTokenConfig{AppID: appID, BaseURL: "https://api.github.com", APIVersion: "2022-11-28", PrivateKeyPEM: privateKey, HTTP: httpClient, Clock: clock, MaxResponse: 1 << 20})
	if err != nil {
		return nil, "", err
	}
	authorityAdapter, err := githubpr.NewRESTAdapter(githubpr.RESTConfig{BaseURL: "https://api.github.com", APIVersion: "2022-11-28", AppID: appID, Tokens: tokenSource, HTTP: httpClient, Clock: clock, MaxResponse: 1 << 20, MaxPages: 10, MaxChangedPaths: 1000})
	if err != nil {
		return nil, "", err
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
	if os.Getenv("FEATUREPROOF_WORKFLOW_CONFIG") != "" {
		workflowCLI, workflowErr := composeWorkflowCLIWithGitHubAuthority(ctx, io.Discard, io.Discard, authorityAdapter)
		if workflowErr != nil {
			return nil, "", workflowErr
		}
		workflowConfig, workflowErr := loadWorkflowConfig()
		if workflowErr != nil {
			return nil, "", workflowErr
		}
		approvedContract, workflowErr := workflowConfig.approvedContractReference()
		if workflowErr != nil {
			return nil, "", workflowErr
		}
		target := validationworkflow.TargetSelection{}
		if configuredTarget, ok := workflowCLI.DirectTargets[workflowConfig.TargetAlias]; ok {
			target.Direct = &configuredTarget
		} else if configuredPreview, ok := workflowCLI.PreviewTargets[workflowConfig.TargetAlias]; ok {
			target.Preview = &configuredPreview
		} else {
			return nil, "", errors.New("workflow target unavailable")
		}
		command := validationworkflow.StartCommand{Principal: workflowCLI.Principal, IdempotencyKey: "github-template", SpecificationRef: workflowConfig.SpecificationPath, ContractRef: workflowConfig.ContractPath, Provider: workflowConfig.Provider, Model: workflowConfig.Model, CompositionRef: workflowCLI.AuthProfiles[workflowConfig.AuthProfile], RuntimeProfile: workflowConfig.RuntimeProfile, RepositoryID: workflowConfig.RepositoryID, Revision: workflowConfig.CommitOID, Approval: validationworkflow.ApprovalRequired, Target: target, ApprovedContract: approvedContract}
		validation = validationworkflow.GitHubGateway{Service: workflowCLI.Service, Principal: workflowCLI.Principal, Commands: map[string]validationworkflow.StartCommand{workflowConfig.TargetAlias: command}}
		selection = githubpr.Selection{Alias: workflowConfig.TargetAlias, Secretless: workflowConfig.TargetKind == "preview"}
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
	config := githubpr.ProductionConfig{AppID: appID, BotID: botID, Permissions: githubpr.RequiredPermissions(), APIOrigin: "https://api.github.com", APIVersion: "2022-11-28", EvidenceOrigin: requiredEnv("FEATUREPROOF_EVIDENCE_ORIGIN"), StoreDir: filepath.Join(root, ".featureproof", "github-pr"), MaxWebhookBytes: 1 << 20, MaxResponseBytes: 1 << 20, MaxPages: 10, MaxChangedPaths: 1000, MaxAttempts: 4, RunTimeout: 30 * time.Minute, CloseTimeout: 10 * time.Second, LeaseTTL: 32 * time.Minute, PollInterval: 2 * time.Second, RetryMaxDuration: 2 * time.Minute, RetryMaxWait: time.Minute, RetryInitialBackoff: time.Second, RetryMaxBackoff: 30 * time.Second, Authorization: githubpr.AuthorizationConfig{Installations: map[int64]map[int64]bool{installation: {repository: true}}, Actors: map[int64]bool{actor: true}, Selections: map[string]githubpr.Selection{selection.Alias: selection}, Forks: map[int64]map[int64]bool{}}}
	runtime, err := githubpr.ComposeRuntime(ctx, config, githubpr.CompositionSecrets{WebhookSecret: secret}, githubpr.RuntimeDependencies{Credentials: tokenSource, HTTP: httpClient, Packages: externalagent.VerifiedPackageReader{}, Validation: validation, Clock: clock, Jitter: githubpr.RandomJitter{}, IDs: githubpr.RandomIDs{}, Review: reviewHandler, ReviewOwner: reviewOwner})
	if err != nil {
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

// isExternalAgentCommand keeps the existing validate/eval dispatch stable.
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
