// Command featureproof-model-smoke runs explicit real-provider OAuth and inference checks.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/model"
	"github.com/asierhr/featureProof/internal/model/openai"
	"github.com/asierhr/featureProof/internal/model/openai/oauth"
)

// smokeOptions contains non-secret selections required before real composition.
type smokeOptions struct {
	OptIn                                       bool
	Action, Provider, Model, Profile, StorePath string
	HostID, RedirectURI                         string
	Timeout, Duration                           time.Duration
	MaxCalls, MaxReplans                        int
	MaxTokens                                   int64
}

// smokeBackend isolates process behavior from OAuth and provider I/O in tests.
type smokeBackend interface {
	Provision(context.Context, smokeOptions) error
	Infer(context.Context, smokeOptions) (model.AttemptRecord, error)
}

// productionBackend composes the real OAuth, transport, adapter and neutral session.
type productionBackend struct{}

// wallClock supplies production time to the neutral model session.
type wallClock struct{}

// Now returns current UTC time for model duration budgets.
func (wallClock) Now() time.Time { return time.Now().UTC() }

// captureObserver retains only the safe attempt record printed by the smoke command.
type captureObserver struct{ record model.AttemptRecord }

// Observe accepts one allowlisted terminal record from the model session.
func (o *captureObserver) Observe(_ context.Context, record model.AttemptRecord) error {
	o.record = record
	return nil
}

// Provision performs one interactive Sign in with ChatGPT flow after explicit opt-in.
func (productionBackend) Provision(ctx context.Context, options smokeOptions) error {
	store, gateway, validator, client, err := productionOAuth(options.StorePath)
	if err != nil {
		return err
	}
	listener, err := oauth.NewLoopbackListener(options.RedirectURI)
	if err != nil {
		return err
	}
	service, err := oauth.NewService(oauth.Config{HostID: options.HostID, AppName: "FeatureProof", RedirectURI: options.RedirectURI}, oauth.SystemBrowser{}, listener, gateway, oauth.CryptoRandom{}, oauth.WallClock{}, validator, store)
	_ = client
	if err != nil {
		return err
	}
	return service.SignIn(ctx, options.Profile)
}

// Infer resolves one selected profile and performs one bounded Responses inference.
func (productionBackend) Infer(ctx context.Context, options smokeOptions) (model.AttemptRecord, error) {
	store, gateway, validator, client, err := productionOAuth(options.StorePath)
	if err != nil {
		return model.AttemptRecord{}, err
	}
	resolver, err := oauth.NewResolver(gateway, oauth.WallClock{}, validator, store)
	if err != nil {
		return model.AttemptRecord{}, err
	}
	credentials, err := oauth.NewProfileSource(resolver, options.Profile)
	if err != nil {
		return model.AttemptRecord{}, err
	}
	transport, err := openai.NewAuthenticatedTransport(credentials, client)
	if err != nil {
		return model.AttemptRecord{}, err
	}
	adapter, err := openai.New(transport, openai.Config{MaxRequestBytes: 64 * 1024, MaxHeaderBytes: 32 * 1024, MaxStreamBytes: 4 * 1024 * 1024, MaxOutputBytes: 64 * 1024})
	if err != nil {
		return model.AttemptRecord{}, err
	}
	observer := &captureObserver{}
	session, err := model.NewSession(model.Config{ProviderID: options.Provider, ModelID: options.Model, AuthProfileID: options.Profile, CallTimeout: options.Timeout, MaxRequestBytes: 64 * 1024, MaxStreamBytes: 4 * 1024 * 1024, MaxOutputBytes: 64 * 1024, Budget: model.BudgetLimits{MaxCalls: options.MaxCalls, MaxTokens: options.MaxTokens, MaxDuration: options.Duration, MaxReplans: options.MaxReplans}}, adapter, wallClock{}, observer)
	if err != nil {
		return model.AttemptRecord{}, err
	}
	_, err = session.Decide(ctx, harness.ModelRequest{RunID: "model-smoke", Step: 1, Objective: "Return a complete_analysis decision for this bounded connectivity check.", ConstraintID: "model-smoke"})
	return observer.record, err
}

// productionOAuth builds official endpoint clients without reading credentials from flags or environment.
func productionOAuth(storePath string) (*oauth.LocalStore, *oauth.HTTPGateway, *oauth.JWKSValidator, *http.Client, error) {
	store, err := oauth.NewLocalStore(storePath)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	client := openai.NewHTTPClient(http.DefaultTransport)
	gateway, err := oauth.NewHTTPGateway(client)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	validator, err := oauth.NewJWKSValidator(client, oauth.WallClock{})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return store, gateway, validator, client, nil
}

// run validates opt-in and arguments before invoking any profile or network dependency.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, backend smokeBackend) int {
	options, err := parseOptions(args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "invalid smoke configuration")
		return 2
	}
	if !options.OptIn {
		fmt.Fprintln(stderr, "real provider smoke requires explicit -opt-in")
		return 2
	}
	if backend == nil {
		fmt.Fprintln(stderr, "smoke composition unavailable")
		return 2
	}
	switch options.Action {
	case "provision":
		if options.HostID == "" || options.RedirectURI == "" {
			fmt.Fprintln(stderr, "provision requires host-id and redirect-uri")
			return 2
		}
		if err := backend.Provision(ctx, options); err != nil {
			printFailure(stderr, options, err)
			return 1
		}
		fmt.Fprintf(stdout, "provider=%s outcome=profile_ready\n", options.Provider)
	case "infer":
		if options.Model == "" {
			fmt.Fprintln(stderr, "infer requires an explicit model")
			return 2
		}
		record, err := backend.Infer(ctx, options)
		if err != nil {
			printFailure(stderr, options, err)
			return 1
		}
		fmt.Fprintf(stdout, "provider=%s model=%s duration=%s input_tokens=%d output_tokens=%d outcome=%s\n", record.Provider, record.Model, record.Duration, record.Usage.InputTokens, record.Usage.OutputTokens, record.Outcome)
	default:
		fmt.Fprintln(stderr, "action must be provision or infer")
		return 2
	}
	return 0
}

// parseOptions reads only non-secret explicit selections and finite budgets.
func parseOptions(args []string, stderr io.Writer) (smokeOptions, error) {
	var options smokeOptions
	flags := flag.NewFlagSet("featureproof-model-smoke", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.BoolVar(&options.OptIn, "opt-in", false, "allow one real provider operation")
	flags.StringVar(&options.Action, "action", "", "provision or infer")
	flags.StringVar(&options.Provider, "provider", "", "explicit provider")
	flags.StringVar(&options.Model, "model", "", "explicit model for infer")
	flags.StringVar(&options.Profile, "profile", "", "opaque auth profile")
	flags.StringVar(&options.StorePath, "profile-store", "", "absolute protected profile store path")
	flags.StringVar(&options.HostID, "host-id", "", "stable opaque host ID for provision")
	flags.StringVar(&options.RedirectURI, "redirect-uri", "", "127.0.0.1 callback for provision")
	flags.DurationVar(&options.Timeout, "timeout", 30*time.Second, "single inference timeout")
	flags.DurationVar(&options.Duration, "duration", time.Minute, "total smoke duration")
	flags.IntVar(&options.MaxCalls, "max-calls", 1, "model call budget")
	flags.Int64Var(&options.MaxTokens, "max-tokens", 10000, "confirmed token budget")
	flags.IntVar(&options.MaxReplans, "max-replans", 0, "replan reservation budget")
	if err := flags.Parse(args); err != nil {
		return smokeOptions{}, err
	}
	if options.Provider != "openai" || options.Profile == "" || options.StorePath == "" || options.Timeout <= 0 || options.Duration <= 0 || options.MaxCalls <= 0 || options.MaxTokens <= 0 || options.MaxReplans < 0 {
		return smokeOptions{}, errors.New("incomplete options")
	}
	return options, nil
}

// printFailure emits only selected public IDs and a typed category.
func printFailure(stderr io.Writer, options smokeOptions, err error) {
	category := model.CategoryInternal
	var typed *model.Error
	if errors.As(err, &typed) {
		category = typed.Category
	}
	fmt.Fprintf(stderr, "provider=%s model=%s outcome=failure category=%s\n", options.Provider, options.Model, category)
}

// main delegates process exit status to the testable smoke runner.
func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, productionBackend{}))
}
