package localvalidation

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPackageV1StableFieldsAndExplicitSections(t *testing.T) {
	index := PackageIndex{Version: PackageVersion, RunID: "run", Constraints: []PackageConstraint{}, Failures: []PackageFailure{}, Facts: []PackageFact{}, Evidence: []PackageEvidence{}, Reproduction: []PackageReproduction{}}
	raw, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"version":1`, `"run_id":"run"`, `"constraints":[]`, `"failures":[]`, `"facts":[]`, `"evidence":[]`, `"reproduction":[]`} {
		if !strings.Contains(string(raw), field) {
			t.Fatalf("missing %s: %s", field, raw)
		}
	}
}

func TestPackageV1RejectsVersionAmbiguousAndIncompleteShapes(t *testing.T) {
	valid := `{"version":1,"run_id":"run","binding":{},"plan_bindings":[],"status":"inconclusive","constraints":[],"failures":[],"facts":[],"evidence":[],"reproduction":[]}`
	for _, tc := range []struct{ raw, category string }{
		{strings.Replace(valid, `"version":1`, `"version":2`, 1), "unsupported_version"},
		{strings.Replace(valid, `"run_id":"run"`, `"run_id":"run","run_id":"other"`, 1), "invalid_schema"},
		{strings.Replace(valid, `"facts":[]`, `"unexpected":0,"facts":[]`, 1), "invalid_schema"},
		{strings.Replace(valid, `"facts":[],`, "", 1), "invalid_schema"},
		{strings.Replace(valid, `"plan_bindings":[]`, `"plan_bindings":null`, 1), "invalid_schema"},
	} {
		if _, err := decodePackageIndex([]byte(tc.raw)); !IsPackageError(err, tc.category) {
			t.Fatalf("category=%s err=%v", tc.category, err)
		}
	}
}
