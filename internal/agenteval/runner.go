package agenteval

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/openapi"
)

const promptVersion = "agentplanning-v1"

// Clock supplies deterministic run timestamps and duration boundaries.
type Clock interface{ Now() time.Time }

// Runner owns sequential admission, capture and scoring for exactly one evaluation run.
type Runner struct {
	assertions AssertionValidator
	clock      Clock
}

// NewRunner composes deterministic collaborators and deliberately accepts no executor or tool registry.
func NewRunner(assertions AssertionValidator, clock Clock) (*Runner, error) {
	if assertions == nil || clock == nil {
		return nil, typedError(InvalidInput, "compose", "runner", nil)
	}
	return &Runner{assertions: assertions, clock: clock}, nil
}

// Run evaluates cases in manifest order with at most one inference per admitted case.
func (r *Runner) Run(ctx context.Context, suite Suite, criteria Criteria, source CandidateSource) (RunResult, error) {
	if err := ctx.Err(); err != nil {
		return RunResult{}, contextEvalError("run", err)
	}
	if source == nil {
		return RunResult{}, typedError(InvalidInput, "run", "source", nil)
	}
	if err := validateCriteria(criteria); err != nil {
		return RunResult{}, err
	}
	runDuration, ok := durationFromMilliseconds(criteria.Limits.MaxDurationMillis)
	if !ok {
		return RunResult{}, typedError(LimitExceeded, "run", "duration", nil)
	}
	runCtx, cancel := context.WithTimeout(ctx, runDuration)
	defer cancel()
	cases := suite.Cases()
	if len(cases) == 0 || len(cases) > criteria.Limits.MaxCases {
		return RunResult{}, typedError(LimitExceeded, "run", "cases", nil)
	}
	started := r.clock.Now()
	run := RunResult{Dataset: suite.Identity(), Cases: make([]CaseResult, 0, len(cases))}
	provider, selectedModel := source.Metadata()
	run.Provider, run.Model = provider, selectedModel
	run.SafeConfiguration = safeConfiguration(suite, criteria, source)
	var terminal error
	for _, c := range cases {
		if err := runCtx.Err(); err != nil {
			return run, contextEvalError("run", err)
		}
		if r.clock.Now().Sub(started) > runDuration {
			return RunResult{}, typedError(Timeout, "run", "duration", context.DeadlineExceeded)
		}
		before := source.Snapshot()
		base, err := source.Model(runCtx, c)
		if err != nil {
			mapped := mapSourceError(runCtx, err)
			var typed *EvalError
			if !errors.As(mapped, &typed) || typed.Category != ModelFailure {
				return run, mapped
			}
			caseResult := failedCase(c, before, source.Snapshot())
			run.Cases = append(run.Cases, caseResult)
			terminal = mapped
			if criteria.FailFast {
				break
			}
			continue
		}
		capture := &capturingModel{next: base, canaries: c.canaries}
		observer := runObserver{}
		generator, err := agentplanning.NewGenerator(capture, r.assertions, allowPolicy(c.descriptors), observer, r.clock)
		if err != nil {
			return RunResult{}, typedError(InvalidInput, "run", "generator", nil)
		}
		contract, conversionErr := openapi.NeutralizeReducedContext(ctx, c.openAPI, "eval-fixture", digestBytes(c.openAPI.Bytes), "eval-policy-v1")
		if conversionErr != nil {
			return RunResult{}, conversionErr
		}
		request := agentplanning.GenerationRequest{RunID: "eval-" + c.id, PlanID: "plan-" + c.id, Constraints: append([]agentplanning.Constraint(nil), c.constraints...), Contract: contract, Capabilities: append([]agentplanning.CapabilityDescriptor(nil), c.descriptors...), Limits: c.limits}
		result, genErr := generator.Generate(runCtx, request)
		after := source.Snapshot()
		caseResult := scoreCase(runCtx, c, result, genErr, capture, before, after)
		run.Cases = append(run.Cases, caseResult)
		if run.PromptText == "" && capture.instructions != "" {
			run.PromptText = capture.instructions
			run.PromptVersion = promptVersion
			run.PromptHash = digestBytes([]byte(capture.instructions))
		}
		if runCtx.Err() != nil {
			return run, contextEvalError("run", runCtx.Err())
		}
		if caseResult.ErrorCategory == string(ModelFailure) {
			terminal = typedError(ModelFailure, "run", "model", nil)
		}
		if criteria.FailFast && !casePasses(caseResult, c.expectation) {
			break
		}
	}
	metrics, err := Aggregate(ctx, run.Cases)
	if err != nil {
		return RunResult{}, err
	}
	run.Metrics = metrics
	run.Verdict, run.Reasons = Evaluate(criteria, metrics, nil)
	return run, terminal
}

// failedCase preserves a safe, scoreable result when a source fails before generation.
func failedCase(c Case, before, after SourceSnapshot) CaseResult {
	result := scoreCase(context.Background(), c, agentplanning.Result{}, errors.New("source failure"), &capturingModel{}, before, after)
	return result
}

// safeConfiguration captures exact local, run, generation and source budgets without auth identity.
func safeConfiguration(suite Suite, criteria Criteria, source CandidateSource) SafeConfiguration {
	provider, selectedModel := source.Metadata()
	sourceBudget := SourceBudget{Kind: "unknown", Provider: provider, Model: selectedModel}
	if configured, ok := source.(interface{ SafeBudget() SourceBudget }); ok {
		sourceBudget = configured.SafeBudget()
	}
	out := SafeConfiguration{SchemaVersion: 1, Load: suite.LoadLimits(), Run: criteria.Limits, Source: sourceBudget}
	for _, c := range suite.Cases() {
		out.Cases = append(out.Cases, CaseConfiguration{CaseID: c.id, Generation: c.limits, Evaluation: c.evaluation})
	}
	return out
}

// capturingModel verifies the privileged/untrusted boundary around the actual generator request.
type capturingModel struct {
	next         agentplanning.PlanningModel
	canaries     []string
	instructions string
	promptHash   string
	violation    bool
}

// Generate captures the actual boundary, rejects canaries in privileged instructions and discards raw payload after forwarding.
func (m *capturingModel) Generate(ctx context.Context, request agentplanning.StructuredRequest) (agentplanning.ModelResponse, error) {
	m.instructions = request.Instructions
	m.promptHash = digestBytes([]byte(request.Instructions))
	for _, canary := range m.canaries {
		if strings.Contains(request.Instructions, canary) {
			m.violation = true
			return agentplanning.ModelResponse{}, errors.New("privileged instruction contamination")
		}
	}
	return m.next.Generate(ctx, request)
}

// allowPolicy authorizes proposal metadata from the descriptor-only case catalog.
type allowPolicy []agentplanning.CapabilityDescriptor

// Evaluate permits only exact descriptor names because evaluations perform no execution.
func (p allowPolicy) Evaluate(ctx context.Context, intent agentplanning.PlanIntent) (agentplanning.PolicyDecision, error) {
	if err := ctx.Err(); err != nil {
		return agentplanning.PolicyDecision{}, err
	}
	for _, d := range p {
		if d.Name == intent.Capability {
			return agentplanning.PolicyDecision{Allowed: true, Reason: "evaluation_descriptor"}, nil
		}
	}
	return agentplanning.PolicyDecision{Allowed: false, Reason: "default_deny"}, nil
}

// runObserver terminates generator telemetry locally without creating a journal dependency.
type runObserver struct{}

// Observe accepts payload-free terminal telemetry without adding an external sink.
func (runObserver) Observe(context.Context, agentplanning.GenerationRecord) error { return nil }

// mapSourceError classifies cancellation and otherwise prevents external error text from escaping.
func mapSourceError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return contextEvalError("source", ctx.Err())
	}
	var eval *EvalError
	if errors.As(err, &eval) {
		return err
	}
	return typedError(ModelFailure, "source", "model", nil)
}

// casePasses identifies a structurally matching case for explicit fail-fast policy.
func casePasses(result CaseResult, expected CaseExpectation) bool {
	if len(result.Findings) > 0 || !result.LimitCompliant {
		return false
	}
	switch expected.Outcome {
	case "plan":
		return result.Outcome == "plan"
	case "insufficient_context":
		return result.Outcome == "insufficient_context" && result.ErrorPath == ""
	case "rejected":
		return result.Outcome == "rejected" && result.ErrorCategory == string(expected.Rejection.Category) && result.ErrorStage == expected.Rejection.Stage && result.ErrorPath == expected.Rejection.Path
	}
	return false
}
