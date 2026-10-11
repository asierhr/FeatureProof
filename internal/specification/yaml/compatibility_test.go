package specyaml_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/asierhr/featureProof/internal/specification"
	specyaml "github.com/asierhr/featureProof/internal/specification/yaml"
)

func fixture(name string) string { return filepath.Join("testdata", name) }

func TestV1CompatibilityFixtures(t *testing.T) {
	t.Setenv("FEATURE_DESCRIPTION", "must-not-be-expanded")

	got, err := specyaml.Load(context.Background(), fixture("valid_v1.yaml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Feature.Description != "${FEATURE_DESCRIPTION}" {
		t.Fatalf("description = %q", got.Feature.Description)
	}
	if got.Target.BaseURL != "http://127.0.0.1:1/application" {
		t.Fatalf("base URL = %q", got.Target.BaseURL)
	}
}

func TestV1InvalidFixturesRemainRejected(t *testing.T) {
	tests := []struct {
		name     string
		category error
	}{
		{"invalid_syntax_v1.yaml", specification.ErrSyntax},
		{"invalid_semantic_v1.yaml", specification.ErrInvalidValue},
		{"unsupported_version.yaml", specification.ErrUnsupportedVersion},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := specyaml.Load(context.Background(), fixture(test.name))
			if !reflect.DeepEqual(got, specification.Specification{}) || !errors.Is(err, test.category) {
				t.Fatalf("Load() = %#v, %v", got, err)
			}
		})
	}
}

func TestV1LoadIsDeterministicAndStateless(t *testing.T) {
	first, err := specyaml.Load(context.Background(), fixture("valid_v1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	first.Constraints[0].ID = "mutated-by-caller"

	second, err := specyaml.Load(context.Background(), fixture("valid_v1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Constraints[0].ID != "literal-value" {
		t.Fatalf("second constraint ID = %q", second.Constraints[0].ID)
	}
}

func TestV1LoadsConcurrentlyWithoutSharedState(t *testing.T) {
	const workers = 32
	results := make(chan specification.Specification, workers)
	errorsFound := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			loaded, err := specyaml.Load(context.Background(), fixture("valid_v1.yaml"))
			if err != nil {
				errorsFound <- err
				return
			}
			results <- loaded
		}()
	}
	group.Wait()
	close(results)
	close(errorsFound)

	for err := range errorsFound {
		t.Errorf("Load() error = %v", err)
	}
	var reference specification.Specification
	for loaded := range results {
		if reference.Version == 0 {
			reference = loaded
			continue
		}
		if !reflect.DeepEqual(loaded, reference) {
			t.Errorf("concurrent Load() = %#v; want %#v", loaded, reference)
		}
	}
}

func TestLoadDoesNotRequireEnvironment(t *testing.T) {
	data, err := os.ReadFile(fixture("valid_v1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("${FEATURE_DESCRIPTION}")) {
		t.Fatal("fixture lost literal environment expression")
	}
	if _, err := specyaml.Load(context.Background(), fixture("valid_v1.yaml")); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}
