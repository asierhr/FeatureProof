package agenteval

import (
	"context"
	"sort"
)

// Delta records a candidate-minus-baseline quality change and its classification.
type Delta struct {
	Category       string   `json:"category"`
	Value          int64    `json:"value"`
	Classification string   `json:"classification"`
	AffectedCases  []string `json:"affected_cases,omitempty"`
}

// Comparison is the canonical compatible-baseline result applied after absolute thresholds.
type Comparison struct {
	SchemaVersion int     `json:"schema_version"`
	Passed        bool    `json:"passed"`
	Deltas        []Delta `json:"deltas"`
}

// BundleSnapshot contains only verified baseline fields needed for compatibility and comparison.
type BundleSnapshot struct {
	Dataset                                        DatasetIdentity
	CaseDigests                                    map[string]string
	MetricSchema, PlanningProtocol, CriteriaSchema string
	Cases                                          []CaseResult
	Metrics                                        Metrics
}

// Compare rejects benchmark drift before calculating exact aggregate and per-case deltas.
func Compare(ctx context.Context, candidate RunResult, baseline BundleSnapshot, criteria Criteria) (Comparison, error) {
	if err := ctx.Err(); err != nil {
		return Comparison{}, contextEvalError("compare", err)
	}
	if baseline.Dataset != candidate.Dataset || baseline.MetricSchema != "agent-eval-metrics/v1" || baseline.PlanningProtocol != "agentplanning/v1" || baseline.CriteriaSchema != "agent-eval-criteria/v1" || len(baseline.CaseDigests) != len(candidate.Cases) {
		return Comparison{}, typedError(IncompatibleBaseline, "compare", "compatibility", nil)
	}
	for _, c := range candidate.Cases {
		if baseline.CaseDigests[c.CaseID] != c.CaseDigest {
			return Comparison{}, typedError(IncompatibleBaseline, "compare", "cases", nil)
		}
	}
	out := Comparison{SchemaVersion: 1, Passed: true}
	for _, name := range []MetricName{MetricConstraintCoverage, MetricPlanValidity, MetricOperationSelection, MetricInsufficientContext, MetricLimitCompliance} {
		current, old := candidate.Metrics.Scores[name], baseline.Metrics.Scores[name]
		if current.Applicable != old.Applicable {
			return Comparison{}, typedError(IncompatibleBaseline, "compare", string(name), nil)
		}
		if !current.Applicable {
			continue
		}
		delta := current.PPM - old.PPM
		out.Deltas = append(out.Deltas, makeDelta(string(name), delta, affectedCases(candidate.Cases, baseline.Cases, string(name))))
		if delta < -criteria.MaxNegativeDeltaPPM[string(name)] {
			out.Passed = false
		}
	}
	for _, category := range []InventionCategory{InventionEndpoint, InventionCapability, InventionIdentity, InventionCredential, InventionEvidence} {
		delta := baseline.Metrics.Inventions[category] - candidate.Metrics.Inventions[category]
		out.Deltas = append(out.Deltas, makeDelta(string(category), delta, affectedCases(candidate.Cases, baseline.Cases, string(category))))
		if delta < -criteria.MaxNegativeDeltaPPM[string(category)] {
			out.Passed = false
		}
	}
	sort.Slice(out.Deltas, func(i, j int) bool { return out.Deltas[i].Category < out.Deltas[j].Category })
	return out, nil
}

// ValidateBaselineCompatibility rejects dataset and schema drift before source composition or inference.
func ValidateBaselineCompatibility(suite Suite, baseline BundleSnapshot) error {
	if baseline.Dataset != suite.Identity() || baseline.MetricSchema != "agent-eval-metrics/v1" || baseline.PlanningProtocol != "agentplanning/v1" || baseline.CriteriaSchema != "agent-eval-criteria/v1" {
		return typedError(IncompatibleBaseline, "compare", "compatibility", nil)
	}
	cases := suite.Cases()
	if len(baseline.CaseDigests) != len(cases) {
		return typedError(IncompatibleBaseline, "compare", "cases", nil)
	}
	for _, c := range cases {
		if baseline.CaseDigests[c.ID()] != c.Digest() {
			return typedError(IncompatibleBaseline, "compare", "cases", nil)
		}
	}
	return nil
}

// makeDelta classifies zero and signed quality movement consistently across scores and safety counts.
func makeDelta(category string, value int64, affected []string) Delta {
	class := "unchanged"
	if value > 0 {
		class = "improved"
	} else if value < 0 {
		class = "regressed"
	}
	sort.Strings(affected)
	return Delta{Category: category, Value: value, Classification: class, AffectedCases: affected}
}

// affectedCases identifies stable case IDs whose contribution changed for one exact category.
func affectedCases(candidate, baseline []CaseResult, category string) []string {
	old := map[string]CaseResult{}
	for _, c := range baseline {
		old[c.CaseID] = c
	}
	out := []string{}
	for _, c := range candidate {
		b, ok := old[c.CaseID]
		if !ok || caseContribution(c, category) != caseContribution(b, category) {
			out = append(out, c.CaseID)
		}
	}
	return out
}

// caseContribution returns the metric numerator/denominator pair or exact finding count for one case.
func caseContribution(value CaseResult, category string) string {
	for _, metric := range []MetricName{MetricConstraintCoverage, MetricPlanValidity, MetricOperationSelection, MetricInsufficientContext, MetricLimitCompliance} {
		if category == string(metric) {
			score := value.Metrics[metric]
			return itoa64(score.Numerator) + "/" + itoa64(score.Denominator)
		}
	}
	count := int64(0)
	for _, finding := range value.Findings {
		if string(finding.Category) == category {
			count++
		}
	}
	return itoa64(count)
}

// itoa64 formats non-negative bounded metric values without float conversion.
func itoa64(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	digits := [20]byte{}
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		position--
		digits[position] = '-'
	}
	return string(digits[position:])
}
