package model_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/model"
	"github.com/asierhr/featureProof/internal/model/openai"
	"github.com/asierhr/featureProof/internal/model/openai/oauth"
	"github.com/asierhr/featureProof/internal/policy"
	"github.com/asierhr/featureProof/internal/tools"
)

// integrationClock supplies deterministic OAuth, model and harness time.
type integrationClock struct {
	mu  sync.Mutex
	now time.Time
}

// Now advances deterministic time without sleeping.
func (c *integrationClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Millisecond)
	return c.now
}

// integrationRandom returns fixed distinct OAuth attempt values.
type integrationRandom struct{ values []string }

// Value consumes one controlled OAuth value.
func (r *integrationRandom) Value(int) (string, error) {
	value := r.values[0]
	r.values = r.values[1:]
	return value, nil
}

// integrationGateway implements a hermetic dynamic registration and token exchange.
type integrationGateway struct{ registration oauth.RegistrationRequest }

// Register captures public OAuth fields and returns a fake browser URL.
func (g *integrationGateway) Register(_ context.Context, request oauth.RegistrationRequest) (string, error) {
	g.registration = request
	return "https://auth.invalid/safe", nil
}

// Exchange returns a complete fake credential set.
func (*integrationGateway) Exchange(context.Context, oauth.ExchangeRequest) (oauth.TokenResponse, error) {
	return oauth.TokenResponse{AccessToken: "integration-access", RefreshToken: "integration-refresh", IDToken: "integration-id", Scope: "chatgpt.tokens.use.direct", ExpiresIn: time.Hour}, nil
}

// Refresh is not expected for the fresh integration credential.
func (*integrationGateway) Refresh(context.Context, string, string, string) (oauth.TokenResponse, error) {
	return oauth.TokenResponse{}, errors.New("unexpected refresh")
}

// Revoke is not expected in the inference integration.
func (*integrationGateway) Revoke(context.Context, string, string) error {
	return errors.New("unexpected revoke")
}

// integrationBrowser confirms the browser boundary was reached without external work.
type integrationBrowser struct{ opened bool }

// Open records the fake authorization handoff.
func (b *integrationBrowser) Open(context.Context, string) error { b.opened = true; return nil }

// integrationListener derives a matching callback from the captured attempt.
type integrationListener struct{ gateway *integrationGateway }

// Await returns the issued client and matching state without opening a socket.
func (l integrationListener) Await(context.Context, string) (oauth.Callback, error) {
	return oauth.Callback{State: l.gateway.registration.State, Code: "integration-code", IssuedClientID: "integration-client"}, nil
}

// integrationValidator supplies one stable validated identity.
type integrationValidator struct{}

// Validate maps fake ID tokens to the integration subject.
func (integrationValidator) Validate(context.Context, string, string, string) (oauth.Identity, error) {
	return oauth.Identity{Subject: "integration-subject"}, nil
}

// integrationHTTP captures bearer placement and returns a complete local stream.
type integrationHTTP struct{ calls int }

// Do handles one Responses request without Internet access.
func (h *integrationHTTP) Do(request *http.Request) (*http.Response, error) {
	h.calls++
	if request.URL.String() != openai.Endpoint() || request.Header.Get("Authorization") != "Bearer integration-access" {
		return nil, errors.New("incorrect request")
	}
	body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"{\\\"version\\\":1,\\\"kind\\\":\\\"complete_analysis\\\"}\"}\n\n" + "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n"
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

// integrationObserver captures the safe terminal record.
type integrationObserver struct{ records []model.AttemptRecord }

// Observe records one model attempt without provider payloads.
func (o *integrationObserver) Observe(_ context.Context, record model.AttemptRecord) error {
	o.records = append(o.records, record)
	return nil
}

// integrationIDs supplies evidence IDs even though no tool executes.
type integrationIDs struct{ next int }

// Next returns one deterministic evidence ID.
func (i *integrationIDs) Next() string { i.next++; return fmt.Sprintf("evidence-%d", i.next) }

// integrationWaiter observes cancellation if harness retry policy is invoked.
type integrationWaiter struct{}

// Wait returns immediately and preserves any context failure.
func (integrationWaiter) Wait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// TestOrdinaryModelSuiteUsesOnlyControlledLocalDependencies traces OAuth through the real adapter and harness without external access.
func TestOrdinaryModelSuiteUsesOnlyControlledLocalDependencies(t *testing.T) {
	clock := &integrationClock{now: time.Unix(1000, 0)}
	gateway := &integrationGateway{}
	store, err := oauth.NewLocalStore(filepath.Join(t.TempDir(), "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	browser := &integrationBrowser{}
	service, err := oauth.NewService(oauth.Config{HostID: "integration-host", AppName: "FeatureProof", RedirectURI: "http://127.0.0.1:4567/auth/callback"}, browser, integrationListener{gateway}, gateway, &integrationRandom{values: []string{"state", "nonce", "verifier"}}, clock, integrationValidator{}, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SignIn(context.Background(), "integration-profile"); err != nil {
		t.Fatal(err)
	}
	credentials, err := oauth.NewProfileSource(service, "integration-profile")
	if err != nil {
		t.Fatal(err)
	}
	httpBoundary := &integrationHTTP{}
	transport, err := openai.NewAuthenticatedTransport(credentials, httpBoundary)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := openai.New(transport, openai.Config{MaxRequestBytes: 8192, MaxHeaderBytes: 1024, MaxStreamBytes: 8192, MaxOutputBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	observer := &integrationObserver{}
	session, err := model.NewSession(model.Config{ProviderID: "openai", ModelID: "selected-model", AuthProfileID: "integration-profile", CallTimeout: time.Second, MaxRequestBytes: 8192, MaxStreamBytes: 8192, MaxOutputBytes: 4096, Budget: model.BudgetLimits{MaxCalls: 1, MaxTokens: 100, MaxDuration: time.Minute}}, adapter, clock, observer)
	if err != nil {
		t.Fatal(err)
	}
	registry, _ := tools.NewRegistry()
	storeEvidence, _ := evidence.NewFileStore(t.TempDir())
	redaction, _ := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 1024})
	h, err := harness.New(harness.Config{RunID: "integration-run", Objective: "check adapter", ConstraintID: "integration-constraint", Limits: harness.Limits{MaxSteps: 1}}, harness.Dependencies{Model: session, Registry: registry, Policy: &policy.Static{}, Evidence: storeEvidence, Journal: harness.NewMemoryJournal(), Sanitizer: evidence.NewSanitizer(redaction), Clock: clock, IDs: &integrationIDs{}, Waiter: integrationWaiter{}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := h.Run(context.Background())
	if err != nil || run.Status != harness.StatusCompleted || httpBoundary.calls != 1 || len(observer.records) != 1 || observer.records[0].Usage.Total() != 3 || !browser.opened {
		t.Fatalf("run=%+v calls=%d records=%+v opened=%v error=%v", run, httpBoundary.calls, observer.records, browser.opened, err)
	}
}
