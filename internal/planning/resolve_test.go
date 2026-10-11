package planning

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/asierhr/featureProof/internal/evidence"
)

// memoryEvidence is a hermetic immutable-record lookup used by reference tests.
type memoryEvidence struct {
	records map[string]evidence.Record
	err     error
}

// Save confirms a record in the fake store for executor tests.
func (m *memoryEvidence) Save(_ context.Context, record evidence.Record) error {
	if m.err != nil {
		return m.err
	}
	if m.records == nil {
		m.records = map[string]evidence.Record{}
	}
	m.records[record.ID] = record
	return nil
}

// Get returns only records explicitly confirmed in this fake store.
func (m *memoryEvidence) Get(ctx context.Context, id string) (evidence.Record, error) {
	if err := ctx.Err(); err != nil {
		return evidence.Record{}, err
	}
	if m.err != nil {
		return evidence.Record{}, m.err
	}
	record, ok := m.records[id]
	if !ok {
		return evidence.Record{}, errors.New("missing evidence")
	}
	return record, nil
}

// stringPointer creates stable field selectors without sharing mutable path state.
func stringPointer(value string) *string { return &value }

// intPointer creates stable array-index selectors for nested reference tests.
func intPointer(value int) *int { return &value }

// referenceStep builds a consumer containing both nested object and array references.
func referenceStep() PlanStep {
	reference := OutputReference{StepID: "source", Path: []PathSegment{{Field: stringPointer("items")}, {Index: intPointer(0)}, {Field: stringPointer("token")}}, ValueType: ValueString}
	return PlanStep{ID: "consumer", Input: map[string]InputValue{
		"body": {Object: map[string]InputValue{"tokens": {Array: []InputValue{{Reference: &reference}, {Literal: &LiteralValue{Value: "fixed"}}}}}},
	}}
}

// TestResolveTypedReferencesFromPersistedAncestorEvidence traces R3 through nested field and index selection.
func TestResolveTypedReferencesFromPersistedAncestorEvidence(t *testing.T) {
	store := &memoryEvidence{records: map[string]evidence.Record{"ev": {ID: "ev", RunID: "run", StepID: "source", Output: map[string]any{"items": []any{map[string]any{"token": "abc"}}}}}}
	results := map[string]StepResult{"source": {StepID: "source", Status: StepPassed, EvidenceIDs: []string{"ev"}, TerminalEvidenceID: "ev"}}
	step := referenceStep()
	resolved, err := ResolveStepInput(context.Background(), "run", step, results, store)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"body": map[string]any{"tokens": []any{"abc", "fixed"}}}
	if !reflect.DeepEqual(resolved, want) {
		t.Fatalf("got %#v, want %#v", resolved, want)
	}
	resolved["body"].(map[string]any)["tokens"].([]any)[0] = "changed"
	if step.Input["body"].Object["tokens"].Array[0].Reference.StepID != "source" {
		t.Fatal("resolution mutated the plan input")
	}
}

// TestResolutionRejectsForeignEvidenceMissingPathAndWrongType traces R3 security and type boundaries.
func TestResolutionRejectsForeignEvidenceMissingPathAndWrongType(t *testing.T) {
	tests := []struct {
		name   string
		record evidence.Record
		mutate func(*PlanStep)
	}{
		{"foreign run", evidence.Record{ID: "ev", RunID: "other", StepID: "source", Output: map[string]any{}}, nil},
		{"foreign step", evidence.Record{ID: "ev", RunID: "run", StepID: "other", Output: map[string]any{}}, nil},
		{"missing path", evidence.Record{ID: "ev", RunID: "run", StepID: "source", Output: map[string]any{}}, nil},
		{"wrong type", evidence.Record{ID: "ev", RunID: "run", StepID: "source", Output: map[string]any{"items": []any{map[string]any{"token": true}}}}, nil},
		{"index out of range", evidence.Record{ID: "ev", RunID: "run", StepID: "source", Output: map[string]any{"items": []any{map[string]any{"token": "ok"}}}}, func(step *PlanStep) { *step.Input["body"].Object["tokens"].Array[0].Reference.Path[1].Index = 4 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			step := referenceStep()
			if test.mutate != nil {
				test.mutate(&step)
			}
			store := &memoryEvidence{records: map[string]evidence.Record{"ev": test.record}}
			results := map[string]StepResult{"source": {StepID: "source", Status: StepPassed, EvidenceIDs: []string{"ev"}, TerminalEvidenceID: "ev"}}
			_, err := ResolveStepInput(context.Background(), "run", step, results, store)
			var planErr *PlanError
			if !errors.As(err, &planErr) || planErr.Category != ErrorResolutionFailed {
				t.Fatalf("got %#v", err)
			}
		})
	}
}

// TestReferenceValueTypesAndDeepCopies covers all supported JSON value types without sharing evidence containers.
func TestReferenceValueTypesAndDeepCopies(t *testing.T) {
	values := []struct {
		value any
		type_ ValueType
	}{
		{"text", ValueString}, {true, ValueBoolean}, {float64(3), ValueNumber},
		{map[string]any{"nested": "value"}, ValueObject}, {[]any{"value"}, ValueArray},
	}
	for _, test := range values {
		if !matchesValueType(test.value, test.type_) {
			t.Fatalf("%#v should match %s", test.value, test.type_)
		}
		copy := deepCopyJSON(test.value)
		if !reflect.DeepEqual(copy, test.value) {
			t.Fatalf("copy changed value %#v", test.value)
		}
	}
	if matchesValueType("3", ValueNumber) || matchesValueType(3, ValueArray) || matchesValueType(nil, ValueObject) {
		t.Fatal("incompatible types matched")
	}
	object := map[string]any{"array": []any{map[string]any{"value": "original"}}}
	copy := deepCopyJSON(object).(map[string]any)
	copy["array"].([]any)[0].(map[string]any)["value"] = "changed"
	if object["array"].([]any)[0].(map[string]any)["value"] != "original" {
		t.Fatal("deep copy shared evidence containers")
	}
}

// TestResolveRejectsUnconfirmedTerminalEvidence covers missing, non-passed, and unrelated terminal pointers.
func TestResolveRejectsUnconfirmedTerminalEvidence(t *testing.T) {
	step := referenceStep()
	store := &memoryEvidence{records: map[string]evidence.Record{}}
	for _, result := range []StepResult{
		{},
		{StepID: "source", Status: StepFailed, EvidenceIDs: []string{"ev"}, TerminalEvidenceID: "ev"},
		{StepID: "source", Status: StepPassed, EvidenceIDs: []string{"other"}, TerminalEvidenceID: "ev"},
	} {
		_, err := ResolveStepInput(context.Background(), "run", step, map[string]StepResult{"source": result}, store)
		var planErr *PlanError
		if !errors.As(err, &planErr) || planErr.Category != ErrorResolutionFailed {
			t.Fatalf("got %#v", err)
		}
	}
}
