package planning

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/specification"
	httptool "github.com/asierhr/featureProof/internal/tools/http"
)

// integrationResolver pins the synthetic application hostname to the local test server.
type integrationResolver struct{ ip net.IP }

// LookupIPAddr returns only the controlled address used by this integration test.
func (r integrationResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: append(net.IP(nil), r.ip...)}}, nil
}

// integrationDialer delegates the already-authorized pinned socket to the local TCP stack.
type integrationDialer struct{ net.Dialer }

// integrationHTTPClock gives the adapter deterministic duration timestamps independently of executor time.
type integrationHTTPClock struct{ calls int }

// Now advances by one millisecond so returned HTTP duration remains deterministic.
func (c *integrationHTTPClock) Now() time.Time {
	c.calls++
	return time.Unix(0, int64(c.calls)*int64(time.Millisecond))
}

// TestHTTPToolReturnsWithoutMutatingExecutorState proves the adapter returns plain data and the executor alone journals, persists, and evaluates it.
func TestHTTPToolReturnsWithoutMutatingExecutorState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(serverURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	tool, err := httptool.New(httptool.Config{
		Target:         specification.Target{ID: "target", BaseURL: "http://" + net.JoinHostPort("app.test", port)},
		AllowedMethods: []string{"GET"}, AllowedRequestHeaders: []string{}, AllowedResponseHeaders: []string{"Content-Type"}, FollowRedirectsForMethods: []string{},
		MaxRedirects: 0, Timeout: time.Second, MaxURLBytes: 1024, MaxRequestHeaderBytes: 1024,
		MaxRequestBodyBytes: 1024, MaxResponseHeaderBytes: 1024, MaxResponseBodyBytes: 1024,
		Resolver: integrationResolver{ip: net.ParseIP("127.0.0.1")}, Dialer: &integrationDialer{}, Clock: &integrationHTTPClock{}, WithTimeout: context.WithTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	executor, fixture := newExecutorFixture(t, tool.Spec())
	direct, err := tool.Execute(context.Background(), harness.ToolCall{Input: map[string]any{"method": "GET", "path": "health"}})
	if err != nil || direct.Output["status"] != 200 || len(fixture.store.records) != 0 {
		t.Fatalf("tool took executor ownership: result=%#v records=%d err=%v", direct, len(fixture.store.records), err)
	}
	events, err := fixture.journal.Events(context.Background(), fixture.run.ID)
	if err != nil || len(events) != 0 {
		t.Fatalf("tool wrote journal events: %#v %v", events, err)
	}
	step := PlanStep{
		ID: "http", Tool: "http.request",
		Input: map[string]InputValue{
			"method": {Literal: &LiteralValue{Value: "GET"}},
			"path":   {Literal: &LiteralValue{Value: "health"}},
		},
		Assertions: []AssertionDeclaration{
			{ID: "status", Definition: map[string]any{"operator": "eq", "path": "/status", "expected": 200}},
			{ID: "body", Definition: map[string]any{"operator": "eq", "path": "/body/ok", "expected": true}},
		},
	}
	plan := ValidationPlan{ID: "plan", ConstraintID: "constraint", Steps: []PlanStep{step}}
	result, err := executor.Execute(context.Background(), validateForExecution(t, fixture.registry, plan))
	if err != nil || result.Status != StepPassed || len(fixture.store.records) != 1 {
		t.Fatalf("executor did not own returned data: result=%#v records=%d err=%v", result, len(fixture.store.records), err)
	}
	for _, record := range fixture.store.records {
		output, _ := record.Output.(map[string]any)
		if output["status"] != 200 || output["body"].(map[string]any)["ok"] != true {
			t.Fatalf("persisted output cannot support deterministic assertions: %#v", record.Output)
		}
	}
}
