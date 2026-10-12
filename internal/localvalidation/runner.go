package localvalidation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/assertion"
	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/planmanifest"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/policy"
	"github.com/asierhr/featureProof/internal/specification"
	specyaml "github.com/asierhr/featureProof/internal/specification/yaml"
	"github.com/asierhr/featureProof/internal/tools"
	httptool "github.com/asierhr/featureProof/internal/tools/http"
)

// Request contains the three explicit inputs accepted by the validate subcommand.
type Request struct{ SpecPath, PlanPath, Target string }

// RuntimeDependencies isolates time, identity, network, filesystem and output for hermetic runs.
type RuntimeDependencies struct {
	Root             string
	Clock            harness.Clock
	IDs              harness.IDGenerator
	Waiter           harness.Waiter
	Resolver         httptool.Resolver
	Dialer           httptool.Dialer
	WithTimeout      httptool.TimeoutFactory
	FinalizeTimeout  time.Duration
	PublisherFactory func(string, string) (*Publisher, error)
	PolicyFactory    func() harness.Policy
	EvidenceFactory  func(*Publisher) (evidence.Store, error)
	Evaluator        planning.StepAssertionEvaluator
	Stdout, Stderr   io.Writer
}

// Runner owns one prevalidated deterministic invocation and its replaceable runtime boundaries.
type Runner struct {
	spec      specification.Specification
	manifest  planmanifest.Manifest
	validated []planning.ValidatedPlan
	registry  *tools.Registry
	policy    harness.Policy
	sanitizer evidence.Sanitizer
	deps      RuntimeDependencies
}

// constraintCatalog exposes the specification constraint namespace to deterministic plan validation.
type constraintCatalog map[string]bool

// HasConstraint exposes exact specification IDs to planning validation.
func (c constraintCatalog) HasConstraint(id string) bool { return c[id] }

// NewRunner loads and validates every input and configuration before generating an ID or touching the run directory.
func NewRunner(ctx context.Context, request Request, deps RuntimeDependencies) (*Runner, error) {
	spec, err := specyaml.Load(ctx, request.SpecPath)
	if err != nil {
		return nil, safeError("load specification", err)
	}
	spec.Target.BaseURL = request.Target
	manifest, err := planmanifest.Load(ctx, request.PlanPath, spec)
	if err != nil {
		return nil, safeError("load plan manifest", err)
	}
	redactionPolicy, err := evidence.NewPolicy(manifest.Redaction)
	if err != nil {
		return nil, safeError("validate redaction", err)
	}
	if deps.Clock == nil || deps.IDs == nil || deps.Waiter == nil || deps.Resolver == nil || deps.Dialer == nil || deps.WithTimeout == nil || deps.PublisherFactory == nil || deps.PolicyFactory == nil || deps.EvidenceFactory == nil || deps.Evaluator == nil || deps.Stdout == nil || deps.Stderr == nil || deps.Root == "" || deps.FinalizeTimeout <= 0 {
		return nil, fmt.Errorf("runtime dependencies are required")
	}
	h := manifest.HTTP
	httpTool, err := httptool.New(httptool.Config{Target: spec.Target, AllowedMethods: h.AllowedMethods, AllowedRequestHeaders: h.AllowedRequestHeaders, AllowedResponseHeaders: h.AllowedResponseHeaders, FollowRedirectsForMethods: h.FollowRedirectsForMethods, MaxRedirects: h.MaxRedirects, Timeout: h.Timeout(), MaxURLBytes: h.MaxURLBytes, MaxRequestHeaderBytes: h.MaxRequestHeaderBytes, MaxRequestBodyBytes: h.MaxRequestBodyBytes, MaxResponseHeaderBytes: h.MaxResponseHeaderBytes, MaxResponseBodyBytes: h.MaxResponseBodyBytes, Resolver: deps.Resolver, Dialer: deps.Dialer, Clock: deps.Clock, WithTimeout: deps.WithTimeout})
	if err != nil {
		return nil, safeError("validate HTTP runtime", err)
	}
	registry, err := tools.NewRegistry(httpTool.Spec())
	if err != nil {
		return nil, safeError("create registry", err)
	}
	catalog := constraintCatalog{}
	for _, constraint := range spec.Constraints {
		catalog[constraint.ID] = true
	}
	validator, err := planning.NewValidator(catalog, registry, assertion.DefinitionValidator{Policy: redactionPolicy})
	if err != nil {
		return nil, err
	}
	validated := make([]planning.ValidatedPlan, len(manifest.Plans))
	for n, plan := range manifest.Plans {
		validated[n], err = validator.Validate(plan)
		if err != nil {
			return nil, safeError("validate plan", err)
		}
	}
	runPolicy := deps.PolicyFactory()
	if runPolicy == nil {
		return nil, fmt.Errorf("policy factory returned nil")
	}
	return &Runner{spec: spec, manifest: manifest, validated: validated, registry: registry, policy: runPolicy, sanitizer: evidence.NewSanitizer(redactionPolicy), deps: deps}, nil
}

// Run executes constraints sequentially, publishes a terminal bundle when possible, and returns the public exit code.
func (r *Runner) Run(ctx context.Context) int {
	runID := r.deps.IDs.Next()
	if runID == "" {
		fmt.Fprintln(r.deps.Stderr, "featureproof_error=id_generation")
		return 3
	}
	started := r.deps.Clock.Now()
	fmt.Fprintf(r.deps.Stdout, "run_id=%s\n", runID)
	publisher, err := r.deps.PublisherFactory(r.deps.Root, runID)
	if err != nil {
		fmt.Fprintln(r.deps.Stderr, "featureproof_error=publication_setup")
		return 3
	}
	store, err := r.deps.EvidenceFactory(publisher)
	if err != nil {
		fmt.Fprintln(r.deps.Stderr, "featureproof_error=evidence_setup")
		return 3
	}
	journal := harness.NewMemoryJournal()
	_ = journal.Append(ctx, harness.Event{Type: harness.EventRunCreated, RunID: runID})
	limits := harness.Limits{MaxSteps: r.spec.Limits.MaxSteps, MaxToolCalls: r.spec.Limits.MaxToolCalls, MaxRetries: r.spec.Limits.MaxRetries}
	shared, err := harness.NewRun(runID, r.spec.Feature.ID, r.spec.Constraints[0].ID, limits)
	if err != nil {
		fmt.Fprintln(r.deps.Stderr, "featureproof_error=run_setup")
		return 3
	}
	fmt.Fprintln(r.deps.Stdout, "phase=executing")
	results := []ConstraintResult{}
	status := ResultPassed
	operational := error(nil)
	for n, constraint := range r.spec.Constraints {
		if err = ctx.Err(); err != nil {
			if terminalCancellation(ctx) {
				status = ResultCancelled
			} else {
				status = ResultError
				operational = err
			}
			break
		}
		shared.ConstraintID = constraint.ID
		executor, buildErr := planning.NewPlanExecutor(planning.ExecutorConfig{Run: shared}, planning.ExecutorDependencies{Registry: r.registry, Policy: r.policy, Evidence: store, Journal: journal, Sanitizer: r.sanitizer, Clock: r.deps.Clock, IDs: r.deps.IDs, Waiter: r.deps.Waiter, Evaluator: r.deps.Evaluator})
		if buildErr != nil {
			operational = buildErr
			status = ResultError
			break
		}
		planResult, executeErr := executor.Execute(ctx, r.validated[n])
		if executeErr != nil {
			if ctx.Err() != nil {
				if terminalCancellation(ctx) {
					status = ResultCancelled
				} else {
					status = ResultError
					operational = executeErr
				}
			} else {
				status = ResultError
				operational = executeErr
			}
			break
		}
		result := constraintResult(constraint.ID, planResult)
		results = append(results, result)
		ids, _ := jsonIDs(result.EvidenceIDs)
		fmt.Fprintf(r.deps.Stdout, "constraint_id=%s status=%s evidence_ids=%s\n", constraint.ID, result.Status, ids)
	}
	if status == ResultPassed {
		status = aggregateStatus(results)
	}
	if status == ResultError {
		fmt.Fprintln(r.deps.Stderr, "featureproof_error=execution")
	}
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.deps.FinalizeTimeout)
	defer cancel()
	terminalType := harness.EventRunCompleted
	if status == ResultCancelled {
		terminalType = harness.EventRunCancelled
	} else if status == ResultError {
		terminalType = harness.EventRunFailed
	} else if status == ResultInconclusive {
		terminalType = harness.EventRunInconclusive
	}
	_ = journal.Append(finalCtx, harness.Event{Type: terminalType, RunID: runID, Status: string(status)})
	finished := r.deps.Clock.Now()
	snapshot := RunSnapshot{ID: runID, FeatureID: r.spec.Feature.ID, TargetID: r.spec.Target.ID, TargetURL: r.spec.Target.BaseURL, Status: status, StartedAt: started, FinishedAt: finished, DurationMS: durationMillis(started, finished), Limits: r.spec.Limits, Consumed: shared.Consumed, Constraints: results}
	events, eventErr := journal.Events(finalCtx, runID)
	if eventErr == nil {
		fmt.Fprintln(r.deps.Stdout, "phase=reporting")
		eventErr = publisher.Publish(finalCtx, snapshot, events, r.manifest, store)
	}
	if eventErr != nil {
		if status != ResultCancelled {
			fmt.Fprintln(r.deps.Stderr, "featureproof_error=publication")
			status = ResultError
		}
	}
	if operational != nil && status != ResultCancelled {
		status = ResultError
	}
	fmt.Fprintf(r.deps.Stdout, "result=%s\n", status)
	return ExitCode(status)
}

// jsonIDs encodes evidence IDs without ambiguous separators or payload content.
func jsonIDs(ids []string) (string, error) { raw, err := jsonMarshal(ids); return string(raw), err }

// durationMillis clamps anomalous clocks so persisted durations remain valid.
func durationMillis(start, end time.Time) int64 {
	value := end.Sub(start).Milliseconds()
	if value < 0 {
		return 0
	}
	return value
}

// systemClock supplies wall time to production boundaries.
type systemClock struct{}

// Now returns the current wall-clock instant.
func (systemClock) Now() time.Time { return time.Now() }

// randomIDs generates opaque run and evidence identities without mutable global state.
type randomIDs struct{}

// Next returns a cryptographically random identifier suitable for fail-on-exists persistence.
func (randomIDs) Next() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(raw[:])
}

// contextWaiter implements executor backoff while preserving caller cancellation.
type contextWaiter struct{}

// Wait blocks only for the requested delay or until the shared context ends.
func (contextWaiter) Wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// DefaultDependencies binds production OS/network boundaries while keeping them replaceable in tests.
func DefaultDependencies(root string, stdout, stderr io.Writer) RuntimeDependencies {
	return RuntimeDependencies{Root: root, Clock: systemClock{}, IDs: randomIDs{}, Waiter: contextWaiter{}, Resolver: net.DefaultResolver, Dialer: &net.Dialer{}, WithTimeout: context.WithTimeout, FinalizeTimeout: 5 * time.Second, PublisherFactory: NewPublisher, PolicyFactory: func() harness.Policy {
		return &policy.Static{Decisions: map[string]harness.PolicyDecision{"http.request": {Allowed: true, Reason: "manifest_allowlist"}}}
	}, EvidenceFactory: func(publisher *Publisher) (evidence.Store, error) { return publisher.EvidenceStore() }, Evaluator: assertion.Engine{}, Stdout: stdout, Stderr: stderr}
}

// RunCommand parses, validates, executes, and classifies one local command invocation.
func RunCommand(ctx context.Context, args []string, _ io.Reader, stdout, stderr io.Writer) int {
	request, err := ParseRequest(args)
	if err != nil {
		fmt.Fprintln(stderr, "usage: featureproof validate --spec PATH --plan PATH --target URL")
		var argument *ArgumentError
		if errors.As(err, &argument) {
			fmt.Fprintf(stderr, "featureproof_error=arguments field=%s category=%s\n", argument.Field, argument.Category)
		} else {
			fmt.Fprintln(stderr, "featureproof_error=arguments")
		}
		return 3
	}
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "featureproof_error=working_directory")
		return 3
	}
	runner, err := NewRunner(ctx, request, DefaultDependencies(root, stdout, stderr))
	if err != nil {
		category, field := validationDiagnostic(err)
		fmt.Fprintf(stderr, "featureproof_error=validation category=%s field=%s\n", category, field)
		return 3
	}
	code := runner.Run(ctx)
	if terminalCancellation(ctx) && ctx.Err() != nil {
		return 130
	}
	return code
}

// validationDiagnostic projects known typed failures into value-free operator guidance.
func validationDiagnostic(err error) (string, string) {
	var manifestError *planmanifest.Error
	if errors.As(err, &manifestError) {
		return manifestError.Category, safeField(manifestError.Field)
	}
	var specificationError *specification.ValidationError
	if errors.As(err, &specificationError) {
		return string(specificationError.Category), safeField(specificationError.Field)
	}
	var unsupported *specification.UnsupportedVersionError
	if errors.As(err, &unsupported) {
		return "unsupported_version", "version"
	}
	var redactionError *evidence.ConfigError
	if errors.As(err, &redactionError) {
		return redactionError.Category, safeField(redactionError.Path)
	}
	var httpError *httptool.ConfigError
	if errors.As(err, &httpError) {
		return "invalid_http_config", safeField(httpError.Field)
	}
	var planError *planning.PlanError
	if errors.As(err, &planError) {
		return string(planError.Category), safeField(planError.Path)
	}
	return "internal", "validation"
}

// trustedDiagnosticSegments is the closed vocabulary allowed in public validation field paths.
var trustedDiagnosticSegments = map[string]bool{
	"$": true, "$mapping": true, "document": true, "version": true,
	"feature": true, "id": true, "description": true, "target": true, "base_url": true,
	"constraints": true, "target_id": true, "limits": true, "max_steps": true, "max_tool_calls": true, "max_retries": true,
	"runtime": true, "http": true, "redaction": true, "policy": true, "plans": true, "steps": true, "constraint_id": true,
	"allowed_methods": true, "allowed_request_headers": true, "allowed_response_headers": true, "follow_redirects_for_methods": true,
	"max_redirects": true, "timeout": true, "timeout_ms": true, "max_url_bytes": true, "max_request_header_bytes": true,
	"max_request_body_bytes": true, "max_response_header_bytes": true, "max_response_body_bytes": true, "max_body_bytes": true,
	"additional_sensitive_headers": true, "sensitive_json_paths": true, "model_sensitive_json_paths": true, "allowed_tools": true,
	"input": true, "members": true, "literal": true, "object": true, "array": true, "reference": true, "step_id": true,
	"path": true, "value_type": true, "field": true, "index": true, "assertions": true, "definition": true, "operator": true,
	"expected": true, "all": true, "any": true, "depends_on": true, "tool": true, "dependencies": true,
}

// safeField admits only internal schema segments and numeric indices, otherwise using a stable placeholder.
func safeField(field string) string {
	if field == "" {
		return "document"
	}
	for _, segment := range strings.Split(field, ".") {
		base := segment
		if bracket := strings.IndexByte(segment, '['); bracket >= 0 {
			base = segment[:bracket]
			remaining := segment[bracket:]
			for remaining != "" {
				if remaining[0] != '[' {
					return "document"
				}
				end := strings.IndexByte(remaining, ']')
				if end < 2 {
					return "document"
				}
				for _, digit := range remaining[1:end] {
					if digit < '0' || digit > '9' {
						return "document"
					}
				}
				remaining = remaining[end+1:]
			}
		}
		if !trustedDiagnosticSegments[base] {
			return "document"
		}
	}
	return field
}
