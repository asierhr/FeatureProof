package agentplanning

import (
	"encoding/json"
	"errors"
	"testing"
)

// TestPlanningProtocolV1StrictDecodeAndSnapshot proves the planning union rejects ambiguous JSON and preserves exact numbers.
func TestPlanningProtocolV1StrictDecodeAndSnapshot(t *testing.T) {
	valid := `{"version":1,"kind":"insufficient_context","missing_context":{"items":[{"kind":"question","constraint_ids":["c1"],"request":"which status is expected?"}]}}`
	decision, err := Decode([]byte(valid), 4096)
	if err != nil || decision.Kind != DecisionInsufficientContext || len(decision.MissingContext.Items) != 1 {
		t.Fatalf("decode valid missing context: %#v %v", decision, err)
	}
	cases := []string{
		`{"version":2,"kind":"insufficient_context","missing_context":{"items":[]}}`,
		`{"version":1,"kind":"other"}`,
		`{"version":1,"kind":"plan","plan":{"id":"p","scenarios":[],"checks":[]},"missing_context":{"items":[]}}`,
		`{"version":1,"kind":"plan"}`,
		`{"version":1,"version":1,"kind":"plan"}`,
		`{"version":1,"kind":"plan","extra":true}`,
		`{"version":1,"kind":"plan"}{}`,
	}
	for _, raw := range cases {
		_, err := Decode([]byte(raw), 4096)
		var typed *Error
		if !errors.As(err, &typed) || typed.Category != CategoryInvalidModelResponse {
			t.Fatalf("expected invalid response for %s: %v", raw, err)
		}
	}
	raw := `{"version":1,"kind":"plan","plan":{"id":"p","scenarios":[{"constraint_id":"c1","scenario":"positive","status":"planned"}],"checks":[{"id":"x","constraint_id":"c1","scenario":"positive","capability":"http.request","steps":[{"id":"s","tool":"http.request","input":{"number":{"literal":{"value":9007199254740993}}},"assertions":[{"id":"a","definition":{"operator":"exists"}}]}],"evidence":[{"step_id":"s","assertion_ids":["a"]}]}]}}`
	decision, err = Decode([]byte(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	n := decision.Plan.Checks[0].Steps[0].Input["number"].Literal.Value
	if n.(json.Number).String() != "9007199254740993" {
		t.Fatalf("number lost precision: %v", n)
	}
}

// TestDecodeEnforcesOutputBound proves oversized output is rejected before allocation-heavy traversal.
func TestDecodeEnforcesOutputBound(t *testing.T) {
	_, err := Decode([]byte(`{"version":1}`), 5)
	var typed *Error
	if !errors.As(err, &typed) || typed.Category != CategoryLimitExceeded {
		t.Fatalf("expected limit error, got %v", err)
	}
}
