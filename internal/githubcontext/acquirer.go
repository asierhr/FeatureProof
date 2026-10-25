package githubcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// unifiedHunkHeader recognizes the complete bounded unified-diff range grammar emitted by GitHub.
var unifiedHunkHeader = regexp.MustCompile(`^@@ -(0|[1-9][0-9]*)(?:,(0|[1-9][0-9]*))? \+(0|[1-9][0-9]*)(?:,(0|[1-9][0-9]*))? @@(?: [^\r\n]*)?$`)

// terminalIdentity binds one content-free terminal outcome to its acquisition without trusting raw request fields.
type terminalIdentity struct {
	AcquisitionDigest string            `json:"acquisition_digest"`
	RunID             string            `json:"run_id,omitempty"`
	RepositoryID      int64             `json:"repository_id,omitempty"`
	PullRequestNumber int64             `json:"pull_request_number,omitempty"`
	HeadOID           string            `json:"head_oid,omitempty"`
	BaseOID           string            `json:"base_oid,omitempty"`
	PolicyDigest      string            `json:"policy_digest,omitempty"`
	Outcome           string            `json:"outcome"`
	Category          Category          `json:"category,omitempty"`
	ObservationDigest string            `json:"observation_digest,omitempty"`
	ContextDigest     string            `json:"context_digest,omitempty"`
	Completeness      Completeness      `json:"completeness,omitempty"`
	Counts            Counts            `json:"counts"`
	OmissionReasons   []OmissionSummary `json:"omission_reasons"`
}

// hunkRange preserves old/new half-open coordinates and declared counts for cross-hunk validation.
type hunkRange struct {
	oldStart int
	oldEnd   int
	newStart int
	newEnd   int
	oldCount int
	newCount int
}

// Gateway is the minimal read-only GitHub context port owned by this consumer.
type Gateway interface {
	Metadata(context.Context, int64, int64, int64, int) (RemoteMetadata, error)
	Files(context.Context, int64, int64, int64, int, int, int) (FilePage, error)
}

// SecretGate rejects sensitive bounded units before persistence or model projection.
type SecretGate interface {
	Check(context.Context, []byte) error
}

// CanaryGate rejects configured canaries and common credential assignments in bounded units.
type CanaryGate struct {
	canaries [][]byte
	patterns []*regexp.Regexp
}

// NewCanaryGate copies private canaries and installs content-only credential detectors.
func NewCanaryGate(canaries [][]byte) *CanaryGate {
	copied := make([][]byte, len(canaries))
	for index := range canaries {
		copied[index] = append([]byte(nil), canaries[index]...)
	}
	return &CanaryGate{canaries: copied, patterns: []*regexp.Regexp{regexp.MustCompile(`(?i)bearer\s+[a-z0-9._-]{8,}`), regexp.MustCompile(`(?i)(token|client_secret|access_token|api_key|password)\s*[:=]\s*[a-z0-9._-]{8,}`)}}
}

// Check scans one already-bounded external unit before any durable or model-facing use.
func (g *CanaryGate) Check(ctx context.Context, raw []byte) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, canary := range g.canaries {
		if len(canary) > 0 && bytes.Contains(raw, canary) {
			return &Error{Category: CategorySecret, Stage: "secret_gate"}
		}
	}
	for _, pattern := range g.patterns {
		if pattern.Match(raw) {
			return &Error{Category: CategorySecret, Stage: "secret_gate"}
		}
	}
	return nil
}

// Clock supplies deterministic time and cancellable waits for retry budgets.
type Clock interface {
	Now() time.Time
	Wait(context.Context, time.Duration) error
}

// Store persists confirmed contexts and idempotency receipts without remote payload logs.
type Store interface {
	Load(context.Context, string) (string, Context, Binding, bool, error)
	Commit(context.Context, string, string, Context, Binding) error
}

// Observer receives one content-free terminal record for each acquisition call.
type Observer interface {
	Observe(context.Context, Record) error
}

// Dependencies are immutable composition-owned capabilities used by Acquirer.
type Dependencies struct {
	Gateway  Gateway
	Secrets  SecretGate
	Clock    Clock
	Store    Store
	Observer Observer
}

// Acquirer coordinates one bounded coherent observation without retaining caller context.
type Acquirer struct{ deps Dependencies }

// NewAcquirer rejects incomplete composition before any request can cross a boundary.
func NewAcquirer(deps Dependencies) (*Acquirer, error) {
	if deps.Gateway == nil || deps.Secrets == nil || deps.Clock == nil || deps.Store == nil || deps.Observer == nil {
		return nil, &Error{Category: CategoryInvalid, Stage: "composition"}
	}
	return &Acquirer{deps: deps}, nil
}

// Acquire obtains, normalizes, scans, seals and idempotently confirms one PR context.
func (a *Acquirer) Acquire(ctx context.Context, request Request, policy Policy) (result Context, binding Binding, err error) {
	started := a.deps.Clock.Now()
	attempts := 0
	defer func() {
		record := terminalRecord(request, result, err, attempts, a.deps.Clock.Now().Sub(started))
		observerCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		if observeErr := a.deps.Observer.Observe(observerCtx, record); observeErr != nil {
			err = terminalObserverError(observerCtx, err, observeErr)
		}
	}()
	if ctx.Err() != nil {
		return Context{}, Binding{}, contextError(ctx, "acquire")
	}
	if validateErr := ValidateRequest(request); validateErr != nil {
		return Context{}, Binding{}, validateErr
	}
	sealedPolicy, sealErr := SealPolicy(policy)
	if sealErr != nil || sealedPolicy.Digest != request.PolicyDigest {
		return Context{}, Binding{}, &Error{Category: CategoryUnauthorized, Stage: "policy"}
	}
	bounded, cancel := context.WithTimeout(ctx, sealedPolicy.Limits.MaxDuration)
	defer cancel()
	fingerprint, _ := digestValue(request)
	storedFingerprint, stored, storedBinding, found, loadErr := a.deps.Store.Load(bounded, request.IdempotencyKey)
	if bounded.Err() != nil {
		return Context{}, Binding{}, contextError(bounded, "load")
	}
	if loadErr != nil {
		return Context{}, Binding{}, &Error{Category: CategoryPersistence, Stage: "load", Cause: loadErr}
	}
	if found {
		if storedFingerprint != fingerprint {
			return Context{}, Binding{}, &Error{Category: CategoryConflict, Stage: "idempotency"}
		}
		return stored, storedBinding, nil
	}
	var last error
	for attempts = 1; attempts <= sealedPolicy.Limits.MaxAttempts; attempts++ {
		result, binding, last = a.acquireOnce(bounded, request, sealedPolicy)
		if last == nil {
			if commitErr := a.deps.Store.Commit(bounded, request.IdempotencyKey, fingerprint, result, binding); commitErr != nil {
				if bounded.Err() != nil {
					return Context{}, Binding{}, contextError(bounded, "commit")
				}
				return Context{}, Binding{}, &Error{Category: CategoryPersistence, Stage: "commit", Cause: commitErr}
			}
			if bounded.Err() != nil {
				return Context{}, Binding{}, contextError(bounded, "commit")
			}
			return result, binding, nil
		}
		if bounded.Err() != nil {
			return Context{}, Binding{}, contextError(bounded, "acquire")
		}
		if !IsCategory(last, CategoryTransient) && !IsCategory(last, CategoryRateLimited) {
			return Context{}, Binding{}, last
		}
		if attempts == sealedPolicy.Limits.MaxAttempts {
			break
		}
		wait := retryDelay(last, attempts, a.deps.Clock.Now(), sealedPolicy.Limits.MaxRetryWait)
		if deadline, ok := bounded.Deadline(); ok && time.Until(deadline) <= wait {
			return Context{}, Binding{}, &Error{Category: CategoryTimeout, Stage: "retry_budget", Cause: context.DeadlineExceeded}
		}
		if waitErr := a.deps.Clock.Wait(bounded, wait); waitErr != nil {
			return Context{}, Binding{}, contextError(bounded, "retry_wait")
		}
	}
	return Context{}, Binding{}, last
}

// terminalRecord converts even malformed acquisition input into one valid content-free durable terminal event.
func terminalRecord(request Request, result Context, operationErr error, attempts int, duration time.Duration) Record {
	record := Record{Version: RecordVersion, Attempts: maxInt(attempts, 0), Duration: duration, Counts: result.Counts, ObservationDigest: result.ObservationDigest, ContextDigest: result.Digest, Completeness: result.Completeness, OmissionReasons: summarizeOmissions(result.Omissions), Outcome: "success"}
	if duration < 0 {
		record.Duration = 0
	}
	if safeID.MatchString(request.RunID) {
		record.RunID = request.RunID
	}
	if request.RepositoryID > 0 {
		record.RepositoryID = request.RepositoryID
	}
	if request.PullRequestNumber > 0 {
		record.PullRequestNumber = request.PullRequestNumber
	}
	if fullOID.MatchString(request.ExpectedHeadOID) {
		record.HeadOID = request.ExpectedHeadOID
	}
	if digestPattern.MatchString(request.PolicyDigest) {
		record.PolicyDigest = request.PolicyDigest
	}
	if fullOID.MatchString(result.Authority.BaseOID) {
		record.BaseOID = result.Authority.BaseOID
	}
	if operationErr != nil {
		record.Category = CategoryInternal
		var typed *Error
		if errors.As(operationErr, &typed) && validCategory(typed.Category) {
			record.Category = typed.Category
		}
		record.Outcome = string(record.Category)
	}
	record.AcquisitionDigest, _ = digestValue(request)
	record.ID = terminalRecordID(record)
	record.Digest = terminalRecordDigest(record)
	return record
}

// terminalRecordID derives the immutable exactly-once address from content-free stable terminal dimensions.
func terminalRecordID(record Record) string {
	digest, _ := digestValue(terminalIdentity{AcquisitionDigest: record.AcquisitionDigest, RunID: record.RunID, RepositoryID: record.RepositoryID, PullRequestNumber: record.PullRequestNumber, HeadOID: record.HeadOID, BaseOID: record.BaseOID, PolicyDigest: record.PolicyDigest, Outcome: record.Outcome, Category: record.Category, ObservationDigest: record.ObservationDigest, ContextDigest: record.ContextDigest, Completeness: record.Completeness, Counts: record.Counts, OmissionReasons: record.OmissionReasons})
	return digest
}

// terminalRecordDigest seals every durable record field while excluding only the digest slot itself.
func terminalRecordDigest(record Record) string {
	record.Digest = ""
	digest, _ := digestValue(record)
	return digest
}

// terminalObserverError preserves confirmed acquisition outputs while classifying the failed audit boundary.
func terminalObserverError(observerCtx context.Context, operationErr, observeErr error) error {
	category := CategoryPersistence
	if errors.Is(observeErr, context.DeadlineExceeded) || errors.Is(observerCtx.Err(), context.DeadlineExceeded) {
		category = CategoryTimeout
	} else if errors.Is(observeErr, context.Canceled) || errors.Is(observerCtx.Err(), context.Canceled) {
		category = CategoryCancelled
	} else {
		var typed *Error
		if errors.As(observeErr, &typed) && validCategory(typed.Category) {
			category = typed.Category
		}
	}
	return &Error{Category: category, Stage: "terminal_record", Cause: errors.Join(operationErr, observeErr)}
}

// acquireOnce performs metadata-files-metadata as an all-or-nothing observation.
func (a *Acquirer) acquireOnce(ctx context.Context, request Request, policy Policy) (Context, Binding, error) {
	first, err := a.deps.Gateway.Metadata(ctx, request.InstallationID, request.RepositoryID, request.PullRequestNumber, policy.Limits.MaxResponseBytes)
	if err != nil {
		return Context{}, Binding{}, mapDependencyError(ctx, "metadata", err)
	}
	if ctx.Err() != nil {
		return Context{}, Binding{}, contextError(ctx, "metadata")
	}
	if err = validateMetadata(first, request); err != nil {
		return Context{}, Binding{}, err
	}
	files := make([]RemoteFile, 0)
	completeness := CompletenessComplete
	pages := 0
	omissions := make([]Omission, 0)
	observedFiles := make([]RemoteFile, 0)
	omittedFiles := make([]RemoteFile, 0)
	for page := 1; page <= policy.Limits.MaxPages; page++ {
		pageResult, pageErr := a.deps.Gateway.Files(ctx, request.InstallationID, request.RepositoryID, request.PullRequestNumber, page, minInt(100, policy.Limits.MaxFiles), policy.Limits.MaxResponseBytes)
		if pageErr != nil {
			return Context{}, Binding{}, mapDependencyError(ctx, "files", pageErr)
		}
		if ctx.Err() != nil {
			return Context{}, Binding{}, contextError(ctx, "files")
		}
		pages++
		if len(pageResult.Files) > 100 {
			return Context{}, Binding{}, &Error{Category: CategoryIntegrity, Stage: "files_page"}
		}
		observedFiles = append(observedFiles, pageResult.Files...)
		remaining := policy.Limits.MaxFiles - len(files)
		if len(pageResult.Files) > remaining {
			files = append(files, pageResult.Files[:remaining]...)
			omittedFiles = append(omittedFiles, pageResult.Files[remaining:]...)
			completeness = CompletenessPartial
			if pageResult.Next {
				omissions = append(omissions, limitOmission("pages", OmissionPages, 1))
			}
			break
		}
		files = append(files, pageResult.Files...)
		if !pageResult.Next {
			break
		}
		if len(files) == policy.Limits.MaxFiles || page == policy.Limits.MaxPages {
			completeness = CompletenessPartial
			omissions = append(omissions, limitOmission("pages", OmissionPages, 1))
			break
		}
	}
	second, err := a.deps.Gateway.Metadata(ctx, request.InstallationID, request.RepositoryID, request.PullRequestNumber, policy.Limits.MaxResponseBytes)
	if err != nil {
		return Context{}, Binding{}, mapDependencyError(ctx, "metadata_confirm", err)
	}
	if ctx.Err() != nil {
		return Context{}, Binding{}, contextError(ctx, "metadata_confirm")
	}
	if first != second {
		return Context{}, Binding{}, &Error{Category: CategoryStale, Stage: "coherence"}
	}
	seen := map[string]bool{}
	for _, remote := range observedFiles {
		if !validRemoteFile(remote) || seen[remote.Path] {
			return Context{}, Binding{}, &Error{Category: CategoryIntegrity, Stage: "file"}
		}
		seen[remote.Path] = true
		patch := ""
		if remote.Patch != nil {
			patch = *remote.Patch
		}
		if err = a.scan(ctx, []byte(remote.Path), []byte(remote.PreviousPath), []byte(patch)); err != nil {
			return Context{}, Binding{}, err
		}
	}
	for _, remote := range omittedFiles {
		omissions = append(omissions, remoteOmission(remote, OmissionFiles))
	}
	return a.buildContext(ctx, request, policy, first, files, pages, completeness, omissions)
}

// buildContext turns one coherent remote observation into detached canonical data.
func (a *Acquirer) buildContext(ctx context.Context, request Request, policy Policy, metadata RemoteMetadata, remoteFiles []RemoteFile, pages int, completeness Completeness, omissions []Omission) (Context, Binding, error) {
	result := Context{Version: ContextVersion, RunID: request.RunID, PolicyID: policy.ID, PolicyRevision: policy.Revision, PolicyDigest: policy.Digest, CatalogDigest: policy.CatalogDigest, Authority: Authority{InstallationID: request.InstallationID, RepositoryID: request.RepositoryID, PullRequestNumber: request.PullRequestNumber, HeadOID: metadata.HeadOID, BaseOID: metadata.BaseOID}, Files: make([]File, 0), Constraints: make([]Constraint, 0), Contracts: make([]ContractReference, 0), Omissions: make([]Omission, 0), Completeness: completeness}
	if err := a.scan(ctx, []byte(metadata.Title), []byte(metadata.Body)); err != nil {
		return Context{}, Binding{}, err
	}
	if len([]byte(metadata.Title)) <= policy.Limits.MaxTitleBytes {
		result.Title = metadata.Title
	} else {
		omissions = append(omissions, omission("title", metadata.Title, OmissionTitle))
		result.Completeness = CompletenessPartial
	}
	constraints, extractErr := extractConstraints(metadata.Body, policy)
	if extractErr != nil {
		return Context{}, Binding{}, extractErr
	}
	if len([]byte(metadata.Body)) <= policy.Limits.MaxBodyBytes {
		result.Description = metadata.Body
	} else {
		omissions = append(omissions, omission("description", metadata.Body, OmissionBody))
		result.Completeness = CompletenessPartial
		if constraintSourcePresent(metadata.Body, policy) {
			return Context{}, Binding{}, &Error{Category: CategoryInsufficient, Stage: "constraint_body_limit"}
		}
	}
	sort.Slice(remoteFiles, func(i, j int) bool { return remoteFiles[i].Path < remoteFiles[j].Path })
	seen := map[string]bool{}
	contractCandidates := make([]File, 0, len(remoteFiles))
	patchBytes, hunkCount, lineCount := 0, 0, 0
	for _, remote := range remoteFiles {
		if !validRemoteFile(remote) || seen[remote.Path] {
			return Context{}, Binding{}, &Error{Category: CategoryIntegrity, Stage: "file"}
		}
		seen[remote.Path] = true
		if !statusAllowed(policy.AllowedStatuses, remote.Status) {
			omissions = append(omissions, omission("file", remote.Path, OmissionStatus))
			continue
		}
		if !pathAllowed(policy.PathPrefixes, remote.Path) {
			omissions = append(omissions, omission("file", remote.Path, OmissionPath))
			continue
		}
		if !extensionAllowed(policy.TextExtensions, remote.Path) {
			omissions = append(omissions, omission("file", remote.Path, OmissionType))
			continue
		}
		contractCandidates = append(contractCandidates, File{Path: remote.Path, PreviousPath: remote.PreviousPath, Status: remote.Status})
		if remote.Patch == nil {
			omissions = append(omissions, omission("file", remote.Path, OmissionPatch))
			continue
		}
		if len([]byte(*remote.Patch)) > policy.Limits.MaxPatchBytesPerFile || patchBytes+len([]byte(*remote.Patch)) > policy.Limits.MaxPatchBytesTotal {
			omissions = append(omissions, omission("file", remote.Path, OmissionPatchSize))
			result.Completeness = CompletenessPartial
			continue
		}
		hunks, parseErr := parsePatch(*remote.Patch)
		if parseErr != nil {
			return Context{}, Binding{}, parseErr
		}
		fileHunks, fileLines := len(hunks), countLines(hunks)
		if hunkCount+fileHunks > policy.Limits.MaxHunks {
			omissions = append(omissions, omission("file", remote.Path, OmissionHunks))
			result.Completeness = CompletenessPartial
			continue
		}
		if lineCount+fileLines > policy.Limits.MaxLines {
			omissions = append(omissions, omission("file", remote.Path, OmissionLines))
			result.Completeness = CompletenessPartial
			continue
		}
		if err := a.scan(ctx, []byte(remote.Path), []byte(remote.PreviousPath), []byte(*remote.Patch)); err != nil {
			return Context{}, Binding{}, err
		}
		if len(result.Files) >= maxInt(0, policy.Limits.MaxItems-1) {
			omissions = append(omissions, remoteOmission(remote, OmissionItems))
			result.Completeness = CompletenessPartial
			continue
		}
		file := File{ID: "file:" + digestBytes([]byte(remote.Path))[:16], Path: remote.Path, PreviousPath: remote.PreviousPath, Status: remote.Status, Additions: remote.Additions, Deletions: remote.Deletions, Changes: remote.Changes, Hunks: hunks}
		file.Digest, _ = digestValue(file)
		result.Files = append(result.Files, file)
		patchBytes += len([]byte(*remote.Patch))
		hunkCount += fileHunks
		lineCount += fileLines
	}
	if len(constraints) > policy.Limits.MaxConstraints {
		omissions = append(omissions, Omission{Kind: "constraints", SafeDigest: digestBytes([]byte("constraints")), Reason: OmissionConstraint, Count: len(constraints) - policy.Limits.MaxConstraints})
		constraints = constraints[:policy.Limits.MaxConstraints]
		result.Completeness = CompletenessPartial
	}
	if constraints == nil {
		constraints = make([]Constraint, 0)
	}
	for _, constraint := range constraints {
		if err := a.scan(ctx, []byte(constraint.ID), []byte(constraint.Description)); err != nil {
			return Context{}, Binding{}, err
		}
	}
	result.Constraints = constraints
	result.Contracts = selectContracts(contractCandidates, policy.ContractRoutes, metadata.HeadOID)
	result.Omissions = canonicalOmissions(omissions)
	if len(result.Omissions) > 0 {
		result.Completeness = CompletenessPartial
	}
	result.Counts = Counts{Pages: pages, FilesObserved: len(remoteFiles), FilesProjected: len(result.Files), Hunks: hunkCount, Lines: lineCount, Constraints: len(result.Constraints), Contracts: len(result.Contracts), Omissions: len(result.Omissions)}
	observation := struct {
		Metadata RemoteMetadata
		Files    []RemoteFile
		Policy   string
	}{metadata, remoteFiles, policy.Digest}
	observationDigest, _ := digestValue(struct {
		Observation any
		Omissions   []Omission
	}{observation, result.Omissions})
	result.ObservationDigest = observationDigest
	unsigned := cloneContext(result)
	unsigned.Digest = ""
	result.Digest, _ = digestValue(unsigned)
	binding := Binding{Version: BindingVersion, RunID: request.RunID, ContextDigest: result.Digest, ObservationDigest: result.ObservationDigest, PolicyDigest: policy.Digest, CatalogDigest: policy.CatalogDigest, Authority: result.Authority, ExtractorVersion: policy.Extractor.Kind + "/" + policy.Extractor.Version, ProducerID: "githubcontext.acquirer", ProducerVersion: "v1"}
	binding.Digest, _ = digestValue(binding)
	return cloneContext(result), binding, nil
}

// summarizeOmissions aggregates closed reasons for a content-free terminal record.
func summarizeOmissions(values []Omission) []OmissionSummary {
	counts := map[OmissionReason]int{}
	for _, value := range values {
		counts[value.Reason] += value.Count
	}
	result := make([]OmissionSummary, 0, len(counts))
	for reason, count := range counts {
		result = append(result, OmissionSummary{Reason: reason, Count: count})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Reason < result[j].Reason })
	return result
}

// Revalidate reacquires current authority and reports stale without trusting historical producers.
func (a *Acquirer) Revalidate(ctx context.Context, request Request, policy Policy, expected Binding) error {
	current, binding, err := a.Refresh(ctx, request, policy)
	if err != nil {
		return err
	}
	if VerifyBinding(expected) != nil || current.Digest != expected.ContextDigest || binding.ObservationDigest != expected.ObservationDigest || binding.PolicyDigest != expected.PolicyDigest || binding.Authority != expected.Authority {
		return &Error{Category: CategoryStale, Stage: "revalidate"}
	}
	return nil
}

// Refresh bypasses idempotent historical receipts and reads current provider authority for revalidation.
func (a *Acquirer) Refresh(ctx context.Context, request Request, policy Policy) (Context, Binding, error) {
	if ctx.Err() != nil {
		return Context{}, Binding{}, contextError(ctx, "refresh")
	}
	if err := ValidateRequest(request); err != nil {
		return Context{}, Binding{}, err
	}
	sealed, err := SealPolicy(policy)
	if err != nil || sealed.Digest != request.PolicyDigest {
		return Context{}, Binding{}, &Error{Category: CategoryUnauthorized, Stage: "policy"}
	}
	bounded, cancel := context.WithTimeout(ctx, sealed.Limits.MaxDuration)
	defer cancel()
	var last error
	for attempt := 1; attempt <= sealed.Limits.MaxAttempts; attempt++ {
		var value Context
		var binding Binding
		value, binding, last = a.acquireOnce(bounded, request, sealed)
		if last == nil {
			return value, binding, nil
		}
		if bounded.Err() != nil {
			return Context{}, Binding{}, contextError(bounded, "refresh")
		}
		if !IsCategory(last, CategoryTransient) && !IsCategory(last, CategoryRateLimited) || attempt == sealed.Limits.MaxAttempts {
			return Context{}, Binding{}, last
		}
		wait := retryDelay(last, attempt, a.deps.Clock.Now(), sealed.Limits.MaxRetryWait)
		if deadline, ok := bounded.Deadline(); ok && time.Until(deadline) <= wait {
			return Context{}, Binding{}, &Error{Category: CategoryTimeout, Stage: "refresh_retry_budget", Cause: context.DeadlineExceeded}
		}
		if err = a.deps.Clock.Wait(bounded, wait); err != nil {
			return Context{}, Binding{}, contextError(bounded, "refresh_retry_wait")
		}
	}
	return Context{}, Binding{}, last
}

// scan checks each bounded unit before it can enter a durable or model-facing object.
func (a *Acquirer) scan(ctx context.Context, values ...[]byte) error {
	for _, value := range values {
		if len(value) == 0 {
			continue
		}
		if err := a.deps.Secrets.Check(ctx, value); err != nil {
			if ctx.Err() != nil {
				return contextError(ctx, "secret_gate")
			}
			return &Error{Category: CategorySecret, Stage: "secret_gate", Cause: err}
		}
	}
	return nil
}

// validateMetadata binds provider output to the exact composition-owned request.
func validateMetadata(value RemoteMetadata, request Request) error {
	if value.InstallationID != request.InstallationID || value.RepositoryID != request.RepositoryID || value.Number != request.PullRequestNumber || value.State != "open" || value.HeadOID != request.ExpectedHeadOID || !fullOID.MatchString(value.HeadOID) || !fullOID.MatchString(value.BaseOID) || !utf8.ValidString(value.Title) || !utf8.ValidString(value.Body) {
		return &Error{Category: CategoryStale, Stage: "metadata_authority"}
	}
	return nil
}

// validRemoteFile rejects ambiguous paths, statuses and provider counts.
func validRemoteFile(value RemoteFile) bool {
	if !validPath(value.Path) || value.Additions < 0 || value.Deletions < 0 || value.Changes < 0 || value.Changes != value.Additions+value.Deletions {
		return false
	}
	if value.Status == StatusRenamed {
		return validPath(value.PreviousPath) && value.PreviousPath != value.Path
	}
	return value.PreviousPath == "" && (value.Status == StatusAdded || value.Status == StatusModified || value.Status == StatusRemoved)
}

// parsePatch accepts only complete unified-diff hunks and preserves lines as inert data.
func parsePatch(raw string) ([]Hunk, error) {
	if !utf8.ValidString(raw) {
		return nil, &Error{Category: CategoryIntegrity, Stage: "patch_utf8"}
	}
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	hunks := make([]Hunk, 0)
	oldCount, newCount, oldSeen, newSeen := 0, 0, 0, 0
	previousContent := false
	for index, line := range lines {
		if strings.HasPrefix(line, "@@") {
			if len(hunks) > 0 && (oldSeen != oldCount || newSeen != newCount || !validHunkSequence(hunks)) {
				return nil, &Error{Category: CategoryIntegrity, Stage: "patch_ranges"}
			}
			ranges, valid := parseHunkHeader(line)
			if !valid {
				return nil, &Error{Category: CategoryIntegrity, Stage: "patch_header"}
			}
			oldCount, newCount = ranges.oldCount, ranges.newCount
			oldSeen, newSeen = 0, 0
			previousContent = false
			hunks = append(hunks, Hunk{Header: line, OldStart: ranges.oldStart, OldEnd: ranges.oldEnd, NewStart: ranges.newStart, NewEnd: ranges.newEnd})
			continue
		}
		if line == "" && index == len(lines)-1 {
			continue
		}
		if len(hunks) == 0 || line == "" {
			return nil, &Error{Category: CategoryIntegrity, Stage: "patch_format"}
		}
		if line == `\ No newline at end of file` {
			if !previousContent {
				return nil, &Error{Category: CategoryIntegrity, Stage: "patch_format"}
			}
			previousContent = false
		} else {
			switch line[0] {
			case ' ':
				oldSeen++
				newSeen++
			case '-':
				oldSeen++
			case '+':
				newSeen++
			default:
				return nil, &Error{Category: CategoryIntegrity, Stage: "patch_format"}
			}
			previousContent = true
			if oldSeen > oldCount || newSeen > newCount {
				return nil, &Error{Category: CategoryIntegrity, Stage: "patch_ranges"}
			}
		}
		hunks[len(hunks)-1].Lines = append(hunks[len(hunks)-1].Lines, line)
	}
	if len(hunks) == 0 || oldSeen != oldCount || newSeen != newCount || !validHunkSequence(hunks) {
		return nil, &Error{Category: CategoryIntegrity, Stage: "patch_hunks"}
	}
	return hunks, nil
}

// parseHunkHeader preserves canonical half-open coordinates and counts without numeric overflow.
func parseHunkHeader(header string) (hunkRange, bool) {
	match := unifiedHunkHeader.FindStringSubmatch(header)
	if match == nil {
		return hunkRange{}, false
	}
	oldStart, oldStartErr := strconv.ParseUint(match[1], 10, 31)
	newStart, newStartErr := strconv.ParseUint(match[3], 10, 31)
	oldCount, oldCountOK := hunkRangeCount(match[2])
	newCount, newCountOK := hunkRangeCount(match[4])
	if oldStartErr != nil || newStartErr != nil || !oldCountOK || !newCountOK || oldCount > 0 && (oldStart == 0 || oldStart+uint64(oldCount)-1 > uint64(1<<31-1)) || newCount > 0 && (newStart == 0 || newStart+uint64(newCount)-1 > uint64(1<<31-1)) {
		return hunkRange{}, false
	}
	return hunkRange{oldStart: int(oldStart), oldEnd: int(oldStart) + oldCount, newStart: int(newStart), newEnd: int(newStart) + newCount, oldCount: oldCount, newCount: newCount}, true
}

// hunkRangeCount applies unified-diff's omitted-count default while rejecting numeric overflow.
func hunkRangeCount(raw string) (int, bool) {
	if raw == "" {
		return 1, true
	}
	value, err := strconv.ParseUint(raw, 10, 31)
	return int(value), err == nil
}

// validHunk proves a stored hunk still matches its declared old/new ranges after local tampering.
func validHunk(hunk Hunk) bool {
	ranges, valid := parseHunkHeader(hunk.Header)
	if !valid || len(hunk.Lines) == 0 || hunk.OldStart != ranges.oldStart || hunk.OldEnd != ranges.oldEnd || hunk.NewStart != ranges.newStart || hunk.NewEnd != ranges.newEnd {
		return false
	}
	oldSeen, newSeen := 0, 0
	previousContent := false
	for _, line := range hunk.Lines {
		if line == `\ No newline at end of file` {
			if !previousContent {
				return false
			}
			previousContent = false
			continue
		}
		if line == "" || !utf8.ValidString(line) {
			return false
		}
		switch line[0] {
		case ' ':
			oldSeen++
			newSeen++
		case '-':
			oldSeen++
		case '+':
			newSeen++
		default:
			return false
		}
		previousContent = true
	}
	return oldSeen == ranges.oldCount && newSeen == ranges.newCount
}

// validHunkSequence enforces monotonic non-overlapping old/new ranges, including zero-width change points.
func validHunkSequence(hunks []Hunk) bool {
	for index, hunk := range hunks {
		if !validHunk(hunk) {
			return false
		}
		if index == 0 {
			continue
		}
		previous := hunks[index-1]
		if hunk.OldStart < previous.OldEnd || hunk.NewStart < previous.NewEnd || previous.OldStart == previous.OldEnd && hunk.OldStart == previous.OldStart || previous.NewStart == previous.NewEnd && hunk.NewStart == previous.NewStart {
			return false
		}
	}
	return true
}

// extractConstraints executes only the extractor kind and version sealed into policy.
func extractConstraints(body string, policy Policy) ([]Constraint, error) {
	switch policy.Extractor.Kind {
	case "markdown_section":
		lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
		markers := make([]int, 0, 1)
		for i, line := range lines {
			if line == policy.Extractor.Marker {
				markers = append(markers, i)
			}
		}
		if len(markers) == 0 {
			return nil, nil
		}
		if len(markers) != 1 {
			return nil, &Error{Category: CategoryConflict, Stage: "constraint_section"}
		}
		start, end := markers[0]+1, len(lines)
		for index := start; index < len(lines); index++ {
			level := markdownHeadingLevel(lines[index])
			if level > 0 && level <= 2 {
				end = index
				break
			}
		}
		values := make([]Constraint, 0)
		seen := map[string]bool{}
		for _, line := range lines[start:end] {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if !strings.HasPrefix(line, "- ") {
				return nil, &Error{Category: CategoryInsufficient, Stage: "constraint_syntax"}
			}
			parts := strings.SplitN(strings.TrimPrefix(line, "- "), ":", 2)
			if len(parts) != 2 {
				return nil, &Error{Category: CategoryInsufficient, Stage: "constraint_syntax"}
			}
			id, description := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
			if !safeID.MatchString(id) || description == "" || seen[id] {
				return nil, &Error{Category: CategoryConflict, Stage: "constraint_identity"}
			}
			seen[id] = true
			values = append(values, Constraint{ID: id, Description: description, SafeReference: "github:body-section:" + id})
		}
		return values, nil
	case "catalog_reference":
		var selected *CatalogEntry
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, policy.Extractor.ReferencePrefix) {
				continue
			}
			if selected != nil {
				return nil, &Error{Category: CategoryConflict, Stage: "catalog_reference"}
			}
			value := strings.TrimSpace(strings.TrimPrefix(line, policy.Extractor.ReferencePrefix))
			for i := range policy.Catalog {
				if value == policy.Catalog[i].Alias+"@"+policy.Catalog[i].Version {
					entry := policy.Catalog[i]
					selected = &entry
				}
			}
			if selected == nil {
				return nil, &Error{Category: CategoryInsufficient, Stage: "catalog_reference"}
			}
		}
		if selected == nil {
			return nil, nil
		}
		return append([]Constraint(nil), selected.Constraints...), nil
	default:
		return nil, &Error{Category: CategoryInvalid, Stage: "extractor"}
	}
}

// markdownHeadingLevel recognizes only plain ATX headings without executing Markdown extensions.
func markdownHeadingLevel(line string) int {
	count := 0
	for count < len(line) && line[count] == '#' {
		count++
	}
	if count == 0 || count >= len(line) || line[count] != ' ' {
		return 0
	}
	return count
}

// constraintSourcePresent reports whether bounded body projection would exclude configured extractor source.
func constraintSourcePresent(body string, policy Policy) bool {
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if policy.Extractor.Kind == "markdown_section" && line == policy.Extractor.Marker || policy.Extractor.Kind == "catalog_reference" && strings.HasPrefix(strings.TrimSpace(line), policy.Extractor.ReferencePrefix) {
			return true
		}
	}
	return false
}

// selectContracts derives safe references exclusively from trusted exact paths.
func selectContracts(files []File, routes []ContractRoute, head string) []ContractReference {
	result := make([]ContractReference, 0)
	seen := map[string]bool{}
	for _, file := range files {
		for _, route := range routes {
			if route.Path != file.Path && route.Path != file.PreviousPath {
				continue
			}
			key := route.ContractID + "@" + route.Version + ":" + file.Path
			if seen[key] {
				continue
			}
			seen[key] = true
			result = append(result, ContractReference{ContractID: route.ContractID, Version: route.Version, Path: file.Path, PreviousPath: file.PreviousPath, HeadOID: head, Status: file.Status})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ContractID != result[j].ContractID {
			return result[i].ContractID < result[j].ContractID
		}
		return result[i].Path < result[j].Path
	})
	return result
}

// mapDependencyError preserves typed dependency categories and caller cancellation.
func mapDependencyError(ctx context.Context, stage string, err error) error {
	if ctx.Err() != nil {
		return contextError(ctx, stage)
	}
	var typed *Error
	if errors.As(err, &typed) {
		return typed
	}
	return &Error{Category: CategoryInternal, Stage: stage, Cause: err}
}

// retryDelay uses provider advice when bounded, otherwise deterministic linear backoff.
func retryDelay(err error, attempt int, now time.Time, maximum time.Duration) time.Duration {
	wait := time.Duration(attempt) * 100 * time.Millisecond
	var typed *Error
	if errors.As(err, &typed) && typed.RetryAt.After(now) {
		wait = typed.RetryAt.Sub(now)
	}
	if wait > maximum {
		wait = maximum
	}
	return wait
}

// omission hashes excluded identity instead of retaining its raw content.
func omission(kind, value string, reason OmissionReason) Omission {
	return Omission{Kind: kind, SafeDigest: digestBytes([]byte(value)), Reason: reason, Count: 1}
}

// remoteOmission binds an excluded file's complete observed DTO to a content-free digest.
func remoteOmission(value RemoteFile, reason OmissionReason) Omission {
	digest, _ := digestValue(value)
	return Omission{Kind: "file", SafeDigest: digest, Reason: reason, Count: 1}
}

// limitOmission represents provider data that could not be enumerated within a trusted local limit.
func limitOmission(kind string, reason OmissionReason, count int) Omission {
	return Omission{Kind: kind, SafeDigest: digestBytes([]byte(kind + ":" + string(reason))), Reason: reason, Count: count}
}

// canonicalOmissions combines equal safe reasons into stable content-free entries.
func canonicalOmissions(values []Omission) []Omission {
	sort.Slice(values, func(i, j int) bool {
		if values[i].Reason != values[j].Reason {
			return values[i].Reason < values[j].Reason
		}
		return values[i].SafeDigest < values[j].SafeDigest
	})
	return values
}

// countLines returns the exact semantic line count across complete hunks.
func countLines(values []Hunk) int {
	total := 0
	for _, h := range values {
		total += len(h.Lines)
	}
	return total
}

// statusAllowed checks the sealed closed status allowlist.
func statusAllowed(values []FileStatus, target FileStatus) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// pathAllowed checks canonical prefixes without interpreting glob syntax from remote data.
func pathAllowed(prefixes []string, target string) bool {
	for _, prefix := range prefixes {
		if prefix == "" || strings.HasPrefix(target, prefix) {
			return true
		}
	}
	return false
}

// extensionAllowed checks the trusted textual suffix allowlist.
func extensionAllowed(values []string, target string) bool {
	for _, value := range values {
		if strings.HasSuffix(strings.ToLower(target), strings.ToLower(value)) {
			return true
		}
	}
	return false
}

// minInt chooses a local page bound without relying on provider URLs.
func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

// maxInt chooses a non-negative item allowance without relying on untrusted cardinalities.
func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

// MemoryStore is a deterministic concurrent idempotency store used by composition tests and local operation.
type MemoryStore struct {
	mu      sync.Mutex
	entries map[string]memoryEntry
}

// memoryEntry keeps one confirmed fingerprint, context and binding under the caller key.
type memoryEntry struct {
	fingerprint string
	context     Context
	binding     Binding
}

// NewMemoryStore constructs an empty content-detached receipt store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{entries: map[string]memoryEntry{}} }

// Load returns a detached confirmed receipt without holding a lock across external work.
func (s *MemoryStore) Load(ctx context.Context, key string) (string, Context, Binding, bool, error) {
	if ctx.Err() != nil {
		return "", Context{}, Binding{}, false, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.entries[key]
	return v.fingerprint, cloneContext(v.context), v.binding, ok, nil
}

// Commit atomically converges identical redelivery and rejects conflicting reuse.
func (s *MemoryStore) Commit(ctx context.Context, key, fingerprint string, value Context, binding Binding) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.entries[key]; ok {
		if old.fingerprint != fingerprint || old.context.Digest != value.Digest {
			return &Error{Category: CategoryConflict, Stage: "idempotency"}
		}
		return nil
	}
	s.entries[key] = memoryEntry{fingerprint: fingerprint, context: cloneContext(value), binding: binding}
	return nil
}

// WallClock supplies production time and cancellable timer waits.
type WallClock struct{}

// Now returns the current wall-clock instant.
func (WallClock) Now() time.Time { return time.Now() }

// Wait blocks only until the timer or caller cancellation wins.
func (WallClock) Wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// JSONBytes exposes canonical JSON only after context integrity verification.
func JSONBytes(value Context) ([]byte, error) {
	if err := VerifyContext(value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
