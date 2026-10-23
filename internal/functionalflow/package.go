package functionalflow

import (
	"encoding/json"

	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/planning"
)

// Event is one payload-free ordered journal record used for offline reconstruction.
type Event struct {
	Sequence              uint64         `json:"sequence"`
	Type                  string         `json:"type"`
	RunID                 string         `json:"run_id"`
	FlowID                string         `json:"flow_id"`
	Revision              uint64         `json:"revision"`
	StepID                string         `json:"step_id,omitempty"`
	DeclarationID         string         `json:"declaration_id,omitempty"`
	Outcome               PrimaryOutcome `json:"outcome,omitempty"`
	Category              ErrorCategory  `json:"category,omitempty"`
	Status                string         `json:"status,omitempty"`
	Attempt               int            `json:"attempt,omitempty"`
	OwnerDigest           string         `json:"owner_digest,omitempty"`
	BindingDigest         string         `json:"binding_digest,omitempty"`
	ResourceBindingDigest string         `json:"resource_binding_digest,omitempty"`
	ContentDigest         string         `json:"content_digest,omitempty"`
	RecordDigest          string         `json:"record_digest,omitempty"`
	KeyDigest             string         `json:"key_digest,omitempty"`
	EvidenceIDs           []string       `json:"evidence_ids,omitempty"`
	ArtifactDigest        string         `json:"artifact_digest"`
	PreviousDigest        string         `json:"previous_digest,omitempty"`
	Digest                string         `json:"digest"`
}

// EvidenceRecord is an authenticated payload-free manifest entry for one sanitized confirmed evidence object.
type EvidenceRecord struct {
	ID                    string `json:"id"`
	RunID                 string `json:"run_id"`
	Revision              uint64 `json:"revision"`
	StepID                string `json:"step_id"`
	BindingDigest         string `json:"binding_digest"`
	ResourceBindingDigest string `json:"resource_binding_digest"`
	ContentDigest         string `json:"content_digest"`
	Terminal              bool   `json:"terminal"`
	Truncated             bool   `json:"truncated"`
	RecordDigest          string `json:"record_digest"`
}

// Package is the complete safe reconstructible lifecycle projection with cleanup kept separate.
type Package struct {
	Version       string                 `json:"version"`
	RunID         string                 `json:"run_id"`
	Artifact      Artifact               `json:"artifact"`
	Evidence      []EvidenceRecord       `json:"evidence"`
	Events        []Event                `json:"events"`
	Primary       Result                 `json:"primary"`
	Fixtures      fixture.FixtureSummary `json:"fixtures"`
	Cleanup       fixture.CleanupReport  `json:"cleanup"`
	PackageDigest string                 `json:"package_digest"`
}

// BuildPackage creates a content-bound lifecycle document without resolved inputs, captures, or credentials.
func BuildPackage(runID string, artifact Artifact, evidence []EvidenceRecord, events []Event, primary Result, fixtures fixture.FixtureSummary, cleanup fixture.CleanupReport) (Package, error) {
	if runID == "" || primary.FlowID != artifact.FlowID || primary.Revision != artifact.Revision || primary.ArtifactDigest != artifact.ArtifactDigest {
		return Package{}, flowError(CategoryIntegrity, "package.binding", nil)
	}
	sealedEvents := make([]Event, len(events))
	copy(sealedEvents, events)
	unsigned := len(sealedEvents) > 0
	for _, event := range sealedEvents {
		unsigned = unsigned && event.Digest == "" && event.PreviousDigest == ""
	}
	for i := range sealedEvents {
		sealedEvents[i].EvidenceIDs = canonicalEvidenceIDs(sealedEvents[i].EvidenceIDs)
		if sealedEvents[i].Sequence != uint64(i+1) || sealedEvents[i].RunID != runID || sealedEvents[i].FlowID != artifact.FlowID || sealedEvents[i].Revision == 0 || sealedEvents[i].Revision > artifact.Revision || sealedEvents[i].ArtifactDigest != artifact.ArtifactDigest {
			return Package{}, flowError(CategoryIntegrity, "package.events["+itoa(i)+"]", nil)
		}
		previous := ""
		if i > 0 {
			previous = sealedEvents[i-1].Digest
		}
		if unsigned {
			sealedEvents[i].PreviousDigest = previous
			sealedEvents[i].Digest = eventDigest(sealedEvents[i])
		}
		if sealedEvents[i].PreviousDigest != previous || sealedEvents[i].Digest != eventDigest(sealedEvents[i]) {
			return Package{}, flowError(CategoryIntegrity, "package.events["+itoa(i)+"].digest", nil)
		}
	}
	sealedEvidence := append([]EvidenceRecord(nil), evidence...)
	pkg := Package{Version: "FunctionalFlowPackage/v1", RunID: runID, Artifact: cloneArtifact(artifact), Evidence: sealedEvidence, Events: sealedEvents, Primary: cloneResult(primary), Fixtures: fixtures, Cleanup: cleanup}
	pkg.PackageDigest = packageDigest(pkg)
	if err := VerifyPackage(pkg); err != nil {
		return Package{}, err
	}
	return pkg, nil
}

// BuildLifecycleEvents derives the complete canonical event machine from one immutable primary result.
func BuildLifecycleEvents(runID string, artifact Artifact, primary Result) []Event {
	events := []Event{{Type: "flow_artifact_committed"}, {Type: "fixtures_prepared"}}
	ledgerByStep := map[string][]string{}
	allEvidence := []string{}
	for _, entry := range primary.Ledger {
		ledgerByStep[entry.StepID] = append(ledgerByStep[entry.StepID], entry.EvidenceIDs...)
		allEvidence = append(allEvidence, entry.EvidenceIDs...)
	}
	for _, step := range primary.Steps {
		events = append(events, Event{Type: "step_terminal", StepID: step.StepID})
		if ids := canonicalEvidenceIDs(ledgerByStep[step.StepID]); len(ids) > 0 {
			events = append(events, Event{Type: "assertions_confirmed", StepID: step.StepID, EvidenceIDs: ids})
		}
	}
	events = append(events, Event{Type: "primary_terminal", Outcome: primary.Outcome, EvidenceIDs: canonicalEvidenceIDs(allEvidence)}, Event{Type: "cleanup_terminal"})
	for i := range events {
		events[i].Sequence = uint64(i + 1)
		events[i].RunID = runID
		events[i].FlowID = artifact.FlowID
		events[i].Revision = artifact.Revision
		events[i].ArtifactDigest = artifact.ArtifactDigest
		events[i].EvidenceIDs = canonicalEvidenceIDs(events[i].EvidenceIDs)
		if i > 0 {
			events[i].PreviousDigest = events[i-1].Digest
		}
		events[i].Digest = eventDigest(events[i])
	}
	return events
}

// SealEvidenceRecord converts one confirmed sanitized evidence object into an immutable package manifest record.
func SealEvidenceRecord(evidence Evidence) (EvidenceRecord, error) {
	if !validEvidence(evidence, evidence.RunID, evidence.Revision, evidence.StepID) || !validDigest(evidence.BindingDigest) {
		return EvidenceRecord{}, flowError(CategoryIntegrity, "package.evidence", nil)
	}
	record := EvidenceRecord{ID: evidence.ID, RunID: evidence.RunID, Revision: evidence.Revision, StepID: evidence.StepID, BindingDigest: evidence.BindingDigest, ResourceBindingDigest: evidence.ResourceBindingDigest, ContentDigest: evidence.Digest, Terminal: evidence.Terminal, Truncated: evidence.Truncated}
	record.RecordDigest = evidenceRecordDigest(record)
	return record, nil
}

// VerifyPackage revalidates all digests, ordering, ownership, and passed-evidence links before projection.
func VerifyPackage(pkg Package) error {
	if pkg.Version != "FunctionalFlowPackage/v1" || pkg.RunID == "" || packageDigest(pkg) != pkg.PackageDigest {
		return flowError(CategoryIntegrity, "package.digest", nil)
	}
	if err := ValidateArtifact(pkg.Artifact); err != nil {
		return err
	}
	evidence := map[string]EvidenceRecord{}
	stepBindings := map[string]BoundStep{}
	for _, step := range pkg.Artifact.Steps {
		stepBindings[step.Step.ID] = step
	}
	for i, record := range pkg.Evidence {
		bound, ok := stepBindings[record.StepID]
		if !ok || record.ID == "" || evidence[record.ID].ID != "" || record.RunID != pkg.RunID || record.Revision != pkg.Artifact.Revision || record.BindingDigest != bound.Operation.BindingDigest || record.ResourceBindingDigest == "" || !record.Terminal || !validDigest(record.ContentDigest) || record.RecordDigest != evidenceRecordDigest(record) {
			return flowError(CategoryIntegrity, "package.evidence["+itoa(i)+"]", nil)
		}
		evidence[record.ID] = record
	}
	for i, event := range pkg.Events {
		previous := ""
		if i > 0 {
			previous = pkg.Events[i-1].Digest
		}
		if event.Sequence != uint64(i+1) || event.RunID != pkg.RunID || event.FlowID != pkg.Artifact.FlowID || event.Revision != pkg.Artifact.Revision || event.ArtifactDigest != pkg.Artifact.ArtifactDigest || event.PreviousDigest != previous || event.Digest != eventDigest(event) {
			return flowError(CategoryIntegrity, "package.events["+itoa(i)+"]", nil)
		}
		switch event.Type {
		case "flow_artifact_committed", "fixtures_prepared", "fixtures_terminal", "step_claimed", "intent_confirmed", "attempt_started", "retry_scheduled", "evidence_confirmed", "assertions_confirmed", "comparisons_confirmed", "recovery_resumed", "step_terminal":
		case "primary_terminal", "cleanup_terminal":
		default:
			return flowError(CategoryIntegrity, "package.events["+itoa(i)+"].type", nil)
		}
		if event.Type == "fixtures_terminal" && !validPackagePreparationStatus(fixture.PreparationStatus(event.Status)) {
			return flowError(CategoryIntegrity, "package.events["+itoa(i)+"].status", nil)
		}
	}
	if len(pkg.Events) < 4 || pkg.Events[0].Type != "flow_artifact_committed" || pkg.Events[1].Type != "fixtures_prepared" && pkg.Events[1].Type != "fixtures_terminal" || pkg.Events[len(pkg.Events)-2].Type != "primary_terminal" || pkg.Events[len(pkg.Events)-1].Type != "cleanup_terminal" || pkg.Events[len(pkg.Events)-2].Outcome != pkg.Primary.Outcome {
		return flowError(CategoryIntegrity, "package.lifecycle", nil)
	}
	if pkg.Events[1].Type == "fixtures_terminal" && pkg.Events[1].Status != string(pkg.Fixtures.PreparationStatus) {
		return flowError(CategoryIntegrity, "package.lifecycle.preparation", nil)
	}
	if pkg.Primary.FlowID != pkg.Artifact.FlowID || pkg.Primary.Revision != pkg.Artifact.Revision || pkg.Primary.ArtifactDigest != pkg.Artifact.ArtifactDigest {
		return flowError(CategoryIntegrity, "package.primary", nil)
	}
	preparationAborted := pkg.Events[1].Type == "fixtures_terminal" && (pkg.Events[1].Status == string(fixture.PreparationFailed) || pkg.Events[1].Status == string(fixture.PreparationCancelled) || pkg.Events[1].Status == string(fixture.PreparationRejected))
	if preparationAborted {
		if len(pkg.Evidence) != 0 || len(pkg.Primary.Steps) != 0 || len(pkg.Primary.Ledger) != 0 || len(pkg.Primary.Comparisons) != 0 || len(pkg.Primary.Constraints) != 0 || pkg.Primary.Outcome == OutcomePassed || pkg.Primary.Outcome == OutcomeFailed {
			return flowError(CategoryIntegrity, "package.primary.aborted", nil)
		}
		return verifyPackageEventMachine(pkg, nil)
	}
	if len(pkg.Primary.Steps) != len(pkg.Artifact.Steps) {
		return flowError(CategoryIntegrity, "package.steps", nil)
	}
	ledger := map[string]LedgerEntry{}
	for i, entry := range pkg.Primary.Ledger {
		key := entry.StepID + "\x00" + entry.Result.DeclarationID
		if _, duplicate := ledger[key]; duplicate {
			return flowError(CategoryIntegrity, "package.ledger["+itoa(i)+"]", nil)
		}
		bound, stepOK := stepBindings[entry.StepID]
		if !stepOK || entry.Revision != pkg.Artifact.Revision || entry.ConstraintID != bound.Step.ConstraintID || !validDigest(bound.DeclarationDigests[entry.Result.DeclarationID]) || len(entry.EvidenceIDs) == 0 && entry.Result.Status != planning.AssertionInconclusive {
			return flowError(CategoryIntegrity, "package.ledger["+itoa(i)+"]", nil)
		}
		ledger[key] = entry
		for _, id := range entry.EvidenceIDs {
			record, ok := evidence[id]
			if !ok || !evidenceOwnedByDeclaration(record, bound.Step, entry.Result.DeclarationID) {
				return flowError(CategoryIntegrity, "package.ledger["+itoa(i)+"]", nil)
			}
		}
	}
	for i, step := range pkg.Artifact.Steps {
		record := pkg.Primary.Steps[i]
		if record.RunID != pkg.RunID || record.Revision != pkg.Artifact.Revision || record.StepID != step.Step.ID {
			return flowError(CategoryIntegrity, "package.steps["+itoa(i)+"]", nil)
		}
		for _, assertion := range record.Assertions {
			entry, ok := ledger[record.StepID+"\x00"+assertion.DeclarationID]
			if !ok || digestValue(entry.Result) != digestValue(assertion) {
				return flowError(CategoryIntegrity, "package.steps["+itoa(i)+"].assertions", nil)
			}
		}
	}
	expectedComparisons := map[string]ObservationComparison{}
	for _, step := range pkg.Artifact.Steps {
		for _, comparison := range step.Step.Comparisons {
			expectedComparisons[declarationCoordinate(step.Step.ID, comparison.ID)] = comparison
		}
	}
	if len(pkg.Primary.Comparisons) != len(expectedComparisons) {
		return flowError(CategoryIntegrity, "package.comparisons", nil)
	}
	seenComparisons := map[string]bool{}
	for i, comparison := range pkg.Primary.Comparisons {
		coordinate := declarationCoordinate(comparison.StepID, comparison.ID)
		declaration, declared := expectedComparisons[coordinate]
		entry, ledgerOK := ledger[comparison.StepID+"\x00"+comparison.ID]
		expectedEvidence := []string{}
		if comparison.BeforeEvidenceID != "" || comparison.AfterEvidenceID != "" {
			expectedEvidence = canonicalEvidenceIDs([]string{comparison.BeforeEvidenceID, comparison.AfterEvidenceID})
		}
		if !declared || seenComparisons[coordinate] || !ledgerOK || entry.Result.Status != comparison.Status || digestValue(canonicalEvidenceIDs(entry.EvidenceIDs)) != digestValue(expectedEvidence) {
			return flowError(CategoryIntegrity, "package.comparisons["+itoa(i)+"]", nil)
		}
		if comparison.Status == planning.AssertionInconclusive && comparison.BeforeEvidenceID == "" && comparison.AfterEvidenceID == "" {
			seenComparisons[coordinate] = true
			continue
		}
		before, beforeOK := evidence[comparison.BeforeEvidenceID]
		after, afterOK := evidence[comparison.AfterEvidenceID]
		if !beforeOK || !afterOK || before.StepID != declaration.BeforeStep || after.StepID != declaration.AfterStep {
			return flowError(CategoryIntegrity, "package.comparisons["+itoa(i)+"]", nil)
		}
		seenComparisons[coordinate] = true
	}
	if err := verifyPackageEventMachine(pkg, ledger); err != nil {
		return err
	}
	flow := Flow{Version: VersionV1, ID: pkg.Artifact.FlowID, Revision: pkg.Artifact.Revision, RequiredEvidence: append([]string(nil), pkg.Artifact.RequiredEvidence...), Steps: make([]Step, len(pkg.Artifact.Steps))}
	for i, step := range pkg.Artifact.Steps {
		flow.Steps[i] = cloneStep(step.Step)
	}
	proof := Ledger{}
	for _, entry := range pkg.Primary.Ledger {
		var err error
		proof, err = proof.Append(entry)
		if err != nil {
			return err
		}
	}
	constraints, outcome := proof.Aggregate(flow, pkg.Primary.Steps, pkg.Primary.Outcome == OutcomeCancelled)
	if outcome != pkg.Primary.Outcome || digestValue(constraints) != digestValue(pkg.Primary.Constraints) {
		return flowError(CategoryIntegrity, "package.verdict", nil)
	}
	return nil
}

// validPackagePreparationStatus closes the package lifecycle union to system-owned fixture outcomes.
func validPackagePreparationStatus(status fixture.PreparationStatus) bool {
	switch status {
	case fixture.PreparationPrepared, fixture.PreparationFailed, fixture.PreparationCancelled, fixture.PreparationNotRequired, fixture.PreparationRejected:
		return true
	default:
		return false
	}
}

// evidenceRecordDigest authenticates the safe manifest coordinates and the external sanitized-content digest.
func evidenceRecordDigest(record EvidenceRecord) string {
	record.RecordDigest = ""
	return digestValue(record)
}

// evidenceOwnedByDeclaration restricts local assertions to their step and comparisons to their exact observations.
func evidenceOwnedByDeclaration(record EvidenceRecord, step Step, declarationID string) bool {
	for _, assertion := range step.Assertions {
		if assertion.ID == declarationID {
			return record.StepID == step.ID
		}
	}
	for _, comparison := range step.Comparisons {
		if comparison.ID == declarationID {
			return record.StepID == comparison.BeforeStep || record.StepID == comparison.AfterStep
		}
	}
	return false
}

// verifyPackageEventMachine enforces artifact, prepare, ordered step/assertion, primary, and cleanup causality.
func verifyPackageEventMachine(pkg Package, ledger map[string]LedgerEntry) error {
	preparation := pkg.Events[1]
	if preparation.Type == "fixtures_terminal" && (preparation.Status == string(fixture.PreparationFailed) || preparation.Status == string(fixture.PreparationCancelled) || preparation.Status == string(fixture.PreparationRejected)) {
		if len(pkg.Events) != 4 || len(pkg.Primary.Ledger) != 0 {
			return flowError(CategoryIntegrity, "package.lifecycle.preparation", nil)
		}
		return verifyPrimaryEventEvidence(pkg)
	}
	rich := false
	for _, event := range pkg.Events {
		if event.Type == "step_claimed" || event.Type == "attempt_started" || event.Type == "evidence_confirmed" {
			rich = true
		}
	}
	if rich {
		return verifyRichPackageEventMachine(pkg, ledger)
	}
	position := 2
	for _, step := range pkg.Artifact.Steps {
		if position >= len(pkg.Events)-2 || pkg.Events[position].Type != "step_terminal" || pkg.Events[position].StepID != step.Step.ID {
			return flowError(CategoryIntegrity, "package.lifecycle", nil)
		}
		position++
		evidenceIDs := []string{}
		for _, required := range step.Step.RequiredEvidence {
			entry := ledger[step.Step.ID+"\x00"+required]
			evidenceIDs = append(evidenceIDs, entry.EvidenceIDs...)
		}
		if len(evidenceIDs) > 0 {
			if position >= len(pkg.Events)-2 || pkg.Events[position].Type != "assertions_confirmed" || pkg.Events[position].StepID != step.Step.ID || digestValue(canonicalEvidenceIDs(pkg.Events[position].EvidenceIDs)) != digestValue(canonicalEvidenceIDs(evidenceIDs)) {
				return flowError(CategoryIntegrity, "package.lifecycle", nil)
			}
			position++
		}
	}
	if position != len(pkg.Events)-2 {
		return flowError(CategoryIntegrity, "package.lifecycle", nil)
	}
	allEvidence := []string{}
	for _, entry := range pkg.Primary.Ledger {
		allEvidence = append(allEvidence, entry.EvidenceIDs...)
	}
	if digestValue(canonicalEvidenceIDs(pkg.Events[len(pkg.Events)-2].EvidenceIDs)) != digestValue(canonicalEvidenceIDs(allEvidence)) {
		return flowError(CategoryIntegrity, "package.primary_terminal", nil)
	}
	return nil
}

// verifyRichPackageEventMachine validates the real append-only execution journal rather than deriving history from Result.
func verifyRichPackageEventMachine(pkg Package, ledger map[string]LedgerEntry) error {
	type causalStep struct {
		claimed, intent, attempted, evidenced, asserted, terminal bool
		lastAttempt                                               int
	}
	states := map[string]causalStep{}
	terminalOrder := []string{}
	for _, event := range pkg.Events[2 : len(pkg.Events)-2] {
		bound, exists := artifactStep(pkg.Artifact, event.StepID)
		if !exists {
			return flowError(CategoryIntegrity, "package.lifecycle.step", nil)
		}
		state := states[event.StepID]
		switch event.Type {
		case "step_claimed":
			if state.claimed || state.terminal || !validDigest(event.OwnerDigest) {
				return flowError(CategoryIntegrity, "package.lifecycle.claim", nil)
			}
			state.claimed = true
		case "intent_confirmed":
			if !state.claimed || bound.Step.Kind != StepAct || state.attempted || state.terminal || event.BindingDigest != bound.Operation.BindingDigest || event.KeyDigest != "" && !validDigest(event.KeyDigest) {
				return flowError(CategoryIntegrity, "package.lifecycle.intent", nil)
			}
			state.intent = true
		case "attempt_started":
			if !state.claimed || bound.Step.Kind == StepAct && !state.intent || event.Attempt != state.lastAttempt+1 || state.terminal || event.BindingDigest != bound.Operation.BindingDigest {
				return flowError(CategoryIntegrity, "package.lifecycle.attempt", nil)
			}
			state.attempted, state.lastAttempt = true, event.Attempt
		case "retry_scheduled":
			if !state.attempted || event.Attempt != state.lastAttempt || state.evidenced || state.terminal {
				return flowError(CategoryIntegrity, "package.lifecycle.retry", nil)
			}
		case "evidence_confirmed":
			if !state.attempted || state.evidenced || state.terminal || !eventEvidenceOwned(pkg, event, event.StepID) {
				return flowError(CategoryIntegrity, "package.lifecycle.evidence", nil)
			}
			state.evidenced = true
		case "assertions_confirmed":
			if !state.evidenced || state.asserted || state.terminal || !eventAssertionEvidenceMatchesLedger(event, bound.Step, ledger) {
				return flowError(CategoryIntegrity, "package.lifecycle.assertions", nil)
			}
			state.asserted = true
		case "comparisons_confirmed":
			if !state.terminal || !eventComparisonEvidenceMatchesLedger(event, bound.Step, ledger) {
				return flowError(CategoryIntegrity, "package.lifecycle.comparisons", nil)
			}
		case "recovery_resumed":
			if !state.claimed || state.terminal {
				return flowError(CategoryIntegrity, "package.lifecycle.recovery", nil)
			}
		case "step_terminal":
			if state.terminal || (!state.claimed && event.Status != string(planning.StepBlocked) && event.Category != CategoryLimit && event.Category != CategoryCancelled) {
				return flowError(CategoryIntegrity, "package.lifecycle.terminal", nil)
			}
			state.terminal = true
			terminalOrder = append(terminalOrder, event.StepID)
		default:
			return flowError(CategoryIntegrity, "package.lifecycle.event", nil)
		}
		states[event.StepID] = state
	}
	expected := topologicalArtifactSteps(pkg.Artifact)
	if digestValue(terminalOrder) != digestValue(expected) {
		return flowError(CategoryIntegrity, "package.lifecycle.order", nil)
	}
	for _, id := range expected {
		if !states[id].terminal {
			return flowError(CategoryIntegrity, "package.lifecycle.terminal", nil)
		}
	}
	return verifyPrimaryEventEvidence(pkg)
}

// artifactStep resolves one sealed step without accepting a caller-provided operation projection.
func artifactStep(artifact Artifact, stepID string) (BoundStep, bool) {
	for _, step := range artifact.Steps {
		if step.Step.ID == stepID {
			return step, true
		}
	}
	return BoundStep{}, false
}

// eventEvidenceOwned checks that every causal evidence reference exists and belongs to the named step.
func eventEvidenceOwned(pkg Package, event Event, stepID string) bool {
	if len(event.EvidenceIDs) != 1 {
		return false
	}
	for _, id := range event.EvidenceIDs {
		found := false
		for _, record := range pkg.Evidence {
			found = found || record.ID == id && record.StepID == stepID && event.BindingDigest == record.BindingDigest && event.ResourceBindingDigest == record.ResourceBindingDigest && event.ContentDigest == record.ContentDigest && event.RecordDigest == record.RecordDigest
		}
		if !found {
			return false
		}
	}
	return true
}

// eventAssertionEvidenceMatchesLedger binds an assertion checkpoint to local immutable declarations for one step.
func eventAssertionEvidenceMatchesLedger(event Event, step Step, ledger map[string]LedgerEntry) bool {
	expected := []string{}
	for _, assertion := range step.Assertions {
		if entry, ok := ledger[step.ID+"\x00"+assertion.ID]; ok {
			expected = append(expected, entry.EvidenceIDs...)
		}
	}
	return digestValue(canonicalEvidenceIDs(event.EvidenceIDs)) == digestValue(canonicalEvidenceIDs(expected))
}

// eventComparisonEvidenceMatchesLedger binds a comparison checkpoint to one declared relation and its two observations.
func eventComparisonEvidenceMatchesLedger(event Event, step Step, ledger map[string]LedgerEntry) bool {
	for _, comparison := range step.Comparisons {
		if comparison.ID != event.DeclarationID {
			continue
		}
		entry, ok := ledger[step.ID+"\x00"+comparison.ID]
		if ok && digestValue(canonicalEvidenceIDs(entry.EvidenceIDs)) == digestValue(canonicalEvidenceIDs(event.EvidenceIDs)) {
			return true
		}
	}
	return false
}

// topologicalArtifactSteps reproduces deterministic declaration-tied scheduling from sealed dependencies.
func topologicalArtifactSteps(artifact Artifact) []string {
	remaining := map[string]bool{}
	done := map[string]bool{}
	for _, step := range artifact.Steps {
		remaining[step.Step.ID] = true
	}
	out := []string{}
	for len(out) < len(artifact.Steps) {
		progress := false
		for _, step := range artifact.Steps {
			if !remaining[step.Step.ID] {
				continue
			}
			ready := true
			for _, dependency := range step.Step.DependsOn {
				ready = ready && done[dependency]
			}
			if ready {
				out, done[step.Step.ID], remaining[step.Step.ID], progress = append(out, step.Step.ID), true, false, true
			}
		}
		if !progress {
			return nil
		}
	}
	return out
}

// verifyPrimaryEventEvidence binds the final journal terminal to every ledger evidence coordinate.
func verifyPrimaryEventEvidence(pkg Package) error {
	all := []string{}
	for _, entry := range pkg.Primary.Ledger {
		all = append(all, entry.EvidenceIDs...)
	}
	if digestValue(canonicalEvidenceIDs(pkg.Events[len(pkg.Events)-2].EvidenceIDs)) != digestValue(canonicalEvidenceIDs(all)) {
		return flowError(CategoryIntegrity, "package.primary_terminal", nil)
	}
	return nil
}

// SafeProjection returns the same allowlisted primary and cleanup view for every external adapter.
func SafeProjection(pkg Package) (Result, fixture.CleanupReport, error) {
	if err := VerifyPackage(pkg); err != nil {
		return Result{}, fixture.CleanupReport{}, err
	}
	return cloneResult(pkg.Primary), cloneCleanup(pkg.Cleanup), nil
}

// eventDigest fingerprints one journal record while excluding its self-digest.
func eventDigest(event Event) string { event.Digest = ""; return digestValue(event) }

// packageDigest fingerprints the complete package while excluding its self-digest.
func packageDigest(pkg Package) string { pkg.PackageDigest = ""; return digestValue(pkg) }

// cloneResult detaches every nested result slice before publication.
func cloneResult(result Result) Result {
	raw, _ := json.Marshal(result)
	var out Result
	_ = json.Unmarshal(raw, &out)
	return out
}

// cloneCleanup detaches nested cleanup incidents and audit records.
func cloneCleanup(report fixture.CleanupReport) fixture.CleanupReport {
	raw, _ := json.Marshal(report)
	var out fixture.CleanupReport
	_ = json.Unmarshal(raw, &out)
	return out
}

// Reconstruct verifies the package then returns its deterministic declaration-ordered primary result.
func Reconstruct(pkg Package) (Result, error) {
	if err := VerifyPackage(pkg); err != nil {
		return Result{}, err
	}
	return cloneResult(pkg.Primary), nil
}
