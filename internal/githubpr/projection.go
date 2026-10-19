package githubpr

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/asierhr/featureProof/internal/localvalidation"
)

// EvidenceLinker creates public links exclusively from verified opaque evidence IDs.
type EvidenceLinker struct {
	base *url.URL
	max  int
}

// NewEvidenceLinker accepts only an exact HTTPS origin without credentials, query, or fragment.
func NewEvidenceLinker(raw string, max int) (*EvidenceLinker, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || max < 64 {
		return nil, &Error{Code: CodeInvalid, Operation: "evidence_origin"}
	}
	parsed.Path = strings.TrimSuffix(parsed.EscapedPath(), "/")
	return &EvidenceLinker{base: parsed, max: max}, nil
}

// Link maps one allowlisted opaque identifier onto the configured exact origin.
func (l *EvidenceLinker) Link(handle string) (string, bool) {
	if l == nil || !safeName.MatchString(handle) || strings.Contains(handle, "..") {
		return "", false
	}
	copy := *l.base
	copy.Path = path.Join(l.base.Path, "evidence", handle)
	copy.RawPath, copy.RawQuery, copy.Fragment, copy.User = "", "", "", nil
	value := copy.String()
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != l.base.Scheme || parsed.Host != l.base.Host || len(value) > l.max {
		return "", false
	}
	return value, true
}

// Projection is the bounded terminal or progress summary shared by checks and comments.
type Projection struct {
	Outcome  string
	Title    string
	Summary  string
	Unlinked int
}

// Projector turns verified packages into bounded GitHub-safe summaries.
type Projector struct {
	Packages             PackageReader
	Links                *EvidenceLinker
	MaxItems, MaxSummary int
}

// ProjectTerminal verifies run identity, head binding, and package contents before rendering facts.
func (p Projector) ProjectTerminal(ctx context.Context, association Association, run RunState) (Projection, error) {
	if p.Packages == nil || p.MaxItems <= 0 || p.MaxSummary <= 0 {
		return Projection{}, &Error{Code: CodeInternal, Operation: "project"}
	}
	if run.Package == "" && (run.Outcome == "error" || run.Outcome == "cancelled" || run.Outcome == "inconclusive") {
		if !association.Provisional || run.RunHandle == "" || association.RunHandle != run.RunHandle || run.RepositoryID != association.Snapshot.RepositoryID || run.HeadOID != association.Snapshot.HeadOID {
			return Projection{}, &Error{Code: CodeIntegrity, Operation: "operational_lineage"}
		}
		outcome := safeOutcome(run.Outcome)
		return Projection{Outcome: outcome, Title: "FeatureProof: " + outcome, Summary: "FeatureProof outcome: **" + outcome + "**\n\nOperational preview validation ended without an evidence package."}, nil
	}
	if run.Package == "" {
		return Projection{}, &Error{Code: CodeInternal, Operation: "project"}
	}
	index, err := p.Packages.Read(ctx, run.Package)
	if err != nil || run.PackageRunID == "" || index.RunID != run.PackageRunID || association.PackageRunID != run.PackageRunID || association.BindingSHA256 == "" || run.BindingSHA256 != association.BindingSHA256 || index.Binding.BindingSHA256 != association.BindingSHA256 || index.Binding.CommitOID != association.Snapshot.HeadOID || index.Binding.RepositoryID != fmt.Sprint(association.Snapshot.RepositoryID) || string(index.Status) != run.Outcome {
		return Projection{}, &Error{Code: CodeIntegrity, Operation: "package"}
	}
	items := make([]string, 0, len(index.Constraints)+len(index.Failures))
	for _, constraint := range index.Constraints {
		entry := fmt.Sprintf("- `%s`: **%s**", safeToken(constraint.ConstraintID), safeOutcome(string(constraint.Status)))
		entry = p.appendEvidence(entry, constraint.EvidenceIDs)
		if string(constraint.Status) == "inconclusive" {
			entry += " (unverified)"
		}
		items = append(items, entry)
	}
	for _, failure := range index.Failures {
		entry := fmt.Sprintf("- failure `%s` scenario `%s` step `%s` assertion `%s` operator `%s`", safeToken(failure.ConstraintID), safeToken(failure.Scenario), safeToken(failure.StepID), safeToken(failure.DeclarationID), safeToken(failure.Operator))
		entry = p.appendEvidence(entry, failure.EvidenceIDs)
		items = append(items, entry)
	}
	sort.Strings(items)
	omitted := 0
	if len(items) > p.MaxItems {
		omitted = len(items) - p.MaxItems
		items = items[:p.MaxItems]
	}
	summary := "FeatureProof outcome: **" + safeOutcome(run.Outcome) + "**\n\n" + strings.Join(items, "\n")
	if omitted > 0 {
		summary += fmt.Sprintf("\n- %d additional items omitted", omitted)
	}
	for len(summary) > p.MaxSummary && len(items) > 0 {
		items = items[:len(items)-1]
		omitted++
		summary = "FeatureProof outcome: **" + safeOutcome(run.Outcome) + "**\n\n" + strings.Join(items, "\n") + fmt.Sprintf("\n- %d additional items omitted", omitted)
	}
	if len(summary) > p.MaxSummary {
		return Projection{}, &Error{Code: CodeInvalid, Operation: "summary_limit"}
	}
	return Projection{Outcome: safeOutcome(run.Outcome), Title: "FeatureProof: " + safeOutcome(run.Outcome), Summary: summary, Unlinked: omitted}, nil
}

// appendEvidence renders every verified opaque reference or an explicit unavailable marker.
func (p Projector) appendEvidence(entry string, ids []string) string {
	if len(ids) == 0 {
		return entry + " (evidence unavailable)"
	}
	for _, id := range ids {
		if p.Links != nil {
			if link, ok := p.Links.Link(id); ok {
				entry += " ([" + safeToken(id) + "](" + link + "))"
				continue
			}
		}
		entry += " (`" + safeToken(id) + "` unavailable)"
	}
	return entry
}

// ProgressProjection renders lifecycle without claiming terminal evidence.
func ProgressProjection(phase string) Projection {
	if phase != "queued" && phase != "running" && phase != "finalizing" {
		phase = "running"
	}
	return Projection{Outcome: phase, Title: "FeatureProof validation", Summary: "FeatureProof status: **" + phase + "**"}
}

// safeOutcome maps untrusted state to the closed publication vocabulary.
func safeOutcome(value string) string {
	switch value {
	case "passed", "failed", "inconclusive", "error", "cancelled", "queued", "running", "finalizing", "stale":
		return value
	default:
		return "error"
	}
}

// safeToken prevents Markdown injection while retaining structural identifiers.
func safeToken(value string) string {
	if safeName.MatchString(value) {
		return value
	}
	return "invalid"
}

// packageReaderAdapter documents the exact external package shape consumed by the projector.
var _ = localvalidation.PackageIndex{}
