package correction

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/asierhr/featureProof/internal/externalagent"
	"github.com/asierhr/featureProof/internal/localvalidation"
)

// VerifiedPackageReader delegates package trust exclusively to localvalidation.ReadPackage.
type VerifiedPackageReader struct{}

// Read verifies inventory, binding and references before returning package semantics.
func (VerifiedPackageReader) Read(ctx context.Context, locator string) (localvalidation.PackageIndex, error) {
	return localvalidation.ReadPackage(ctx, locator)
}

// ExternalAdapter projects a coordinator into the shared CLI/MCP application contract.
type ExternalAdapter struct{ Coordinator *Coordinator }

// SubmitCorrection admits only the three bounded fields supplied by the external contract.
func (a ExternalAdapter) SubmitCorrection(ctx context.Context, principal string, input externalagent.CorrectionSubmitInput) (externalagent.CorrectionView, error) {
	if a.Coordinator == nil {
		return externalagent.CorrectionView{}, &externalagent.AppError{Code: externalagent.CodeInternal}
	}
	record, err := a.Coordinator.Submit(ctx, SubmitRequest{PrincipalID: principal, ParentHandle: input.ParentRunHandle, SelectionAlias: input.SelectionAlias, IdempotencyKey: input.IdempotencyKey})
	if err != nil {
		return externalagent.CorrectionView{}, externalError(err)
	}
	return projectCorrection(record), nil
}

// CorrectionStatus rechecks principal ownership before projecting lifecycle.
func (a ExternalAdapter) CorrectionStatus(ctx context.Context, principal string, input externalagent.CorrectionHandleInput) (externalagent.CorrectionView, error) {
	record, err := a.owned(ctx, principal, input.CorrectionHandle)
	if err != nil {
		return externalagent.CorrectionView{}, err
	}
	return projectCorrection(record), nil
}

// CorrectionResult exposes only terminal allowlisted comparison fields and opaque evidence handles.
func (a ExternalAdapter) CorrectionResult(ctx context.Context, principal string, input externalagent.CorrectionHandleInput) (externalagent.CorrectionResultView, error) {
	record, err := a.owned(ctx, principal, input.CorrectionHandle)
	if err != nil {
		return externalagent.CorrectionResultView{}, err
	}
	if record.Phase != PhaseTerminal {
		return externalagent.CorrectionResultView{}, &externalagent.AppError{Code: externalagent.CodeInProgress, Retryable: true}
	}
	result := externalagent.CorrectionResultView{CorrectionView: projectCorrection(record), Corrected: []externalagent.ComparisonEntryView{}, Persistent: []externalagent.ComparisonEntryView{}, New: []externalagent.ComparisonEntryView{}, NotApplicable: []externalagent.ComparisonEntryView{}, Inconclusive: []externalagent.ComparisonEntryView{}}
	if record.Comparison == nil {
		return result, nil
	}
	for _, entry := range record.Comparison.Entries {
		view := externalagent.ComparisonEntryView{Scenario: externalagent.ComparisonScenarioView{ConstraintID: entry.Identity.ConstraintID, Scenario: entry.Identity.Scenario, BatchID: entry.Identity.BatchID, CheckID: entry.Identity.CheckID, StepID: entry.Identity.StepID, DeclarationID: entry.Identity.DeclarationID}, Class: string(entry.Class), EvidenceHandles: []string{}}
		for _, evidence := range entry.Evidence {
			view.EvidenceHandles = append(view.EvidenceHandles, opaqueEvidence(record.Link.PrincipalID, evidence))
		}
		switch entry.Class {
		case Corrected:
			result.Corrected = append(result.Corrected, view)
		case Persistent:
			result.Persistent = append(result.Persistent, view)
		case NewFailure:
			result.New = append(result.New, view)
		case NotApplicable:
			result.NotApplicable = append(result.NotApplicable, view)
		case Inconclusive:
			result.Inconclusive = append(result.Inconclusive, view)
		}
	}
	return result, nil
}

// CancelCorrection persists and joins cancellation through the coordinator supervisor.
func (a ExternalAdapter) CancelCorrection(ctx context.Context, principal string, input externalagent.CorrectionCancelInput) (externalagent.CorrectionView, error) {
	if a.Coordinator == nil {
		return externalagent.CorrectionView{}, &externalagent.AppError{Code: externalagent.CodeInternal}
	}
	record, err := a.Coordinator.Cancel(ctx, input.CorrectionHandle, principal, input.IdempotencyKey)
	if err != nil {
		return externalagent.CorrectionView{}, externalError(err)
	}
	return projectCorrection(record), nil
}

// owned maps malformed, missing and foreign correction handles to the same public result.
func (a ExternalAdapter) owned(ctx context.Context, principal, handle string) (Record, error) {
	if a.Coordinator == nil {
		return Record{}, &externalagent.AppError{Code: externalagent.CodeInternal}
	}
	record, err := a.Coordinator.Status(ctx, handle)
	if err != nil || record.Link.PrincipalID != principal {
		return Record{}, &externalagent.AppError{Code: externalagent.CodeNotFound}
	}
	return record, nil
}

// projectCorrection maps private lineage to the bounded lifecycle DTO.
func projectCorrection(record Record) externalagent.CorrectionView {
	return externalagent.CorrectionView{CorrectionHandle: record.Link.Handle, ParentRunHandle: opaqueRun(record.Link.PrincipalID, record.Link.ParentRunID), ChildRunHandle: opaqueOptionalRun(record.Link.PrincipalID, record.Link.ChildRunID), Phase: string(record.Phase), Status: externalagent.Outcome(record.Outcome), TerminalReason: string(record.TerminalReason), LimitDimension: safeLimitDimension(record.LimitDimension), UpdatedAt: record.Link.UpdatedAt}
}

// safeLimitDimension exposes only the four cumulative budget axes approved by the application contract.
func safeLimitDimension(value string) string {
	switch value {
	case "attempts", "duration", "model_calls", "model_tokens":
		return value
	default:
		return ""
	}
}

// opaqueRun derives a stable non-reversible handle from private run identity.
func opaqueRun(principal, run string) string {
	sum := sha256.Sum256([]byte(principal + "\x00run\x00" + run))
	return "run_" + hex.EncodeToString(sum[:16])
}

// opaqueOptionalRun omits an unconfirmed child instead of hashing an empty identity.
func opaqueOptionalRun(principal, run string) string {
	if run == "" {
		return ""
	}
	return opaqueRun(principal, run)
}

// opaqueEvidence derives a stable handle scoped to principal, origin and verified evidence ID.
func opaqueEvidence(principal string, evidence EvidenceReference) string {
	sum := sha256.Sum256([]byte(principal + "\x00evidence\x00" + evidence.Origin + "\x00" + evidence.EvidenceID))
	return "evidence_" + hex.EncodeToString(sum[:16])
}

// externalError translates internal categories to the existing safe application vocabulary.
func externalError(err error) error {
	var correctionError *Error
	if !errors.As(err, &correctionError) {
		return &externalagent.AppError{Code: externalagent.CodeInternal}
	}
	switch correctionError.Category {
	case InvalidRequest:
		return &externalagent.AppError{Code: externalagent.CodeInvalidRequest, Field: "request"}
	case NotFound:
		return &externalagent.AppError{Code: externalagent.CodeNotFound}
	case Conflict:
		return &externalagent.AppError{Code: externalagent.CodeConflict}
	default:
		return &externalagent.AppError{Code: externalagent.CodeInternal, Retryable: correctionError.Category == Infrastructure}
	}
}

// LocalComparisonPublisher writes canonical comparison bytes atomically and never replaces a result.
type LocalComparisonPublisher struct{ Root string }

// Publish fsyncs staging before a no-replace rename into the correction directory.
func (p LocalComparisonPublisher) Publish(ctx context.Context, handle string, comparison Comparison) error {
	if err := ctx.Err(); err != nil || !safeID.MatchString(handle) || !filepath.IsAbs(p.Root) {
		return &Error{Category: Infrastructure, Field: "comparison_publish"}
	}
	if !validComparisonDocument(comparison) {
		return &Error{Category: IntegrityFailure, Field: "comparison_publish"}
	}
	raw, err := json.Marshal(comparison)
	if err != nil {
		return &Error{Category: Infrastructure, Field: "comparison_publish"}
	}
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		return &Error{Category: Infrastructure, Field: "comparison_publish"}
	}
	target := filepath.Join(p.Root, handle+".json")
	temp, err := os.CreateTemp(p.Root, ".comparison-*")
	if err != nil {
		return &Error{Category: Infrastructure, Field: "comparison_publish"}
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return &Error{Category: Infrastructure, Field: "comparison_publish"}
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return &Error{Category: Infrastructure, Field: "comparison_publish"}
	}
	if err := temp.Close(); err != nil {
		return &Error{Category: Infrastructure, Field: "comparison_publish"}
	}
	if err := os.Link(name, target); errors.Is(err, os.ErrExist) {
		matched, resolveErr := p.Resolve(ctx, handle, comparison.Digest)
		if resolveErr != nil || !matched {
			return &Error{Category: Conflict, Field: "comparison_publish"}
		}
		return nil
	} else if err != nil {
		return &Error{Category: Infrastructure, Field: "comparison_publish"}
	}
	_ = os.Remove(name)
	return nil
}

// Resolve verifies whether the exact comparison digest is already visible after an uncertain publish response.
func (p LocalComparisonPublisher) Resolve(ctx context.Context, handle, digest string) (bool, error) {
	if err := ctx.Err(); err != nil || !safeID.MatchString(handle) || !filepath.IsAbs(p.Root) || !digestPattern.MatchString(digest) {
		return false, &Error{Category: Infrastructure, Field: "comparison_publish"}
	}
	raw, err := os.ReadFile(filepath.Join(p.Root, handle+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, &Error{Category: Infrastructure, Field: "comparison_publish"}
	}
	var comparison Comparison
	if json.Unmarshal(raw, &comparison) != nil || comparison.Digest != digest || !validComparisonDocument(comparison) {
		return false, &Error{Category: IntegrityFailure, Field: "comparison_publish"}
	}
	canonical, marshalErr := json.Marshal(comparison)
	if marshalErr != nil || !bytes.Equal(raw, canonical) {
		return false, &Error{Category: IntegrityFailure, Field: "comparison_publish"}
	}
	return true, nil
}

// validComparisonDocument verifies the stable identity and canonical digest before an artifact crosses publication.
func validComparisonDocument(comparison Comparison) bool {
	if comparison.Version != Version || !safeID.MatchString(comparison.ParentRunID) || !safeID.MatchString(comparison.ChildRunID) || !digestPattern.MatchString(comparison.Digest) {
		return false
	}
	want := comparison.Digest
	comparison.Digest = ""
	return want == hashCanonical(comparison)
}
