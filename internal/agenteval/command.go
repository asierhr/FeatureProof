package agenteval

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/asierhr/featureProof/internal/assertion"
)

// CommandOptions holds explicit evaluation selection and resource budgets parsed at the CLI boundary.
type CommandOptions struct {
	Dataset, Criteria, Output, Source, Replay, ReplaySHA, Baseline, RunID      string
	Live                                                                       bool
	Provider, Model, Profile, ProfileStore                                     string
	CallTimeout, MaxDuration                                                   time.Duration
	MaxCalls, MaxReplans                                                       int
	MaxTokens, MaxRequestBytes, MaxHeaderBytes, MaxStreamBytes, MaxOutputBytes int64
}

// LiveComposer resolves authentication only after every local preflight has succeeded.
type LiveComposer interface {
	Compose(context.Context, CommandOptions) (CandidateSource, error)
}

// RunCommand executes the dedicated eval agent workflow and returns its documented process code.
func RunCommand(ctx context.Context, args []string, stdout, stderr io.Writer, composer LiveComposer) int {
	options, err := ParseCommandOptions(args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "invalid agent evaluation configuration")
		return 2
	}
	criteria, err := LoadCriteria(ctx, options.Criteria, 1<<20)
	if err != nil {
		return commandFailure(stderr, err)
	}
	registry := StaticContractRegistry{"http.request/v1": AcceptDescriptor}
	loader, _ := NewLoader(registry)
	root := filepath.Dir(options.Dataset)
	suite, err := loader.Load(ctx, root, filepath.Base(options.Dataset), LoadLimits{MaxFiles: criteria.Limits.MaxFiles, MaxTotalBytes: criteria.Limits.MaxTotalBytes, MaxFileBytes: criteria.Limits.MaxResultBytes, MaxCases: criteria.Limits.MaxCases})
	if err != nil {
		return commandFailure(stderr, err)
	}
	var baseline *BundleSnapshot
	if options.Baseline != "" {
		value, loadErr := LoadBundle(ctx, options.Baseline, criteria.Limits.MaxArtifactBytes)
		if loadErr != nil {
			return commandFailure(stderr, loadErr)
		}
		baseline = &value
		if compatibleErr := ValidateBaselineCompatibility(suite, value); compatibleErr != nil {
			return commandFailure(stderr, compatibleErr)
		}
	}
	var source CandidateSource
	if options.Source == "replay" {
		source, err = LoadReplay(ctx, options.Replay, options.ReplaySHA, criteria.Limits.MaxReplayBytes)
	} else {
		if composer == nil {
			return commandFailure(stderr, typedError(InvalidInput, "live", "composer", nil))
		}
		source, err = composer.Compose(ctx, options)
	}
	if err != nil {
		return commandFailure(stderr, err)
	}
	runner, _ := NewRunner(assertion.DefinitionValidator{}, SessionClock{})
	started := time.Now().UTC()
	run, err := runner.Run(ctx, suite, criteria, source)
	if err != nil {
		return commandFailure(stderr, err)
	}
	var comparison *Comparison
	if baseline != nil {
		value, compareErr := Compare(ctx, run, *baseline, criteria)
		if compareErr != nil {
			return commandFailure(stderr, compareErr)
		}
		comparison = &value
		run.Verdict, run.Reasons = Evaluate(criteria, run.Metrics, comparison)
	}
	runID := options.RunID
	if runID == "" {
		runID = "run-" + started.Format("20060102-150405")
	}
	forbidden := []string{options.Profile}
	for _, c := range suite.Cases() {
		forbidden = append(forbidden, c.Canaries()...)
	}
	dir, err := NewWriter(nil).Write(ctx, options.Output, runID, run, criteria, comparison, started, time.Now().UTC(), forbidden)
	if err != nil {
		return commandFailure(stderr, err)
	}
	fmt.Fprintf(stdout, "bundle=%s verdict=%s\n", dir, run.Verdict)
	if run.Verdict == "failed" {
		return 1
	}
	return 0
}

// ParseCommandOptions rejects incomplete source selection and live budgets without assigning provider defaults.
func ParseCommandOptions(args []string, stderr io.Writer) (CommandOptions, error) {
	var o CommandOptions
	flags := flag.NewFlagSet("featureproof eval agent", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&o.Dataset, "dataset", "", "local dataset manifest")
	flags.StringVar(&o.Criteria, "criteria", "", "local criteria file")
	flags.StringVar(&o.Output, "output", "", "bundle parent directory")
	flags.StringVar(&o.Source, "source", "", "replay or live")
	flags.StringVar(&o.Replay, "replay", "", "local replay file")
	flags.StringVar(&o.ReplaySHA, "replay-sha256", "", "replay digest")
	flags.StringVar(&o.Baseline, "baseline", "", "optional complete baseline bundle")
	flags.StringVar(&o.RunID, "run-id", "", "optional stable output run id")
	flags.BoolVar(&o.Live, "live", false, "explicitly allow live model I/O")
	flags.StringVar(&o.Provider, "provider", "", "explicit provider")
	flags.StringVar(&o.Model, "model", "", "explicit model")
	flags.StringVar(&o.Profile, "auth-profile", "", "opaque auth profile")
	flags.StringVar(&o.ProfileStore, "profile-store", "", "protected profile store")
	flags.DurationVar(&o.CallTimeout, "call-timeout", 0, "per-call timeout")
	flags.DurationVar(&o.MaxDuration, "max-duration", 0, "session duration budget")
	flags.IntVar(&o.MaxCalls, "max-calls", 0, "call budget")
	flags.Int64Var(&o.MaxTokens, "max-tokens", 0, "token budget")
	flags.IntVar(&o.MaxReplans, "max-replans", -1, "replan budget")
	flags.Int64Var(&o.MaxRequestBytes, "max-request-bytes", 0, "request budget")
	flags.Int64Var(&o.MaxHeaderBytes, "max-header-bytes", 0, "response header budget")
	flags.Int64Var(&o.MaxStreamBytes, "max-stream-bytes", 0, "stream budget")
	flags.Int64Var(&o.MaxOutputBytes, "max-output-bytes", 0, "output budget")
	if err := flags.Parse(args); err != nil {
		return CommandOptions{}, err
	}
	if flags.NArg() != 0 || o.Dataset == "" || o.Criteria == "" || o.Output == "" || (o.Source != "replay" && o.Source != "live") {
		return CommandOptions{}, errors.New("missing common options")
	}
	if o.Source == "replay" {
		if o.Live || o.Replay == "" || !sha256Text.MatchString(o.ReplaySHA) {
			return CommandOptions{}, errors.New("invalid replay selection")
		}
		return o, nil
	}
	if !o.Live || o.Provider == "" || o.Model == "" || o.Profile == "" || o.ProfileStore == "" || o.CallTimeout.Milliseconds() <= 0 || o.MaxDuration.Milliseconds() <= 0 || o.MaxCalls <= 0 || o.MaxTokens <= 0 || o.MaxReplans < 0 || o.MaxRequestBytes <= 0 || o.MaxHeaderBytes <= 0 || o.MaxStreamBytes <= 0 || o.MaxOutputBytes <= 0 {
		return CommandOptions{}, errors.New("incomplete live selection")
	}
	return o, nil
}

// commandFailure emits only a stable category and maps cancellation to exit 3.
func commandFailure(stderr io.Writer, err error) int {
	category := InvalidInput
	var typed *EvalError
	if errors.As(err, &typed) {
		category = typed.Category
	}
	fmt.Fprintf(stderr, "agent evaluation failed category=%s\n", category)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || category == Cancelled || category == Timeout {
		return 3
	}
	return 2
}
