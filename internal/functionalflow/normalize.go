package functionalflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/planning"
)

// GeneratedAuthority supplies trusted facts omitted from a model-produced prepared plan.
type GeneratedAuthority struct {
	ContextDigest    string
	TargetID         string
	ContractDigest   string
	Effects          map[string]bool
	OperationEffects map[string]bool
	PublicIdentity   string
}

// DecodeProvided strictly decodes one bounded provided document without accepting duplicate or trailing JSON.
func DecodeProvided(document []byte, limits Limits) (Flow, error) {
	if limits.MaxDocumentBytes <= 0 || len(document) == 0 || len(document) > limits.MaxDocumentBytes {
		return Flow{}, flowError(CategoryLimit, "document", nil)
	}
	if err := rejectDuplicateKeys(document); err != nil {
		return Flow{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var flow Flow
	if err := decoder.Decode(&flow); err != nil {
		return Flow{}, flowError(CategoryInvalid, "document", nil)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Flow{}, err
	}
	flow.Source = SourceProvided
	return cloneFlow(flow), nil
}

// NormalizeGenerated maps an already prepared plan to the same canonical graph used by provided flows.
func NormalizeGenerated(prepared agentplanning.PreparedPlan, authority GeneratedAuthority) (Flow, error) {
	return NormalizeGeneratedSnapshot(prepared.Snapshot(), authority)
}

// NormalizeGeneratedExecution overlays only revalidated materialized step inputs and identity bindings onto the canonical generated graph.
func NormalizeGeneratedExecution(snapshot agentplanning.PreparedSnapshot, plans []planning.ValidationPlan, authority GeneratedAuthority) (Flow, error) {
	flow, err := NormalizeGeneratedSnapshot(snapshot, authority)
	if err != nil {
		return Flow{}, err
	}
	materialized := map[string]planning.PlanStep{}
	for _, plan := range plans {
		for _, step := range plan.Steps {
			if _, duplicate := materialized[step.ID]; duplicate {
				return Flow{}, flowError(CategoryIntegrity, "generated.materialized.id", nil)
			}
			materialized[step.ID] = step
		}
	}
	if len(materialized) != len(flow.Steps) {
		return Flow{}, flowError(CategoryIntegrity, "generated.materialized", nil)
	}
	for i := range flow.Steps {
		step, ok := materialized[flow.Steps[i].ID]
		if !ok || step.Input == nil {
			return Flow{}, flowError(CategoryIntegrity, "generated.materialized.step", nil)
		}
		flow.Steps[i].Input = convertPlanningInput(step.Input)
		if step.IdentityBinding != nil {
			flow.Steps[i].IdentityID = step.IdentityBinding.IdentityID
		}
	}
	return cloneFlow(flow), nil
}

// NormalizeGeneratedSnapshot revalidates a recovered private planning snapshot through the same canonical adapter.
func NormalizeGeneratedSnapshot(snapshot agentplanning.PreparedSnapshot, authority GeneratedAuthority) (Flow, error) {
	if snapshot.ID == "" || authority.ContextDigest == "" || authority.TargetID == "" || authority.ContractDigest == "" {
		return Flow{}, flowError(CategoryAuthority, "generated.authority", nil)
	}
	flow := Flow{Version: VersionV1, ID: snapshot.ID, Source: SourceGenerated, Revision: 1, ContextDigest: authority.ContextDigest, Steps: []Step{}, RequiredEvidence: []string{}}
	seenStep := map[string]bool{}
	for _, check := range snapshot.Checks {
		for _, candidate := range check.Steps {
			stepID := check.ID + "-" + candidate.ID
			if seenStep[stepID] {
				return Flow{}, flowError(CategoryInvalid, "checks.steps.id", nil)
			}
			seenStep[stepID] = true
			identityID := check.IdentityID
			if candidate.IdentityBinding != nil {
				identityID = candidate.IdentityBinding.IdentityID
			}
			if identityID == "" {
				return Flow{}, flowError(CategoryAuthority, "checks.identity_id", nil)
			}
			method, path := "", ""
			if check.Operation != nil {
				method, path = check.Operation.Method, check.Operation.Path
			}
			effectful := authority.Effects[candidate.Tool]
			if exact, exists := authority.OperationEffects[method+"\x00"+path]; exists {
				effectful = exact
			}
			kind := StepObserve
			if effectful {
				kind = StepAct
			}
			assertions := append([]planning.AssertionDeclaration(nil), candidate.Assertions...)
			required := make([]string, 0, len(assertions))
			for _, declaration := range assertions {
				required = append(required, declaration.ID)
			}
			dependencies := append([]string(nil), candidate.DependsOn...)
			if dependencies == nil {
				dependencies = []string{}
			}
			for i := range dependencies {
				dependencies[i] = check.ID + "-" + dependencies[i]
			}
			input := convertPlanningInput(candidate.Input)
			prefixGeneratedStepReferences(input, check.ID+"-")
			flow.Steps = append(flow.Steps, Step{ID: stepID, Kind: kind, ConstraintID: check.ConstraintID, DependsOn: dependencies, Operation: OperationReference{Capability: candidate.Tool, TargetID: authority.TargetID, Method: method, Path: path, ContractDigest: authority.ContractDigest}, IdentityID: identityID, Input: input, Assertions: assertions, Comparisons: []ObservationComparison{}, RequiredEvidence: required})
			for _, declarationID := range required {
				flow.RequiredEvidence = append(flow.RequiredEvidence, declarationCoordinate(stepID, declarationID))
			}
		}
	}
	return cloneFlow(flow), nil
}

// prefixGeneratedStepReferences maps check-local model IDs to the globally unique materialized execution namespace.
func prefixGeneratedStepReferences(input map[string]InputValue, prefix string) {
	for key, value := range input {
		if value.Reference != nil && value.Reference.Step != nil {
			value.Reference.Step.StepID = prefix + value.Reference.Step.StepID
		}
		if value.Object != nil {
			prefixGeneratedStepReferences(value.Object, prefix)
		}
		for i := range value.Array {
			child := map[string]InputValue{"value": value.Array[i]}
			prefixGeneratedStepReferences(child, prefix)
			value.Array[i] = child["value"]
		}
		input[key] = value
	}
}

// convertPlanningInput lifts historical step references into the functional-flow closed reference union.
func convertPlanningInput(input map[string]planning.InputValue) map[string]InputValue {
	out := make(map[string]InputValue, len(input))
	for key, value := range input {
		out[key] = convertPlanningValue(value)
	}
	return out
}

// convertPlanningValue recursively isolates one historical planning input node.
func convertPlanningValue(value planning.InputValue) InputValue {
	out := InputValue{}
	if value.Literal != nil {
		literal := *value.Literal
		out.Literal = &literal
	}
	if value.Object != nil {
		out.Object = convertPlanningInput(value.Object)
	}
	if value.Array != nil {
		out.Array = make([]InputValue, len(value.Array))
		for i := range value.Array {
			out.Array[i] = convertPlanningValue(value.Array[i])
		}
	}
	if value.Reference != nil {
		ref := StepReference{StepID: value.Reference.StepID, Path: clonePath(value.Reference.Path), ValueType: value.Reference.ValueType}
		out.Reference = &Reference{Step: &ref}
	}
	return out
}

// rejectDuplicateKeys walks JSON tokens and rejects duplicate object members before struct decoding.
func rejectDuplicateKeys(document []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, compound := token.(json.Delim)
		if !compound {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return errors.New("duplicate key")
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("invalid delimiter")
		}
	}
	if err := walk(); err != nil {
		return flowError(CategoryInvalid, "document", nil)
	}
	return nil
}

// ensureJSONEOF rejects a second JSON value or non-whitespace trailing content.
func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return flowError(CategoryInvalid, "document.trailing", nil)
	}
	return nil
}

// semanticDigest fingerprints source-neutral flow meaning so both producer paths converge.
func semanticDigest(flow Flow) (string, error) {
	copy := cloneFlow(flow)
	copy.Source = ""
	raw, err := json.Marshal(copy)
	if err != nil {
		return "", flowError(CategoryInternal, "digest", nil)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// flowError creates a bounded diagnostic and strips collaborator-controlled causes.
func flowError(category ErrorCategory, path string, cause error) error {
	if !errors.Is(cause, contextCanceled()) && !errors.Is(cause, contextDeadline()) {
		cause = nil
	}
	return &Error{Category: category, Path: safePath(path), Cause: cause}
}

// contextCanceled returns the standard cancellation sentinel without exposing arbitrary causes.
func contextCanceled() error { return context.Canceled }

// contextDeadline returns the standard deadline sentinel without exposing arbitrary causes.
func contextDeadline() error { return context.DeadlineExceeded }

// safePath constrains diagnostics to system-owned structural tokens.
func safePath(path string) string {
	if path == "" || len(path) > 160 || strings.ContainsAny(path, "?#\\") || strings.Contains(path, "://") {
		return "flow"
	}
	for _, r := range path {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-[]", r)) {
			return "flow"
		}
	}
	return path
}
