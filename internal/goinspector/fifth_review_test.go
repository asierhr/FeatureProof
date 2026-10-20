package goinspector

import (
	"context"
	"strings"
	"testing"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// countingRepositoryFS records whether validation leaked an invalid request to the repository capability.
type countingRepositoryFS struct{ reads int }

// ReadSnapshot increments the observable capability count and returns one minimal repository.
func (s *countingRepositoryFS) ReadSnapshot(context.Context, SnapshotRequest) (map[string][]byte, error) {
	s.reads++
	return map[string][]byte{"app/main.go": []byte("package app")}, nil
}

// cancellingRepositoryFS crosses the freeze/input phase boundary only after returning complete bytes.
type cancellingRepositoryFS struct{ cancel context.CancelFunc }

// ReadSnapshot cancels the one outer context at the deterministic phase barrier before input sealing.
func (s cancellingRepositoryFS) ReadSnapshot(context.Context, SnapshotRequest) (map[string][]byte, error) {
	s.cancel()
	return map[string][]byte{"app/main.go": []byte("package app\nfunc routes(){}")}, nil
}

// TestCompleteAuthorityValidationPrecedesRepositoryRead proves every configuration class is rejected before capability use.
func TestCompleteAuthorityValidationPrecedesRepositoryRead(t *testing.T) {
	cases := []func(*Configuration){
		func(value *Configuration) { value.RepositoryID = "" },
		func(value *Configuration) { value.Revision = "short" },
		func(value *Configuration) { value.Roots = []string{"../escape"} },
		func(value *Configuration) { value.Build.GOOS = "" },
		func(value *Configuration) { value.EntrySymbols = nil },
		func(value *Configuration) {
			value.Auth = []AuthMatcher{{Identity: AuthIdentity{Kind: AuthIdentityLocal, ImportPath: "app", Symbol: "bad symbol"}}}
		},
		func(value *Configuration) { value.Projection.MaxStringBytes = 0 },
		func(value *Configuration) { value.Limits.MaxFiles = 0 },
	}
	for index, mutate := range cases {
		config := testConfig()
		mutate(&config)
		capability := &countingRepositoryFS{}
		if _, err := FreezeConfiguredRepository(context.Background(), capability, "unused", config); err == nil {
			t.Fatalf("case %d accepted invalid authority", index)
		}
		if capability.reads != 0 {
			t.Fatalf("case %d performed %d repository reads", index, capability.reads)
		}
	}
}

// TestOneDeadlineCancelsBetweenFreezeAndInput proves later phases cannot receive a fresh duration budget.
func TestOneDeadlineCancelsBetweenFreezeAndInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	input, err := FreezeConfiguredRepository(ctx, cancellingRepositoryFS{cancel: cancel}, "unused", testConfig())
	if err == nil || input.digest != "" {
		t.Fatalf("phase cancellation returned input: %#v %v", input, err)
	}
}

// TestCustomJSONMethodsDegradeRequestResponseAndNestedSchemas prevents ordinary-struct claims for custom serialization.
func TestCustomJSONMethodsDegradeRequestResponseAndNestedSchemas(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/main.go": []byte(`package app
import ("encoding/json"; "net/http")
type Custom struct{ Secret string }
func (Custom) MarshalJSON()([]byte,error){ return nil,nil }
func (*Custom) UnmarshalJSON([]byte) error { return nil }
type Envelope struct{ Value Custom ` + "`json:\"value\"`" + ` }
func h(w http.ResponseWriter,r *http.Request){ var in Custom; _=json.NewDecoder(r.Body).Decode(&in); var out Envelope; w.Header().Set("Content-Type","application/json"); _=json.NewEncoder(w).Encode(out) }
func routes(){ http.HandleFunc("POST /x",h) }
`)}, nil)
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	op := result.Snapshot.Operations[0]
	if op.RequestBody != nil {
		t.Fatalf("custom request projected: %+v", op.RequestBody)
	}
	if response := op.Responses["200"]; response.Content["application/json"].Schema.Properties["value"] != nil {
		t.Fatalf("nested custom field projected as ordinary JSON: %+v", response)
	}
	if op.Completeness != contractcatalog.CompletenessPartial {
		t.Fatalf("completeness=%s", op.Completeness)
	}
}

// TestDecodeEncodeUseExactLexicalObjects rejects same-named declarations outside the use scope and accepts the bound one.
func TestDecodeEncodeUseExactLexicalObjects(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/main.go": []byte(`package app
import ("encoding/json"; "net/http")
type Good struct{ Name string }
type Bad struct{ Leak string }
func h(w http.ResponseWriter,r *http.Request){ var body Good; { var body Bad; _=body }; _=json.NewDecoder(r.Body).Decode(&body); var out Good; if true { var out Bad; _=out }; w.Header().Set("Content-Type","application/json"); _=json.NewEncoder(w).Encode(out) }
func routes(){ http.HandleFunc("POST /x",h) }
`)}, nil)
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	op := result.Snapshot.Operations[0]
	if op.RequestBody.Content["application/json"].Schema.Properties["Name"] == nil || op.RequestBody.Content["application/json"].Schema.Properties["Leak"] != nil {
		t.Fatalf("request object mismatch: %+v", op.RequestBody)
	}
	response := op.Responses["200"].Content["application/json"].Schema
	if response.Properties["Name"] == nil || response.Properties["Leak"] != nil {
		t.Fatalf("response object mismatch: %+v", response)
	}
}

// TestResponseInterpreterPreservesUnsupportedStatusesAndFirstCommit verifies conservative control flow and net/http commit semantics.
func TestResponseInterpreterPreservesUnsupportedStatusesAndFirstCommit(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/main.go": []byte(`package app
import ("encoding/json"; "net/http")
type Reply struct{ OK bool }
func h(w http.ResponseWriter,r *http.Request){ switch r.Method { case "x": w.WriteHeader(400) }; for false { w.WriteHeader(401) }; select { default: w.WriteHeader(403) }; w.Header().Set("Content-Type","application/json"); w.WriteHeader(201); w.WriteHeader(202); w.Header().Set("Content-Type","text/plain"); var out Reply; _=json.NewEncoder(w).Encode(out) }
func routes(){ http.HandleFunc("GET /x",h) }
`)}, nil)
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	responses := result.Snapshot.Operations[0].Responses
	for _, status := range []string{"400", "401", "403", "201"} {
		if _, ok := responses[status]; !ok {
			t.Fatalf("missing explicit status %s: %+v", status, responses)
		}
	}
	if _, ok := responses["202"]; ok {
		t.Fatalf("second commit changed status: %+v", responses)
	}
	if responses["201"].Content["application/json"].Schema == nil {
		t.Fatalf("pre-commit JSON header lost: %+v", responses["201"])
	}
}

// TestClaimProvenanceSeparatesParametersBodiesAndStatuses verifies unrelated facts never share one coarse provenance list.
func TestClaimProvenanceSeparatesParametersBodiesAndStatuses(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/main.go": []byte(`package app
import ("encoding/json"; "net/http")
type Body struct{ Name string }
func h(w http.ResponseWriter,r *http.Request){
 _=r.PathValue("id")
 _=r.URL.Query().Get("q")
 var body Body
 _=json.NewDecoder(r.Body).Decode(&body)
 if r.Method=="x" { w.WriteHeader(400); return }
 w.WriteHeader(201)
}
func routes(){ http.HandleFunc("POST /x/{id}",h) }
`)}, nil)
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]string{}
	for _, claim := range result.Snapshot.Operations[0].Claims {
		locations := []string{}
		for _, provenance := range claim.Provenance {
			locations = append(locations, provenance.SafeLocation)
		}
		fields[claim.Field] = strings.Join(locations, "|")
	}
	if strings.Contains(fields["parameters/0"], "/decode") || strings.Contains(fields["request_body"], "/query") {
		t.Fatalf("parameter/body provenance crossed: %+v", fields)
	}
	if fields["responses/400"] == fields["responses/201"] || !strings.Contains(fields["responses/400"], "/status") {
		t.Fatalf("status provenance collapsed: %+v", fields)
	}
}

// TestUnavailableImplicitImportCannotAuthorizeMiddlewareButExplicitAliasCan separates unresolved package names from syntax-owned aliases.
func TestUnavailableImplicitImportCannotAuthorizeMiddlewareButExplicitAliasCan(t *testing.T) {
	fixture := func(importLine string) Input {
		return sealedInput(t, map[string][]byte{"app/main.go": []byte("package app\nimport (\"net/http\"; \"github.com/go-chi/chi/v5\"; " + importLine + ")\nfunc h(w http.ResponseWriter,r *http.Request){w.WriteHeader(204)}\nfunc routes(r chi.Router){ r.Use(missing.Wrap); r.Get(\"/x\",h) }\n")}, func(config *Configuration) {
			config.Auth = []AuthMatcher{{Identity: AuthIdentity{Kind: AuthIdentityExternal, ImportPath: "example/not-the-package-name", Alias: "missing", Symbol: "Wrap"}, Scheme: contractcatalog.SecurityScheme{Name: "session", Type: "http", Scheme: "bearer"}}}
		})
	}
	implicit, err := (Inspector{}).Inspect(context.Background(), fixture("\"example/missing\""), "source")
	if err == nil && len(implicit.Snapshot.Operations) > 0 && len(implicit.Snapshot.Operations[0].Security) > 0 {
		t.Fatal("implicit unavailable import authorized middleware")
	}
	explicit, err := (Inspector{}).Inspect(context.Background(), fixture("missing \"example/not-the-package-name\""), "source")
	if err != nil {
		t.Fatal(err)
	}
	if len(explicit.Snapshot.Operations) != 1 || len(explicit.Snapshot.Operations[0].Security) != 1 {
		t.Fatalf("explicit alias not preserved: %+v", explicit.Snapshot.Operations)
	}
}
