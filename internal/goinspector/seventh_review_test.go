package goinspector

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// fixedInspectionClock returns one future wall instant chosen by the test before orchestration starts.
type fixedInspectionClock struct{ now time.Time }

// Now returns the sealed outer instant without consulting environment time again.
func (c fixedInspectionClock) Now() time.Time { return c.now }

// TestTypedAuthIdentitySeparatesLocalAndExternalDomains rejects equal textual selectors across identity domains.
func TestTypedAuthIdentitySeparatesLocalAndExternalDomains(t *testing.T) {
	input := sealedInput(t, map[string][]byte{
		"go.mod": []byte("module ext\n"),
		"app/main.go": []byte(`package app
import ("net/http"; "github.com/go-chi/chi/v5"; ext "outside.test/missing")
func Require(next any)any{return next}
func h(w http.ResponseWriter,r *http.Request){w.WriteHeader(204)}
func routes(r chi.Router){
 r.Use(Require)
 r.Use(ext.Require)
 r.Get("/x",h)
}`),
	}, func(config *Configuration) {
		config.Auth = []AuthMatcher{{Identity: AuthIdentity{Kind: AuthIdentityLocal, ImportPath: "ext/app", Symbol: "Require"}, Scheme: contractcatalog.SecurityScheme{Name: "local", Type: "http"}}}
	})
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	op := result.Snapshot.Operations[0]
	if len(op.Security) != 1 || op.SecuritySchemes["local"].Name == "" {
		t.Fatalf("typed local identity mismatch: %+v", op.Security)
	}
	for _, claim := range op.Claims {
		if claim.Field != "security_schemes/local" {
			continue
		}
		joined := ""
		for _, provenance := range claim.Provenance {
			joined += provenance.SafeLocation + "|"
		}
		if !strings.Contains(joined, "/line-6/") || strings.Contains(joined, "/line-7/") {
			t.Fatalf("external textual collision entered provenance: %s", joined)
		}
	}
}

// TestTypedExternalIdentityIncludesLiteralImportPath separates equal aliases and symbols in different files and scopes.
func TestTypedExternalIdentityIncludesLiteralImportPath(t *testing.T) {
	input := sealedInput(t, map[string][]byte{
		"app/root.go": []byte("package app\nimport \"github.com/go-chi/chi/v5\"\nfunc routes(r chi.Router){buildA(r);buildB(r)}"),
		"app/a.go":    []byte("package app\nimport (\"net/http\";\"github.com/go-chi/chi/v5\";ext \"outside.test/a\")\nfunc ha(w http.ResponseWriter,r *http.Request){}\nfunc buildA(r chi.Router){r.Use(ext.Require);r.Get(\"/a\",ha)}"),
		"app/b.go":    []byte("package app\nimport (\"net/http\";\"github.com/go-chi/chi/v5\";ext \"outside.test/b\")\nfunc hb(w http.ResponseWriter,r *http.Request){}\nfunc buildB(r chi.Router){r.Use(ext.Require);r.Get(\"/b\",hb)}"),
	}, func(config *Configuration) {
		config.Auth = []AuthMatcher{{Identity: AuthIdentity{Kind: AuthIdentityExternal, ImportPath: "outside.test/a", Alias: "ext", Symbol: "Require"}, Scheme: contractcatalog.SecurityScheme{Name: "a", Type: "http"}}}
	})
	result, err := (Inspector{}).Inspect(context.Background(), input, "source")
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range result.Snapshot.Operations {
		if operation.Key.Path == "/a" && len(operation.Security) != 1 {
			t.Fatalf("exact external matcher missing: %+v", operation)
		}
		if operation.Key.Path == "/b" && len(operation.Security) != 0 {
			t.Fatalf("other import path authorized: %+v", operation)
		}
	}
}

// TestSealedAbsoluteDeadlineCannotRestartAfterFreeze waits on the real future barrier and then rejects registry inspection.
func TestSealedAbsoluteDeadlineCannotRestartAfterFreeze(t *testing.T) {
	now := time.Now()
	config := testConfig()
	config.Limits.MaxDuration = 100 * time.Millisecond
	input, err := FreezeConfiguredRepositoryWithClock(context.Background(), mutatingRepositoryFS{files: map[string][]byte{"app/main.go": []byte("package app\nfunc routes(){}")}}, "unused", config, fixedInspectionClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	deadline, exists := inputDeadline(input)
	if !exists || !deadline.Equal(now.Add(config.Limits.MaxDuration)) {
		t.Fatalf("deadline not sealed: %v %v", deadline, exists)
	}
	barrier, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	<-barrier.Done()
	_, err = (Inspector{}).Inspect(context.Background(), input, "source")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("inspection restarted budget: %v", err)
	}
	request := SourceRequest("source", input, contractcatalog.Limits{MaxSources: 1, MaxOperations: 10, MaxClaims: 100, MaxBytes: 1 << 20, MaxDepth: 10, MaxDiagnostics: 10, MaxLocationBytes: 256}, config.Projection)
	_, err = (DerivedSource{}).Load(context.Background(), request)
	var catalogError *contractcatalog.Error
	if !errors.As(err, &catalogError) || catalogError.Category != contractcatalog.CategoryCancelled {
		t.Fatalf("registry did not preserve deadline category: %v", err)
	}
}

// TestFreezeSealsTheShorterOuterDeadline verifies both parent-shorter and configured-shorter production paths.
func TestFreezeSealsTheShorterOuterDeadline(t *testing.T) {
	now := time.Now()
	for _, scenario := range []struct {
		name            string
		parentDuration  time.Duration
		maximumDuration time.Duration
		expected        time.Duration
	}{
		{name: "parent", parentDuration: 2 * time.Second, maximumDuration: 4 * time.Second, expected: 2 * time.Second},
		{name: "configured", parentDuration: 4 * time.Second, maximumDuration: 2 * time.Second, expected: 2 * time.Second},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			parent, cancel := context.WithDeadline(context.Background(), now.Add(scenario.parentDuration))
			defer cancel()
			config := testConfig()
			config.Limits.MaxDuration = scenario.maximumDuration
			input, err := FreezeConfiguredRepositoryWithClock(parent, mutatingRepositoryFS{files: map[string][]byte{"app/main.go": []byte("package app\nfunc routes(){}")}}, "unused", config, fixedInspectionClock{now: now})
			if err != nil {
				t.Fatal(err)
			}
			deadline, exists := inputDeadline(input)
			if !exists || !deadline.Equal(now.Add(scenario.expected)) {
				t.Fatalf("sealed deadline=%v exists=%v", deadline, exists)
			}
		})
	}
}

// TestSealedDeadlineExpiresInsideEveryInspectionPhase proves registry execution cannot mint a new phase budget.
func TestSealedDeadlineExpiresInsideEveryInspectionPhase(t *testing.T) {
	for _, target := range []string{"index", "routes", "projection"} {
		t.Run(target, func(t *testing.T) {
			now := time.Now()
			config := testConfig()
			config.Limits.MaxDuration = 100 * time.Millisecond
			input, err := FreezeConfiguredRepositoryWithClock(context.Background(), mutatingRepositoryFS{files: map[string][]byte{"app/main.go": []byte("package app\nimport \"net/http\"\nfunc h(http.ResponseWriter,*http.Request){}\nfunc routes(){http.HandleFunc(\"GET /x\",h)}")}}, "unused", config, fixedInspectionClock{now: now})
			if err != nil {
				t.Fatal(err)
			}
			inspector := Inspector{phaseBarrier: func(ctx context.Context, stage string) {
				if stage == target {
					<-ctx.Done()
				}
			}}
			request := SourceRequest("source", input, contractcatalog.Limits{MaxSources: 1, MaxOperations: 10, MaxClaims: 100, MaxBytes: 1 << 20, MaxDepth: 10, MaxDiagnostics: 10, MaxLocationBytes: 256}, config.Projection)
			_, err = (DerivedSource{Inspector: inspector}).Load(context.Background(), request)
			var catalogError *contractcatalog.Error
			if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &catalogError) || catalogError.Category != contractcatalog.CategoryCancelled {
				t.Fatalf("phase %s restarted or lost deadline: %v", target, err)
			}
		})
	}
}

// inputDeadline exposes only the sealed timestamp to same-package budget tests without retaining a context.
func inputDeadline(input Input) (time.Time, bool) {
	return input.deadlineAt, !input.deadlineAt.IsZero()
}
