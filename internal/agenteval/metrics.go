package agenteval

import (
	"context"
	"math"
	"sort"
)

// score calculates exact integer ppm and marks zero denominators explicitly inapplicable.
func score(numerator, denominator int64) Score {
	if denominator == 0 {
		return Score{Numerator: numerator, Denominator: 0, Applicable: false}
	}
	if numerator < 0 || denominator < 0 || numerator > denominator || numerator > math.MaxInt64/1_000_000 {
		return Score{Numerator: numerator, Denominator: denominator, PPM: -1, Applicable: true}
	}
	return Score{Numerator: numerator, Denominator: denominator, PPM: numerator * 1_000_000 / denominator, Applicable: true}
}

// Aggregate sums per-case rational metrics with checked arithmetic and computes a canonical digest.
func Aggregate(ctx context.Context, cases []CaseResult) (Metrics, error) {
	out := Metrics{SchemaVersion: 1, Scores: map[MetricName]Score{}, Inventions: map[InventionCategory]int64{}, Confusion: map[string]int64{"tp": 0, "fp": 0, "tn": 0, "fn": 0}}
	names := []MetricName{MetricConstraintCoverage, MetricPlanValidity, MetricOperationSelection, MetricInsufficientContext, MetricLimitCompliance}
	for _, name := range names {
		var n, d int64
		for _, c := range cases {
			if err := ctx.Err(); err != nil {
				return Metrics{}, contextEvalError("metrics", err)
			}
			v := c.Metrics[name]
			if v.Numerator < 0 || v.Denominator < 0 || n > math.MaxInt64-v.Numerator || d > math.MaxInt64-v.Denominator {
				return Metrics{}, typedError(LimitExceeded, "metrics", string(name), nil)
			}
			n += v.Numerator
			d += v.Denominator
		}
		value := score(n, d)
		if value.PPM < 0 {
			return Metrics{}, typedError(LimitExceeded, "metrics", string(name), nil)
		}
		out.Scores[name] = value
	}
	for _, c := range cases {
		if c.Outcome == "failed" || (c.ExpectedOutcome != "" && c.Outcome != c.ExpectedOutcome) || (c.ErrorCategory != "" && c.Outcome != "rejected") {
			out.ExpectationFailures++
		}
		for _, f := range c.Findings {
			if out.Inventions[f.Category] == math.MaxInt64 {
				return Metrics{}, typedError(LimitExceeded, "metrics", "inventions", nil)
			}
			out.Inventions[f.Category]++
		}
		if c.ExpectedOutcome == "insufficient_context" {
			if c.Outcome == "insufficient_context" {
				out.Confusion["tp"]++
			} else {
				out.Confusion["fn"]++
			}
		} else if c.ExpectedOutcome == "plan" {
			if c.Outcome == "insufficient_context" {
				out.Confusion["fp"]++
			} else {
				out.Confusion["tn"]++
			}
		}
	}
	canonicalCases := append([]CaseResult(nil), cases...)
	sort.Slice(canonicalCases, func(i, j int) bool { return canonicalCases[i].CaseID < canonicalCases[j].CaseID })
	payload := struct {
		Cases               []CaseResult                `json:"cases"`
		Scores              map[MetricName]Score        `json:"scores"`
		Inventions          map[InventionCategory]int64 `json:"inventions"`
		ExpectationFailures int64                       `json:"expectation_failures"`
	}{canonicalCases, out.Scores, out.Inventions, out.ExpectationFailures}
	data, err := canonicalJSON(payload)
	if err != nil {
		return Metrics{}, typedError(InvalidInput, "metrics", "canonical", nil)
	}
	out.ResultDigest = digestBytes(data)
	return out, nil
}

// Evaluate applies hard fails and absolute thresholds without consulting model prose.
func Evaluate(criteria Criteria, metrics Metrics, comparison *Comparison) (string, []string) {
	reasons := []string{}
	if metrics.ExpectationFailures > 0 {
		reasons = append(reasons, "incomplete_measurement")
	}
	for _, category := range criteria.HardFails {
		if metrics.Inventions[category] > 0 {
			reasons = append(reasons, "hard_fail:"+string(category))
		}
	}
	for name, minimum := range criteria.MinimumPPM {
		value := metrics.Scores[name]
		if value.Applicable && value.PPM < minimum {
			reasons = append(reasons, "threshold:"+string(name))
		}
	}
	if comparison != nil && !comparison.Passed {
		reasons = append(reasons, "baseline_regression")
	}
	sort.Strings(reasons)
	if len(reasons) > 0 {
		return "failed", reasons
	}
	return "passed", nil
}
