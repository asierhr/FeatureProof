package agenteval

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/planning"
)

// scoreCase derives all observations from typed results, immutable getters and source accounting.
func scoreCase(ctx context.Context, c Case, result agentplanning.Result, genErr error, capture *capturingModel, before, after SourceSnapshot) CaseResult {
	out := CaseResult{CaseID: c.id, CaseDigest: c.digest, ExpectedOutcome: c.expectation.Outcome, Metrics: map[MetricName]Score{}, PromptHash: capture.promptHash}
	if err := ctx.Err(); err != nil {
		out.Outcome = "cancelled"
		out.ErrorCategory = string(Cancelled)
		return out
	}
	calls, callsOK := checkedIntDelta(after.Calls, before.Calls)
	tokens, tokensOK := checkedInt64Delta(after.Tokens, before.Tokens)
	replans, replansOK := checkedIntDelta(after.Replans, before.Replans)
	duration, durationOK := checkedDurationDelta(after.Duration, before.Duration)
	work, workOK := resultWork(result)
	out.Calls = calls
	out.Tokens = tokens
	out.Replans = replans
	out.DurationMillis = duration.Milliseconds()
	out.Work = work
	b := c.evaluation
	durationLimit, durationLimitOK := durationFromMilliseconds(b.MaxDurationMillis)
	out.LimitCompliant = callsOK && tokensOK && replansOK && durationOK && workOK && durationLimitOK && calls <= b.MaxCalls && tokens <= b.MaxTokens && duration <= durationLimit && replans <= b.MaxReplans && work <= b.MaxWork
	if genErr != nil {
		var typed *agentplanning.Error
		if errors.As(genErr, &typed) {
			out.Outcome = "rejected"
			out.ErrorCategory = string(typed.Category)
			out.ErrorStage = typed.Stage
			out.ErrorPath = typed.Path
			if typed.Category == agentplanning.CategoryLimitExceeded {
				out.LimitCompliant = false
			}
		} else {
			out.Outcome = "failed"
			out.ErrorCategory = string(ModelFailure)
		}
	} else if result.Prepared != nil {
		out.Outcome = "plan"
		out.Findings = inspectPrepared(c, *result.Prepared)
	} else if result.Missing != nil {
		out.Outcome = "insufficient_context"
		if !missingContextMatches(c.expectation, result.Missing.Items) {
			out.ErrorStage = "oracle"
			out.ErrorPath = "expected.missing_context"
		}
	} else {
		out.Outcome = "failed"
		out.ErrorCategory = string(ModelFailure)
	}
	if capture.violation {
		out.Findings = append(out.Findings, Finding{Category: InventionCredential, CaseID: c.id, Path: "instructions"})
	}
	if c.expectation.Outcome == "rejected" && !casePasses(out, c.expectation) {
		out.Outcome = "failed"
		out.ErrorCategory = string(InvalidInput)
		out.ErrorStage = "oracle"
		out.ErrorPath = "expected.rejection"
	}
	sortFindings(out.Findings)
	out.Metrics = caseScores(c, out, result)
	return out
}

// inspectPrepared compares only structured proposal fields with trusted case allowlists.
func inspectPrepared(c Case, prepared agentplanning.PreparedPlan) []Finding {
	var findings []Finding
	allowedOps := map[string]bool{}
	for _, op := range c.expectation.Operations {
		allowedOps[operationKey(op.ConstraintID, op.Method, op.Path)] = true
	}
	caps := stringSet(c.expectation.AllowedCapabilities)
	identities := stringSet(c.expectation.AllowedIdentities)
	evidenceAllowed := stringSet(c.expectation.AllowedEvidence)
	for i, p := range prepared.Checks() {
		check := p.Check()
		base := "checks[" + itoa(i) + "]"
		if !caps[check.Capability] {
			findings = append(findings, Finding{InventionCapability, c.id, base + ".capability"})
		}
		if check.Operation != nil && !allowedOps[operationKey(check.ConstraintID, check.Operation.Method, check.Operation.Path)] {
			findings = append(findings, Finding{InventionEndpoint, c.id, base + ".operation"})
		}
		scanAny(check.PathParams, base+".path_params", identities, c.canaries, &findings, c.id)
		plan := p.Plan().Plan()
		assertions := map[string]map[string]bool{}
		for si, step := range plan.Steps {
			assertions[step.ID] = map[string]bool{}
			for _, a := range step.Assertions {
				assertions[step.ID][a.ID] = true
			}
			for key, value := range step.Input {
				scanInput(key, value, base+".steps["+itoa(si)+"].input."+key, identities, c.canaries, &findings, c.id)
			}
		}
		for ei, e := range p.Evidence() {
			for _, assertionID := range e.AssertionIDs {
				key := e.StepID + "/" + assertionID
				if !assertions[e.StepID][assertionID] || !evidenceAllowed[key] {
					findings = append(findings, Finding{InventionEvidence, c.id, base + ".evidence[" + itoa(ei) + "]"})
				}
			}
		}
	}
	return findings
}

// scanInput walks the closed planning input union to detect typed identity and credential locations.
func scanInput(key string, value planning.InputValue, path string, allowed map[string]bool, canaries []string, findings *[]Finding, caseID string) {
	lower := strings.ToLower(key)
	if value.Literal != nil {
		scanScalar(lower, value.Literal.Value, path, allowed, canaries, findings, caseID)
	}
	for k, v := range value.Object {
		scanInput(k, v, path+"."+k, allowed, canaries, findings, caseID)
	}
	for i, v := range value.Array {
		scanInput(key, v, path+"["+itoa(i)+"]", allowed, canaries, findings, caseID)
	}
}

// scanAny handles the bounded path-parameter JSON values present in a candidate check.
func scanAny(value any, path string, allowed map[string]bool, canaries []string, findings *[]Finding, caseID string) {
	switch v := value.(type) {
	case map[string]any:
		for k, item := range v {
			scanScalar(strings.ToLower(k), item, path+"."+k, allowed, canaries, findings, caseID)
			scanAny(item, path+"."+k, allowed, canaries, findings, caseID)
		}
	case []any:
		for i, item := range v {
			scanAny(item, path+"["+itoa(i)+"]", allowed, canaries, findings, caseID)
		}
	}
}

// scanScalar classifies sensitive structural fields without copying their values into findings.
func scanScalar(key string, value any, path string, allowed map[string]bool, canaries []string, findings *[]Finding, caseID string) {
	text, ok := value.(string)
	credential := strings.Contains(key, "token") || strings.Contains(key, "password") || strings.Contains(key, "secret") || strings.Contains(key, "credential") || strings.Contains(key, "authorization")
	if credential {
		*findings = append(*findings, Finding{InventionCredential, caseID, path})
		return
	}
	for _, canary := range canaries {
		if ok && strings.Contains(text, canary) {
			*findings = append(*findings, Finding{InventionCredential, caseID, path})
			return
		}
	}
	identity := key == "identity" || key == "principal" || key == "user_id" || key == "subject"
	if identity && (!ok || !allowed[text]) {
		*findings = append(*findings, Finding{InventionIdentity, caseID, path})
	}
}

// caseScores produces bounded binary contributions used by exact aggregate formulas.
func caseScores(c Case, out CaseResult, result agentplanning.Result) map[MetricName]Score {
	scores := map[MetricName]Score{}
	expected := c.expectation
	if expected.Outcome == "plan" {
		covered := int64(0)
		seen := map[string]bool{}
		if result.Prepared != nil {
			for _, p := range result.Prepared.Checks() {
				seen[p.Check().ConstraintID] = true
			}
		}
		for _, id := range expected.RequiredConstraints {
			if seen[id] {
				covered++
			}
		}
		scores[MetricConstraintCoverage] = score(covered, int64(len(expected.RequiredConstraints)))
		valid := int64(0)
		if out.Outcome == "plan" && len(out.Findings) == 0 {
			valid = 1
		}
		scores[MetricPlanValidity] = score(valid, 1)
		opValid := int64(0)
		if result.Prepared != nil && exactOperations(expected.Operations, *result.Prepared) {
			opValid = 1
		}
		scores[MetricOperationSelection] = score(opValid, 1)
	} else {
		scores[MetricConstraintCoverage] = score(0, 0)
		scores[MetricPlanValidity] = score(0, 0)
		scores[MetricOperationSelection] = score(0, 0)
	}
	if expected.Outcome == "plan" || expected.Outcome == "insufficient_context" {
		correct := int64(0)
		if out.Outcome == expected.Outcome && (out.Outcome != "insufficient_context" || out.ErrorPath == "") {
			correct = 1
		}
		scores[MetricInsufficientContext] = score(correct, 1)
	} else {
		scores[MetricInsufficientContext] = score(0, 0)
	}
	compliant := int64(0)
	if out.LimitCompliant {
		compliant = 1
	}
	scores[MetricLimitCompliance] = score(compliant, 1)
	return scores
}

// missingContextMatches compares exact kind and constraint sets while ignoring model prose and ordering.
func missingContextMatches(expected CaseExpectation, items []agentplanning.MissingItem) bool {
	wantKinds := stringSetMissing(expected.MissingKinds)
	wantConstraints := stringSet(expected.MissingConstraints)
	gotKinds := map[string]bool{}
	gotConstraints := map[string]bool{}
	for _, item := range items {
		gotKinds[string(item.Kind)] = true
		for _, id := range item.ConstraintIDs {
			gotConstraints[id] = true
		}
	}
	return equalStringSets(wantKinds, gotKinds) && equalStringSets(wantConstraints, gotConstraints)
}

// stringSetMissing converts a validated missing-kind set to its stable string representation.
func stringSetMissing(values []agentplanning.MissingKind) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[string(value)] = true
	}
	return out
}

// equalStringSets requires exact membership and cardinality for bounded structural sets.
func equalStringSets(left, right map[string]bool) bool {
	if len(left) != len(right) {
		return false
	}
	for value := range left {
		if !right[value] {
			return false
		}
	}
	return true
}

// checkedIntDelta rejects decreasing or overflow-prone accounting snapshots.
func checkedIntDelta(after, before int) (int, bool) {
	if before < 0 || after < before {
		return 0, false
	}
	return after - before, true
}

// checkedInt64Delta rejects decreasing token accounting snapshots.
func checkedInt64Delta(after, before int64) (int64, bool) {
	if before < 0 || after < before {
		return 0, false
	}
	return after - before, true
}

// checkedDurationDelta rejects decreasing source duration snapshots.
func checkedDurationDelta(after, before time.Duration) (time.Duration, bool) {
	if before < 0 || after < before {
		return 0, false
	}
	return after - before, true
}

// resultWork counts bounded prepared structures with checked arithmetic for the work metric.
func resultWork(result agentplanning.Result) (int64, bool) {
	if result.Prepared == nil {
		if result.Missing != nil {
			return int64(len(result.Missing.Items)), true
		}
		return 0, true
	}
	var work int64
	for _, prepared := range result.Prepared.Checks() {
		if work == int64(^uint64(0)>>1) {
			return 0, false
		}
		work++
		for _, step := range prepared.Plan().Plan().Steps {
			addition := int64(len(step.Assertions))
			if addition == int64(^uint64(0)>>1) {
				return 0, false
			}
			addition++
			if work > int64(^uint64(0)>>1)-addition {
				return 0, false
			}
			work += addition
		}
	}
	return work, true
}

// exactOperations compares canonical operation sets and rejects missing or extra selections.
func exactOperations(expected []OperationExpectation, prepared agentplanning.PreparedPlan) bool {
	want := make([]string, 0, len(expected))
	for _, op := range expected {
		want = append(want, operationKey(op.ConstraintID, op.Method, op.Path))
	}
	got := []string{}
	for _, p := range prepared.Checks() {
		c := p.Check()
		if c.Operation != nil {
			got = append(got, operationKey(c.ConstraintID, c.Operation.Method, c.Operation.Path))
		}
	}
	sort.Strings(want)
	sort.Strings(got)
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if want[i] != got[i] {
			return false
		}
	}
	return true
}

// operationKey creates a collision-safe canonical operation identity.
func operationKey(constraint, method, path string) string {
	return constraint + "\x00" + strings.ToUpper(method) + "\x00" + path
}

// stringSet builds a bounded exact-membership index from a validated slice.
func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, v := range values {
		out[v] = true
	}
	return out
}

// sortFindings stabilizes oracle output independently of input map traversal.
func sortFindings(values []Finding) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].Category != values[j].Category {
			return values[i].Category < values[j].Category
		}
		if values[i].CaseID != values[j].CaseID {
			return values[i].CaseID < values[j].CaseID
		}
		return values[i].Path < values[j].Path
	})
}
