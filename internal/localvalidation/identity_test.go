package localvalidation

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/revisionbinding"
	"github.com/asierhr/featureProof/internal/testidentity"
)

// TestPlanningInputValueExposesLiteralPayloadToExecutableValidation proves generated literals cross the descriptor boundary as their JSON values.
func TestPlanningInputValueExposesLiteralPayloadToExecutableValidation(t *testing.T) {
	value := planningInputValue(planning.InputValue{Literal: &planning.LiteralValue{Value: "GET"}})
	if value != "GET" {
		t.Fatalf("literal boundary value=%#v", value)
	}
}

// localCredentialResolver returns one controlled lease and records opaque-reference resolution count.
type localCredentialResolver struct {
	mu    sync.Mutex
	calls int
}

// Resolve mints a fresh attempt-owned canary lease without exposing it to runner state.
func (r *localCredentialResolver) Resolve(_ context.Context, request testidentity.ResolutionRequest) (testidentity.CredentialLease, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return testidentity.NewCredentialLease([]byte("CANARY-LOCAL-IDENTITY"), request.Mechanism, time.Time{}, nil), nil
}

// localIdentityObserver captures only payload-free terminal identity facts.
type localIdentityObserver struct {
	mu      sync.Mutex
	records []testidentity.AttemptRecord
}

// Observe appends one safe terminal attempt record for vertical workflow assertions.
func (o *localIdentityObserver) Observe(_ context.Context, record testidentity.AttemptRecord) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.records = append(o.records, record)
	return nil
}

// writeIdentityInputs creates compatible specification/manifest v2 files with one explicit authenticated operation.
func writeIdentityInputs(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	spec := "version: 2\nfeature:\n  id: feature\n  description: local\ntarget:\n  id: deployed\n  base_url: https://unused.example\nconstraints:\n  - id: health\n    description: responds\n    target_id: deployed\nlimits:\n  max_steps: 2\n  max_tool_calls: 1\n  max_retries: 0\nidentities:\n  - id: customer\n    target_id: deployed\n    credential_ref: cred_customer\n    mechanism:\n      type: bearer\n    operations:\n      - capability: http.request\n        method: GET\n        path: /\n"
	manifest := `{"version":2,"runtime":{"http":{"allowed_methods":["GET"],"allowed_request_headers":[],"allowed_response_headers":["Content-Type"],"follow_redirects_for_methods":[],"max_redirects":0,"timeout_ms":1000,"max_url_bytes":2048,"max_request_header_bytes":8192,"max_request_body_bytes":1024,"max_response_header_bytes":8192,"max_response_body_bytes":1024},"redaction":{"max_body_bytes":1024,"additional_sensitive_headers":[],"sensitive_json_paths":[],"model_sensitive_json_paths":[]},"policy":{"allowed_tools":["http.request"]}},"plans":[{"id":"health-plan","constraint_id":"health","steps":[{"id":"request","tool":"http.request","identity_id":"customer","input":{"method":{"literal":"GET"},"path":{"literal":"/"}},"assertions":[{"id":"status","definition":{"operator":"eq","path":"/status","expected":200}}],"depends_on":[]}]}]}`
	specPath, planPath := filepath.Join(root, "spec.yaml"), filepath.Join(root, "plan.json")
	if err := os.WriteFile(specPath, []byte(spec), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	return specPath, planPath
}

// TestIdentityAwareLocalCompositionFailsClosedAndExecutesWithoutPersistingSecret proves resolver wiring, late auth, and durable redaction.
func TestIdentityAwareLocalCompositionFailsClosedAndExecutesWithoutPersistingSecret(t *testing.T) {
	seen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		seen <- request.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"CANARY-LOCAL-IDENTITY","ok":true}`))
	}))
	defer server.Close()
	specPath, planPath := writeIdentityInputs(t)
	root := t.TempDir()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	deps := DefaultDependencies(root, stdout, stderr)
	commit := strings.Repeat("a", 40)
	deps.RevisionResolver = fakeRevision{revisionbinding.Revision{RepositoryID: "repo", CommitOID: commit}}
	deps.EnvironmentAttestor = fakeAttestation{revisionbinding.Attestation{EnvironmentID: "preview", RepositoryID: "repo", CommitOID: commit, Reference: "proof", ValidUntil: time.Unix(10000, 0), Verified: true}}
	deps.SafeProjector, deps.Clock, deps.IDs, deps.Waiter, deps.FinalizeTimeout = fixtureProjector{}, &testClock{next: time.Unix(100, 0)}, &testIDs{}, testWaiter{}, 10*time.Second
	request := Request{SpecPath: specPath, PlanPath: planPath, Target: server.URL, RepositoryID: "repo", Revision: "main", EnvironmentID: "preview"}
	if _, err := NewRunner(context.Background(), request, deps); err == nil {
		t.Fatal("missing credential composition accepted")
	}
	resolver, observer := &localCredentialResolver{}, &localIdentityObserver{}
	deps.CredentialResolvers, deps.IdentityObserver = map[string]testidentity.CredentialResolver{"cred_customer": resolver}, observer
	runner, err := NewRunner(context.Background(), request, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code := runner.Run(context.Background()); code != 0 {
		t.Fatalf("run code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if got := <-seen; got != "Bearer CANARY-LOCAL-IDENTITY" {
		t.Fatalf("auth=%q", got)
	}
	all := ""
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		raw, readErr := os.ReadFile(path)
		all += string(raw)
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(all, "CANARY-LOCAL-IDENTITY") || strings.Contains(all, "cred_customer") {
		t.Fatalf("secret or credential reference persisted: %s", fmt.Sprintf("%d bytes", len(all)))
	}
	if resolver.calls != 1 || len(observer.records) != 1 || observer.records[0].IdentityID != "customer" || observer.records[0].RunID != "id-1" {
		t.Fatalf("resolver/observer mismatch: calls=%d records=%#v", resolver.calls, observer.records)
	}
}

// TestIdentityAssertionRedactionIsSharedBeforeRunnerComposition proves explicit manifests cannot assert on authentication locations before any credential resolution.
func TestIdentityAssertionRedactionIsSharedBeforeRunnerComposition(t *testing.T) {
	specPath, planPath := writeIdentityInputs(t)
	raw, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`"path":"/status"`), []byte(`"path":"/body/token"`), 1)
	if err := os.WriteFile(planPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	resolver := &localCredentialResolver{}
	deps := DefaultDependencies(t.TempDir(), &bytes.Buffer{}, &bytes.Buffer{})
	deps.CredentialResolvers = map[string]testidentity.CredentialResolver{"cred_customer": resolver}
	deps.IdentityObserver = &localIdentityObserver{}
	request := Request{SpecPath: specPath, PlanPath: planPath, Target: "https://target.test", RepositoryID: "repo", Revision: "main", EnvironmentID: "preview"}
	if _, err := NewRunner(context.Background(), request, deps); err == nil {
		t.Fatal("assertion over identity authentication path was accepted")
	}
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if resolver.calls != 0 {
		t.Fatalf("credential resolver called %d times before assertion rejection", resolver.calls)
	}
}
