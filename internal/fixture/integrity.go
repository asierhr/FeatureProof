package fixture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/asierhr/featureProof/internal/specification"
)

// canonicalBoundSet contains every non-secret executable input authenticated across deliveries.
type canonicalBoundSet struct {
	RunID        string
	TargetID     string
	RepositoryID string
	Revision     string
	Fixtures     []canonicalBoundFixture
	CreateOrder  []string
	CleanupOrder []string
}

// canonicalBoundFixture includes the declaration hidden from ordinary projections plus both sealed operations.
type canonicalBoundFixture struct {
	Declaration specification.Fixture
	Create      BoundOperation
	Cleanup     BoundOperation
}

// ComputeBoundDigest returns the canonical seal over all non-secret fixture semantics.
func ComputeBoundDigest(bound BoundSet) (string, error) {
	fixtures := make([]canonicalBoundFixture, len(bound.Fixtures))
	for i, item := range bound.Fixtures {
		fixtures[i] = canonicalBoundFixture{Declaration: item.Declaration, Create: item.Create, Cleanup: item.Cleanup}
	}
	material := canonicalBoundSet{RunID: bound.RunID, TargetID: bound.TargetID, RepositoryID: bound.RepositoryID, Revision: bound.Revision, Fixtures: fixtures, CreateOrder: bound.CreateOrder, CleanupOrder: bound.CleanupOrder}
	raw, err := json.Marshal(material)
	if err != nil {
		return "", &Error{Category: CategoryInvalid, Phase: "binding_digest", Cause: err}
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// SealBoundSet computes and installs the canonical digest for trusted composition and tests.
func SealBoundSet(bound BoundSet) (BoundSet, error) {
	digest, err := ComputeBoundDigest(bound)
	if err != nil {
		return BoundSet{}, err
	}
	bound.Digest = digest
	return cloneBoundSet(bound), nil
}

// ValidateBoundSet recomputes the seal and checks exact order and binding coordinates before any state or I/O use.
func ValidateBoundSet(bound BoundSet) error {
	digest, err := ComputeBoundDigest(bound)
	if err != nil || digest != bound.Digest || bound.RunID == "" || bound.TargetID == "" || bound.RepositoryID == "" || bound.Revision == "" || len(bound.Fixtures) != len(bound.CreateOrder) || len(bound.CreateOrder) != len(bound.CleanupOrder) {
		return &Error{Category: CategoryInvalid, Phase: "binding_drift", Cause: err}
	}
	seen := map[string]bool{}
	for i, id := range bound.CreateOrder {
		item, ok := boundFixtureByID(bound, id)
		if !ok || seen[id] || item.Create.BindingDigest == "" || item.Cleanup.BindingDigest == "" || bound.CleanupOrder[len(bound.CleanupOrder)-1-i] != id {
			return &Error{Category: CategoryInvalid, Phase: "binding_shape", FixtureID: id}
		}
		seen[id] = true
	}
	return nil
}

// ValidateState authenticates all durable keys, transitions, captures, and claims against the sealed authority.
func ValidateState(bound BoundSet, state FixtureState) error {
	if err := ValidateBoundSet(bound); err != nil {
		return err
	}
	digest, digestErr := ComputeStateDigest(state)
	if digestErr != nil || state.SnapshotDigest != "" && digest != state.SnapshotDigest || state.BoundDigest != bound.Digest || state.Intents == nil || state.Eligible == nil || state.Prepared == nil || state.Captures == nil || state.CleanupTerminal == nil || state.CleanupClaims == nil || state.Reservations == nil {
		return &Error{Category: CategoryInvalid, Phase: "state_drift"}
	}
	known := map[string]BoundFixture{}
	for _, item := range bound.Fixtures {
		known[item.Declaration.ID] = item
	}
	for _, keys := range []map[string]bool{state.Intents, state.Eligible, state.Prepared} {
		for id := range keys {
			if _, ok := known[id]; !ok {
				return &Error{Category: CategoryInvalid, Phase: "state_fixture", FixtureID: id}
			}
		}
	}
	for id, terminal := range state.CleanupTerminal {
		if _, ok := known[id]; !ok || !validTerminal(terminal) || !state.Intents[id] || state.SnapshotDigest != "" && !hasTerminalAttempt(state, id, terminal) {
			return &Error{Category: CategoryInvalid, Phase: "state_terminal", FixtureID: id}
		}
	}
	for id, claim := range state.CleanupClaims {
		if _, ok := known[id]; !ok || claim.Owner == "" || claim.Token == "" || claim.Attempt <= 0 || claim.ExpiresAt.IsZero() || !state.Intents[id] || state.CleanupTerminal[id] != "" {
			return &Error{Category: CategoryInvalid, Phase: "state_claim", FixtureID: id}
		}
	}
	for id := range state.Prepared {
		if state.Prepared[id] && (!state.Intents[id] || !state.Eligible[id]) {
			return &Error{Category: CategoryInvalid, Phase: "state_transition", FixtureID: id}
		}
	}
	for id, eligible := range state.Eligible {
		if eligible && !state.Intents[id] {
			return &Error{Category: CategoryInvalid, Phase: "state_transition", FixtureID: id}
		}
	}
	for key, capture := range state.Captures {
		item, ok := known[capture.FixtureID]
		if !ok || !state.Prepared[capture.FixtureID] || key != captureKey(capture.FixtureID, capture.Name) || capture.RunID != bound.RunID || capture.BindingDigest != item.Create.BindingDigest || !declaresCapture(item, capture) {
			return &Error{Category: CategoryInvalid, Phase: "capture_binding", FixtureID: capture.FixtureID}
		}
		raw, marshalErr := json.Marshal(capture.Value)
		sum := sha256.Sum256(raw)
		if marshalErr != nil || capture.Bytes != len(raw) || capture.Digest != hex.EncodeToString(sum[:]) || !matchesType(capture.Value, capture.ValueType) {
			return &Error{Category: CategoryInvalid, Phase: "capture_digest", FixtureID: capture.FixtureID, Cause: marshalErr}
		}
	}
	ordinals := map[string]int{}
	seenAttempts := map[string]bool{}
	for _, attempt := range state.Attempts {
		item, ok := known[attempt.FixtureID]
		key := attempt.FixtureID + "\x00" + attempt.Phase
		coordinate := key + fmt.Sprint("\x00", attempt.Attempt)
		validCategory := attempt.Category == "" && attempt.Phase == "create" || validTerminal(attempt.Category) || attempt.Phase == "create" && attempt.Category == CategoryCapture
		if !ok || attempt.Attempt <= 0 || attempt.Phase != "create" && attempt.Phase != "cleanup" || attempt.BindingDigest != bindingForPhase(item, attempt.Phase) || !validCategory || seenAttempts[coordinate] || attempt.Attempt < ordinals[key] {
			return &Error{Category: CategoryInvalid, Phase: "state_attempt", FixtureID: attempt.FixtureID}
		}
		seenAttempts[coordinate] = true
		ordinals[key] = attempt.Attempt
	}
	for key, reserved := range state.Reservations {
		parts := strings.Split(key, "\x00")
		ordinal := 0
		if len(parts) == 3 {
			ordinal, _ = strconv.Atoi(parts[2])
		}
		item, knownFixture := known[parts[0]]
		if !reserved || len(parts) != 3 || !knownFixture || parts[1] != "create" && parts[1] != "cleanup" || ordinal <= 0 || bindingForPhase(item, parts[1]) == "invalid" {
			return &Error{Category: CategoryInvalid, Phase: "state_budget"}
		}
		if seenAttempts[key] && !state.Reservations[key] {
			return &Error{Category: CategoryInvalid, Phase: "state_budget", FixtureID: parts[0]}
		}
	}
	for coordinate := range seenAttempts {
		if !state.Reservations[coordinate] && attemptRequiresReservation(state.Attempts, coordinate) {
			return &Error{Category: CategoryInvalid, Phase: "state_budget"}
		}
	}
	pendingIDs := map[string]bool{}
	for _, event := range state.PendingEvents {
		if event.ID == "" || pendingIDs[event.ID] || event.RunID != bound.RunID {
			return &Error{Category: CategoryInvalid, Phase: "state_event", FixtureID: event.FixtureID}
		}
		pendingIDs[event.ID] = true
		if event.FixtureID != "" {
			item, ok := known[event.FixtureID]
			if !ok || event.BindingDigest != bindingForPhase(item, event.Phase) {
				return &Error{Category: CategoryInvalid, Phase: "state_event", FixtureID: event.FixtureID}
			}
		}
		if event.Type == "capture_confirmed" {
			if event.Capture == nil {
				return &Error{Category: CategoryInvalid, Phase: "state_capture_event", FixtureID: event.FixtureID}
			}
			capture, ok := state.Captures[captureKey(event.FixtureID, event.Capture.Name)]
			if !ok || event.Capture.Name != capture.Name || event.Capture.ValueType != capture.ValueType || event.Capture.Bytes != capture.Bytes || event.Capture.Digest != capture.Digest {
				return &Error{Category: CategoryInvalid, Phase: "state_capture_event", FixtureID: event.FixtureID}
			}
		}
	}
	if state.ReportConfirmed && !allCleanupTerminal(state) {
		return &Error{Category: CategoryInvalid, Phase: "state_report"}
	}
	return nil
}

// attemptRequiresReservation distinguishes effect-capable attempts from terminal closures that consume no call budget.
func attemptRequiresReservation(attempts []AttemptState, coordinate string) bool {
	for _, attempt := range attempts {
		key := attempt.FixtureID + "\x00" + attempt.Phase + fmt.Sprint("\x00", attempt.Attempt)
		if key != coordinate {
			continue
		}
		return attempt.Category != CategoryBudget && attempt.Category != CategoryDeadline && attempt.Category != CategoryCancelled && attempt.Category != CategoryInputUnavailable
	}
	return false
}

// ComputeStateDigest authenticates every durable semantic field while excluding storage version and the digest itself.
func ComputeStateDigest(state FixtureState) (string, error) {
	state.Version = 0
	state.SnapshotDigest = ""
	state.AuthorityMAC = ""
	raw, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// SealState installs the complete durable snapshot digest before a CAS transition.
func SealState(state FixtureState) (FixtureState, error) {
	digest, err := ComputeStateDigest(state)
	if err != nil {
		return FixtureState{}, err
	}
	state.SnapshotDigest = digest
	return state, nil
}

// hasTerminalAttempt requires every durable terminal to be backed by a matching terminal attempt.
func hasTerminalAttempt(state FixtureState, fixtureID string, category Category) bool {
	for _, attempt := range state.Attempts {
		if attempt.FixtureID == fixtureID && attempt.Phase == "cleanup" && attempt.Terminal && attempt.Category == category {
			return true
		}
	}
	return false
}

// validTerminal recognizes every durable cleanup terminal and rejects open strings.
func validTerminal(category Category) bool {
	switch category {
	case CategorySuccess, CategoryInputUnavailable, CategoryBudget, CategoryDeadline, CategoryCancelled, CategoryDenied, CategoryUnavailable, CategoryUncertain, CategoryInternal, CategoryInvalid:
		return true
	}
	return false
}

// ValidateSummary rejects forged public fixture audits by enforcing closed states, coordinates, and aggregate cleanup semantics.
func ValidateSummary(summary FixtureSummary, runID string) error {
	if summary.RunID != runID || !safeRunID.MatchString(summary.RunID) || !validHexDigest(summary.FixtureSetDigest) {
		return &Error{Category: CategoryInvalid, Phase: "summary_binding"}
	}
	switch summary.PreparationStatus {
	case PreparationNotRequired, PreparationPrepared, PreparationFailed, PreparationCancelled, PreparationRejected:
	default:
		return &Error{Category: CategoryInvalid, Phase: "summary_preparation"}
	}
	switch summary.Cleanup.Status {
	case CleanupNotRequired, CleanupSucceeded, CleanupPartial, CleanupFailed, CleanupDeadlineExceeded:
	default:
		return &Error{Category: CategoryInvalid, Phase: "summary_cleanup"}
	}
	results := map[string]FixtureResult{}
	for _, result := range summary.Fixtures {
		if !safeRunID.MatchString(result.FixtureID) || results[result.FixtureID].FixtureID != "" {
			return &Error{Category: CategoryInvalid, Phase: "summary_fixture"}
		}
		switch result.CreateStatus {
		case CreatePrepared, CreateFailed, CreateUncertain:
			if result.Cause != "" {
				return &Error{Category: CategoryInvalid, Phase: "summary_fixture", FixtureID: result.FixtureID}
			}
		case CreateBlocked:
			if result.Cause != BlockedDependencyFailed && result.Cause != BlockedPreparationAborted {
				return &Error{Category: CategoryInvalid, Phase: "summary_fixture", FixtureID: result.FixtureID}
			}
		default:
			return &Error{Category: CategoryInvalid, Phase: "summary_fixture", FixtureID: result.FixtureID}
		}
		captureNames := map[string]bool{}
		for _, name := range result.ConfirmedCaptureNames {
			if name == "" || captureNames[name] {
				return &Error{Category: CategoryInvalid, Phase: "summary_fixture", FixtureID: result.FixtureID}
			}
			captureNames[name] = true
		}
		if result.CreateStatus != CreatePrepared && len(captureNames) != 0 {
			return &Error{Category: CategoryInvalid, Phase: "summary_fixture", FixtureID: result.FixtureID}
		}
		results[result.FixtureID] = result
	}
	if (summary.PreparationStatus == PreparationNotRequired || summary.PreparationStatus == PreparationRejected) && len(results) != 0 || summary.PreparationStatus == PreparationPrepared && len(results) == 0 {
		return &Error{Category: CategoryInvalid, Phase: "summary_preparation"}
	}
	preparedResults := 0
	for _, result := range results {
		if result.CreateStatus == CreatePrepared {
			preparedResults++
		}
	}
	if summary.PreparationStatus == PreparationPrepared && preparedResults != len(results) || (summary.PreparationStatus == PreparationFailed || summary.PreparationStatus == PreparationCancelled) && preparedResults == len(results) {
		return &Error{Category: CategoryInvalid, Phase: "summary_preparation"}
	}
	intents, terminals := map[string]bool{}, map[string]Category{}
	terminalAttempt := map[string]int{}
	createBinding, cleanupBinding := map[string]string{}, map[string]string{}
	createTerminal, createTerminalCategory, captureNames := map[string]bool{}, map[string]Category{}, map[string]map[string]bool{}
	nextOrdinal := map[string]int{}
	seen := map[string]bool{}
	for _, record := range summary.Cleanup.Audit {
		result, knownResult := results[record.FixtureID]
		if !safeRunID.MatchString(record.FixtureID) || !validHexDigest(record.BindingDigest) || !knownResult || result.CreateStatus == CreateBlocked || record.EvidenceID != "" && !validHexDigest(record.EvidenceID) {
			return &Error{Category: CategoryInvalid, Phase: "summary_audit"}
		}
		if record.Phase == "intent" {
			if record.Attempt != 0 || record.Category != "" || record.Terminal || record.Capture != nil || intents[record.FixtureID] {
				return &Error{Category: CategoryInvalid, Phase: "summary_audit"}
			}
			intents[record.FixtureID] = true
			createBinding[record.FixtureID] = record.BindingDigest
			continue
		}
		if record.Phase == "capture" {
			if record.Attempt != 0 || record.Category != "" || record.Terminal || record.Capture == nil || record.Capture.Name == "" || record.Capture.Bytes <= 0 || !validCaptureType(record.Capture.ValueType) || len(record.Capture.Digest) != 64 {
				return &Error{Category: CategoryInvalid, Phase: "summary_capture", FixtureID: record.FixtureID}
			}
			if _, err := hex.DecodeString(record.Capture.Digest); err != nil || createBinding[record.FixtureID] != record.BindingDigest {
				return &Error{Category: CategoryInvalid, Phase: "summary_capture", FixtureID: record.FixtureID}
			}
			if captureNames[record.FixtureID] == nil {
				captureNames[record.FixtureID] = map[string]bool{}
			}
			if captureNames[record.FixtureID][record.Capture.Name] {
				return &Error{Category: CategoryInvalid, Phase: "summary_capture", FixtureID: record.FixtureID}
			}
			captureNames[record.FixtureID][record.Capture.Name] = true
			continue
		}
		phaseCoordinate := record.FixtureID + "\x00" + record.Phase
		coordinate := phaseCoordinate + fmt.Sprint("\x00", record.Attempt)
		expectedOrdinal := nextOrdinal[phaseCoordinate] + 1
		if record.Phase != "create" && record.Phase != "cleanup" || record.Attempt != expectedOrdinal || seen[coordinate] || record.Capture != nil || record.Category != "" && !validTerminal(record.Category) || record.Phase == "create" && record.Category == CategorySuccess {
			return &Error{Category: CategoryInvalid, Phase: "summary_audit"}
		}
		seen[coordinate] = true
		nextOrdinal[phaseCoordinate] = record.Attempt
		bindings := createBinding
		if record.Phase == "cleanup" {
			bindings = cleanupBinding
		}
		if bindings[record.FixtureID] != "" && bindings[record.FixtureID] != record.BindingDigest {
			return &Error{Category: CategoryInvalid, Phase: "summary_binding", FixtureID: record.FixtureID}
		}
		bindings[record.FixtureID] = record.BindingDigest
		if record.Phase == "create" && record.Terminal {
			if createTerminal[record.FixtureID] {
				return &Error{Category: CategoryInvalid, Phase: "summary_create", FixtureID: record.FixtureID}
			}
			createTerminal[record.FixtureID] = true
			createTerminalCategory[record.FixtureID] = record.Category
		} else if record.Phase == "create" && createTerminal[record.FixtureID] {
			return &Error{Category: CategoryInvalid, Phase: "summary_create", FixtureID: record.FixtureID}
		}
		if record.Phase == "cleanup" && record.Terminal {
			if !intents[record.FixtureID] || record.Category == "" || terminals[record.FixtureID] != "" {
				return &Error{Category: CategoryInvalid, Phase: "summary_audit"}
			}
			terminals[record.FixtureID] = record.Category
			terminalAttempt[record.FixtureID] = record.Attempt
		}
	}
	for id, result := range results {
		invalidPrepared := result.CreateStatus == CreatePrepared && (!intents[id] || !createTerminal[id] || createTerminalCategory[id] != "")
		invalidFailed := result.CreateStatus == CreateFailed && (!createTerminal[id] || createTerminalCategory[id] == "" || createTerminalCategory[id] == CategorySuccess)
		invalidUncertain := result.CreateStatus == CreateUncertain && (!intents[id] || createTerminal[id] && createTerminalCategory[id] == "")
		if invalidPrepared || invalidFailed || invalidUncertain || result.CreateStatus == CreateBlocked && (intents[id] || createTerminal[id]) {
			return &Error{Category: CategoryInvalid, Phase: "summary_fixture", FixtureID: id}
		}
		projected := map[string]bool{}
		for _, name := range result.ConfirmedCaptureNames {
			projected[name] = true
		}
		if len(projected) != len(captureNames[id]) {
			return &Error{Category: CategoryInvalid, Phase: "summary_capture", FixtureID: id}
		}
		for name := range projected {
			if !captureNames[id][name] {
				return &Error{Category: CategoryInvalid, Phase: "summary_capture", FixtureID: id}
			}
		}
	}
	if len(terminals) != len(intents) {
		return &Error{Category: CategoryInvalid, Phase: "summary_status"}
	}
	successes := 0
	failures := map[string]Category{}
	for id := range intents {
		if terminals[id] == CategorySuccess {
			successes++
		} else if terminals[id] != "" {
			failures[id] = terminals[id]
		}
	}
	incidents := map[string]CleanupIncident{}
	for _, incident := range summary.Cleanup.Incidents {
		terminal, ok := failures[incident.FixtureID]
		key := incident.FixtureID + "\x00" + strconv.Itoa(incident.Attempt)
		if !ok || incident.Phase != "cleanup" || incident.Attempt != terminalAttempt[incident.FixtureID] || incident.Category != terminal || incident.BindingDigest == "" || incident.BindingDigest != cleanupBinding[incident.FixtureID] || incidents[key].FixtureID != "" {
			return &Error{Category: CategoryInvalid, Phase: "summary_incident", FixtureID: incident.FixtureID}
		}
		incidents[key] = incident
	}
	if len(incidents) != len(failures) {
		return &Error{Category: CategoryInvalid, Phase: "summary_incident"}
	}
	switch summary.Cleanup.Status {
	case CleanupNotRequired:
		if len(intents) != 0 || len(terminals) != 0 || len(summary.Cleanup.Incidents) != 0 {
			return &Error{Category: CategoryInvalid, Phase: "summary_status"}
		}
	case CleanupSucceeded:
		if len(intents) == 0 || successes != len(intents) {
			return &Error{Category: CategoryInvalid, Phase: "summary_status"}
		}
	case CleanupPartial:
		if successes == 0 || len(failures) == 0 {
			return &Error{Category: CategoryInvalid, Phase: "summary_status"}
		}
	case CleanupFailed:
		if len(intents) == 0 || successes != 0 || len(failures) != len(intents) {
			return &Error{Category: CategoryInvalid, Phase: "summary_status"}
		}
	case CleanupDeadlineExceeded:
		deadline := false
		for _, category := range failures {
			deadline = deadline || category == CategoryDeadline
		}
		if !deadline {
			return &Error{Category: CategoryInvalid, Phase: "summary_status"}
		}
	}
	return nil
}

// validHexDigest recognizes canonical SHA-256 identifiers without accepting arbitrary collaborator text.
func validHexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// validCaptureType recognizes the only capture types that may enter the public audit.
func validCaptureType(valueType string) bool {
	switch valueType {
	case "string", "number", "boolean", "object", "array":
		return true
	}
	return false
}

// declaresCapture verifies the durable capture remains one of the exact declarations and type/size bounds.
func declaresCapture(item BoundFixture, capture Capture) bool {
	for _, declaration := range item.Declaration.Captures {
		if declaration.Name == capture.Name && declaration.ValueType == capture.ValueType && capture.Bytes <= declaration.MaxBytes {
			return true
		}
	}
	return false
}

// bindingForPhase selects the sealed operation digest for a closed lifecycle phase.
func bindingForPhase(item BoundFixture, phase string) string {
	if phase == "create" {
		return item.Create.BindingDigest
	}
	if phase == "cleanup" {
		return item.Cleanup.BindingDigest
	}
	return fmt.Sprint("invalid")
}
