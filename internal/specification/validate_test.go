package specification

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

// TestFixtureValueModelRejectsAmbiguousNonFiniteAndNullReferenceTypes verifies the internal v3 union independently of YAML.
func TestFixtureValueModelRejectsAmbiguousNonFiniteAndNullReferenceTypes(t *testing.T) {
	cases := []FixtureValue{
		{Kind: "literal", Literal: math.NaN()},
		{Kind: "literal", Literal: math.Inf(1)},
		{Kind: "literal", Literal: json.Number("NaN")},
		{Kind: "literal", Literal: json.Number("+Inf")},
		{Kind: "literal", Literal: json.Number("-Inf")},
		{Kind: "literal", Literal: json.Number("1e10000")},
		{Kind: "literal", Literal: make(chan int)},
		{Kind: "literal", Literal: "x", Object: map[string]FixtureValue{}},
		{Kind: "reference", Reference: &FixtureReference{FixtureID: "a", Output: "id", ValueType: "null"}},
		{Kind: "reference", Reference: &FixtureReference{FixtureID: "a", Output: "id", ValueType: "string"}, Array: []FixtureValue{}},
		{Kind: "object", Object: map[string]FixtureValue{}, Array: []FixtureValue{}},
	}
	for index, value := range cases {
		if err := validateFixtureValue(value, "fixture", 0); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("case %d accepted: %v", index, err)
		}
	}
}

func validSpecification() Specification {
	return Specification{
		Version: 1,
		Feature: Feature{ID: "checkout-search", Description: "Search returns products"},
		Target:  Target{ID: "staging_1", BaseURL: "https://staging.example.test"},
		Constraints: []Constraint{{
			ID:          "search.results-1",
			Description: "A known search returns at least one result",
			TargetID:    "staging_1",
		}},
		Limits: Limits{MaxSteps: 1, MaxToolCalls: 1, MaxRetries: 0},
	}
}

func TestValidationErrorIdentifiesCategoryAndField(t *testing.T) {
	spec := validSpecification()
	spec.Constraints[0].Description = " "
	err := Validate(spec)
	if !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("errors.Is(error, ErrInvalidValue) = false; error = %v", err)
	}
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) || validationErr.Category != CategoryInvalidValue || validationErr.Field != "constraints[0].description" {
		t.Fatalf("ValidationError = %#v", validationErr)
	}
}

func TestValidateRejectsMissingRequiredFields(t *testing.T) {
	tests := []struct {
		name   string
		field  string
		mutate func(*Specification)
	}{
		{"version", "version", func(s *Specification) { s.Version = 0 }},
		{"feature id", "feature.id", func(s *Specification) { s.Feature.ID = "" }},
		{"feature description", "feature.description", func(s *Specification) { s.Feature.Description = "" }},
		{"target id", "target.id", func(s *Specification) { s.Target.ID = "" }},
		{"target base URL", "target.base_url", func(s *Specification) { s.Target.BaseURL = "" }},
		{"constraints", "constraints", func(s *Specification) { s.Constraints = nil }},
		{"constraint id", "constraints[0].id", func(s *Specification) { s.Constraints[0].ID = "" }},
		{"constraint description", "constraints[0].description", func(s *Specification) { s.Constraints[0].Description = "" }},
		{"constraint target", "constraints[0].target_id", func(s *Specification) { s.Constraints[0].TargetID = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := validSpecification()
			test.mutate(&spec)
			err := Validate(spec)
			var validationErr *ValidationError
			if !errors.Is(err, ErrRequired) || !errors.As(err, &validationErr) || validationErr.Field != test.field {
				t.Fatalf("ValidationError = %#v; want required at %q", validationErr, test.field)
			}
		})
	}
}

func TestValidateRejectsDuplicateConstraintIDs(t *testing.T) {
	spec := validSpecification()
	spec.Constraints = append(spec.Constraints, spec.Constraints[0])
	err := Validate(spec)
	var validationErr *ValidationError
	if !errors.Is(err, ErrDuplicateID) || !errors.As(err, &validationErr) || validationErr.Field != "constraints[1].id" {
		t.Fatalf("ValidationError = %#v", validationErr)
	}
}

func TestValidateRejectsUnknownTargetReference(t *testing.T) {
	spec := validSpecification()
	spec.Constraints[0].TargetID = "production"
	err := Validate(spec)
	var validationErr *ValidationError
	if !errors.Is(err, ErrUnknownReference) || !errors.As(err, &validationErr) || validationErr.Field != "constraints[0].target_id" {
		t.Fatalf("ValidationError = %#v", validationErr)
	}
}

func TestValidationReturnsStableFirstError(t *testing.T) {
	spec := validSpecification()
	spec.Feature.ID = "bad id"
	spec.Target.ID = "bad/id"
	for range 10 {
		err := Validate(spec)
		var validationErr *ValidationError
		if !errors.As(err, &validationErr) || validationErr.Field != "feature.id" {
			t.Fatalf("ValidationError = %#v", validationErr)
		}
	}
}

func TestValidationErrorsExposeStableCategoriesAndContext(t *testing.T) {
	cause := errors.New("cause")
	tests := []struct {
		category Category
		sentinel error
	}{
		{CategoryRequired, ErrRequired},
		{CategoryInvalidValue, ErrInvalidValue},
		{CategoryDuplicateID, ErrDuplicateID},
		{CategoryUnknownReference, ErrUnknownReference},
		{CategoryUnknownField, ErrUnknownField},
		{CategorySyntax, ErrSyntax},
	}
	for _, test := range tests {
		err := &ValidationError{Category: test.category, Field: "feature.id", Cause: cause}
		if !errors.Is(err, test.sentinel) || !errors.Is(err, cause) {
			t.Fatalf("error category %q does not preserve identity or cause", test.category)
		}
		if got := err.Error(); got != "validate specification feature.id: "+string(test.category) {
			t.Fatalf("Error() = %q", got)
		}
	}

	located := &ValidationError{Category: CategorySyntax, Line: 3, Column: 4}
	if got := located.Error(); got != "validate specification line 3, column 4: syntax" {
		t.Fatalf("located Error() = %q", got)
	}
	unlocated := &ValidationError{Category: CategorySyntax}
	if got := unlocated.Error(); got != "validate specification: syntax" {
		t.Fatalf("unlocated Error() = %q", got)
	}
}

func TestUnsupportedVersionErrorReportsContract(t *testing.T) {
	err := &UnsupportedVersionError{Received: 2, Supported: []int{1}}
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatal("unsupported version identity not preserved")
	}
	if got := err.Error(); got != "validate specification version: unsupported_version received=2 supported=[1]" {
		t.Fatalf("Error() = %q", got)
	}
}

// TestValidateRejectsKnownButUnsupportedInternalVersion keeps unknown future contracts outside domain interpretation.
func TestValidateRejectsKnownButUnsupportedInternalVersion(t *testing.T) {
	spec := validSpecification()
	spec.Version = 4
	if !errors.Is(Validate(spec), ErrInvalidValue) {
		t.Fatalf("Validate() error = %v", Validate(spec))
	}
}

func TestValidateAcceptsCompleteInternalModelAndBoundaryLimits(t *testing.T) {
	spec := validSpecification()

	if err := Validate(spec); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsInvalidModelValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Specification)
	}{
		{"feature id", func(s *Specification) { s.Feature.ID = "-bad" }},
		{"blank feature description", func(s *Specification) { s.Feature.Description = "  " }},
		{"target id", func(s *Specification) { s.Target.ID = "bad/id" }},
		{"relative target URL", func(s *Specification) { s.Target.BaseURL = "/relative" }},
		{"URL user info", func(s *Specification) { s.Target.BaseURL = "https://user@example.test" }},
		{"URL query", func(s *Specification) { s.Target.BaseURL = "https://example.test?q=1" }},
		{"URL fragment", func(s *Specification) { s.Target.BaseURL = "https://example.test/#x" }},
		{"empty constraints", func(s *Specification) { s.Constraints = nil }},
		{"constraint id", func(s *Specification) { s.Constraints[0].ID = "bad id" }},
		{"blank constraint description", func(s *Specification) { s.Constraints[0].Description = "\t" }},
		{"zero steps", func(s *Specification) { s.Limits.MaxSteps = 0 }},
		{"zero tool calls", func(s *Specification) { s.Limits.MaxToolCalls = 0 }},
		{"negative retries", func(s *Specification) { s.Limits.MaxRetries = -1 }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := validSpecification()
			test.mutate(&spec)
			if err := Validate(spec); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

// validV3Specification returns a complete domain fixture with every recursive input location represented.
func validV3Specification() Specification {
	spec := validSpecification()
	spec.Version = 3
	spec.Identities = []TestIdentity{{ID: "writer", TargetID: spec.Target.ID, CredentialRef: "credential", Mechanism: AuthenticationMechanism{Type: "bearer"}, Operations: []AuthorizedOperation{{Capability: "http.request", Method: "POST", Path: "/resources"}, {Capability: "http.request", Method: "DELETE", Path: "/resources/{id}"}}}}
	body := FixtureValue{Kind: "object", Object: map[string]FixtureValue{"name": {Kind: "literal", Literal: "value"}, "tags": {Kind: "array", Array: []FixtureValue{{Kind: "literal", Literal: true}}}}}
	field, index := "body", 0
	spec.Fixtures = []Fixture{{
		ID: "resource", DependsOn: []string{},
		Create:   FixtureOperation{Method: "POST", Path: "/resources", IdentityID: "writer", Input: FixtureInput{Query: map[string]FixtureValue{"page": {Kind: "literal", Literal: 1}}, Body: &body}, AcceptedStatuses: []int{201}, Idempotency: "contract"},
		Captures: []CaptureDeclaration{{Name: "id", Path: []CapturePathSegment{{Field: &field}, {Index: &index}}, ValueType: "string", MaxBytes: 64}},
		Cleanup:  FixtureOperation{Method: "DELETE", Path: "/resources/{id}", IdentityID: "writer", Input: FixtureInput{PathParameters: map[string]FixtureValue{"id": {Kind: "reference", Reference: &FixtureReference{FixtureID: "resource", Output: "id", ValueType: "string"}}}}, AcceptedStatuses: []int{204, 404}, Idempotency: "contract"},
	}}
	return spec
}

// cloneV3Specification isolates each behavioral validation mutation through the public JSON model.
func cloneV3Specification(t *testing.T, value Specification) Specification {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var clone Specification
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

// TestValidateV3FixtureBehavioralMatrix exercises every closed declaration boundary through the domain entrypoint.
func TestValidateV3FixtureBehavioralMatrix(t *testing.T) {
	valid := validV3Specification()
	if err := Validate(valid); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*Specification){
		func(value *Specification) { value.Fixtures = append(value.Fixtures, value.Fixtures[0]) },
		func(value *Specification) { value.Fixtures[0].DependsOn = []string{"bad value"} },
		func(value *Specification) { value.Fixtures[0].DependsOn = []string{"resource"} },
		func(value *Specification) { value.Fixtures[0].DependsOn = []string{"other", "other"} },
		func(value *Specification) { value.Fixtures[0].Create.Method = "INVALID METHOD" },
		func(value *Specification) { value.Fixtures[0].Create.Path = "https://untrusted.test" },
		func(value *Specification) { value.Fixtures[0].Create.IdentityID = "" },
		func(value *Specification) { value.Fixtures[0].Create.Idempotency = "caller" },
		func(value *Specification) { value.Fixtures[0].Create.AcceptedStatuses = nil },
		func(value *Specification) { value.Fixtures[0].Create.AcceptedStatuses = []int{99} },
		func(value *Specification) { value.Fixtures[0].Create.AcceptedStatuses = []int{201, 201} },
		func(value *Specification) {
			value.Fixtures[0].Create.Input.Query = map[string]FixtureValue{"bad name": {Kind: "literal", Literal: 1}}
		},
		func(value *Specification) {
			value.Fixtures[0].Cleanup.Input.PathParameters = map[string]FixtureValue{"bad name": {Kind: "literal", Literal: 1}}
		},
		func(value *Specification) { value.Fixtures[0].Captures = nil },
		func(value *Specification) {
			value.Fixtures[0].Captures = append(value.Fixtures[0].Captures, value.Fixtures[0].Captures[0])
		},
		func(value *Specification) { value.Fixtures[0].Captures[0].Path = nil },
		func(value *Specification) { value.Fixtures[0].Captures[0].ValueType = "null" },
		func(value *Specification) { value.Fixtures[0].Captures[0].MaxBytes = 0 },
		func(value *Specification) { value.Fixtures[0].Captures[0].Path[0] = CapturePathSegment{} },
		func(value *Specification) {
			empty := ""
			value.Fixtures[0].Captures[0].Path[0] = CapturePathSegment{Field: &empty}
		},
		func(value *Specification) {
			negative := -1
			value.Fixtures[0].Captures[0].Path[0] = CapturePathSegment{Index: &negative}
		},
	}
	for index, mutate := range mutations {
		candidate := cloneV3Specification(t, valid)
		mutate(&candidate)
		if err := Validate(candidate); err == nil {
			t.Fatalf("v3 fixture mutation %d accepted", index)
		}
	}
}

// TestFixtureJSONAndUnionBoundaries covers finite recursive JSON kinds, empty keys, depth, and each valid union arm.
func TestFixtureJSONAndUnionBoundaries(t *testing.T) {
	validJSON := []any{nil, "value", true, json.Number("1.25"), float32(1), float64(1), int(1), int8(1), int16(1), int32(1), int64(1), uint(1), uint8(1), uint16(1), uint32(1), uint64(1), map[string]any{"key": []any{1, true}}, []any{"value"}}
	for index, value := range validJSON {
		if !validFixtureJSON(value, 0) {
			t.Fatalf("finite JSON kind %d rejected", index)
		}
	}
	invalidJSON := []any{float32(float32(math.Inf(1))), map[string]any{"": 1}, map[string]any{"key": make(chan int)}, []any{make(chan int)}}
	for index, value := range invalidJSON {
		if validFixtureJSON(value, 0) {
			t.Fatalf("invalid JSON kind %d accepted", index)
		}
	}
	if validFixtureJSON(nil, 33) {
		t.Fatal("fixture JSON depth limit ignored")
	}
	values := []FixtureValue{{Kind: "literal", Literal: nil}, {Kind: "reference", Reference: &FixtureReference{FixtureID: "resource", Output: "id", ValueType: "string"}}, {Kind: "object", Object: map[string]FixtureValue{"key": {Kind: "literal", Literal: 1}}}, {Kind: "array", Array: []FixtureValue{{Kind: "literal", Literal: true}}}}
	for index, value := range values {
		if err := validateFixtureValue(value, "fixture", 0); err != nil {
			t.Fatalf("valid union %d rejected: %v", index, err)
		}
	}
	if err := validateFixtureValue(values[0], "fixture", 33); err == nil {
		t.Fatal("fixture union depth limit ignored")
	}
}
