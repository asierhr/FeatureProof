//go:build postgres_integration

package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/asierhr/featureProof/internal/controlplane/contracttest"
	controlplanepg "github.com/asierhr/featureProof/internal/controlplane/postgres"
	"github.com/asierhr/featureProof/internal/correction"
	"github.com/asierhr/featureProof/internal/environment"
	"github.com/asierhr/featureProof/internal/githubpr"
	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/validationworkflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// controlPlaneRejectWriter injects a terminal output failure after a product
// operation has completed so command boundaries remain fail closed.
type controlPlaneRejectWriter struct{}

// Write rejects every encoded result without retaining operational data.
func (controlPlaneRejectWriter) Write([]byte) (int, error) {
	return 0, errors.New("output unavailable")
}

// TestOperationalControlPlaneCommandsUseAuthorizedPostgres exercises the real
// executable controllers, including process identity, migration, readiness,
// bounded query and explicit retention without any local fallback.
func TestOperationalControlPlaneCommandsUseAuthorizedPostgres(t *testing.T) {
	dsn := os.Getenv("FEATUREPROOF_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Fatal("FEATUREPROOF_POSTGRES_TEST_DSN is required")
	}
	operator, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	project := "cmd-operations-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	for name, value := range map[string]string{
		"FEATUREPROOF_CONTROL_PLANE_POSTGRES_DSN":          dsn,
		"FEATUREPROOF_CONTROL_PLANE_IDENTITY":              "cmd-operations",
		"FEATUREPROOF_CONTROL_PLANE_OPERATOR_UID":          operator.Uid,
		"FEATUREPROOF_CONTROL_PLANE_CURSOR_SECRET":         strings.Repeat("c", 32),
		"FEATUREPROOF_CONTROL_PLANE_QUERY_PROJECT_ID":      project,
		"FEATUREPROOF_CONTROL_PLANE_QUERY_STATE":           "",
		"FEATUREPROOF_CONTROL_PLANE_QUERY_CURSOR":          "",
		"FEATUREPROOF_CONTROL_PLANE_QUERY_LIMIT":           "10",
		"FEATUREPROOF_CONTROL_PLANE_RETENTION_PROJECT_ID":  project,
		"FEATUREPROOF_CONTROL_PLANE_RETENTION_BEFORE_UNIX": strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
		"FEATUREPROOF_CONTROL_PLANE_RETENTION_BATCH_SIZE":  "10",
	} {
		t.Setenv(name, value)
	}
	var output, diagnostics bytes.Buffer
	if code := runControlPlaneJobs(context.Background(), &output, &diagnostics); code != 0 || !strings.Contains(output.String(), `"Jobs":[]`) {
		t.Fatalf("jobs code=%d output=%q diagnostics=%q", code, output.String(), diagnostics.String())
	}
	output.Reset()
	diagnostics.Reset()
	if code := runControlPlaneRetention(context.Background(), &output, &diagnostics); code != 0 || !strings.Contains(output.String(), `"deleted":0`) {
		t.Fatalf("retention code=%d output=%q diagnostics=%q", code, output.String(), diagnostics.String())
	}
	t.Setenv("FEATUREPROOF_CONTROL_PLANE_QUERY_LIMIT", "invalid")
	if code := runControlPlaneJobs(context.Background(), io.Discard, io.Discard); code != 2 {
		t.Fatalf("malformed query limit returned %d", code)
	}
	t.Setenv("FEATUREPROOF_CONTROL_PLANE_RETENTION_BEFORE_UNIX", "invalid")
	if code := runControlPlaneRetention(context.Background(), io.Discard, io.Discard); code != 2 {
		t.Fatalf("malformed retention policy returned %d", code)
	}
	t.Setenv("FEATUREPROOF_CONTROL_PLANE_RETENTION_BEFORE_UNIX", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
	if code := runControlPlaneRetention(context.Background(), controlPlaneRejectWriter{}, io.Discard); code != 3 {
		t.Fatalf("retention output failure returned %d", code)
	}
	t.Setenv("FEATUREPROOF_CONTROL_PLANE_QUERY_LIMIT", "10")
	if code := runControlPlaneJobs(context.Background(), controlPlaneRejectWriter{}, io.Discard); code != 3 {
		t.Fatalf("jobs output failure returned %d", code)
	}
	t.Setenv("FEATUREPROOF_CONTROL_PLANE_CURSOR_SECRET", "short")
	if code := runControlPlaneJobs(context.Background(), io.Discard, io.Discard); code != 3 {
		t.Fatalf("weak cursor secret returned %d", code)
	}
	t.Setenv("FEATUREPROOF_CONTROL_PLANE_CURSOR_SECRET", strings.Repeat("c", 32))
	t.Setenv("FEATUREPROOF_CONTROL_PLANE_OPERATOR_UID", "not-the-current-operator")
	if code := runControlPlaneJobs(context.Background(), io.Discard, io.Discard); code != 3 {
		t.Fatalf("unauthorized operator returned %d", code)
	}
	t.Setenv("FEATUREPROOF_CONTROL_PLANE_OPERATOR_UID", operator.Uid)
	store, err := controlplanepg.Open(context.Background(), controlplanepg.Config{DSN: dsn, BackendIdentity: "cmd-operations", MaxConnections: 2, StatementTimeout: 5 * time.Second, MaxLease: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	runID := "cmd-import-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	epoch, err := controlplane.SealBackendEpoch(controlplane.BackendEpoch{RunID: runID, ProjectID: project, Epoch: 1, Mode: controlplane.BackendLocal, BackendIdentity: "cmd-local", SchemaVersion: 1, CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	head, err := store.ReserveInitial(context.Background(), epoch)
	store.Close()
	if err != nil {
		t.Fatal(err)
	}
	localRoot := t.TempDir()
	if err = os.MkdirAll(filepath.Join(localRoot, ".featureproof", "workflows", "functional-flow", "packages"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"FEATUREPROOF_CONTROL_PLANE_IMPORT_EXPECTED_EPOCH": "1",
		"FEATUREPROOF_CONTROL_PLANE_IMPORT_PAGE_SIZE":      "1",
		"FEATUREPROOF_CONTROL_PLANE_IMPORT_ID":             runID + "-import",
		"FEATUREPROOF_CONTROL_PLANE_IMPORT_RUN_ID":         runID,
		"FEATUREPROOF_CONTROL_PLANE_IMPORT_PROJECT_ID":     project,
		"FEATUREPROOF_CONTROL_PLANE_IMPORT_EXPECTED_HEAD":  head.Digest,
		"FEATUREPROOF_CONTROL_PLANE_LOCAL_ROOT":            localRoot,
		"FEATUREPROOF_CONTROL_PLANE_LOCAL_IDENTITY":        "cmd-local",
	} {
		t.Setenv(name, value)
	}
	output.Reset()
	if code := runControlPlaneImport(context.Background(), &output, io.Discard); code != 0 || !strings.Contains(output.String(), `"active_epoch":2`) {
		t.Fatalf("import code=%d output=%q", code, output.String())
	}
	t.Setenv("FEATUREPROOF_CONTROL_PLANE_IMPORT_EXPECTED_EPOCH", "0")
	if code := runControlPlaneImport(context.Background(), io.Discard, io.Discard); code != 2 {
		t.Fatalf("invalid import epoch returned %d", code)
	}
}

// TestGitHubControlPlanePostgresComposition verifies executable selection, migration, readiness and ingress admission against the isolated gate.
func TestGitHubControlPlanePostgresComposition(t *testing.T) {
	dsn := os.Getenv("FEATUREPROOF_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Fatal("FEATUREPROOF_POSTGRES_TEST_DSN is required")
	}
	t.Setenv("FEATUREPROOF_CONTROL_PLANE_BACKEND", string(controlplane.BackendPostgres))
	t.Setenv("FEATUREPROOF_CONTROL_PLANE_POSTGRES_DSN", dsn)
	t.Setenv("FEATUREPROOF_CONTROL_PLANE_IDENTITY", "cmd-integration")
	ctx := context.Background()
	ingress, build, err := composeGitHubControlPlane(ctx, githubpr.WallClock{}, time.Minute)
	if err != nil || ingress == nil || build == nil {
		t.Fatal("compose", err)
	}
	store, ok := ingress.(*controlplanepg.Store)
	if !ok {
		t.Fatal("unexpected control-plane implementation")
	}
	t.Cleanup(store.Close)
	deliveryID := "cmd-integration-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	command := githubpr.WebhookCommand{DeliveryID: deliveryID, Event: "issue_comment", InstallationID: 1, RepositoryID: 42, PullRequestNumber: 7, ActorID: 9, SelectionAlias: "default", PayloadDigest: integrationCommandDigest("payload")}
	admission, err := build(ctx, command)
	if err != nil {
		t.Fatal("build", err)
	}
	created, err := ingress.Admit(ctx, admission)
	if err != nil || !created {
		t.Fatal("admit", created, err)
	}
	created, err = ingress.Admit(ctx, admission)
	if err != nil || created {
		t.Fatal("redelivery", created, err)
	}
	loaded, err := store.LoadIngress(ctx, admission.Epoch.RunID)
	if err != nil || loaded.Envelope.ExternalID != command.DeliveryID {
		t.Fatal("reload", err)
	}
}

// TestGitHubRuntimePostgresCompositionOwnsLifecycle proves the executable can
// compose and shut down its complete PostgreSQL runtime without local stores.
func TestGitHubRuntimePostgresCompositionOwnsLifecycle(t *testing.T) {
	dsn := os.Getenv("FEATUREPROOF_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Fatal("FEATUREPROOF_POSTGRES_TEST_DSN is required")
	}
	for name, value := range map[string]string{
		"FEATUREPROOF_CONTROL_PLANE_BACKEND":      string(controlplane.BackendPostgres),
		"FEATUREPROOF_CONTROL_PLANE_POSTGRES_DSN": dsn,
		"FEATUREPROOF_CONTROL_PLANE_IDENTITY":     "github-runtime-postgres",
		"FEATUREPROOF_GITHUB_INSTALLATION_ID":     "101",
		"FEATUREPROOF_GITHUB_REPOSITORY_ID":       "202",
		"FEATUREPROOF_GITHUB_ACTOR_ID":            "303",
		"FEATUREPROOF_GITHUB_APP_ID":              "404",
		"FEATUREPROOF_GITHUB_BOT_ID":              "505",
		"FEATUREPROOF_GITHUB_WEBHOOK_SECRET":      strings.Repeat("s", 32),
		"FEATUREPROOF_GITHUB_PRIVATE_KEY_PEM":     "composition-owned-private-key-reference",
		"FEATUREPROOF_EVIDENCE_ORIGIN":            "https://evidence.invalid",
		"FEATUREPROOF_GITHUB_LISTEN":              "127.0.0.1:0",
		"FEATUREPROOF_WORKFLOW_CONFIG":            "",
		"FEATUREPROOF_PREVIEW_CONFIG":             "",
		"FEATUREPROOF_PROJECT_MEMORY_CONFIG":      "",
		"FEATUREPROOF_CONTRACT_REVIEW_CONFIG":     "",
	} {
		t.Setenv(name, value)
	}
	runtime, address, err := composeGitHubRuntime(context.Background())
	if err != nil || runtime == nil || address != "127.0.0.1:0" {
		t.Fatal("compose runtime", address, err)
	}
	if err = runtime.Start(context.Background()); err != nil {
		t.Fatal("start runtime", err)
	}
	_ = runtime.Close(context.Background())
	serverContext, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	if code := runGitHubServer(serverContext, io.Discard); code != 0 {
		t.Fatalf("shutdown server returned %d", code)
	}
	t.Setenv("FEATUREPROOF_GITHUB_LISTEN", "invalid address")
	if code := runGitHubServer(context.Background(), io.Discard); code != 3 {
		t.Fatalf("listen failure returned %d", code)
	}
	cancelled, cancelImmediately := context.WithCancel(context.Background())
	cancelImmediately()
	if code := runGitHubServer(cancelled, io.Discard); code != 3 {
		t.Fatalf("start cancellation returned %d", code)
	}
}

// postgresWorkflowPersistence projects the complete consumer-owned PostgreSQL
// graph into the exact ports used by productive workflow composition.
func postgresWorkflowPersistence(domains *controlplanepg.DomainStores, store *controlplanepg.Store) *workflowPersistence {
	return &workflowPersistence{workflows: domains.Workflow, artifacts: domains.PlanArtifacts, executables: domains.ExecutablePlan, approvals: domains.Approval, environments: domains.Environment, capacity: domains.Capacity, cleanupLedger: domains.CleanupLedger, cleanup: domains.Cleanup, functional: domains.Functional, journal: domains.Functional, packages: domains.Packages, corrections: domains.Correction, fixtures: domains.Fixtures, context: domains.Context, functionalRuntime: store}
}

// routing GitHub and model traffic to deterministic in-process providers.
type postgresVerticalTransport struct {
	model  *workflowProductModelTransport
	target http.RoundTripper
	host   string
	github atomic.Int32
}

// RoundTrip serves exact GitHub authority documents and delegates target/model
// requests to their real protocol adapters without network access.
func (transport *postgresVerticalTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Host == transport.host {
		return transport.target.RoundTrip(request)
	}
	transport.github.Add(1)
	body, status := "", http.StatusOK
	switch request.URL.Path {
	case "/app/installations/101/access_tokens":
		status = http.StatusCreated
		body = `{"token":"short-lived","expires_at":"2100-01-01T00:00:00Z","permissions":{"checks":"write","issues":"write","metadata":"read","pull_requests":"read"},"repositories":[{"id":202}]}`
	case "/repositories/202/pulls/7":
		body = `{"number":7,"state":"open","user":{"id":303},"head":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repo":{"id":202}},"base":{"repo":{"id":202,"full_name":"owner/repository"}}}`
	case "/repos/owner/repository/pulls/7/files":
		body = `[{"filename":"internal/service.go"}]`
	default:
		transport.github.Add(-1)
		return transport.model.RoundTrip(request)
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

// preparePostgresVerticalWorkflow writes the complete v3 fixture workflow
// inputs without composing or opening any local persistence adapter.
func preparePostgresVerticalWorkflow(t *testing.T, root, targetURL string) string {
	t.Helper()
	configPath, specPath, _ := writeWorkflowCompositionFixture(t, root, workflowFixtureContractFixture(), "/pets", workflowProjectionFixture())
	specification := "version: 3\nfeature:\n  id: postgres-vertical\n  description: postgres vertical\ntarget:\n  id: deployed\n  base_url: " + targetURL + "\nconstraints:\n  - id: c1\n    description: selected contract\n    target_id: deployed\nlimits:\n  max_steps: 6\n  max_tool_calls: 3\n  max_retries: 0\nidentities:\n  - id: customer\n    target_id: deployed\n    credential_ref: cred_customer\n    mechanism:\n      type: bearer\n    operations:\n      - capability: http.request\n        method: GET\n        path: /pets\n      - capability: http.request\n        method: POST\n        path: /fixtures\n      - capability: http.request\n        method: DELETE\n        path: /fixtures/{id}\nfixtures:\n  - id: account\n    depends_on: []\n    create:\n      operation: {method: POST, path: /fixtures}\n      identity_id: customer\n      input:\n        body:\n          object:\n            name: {literal: product-fixture}\n      accepted_statuses: [201]\n      idempotency: contract\n    capture:\n      - name: id\n        path: [id]\n        value_type: string\n        max_bytes: 64\n    cleanup:\n      operation: {method: DELETE, path: \"/fixtures/{id}\"}\n      identity_id: customer\n      input:\n        path_parameters:\n          id:\n            reference: {fixture_id: account, output: id, value_type: string}\n      accepted_statuses: [204, 404]\n      idempotency: contract\n"
	if err := os.WriteFile(specPath, []byte(specification), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := `{"version":3,"runtime":{"http":{"allowed_methods":["GET","POST","DELETE"],"allowed_request_headers":["Idempotency-Key"],"allowed_response_headers":["Content-Type"],"follow_redirects_for_methods":[],"max_redirects":0,"timeout_ms":1000,"max_url_bytes":2048,"max_request_header_bytes":8192,"max_request_body_bytes":1024,"max_response_header_bytes":8192,"max_response_body_bytes":1024},"redaction":{"max_body_bytes":1024,"additional_sensitive_headers":[],"sensitive_json_paths":[],"model_sensitive_json_paths":[]},"policy":{"allowed_tools":["http.request"]}},"plans":[{"id":"p1","constraint_id":"c1","steps":[{"id":"request","tool":"http.request","identity_id":"customer","input":{"method":{"literal":"GET"},"path":{"literal":"/pets"}},"assertions":[{"id":"status","definition":{"operator":"eq","path":"/status","expected":200}}],"depends_on":[]}]}]}`
	if err := os.WriteFile(filepath.Join(root, "runtime.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := map[string]any{"profile": map[string]any{"ProfileID": "profile", "ClientID": "client", "Subject": "subject", "AccessToken": "MODEL-CREDENTIAL", "RefreshToken": "refresh", "IDToken": "id", "Scopes": []string{"chatgpt.tokens.use.direct"}, "ExpiresAt": time.Now().Add(time.Hour)}}
	profileRaw, _ := json.Marshal(profile)
	if err := os.WriteFile(filepath.Join(root, "profiles.json"), profileRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	rewriteWorkflowConfig(t, configPath, func(value map[string]any) {
		value["repository_id"], value["target_alias"], value["target_url"] = "202", "default", targetURL
		value["identity_credential_environment"] = map[string]any{"cred_customer": "FEATUREPROOF_PRODUCT_IDENTITY"}
		value["identity_observation_timeout_ms"] = 1000
		value["contract_selections"] = map[string]any{"c1": []any{map[string]any{"method": "GET", "path": "/pets"}, map[string]any{"method": "POST", "path": "/fixtures"}, map[string]any{"method": "DELETE", "path": "/fixtures/{id}"}}}
		value["fixture_runtime"] = map[string]any{"max_capture_bytes": 1024, "max_tool_calls": 8, "max_retries": 2, "create_retries": 1, "cleanup_timeout_ms": 10000, "cleanup_max_calls": 2, "cleanup_retries": 1, "retry_backoff_ms": 10, "lease_ttl_ms": 11000}
		catalog := value["environment_catalog"].(map[string]any)
		item := catalog["items"].([]any)[0].(map[string]any)
		item["alias"], item["project_id"], item["repository_id"] = "default", "202", "202"
	})
	return configPath
}

// deliverPostgresVerticalWebhook crosses the same authenticated HTTP handler
// used in production and requires persist-before-ack acceptance.
func deliverPostgresVerticalWebhook(t *testing.T, runtime *githubpr.Runtime, secret, delivery, body string) {
	t.Helper()
	payload := []byte(`{"action":"created","installation":{"id":101},"repository":{"id":202},"issue":{"number":7,"pull_request":{}},"comment":{"body":` + strconv.Quote(body) + `,"user":{"id":303}}}`)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	request.Header.Set("X-GitHub-Delivery", delivery)
	request.Header.Set("X-GitHub-Event", "issue_comment")
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	recorder := httptest.NewRecorder()
	runtime.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("webhook %s status=%d body=%s", delivery, recorder.Code, recorder.Body.String())
	}
}

// postgresVerticalBarrier converts committed workflow/effect transitions into
// database notifications, avoiding timing polls in the product-runtime gate.
type postgresVerticalBarrier struct {
	conn *pgx.Conn
}

// newPostgresVerticalBarrier installs test-only AFTER triggers and subscribes
// before product work begins, so no committed transition can be missed.
func newPostgresVerticalBarrier(t *testing.T, dsn string) *postgresVerticalBarrier {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE OR REPLACE FUNCTION featureproof_test_notify_workflow() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_notify('featureproof_vertical', NEW.idempotency_key); RETURN NEW; END $$`,
		`CREATE OR REPLACE FUNCTION featureproof_test_notify_effect() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_notify('featureproof_vertical', NEW.project_id); RETURN NEW; END $$`,
		`DROP TRIGGER IF EXISTS featureproof_test_notify_workflow ON workflow_runs`,
		`CREATE TRIGGER featureproof_test_notify_workflow AFTER INSERT OR UPDATE ON workflow_runs FOR EACH ROW EXECUTE FUNCTION featureproof_test_notify_workflow()`,
		`DROP TRIGGER IF EXISTS featureproof_test_notify_effect ON effect_intents`,
		`CREATE TRIGGER featureproof_test_notify_effect AFTER INSERT OR UPDATE ON effect_intents FOR EACH ROW EXECUTE FUNCTION featureproof_test_notify_effect()`,
		`LISTEN featureproof_vertical`,
	} {
		if _, err = conn.Exec(context.Background(), statement); err != nil {
			conn.Close(context.Background())
			t.Fatal(err)
		}
	}
	barrier := &postgresVerticalBarrier{conn: conn}
	t.Cleanup(func() { barrier.conn.Close(context.Background()) })
	return barrier
}

// waitWorkflow returns only after the durable projection reaches the requested
// phase and a passive approval phase has released its owner for another process.
func (barrier *postgresVerticalBarrier) waitWorkflow(t *testing.T, key string, phase validationworkflow.Phase) validationworkflow.Record {
	t.Helper()
	waitContext, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var lastRecord validationworkflow.Record
	var lastError error
	for {
		var data []byte
		var owner string
		err := barrier.conn.QueryRow(waitContext, `SELECT record,COALESCE(owner,'') FROM workflow_runs WHERE idempotency_key=$1`, key).Scan(&data, &owner)
		var record validationworkflow.Record
		if err == nil {
			lastError = json.Unmarshal(data, &record)
			lastRecord = record
		} else {
			lastError = err
		}
		if lastError == nil && record.Phase == phase && (phase != validationworkflow.PhaseAwaitingApproval || owner == "") {
			return record
		}
		if _, err = barrier.conn.WaitForNotification(waitContext); err != nil {
			break
		}
	}
	var fixtureDocument []byte
	diagnosticErr := lastError
	if lastRecord.Handle != "" {
		diagnosticErr = barrier.conn.QueryRow(context.Background(), `SELECT document FROM fixture_states WHERE run_id=$1`, lastRecord.Handle).Scan(&fixtureDocument)
	}
	t.Fatalf("workflow %s did not reach %s; last phase=%s category=%s cleanup=%v fixture=%+v fixture_document=%s diagnostic=%v error=%v", key, phase, lastRecord.Phase, lastRecord.OperationalCategory, lastRecord.CleanupPending, lastRecord.FixtureSummary, fixtureDocument, diagnosticErr, lastError)
	return validationworkflow.Record{}
}

// waitConfirmedEffect observes the exact outbox receipt that atomically closes
// one delivery job and never infers completion from project-wide counts.
func (barrier *postgresVerticalBarrier) waitConfirmedEffect(t *testing.T, operationID string) {
	t.Helper()
	waitContext, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for {
		var state string
		err := barrier.conn.QueryRow(waitContext, `SELECT state FROM effect_intents WHERE operation_id=$1`, operationID).Scan(&state)
		if err == nil && state == "confirmed" {
			return
		}
		if _, err = barrier.conn.WaitForNotification(waitContext); err != nil {
			t.Fatalf("effect %s did not converge: state=%s error=%v", operationID, state, err)
		}
	}
}

// diagnostics reports the durable job, outbox and workflow states that explain why a vertical milestone was not reached.
func (barrier *postgresVerticalBarrier) diagnostics(projectID, workflowKey string) string {
	diagnosticContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	jobs, err := barrier.conn.Query(diagnosticContext, `SELECT j.job_id,j.state,j.phase,j.attempt,COALESCE(convert_from(j.document,'UTF8')::jsonb->>'last_category',''),COALESCE(string_agg(e.kind,',' ORDER BY e.sequence),'') FROM jobs j LEFT JOIN job_events e ON e.job_id=j.job_id WHERE j.project_id=$1 GROUP BY j.job_id,j.state,j.phase,j.attempt,j.document ORDER BY j.job_id`, projectID)
	if err != nil {
		return "jobs_query=" + err.Error()
	}
	defer jobs.Close()
	report := ""
	for jobs.Next() {
		var jobID, state, phase, category, events string
		var attempt uint64
		if err = jobs.Scan(&jobID, &state, &phase, &attempt, &category, &events); err != nil {
			return "jobs_scan=" + err.Error()
		}
		report += fmt.Sprintf("job[%s state=%s phase=%s attempt=%d category=%s events=%s] ", jobID, state, phase, attempt, category, events)
	}
	var workflowPhase, workflowOwner, workflowEvents string
	if err = barrier.conn.QueryRow(diagnosticContext, `SELECT w.phase,COALESCE(w.owner,''),COALESCE(string_agg(COALESCE(convert_from(e.event,'UTF8')::jsonb->>'Type',convert_from(e.event,'UTF8')::jsonb->>'type'),',' ORDER BY e.sequence),'') FROM workflow_runs w LEFT JOIN workflow_events e ON e.handle=w.handle WHERE w.idempotency_key=$1 GROUP BY w.phase,w.owner`, workflowKey).Scan(&workflowPhase, &workflowOwner, &workflowEvents); err != nil {
		return report + "workflow_query=" + err.Error()
	}
	return report + fmt.Sprintf("workflow[phase=%s owner=%s events=%s]", workflowPhase, workflowOwner, workflowEvents)
}

// TestPostgresProductRuntimeContinuesOnlyThroughDurableWorker proves process
// N/N+1 restart, filesystem loss, JIT outbox execution and full fixture cleanup.
func TestPostgresProductRuntimeContinuesOnlyThroughDurableWorker(t *testing.T) {
	dsn := os.Getenv("FEATUREPROOF_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Fatal("FEATUREPROOF_POSTGRES_TEST_DSN is required")
	}
	root := t.TempDir()
	t.Chdir(root)
	var creates, reads, deletes atomic.Int32
	readStarted, releaseRead := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-releaseRead:
		default:
			close(releaseRead)
		}
	}()
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/fixtures":
			creates.Add(1)
			writer.WriteHeader(http.StatusCreated)
			_, _ = writer.Write([]byte(`{"id":"postgres-fixture"}`))
		case request.Method == http.MethodGet && request.URL.Path == "/pets":
			reads.Add(1)
			close(readStarted)
			<-releaseRead
			_, _ = writer.Write([]byte(`{"ok":true}`))
		case request.Method == http.MethodDelete && request.URL.Path == "/fixtures/postgres-fixture":
			deletes.Add(1)
			writer.WriteHeader(http.StatusNoContent)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer target.Close()
	configPath := preparePostgresVerticalWorkflow(t, root, target.URL)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	secret := strings.Repeat("s", 32)
	for name, value := range map[string]string{"FEATUREPROOF_CONTROL_PLANE_BACKEND": string(controlplane.BackendPostgres), "FEATUREPROOF_CONTROL_PLANE_POSTGRES_DSN": dsn, "FEATUREPROOF_CONTROL_PLANE_IDENTITY": "github-product-vertical", "FEATUREPROOF_GITHUB_INSTALLATION_ID": "101", "FEATUREPROOF_GITHUB_REPOSITORY_ID": "202", "FEATUREPROOF_GITHUB_ACTOR_ID": "303", "FEATUREPROOF_GITHUB_APP_ID": "404", "FEATUREPROOF_GITHUB_BOT_ID": "505", "FEATUREPROOF_GITHUB_WEBHOOK_SECRET": secret, "FEATUREPROOF_GITHUB_PRIVATE_KEY_PEM": string(privatePEM), "FEATUREPROOF_GITHUB_API_ORIGIN": "https://api.github.test", "FEATUREPROOF_EVIDENCE_ORIGIN": "https://evidence.invalid", "FEATUREPROOF_GITHUB_LISTEN": "127.0.0.1:0", "FEATUREPROOF_WORKFLOW_CONFIG": configPath, "FEATUREPROOF_PRODUCT_IDENTITY": "POSTGRES-IDENTITY-AFTER-RESTART", "FEATUREPROOF_PREVIEW_CONFIG": "", "FEATUREPROOF_PROJECT_MEMORY_CONFIG": "", "FEATUREPROOF_CONTRACT_REVIEW_CONFIG": ""} {
		t.Setenv(name, value)
	}
	model := &workflowProductModelTransport{}
	targetURL, _ := http.NewRequest(http.MethodGet, target.URL, nil)
	transport := &postgresVerticalTransport{model: model, target: target.Client().Transport, host: targetURL.URL.Host}
	previousTransport := http.DefaultTransport
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = previousTransport }()

	first, _, err := composeGitHubRuntime(context.Background())
	if err != nil {
		t.Fatal("compose process N", err)
	}
	barrier := newPostgresVerticalBarrier(t, dsn)
	if err = first.Start(context.Background()); err != nil {
		t.Fatal("start process N", err)
	}
	defer first.Close(context.Background())
	delivery := "vertical-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	deliverPostgresVerticalWebhook(t, first, secret, delivery, "/featureproof validate")
	hash := sha256.Sum256([]byte(delivery))
	jobID := "ingress-" + hex.EncodeToString(hash[:])
	workflowKey := "github:" + jobID
	pending := barrier.waitWorkflow(t, workflowKey, validationworkflow.PhaseAwaitingApproval)
	barrier.waitConfirmedEffect(t, jobID+"-dispatch")
	if err = first.Close(context.Background()); err != nil {
		t.Fatal("close process N", err)
	}
	stateRoot := filepath.Join(root, ".featureproof")
	if err = os.RemoveAll(stateRoot); err != nil {
		t.Fatal(err)
	}

	second, _, err := composeGitHubRuntime(context.Background())
	if err != nil || second.Start(context.Background()) != nil {
		t.Fatal("compose/start process N+1", err)
	}
	defer second.Close(context.Background())
	approvalDelivery := delivery + "-approval"
	approvalHash := sha256.Sum256([]byte(approvalDelivery))
	approvalJobID := "ingress-" + hex.EncodeToString(approvalHash[:])
	deliverPostgresVerticalWebhook(t, second, secret, approvalDelivery, "/featureproof contract-review decide "+pending.Handle+" "+strconv.FormatUint(pending.ApprovalRevision, 10)+" accept_all")
	select {
	case <-readStarted:
	case <-time.After(90 * time.Second):
		t.Fatalf("productive execution did not reach target effect: %s", barrier.diagnostics("repository-202", workflowKey))
	}
	activePool, poolErr := pgxpool.New(context.Background(), dsn)
	if poolErr != nil {
		t.Fatal(poolErr)
	}
	var activeKinds []string
	if poolErr = activePool.QueryRow(context.Background(), `SELECT array_agg(DISTINCT reference_kind ORDER BY reference_kind) FROM retention_references WHERE job_id=$1 AND live`, jobID).Scan(&activeKinds); poolErr != nil {
		activePool.Close()
		t.Fatal(poolErr)
	}
	activePool.Close()
	wantActive := []string{"cleanup", "environment", "fixture", "workflow"}
	activeReferencesMatch := reflect.DeepEqual(activeKinds, wantActive)
	close(releaseRead)
	if !activeReferencesMatch {
		t.Fatalf("active causal references=%v", activeKinds)
	}
	terminal := barrier.waitWorkflow(t, workflowKey, validationworkflow.PhaseTerminal)
	barrier.waitConfirmedEffect(t, approvalJobID+"-dispatch")
	if err = second.Close(context.Background()); err != nil {
		t.Fatal("close process N+1", err)
	}
	if terminal.Outcome == nil || *terminal.Outcome != validationworkflow.OutcomePassed || creates.Load() != 1 || reads.Load() != 1 || deletes.Load() != 1 || model.calls.Load() != 1 || transport.github.Load() < 6 {
		t.Fatalf("terminal=%+v model=%d github=%d target=%d/%d/%d", terminal, model.calls.Load(), transport.github.Load(), creates.Load(), reads.Load(), deletes.Load())
	}
	if _, statErr := os.Stat(stateRoot); !os.IsNotExist(statErr) {
		t.Fatalf("PostgreSQL runtime recreated LocalStore root: %v", statErr)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var packages, confirmed, activeReferences int
	var referenceKinds []string
	if err = pool.QueryRow(context.Background(), `SELECT count(*) FROM packages WHERE run_id=$1`, pending.Handle).Scan(&packages); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(context.Background(), `SELECT count(*) FROM effect_intents WHERE state='confirmed' AND project_id='repository-202'`).Scan(&confirmed); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(context.Background(), `SELECT count(*) FROM retention_references WHERE job_id=$1 AND live`, jobID).Scan(&activeReferences); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(context.Background(), `SELECT array_agg(DISTINCT reference_kind ORDER BY reference_kind) FROM retention_references WHERE job_id=$1`, jobID).Scan(&referenceKinds); err != nil {
		t.Fatal(err)
	}
	wantReferences := []string{"cleanup", "environment", "fixture", "package", "workflow"}
	if packages != 1 || confirmed < 2 || activeReferences != 0 || !reflect.DeepEqual(referenceKinds, wantReferences) {
		t.Fatalf("packages=%d confirmed=%d active_references=%d references=%v", packages, confirmed, activeReferences, referenceKinds)
	}
}

// TestPostgresWorkflowCompositionSurvivesFilesystemRemoval proves productive
// workflow composition reopens every domain port from PostgreSQL and never
// recreates a local feature state root after process restart.
func TestPostgresWorkflowCompositionSurvivesFilesystemRemoval(t *testing.T) {
	dsn := os.Getenv("FEATUREPROOF_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Fatal("FEATUREPROOF_POSTGRES_TEST_DSN is required")
	}
	ctx := context.Background()
	root := t.TempDir()
	t.Chdir(root)
	configPath, _, _ := writeWorkflowCompositionFixture(t, root, workflowContractFixture(), "/pets", workflowProjectionFixture())
	t.Setenv("FEATUREPROOF_WORKFLOW_CONFIG", configPath)
	store, err := controlplanepg.Open(ctx, controlplanepg.Config{DSN: dsn, BackendIdentity: "cmd-vertical", MaxConnections: 4, StatementTimeout: 5 * time.Second, MaxLease: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	domains, err := store.DomainStores(controlplanepg.DomainStoreConfig{Clock: githubpr.WallClock{}, FunctionalTTL: time.Minute, CapacityLimits: map[string]environment.CapacityLimits{"repo": {Environments: 2, CPUMilli: 2000, MemoryBytes: 1 << 30, ObjectCount: 100, MaxTTL: time.Hour}}})
	if err != nil {
		t.Fatal(err)
	}
	persistence := postgresWorkflowPersistence(domains, store)
	if cli, composeErr := composeWorkflowCLIWithPersistence(ctx, io.Discard, io.Discard, persistence); composeErr != nil || cli == nil {
		t.Fatal("first postgres composition", composeErr)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	runID := "cmd-vertical-" + strconv.FormatInt(now.UnixNano(), 10)
	correctionID := runID + "-correction"
	record := validationworkflow.Record{Version: 1, Handle: runID, Principal: "cmd-vertical-principal", IdempotencyKey: runID + "-key", RequestDigest: integrationCommandDigest(runID + "-request"), RepositoryID: "repo", Revision: strings.Repeat("a", 40), Phase: validationworkflow.PhaseAdmitted}
	if _, created, boundContext, reserveErr := domains.Workflow.ReserveBound(ctx, record, "cmd-vertical-worker", now, time.Minute); reserveErr != nil || !created {
		t.Fatal("postgres workflow seed", created, reserveErr)
	} else {
		ctx = boundContext
	}
	artifact, err := validationworkflow.SealPlanArtifactForStore(validationworkflow.PlanArtifact{Version: validationworkflow.ArtifactVersion, WorkflowID: record.Handle, PlanID: "cmd-vertical-plan", Plans: []localvalidation.PlanProjection{{ID: "cmd-vertical-plan"}}, ContextInstructionsHash: integrationCommandDigest("cmd-vertical-instructions")})
	if err != nil || domains.PlanArtifacts.Commit(ctx, artifact) != nil {
		t.Fatal("postgres plan artifact", err)
	}
	material := validationworkflow.ExecutableMaterial{Version: validationworkflow.ArtifactVersion, WorkflowID: record.Handle, ArtifactSHA256: artifact.ArtifactSHA256, ContextDigest: integrationCommandDigest("cmd-vertical-context"), RevisionDigest: integrationCommandDigest("cmd-vertical-revision"), TargetDigest: integrationCommandDigest("cmd-vertical-target"), Snapshot: agentplanning.PreparedSnapshot{ID: "cmd-vertical-plan", Checks: []agentplanning.CandidateCheck{{}}}, CommitmentKey: []byte("0123456789abcdef0123456789abcdef")}
	if err = domains.ExecutablePlan.Commit(ctx, record.Handle, material); err != nil {
		t.Fatal("postgres executable", err)
	}
	selection, err := environment.SealSelection(environment.Selection{Version: 1, Alias: "cmd-vertical-target", ProviderID: "existing-url", ProviderVersion: "v1", Kind: environment.KindExistingURL, TemplateReference: "target-ref", ProfileID: "runtime", NetworkPolicyID: "network", Secretless: true, ProjectID: "repo", RepositoryID: "repo", Capabilities: []string{"http"}})
	if err != nil {
		t.Fatal(err)
	}
	environmentRecord := environment.Record{Version: 1, RunID: record.Handle, RequestDigest: record.RequestDigest, AcquisitionKey: integrationCommandDigest("cmd-vertical-acquisition"), CommitOID: record.Revision, Selection: selection, State: environment.StateSelected, Revision: 1}
	if _, _, err = domains.Environment.Reserve(ctx, environmentRecord); err != nil {
		t.Fatal("postgres environment", err)
	}
	if err = contracttest.RunCorrectionStore(ctx, func() (correction.Store, error) { return domains.Correction, nil }, record.Handle, correctionID, "repo", now); err != nil {
		t.Fatal("postgres correction", err)
	}
	if err = contracttest.RunFunctionalStore(ctx, func() (contracttest.FunctionalStore, error) { return domains.Functional, nil }, record.Handle); err != nil {
		t.Fatal("postgres functional flow", err)
	}
	if err = contracttest.RunCleanupStore(ctx, func() (contracttest.CleanupStore, error) { return domains.Cleanup, nil }, record.Handle, "repo", now); err != nil {
		t.Fatal("postgres cleanup", err)
	}
	head, epoch, err := store.Load(ctx, record.Handle)
	if err != nil {
		t.Fatal(err)
	}
	packageBytes := []byte(`{"version":"cmd-vertical/v1","run_id":"cmd-vertical-run"}`)
	packageDocument := validationworkflow.PackageDocument{ProjectID: "repo", RunID: record.Handle, PackageID: runID + "-package", MediaType: "application/vnd.featureproof.test+json", SchemaVersion: "cmd-vertical/v1", ArtifactDigest: artifact.ArtifactSHA256, BindingDigest: head.EpochDigest, BackendEpoch: head.Epoch, BackendIdentity: epoch.BackendIdentity, Document: packageBytes, Digest: integrationCommandDigest(string(packageBytes)), CreatedAt: now}
	if _, err = domains.Packages.CommitPackage(ctx, packageDocument); err != nil {
		t.Fatal("postgres package", err)
	}
	terminalRecord, err := domains.Workflow.Load(ctx, record.Handle)
	if err != nil {
		t.Fatal(err)
	}
	terminalOutcome := validationworkflow.OutcomeCancelled
	terminalRecord.Phase, terminalRecord.Outcome, terminalRecord.CleanupPending = validationworkflow.PhaseTerminal, &terminalOutcome, false
	if _, err = domains.Workflow.SaveOwned(ctx, terminalRecord, terminalRecord.Owner, terminalRecord.VersionCAS, now.Add(10*time.Second), time.Minute, validationworkflow.Event{Type: "vertical_terminal"}); err != nil {
		t.Fatal("terminalize vertical workflow", err)
	}
	stateRoot := filepath.Join(root, ".featureproof")
	if err = os.RemoveAll(stateRoot); err != nil {
		t.Fatal(err)
	}
	restarted, err := controlplanepg.Open(ctx, controlplanepg.Config{DSN: dsn, BackendIdentity: "cmd-vertical", MaxConnections: 4, StatementTimeout: 5 * time.Second, MaxLease: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restartedDomains, err := restarted.DomainStores(controlplanepg.DomainStoreConfig{Clock: githubpr.WallClock{}, FunctionalTTL: time.Minute, CapacityLimits: map[string]environment.CapacityLimits{"repo": {Environments: 2, CPUMilli: 2000, MemoryBytes: 1 << 30, ObjectCount: 100, MaxTTL: time.Hour}}})
	if err != nil {
		t.Fatal(err)
	}
	restartedPersistence := postgresWorkflowPersistence(restartedDomains, restarted)
	if cli, composeErr := composeWorkflowCLIWithPersistence(ctx, io.Discard, io.Discard, restartedPersistence); composeErr != nil || cli == nil {
		t.Fatal("restarted postgres composition", composeErr)
	}
	if loaded, loadErr := restartedDomains.Workflow.Load(ctx, record.Handle); loadErr != nil || loaded.Handle != record.Handle {
		t.Fatal("restarted workflow", loadErr)
	}
	if loaded, loadErr := restartedDomains.PlanArtifacts.Load(ctx, record.Handle); loadErr != nil || loaded.ArtifactSHA256 != artifact.ArtifactSHA256 {
		t.Fatal("restarted artifact", loadErr)
	}
	if loaded, loadErr := restartedDomains.ExecutablePlan.Load(ctx, record.Handle); loadErr != nil || loaded.ArtifactSHA256 != material.ArtifactSHA256 {
		t.Fatal("restarted executable", loadErr)
	}
	if loaded, loadErr := restartedDomains.Environment.Load(ctx, record.Handle); loadErr != nil || loaded.RunID != record.Handle {
		t.Fatal("restarted environment", loadErr)
	}
	if loaded, loadErr := restartedDomains.Correction.Get(ctx, correctionID); loadErr != nil || loaded.Link.RootRunID != record.Handle {
		t.Fatal("restarted correction", loadErr)
	}
	if terminal, found, loadErr := restartedDomains.Functional.Terminal(ctx, record.Handle, 1, "step-1"); loadErr != nil || !found || terminal.RunID != record.Handle {
		t.Fatal("restarted functional terminal", found, loadErr)
	}
	if status, loadErr := restartedDomains.Cleanup.Status(ctx, record.Handle+"-cleanup"); loadErr != nil || status.CleanupID != record.Handle+"-cleanup" {
		t.Fatal("restarted cleanup", loadErr)
	}
	if loaded, loadErr := restartedDomains.Packages.LoadPackage(ctx, "repo", packageDocument.PackageID); loadErr != nil || loaded.Digest != packageDocument.Digest {
		t.Fatal("restarted package", loadErr)
	}
	if _, statErr := os.Stat(stateRoot); !os.IsNotExist(statErr) {
		t.Fatalf("postgres composition recreated local state: %v", statErr)
	}
}

// integrationCommandDigest creates one valid content identity for the executable gate fixture.
func integrationCommandDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
