package goinspector

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// TestRequestFactsRequireExactParameterObject rejects every same-named shadow for path, query and JSON decode facts.
func TestRequestFactsRequireExactParameterObject(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/main.go": []byte(`package app
import ("encoding/json"; "net/http"; "github.com/go-chi/chi/v5")
type Body struct{ Name string }
type fakeRequest struct{ URL *http.Request; Body any }
func h(w http.ResponseWriter,r *http.Request){
 { r := &fakeRequest{}; _=r.PathValue("id") }
 { var r fakeRequest; _=r.URL.Query().Get("q") }
 { r := &fakeRequest{}; var body Body; _=json.NewDecoder(r.Body).Decode(&body) }
 { r := &fakeRequest{}; _=chi.URLParam(r,"id") }
 w.WriteHeader(204)
}
func routes(){ http.HandleFunc("POST /x/{id}",h) }
`)}, nil)
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	op := result.Snapshot.Operations[0]
	if len(op.Parameters) != 0 || op.RequestBody != nil {
		t.Fatalf("shadowed request emitted claims: %+v", op)
	}
	for _, claim := range op.Claims {
		if strings.HasPrefix(claim.Field, "parameters/") || strings.HasPrefix(claim.Field, "request_body/content/") {
			t.Fatalf("shadowed request claim survived: %+v", claim)
		}
	}
}

// TestAuthIdentityAndProvenanceArePerMatcher proves canonical local identity, explicit alias identity and negative provenance separation.
func TestAuthIdentityAndProvenanceArePerMatcher(t *testing.T) {
	files := map[string][]byte{
		"go.mod": []byte("module example.test/service\n"),
		"app/routes.go": []byte(`package app
import ("net/http"; "github.com/go-chi/chi/v5"; left "example.test/service/security/a"; right "example.test/service/security/b"; ext "outside.test/not-real"; nope "outside.test/nope")
func h(w http.ResponseWriter,r *http.Request){w.WriteHeader(204)}
func routes(r chi.Router){
 r.Use(left.Require)
 r.Use(right.Require)
 r.Use(nope.Skip)
 r.With(ext.Check).Get("/x",h)
}`),
		"security/a/auth.go": []byte("package auth\nfunc Require(next any)any{return next}"),
		"security/b/auth.go": []byte("package auth\nfunc Require(next any)any{return next}"),
	}
	input := sealedInput(t, files, func(config *Configuration) {
		config.Roots = []string{"app", "security"}
		config.ChangedPaths = []string{"security/a/auth.go"}
		config.Auth = []AuthMatcher{
			{Identity: AuthIdentity{Kind: AuthIdentityLocal, ImportPath: "example.test/service/security/a", Symbol: "Require"}, Scheme: contractcatalog.SecurityScheme{Name: "left", Type: "http", Scheme: "bearer"}},
			{Identity: AuthIdentity{Kind: AuthIdentityExternal, ImportPath: "outside.test/not-real", Alias: "ext", Symbol: "Check"}, Scheme: contractcatalog.SecurityScheme{Name: "external", Type: "apiKey", In: "header"}},
		}
	})
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	op := result.Snapshot.Operations[0]
	if len(op.Security) != 2 || len(result.AffectedOperations) != 1 {
		t.Fatalf("auth or impact mismatch: %+v %+v", op.Security, result.AffectedOperations)
	}
	locations := map[string]string{}
	for _, claim := range op.Claims {
		if claim.Kind != contractcatalog.ClaimSecurity {
			continue
		}
		parts := []string{}
		for _, provenance := range claim.Provenance {
			parts = append(parts, provenance.SafeLocation)
		}
		locations[claim.Field] = strings.Join(parts, "|")
	}
	if !strings.Contains(locations["security_schemes/left"], "security/a/auth.go") || strings.Contains(locations["security_schemes/left"], "security/b/auth.go") {
		t.Fatalf("left provenance crossed: %+v", locations)
	}
	if strings.Contains(locations["security_schemes/external"], "security/a/auth.go") || !strings.Contains(locations["security_schemes/external"], "middleware-application") {
		t.Fatalf("external provenance crossed: %+v", locations)
	}
	if _, exists := op.SecuritySchemes["right"]; exists {
		t.Fatal("same declared package in another directory was authorized")
	}
}

// TestSecurityAbsenceClaimDoesNotInheritUnmatchedMiddleware proves an unallowlisted application cannot become absence provenance.
func TestSecurityAbsenceClaimDoesNotInheritUnmatchedMiddleware(t *testing.T) {
	input := sealedInput(t, map[string][]byte{"app/main.go": []byte(`package app
import ("net/http"; "github.com/go-chi/chi/v5"; nope "outside.test/nope")
func h(w http.ResponseWriter,r *http.Request){w.WriteHeader(204)}
func routes(r chi.Router){r.Use(nope.Skip);r.Get("/x",h)}
`)}, func(config *Configuration) {
		config.Auth = []AuthMatcher{{Identity: AuthIdentity{Kind: AuthIdentityExternal, ImportPath: "outside.test/other", Alias: "other", Symbol: "Check"}, Scheme: contractcatalog.SecurityScheme{Name: "other", Type: "http"}}}
	})
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	op := result.Snapshot.Operations[0]
	if len(op.Security) != 0 {
		t.Fatalf("unmatched middleware authorized: %+v", op.Security)
	}
	for _, claim := range op.Claims {
		if claim.Field != "security" {
			continue
		}
		for _, provenance := range claim.Provenance {
			if strings.Contains(provenance.SafeLocation, "middleware-application") {
				t.Fatalf("absence inherited middleware provenance: %+v", claim)
			}
		}
	}
}

// TestAuthConfigurationRejectsCollisionsAndOwnsItsDigest proves canonical matcher authority is immutable and unambiguous.
func TestAuthConfigurationRejectsCollisionsAndOwnsItsDigest(t *testing.T) {
	snapshot, err := NewSnapshot("repo", testRevision, map[string][]byte{"app/main.go": []byte("package app\nfunc routes(){}")})
	if err != nil {
		t.Fatal(err)
	}
	config := testConfig()
	config.Auth = []AuthMatcher{{Identity: AuthIdentity{Kind: AuthIdentityLocal, ImportPath: "app", Symbol: "Require"}, Scheme: contractcatalog.SecurityScheme{Name: "one", Type: "http"}}}
	input, err := newInput(snapshot, config, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	digest := input.Digest()
	config.Auth[0].Identity.Symbol = "Other"
	config.Auth[0].Scheme.Name = "changed"
	if input.Digest() != digest || input.config.Auth[0].Identity.Symbol != "Require" || input.config.Auth[0].Scheme.Name != "one" {
		t.Fatal("caller mutated sealed auth authority")
	}
	for _, collision := range [][]AuthMatcher{
		{{Identity: AuthIdentity{Kind: AuthIdentityLocal, ImportPath: "app", Symbol: "Require"}, Scheme: contractcatalog.SecurityScheme{Name: "one", Type: "http"}}, {Identity: AuthIdentity{Kind: AuthIdentityLocal, ImportPath: "app", Symbol: "Require"}, Scheme: contractcatalog.SecurityScheme{Name: "two", Type: "http"}}},
		{{Identity: AuthIdentity{Kind: AuthIdentityExternal, ImportPath: "outside.test/auth", Alias: "auth", Symbol: "Require"}, Scheme: contractcatalog.SecurityScheme{Name: "one", Type: "http"}}, {Identity: AuthIdentity{Kind: AuthIdentityExternal, ImportPath: "outside.test/auth", Alias: "auth", Symbol: "Require"}, Scheme: contractcatalog.SecurityScheme{Name: "two", Type: "http"}}},
		{{Identity: AuthIdentity{Kind: AuthIdentityLocal, ImportPath: "app", Symbol: "Require"}, Scheme: contractcatalog.SecurityScheme{Name: "one", Type: "http"}}, {Identity: AuthIdentity{Kind: AuthIdentityLocal, ImportPath: "app", Symbol: "Other"}, Scheme: contractcatalog.SecurityScheme{Name: "one", Type: "http"}}},
	} {
		invalid := testConfig()
		invalid.Auth = collision
		if _, err := newInput(snapshot, invalid, time.Time{}); err == nil {
			t.Fatalf("auth collision accepted: %+v", collision)
		}
	}
	separate := testConfig()
	separate.Auth = []AuthMatcher{
		{Identity: AuthIdentity{Kind: AuthIdentityLocal, ImportPath: "outside.test/auth", Symbol: "Require"}, Scheme: contractcatalog.SecurityScheme{Name: "local", Type: "http"}},
		{Identity: AuthIdentity{Kind: AuthIdentityExternal, ImportPath: "outside.test/auth", Alias: "auth", Symbol: "Require"}, Scheme: contractcatalog.SecurityScheme{Name: "external", Type: "http"}},
	}
	if _, err := newInput(snapshot, separate, time.Time{}); err != nil {
		t.Fatalf("domain-separated identities collided: %v", err)
	}
}

// TestInspectionContextChoosesShortestDeadline uses an explicit clock and barriers without sleeping.
func TestInspectionContextChoosesShortestDeadline(t *testing.T) {
	now := time.Now()
	longParent, cancelLong := context.WithDeadline(context.Background(), now.Add(10*time.Second))
	defer cancelLong()
	shortBudget, cancelBudget := inspectionContextAt(longParent, time.Second, now)
	defer cancelBudget()
	deadline, _ := shortBudget.Deadline()
	if !deadline.Equal(now.Add(time.Second)) {
		t.Fatalf("configured maximum did not win: %v", deadline)
	}
	shortParent, cancelParent := context.WithDeadline(context.Background(), now.Add(500*time.Millisecond))
	defer cancelParent()
	parentBudget, cancelChild := inspectionContextAt(shortParent, time.Second, now)
	defer cancelChild()
	deadline, _ = parentBudget.Deadline()
	if !deadline.Equal(now.Add(500 * time.Millisecond)) {
		t.Fatalf("parent deadline did not win: %v", deadline)
	}
	expired, cancelExpired := inspectionContextAt(context.Background(), time.Millisecond, now)
	defer cancelExpired()
	<-expired.Done()
	if expired.Err() != context.DeadlineExceeded {
		t.Fatalf("phase barrier err=%v", expired.Err())
	}
}
