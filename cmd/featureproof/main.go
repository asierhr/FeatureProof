// Command featureproof exposes local on-demand validation.
package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/asierhr/featureProof/internal/agenteval"
	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/model"
	"github.com/asierhr/featureProof/internal/model/openai"
	"github.com/asierhr/featureProof/internal/model/openai/oauth"
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
	if len(args) >= 2 && args[0] == "eval" && args[1] == "agent" {
		return agenteval.RunCommand(ctx, args[2:], stdout, stderr, productionEvalComposer{})
	}
	return localvalidation.RunCommand(localvalidation.TerminalSignalContext(ctx), args, stdin, stdout, stderr)
}

// main delegates process termination to the stable command exit code.
func main() { processExit(run(processArgs(), processInput, processOutput, processError)) }
