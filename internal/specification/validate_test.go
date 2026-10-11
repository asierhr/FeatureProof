package specification

import (
	"errors"
	"testing"
)

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

func TestValidateRejectsKnownButUnsupportedInternalVersion(t *testing.T) {
	spec := validSpecification()
	spec.Version = 2
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
