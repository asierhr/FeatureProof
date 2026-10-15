package agenteval

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/openapi"
	"github.com/asierhr/featureProof/internal/planning"
)

var stableID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var semanticVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
var sha256Text = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Loader verifies and composes local evaluation inputs before any model source is consulted.
type Loader struct {
	contracts     ContractRegistry
	afterSnapshot func()
}

// NewLoader requires an explicit allowlisted descriptor registry.
func NewLoader(contracts ContractRegistry) (*Loader, error) {
	if contracts == nil {
		return nil, typedError(InvalidInput, "compose", "contract_registry", nil)
	}
	return &Loader{contracts: contracts}, nil
}

// datasetWire is the closed JSON manifest shape decoded before domain composition.
type datasetWire struct {
	SchemaVersion  int              `json:"schema_version"`
	DatasetID      string           `json:"dataset_id"`
	DatasetVersion string           `json:"dataset_version"`
	Cases          []datasetRefWire `json:"cases"`
}

// datasetRefWire links one ordered case to exact local bytes.
type datasetRefWire struct {
	ID     string `json:"id"`
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

// caseWire is the non-executable serialized case contract.
type caseWire struct {
	SchemaVersion     int                        `json:"schema_version"`
	ID                string                     `json:"id"`
	Category          string                     `json:"category"`
	Tags              []string                   `json:"tags"`
	Constraints       []agentplanning.Constraint `json:"constraints"`
	OpenAPI           openAPIWire                `json:"openapi"`
	Descriptors       []descriptorWire           `json:"descriptors"`
	Limits            limitsWire                 `json:"limits"`
	Evaluation        EvaluationBudget           `json:"evaluation"`
	ExternalFragments []externalFragmentWire     `json:"external_fragments"`
	SyntheticCanaries []string                   `json:"synthetic_canaries"`
	Expected          expectationWire            `json:"expected"`
}

// openAPIWire selects trusted provenance from one digest-bound local document.
type openAPIWire struct {
	File                    string          `json:"file"`
	SHA256                  string          `json:"sha256"`
	Selections              []selectionWire `json:"selections"`
	ExpectedOmittedCanaries []string        `json:"expected_omitted_canaries"`
}

// selectionWire ties one known constraint to explicit OpenAPI operation keys.
type selectionWire struct {
	ConstraintID string                 `json:"constraint_id"`
	Operations   []openapi.OperationKey `json:"operations"`
}

// descriptorWire carries capability metadata and an allowlisted validator ID without a tool handle.
type descriptorWire struct {
	Name        string `json:"name"`
	Class       string `json:"class"`
	Description string `json:"description"`
	Effects     bool   `json:"effects"`
	Idempotent  bool   `json:"idempotent"`
	ContractID  string `json:"contract_id"`
}

// limitsWire requires every feature-10 generation ceiling in JSON.
type limitsWire struct {
	MaxInputBytes        int64 `json:"max_input_bytes"`
	MaxOutputBytes       int64 `json:"max_output_bytes"`
	MaxConstraints       int   `json:"max_constraints"`
	MaxCapabilities      int   `json:"max_capabilities"`
	MaxChecks            int   `json:"max_checks"`
	MaxStepsPerCheck     int   `json:"max_steps_per_check"`
	MaxAssertionsPerStep int   `json:"max_assertions_per_step"`
	MaxMissingItems      int   `json:"max_missing_items"`
	MaxValidationWork    int   `json:"max_validation_work"`
}

// externalFragmentWire inserts a synthetic log or response canary only into untrusted data.
type externalFragmentWire struct {
	Source       string `json:"source"`
	ConstraintID string `json:"constraint_id"`
	Canary       string `json:"canary"`
}

// expectationWire expresses bounded structural outcomes without natural-language matching.
type expectationWire struct {
	Outcome             string                      `json:"outcome"`
	RequiredConstraints []string                    `json:"required_constraints"`
	AllowedCapabilities []string                    `json:"allowed_capabilities"`
	AllowedIdentities   []string                    `json:"allowed_identities"`
	AllowedEvidence     []string                    `json:"allowed_evidence"`
	Operations          []operationWire             `json:"operations"`
	MissingKinds        []agentplanning.MissingKind `json:"missing_kinds"`
	MissingConstraints  []string                    `json:"missing_constraints"`
	Rejection           *rejectionWire              `json:"rejection"`
}

// operationWire is one exact expected constraint-operation relation.
type operationWire struct {
	ConstraintID string `json:"constraint_id"`
	Method       string `json:"method"`
	Path         string `json:"path"`
}

// rejectionWire allowlists one safe typed early-rejection signature.
type rejectionWire struct {
	Category agentplanning.ErrorCategory `json:"category"`
	Stage    string                      `json:"stage"`
	Path     string                      `json:"path"`
}

// Load snapshots a manifest, its cases and OpenAPI fixtures under checked local limits.
func (l *Loader) Load(ctx context.Context, root, manifestPath string, limits LoadLimits) (Suite, error) {
	if err := ctx.Err(); err != nil {
		return Suite{}, contextEvalError("load", err)
	}
	if limits.MaxFiles <= 0 || limits.MaxTotalBytes <= 0 || limits.MaxFileBytes <= 0 || limits.MaxCases <= 0 || limits.MaxFileBytes > limits.MaxTotalBytes {
		return Suite{}, typedError(InvalidInput, "preflight", "limits", nil)
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return Suite{}, typedError(InvalidInput, "load", "root", nil)
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return Suite{}, typedError(InvalidInput, "load", "root", nil)
	}
	state := loadState{root: base, limits: limits}
	manifest, err := state.read(ctx, manifestPath)
	if err != nil {
		return Suite{}, err
	}
	var wire datasetWire
	if err := decodeStrict(manifest, &wire); err != nil {
		return Suite{}, typedError(InvalidInput, "dataset", "manifest", nil)
	}
	if wire.SchemaVersion != 1 {
		return Suite{}, typedError(UnsupportedVersion, "dataset", "schema_version", nil)
	}
	if !stableID.MatchString(wire.DatasetID) || !semanticVersion.MatchString(wire.DatasetVersion) || len(wire.Cases) == 0 || len(wire.Cases) > limits.MaxCases {
		return Suite{}, typedError(InvalidInput, "dataset", "identity", nil)
	}
	seen := map[string]bool{}
	cases := make([]Case, 0, len(wire.Cases))
	logical := datasetWire{SchemaVersion: wire.SchemaVersion, DatasetID: wire.DatasetID, DatasetVersion: wire.DatasetVersion, Cases: make([]datasetRefWire, 0, len(wire.Cases))}
	for i, ref := range wire.Cases {
		if err := ctx.Err(); err != nil {
			return Suite{}, contextEvalError("dataset", err)
		}
		if !stableID.MatchString(ref.ID) || seen[ref.ID] || !sha256Text.MatchString(ref.SHA256) {
			return Suite{}, typedError(InvalidInput, "dataset", fmt.Sprintf("cases[%d]", i), nil)
		}
		seen[ref.ID] = true
		data, readErr := state.read(ctx, ref.File)
		if readErr != nil {
			return Suite{}, readErr
		}
		if digestBytes(data) != ref.SHA256 {
			return Suite{}, typedError(IntegrityFailure, "dataset", fmt.Sprintf("cases[%d]", i), nil)
		}
		c, composeErr := l.composeCase(ctx, &state, filepath.Dir(ref.File), ref.ID, data)
		if composeErr != nil {
			return Suite{}, composeErr
		}
		cases = append(cases, c)
		logical.Cases = append(logical.Cases, datasetRefWire{ID: ref.ID, SHA256: c.digest})
	}
	canonical, _ := canonicalJSON(logical)
	return Suite{identity: DatasetIdentity{ID: wire.DatasetID, Version: wire.DatasetVersion, Digest: digestBytes(canonical)}, cases: cases, load: limits}, nil
}

// loadState owns checked file and byte counters for one immutable suite snapshot.
type loadState struct {
	root   string
	limits LoadLimits
	files  int
	total  int64
}

// read opens one regular file, proves the opened handle still names an in-root target, and accounts its snapshot bytes.
func (s *loadState) read(ctx context.Context, relative string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, contextEvalError("load", err)
	}
	if strings.TrimSpace(relative) == "" || filepath.IsAbs(relative) || strings.Contains(relative, "://") {
		return nil, typedError(InvalidInput, "load", "path", nil)
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, typedError(InvalidInput, "load", "path", nil)
	}
	path := filepath.Join(s.root, clean)
	file, err := os.Open(path)
	if err != nil {
		return nil, typedError(InvalidInput, "load", "file", nil)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return nil, typedError(InvalidInput, "load", "file", nil)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, typedError(InvalidInput, "load", "file", nil)
	}
	rel, err := filepath.Rel(s.root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, typedError(InvalidInput, "load", "path", nil)
	}
	current, err := os.Stat(resolved)
	if err != nil || !os.SameFile(opened, current) {
		return nil, typedError(InvalidInput, "load", "file", nil)
	}
	if opened.Size() < 0 || opened.Size() >= math.MaxInt64 || opened.Size() > s.limits.MaxFileBytes || s.total > s.limits.MaxTotalBytes-opened.Size() {
		return nil, typedError(LimitExceeded, "load", "bytes", nil)
	}
	if s.files >= s.limits.MaxFiles {
		return nil, typedError(LimitExceeded, "load", "files", nil)
	}
	data, err := io.ReadAll(io.LimitReader(file, opened.Size()+1))
	if err != nil || int64(len(data)) != opened.Size() {
		return nil, typedError(InvalidInput, "load", "file", nil)
	}
	s.files++
	s.total += int64(len(data))
	return data, nil
}

// composeCase validates all structural expectations and rebuilds trusted OpenAPI provenance locally.
func (l *Loader) composeCase(ctx context.Context, state *loadState, caseDir, expectedID string, data []byte) (Case, error) {
	var wire caseWire
	if err := decodeStrict(data, &wire); err != nil {
		return Case{}, typedError(InvalidInput, "case", "document", nil)
	}
	if wire.SchemaVersion != 1 {
		return Case{}, typedError(UnsupportedVersion, "case", "schema_version", nil)
	}
	if wire.ID != expectedID || !stableID.MatchString(wire.ID) || !validLimits(wire.Limits) {
		return Case{}, typedError(InvalidInput, "case", "identity", nil)
	}
	constraints := append([]agentplanning.Constraint(nil), wire.Constraints...)
	ids := map[string]bool{}
	for i, c := range constraints {
		if !stableID.MatchString(c.ID) || strings.TrimSpace(c.Description) == "" || ids[c.ID] {
			return Case{}, typedError(InvalidInput, "case", fmt.Sprintf("constraints[%d]", i), nil)
		}
		ids[c.ID] = true
	}
	canaries := append([]string(nil), wire.SyntheticCanaries...)
	if containsUndesignatedSecret(data, canaries) {
		return Case{}, typedError(InvalidInput, "case", "secret_shape", nil)
	}
	for _, f := range wire.ExternalFragments {
		if f.Canary == "" || !contains(canaries, f.Canary) || !ids[f.ConstraintID] || (f.Source != "log" && f.Source != "response") {
			return Case{}, typedError(InvalidInput, "case", "external_fragments", nil)
		}
		for i := range constraints {
			if constraints[i].ID == f.ConstraintID {
				constraints[i].Description += "\n[external " + f.Source + "] " + f.Canary
			}
		}
	}
	openPath := filepath.Join(caseDir, wire.OpenAPI.File)
	openBytes, err := state.read(ctx, openPath)
	if err != nil {
		return Case{}, err
	}
	if !sha256Text.MatchString(wire.OpenAPI.SHA256) || digestBytes(openBytes) != wire.OpenAPI.SHA256 {
		return Case{}, typedError(IntegrityFailure, "case", "openapi", nil)
	}
	if l.afterSnapshot != nil {
		l.afterSnapshot()
	}
	cfg := openapi.Config{AuthorizedRoot: state.root, MaxDocumentBytes: state.limits.MaxFileBytes, MaxTotalBytes: state.limits.MaxTotalBytes, MaxDocuments: state.limits.MaxFiles, MaxReferences: 128, MaxReferenceDepth: 16, MaxObservedBodyBytes: 1 << 20, MaxContextBytes: int(wire.Limits.MaxInputBytes)}
	snapshot := openAPISnapshot{root: state.root, files: map[string][]byte{filepath.Clean(filepath.Join(state.root, openPath)): append([]byte(nil), openBytes...)}}
	ol, err := openapi.NewLoader(ctx, cfg, snapshot)
	if err != nil {
		return Case{}, typedError(InvalidInput, "case", "openapi", nil)
	}
	contract, err := ol.Load(ctx, openPath)
	if err != nil {
		return Case{}, typedError(InvalidInput, "case", "openapi", nil)
	}
	selections := make([]openapi.ConstraintSelection, len(wire.OpenAPI.Selections))
	for i, s := range wire.OpenAPI.Selections {
		if !ids[s.ConstraintID] {
			return Case{}, typedError(InvalidInput, "case", "selections", nil)
		}
		selections[i] = openapi.ConstraintSelection{ConstraintID: s.ConstraintID, Operations: append([]openapi.OperationKey(nil), s.Operations...)}
	}
	reduced, err := contract.BuildContext(ctx, openapi.ContextRequest{Constraints: selections})
	if err != nil {
		return Case{}, typedError(InvalidInput, "case", "selection", nil)
	}
	descriptors := make([]agentplanning.CapabilityDescriptor, len(wire.Descriptors))
	for i, d := range wire.Descriptors {
		validator, ok := l.contracts.Resolve(d.ContractID)
		if !ok || !contains(wire.Expected.AllowedCapabilities, d.Name) {
			return Case{}, typedError(InvalidInput, "case", fmt.Sprintf("descriptors[%d]", i), nil)
		}
		descriptors[i] = agentplanning.CapabilityDescriptor{Name: d.Name, Class: d.Class, Description: d.Description, Effects: d.Effects, Idempotent: d.Idempotent, Validate: validator}
	}
	if err := validateExpectationUniqueness(wire.Expected); err != nil {
		return Case{}, err
	}
	normalizeExpectationWire(&wire.Expected)
	expectation := makeExpectation(wire.Expected)
	if err := validateExpectation(expectation, ids, reduced, descriptors); err != nil {
		return Case{}, err
	}
	for _, omitted := range wire.OpenAPI.ExpectedOmittedCanaries {
		if !contains(canaries, omitted) || strings.Contains(string(reduced.Bytes), omitted) {
			return Case{}, typedError(InvalidInput, "case", "omitted_canary", nil)
		}
	}
	for _, canary := range canaries {
		if strings.Contains(canary, " ") || len(canary) < 8 {
			return Case{}, typedError(InvalidInput, "case", "synthetic_canaries", nil)
		}
	}
	sort.Strings(canaries)
	if !validEvaluationBudget(wire.Evaluation) {
		return Case{}, typedError(InvalidInput, "case", "evaluation", nil)
	}
	if _, ok := durationFromMilliseconds(wire.Evaluation.MaxDurationMillis); !ok {
		return Case{}, typedError(LimitExceeded, "case", "evaluation.max_duration_millis", nil)
	}
	logical, err := canonicalJSON(wire)
	if err != nil {
		return Case{}, typedError(InvalidInput, "case", "canonical", nil)
	}
	return Case{id: wire.ID, category: wire.Category, digest: digestBytes(logical), tags: append([]string(nil), wire.Tags...), canaries: canaries, constraints: constraints, openAPI: reduced, descriptors: descriptors, limits: makeLimits(wire.Limits), evaluation: wire.Evaluation, expectation: expectation}, nil
}

// openAPISnapshot serves only immutable, digest-verified document bytes to the OpenAPI composer.
type openAPISnapshot struct {
	root  string
	files map[string][]byte
}

// Canonicalize creates stable in-root names without consulting mutable filesystem state.
func (s openAPISnapshot) Canonicalize(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	value = filepath.Clean(value)
	rel, err := filepath.Rel(s.root, value)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("snapshot path outside root")
	}
	return value, nil
}

// ReadFile returns an isolated copy of previously verified bytes and refuses unsnapshotted references.
func (s openAPISnapshot) ReadFile(ctx context.Context, path string, max int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, ok := s.files[filepath.Clean(path)]
	if !ok || int64(len(data)) > max {
		return nil, errors.New("document absent from verified snapshot")
	}
	return append([]byte(nil), data...), nil
}

// validLimits ensures all feature-10 planning limits are explicit and positive.
func validLimits(v limitsWire) bool {
	return v.MaxInputBytes > 0 && v.MaxOutputBytes > 0 && v.MaxConstraints > 0 && v.MaxCapabilities > 0 && v.MaxChecks > 0 && v.MaxStepsPerCheck > 0 && v.MaxAssertionsPerStep > 0 && v.MaxMissingItems > 0 && v.MaxValidationWork > 0
}

// validEvaluationBudget requires explicit positive ceilings for every measured dimension.
func validEvaluationBudget(v EvaluationBudget) bool {
	return v.MaxCalls > 0 && v.MaxTokens > 0 && v.MaxDurationMillis > 0 && v.MaxReplans >= 0 && v.MaxWork > 0
}

// validateExpectationUniqueness rejects duplicate members before normalization can hide ambiguous weighting.
func validateExpectationUniqueness(value expectationWire) error {
	sets := []struct {
		path   string
		values []string
	}{
		{"expected.required_constraints", value.RequiredConstraints},
		{"expected.allowed_capabilities", value.AllowedCapabilities},
		{"expected.allowed_identities", value.AllowedIdentities},
		{"expected.allowed_evidence", value.AllowedEvidence},
		{"expected.missing_constraints", value.MissingConstraints},
	}
	for _, set := range sets {
		seen := map[string]bool{}
		for _, member := range set.values {
			if seen[member] {
				return typedError(InvalidInput, "case", set.path, nil)
			}
			seen[member] = true
		}
	}
	operations := map[string]bool{}
	for _, operation := range value.Operations {
		key := operationKey(operation.ConstraintID, operation.Method, operation.Path)
		if operations[key] {
			return typedError(InvalidInput, "case", "expected.operations", nil)
		}
		operations[key] = true
	}
	return nil
}

// normalizeExpectationWire sorts every semantic set and relation by its canonical key before hashing and composition.
func normalizeExpectationWire(value *expectationWire) {
	sort.Strings(value.RequiredConstraints)
	sort.Strings(value.AllowedCapabilities)
	sort.Strings(value.AllowedIdentities)
	sort.Strings(value.AllowedEvidence)
	sort.Strings(value.MissingConstraints)
	sort.Slice(value.MissingKinds, func(i, j int) bool { return value.MissingKinds[i] < value.MissingKinds[j] })
	for i := range value.Operations {
		value.Operations[i].Method = strings.ToUpper(value.Operations[i].Method)
	}
	sort.Slice(value.Operations, func(i, j int) bool {
		left := operationKey(value.Operations[i].ConstraintID, value.Operations[i].Method, value.Operations[i].Path)
		right := operationKey(value.Operations[j].ConstraintID, value.Operations[j].Method, value.Operations[j].Path)
		return left < right
	})
}

// makeLimits converts a fully validated wire budget without adding defaults.
func makeLimits(v limitsWire) agentplanning.Limits {
	return agentplanning.Limits{MaxInputBytes: v.MaxInputBytes, MaxOutputBytes: v.MaxOutputBytes, MaxConstraints: v.MaxConstraints, MaxCapabilities: v.MaxCapabilities, MaxChecks: v.MaxChecks, MaxStepsPerCheck: v.MaxStepsPerCheck, MaxAssertionsPerStep: v.MaxAssertionsPerStep, MaxMissingItems: v.MaxMissingItems, MaxValidationWork: v.MaxValidationWork}
}

// makeExpectation converts JSON expectations into isolated domain values.
func makeExpectation(v expectationWire) CaseExpectation {
	out := CaseExpectation{Outcome: v.Outcome, RequiredConstraints: append([]string(nil), v.RequiredConstraints...), AllowedCapabilities: append([]string(nil), v.AllowedCapabilities...), AllowedIdentities: append([]string(nil), v.AllowedIdentities...), AllowedEvidence: append([]string(nil), v.AllowedEvidence...), MissingKinds: append([]agentplanning.MissingKind(nil), v.MissingKinds...), MissingConstraints: append([]string(nil), v.MissingConstraints...)}
	for _, op := range v.Operations {
		out.Operations = append(out.Operations, OperationExpectation{ConstraintID: op.ConstraintID, Method: strings.ToUpper(op.Method), Path: op.Path})
	}
	if v.Rejection != nil {
		out.Rejection = &RejectionExpectation{Category: v.Rejection.Category, Stage: v.Rejection.Stage, Path: v.Rejection.Path}
	}
	return out
}

// validateExpectation closes all expected sets over the case's trusted provenance and catalog.
func validateExpectation(e CaseExpectation, ids map[string]bool, reduced openapi.ReducedContext, descriptors []agentplanning.CapabilityDescriptor) error {
	if e.Outcome != "plan" && e.Outcome != "insufficient_context" && e.Outcome != "rejected" {
		return typedError(InvalidInput, "case", "expected.outcome", nil)
	}
	for _, id := range e.RequiredConstraints {
		if !ids[id] {
			return typedError(InvalidInput, "case", "expected.required_constraints", nil)
		}
	}
	caps := map[string]bool{}
	for _, d := range descriptors {
		caps[d.Name] = true
	}
	for _, c := range e.AllowedCapabilities {
		if !caps[c] {
			return typedError(InvalidInput, "case", "expected.allowed_capabilities", nil)
		}
	}
	retained := map[string]bool{}
	for _, r := range reduced.RetainedOperations() {
		retained[r.ConstraintID+"\x00"+strings.ToUpper(r.Operation.Key.Method)+"\x00"+r.Operation.Key.Path] = true
	}
	for _, op := range e.Operations {
		if !retained[op.ConstraintID+"\x00"+op.Method+"\x00"+op.Path] {
			return typedError(InvalidInput, "case", "expected.operations", nil)
		}
	}
	if e.Outcome == "rejected" && e.Rejection == nil {
		return typedError(InvalidInput, "case", "expected.rejection", nil)
	}
	if e.Outcome != "rejected" && e.Rejection != nil {
		return typedError(InvalidInput, "case", "expected.rejection", nil)
	}
	kinds := map[agentplanning.MissingKind]bool{}
	for _, kind := range e.MissingKinds {
		if (kind != agentplanning.MissingQuestion && kind != agentplanning.MissingEvidence) || kinds[kind] {
			return typedError(InvalidInput, "case", "expected.missing_kinds", nil)
		}
		kinds[kind] = true
	}
	missing := map[string]bool{}
	for _, id := range e.MissingConstraints {
		if !ids[id] || missing[id] {
			return typedError(InvalidInput, "case", "expected.missing_constraints", nil)
		}
		missing[id] = true
	}
	if e.Outcome == "insufficient_context" && (len(kinds) == 0 || len(missing) == 0) {
		return typedError(InvalidInput, "case", "expected.missing_context", nil)
	}
	if e.Outcome != "insufficient_context" && (len(kinds) != 0 || len(missing) != 0) {
		return typedError(InvalidInput, "case", "expected.missing_context", nil)
	}
	return nil
}

// StaticContractRegistry is an exact local map from versioned IDs to non-executing validators.
type StaticContractRegistry map[string]func(map[string]planning.InputValue) error

// Resolve returns only a descriptor validator and never an executable tool handle.
func (r StaticContractRegistry) Resolve(id string) (func(map[string]planning.InputValue) error, bool) {
	v, ok := r[id]
	return v, ok
}

// AcceptDescriptor validates the descriptor boundary while deferring input semantics to deterministic plan validation.
func AcceptDescriptor(map[string]planning.InputValue) error { return nil }

// contains performs exact membership checks for small bounded allowlists.
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// containsUndesignatedSecret rejects common credential-shaped fixture text unless explicitly marked synthetic.
func containsUndesignatedSecret(data []byte, canaries []string) bool {
	sanitized := string(data)
	for _, canary := range canaries {
		sanitized = strings.ReplaceAll(sanitized, canary, "")
	}
	lower := strings.ToLower(sanitized)
	for _, shape := range []string{"sk-", "bearer ", "password=", "\"authorization\":", "\"credential\":"} {
		if strings.Contains(lower, shape) {
			return true
		}
	}
	return false
}

// contextEvalError maps cancellation and deadline to the public stable categories.
func contextEvalError(stage string, err error) error {
	category := Cancelled
	if errors.Is(err, context.DeadlineExceeded) {
		category = Timeout
	}
	return typedError(category, stage, "context", err)
}
