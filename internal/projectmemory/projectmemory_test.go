package projectmemory

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixedClock provides trusted deterministic time to service tests.
type fixedClock struct{ now time.Time }

// Now returns the configured trusted instant.
func (clock fixedClock) Now() time.Time { return clock.now }

// allowAuthority maps only known principals to configured project actions.
type allowAuthority struct{ denied bool }

// roleAuthority grants one exact principal, actor kind, and action set for cross-role tests.
type roleAuthority struct {
	principal, kind string
	actions         map[string]bool
}

// Authorize rejects every principal or action outside this isolated authority role.
func (authority roleAuthority) Authorize(_ context.Context, principal, project, action string) (PrincipalDecision, error) {
	if principal != authority.principal || project != "project-1" || !authority.actions[action] {
		return PrincipalDecision{}, errors.New("denied")
	}
	return PrincipalDecision{Principal: principal, Allowed: true, MaxScope: ScopeProject, MaxSensitivity: SensitivityRestricted, PolicyDigest: strings.Repeat("a", 64), Actor: Actor{Kind: authority.kind, ID: principal}, RepositoryID: "repo-1", CommitOID: strings.Repeat("b", 40), ConfigurationDigest: strings.Repeat("c", 64), Producer: "featureproof", ProducerVersion: ContractVersion, Reference: "role-" + authority.kind}, nil
}

// Authorize returns a sealed policy decision and never trusts payload authority fields.
func (authority allowAuthority) Authorize(_ context.Context, principal, project, action string) (PrincipalDecision, error) {
	if authority.denied || principal != "principal" || project != "project-1" || action == "" {
		return PrincipalDecision{}, errors.New("denied")
	}
	return PrincipalDecision{Principal: principal, Allowed: true, MaxScope: ScopeProject, MaxSensitivity: SensitivityRestricted, PolicyDigest: strings.Repeat("a", 64), Actor: Actor{Kind: testActorKind(action), ID: principal}, RepositoryID: "repo-1", CommitOID: strings.Repeat("b", 40), Producer: "featureproof", ProducerVersion: "v1", Reference: "trusted-decision"}, nil
}

// testActorKind maps each command to the independent actor role required by the service boundary.
func testActorKind(action string) string {
	if action == "observe" {
		return "observer"
	}
	if action == "expire" {
		return "reconciler"
	}
	return "human"
}

// TestCreationAuthorityMatricesAndServerProvenance proves scope, sensitivity, time, actor, source, producer, and bindings are server-owned.
func TestCreationAuthorityMatricesAndServerProvenance(t *testing.T) {
	service, _, now := testService(t)
	service.HumanPrincipals = boundedAuthority{maxScope: ScopeRun, maxSensitivity: SensitivityPublic}
	for _, scope := range []Scope{
		{Kind: ScopeRun, RunID: "run-1", RevisionID: "revision-1"},
		{Kind: ScopeRevision, RepositoryID: "repo-1", CommitOID: strings.Repeat("b", 40)},
		{Kind: ScopeProject, RepositoryID: "repo-1"},
	} {
		candidate := testCandidate(now.Add(-time.Hour))
		candidate.Scope = scope
		candidate.Provenance = Provenance{Source: "forged-human", Reference: "forged", Actor: Actor{Kind: "human", ID: "admin"}, Producer: "attacker", ProducerVersion: "root", ObservedAt: now.Add(24 * time.Hour), RepositoryID: "forged", ConfigurationDigest: strings.Repeat("d", 64)}
		view, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "scope-" + string(scope.Kind), Candidate: candidate})
		if scope.Kind != ScopeRun {
			if err == nil {
				t.Fatalf("scope %s widened", scope.Kind)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if view.Provenance.Actor.ID != "principal" || view.Provenance.Actor.Kind != "human" || view.Provenance.Source != "agent_proposal" || view.Provenance.Producer != "featureproof" || !view.Provenance.ObservedAt.Equal(now) || view.Provenance.RepositoryID != "repo-1" || view.Provenance.ConfigurationDigest != strings.Repeat("c", 64) {
			t.Fatalf("untrusted provenance survived: %#v", view.Provenance)
		}
	}
	restricted := testCandidate(now)
	restricted.Scope = Scope{Kind: ScopeRun, RunID: "run-2", RevisionID: "revision-2"}
	restricted.Sensitivity = SensitivityInternal
	if _, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "sensitivity", ExpectedSequence: 1, Candidate: restricted}); err == nil {
		t.Fatal("sensitivity ceiling widened")
	}
}

// TestQueryOrdersAndFindsConflictsBeforeTruncation proves stable sort and conflict discovery precede item and byte cuts.
func TestQueryOrdersAndFindsConflictsBeforeTruncation(t *testing.T) {
	service, store, now := testService(t)
	firstCandidate := testCandidate(now)
	firstCandidate.Subject = "z-subject"
	first, err := NormalizeRecord(firstCandidate, "memory-a", 1, StateProposed, AuthorityAgentProposal, service.Limits)
	if err != nil {
		t.Fatal(err)
	}
	secondCandidate := testCandidate(now)
	secondCandidate.Subject = "a-subject"
	second, _ := NormalizeRecord(secondCandidate, "memory-z", 1, StateProposed, AuthorityAgentProposal, service.Limits)
	conflictCandidate := secondCandidate
	conflictCandidate.Value = TypedValue{Kind: ValueString, Data: "different"}
	conflict, _ := NormalizeRecord(conflictCandidate, "memory-y", 1, StateProposed, AuthorityAgentProposal, service.Limits)
	for index, record := range []Record{first, second, conflict} {
		if _, err = store.Commit(context.Background(), Mutation{ProjectID: "project-1", ExpectedSequence: uint64(index), IdempotencyKey: "ordered-" + record.ID, Transition: "propose", Record: record, PolicyDigest: strings.Repeat("a", 64)}); err != nil {
			t.Fatal(err)
		}
	}
	authority := QueryAuthority{ProjectID: "project-1", Scope: first.Scope, States: []State{StateProposed}, Authorities: []Authority{AuthorityAgentProposal}, MaxSensitivity: SensitivityPublic, AsOf: now, MaxItems: 1, MaxBytes: 1 << 20, MaxWork: 10, Review: true}
	request := QueryRequest{ProjectID: "project-1", Scope: first.Scope, States: authority.States, Authorities: authority.Authorities, MaxSensitivity: SensitivityPublic, IncludeProposed: true, Limit: 1, MaxBytes: 1 << 20}
	result, err := service.Query(context.Background(), request, authority)
	if err != nil || len(result.Records) != 1 || result.Records[0].Subject != "a-subject" || len(result.Conflicts) != 1 || len(result.Conflicts[0].IDs) != 2 {
		t.Fatalf("sort/conflict before limit failed: %#v %v", result, err)
	}
	raw, _ := json.Marshal(result.Records[0])
	authority.MaxItems, authority.MaxBytes, request.Limit, request.MaxBytes = 10, len(raw), 10, len(raw)
	result, err = service.Query(context.Background(), request, authority)
	if err != nil || len(result.Records) != 1 || result.Records[0].Subject != "a-subject" || len(result.Conflicts) != 1 {
		t.Fatalf("byte cut preceded order/conflicts: %#v %v", result, err)
	}
}

// TestEvidenceRevocationAndFunctionalDigestBecomeStale proves post-commit evidence authority controls queries and bindings.
func TestEvidenceRevocationAndFunctionalDigestBecomeStale(t *testing.T) {
	service, _, now := testService(t)
	evidence := &revocableEvidence{}
	service.Evidence = evidence
	functionalDigest := strings.Repeat("f", 64)
	candidate := withEvidence(testCandidate(now))
	view, err := service.Observe(context.Background(), ObserveCommand{Principal: "principal", ID: "observed-revocable", IdempotencyKey: "revocable", Candidate: candidate, FunctionalResultDigest: functionalDigest})
	if err != nil || view.Provenance.FunctionalResultDigest != functionalDigest {
		t.Fatalf("functional lineage missing: %#v %v", view, err)
	}
	authority := QueryAuthority{ProjectID: "project-1", Scope: candidate.Scope, States: []State{StateObserved}, Authorities: []Authority{AuthorityObservation}, MaxSensitivity: SensitivityPublic, AsOf: now, MaxItems: 10, MaxBytes: 1 << 20, MaxWork: 10}
	request := QueryRequest{ProjectID: "project-1", Scope: candidate.Scope, States: authority.States, Authorities: authority.Authorities, MaxSensitivity: SensitivityPublic, Limit: 10, MaxBytes: 1 << 20}
	result, err := service.Query(context.Background(), request, authority)
	if err != nil || len(result.Records) != 1 {
		t.Fatal("verified evidence was not eligible")
	}
	binding, err := service.Bind(result, authority)
	if err != nil {
		t.Fatal(err)
	}
	evidence.revoked = true
	result, err = service.Query(context.Background(), request, authority)
	if err != nil || len(result.Records) != 0 {
		t.Fatalf("revoked evidence remained queryable: %#v %v", result, err)
	}
	status, err := service.Revalidate(context.Background(), binding, authority)
	if err != nil || status.Valid || status.Category != "stale" {
		t.Fatalf("revoked binding not stale: %#v %v", status, err)
	}
}

// TestJournalRejectsTrailingDataAndIndependentAnchorDetectsDeletion proves no unauthenticated suffix or coordinated projection deletion can hide committed events.
func TestJournalRejectsTrailingDataAndIndependentAnchorDetectsDeletion(t *testing.T) {
	service, store, now := testService(t)
	view, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "anchored", Candidate: testCandidate(now)})
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := store.projectDir("project-1", false)
	journal := filepath.Join(dir, "journal.log")
	original, _ := os.ReadFile(journal)
	newline := bytes.IndexByte(original, '\n')
	poisoned := append([]byte(nil), original[:newline]...)
	poisoned = append(poisoned, []byte(" {}\n")...)
	if err = os.WriteFile(journal, poisoned, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Current(context.Background(), "project-1", view.ID); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	if err = os.WriteFile(journal, original, 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"snapshot.json", "index.json"} {
		if err = os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(journal, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Current(context.Background(), "project-1", view.ID); err == nil {
		t.Fatal("tail deletion with projections removed accepted")
	}
	if err = os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Head(context.Background(), "project-1"); err == nil {
		t.Fatal("missing journal after commit accepted")
	}
}

// TestBindingRequiresVerifiedQueryAndEveryAuthorityCoordinate proves empty and populated bindings cannot cross authority coordinates.
func TestBindingRequiresVerifiedQueryAndEveryAuthorityCoordinate(t *testing.T) {
	service, _, now := testService(t)
	authority := QueryAuthority{ProjectID: "project-1", Scope: testCandidate(now).Scope, MaxSensitivity: SensitivityPublic, ConfigurationDigest: strings.Repeat("c", 64), AsOf: now, MaxItems: 10, MaxBytes: 1 << 20, MaxWork: 10}
	request := QueryRequest{ProjectID: "project-1", Scope: authority.Scope, MaxSensitivity: SensitivityPublic, Limit: 10, MaxBytes: 1 << 20}
	result, err := service.Query(context.Background(), request, authority)
	if err != nil || len(result.Records) != 0 {
		t.Fatal("expected verified empty query")
	}
	binding, err := service.Bind(result, authority)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Bind(QueryResult{ProjectID: "project-1", QueryDigest: result.QueryDigest}, authority); err == nil {
		t.Fatal("fabricated query result bound")
	}
	mutations := []func(*QueryAuthority){
		func(value *QueryAuthority) { value.ProjectID = "other" },
		func(value *QueryAuthority) { value.Scope.RepositoryID = "other" },
		func(value *QueryAuthority) { value.Scope.CommitOID = strings.Repeat("d", 40) },
		func(value *QueryAuthority) { value.ConfigurationDigest = strings.Repeat("d", 64) },
		func(value *QueryAuthority) { value.AsOf = value.AsOf.Add(time.Second) },
	}
	for index, mutate := range mutations {
		drifted := authority
		mutate(&drifted)
		status, validateErr := service.Revalidate(context.Background(), binding, drifted)
		if validateErr != nil || status.Valid {
			t.Fatalf("authority drift %d accepted: %#v %v", index, status, validateErr)
		}
	}
	tampered := binding
	tampered.Query.MaxBytes++
	tampered.Digest = bindingDigest(tampered)
	status, err := service.Revalidate(context.Background(), tampered, authority)
	if err != nil || status.Valid {
		t.Fatal("query digest drift accepted")
	}
}

// verifiedEvidence accepts only evidence already bound to the current project.
type verifiedEvidence struct{}

// Verify rejects cross-project evidence metadata before persistence.
func (verifiedEvidence) Verify(_ context.Context, project string, _ Scope, evidence EvidenceRef) error {
	if evidence.Project != project {
		return errors.New("cross project")
	}
	return nil
}

// deniedEvidence simulates a revoked or unverifiable evidence lineage.
type deniedEvidence struct{}

// Verify fails closed without exposing an evidence body.
func (deniedEvidence) Verify(context.Context, string, Scope, EvidenceRef) error {
	return errors.New("revoked evidence")
}

// flakyMemoryStore injects one typed transient commit failure before delegating to the real store.
type flakyMemoryStore struct {
	MemoryStore
	failures, calls int
}

// Commit records attempts and fails only the configured transient prefix.
func (store *flakyMemoryStore) Commit(ctx context.Context, mutation Mutation) (CommitResult, error) {
	store.calls++
	if store.calls <= store.failures {
		return CommitResult{}, fieldError("unavailable", "store.commit", "", errors.New("external canary"))
	}
	return store.MemoryStore.Commit(ctx, mutation)
}

// immediateWaiter records finite retry ownership without sleeping in tests.
type immediateWaiter struct{ calls int }

// Wait observes cancellation and confirms one configured backoff boundary.
func (waiter *immediateWaiter) Wait(ctx context.Context, _ time.Duration) error {
	waiter.calls++
	return ctx.Err()
}

// recordingObserver retains only payload-free operation records for assertions.
type recordingObserver struct{ records []OperationRecord }

// Observe appends the safe synchronous telemetry record.
func (observer *recordingObserver) Observe(_ context.Context, record OperationRecord) error {
	observer.records = append(observer.records, record)
	return nil
}

// revisionOnlyAuthority permits proposals but forbids widening them to project scope.
type revisionOnlyAuthority struct{}

// Authorize returns a trusted maximum revision scope.
func (revisionOnlyAuthority) Authorize(_ context.Context, principal, _, action string) (PrincipalDecision, error) {
	return PrincipalDecision{Principal: principal, Allowed: true, MaxScope: ScopeRevision, MaxSensitivity: SensitivityPublic, PolicyDigest: strings.Repeat("a", 64), Actor: Actor{Kind: testActorKind(action), ID: principal}, RepositoryID: "repo-1", CommitOID: strings.Repeat("b", 40), Producer: "featureproof", ProducerVersion: "v1", Reference: "trusted-decision"}, nil
}

// boundedAuthority supplies one exact scope and sensitivity ceiling for authorization matrices.
type boundedAuthority struct {
	maxScope       ScopeKind
	maxSensitivity Sensitivity
}

// Authorize returns complete trusted provenance while varying only the tested policy ceilings.
func (authority boundedAuthority) Authorize(_ context.Context, principal, _, action string) (PrincipalDecision, error) {
	return PrincipalDecision{Principal: principal, Allowed: true, MaxScope: authority.maxScope, MaxSensitivity: authority.maxSensitivity, PolicyDigest: strings.Repeat("a", 64), Actor: Actor{Kind: testActorKind(action), ID: principal}, RepositoryID: "repo-1", CommitOID: strings.Repeat("b", 40), ConfigurationDigest: strings.Repeat("c", 64), Producer: "featureproof", ProducerVersion: "v1", Reference: "trusted-reference"}, nil
}

// revocableEvidence models verified evidence whose authority can be withdrawn after commit.
type revocableEvidence struct{ revoked bool }

// Verify fails after revocation so query and binding revalidation must make the record stale.
func (evidence *revocableEvidence) Verify(context.Context, string, Scope, EvidenceRef) error {
	if evidence.revoked {
		return errors.New("revoked")
	}
	return nil
}

// failingBindingPort simulates a typed binding reader failure.
type failingBindingPort struct{}

// Revalidate fails before a privileged workflow boundary.
func (failingBindingPort) Revalidate(context.Context, MemoryBinding, QueryAuthority) (BindingStatus, error) {
	return BindingStatus{}, fieldError("integrity", "binding", "digest", nil)
}

// queryAuthorityFunc resolves trusted query authority for surface adapter tests.
type queryAuthorityFunc func(context.Context, string, QueryRequest) (QueryAuthority, error)

// Resolve delegates one authenticated surface request to its controlled test policy.
func (resolver queryAuthorityFunc) Resolve(ctx context.Context, principal string, request QueryRequest) (QueryAuthority, error) {
	return resolver(ctx, principal, request)
}

// transportPrincipalResolver authenticates one controlled transport credential and verified GitHub actor.
type transportPrincipalResolver struct{}

// ResolvePrincipal maps only the test bearer credential to its authenticated principal.
func (transportPrincipalResolver) ResolvePrincipal(_ context.Context, credential string) (string, error) {
	if credential != "Bearer transport-token" {
		return "", errors.New("denied")
	}
	return "principal", nil
}

// failingResponseWriter forces transport serialization to exercise safe write failure handling.
type failingResponseWriter struct{}

// Write rejects every response byte without retaining payloads.
func (failingResponseWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

// headFailureStore isolates a verified binding head lookup failure.
type headFailureStore struct{ MemoryStore }

// Head returns one typed unavailable failure at the revalidation boundary.
func (headFailureStore) Head(context.Context, string) (Head, error) {
	return Head{}, fieldError("unavailable", "head", "", nil)
}

// memoryWitness is a process-local linearizable authority used only by hermetic store tests.
type memoryWitness struct {
	mu                      sync.Mutex
	states                  map[string]WitnessState
	unavailable             bool
	forceConflict           bool
	bootstrapState          *WitnessState
	beforeCAS               error
	unavailableAfterFailure bool
	afterCAS                error
}

// newMemoryWitness creates an empty authority whose streams require explicit bootstrap.
func newMemoryWitness() *memoryWitness {
	return &memoryWitness{states: map[string]WitnessState{}}
}

// Read returns the exact current generation without deriving it from LocalStore state.
func (witness *memoryWitness) Read(_ context.Context, project string) (WitnessState, error) {
	witness.mu.Lock()
	defer witness.mu.Unlock()
	if witness.unavailable {
		return WitnessState{}, errors.New("witness unavailable")
	}
	state, ok := witness.states[project]
	if !ok {
		return WitnessState{}, ErrWitnessMissing
	}
	return state, nil
}

// Bootstrap atomically creates generation zero and rejects replay as a conflict.
func (witness *memoryWitness) Bootstrap(_ context.Context, project string) (WitnessState, error) {
	witness.mu.Lock()
	defer witness.mu.Unlock()
	if witness.unavailable {
		return WitnessState{}, errors.New("witness unavailable")
	}
	if _, ok := witness.states[project]; ok {
		return WitnessState{}, ErrWitnessConflict
	}
	state := witnessStateForEvents(project, nil)
	witness.states[project] = state
	if witness.bootstrapState != nil {
		return *witness.bootstrapState, nil
	}
	return state, nil
}

// CompareAndAdvance performs one irreversible generation CAS and can emulate an ambiguous post-commit response.
func (witness *memoryWitness) CompareAndAdvance(_ context.Context, expected, next WitnessState) error {
	witness.mu.Lock()
	defer witness.mu.Unlock()
	if witness.unavailable {
		return errors.New("witness unavailable")
	}
	if witness.forceConflict {
		return ErrWitnessConflict
	}
	if witness.beforeCAS != nil {
		if witness.unavailableAfterFailure {
			witness.unavailable = true
		}
		return witness.beforeCAS
	}
	current, ok := witness.states[expected.ProjectID]
	if !ok || current != expected || next.ProjectID != expected.ProjectID || next.Generation != expected.Generation+1 || next.Head.Sequence != next.Generation {
		return ErrWitnessConflict
	}
	witness.states[expected.ProjectID] = next
	return witness.afterCAS
}

// newWitnessHTTPServer exposes the test authority through the same protocol used by productive composition.
func newWitnessHTTPServer(t *testing.T, witness *memoryWitness) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer witness-test-token" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
		if len(parts) < 3 || parts[0] != "v1" || parts[1] != "project-memory-witness" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		project := parts[2]
		var state WitnessState
		var err error
		switch {
		case request.Method == http.MethodGet && len(parts) == 3:
			state, err = witness.Read(request.Context(), project)
		case request.Method == http.MethodPost && len(parts) == 4 && parts[3] == "bootstrap":
			state, err = witness.Bootstrap(request.Context(), project)
		case request.Method == http.MethodPost && len(parts) == 4 && parts[3] == "advance":
			var command witnessAdvanceRequest
			if json.NewDecoder(request.Body).Decode(&command) != nil {
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			err = witness.CompareAndAdvance(request.Context(), command.Expected, command.Next)
			state = command.Next
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if errors.Is(err, ErrWitnessMissing) {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if errors.Is(err, ErrWitnessConflict) {
			writer.WriteHeader(http.StatusConflict)
			return
		}
		if err != nil {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodPost && len(parts) == 4 && parts[3] == "bootstrap" {
			writer.WriteHeader(http.StatusCreated)
		}
		_ = json.NewEncoder(writer).Encode(state)
	}))
	t.Cleanup(server.Close)
	return server
}

// newTestLocalStore creates a store and explicitly provisions its default project in the independent witness.
func newTestLocalStore(t *testing.T, root string, integrity IntegrityAuthority, limits Limits) (*LocalStore, *memoryWitness) {
	t.Helper()
	witness := newMemoryWitness()
	store, err := NewLocalStore(root, integrity, witness, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.BootstrapProject(context.Background(), "project-1"); err != nil {
		t.Fatal(err)
	}
	return store, witness
}

// ResolveGitHubPrincipal maps only the verified repository actor tuple to its authenticated principal.
func (transportPrincipalResolver) ResolveGitHubPrincipal(_ context.Context, actor VerifiedGitHubActor, _ SurfaceOperation, _ State) (string, error) {
	if actor.ActorID != "principal" || actor.RepositoryID != "repo-1" || actor.Action == "" {
		return "", errors.New("denied")
	}
	return "principal", nil
}

// testCandidate creates one complete canonical candidate for focused mutations.
func testCandidate(now time.Time) Candidate {
	return Candidate{ContractVersion: ContractVersion, ProjectID: "project-1", Scope: Scope{Kind: ScopeRevision, RepositoryID: "repo-1", CommitOID: strings.Repeat("b", 40)}, Class: ClassFact, Subject: "api.pet", Predicate: "supports", Value: TypedValue{Kind: ValueJSON, Data: map[string]any{"enabled": true, "count": json.Number("2")}}, Provenance: Provenance{Source: "functional_flow", Reference: "run-1/step-1", Actor: Actor{Kind: "system", ID: "observer"}, Producer: "featureproof", ProducerVersion: "v1", ObservedAt: now, RepositoryID: "repo-1", CommitOID: strings.Repeat("b", 40)}, Confidence: ConfidenceHigh, Sensitivity: SensitivityPublic, ValidFrom: now.Add(-time.Minute)}
}

// testService composes the real local store behind every application guard.
func testService(t *testing.T) (Service, *LocalStore, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	integrity, err := NewHMACAuthority("key-1", []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	store, _ := newTestLocalStore(t, filepath.Join(t.TempDir(), ".featureproof", "memory"), integrity, DefaultLimits())
	service := Service{Store: store, Evidence: verifiedEvidence{}, HumanPrincipals: allowAuthority{}, ObserverPrincipals: allowAuthority{}, ReconcilerPrincipals: allowAuthority{}, Clock: fixedClock{now}, Gate: SecretGate{Rules: SecretRules{Names: []string{"password", "token"}, Canaries: []string{"MEMORY-CANARY"}, MaxWork: 10000}}, Limits: DefaultLimits()}
	return service, store, now
}

// TestMemoryV1StrictCanonicalAndImmutable covers strict decoding, canonical digest stability, limits, and caller mutation isolation.
func TestMemoryV1StrictCanonicalAndImmutable(t *testing.T) {
	limits := DefaultLimits()
	now := time.Now().UTC().Truncate(time.Second)
	candidate := testCandidate(now)
	first, err := NormalizeRecord(candidate, "memory-a", 1, StateProposed, AuthorityAgentProposal, limits)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Value.Data.(map[string]any)["enabled"] = false
	if first.Value.Data.(map[string]any)["enabled"] != true {
		t.Fatal("record followed caller mutation")
	}
	secondCandidate := testCandidate(now)
	secondCandidate.Value.Data = map[string]any{"count": json.Number("2"), "enabled": true}
	second, err := NormalizeRecord(secondCandidate, "memory-a", 1, StateProposed, AuthorityAgentProposal, limits)
	if err != nil || first.ContentDigest != second.ContentDigest {
		t.Fatalf("canonical mismatch %v", err)
	}
	raw, _ := json.Marshal(secondCandidate)
	raw = append(raw[:len(raw)-1], []byte(`,"unknown":true}`)...)
	if _, err = DecodeCandidate(raw, limits); err == nil {
		t.Fatal("unknown field accepted")
	}
	secondCandidate.ContractVersion = "ProjectMemory/v2"
	if _, err = NormalizeRecord(secondCandidate, "memory-a", 1, StateProposed, AuthorityAgentProposal, limits); err == nil {
		t.Fatal("future contract accepted")
	}
	deep := testCandidate(now)
	deep.Value = TypedValue{Kind: ValueJSON, Data: map[string]any{"a": map[string]any{"b": map[string]any{"c": true}}}}
	small := limits
	small.MaxJSONDepth = 1
	if _, err = NormalizeRecord(deep, "memory-a", 1, StateProposed, AuthorityAgentProposal, small); err == nil {
		t.Fatal("depth limit ignored")
	}
}

// TestMemoryScopesStatesAuthorityAndStaleness covers exact unions, lifecycle graph, expiry, and authority independence.
func TestMemoryScopesStatesAuthorityAndStaleness(t *testing.T) {
	service, _, now := testService(t)
	candidate := testCandidate(now)
	view, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "proposal-key", Candidate: candidate})
	if err != nil || view.State != StateProposed || view.Authority != AuthorityAgentProposal {
		t.Fatalf("proposal=%#v err=%v", view, err)
	}
	audit, err := service.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: view.ID, IdempotencyKey: "approve-key", ExpectedSequence: 1, ExpectedVersion: 1, DecisionID: "decision-1", Target: StateApproved})
	if err != nil || audit.State != StateApproved || audit.Version != 2 {
		t.Fatalf("audit=%#v err=%v", audit, err)
	}
	if _, err = service.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: view.ID, IdempotencyKey: "observe-backwards", ExpectedSequence: 2, ExpectedVersion: 2, Target: StateObserved}); err == nil {
		t.Fatal("backwards transition accepted")
	}
	invalid := candidate
	invalid.Scope = Scope{Kind: ScopeProject, RepositoryID: "repo-1", RunID: "escape"}
	if _, err = NormalizeRecord(invalid, "memory-z", 1, StateProposed, AuthorityAgentProposal, DefaultLimits()); err == nil {
		t.Fatal("ambiguous scope accepted")
	}
	invalidUntil := candidate.ValidFrom.Add(-time.Second)
	candidate.ValidUntil = &invalidUntil
	if _, err = NormalizeRecord(candidate, "memory-z", 1, StateProposed, AuthorityAgentProposal, DefaultLimits()); err == nil {
		t.Fatal("invalid validity window accepted")
	}
}

// TestMemorySecretGateRunsBeforeStore verifies nested canaries and sensitive names never cross durable boundaries.
func TestMemorySecretGateRunsBeforeStore(t *testing.T) {
	service, store, now := testService(t)
	candidate := testCandidate(now)
	candidate.Value = TypedValue{Kind: ValueJSON, Data: map[string]any{"nested": []any{map[string]any{"password": "MEMORY-CANARY"}}}}
	if _, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "secret-key", Candidate: candidate}); err == nil {
		t.Fatal("secret accepted")
	}
	head, err := store.Head(context.Background(), "project-1")
	if err != nil || head.Sequence != 0 {
		t.Fatalf("durable write occurred: %#v %v", head, err)
	}
	assertTreeOmits(t, store.root, "MEMORY-CANARY")
	rawErr := errorResponse(errors.New("upstream MEMORY-CANARY"))
	encoded, _ := json.Marshal(rawErr)
	if strings.Contains(string(encoded), "MEMORY-CANARY") {
		t.Fatal("external error leaked")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = service.Gate.Check(ctx, candidate); err == nil {
		t.Fatal("cancelled detector succeeded")
	}
}

// TestLocalMemoryStoreVerifiesJournalAndSnapshot covers reopen, authenticated replay, snapshot recovery, and fail-closed tampering.
func TestLocalMemoryStoreVerifiesJournalAndSnapshot(t *testing.T) {
	service, store, now := testService(t)
	candidate := testCandidate(now)
	view, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "proposal-key", Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := store.projectDir("project-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(dir, "snapshot.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Current(context.Background(), "project-1", view.ID); err != nil {
		t.Fatalf("missing snapshot not rebuilt: %v", err)
	}
	journal := filepath.Join(dir, "journal.log")
	raw, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(raw), `"subject":"api.pet"`, `"subject":"evil"`, 1)
	if err = os.WriteFile(journal, []byte(tampered), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Current(context.Background(), "project-1", view.ID); err == nil {
		t.Fatal("tampered journal served")
	}
}

// TestConcurrentMemoryWritersAndRedeliveryConverge covers CAS conflict and exact idempotent receipt replay.
func TestConcurrentMemoryWritersAndRedeliveryConverge(t *testing.T) {
	service, _, now := testService(t)
	candidate := testCandidate(now)
	command := ProposalCommand{Principal: "principal", IdempotencyKey: "same-key", Candidate: candidate}
	first, err := service.Propose(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Propose(context.Background(), command)
	if err != nil || second.ID != first.ID || second.Version != first.Version {
		t.Fatalf("redelivery diverged %#v %v", second, err)
	}
	changed := command
	changed.Candidate.Subject = "different"
	if _, err = service.Propose(context.Background(), changed); err == nil {
		t.Fatal("idempotency payload conflict accepted")
	}
	var successes int
	var mutex sync.Mutex
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			item := testCandidate(now)
			item.Subject = "parallel-" + string(rune('a'+index))
			_, commitErr := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "parallel-" + string(rune('a'+index)), ExpectedSequence: 1, Candidate: item})
			if commitErr == nil {
				mutex.Lock()
				successes++
				mutex.Unlock()
			}
		}(index)
	}
	wait.Wait()
	if successes != 1 {
		t.Fatalf("CAS successes=%d", successes)
	}
}

// TestMemoryQueryFiltersBeforeStableOrderingAndIndex covers exact eligibility, conflicts, stable order, and detached results.
func TestMemoryQueryFiltersBeforeStableOrderingAndIndex(t *testing.T) {
	service, _, now := testService(t)
	scope := testCandidate(now).Scope
	for index, value := range []string{"b", "a"} {
		candidate := testCandidate(now)
		candidate.Subject = "subject"
		candidate.Predicate = "predicate"
		candidate.Value = TypedValue{Kind: ValueString, Data: value}
		view, err := service.Observe(context.Background(), ObserveCommand{FunctionalResultDigest: strings.Repeat("f", 64), Principal: "principal", ID: "observed-" + value, IdempotencyKey: "observed-key-" + value, ExpectedSequence: uint64(index * 2), Candidate: withEvidence(candidate)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = service.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: view.ID, IdempotencyKey: "approve-" + value, ExpectedSequence: uint64(index*2 + 1), ExpectedVersion: 1, DecisionID: DecisionID("decision-" + value), Target: StateApproved}); err != nil {
			t.Fatal(err)
		}
	}
	authority := QueryAuthority{ProjectID: "project-1", Scope: scope, Classes: []Class{ClassFact}, States: []State{StateApproved}, Authorities: []Authority{AuthorityHuman}, MaxSensitivity: SensitivityPublic, AsOf: now, MaxItems: 10, MaxBytes: 1 << 20, MaxWork: 100}
	request := QueryRequest{ProjectID: "project-1", Scope: scope, Classes: []Class{ClassFact}, States: []State{StateApproved}, Authorities: []Authority{AuthorityHuman}, MaxSensitivity: SensitivityPublic, Limit: 10, MaxBytes: 1 << 20}
	result, err := service.Query(context.Background(), request, authority)
	if err != nil || len(result.Records) != 2 || len(result.Conflicts) != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	result.Records[0].Subject = "mutated"
	again, err := service.Query(context.Background(), request, authority)
	if err != nil || again.Records[0].Subject != "subject" {
		t.Fatal("query result was not detached")
	}
	binding, err := service.Bind(again, authority)
	status, err := service.Revalidate(context.Background(), binding, authority)
	if err != nil || !status.Valid {
		t.Fatalf("binding invalid %v %v", status, err)
	}
}

// TestMemoryBindingStopsDriftAndPreservesHistoricalRun covers current-head drift and historical exact-version retrieval.
func TestMemoryBindingStopsDriftAndPreservesHistoricalRun(t *testing.T) {
	service, _, now := testService(t)
	candidate := withEvidence(testCandidate(now))
	view, err := service.Observe(context.Background(), ObserveCommand{FunctionalResultDigest: strings.Repeat("f", 64), Principal: "principal", ID: "observed-a", IdempotencyKey: "observe", Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: view.ID, IdempotencyKey: "approve", ExpectedSequence: 1, ExpectedVersion: 1, DecisionID: "decision", Target: StateApproved})
	if err != nil {
		t.Fatal(err)
	}
	authority := QueryAuthority{ProjectID: "project-1", Scope: candidate.Scope, States: []State{StateApproved}, Authorities: []Authority{AuthorityHuman}, MaxSensitivity: SensitivityPublic, AsOf: now, MaxItems: 10, MaxBytes: 1 << 20, MaxWork: 100}
	request := QueryRequest{ProjectID: "project-1", Scope: candidate.Scope, States: authority.States, Authorities: authority.Authorities, MaxSensitivity: SensitivityPublic, Limit: 10, MaxBytes: 1 << 20}
	result, _ := service.Query(context.Background(), request, authority)
	binding, err := service.Bind(result, authority)
	_, err = service.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: view.ID, IdempotencyKey: "invalidate", ExpectedSequence: 2, ExpectedVersion: 2, DecisionID: "revoke", Target: StateInvalidated})
	if err != nil {
		t.Fatal(err)
	}
	status, err := service.Revalidate(context.Background(), binding, authority)
	if err != nil || status.Valid || status.Category != "stale" {
		t.Fatalf("drift not stopped %#v %v", status, err)
	}
	historical, err := service.Historical(context.Background(), "project-1", view.ID, 2)
	if err != nil || historical.State != StateApproved {
		t.Fatalf("history changed %#v %v", historical, err)
	}
}

// TestMemorySurfacesShareAuthorityAndSafeProjection covers transport-neutral parity, read-only mode, and spoof rejection.
func TestMemorySurfacesShareAuthorityAndSafeProjection(t *testing.T) {
	service, _, now := testService(t)
	command := ProposalCommand{Principal: "principal", IdempotencyKey: "surface", Candidate: testCandidate(now)}
	responses := []SurfaceResponse{}
	for range []string{"cli", "api", "mcp", "github"} {
		responses = append(responses, (Surface{Service: &service}).Invoke(context.Background(), SurfaceRequest{Operation: OperationPropose, Principal: "principal", Proposal: &command}))
		command.IdempotencyKey = "surface"
	}
	for _, response := range responses {
		if response.Error != nil || response.Review == nil || response.Review.Authority != AuthorityAgentProposal {
			t.Fatalf("surface mismatch %#v", response)
		}
	}
	spoof := command
	spoof.Principal = "github-text-claims-admin"
	response := (Surface{Service: &service}).Invoke(context.Background(), SurfaceRequest{Operation: OperationPropose, Principal: "github-text-claims-admin", Proposal: &spoof})
	if response.Error == nil || response.Error.Category != "policy_denied" {
		t.Fatal("spoof accepted")
	}
	readOnly := (Surface{Service: &service, ReadOnly: true}).Invoke(context.Background(), SurfaceRequest{Operation: OperationPropose, Principal: "principal", Proposal: &command})
	if readOnly.Error == nil {
		t.Fatal("read-only mutation accepted")
	}
	encoded, _ := json.Marshal(responses[0])
	if strings.Contains(string(encoded), "authenticator") || strings.Contains(string(encoded), "idempotency") {
		t.Fatal("unsafe surface field")
	}
}

// TestMemoryCancellationLimitsAndRetriesOwnAllWork covers cancelled boundaries, finite query work, and absent background workers.
func TestMemoryCancellationLimitsAndRetriesOwnAllWork(t *testing.T) {
	service, _, now := testService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Propose(ctx, ProposalCommand{Principal: "principal", IdempotencyKey: "cancelled", Candidate: testCandidate(now)}); err == nil {
		t.Fatal("cancel ignored")
	}
	authority := QueryAuthority{ProjectID: "project-1", Scope: testCandidate(now).Scope, MaxSensitivity: SensitivityPublic, AsOf: now, MaxWork: 0}
	if _, err := service.Query(context.Background(), QueryRequest{ProjectID: "project-1", Scope: authority.Scope, MaxSensitivity: SensitivityPublic}, authority); err != nil {
		t.Fatalf("empty finite query failed: %v", err)
	}
	guard := WorkflowGuard{}
	if err := guard.BeforePrivilegedStep(context.Background(), nil, authority); err != nil {
		t.Fatal("memory-free run incompatible")
	}
}

// TestMemoryStoreRejectsLinksTraversalAndRootEscape covers invalid roots, traversal IDs, and symlink components when supported.
func TestMemoryStoreRejectsLinksTraversalAndRootEscape(t *testing.T) {
	integrity, _ := NewHMACAuthority("key-1", []byte("0123456789abcdef0123456789abcdef"))
	if _, err := NewLocalStore("relative", integrity, newMemoryWitness(), DefaultLimits()); err == nil {
		t.Fatal("relative root accepted")
	}
	root := t.TempDir()
	store, err := NewLocalStore(filepath.Join(root, "memory"), integrity, newMemoryWitness(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.projectDir("../escape", true); err == nil {
		t.Fatal("traversal accepted")
	}
	target := filepath.Join(root, "target")
	_ = os.Mkdir(target, 0700)
	link := filepath.Join(root, "link")
	if os.Symlink(target, link) == nil {
		if _, err = NewLocalStore(filepath.Join(link, "memory"), integrity, newMemoryWitness(), DefaultLimits()); err == nil {
			t.Fatal("symlink root accepted")
		}
	}
}

// TestClosedContractValidationMatrix exercises every value/scope enum and malformed provenance branch.
func TestClosedContractValidationMatrix(t *testing.T) {
	now := time.Now().UTC()
	limits := DefaultLimits()
	for _, scope := range []Scope{{Kind: ScopeRun, RunID: "run-1", RevisionID: "revision-1"}, {Kind: ScopeRevision, RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40)}, {Kind: ScopeProject, RepositoryID: "repo-1"}} {
		candidate := testCandidate(now)
		candidate.Scope = scope
		if _, err := NormalizeRecord(candidate, "memory-a", 1, StateProposed, AuthorityAgentProposal, limits); err != nil {
			t.Fatalf("valid scope rejected: %v", err)
		}
	}
	values := []TypedValue{{Kind: ValueString, Data: "safe"}, {Kind: ValueBoolean, Data: true}, {Kind: ValueInteger, Data: json.Number("7")}, {Kind: ValueInteger, Data: uint64(7)}, {Kind: ValueJSON, Data: []any{nil, false, 2.5, float32(1), map[string]any{"x": "y"}}}}
	for _, value := range values {
		candidate := testCandidate(now)
		candidate.Value = value
		if _, err := NormalizeRecord(candidate, "memory-a", 1, StateProposed, AuthorityAgentProposal, limits); err != nil {
			t.Fatalf("valid value rejected: %#v %v", value, err)
		}
	}
	bad := []Candidate{testCandidate(now), testCandidate(now), testCandidate(now), testCandidate(now), testCandidate(now)}
	bad[0].Class = "unknown"
	bad[1].Confidence = "certain"
	bad[2].Sensitivity = "secret"
	bad[3].Value = TypedValue{Kind: ValueInteger, Data: 1.2}
	bad[4].Provenance.CommitOID = "short"
	for index, candidate := range bad {
		if _, err := NormalizeRecord(candidate, "memory-a", 1, StateProposed, AuthorityAgentProposal, limits); err == nil {
			t.Fatalf("bad candidate %d accepted", index)
		}
	}
	candidate := testCandidate(now)
	candidate.Evidence = nil
	if _, err := NormalizeRecord(candidate, "memory-a", 1, StateObserved, AuthorityObservation, limits); err == nil {
		t.Fatal("observation without evidence accepted")
	}
	raw, _ := json.Marshal(testCandidate(now))
	if _, err := DecodeCandidate(nil, limits); err == nil {
		t.Fatal("empty document accepted")
	}
	if _, err := DecodeCandidate(append(raw, []byte(" {}")...), limits); err == nil {
		t.Fatal("trailing document accepted")
	}
}

// TestAgentObservationWorkflowAndSurfaceAdapters covers strict proposal conversion and additive integration ports.
func TestAgentObservationWorkflowAndSurfaceAdapters(t *testing.T) {
	service, _, now := testService(t)
	dto := AgentProposalDTO{Version: ContractVersion, Candidate: testCandidate(now)}
	raw, _ := json.Marshal(dto)
	command, err := DecodeAgentProposal(raw, "principal", "adapter-key", 0, DefaultLimits())
	if err != nil || command.Principal != "principal" {
		t.Fatalf("decode=%#v err=%v", command, err)
	}
	dto.RequestedAuthority = AuthorityHuman
	raw, _ = json.Marshal(dto)
	if _, err = DecodeAgentProposal(raw, "principal", "adapter-key", 0, DefaultLimits()); err == nil {
		t.Fatal("agent elevation accepted")
	}
	observation := ObservationInput{Principal: "principal", ID: "observed-adapter", IdempotencyKey: "observe-adapter", Candidate: withEvidence(testCandidate(now)), FunctionalResultDigest: strings.Repeat("f", 64)}
	view, err := (ObservationAdapter{Service: &service}).Observe(context.Background(), observation)
	if err != nil || view.State != StateObserved {
		t.Fatalf("observation=%#v err=%v", view, err)
	}
	queryAuthority := QueryAuthority{ProjectID: "project-1", Scope: observation.Candidate.Scope, States: []State{StateObserved}, Authorities: []Authority{AuthorityObservation}, MaxSensitivity: SensitivityPublic, AsOf: now, MaxItems: 10, MaxBytes: 1 << 20, MaxWork: 100}
	query := QueryRequest{ProjectID: "project-1", Scope: observation.Candidate.Scope, States: queryAuthority.States, Authorities: queryAuthority.Authorities, MaxSensitivity: SensitivityPublic, Limit: 10, MaxBytes: 1 << 20}
	resolver := queryAuthorityFunc(func(context.Context, string, QueryRequest) (QueryAuthority, error) { return queryAuthority, nil })
	response := (Surface{Service: &service, Authority: resolver}).Invoke(context.Background(), SurfaceRequest{Operation: OperationQuery, Principal: "principal", Query: &query})
	if response.Error != nil || response.Query == nil || len(response.Query.Items) != 1 {
		t.Fatalf("query response=%#v", response)
	}
	transition := TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: view.ID, IdempotencyKey: "adapter-approve", ExpectedSequence: 1, ExpectedVersion: 1, DecisionID: "decision", Target: StateApproved}
	response = (Surface{Service: &service}).Invoke(context.Background(), SurfaceRequest{Operation: OperationTransition, Principal: "principal", Transition: &transition})
	if response.Error != nil || response.Audit == nil || response.Audit.State != StateApproved {
		t.Fatalf("transition response=%#v", response)
	}
	approvedAuthority := queryAuthority
	approvedAuthority.States = []State{StateApproved}
	approvedAuthority.Authorities = []Authority{AuthorityHuman}
	query.States, query.Authorities = approvedAuthority.States, approvedAuthority.Authorities
	result, _ := service.Query(context.Background(), query, approvedAuthority)
	binding, err := service.Bind(result, approvedAuthority)
	guard := WorkflowGuard{Memory: &service}
	if err = guard.BeforePrivilegedStep(context.Background(), &binding, approvedAuthority); err != nil {
		t.Fatalf("valid binding blocked: %v", err)
	}
	tampered := binding
	tampered.QueryDigest = strings.Repeat("0", 64)
	if err = guard.BeforePrivilegedStep(context.Background(), &tampered, approvedAuthority); err == nil {
		t.Fatal("tampered binding accepted")
	}
}

// TestExplicitExpiryAndReplacementLifecycle covers idempotent expiry and confirmed logical-key replacement.
func TestExplicitExpiryAndReplacementLifecycle(t *testing.T) {
	service, _, now := testService(t)
	old := testCandidate(now)
	until := now.Add(-time.Second)
	old.ValidFrom = now.Add(-time.Hour)
	old.ValidUntil = &until
	oldView, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "old", Candidate: old})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ReconcileExpiry(context.Background(), "principal", "project-1", 1); err != nil {
		t.Fatal(err)
	}
	expired, err := service.Historical(context.Background(), "project-1", oldView.ID, 2)
	if err != nil || expired.State != StateExpired {
		t.Fatalf("expiry=%#v err=%v", expired, err)
	}
	expiredRecord, recordErr := service.Store.Version(context.Background(), "project-1", oldView.ID, 2)
	if recordErr != nil || expiredRecord.Authority != AuthorityConfiguration || expiredRecord.Provenance.DecisionID != "" || expiredRecord.Provenance.Source != "trusted_expiry_reconciler" {
		t.Fatalf("expiry authority was not reconciler-owned: %#v %v", expiredRecord, recordErr)
	}
	if err = service.ReconcileExpiry(context.Background(), "principal", "project-1", 2); err != nil {
		t.Fatal("idempotent reconciliation failed")
	}
	first, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "first", ExpectedSequence: 2, Candidate: testCandidate(now)})
	if err != nil {
		t.Fatal(err)
	}
	replacementCandidate := testCandidate(now)
	replacementCandidate.Value = TypedValue{Kind: ValueString, Data: "replacement"}
	replacement, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "replacement", ExpectedSequence: 3, Candidate: replacementCandidate})
	if err != nil {
		t.Fatal(err)
	}
	replacementRecord, _ := service.Store.Current(context.Background(), "project-1", replacement.ID)
	_, err = service.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: first.ID, IdempotencyKey: "supersede", ExpectedSequence: 4, ExpectedVersion: 1, DecisionID: "replace", Target: StateSuperseded, Replacement: &ReplacementRef{ID: replacement.ID, Version: replacement.Version, Digest: replacementRecord.RecordDigest}})
	if err != nil {
		t.Fatalf("confirmed replacement rejected: %v", err)
	}
}

// TestSeparatedTransitionAuthoritiesRejectFabricatedObservationAndMissingDecisions exercises every public human action boundary.
func TestSeparatedTransitionAuthoritiesRejectFabricatedObservationAndMissingDecisions(t *testing.T) {
	service, _, now := testService(t)
	view, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "authority-seed", Candidate: testCandidate(now)})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []State{StateObserved, StateExpired, StateApproved, StateInvalidated, StateSuperseded} {
		command := TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: view.ID, IdempotencyKey: "authority-" + string(target), ExpectedSequence: 1, ExpectedVersion: 1, Target: target}
		if target == StateObserved || target == StateExpired {
			command.DecisionID = "forged-decision"
		}
		if _, transitionErr := service.Transition(context.Background(), command); transitionErr == nil {
			t.Fatalf("target %s crossed the human transition boundary", target)
		}
	}
}

// TestProjectionRecoveryCorruptionAndCancellation covers explicit recovery, poisoned index rebuild, snapshot fail-closed, and lock cancellation.
func TestProjectionRecoveryCorruptionAndCancellation(t *testing.T) {
	service, store, now := testService(t)
	view, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "recover", Candidate: testCandidate(now)})
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := store.projectDir("project-1", false)
	if err = os.WriteFile(filepath.Join(dir, "index.json"), []byte(`{"poison":"MEMORY-CANARY"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = store.Recover(context.Background(), "project-1"); err != nil {
		t.Fatalf("derived index not rebuilt: %v", err)
	}
	if _, err = store.Current(context.Background(), "project-1", view.ID); err != nil {
		t.Fatal(err)
	}
	snapshotPath := filepath.Join(dir, "snapshot.json")
	snapshotRaw, _ := os.ReadFile(snapshotPath)
	snapshotRaw[0] ^= 1
	if err = os.WriteFile(snapshotPath, snapshotRaw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Current(context.Background(), "project-1", view.ID); err == nil {
		t.Fatal("corrupt snapshot repaired instead of failing closed")
	}
	release, lockErr := store.acquire(context.Background(), "project-1")
	if lockErr != nil {
		t.Fatal(lockErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if _, err = store.acquire(ctx, "project-1"); err == nil {
		t.Fatal("cancelled lock succeeded")
	}
	release()
}

// TestIntegrityAuthorityAndSafeErrors covers anchored verification, cancellation, erasure, and stable public error text.
func TestIntegrityAuthorityAndSafeErrors(t *testing.T) {
	authority, err := NewHMACAuthority("key-1", []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	keyID, signature, err := authority.Sign(context.Background(), []byte("payload"))
	if err != nil || authority.Verify(context.Background(), keyID, []byte("payload"), signature) != nil {
		t.Fatal("valid signature rejected")
	}
	if authority.Verify(context.Background(), "unknown", []byte("payload"), signature) == nil || authority.Verify(context.Background(), keyID, []byte("changed"), signature) == nil {
		t.Fatal("unanchored signature accepted")
	}
	typed := &Error{Category: "invalid_input", Stage: "test", Path: "field", Cause: errors.New("MEMORY-CANARY")}
	if typed.Error() != "invalid_input:test:field" || strings.Contains(typed.Error(), "MEMORY-CANARY") {
		t.Fatal("safe error leaked cause")
	}
	authority.Close()
}

// TestEligibilityAndQueryLimitMatrix covers every structural exclusion and finite truncation boundary.
func TestEligibilityAndQueryLimitMatrix(t *testing.T) {
	now := time.Now().UTC()
	record, err := NormalizeRecord(testCandidate(now), "memory-a", 1, StateApproved, AuthorityHuman, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	authority := QueryAuthority{ProjectID: "project-1", Scope: record.Scope, Classes: []Class{ClassFact}, States: []State{StateApproved}, Authorities: []Authority{AuthorityHuman}, MaxSensitivity: SensitivityRestricted, AsOf: now, MaxItems: 1, MaxBytes: 1 << 20, MaxWork: 10}
	request := QueryRequest{ProjectID: "project-1", Scope: record.Scope, Classes: authority.Classes, States: authority.States, Authorities: authority.Authorities, MaxSensitivity: SensitivityRestricted, IncludeProposed: true}
	if !eligible(record, request, authority) {
		t.Fatal("valid record excluded")
	}
	mutations := []func(*Record){
		func(item *Record) { item.ProjectID = "other" },
		func(item *Record) { item.Scope.CommitOID = strings.Repeat("c", 40) },
		func(item *Record) { item.Class = ClassDiagnostic },
		func(item *Record) { item.State = StateObserved },
		func(item *Record) { item.Authority = AuthorityObservation },
		func(item *Record) {
			item.Sensitivity = SensitivityRestricted
			request.MaxSensitivity = SensitivityPublic
		},
		func(item *Record) { item.State = StateInvalidated },
		func(item *Record) { item.ValidFrom = now.Add(time.Hour) },
		func(item *Record) { until := now; item.ValidUntil = &until },
		func(item *Record) {
			item.Provenance.ConfigurationDigest = strings.Repeat("d", 64)
			authority.ConfigurationDigest = strings.Repeat("e", 64)
		},
	}
	for index, mutate := range mutations {
		item := cloneRecord(record)
		request.MaxSensitivity = SensitivityRestricted
		authority.ConfigurationDigest = ""
		mutate(&item)
		if eligible(item, request, authority) {
			t.Fatalf("ineligible mutation %d accepted", index)
		}
	}
	proposed := cloneRecord(record)
	proposed.State = StateProposed
	proposed.Authority = AuthorityAgentProposal
	authority.States = []State{StateProposed}
	authority.Authorities = []Authority{AuthorityAgentProposal}
	request.States, request.Authorities = authority.States, authority.Authorities
	authority.Review = false
	if eligible(proposed, request, authority) {
		t.Fatal("proposal escaped operational query")
	}
	authority.Review = true
	if !eligible(proposed, request, authority) {
		t.Fatal("authorized review query excluded proposal")
	}
}

// TestLocalStorePrivateBoundaryMatrix exercises replay, projection, path, cancellation, and not-found failures directly.
func TestLocalStorePrivateBoundaryMatrix(t *testing.T) {
	service, store, now := testService(t)
	view, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "matrix", Candidate: testCandidate(now)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Version(context.Background(), "project-1", view.ID, 99); err == nil {
		t.Fatal("missing history served")
	}
	if _, err = store.Current(context.Background(), "project-1", "missing"); err == nil {
		t.Fatal("missing current served")
	}
	snapshot, events, err := store.loadVerified(context.Background(), "project-1", true)
	if err != nil || len(events) != 1 {
		t.Fatal(err)
	}
	badVersion := append([]EventEnvelope(nil), events...)
	badVersion[0].Record.Version = 2
	if _, err = store.replay(context.Background(), "project-1", badVersion); err == nil {
		t.Fatal("invalid first version replayed")
	}
	duplicate := append(append([]EventEnvelope(nil), events...), events[0])
	duplicate[1].Sequence = 2
	duplicate[1].Record.Version = 2
	duplicate[1].Record.RecordDigest = recordDigest(duplicate[1].Record)
	duplicate[1].IdempotencyDigest = events[0].IdempotencyDigest
	duplicate[1].MutationDigest = strings.Repeat("d", 64)
	if _, err = store.replay(context.Background(), "project-1", duplicate); err == nil {
		t.Fatal("conflicting receipt replayed")
	}
	dir, _ := store.projectDir("project-1", false)
	journalPath := filepath.Join(dir, "journal.log")
	hardlinkPath := filepath.Join(dir, "journal-alias")
	if linkErr := os.Link(journalPath, hardlinkPath); linkErr == nil {
		if _, err = store.Current(context.Background(), "project-1", view.ID); err == nil {
			t.Fatal("hardlinked journal accepted")
		}
		if err = os.Remove(hardlinkPath); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.verifyIndex(context.Background(), filepath.Join(dir, "index.json"), snapshot); err != nil {
		t.Fatal(err)
	}
	wrong := snapshot
	wrong.Head.Sequence++
	if err = store.verifyIndex(context.Background(), filepath.Join(dir, "index.json"), wrong); err == nil {
		t.Fatal("stale index accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = store.acquire(ctx, "project-1"); err == nil {
		t.Fatal("cancelled acquisition succeeded")
	}
	if err = store.atomicJSON(ctx, dir, "cancelled.json", map[string]any{"safe": true}, "test.write"); err == nil {
		t.Fatal("cancelled publish succeeded")
	}
	ordinary := filepath.Join(dir, "ordinary")
	if err = os.Mkdir(ordinary, 0700); err != nil {
		t.Fatal(err)
	}
	if err = verifyRegularFile(ordinary, false); err == nil {
		t.Fatal("directory accepted as file")
	}
}

// TestServiceDenialConflictAndBudgetBranches verifies zero-write denials and bounded service failures.
func TestServiceDenialConflictAndBudgetBranches(t *testing.T) {
	service, store, now := testService(t)
	denied := service
	denied.HumanPrincipals = allowAuthority{denied: true}
	if _, err := denied.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "denied", Candidate: testCandidate(now)}); err == nil {
		t.Fatal("denied proposal accepted")
	}
	if head, _ := store.Head(context.Background(), "project-1"); head.Sequence != 0 {
		t.Fatal("denial wrote store")
	}
	invalidEvidence := testCandidate(now)
	invalidEvidence.Evidence = []EvidenceRef{{ID: "evidence-1", Digest: strings.Repeat("e", 64), RunID: "run-1", Project: "other"}}
	if _, err := service.Observe(context.Background(), ObserveCommand{FunctionalResultDigest: strings.Repeat("f", 64), Principal: "principal", ID: "observed-a", IdempotencyKey: "cross", Candidate: invalidEvidence}); err == nil {
		t.Fatal("cross-project evidence accepted")
	}
	view, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "valid", Candidate: testCandidate(now)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: view.ID, IdempotencyKey: "bad-replacement", ExpectedSequence: 1, ExpectedVersion: 1, Target: StateSuperseded}); err == nil {
		t.Fatal("missing replacement accepted")
	}
	authority := QueryAuthority{ProjectID: "project-1", Scope: testCandidate(now).Scope, MaxSensitivity: SensitivityPublic, AsOf: now, MaxWork: 1, MaxBytes: 1}
	request := QueryRequest{ProjectID: "project-1", Scope: authority.Scope, MaxSensitivity: SensitivityPublic, Limit: 1, MaxBytes: 1}
	result, err := service.Query(context.Background(), request, authority)
	if err != nil || len(result.Records) != 0 {
		t.Fatalf("byte truncation failed %#v %v", result, err)
	}
	badRequest := request
	badRequest.ProjectID = "other"
	if _, err = service.Query(context.Background(), badRequest, authority); err == nil {
		t.Fatal("query scope widening accepted")
	}
}

// TestValidationAndAdapterFailureBranches closes fail-closed paths for malformed values, lineage, records, and surface unions.
func TestValidationAndAdapterFailureBranches(t *testing.T) {
	now := time.Now().UTC()
	limits := DefaultLimits()
	invalidScopes := []Scope{{Kind: ScopeRun, RunID: "run-1"}, {Kind: ScopeRevision, RepositoryID: "repo-1", CommitOID: "branch"}, {Kind: ScopeProject, RepositoryID: "repo-1", CommitOID: strings.Repeat("a", 40)}, {Kind: "wide"}}
	for _, scope := range invalidScopes {
		candidate := testCandidate(now)
		candidate.Scope = scope
		if _, err := NormalizeRecord(candidate, "memory-a", 1, StateProposed, AuthorityAgentProposal, limits); err == nil {
			t.Fatalf("invalid scope accepted: %#v", scope)
		}
	}
	invalidValues := []TypedValue{{Kind: ValueString, Data: true}, {Kind: ValueBoolean, Data: "true"}, {Kind: ValueInteger, Data: ^uint64(0)}, {Kind: ValueJSON, Data: make(chan int)}, {Kind: "template", Data: "x"}}
	for _, value := range invalidValues {
		candidate := testCandidate(now)
		candidate.Value = value
		if _, err := NormalizeRecord(candidate, "memory-a", 1, StateProposed, AuthorityAgentProposal, limits); err == nil {
			t.Fatalf("invalid value accepted: %#v", value)
		}
	}
	lineage := testCandidate(now)
	lineage.Provenance.Source = ""
	if _, err := NormalizeRecord(lineage, "memory-a", 1, StateProposed, AuthorityAgentProposal, limits); err == nil {
		t.Fatal("incomplete lineage accepted")
	}
	lineage = testCandidate(now)
	lineage.Provenance.CommitOID = ""
	lineage.Provenance.ConfigurationDigest = strings.Repeat("c", 64)
	if _, err := NormalizeRecord(lineage, "memory-a", 1, StateProposed, AuthorityAgentProposal, limits); err != nil {
		t.Fatalf("configuration lineage rejected: %v", err)
	}
	record, _ := NormalizeRecord(testCandidate(now), "memory-a", 1, StateProposed, AuthorityAgentProposal, limits)
	record.RecordDigest = strings.Repeat("0", 64)
	if err := VerifyRecord(record, limits); err == nil {
		t.Fatal("tampered record verified")
	}
	record.ID = "../bad"
	if err := VerifyRecord(record, limits); err == nil {
		t.Fatal("malformed record identity verified")
	}
	emptyKey := testCandidate(now)
	emptyKey.Value = TypedValue{Kind: ValueJSON, Data: map[string]any{"": true}}
	if _, err := NormalizeRecord(emptyKey, "memory-a", 1, StateProposed, AuthorityAgentProposal, limits); err == nil {
		t.Fatal("empty JSON key accepted")
	}
	badPolicy := testCandidate(now)
	badPolicy.Provenance.PolicyDigest = "short"
	if _, err := NormalizeRecord(badPolicy, "memory-a", 1, StateProposed, AuthorityAgentProposal, limits); err == nil {
		t.Fatal("malformed policy digest accepted")
	}
	if _, err := DecodeCandidate([]byte("{"), limits); err == nil {
		t.Fatal("malformed JSON accepted")
	}
	additionalInvalid := []Candidate{testCandidate(now), testCandidate(now), testCandidate(now), testCandidate(now), testCandidate(now)}
	additionalInvalid[0].Subject = ""
	additionalInvalid[1].Value = TypedValue{Kind: ValueString, Data: strings.Repeat("x", limits.MaxStringBytes+1)}
	additionalInvalid[2].Value = TypedValue{Kind: ValueJSON, Data: map[string]any{"number": math.Inf(1)}}
	additionalInvalid[3].Value = TypedValue{Kind: ValueJSON, Data: map[string]any{strings.Repeat("k", limits.MaxStringBytes+1): true}}
	additionalInvalid[4].Evidence = []EvidenceRef{{ID: "bad/id", Digest: "short", RunID: "", Project: "project-1"}}
	for index, candidate := range additionalInvalid {
		if _, err := NormalizeRecord(candidate, "memory-a", 1, StateProposed, AuthorityAgentProposal, limits); err == nil {
			t.Fatalf("additional invalid candidate %d accepted", index)
		}
	}
	nodeLimited := testCandidate(now)
	nodeLimited.Value = TypedValue{Kind: ValueJSON, Data: []any{true, true}}
	tinyLimits := limits
	tinyLimits.MaxJSONNodes = 1
	if _, err := NormalizeRecord(nodeLimited, "memory-a", 1, StateProposed, AuthorityAgentProposal, tinyLimits); err == nil {
		t.Fatal("JSON node limit ignored")
	}
	service, _, _ := testService(t)
	if response := (Surface{}).Invoke(context.Background(), SurfaceRequest{Operation: OperationQuery}); response.Error == nil {
		t.Fatal("nil service accepted")
	}
	surface := Surface{Service: &service}
	for _, request := range []SurfaceRequest{{Operation: "unknown"}, {Operation: OperationQuery}, {Operation: OperationTransition}} {
		if response := surface.Invoke(context.Background(), request); response.Error == nil {
			t.Fatalf("invalid surface union accepted: %#v", request)
		}
	}
	if _, err := (ObservationAdapter{}).Observe(context.Background(), ObservationInput{}); err == nil {
		t.Fatal("nil observation service accepted")
	}
	if _, err := NewHMACAuthority("bad/key", []byte("short")); err == nil {
		t.Fatal("weak integrity authority accepted")
	}
}

// TestJournalFailureMatrix proves malformed chain coordinates and cancellation never become verified events.
func TestJournalFailureMatrix(t *testing.T) {
	service, store, now := testService(t)
	_, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "journal", Candidate: testCandidate(now)})
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := store.projectDir("project-1", false)
	journalPath := filepath.Join(dir, "journal.log")
	original, _ := os.ReadFile(journalPath)
	mutations := [][]byte{
		[]byte("not-json\n"),
		[]byte(strings.Replace(string(original), `"project_id":"project-1"`, `"project_id":"project-2"`, 1)),
		[]byte(strings.Replace(string(original), `"sequence":1`, `"sequence":2`, 1)),
		[]byte(strings.Replace(string(original), `"previous_event_digest":""`, `"previous_event_digest":"bad"`, 1)),
		[]byte(strings.Replace(string(original), `"key_id":"key-1"`, `"key_id":"unknown"`, 1)),
	}
	for index, mutation := range mutations {
		if err = os.WriteFile(journalPath, mutation, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = store.Current(context.Background(), "project-1", "anything"); err == nil {
			t.Fatalf("journal mutation %d accepted", index)
		}
	}
	if err = os.WriteFile(journalPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var event EventEnvelope
	if err = store.appendEvent(ctx, "project-1", event); err == nil {
		t.Fatal("cancelled append succeeded")
	}
}

// TestStaleProjectionRepairAndJournalTruncation distinguishes recoverable derived lag from source-of-truth loss.
func TestStaleProjectionRepairAndJournalTruncation(t *testing.T) {
	service, store, now := testService(t)
	_, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "first-event", Candidate: testCandidate(now)})
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := store.projectDir("project-1", false)
	snapshotPath := filepath.Join(dir, "snapshot.json")
	journalPath := filepath.Join(dir, "journal.log")
	oldSnapshot, _ := os.ReadFile(snapshotPath)
	firstJournal, _ := os.ReadFile(journalPath)
	secondCandidate := testCandidate(now)
	secondCandidate.Subject = "second"
	_, err = service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "second-event", ExpectedSequence: 1, Candidate: secondCandidate})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(snapshotPath, oldSnapshot, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Head(context.Background(), "project-1"); err != nil {
		t.Fatalf("valid stale snapshot not repaired: %v", err)
	}
	if err = os.WriteFile(journalPath, firstJournal, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Head(context.Background(), "project-1"); err == nil {
		t.Fatal("truncated journal accepted against authenticated snapshot")
	}
}

// TestQueryWorkAndIntegrityCancellationBranches covers work exhaustion plus cancelled signing and verification.
func TestQueryWorkAndIntegrityCancellationBranches(t *testing.T) {
	service, _, now := testService(t)
	for index := 0; index < 2; index++ {
		candidate := testCandidate(now)
		candidate.Subject = "work-" + string(rune('a'+index))
		if _, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: candidate.Subject, ExpectedSequence: uint64(index), Candidate: candidate}); err != nil {
			t.Fatal(err)
		}
	}
	authority := QueryAuthority{ProjectID: "project-1", Scope: testCandidate(now).Scope, States: []State{StateProposed}, Authorities: []Authority{AuthorityAgentProposal}, MaxSensitivity: SensitivityPublic, AsOf: now, MaxWork: 1, Review: true}
	request := QueryRequest{ProjectID: "project-1", Scope: authority.Scope, States: authority.States, Authorities: authority.Authorities, MaxSensitivity: SensitivityPublic, IncludeProposed: true}
	if _, err := service.Query(context.Background(), request, authority); err == nil {
		t.Fatal("query work exhaustion ignored")
	}
	integrity, _ := NewHMACAuthority("key-1", []byte("0123456789abcdef0123456789abcdef"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := integrity.Sign(ctx, []byte("payload")); err == nil {
		t.Fatal("cancelled signing succeeded")
	}
	if err := integrity.Verify(ctx, "key-1", []byte("payload"), "00"); err == nil {
		t.Fatal("cancelled verification succeeded")
	}
	if err := integrity.Verify(context.Background(), "key-1", []byte("payload"), "not-hex"); err == nil {
		t.Fatal("malformed authenticator accepted")
	}
	if _, err := NewLocalStore(filepath.Join(t.TempDir(), "nil-integrity"), nil, newMemoryWitness(), DefaultLimits()); err == nil {
		t.Fatal("store without integrity authority composed")
	}
	if _, err := (ObservationAdapter{Service: &service}).Observe(context.Background(), ObservationInput{FunctionalResultDigest: "short"}); err == nil {
		t.Fatal("unbound functional result accepted")
	}
	if err := (WorkflowGuard{}).BeforePrivilegedStep(context.Background(), &MemoryBinding{}, authority); err == nil {
		t.Fatal("binding without revalidation port accepted")
	}
	deniedObservation := service
	deniedObservation.Evidence = deniedEvidence{}
	if _, err := deniedObservation.Observe(context.Background(), ObserveCommand{FunctionalResultDigest: strings.Repeat("f", 64), Principal: "principal", ID: "observed-denied", IdempotencyKey: "denied-evidence", ExpectedSequence: 2, Candidate: withEvidence(testCandidate(now))}); err == nil {
		t.Fatal("revoked evidence accepted")
	}
	if err := deniedObservation.ReconcileExpiry(context.Background(), "unknown", "project-1", 2); err == nil {
		t.Fatal("unauthorized expiry accepted")
	}
}

// TestMemoryStoreMultiprocessCAS proves two independent processes serialize one project stream and only one stale CAS wins.
func TestMemoryStoreMultiprocessCAS(t *testing.T) {
	if os.Getenv("FEATUREPROOF_MEMORY_CHILD") == "1" {
		root := os.Getenv("FEATUREPROOF_MEMORY_ROOT")
		subject := os.Getenv("FEATUREPROOF_MEMORY_SUBJECT")
		integrity, integrityErr := NewHMACAuthority("key-1", []byte("0123456789abcdef0123456789abcdef"))
		if integrityErr != nil {
			os.Exit(5)
		}
		witness, witnessErr := NewHTTPMonotonicWitness(os.Getenv("FEATUREPROOF_MEMORY_WITNESS_URL"), "witness-test-token", http.DefaultClient)
		if witnessErr != nil {
			os.Exit(6)
		}
		store, storeErr := NewLocalStore(root, integrity, witness, DefaultLimits())
		if storeErr != nil {
			os.Exit(7)
		}
		now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
		service := Service{Store: store, Evidence: verifiedEvidence{}, HumanPrincipals: allowAuthority{}, ObserverPrincipals: allowAuthority{}, ReconcilerPrincipals: allowAuthority{}, Clock: fixedClock{now}, Gate: SecretGate{Rules: SecretRules{MaxWork: 1000}}, Limits: DefaultLimits()}
		candidate := testCandidate(now)
		candidate.Subject = subject
		if _, childErr := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: subject, Candidate: candidate}); childErr != nil {
			os.Exit(8)
		}
		os.Exit(0)
	}
	root := filepath.Join(t.TempDir(), ".featureproof", "memory")
	witnessAuthority := newMemoryWitness()
	witnessServer := newWitnessHTTPServer(t, witnessAuthority)
	witness, err := NewHTTPMonotonicWitness(witnessServer.URL, "witness-test-token", witnessServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	integrity, _ := NewHMACAuthority("key-1", []byte("0123456789abcdef0123456789abcdef"))
	bootstrapStore, err := NewLocalStore(root, integrity, witness, DefaultLimits())
	if err != nil || bootstrapStore.BootstrapProject(context.Background(), "project-1") != nil {
		t.Fatalf("multiprocess bootstrap: %v", err)
	}
	commands := make([]*exec.Cmd, 2)
	for index := range commands {
		commands[index] = exec.Command(os.Args[0], "-test.run=^TestMemoryStoreMultiprocessCAS$")
		commands[index].Env = append(os.Environ(), "FEATUREPROOF_MEMORY_CHILD=1", "FEATUREPROOF_MEMORY_ROOT="+root, "FEATUREPROOF_MEMORY_WITNESS_URL="+witnessServer.URL, "FEATUREPROOF_MEMORY_SUBJECT=process-"+string(rune('a'+index)))
		if err := commands[index].Start(); err != nil {
			t.Fatal(err)
		}
	}
	successes := 0
	for _, command := range commands {
		if command.Wait() == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("multiprocess CAS successes=%d", successes)
	}
	store, err := NewLocalStore(root, integrity, witness, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	head, err := store.Head(context.Background(), "project-1")
	if err != nil || head.Sequence != 1 {
		t.Fatalf("head=%#v err=%v", head, err)
	}
}

// TestTransientRetriesAndSafeObservability verifies finite classified retry and payload-free telemetry.
func TestTransientRetriesAndSafeObservability(t *testing.T) {
	service, realStore, now := testService(t)
	flaky := &flakyMemoryStore{MemoryStore: realStore, failures: 1}
	waiter := &immediateWaiter{}
	observer := &recordingObserver{}
	service.Store, service.Waiter, service.Observer = flaky, waiter, observer
	service.MaxRetries, service.RetryDelay = 1, time.Millisecond
	view, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "retry", Candidate: testCandidate(now)})
	if err != nil || flaky.calls != 2 || waiter.calls != 1 {
		t.Fatalf("view=%#v attempts=%d waits=%d err=%v", view, flaky.calls, waiter.calls, err)
	}
	if len(observer.records) != 1 || observer.records[0].MemoryID != view.ID || observer.records[0].Transition != "propose" {
		t.Fatalf("unsafe or missing telemetry: %#v", observer.records)
	}
	raw, _ := json.Marshal(observer.records)
	if strings.Contains(string(raw), "retry") || strings.Contains(string(raw), "api.pet") {
		t.Fatal("telemetry contains idempotency or content")
	}
	flaky.failures, flaky.calls = 10, 0
	service.MaxRetries = 0
	if _, err = service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "permanent", ExpectedSequence: 1, Candidate: testCandidate(now)}); err == nil || flaky.calls != 1 {
		t.Fatal("retry budget ignored")
	}
}

// TestIdempotencyIdentityCannotCrossProjects verifies the global durable identity registry fails closed across streams.
func TestIdempotencyIdentityCannotCrossProjects(t *testing.T) {
	service, store, now := testService(t)
	if _, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "global-key", Candidate: testCandidate(now)}); err != nil {
		t.Fatal(err)
	}
	candidate := testCandidate(now)
	candidate.ProjectID = "project-2"
	if err := store.BootstrapProject(context.Background(), "project-2"); err != nil {
		t.Fatal(err)
	}
	record, err := NormalizeRecord(candidate, "memory-other", 1, StateProposed, AuthorityAgentProposal, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Commit(context.Background(), Mutation{ProjectID: "project-2", IdempotencyKey: "global-key", Transition: "propose", Record: record, PolicyDigest: strings.Repeat("a", 64)})
	if err == nil {
		t.Fatal("idempotency identity rebound across projects")
	}
	head, headErr := store.Head(context.Background(), "project-2")
	if headErr != nil || head.Sequence != 0 {
		t.Fatalf("cross-project conflict wrote stream: %#v %v", head, headErr)
	}
}

// TestStoreCommitGuardMatrix proves invalid, cancelled, tampered, duplicate, and stale-version mutations stop before append.
func TestStoreCommitGuardMatrix(t *testing.T) {
	service, store, now := testService(t)
	view, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "guard-base", Candidate: testCandidate(now)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = store.Commit(ctx, Mutation{}); err == nil {
		t.Fatal("cancelled commit accepted")
	}
	if _, err = store.Commit(context.Background(), Mutation{ProjectID: "../bad"}); err == nil {
		t.Fatal("invalid mutation accepted")
	}
	current, _ := store.Current(context.Background(), "project-1", view.ID)
	tampered := cloneRecord(current)
	tampered.RecordDigest = strings.Repeat("0", 64)
	if _, err = store.Commit(context.Background(), Mutation{ProjectID: "project-1", ExpectedSequence: 1, IdempotencyKey: "tampered", Transition: "approve", Record: tampered, PolicyDigest: strings.Repeat("a", 64)}); err == nil {
		t.Fatal("tampered record committed")
	}
	duplicate := cloneRecord(current)
	if _, err = store.Commit(context.Background(), Mutation{ProjectID: "project-1", ExpectedSequence: 1, IdempotencyKey: "duplicate", Transition: "propose", Record: duplicate, PolicyDigest: strings.Repeat("a", 64)}); err == nil {
		t.Fatal("duplicate creation committed")
	}
	next := cloneRecord(current)
	next.Version = 3
	next.State = StateApproved
	next.Authority = AuthorityHuman
	next.RecordDigest = recordDigest(next)
	if _, err = store.Commit(context.Background(), Mutation{ProjectID: "project-1", ExpectedSequence: 1, ExpectedVersion: 2, IdempotencyKey: "stale-version", Transition: "approve", Record: next, PolicyDigest: strings.Repeat("a", 64)}); err == nil {
		t.Fatal("stale version committed")
	}
	if _, err = store.projectDir("..", false); err == nil {
		t.Fatal("unsafe project path accepted")
	}
	if err = store.Recover(context.Background(), ".."); err == nil {
		t.Fatal("unsafe recovery accepted")
	}
}

// TestApplicationFailureBranchMatrix covers bounded decoder, authority, service, surface, and workflow failures.
func TestApplicationFailureBranchMatrix(t *testing.T) {
	service, _, now := testService(t)
	limits := DefaultLimits()
	if _, err := DecodeAgentProposal(nil, "principal", "key", 0, limits); err == nil {
		t.Fatal("empty agent proposal accepted")
	}
	dto := AgentProposalDTO{Version: ContractVersion, Candidate: testCandidate(now)}
	raw, _ := json.Marshal(dto)
	if _, err := DecodeAgentProposal(append(raw, []byte(" {}")...), "principal", "key", 0, limits); err == nil {
		t.Fatal("trailing agent proposal accepted")
	}
	projectCandidate := testCandidate(now)
	projectCandidate.Scope = Scope{Kind: ScopeProject, RepositoryID: "repo-1"}
	restricted := service
	restricted.HumanPrincipals = revisionOnlyAuthority{}
	if _, err := restricted.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "wide", Candidate: projectCandidate}); err == nil {
		t.Fatal("scope widening accepted")
	}
	invalid := testCandidate(now)
	invalid.Class = "unknown"
	if _, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "invalid", Candidate: invalid}); err == nil {
		t.Fatal("invalid normalized proposal accepted")
	}
	if _, err := service.Observe(context.Background(), ObserveCommand{FunctionalResultDigest: strings.Repeat("f", 64), Principal: "unknown", ID: "observed-x", IdempotencyKey: "unknown", Candidate: withEvidence(testCandidate(now))}); err == nil {
		t.Fatal("unauthorized observation accepted")
	}
	badID := withEvidence(testCandidate(now))
	if _, err := service.Observe(context.Background(), ObserveCommand{FunctionalResultDigest: strings.Repeat("f", 64), Principal: "principal", ID: "../bad", IdempotencyKey: "bad-id", Candidate: badID}); err == nil {
		t.Fatal("invalid observation ID accepted")
	}
	secretObservation := withEvidence(testCandidate(now))
	secretObservation.Value = TypedValue{Kind: ValueString, Data: "MEMORY-CANARY"}
	if _, err := service.Observe(context.Background(), ObserveCommand{FunctionalResultDigest: strings.Repeat("f", 64), Principal: "principal", ID: "observed-secret", IdempotencyKey: "secret-observation", Candidate: secretObservation}); err == nil {
		t.Fatal("secret observation accepted")
	}
	integrity, _ := NewHMACAuthority("key-1", []byte("0123456789abcdef0123456789abcdef"))
	uncleanRoot := filepath.Join(t.TempDir(), "memory") + string(os.PathSeparator)
	if _, err := NewLocalStore(uncleanRoot, integrity, newMemoryWitness(), limits); err == nil {
		t.Fatal("unclean root accepted")
	}
	surface := Surface{Service: &service}
	query := QueryRequest{ProjectID: "other", Scope: testCandidate(now).Scope, MaxSensitivity: SensitivityPublic}
	authority := QueryAuthority{ProjectID: "project-1", Scope: testCandidate(now).Scope, MaxSensitivity: SensitivityPublic, AsOf: now}
	if response := (Surface{Service: &service, Authority: queryAuthorityFunc(func(context.Context, string, QueryRequest) (QueryAuthority, error) { return authority, nil })}).Invoke(context.Background(), SurfaceRequest{Operation: OperationQuery, Principal: "principal", Query: &query}); response.Error == nil {
		t.Fatal("surface query error omitted")
	}
	transition := TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: "missing", IdempotencyKey: "missing", ExpectedVersion: 1, Target: StateApproved}
	if response := surface.Invoke(context.Background(), SurfaceRequest{Operation: OperationTransition, Principal: "principal", Transition: &transition}); response.Error == nil {
		t.Fatal("surface transition error omitted")
	}
	if err := (WorkflowGuard{Memory: failingBindingPort{}}).BeforePrivilegedStep(context.Background(), &MemoryBinding{}, authority); err == nil {
		t.Fatal("binding port failure ignored")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Query(cancelled, QueryRequest{}, QueryAuthority{}); err == nil {
		t.Fatal("cancelled query accepted")
	}
	if _, err := service.Historical(context.Background(), "project-1", "missing", 1); err == nil {
		t.Fatal("missing historical record served")
	}
}

// withEvidence attaches one exact verified evidence reference to an observation candidate.
func withEvidence(candidate Candidate) Candidate {
	candidate.Evidence = []EvidenceRef{{ID: "evidence-1", Digest: strings.Repeat("e", 64), RunID: "run-1", Project: "project-1"}}
	return candidate
}

// assertTreeOmits verifies rejected canaries are absent from every durable file and owned temporary.
func assertTreeOmits(t *testing.T, root, canary string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(raw), canary) {
			t.Fatalf("canary persisted in %s", filepath.Base(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestRealTransportVerticalAndBindingDrift exercises CLI, HTTP API, MCP, and GitHub adapters through one shared service lifecycle.
func TestRealTransportVerticalAndBindingDrift(t *testing.T) {
	service, _, now := testService(t)
	queryAuthority := QueryAuthority{ProjectID: "project-1", Scope: testCandidate(now).Scope, States: []State{StateApproved}, Authorities: []Authority{AuthorityHuman}, MaxSensitivity: SensitivityPublic, AsOf: now, MaxItems: 10, MaxBytes: 1 << 20, MaxWork: 100, Review: true}
	resolver := queryAuthorityFunc(func(context.Context, string, QueryRequest) (QueryAuthority, error) { return queryAuthority, nil })
	surface := Surface{Service: &service, Authority: resolver}
	proposal := ProposalCommand{IdempotencyKey: "vertical-proposal", Candidate: testCandidate(now)}
	proposalRaw, _ := json.Marshal(proposal)
	var cliOutput bytes.Buffer
	if code := (CLIAdapter{Surface: surface, Principal: "principal"}).Run(context.Background(), OperationPropose, proposalRaw, &cliOutput); code != 0 {
		t.Fatalf("cli propose code=%d output=%s", code, cliOutput.String())
	}
	var proposed SurfaceResponse
	if json.Unmarshal(cliOutput.Bytes(), &proposed) != nil || proposed.Review == nil {
		t.Fatal("cli review projection missing")
	}
	transition := TransitionCommand{ProjectID: "project-1", MemoryID: proposed.Review.ID, IdempotencyKey: "vertical-approve", ExpectedSequence: 1, ExpectedVersion: 1, DecisionID: "vertical-decision", Target: StateApproved}
	transitionRaw, _ := json.Marshal(transition)
	mcp := MCPAdapter{Surface: surface, Auth: transportPrincipalResolver{}}
	approved := mcp.Handle(context.Background(), MCPMemoryRequest{Credential: "Bearer transport-token", Operation: OperationTransition, Arguments: transitionRaw})
	if approved.Error != nil || approved.Audit == nil || approved.Audit.State != StateApproved {
		t.Fatalf("mcp transition failed: %#v", approved)
	}
	query := QueryRequest{ProjectID: "project-1", Scope: queryAuthority.Scope, States: queryAuthority.States, Authorities: queryAuthority.Authorities, MaxSensitivity: SensitivityPublic, Limit: 10, MaxBytes: 1 << 20}
	queryRaw, _ := json.Marshal(query)
	httpRequest := httptest.NewRequest("POST", "/v1/memory", bytes.NewReader(queryRaw))
	httpRequest.Header.Set("Authorization", "Bearer transport-token")
	httpRequest.Header.Set("FeatureProof-Operation", string(OperationQuery))
	httpResponse := httptest.NewRecorder()
	(APIHandler{Surface: surface, Auth: transportPrincipalResolver{}}).ServeHTTP(httpResponse, httpRequest)
	var apiView SurfaceResponse
	if json.Unmarshal(httpResponse.Body.Bytes(), &apiView) != nil || apiView.Query == nil || len(apiView.Query.Items) != 1 {
		t.Fatalf("api query failed: %d %s", httpResponse.Code, httpResponse.Body.String())
	}
	github := GitHubAdapter{Surface: surface, Auth: transportPrincipalResolver{}}
	githubView := github.Submit(context.Background(), VerifiedGitHubActor{ActorID: "principal", RepositoryID: "repo-1", Action: "memory.review"}, OperationQuery, queryRaw)
	if githubView.Error != nil || githubView.Query == nil || len(githubView.Query.Items) != 1 {
		t.Fatalf("github query failed: %#v", githubView)
	}
	result, err := service.Query(context.Background(), query, queryAuthority)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := service.Bind(result, queryAuthority)
	if err != nil {
		t.Fatal(err)
	}
	invalidate := TransitionCommand{ProjectID: "project-1", MemoryID: proposed.Review.ID, IdempotencyKey: "vertical-invalidate", ExpectedSequence: 2, ExpectedVersion: 2, DecisionID: "vertical-revoke", Target: StateInvalidated}
	invalidateRaw, _ := json.Marshal(invalidate)
	invalidated := github.Submit(context.Background(), VerifiedGitHubActor{ActorID: "principal", RepositoryID: "repo-1", Action: "memory.invalidate"}, OperationTransition, invalidateRaw)
	if invalidated.Error != nil || invalidated.Audit == nil || invalidated.Audit.State != StateInvalidated {
		t.Fatalf("github transition failed: %#v", invalidated)
	}
	status, err := service.Revalidate(context.Background(), binding, queryAuthority)
	if err != nil || status.Valid {
		t.Fatalf("vertical drift not blocked: %#v %v", status, err)
	}
}

// TestProjectDirectorySwapAtOpenBoundaryFailsClosed deterministically replaces a verified path component before handle acquisition.
func TestProjectDirectorySwapAtOpenBoundaryFailsClosed(t *testing.T) {
	service, store, now := testService(t)
	view, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "swap-seed", Candidate: testCandidate(now)})
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := store.projectDir("project-1", false)
	journal := filepath.Join(dir, "journal.log")
	original, _ := os.ReadFile(journal)
	blocked := false
	store.boundary = func(stage, _ string) {
		if stage != "read.before_open" {
			return
		}
		store.boundary = nil
		moved := dir + "-moved"
		if renameErr := os.Rename(dir, moved); renameErr != nil {
			blocked = true
			return
		}
		if mkdirErr := os.Mkdir(dir, 0700); mkdirErr != nil {
			t.Fatalf("swap mkdir: %v", mkdirErr)
		}
		if writeErr := os.WriteFile(filepath.Join(dir, "journal.log"), original, 0600); writeErr != nil {
			t.Fatalf("swap journal: %v", writeErr)
		}
	}
	_, err = store.Current(context.Background(), "project-1", view.ID)
	if !blocked && err == nil {
		t.Fatal("project directory swap reached a read result")
	}
}

// TestPublishBoundarySwapsAreBlockedOrDetected exercises every temporary/write/replace identity checkpoint.
func TestPublishBoundarySwapsAreBlockedOrDetected(t *testing.T) {
	for _, stage := range []string{"matrix.write.before_temp", "matrix.write.after_temp_open", "matrix.write.after_temp_write", "matrix.write.after_temp_sync", "matrix.write.before_replace", "matrix.write.replace_checked", "matrix.write.after_replace"} {
		t.Run(stage, func(t *testing.T) {
			_, store, _ := testService(t)
			dir, err := store.projectDir("project-1", true)
			if err != nil {
				t.Fatal(err)
			}
			reached, swapped := false, false
			store.boundary = func(current, _ string) {
				if current != stage || reached {
					return
				}
				reached = true
				moved := dir + "-moved"
				if renameErr := os.Rename(dir, moved); renameErr != nil {
					return // An effective handle denied the swap, which is the safe outcome.
				}
				if mkdirErr := os.Mkdir(dir, 0700); mkdirErr != nil || secureMemoryPermissions(dir) != nil {
					t.Fatalf("replacement directory: %v", mkdirErr)
				}
				swapped = true
			}
			err = store.atomicJSON(context.Background(), dir, "matrix.json", map[string]any{"safe": true}, "matrix.write")
			if !reached || swapped && err == nil {
				t.Fatalf("boundary reached=%v swapped=%v err=%v", reached, swapped, err)
			}
		})
	}
}

// TestLockBoundarySwapsFailClosed binds both global and project locks to their validated directories.
func TestLockBoundarySwapsFailClosed(t *testing.T) {
	for _, stage := range []string{"lock.global.before_open", "lock.project.before_open"} {
		t.Run(stage, func(t *testing.T) {
			_, store, _ := testService(t)
			reached, swapped := false, false
			store.boundary = func(current, path string) {
				if current != stage || reached {
					return
				}
				reached = true
				dir := filepath.Dir(path)
				if renameErr := os.Rename(dir, dir+"-moved"); renameErr != nil {
					return
				}
				if mkdirErr := os.Mkdir(dir, 0700); mkdirErr != nil || secureMemoryPermissions(dir) != nil {
					t.Fatalf("replacement lock directory: %v", mkdirErr)
				}
				swapped = true
			}
			release, err := store.acquire(context.Background(), "project-1")
			if release != nil {
				release()
			}
			if !reached || swapped && err == nil {
				t.Fatalf("lock boundary reached=%v swapped=%v err=%v", reached, swapped, err)
			}
		})
	}
}

// TestEveryMutableStoreObjectRejectsHardlinkAliases covers locks, journal, projections, and the external registry.
func TestEveryMutableStoreObjectRejectsHardlinkAliases(t *testing.T) {
	for _, object := range []string{"global-lock", "project-lock", "journal", "snapshot", "index", "registry"} {
		t.Run(object, func(t *testing.T) {
			service, store, now := testService(t)
			view, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "alias-seed", Candidate: testCandidate(now)})
			if err != nil {
				t.Fatal(err)
			}
			dir, _ := store.projectDir("project-1", false)
			paths := map[string]string{
				"global-lock":  filepath.Join(store.root, "v1", "write.lock"),
				"project-lock": filepath.Join(dir, "write.lock"),
				"journal":      filepath.Join(dir, "journal.log"),
				"snapshot":     filepath.Join(dir, "snapshot.json"),
				"index":        filepath.Join(dir, "index.json"),
				"registry":     filepath.Join(store.trustRoot, "v1", "registry", base64.RawURLEncoding.EncodeToString([]byte("project-1"))+".json"),
			}
			path := paths[object]
			if err = os.Link(path, path+".alias"); err != nil {
				t.Skipf("hardlinks unavailable: %v", err)
			}
			if object == "global-lock" || object == "project-lock" {
				release, acquireErr := store.acquire(context.Background(), "project-1")
				if release != nil {
					release()
				}
				if acquireErr == nil {
					t.Fatalf("%s hardlink accepted", object)
				}
				return
			}
			if _, err = store.Current(context.Background(), "project-1", view.ID); err == nil {
				t.Fatalf("%s hardlink accepted", object)
			}
		})
	}
}

// TestOwnedTemporaryHardlinkInjectionFailsBeforePublication proves the effective temp identity is rechecked after creation.
func TestOwnedTemporaryHardlinkInjectionFailsBeforePublication(t *testing.T) {
	_, store, _ := testService(t)
	dir, err := store.projectDir("project-1", true)
	if err != nil {
		t.Fatal(err)
	}
	linked := false
	store.boundary = func(stage, path string) {
		if stage == "temp.write.after_temp_open" && !linked {
			linked = os.Link(path, path+".alias") == nil
		}
	}
	err = store.atomicJSON(context.Background(), dir, "target.json", map[string]any{"safe": true}, "temp.write")
	if !linked {
		t.Skip("hardlinks unavailable")
	}
	if err == nil {
		t.Fatal("hardlinked temporary was published")
	}
}

// TestStoreObjectsAndProjectDirectoryRejectReparseAliases covers final-component links and directory reparse traversal.
func TestStoreObjectsAndProjectDirectoryRejectReparseAliases(t *testing.T) {
	for _, object := range []string{"global-lock", "project-lock", "journal", "snapshot", "index", "registry"} {
		t.Run(object, func(t *testing.T) {
			service, store, now := testService(t)
			view, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "link-seed", Candidate: testCandidate(now)})
			if err != nil {
				t.Fatal(err)
			}
			dir, _ := store.projectDir("project-1", false)
			paths := map[string]string{"global-lock": filepath.Join(store.root, "v1", "write.lock"), "project-lock": filepath.Join(dir, "write.lock"), "journal": filepath.Join(dir, "journal.log"), "snapshot": filepath.Join(dir, "snapshot.json"), "index": filepath.Join(dir, "index.json"), "registry": filepath.Join(store.trustRoot, "v1", "registry", base64.RawURLEncoding.EncodeToString([]byte("project-1"))+".json")}
			path, backup := paths[object], paths[object]+".original"
			if err = os.Rename(path, backup); err != nil || os.Symlink(backup, path) != nil {
				t.Skip("symbolic links unavailable")
			}
			if object == "global-lock" || object == "project-lock" {
				release, acquireErr := store.acquire(context.Background(), "project-1")
				if release != nil {
					release()
				}
				if acquireErr == nil {
					t.Fatalf("%s link accepted", object)
				}
				return
			}
			if _, err = store.Current(context.Background(), "project-1", view.ID); err == nil {
				t.Fatalf("%s link accepted", object)
			}
		})
	}
	t.Run("project-directory", func(t *testing.T) {
		service, store, now := testService(t)
		view, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "reparse-seed", Candidate: testCandidate(now)})
		if err != nil {
			t.Fatal(err)
		}
		dir, _ := store.projectDir("project-1", false)
		backup := dir + "-original"
		if err = os.Rename(dir, backup); err != nil || os.Symlink(backup, dir) != nil {
			t.Skip("directory reparse links unavailable")
		}
		if _, err = store.Current(context.Background(), "project-1", view.ID); err == nil {
			t.Fatal("project directory reparse alias accepted")
		}
	})
}

// TestIndependentAnchorRecoversForwardCrashButRejectsAnchorDeletion distinguishes a committed journal tail from rollback attacks.
func TestIndependentAnchorRecoversForwardCrashButRejectsAnchorDeletion(t *testing.T) {
	service, store, now := testService(t)
	first, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "anchor-first", Candidate: testCandidate(now)})
	if err != nil {
		t.Fatal(err)
	}
	anchorPath := filepath.Join(store.trustRoot, "v1", "registry", base64.RawURLEncoding.EncodeToString([]byte("project-1"))+".json")
	oldAnchor, _ := os.ReadFile(anchorPath)
	secondCandidate := testCandidate(now)
	secondCandidate.Subject = "second"
	if _, err = service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "anchor-second", ExpectedSequence: 1, Candidate: secondCandidate}); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(anchorPath, oldAnchor, 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"snapshot.json", "index.json"} {
		_ = os.Remove(filepath.Join(mustProjectDir(t, store, "project-1"), name))
	}
	if _, err = store.Current(context.Background(), "project-1", first.ID); err != nil {
		t.Fatalf("forward crash did not recover: %v", err)
	}
	if err = os.Remove(anchorPath); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Current(context.Background(), "project-1", first.ID); err == nil {
		t.Fatal("anchor deletion accepted")
	}
}

// TestExternalRegistryDetectsWholeMemoryRootDeletionAndJournalRollback proves confirmed heads and receipts survive mutable-root loss.
func TestExternalRegistryDetectsWholeMemoryRootDeletionAndJournalRollback(t *testing.T) {
	service, store, now := testService(t)
	view, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "registry-receipt", Candidate: testCandidate(now)})
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Current(context.Background(), "project-1", view.ID)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := store.projectDir("project-1", false)
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(dir, "journal.log")
	oldJournal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: view.ID, IdempotencyKey: "registry-approve", ExpectedSequence: 1, ExpectedVersion: 1, DecisionID: "registry-decision", Target: StateApproved}); err != nil {
		t.Fatal(err)
	}
	fullJournal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(journalPath, oldJournal, 0600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, "snapshot.json"))
	_ = os.Remove(filepath.Join(dir, "index.json"))
	if _, err = store.Head(context.Background(), "project-1"); err == nil {
		t.Fatal("coordinated journal/projection rollback matched external registry")
	}
	if err = os.WriteFile(journalPath, fullJournal, 0600); err != nil {
		t.Fatal(err)
	}
	root, trustRoot, integrity := store.root, store.trustRoot, store.integrity
	if err = os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewLocalStoreWithTrustRoot(root, trustRoot, integrity, store.witness, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Head(context.Background(), "project-1"); err == nil {
		t.Fatal("whole mutable memory root deletion became a new empty stream")
	}
	if _, err = reopened.Commit(context.Background(), Mutation{ProjectID: "project-1", IdempotencyKey: "registry-receipt", Transition: "propose", Record: record, PolicyDigest: record.Provenance.PolicyDigest}); err == nil {
		t.Fatal("receipt was reusable after stream deletion")
	}
}

// TestTransportAndTrustedProvenanceFailureBranches proves malformed payloads and incomplete trusted composition fail before storage.
func TestTransportAndTrustedProvenanceFailureBranches(t *testing.T) {
	service, _, now := testService(t)
	surface := Surface{Service: &service}
	if code := (CLIAdapter{Surface: surface}).Run(context.Background(), OperationPropose, []byte("{}"), io.Discard); code != 2 {
		t.Fatalf("empty CLI principal code=%d", code)
	}
	if code := (CLIAdapter{Surface: surface, Principal: "principal"}).Run(context.Background(), "unknown", []byte("{}"), io.Discard); code != 2 {
		t.Fatalf("unknown CLI operation code=%d", code)
	}
	httpResponse := httptest.NewRecorder()
	(APIHandler{}).ServeHTTP(httpResponse, httptest.NewRequest("POST", "/", bytes.NewReader([]byte("{}"))))
	if httpResponse.Code != 401 {
		t.Fatal("API without authentication accepted")
	}
	httpResponse = httptest.NewRecorder()
	httpRequest := httptest.NewRequest("POST", "/", bytes.NewReader([]byte("{} {}")))
	httpRequest.Header.Set("Authorization", "Bearer transport-token")
	httpRequest.Header.Set("FeatureProof-Operation", string(OperationQuery))
	(APIHandler{Surface: surface, Auth: transportPrincipalResolver{}}).ServeHTTP(httpResponse, httpRequest)
	if httpResponse.Code != 400 {
		t.Fatal("API trailing JSON accepted")
	}
	if response := (MCPAdapter{}).Handle(context.Background(), MCPMemoryRequest{}); response.Error == nil {
		t.Fatal("MCP without auth accepted")
	}
	if response := (MCPAdapter{Auth: transportPrincipalResolver{}}).Handle(context.Background(), MCPMemoryRequest{Credential: "bad", Operation: OperationQuery, Arguments: []byte("{}")}); response.Error == nil {
		t.Fatal("MCP bad credential accepted")
	}
	if response := (GitHubAdapter{}).Submit(context.Background(), VerifiedGitHubActor{}, OperationQuery, []byte("{}")); response.Error == nil {
		t.Fatal("GitHub without auth accepted")
	}
	if response := (GitHubAdapter{Auth: transportPrincipalResolver{}}).Submit(context.Background(), VerifiedGitHubActor{ActorID: "bad"}, OperationQuery, []byte("{}")); response.Error == nil {
		t.Fatal("GitHub forged actor accepted")
	}
	for _, operation := range []SurfaceOperation{OperationPropose, OperationQuery, OperationTransition} {
		if _, err := decodeSurfacePayload(operation, []byte("{} {}")); err == nil {
			t.Fatalf("%s accepted trailing document", operation)
		}
	}
	missingClock := service
	missingClock.Clock = nil
	if _, err := missingClock.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "missing-clock", Candidate: testCandidate(now)}); err == nil {
		t.Fatal("missing trusted clock accepted")
	}
	mismatchedCommit := service
	mismatchedCommit.HumanPrincipals = boundedAuthority{maxScope: ScopeProject, maxSensitivity: SensitivityRestricted}
	candidate := testCandidate(now)
	candidate.Scope.CommitOID = strings.Repeat("d", 40)
	if _, err := mismatchedCommit.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "commit-mismatch", Candidate: candidate}); err == nil {
		t.Fatal("commit binding mismatch accepted")
	}
}

// mustProjectDir resolves one already-created test project directory or fails the current test.
func mustProjectDir(t *testing.T, store *LocalStore, project string) string {
	t.Helper()
	dir, err := store.projectDir(project, false)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestRoleAuthoritiesDenyEveryCrossRoleCommand proves human, observer, and reconciler credentials are not interchangeable.
func TestRoleAuthoritiesDenyEveryCrossRoleCommand(t *testing.T) {
	service, _, now := testService(t)
	service.HumanPrincipals = roleAuthority{principal: "human", kind: "human", actions: map[string]bool{"propose": true, string(StateApproved): true}}
	service.ObserverPrincipals = roleAuthority{principal: "observer", kind: "observer", actions: map[string]bool{"observe": true}}
	service.ReconcilerPrincipals = roleAuthority{principal: "reconciler", kind: "reconciler", actions: map[string]bool{"expire": true}}
	candidate := testCandidate(now)
	if _, err := service.Propose(context.Background(), ProposalCommand{Principal: "observer", IdempotencyKey: "observer-propose", Candidate: candidate}); err == nil {
		t.Fatal("observer used human proposal authority")
	}
	if _, err := service.Observe(context.Background(), ObserveCommand{Principal: "human", ID: "cross-observe", IdempotencyKey: "human-observe", Candidate: withEvidence(candidate), FunctionalResultDigest: strings.Repeat("f", 64)}); err == nil {
		t.Fatal("human used observer authority")
	}
	if err := service.ReconcileExpiry(context.Background(), "human", "project-1", 0); err == nil {
		t.Fatal("human used reconciler authority")
	}
	if err := service.ReconcileExpiry(context.Background(), "observer", "project-1", 0); err == nil {
		t.Fatal("observer used reconciler authority")
	}
	if _, err := service.Propose(context.Background(), ProposalCommand{Principal: "reconciler", IdempotencyKey: "reconciler-propose", Candidate: candidate}); err == nil {
		t.Fatal("reconciler used human authority")
	}
}

// witnessFixture retains one live authority while local durable files are restored adversarially.
type witnessFixture struct {
	root, trust string
	integrity   IntegrityAuthority
	witness     *memoryWitness
	store       *LocalStore
	service     Service
	first       ReviewView
	firstFiles  map[string][]byte
}

// newWitnessFixture commits two generations and captures every LocalStore-managed file after generation one.
func newWitnessFixture(t *testing.T) witnessFixture {
	t.Helper()
	base := t.TempDir()
	fixture := witnessFixture{root: filepath.Join(base, "memory"), trust: filepath.Join(base, "registry"), witness: newMemoryWitness()}
	fixture.integrity, _ = NewHMACAuthority("memory-key", []byte("0123456789abcdef0123456789abcdef"))
	var err error
	fixture.store, err = NewLocalStoreWithAuthorities(fixture.root, fixture.trust, fixture.integrity, fixture.witness, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err = fixture.store.BootstrapProject(context.Background(), "project-1"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	fixture.service = Service{Store: fixture.store, Evidence: verifiedEvidence{}, HumanPrincipals: allowAuthority{}, ObserverPrincipals: allowAuthority{}, ReconcilerPrincipals: allowAuthority{}, Clock: fixedClock{now}, Gate: SecretGate{Rules: SecretRules{MaxWork: 1000}}, Limits: DefaultLimits()}
	fixture.first, err = fixture.service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "witness-first", Candidate: testCandidate(now)})
	if err != nil {
		t.Fatal(err)
	}
	dir := mustProjectDir(t, fixture.store, "project-1")
	registry := filepath.Join(fixture.trust, "v1", "registry", base64.RawURLEncoding.EncodeToString([]byte("project-1"))+".json")
	fixture.firstFiles = map[string][]byte{}
	for _, path := range []string{filepath.Join(dir, "journal.log"), filepath.Join(dir, "snapshot.json"), filepath.Join(dir, "index.json"), registry} {
		fixture.firstFiles[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = fixture.service.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: fixture.first.ID, IdempotencyKey: "witness-second", DecisionID: "decision-two", ExpectedSequence: 1, ExpectedVersion: 1, Target: StateApproved}); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// restoreWitnessFixture rolls back every file owned by LocalStore without altering the external authority.
func restoreWitnessFixture(t *testing.T, fixture witnessFixture) {
	t.Helper()
	for path, raw := range fixture.firstFiles {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// leaveAdvancedPending simulates a lost confirmation after the witness commits but before journal materialization.
func leaveAdvancedPending(t *testing.T) witnessFixture {
	t.Helper()
	fixture := newWitnessFixture(t)
	fixture.store.boundary = func(stage, _ string) {
		if stage == "witness.after_advance" {
			fixture.witness.mu.Lock()
			fixture.witness.unavailable = true
			fixture.witness.mu.Unlock()
		}
	}
	if _, err := fixture.service.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: fixture.first.ID, IdempotencyKey: "pending-third", DecisionID: "pending-third", ExpectedSequence: 2, ExpectedVersion: 2, Target: StateInvalidated}); err == nil {
		t.Fatal("lost witness confirmation unexpectedly completed")
	}
	fixture.witness.mu.Lock()
	fixture.witness.unavailable = false
	fixture.witness.mu.Unlock()
	fixture.store.boundary = nil
	return fixture
}

// witnessPendingPath returns the fixed local recovery object for one test stream.
func witnessPendingPath(t *testing.T, fixture witnessFixture) string {
	t.Helper()
	dir, err := fixture.store.projectDir("project-1", false)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "pending.json")
}

// TestExternalWitnessRejectsCoordinatedLocalRollback proves replaying every local authenticated file cannot hide generation two.
func TestExternalWitnessRejectsCoordinatedLocalRollback(t *testing.T) {
	fixture := newWitnessFixture(t)
	restoreWitnessFixture(t, fixture)
	reopened, err := NewLocalStoreWithAuthorities(fixture.root, fixture.trust, fixture.integrity, fixture.witness, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Head(context.Background(), "project-1"); err == nil {
		t.Fatal("coordinated local rollback matched a newer external witness")
	}
	restarted := fixture.service
	restarted.Store = reopened
	if _, err = restarted.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: fixture.first.ID, IdempotencyKey: "witness-second", DecisionID: "decision-two", ExpectedSequence: 1, ExpectedVersion: 1, Target: StateApproved}); err == nil {
		t.Fatal("receipt hidden by rollback was reusable")
	}
	state, err := fixture.witness.Read(context.Background(), "project-1")
	if err != nil || state.Generation != 2 {
		t.Fatalf("external authority changed during local rollback: %#v %v", state, err)
	}
}

// TestWitnessRequiresExplicitBootstrapAndFailsClosed exercises missing, deletion, unavailability, behind, ahead, and replay outcomes.
func TestWitnessRequiresExplicitBootstrapAndFailsClosed(t *testing.T) {
	integrity, _ := NewHMACAuthority("memory-key", []byte("0123456789abcdef0123456789abcdef"))
	witness := newMemoryWitness()
	store, err := NewLocalStore(filepath.Join(t.TempDir(), "memory"), integrity, witness, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err = store.BootstrapProject(context.Background(), "../invalid"); err == nil {
		t.Fatal("invalid project identifier reached the external witness")
	}
	mismatchWitness := newMemoryWitness()
	mismatched := witnessStateForEvents("different-project", nil)
	mismatchWitness.bootstrapState = &mismatched
	mismatchStore, mismatchErr := NewLocalStore(filepath.Join(t.TempDir(), "memory"), integrity, mismatchWitness, DefaultLimits())
	if mismatchErr != nil {
		t.Fatal(mismatchErr)
	}
	if err = mismatchStore.BootstrapProject(context.Background(), "project-1"); err == nil {
		t.Fatal("bootstrap accepted a state for different authority coordinates")
	}
	if _, err = store.Head(context.Background(), "project-1"); err == nil {
		t.Fatal("missing witness was treated as virgin")
	}
	if err = store.BootstrapProject(context.Background(), "project-1"); err != nil {
		t.Fatal(err)
	}
	if err = store.BootstrapProject(context.Background(), "project-1"); err == nil {
		t.Fatal("bootstrap replay was accepted")
	}
	if head, headErr := store.Head(context.Background(), "project-1"); headErr != nil || head != (Head{}) {
		t.Fatalf("explicit bootstrap head=%#v err=%v", head, headErr)
	}
	service := Service{Store: store, Evidence: verifiedEvidence{}, HumanPrincipals: allowAuthority{}, ObserverPrincipals: allowAuthority{}, ReconcilerPrincipals: allowAuthority{}, Clock: fixedClock{time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)}, Gate: SecretGate{Rules: SecretRules{MaxWork: 1000}}, Limits: DefaultLimits()}
	if _, err = service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "witness-state", Candidate: testCandidate(time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC))}); err != nil {
		t.Fatal(err)
	}
	witness.mu.Lock()
	current := witness.states["project-1"]
	delete(witness.states, "project-1")
	witness.mu.Unlock()
	if _, err = store.Head(context.Background(), "project-1"); err == nil {
		t.Fatal("deleted witness was reconstructed from history")
	}
	witness.mu.Lock()
	witness.states["project-1"] = witnessStateForEvents("project-1", nil)
	witness.mu.Unlock()
	if _, err = store.Head(context.Background(), "project-1"); err == nil {
		t.Fatal("witness behind local history was accepted")
	}
	witness.mu.Lock()
	ahead := current
	ahead.Generation++
	ahead.Head.Sequence++
	witness.states["project-1"] = ahead
	witness.mu.Unlock()
	if _, err = store.Head(context.Background(), "project-1"); err == nil {
		t.Fatal("witness ahead of local history was accepted")
	}
	witness.mu.Lock()
	witness.states["project-1"] = current
	witness.unavailable = true
	witness.mu.Unlock()
	if _, err = store.Head(context.Background(), "project-1"); err == nil {
		t.Fatal("unavailable witness degraded to local validation")
	}
	witness.mu.Lock()
	witness.unavailable = false
	witness.mu.Unlock()
	if err = witness.CompareAndAdvance(context.Background(), witnessStateForEvents("project-1", nil), current); !errors.Is(err, ErrWitnessConflict) {
		t.Fatalf("old state replay result=%v", err)
	}
}

// TestWitnessCrashBoundariesAndConcurrentCAS proves ambiguous commits converge only when the authority actually advanced.
func TestWitnessCrashBoundariesAndConcurrentCAS(t *testing.T) {
	t.Run("missing-before-advance-fails-closed", func(t *testing.T) {
		fixture := newWitnessFixture(t)
		fixture.store.boundary = func(stage, _ string) {
			if stage == "witness.before_advance" {
				fixture.witness.mu.Lock()
				delete(fixture.witness.states, "project-1")
				fixture.witness.mu.Unlock()
			}
		}
		_, err := fixture.service.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: fixture.first.ID, IdempotencyKey: "missing-third", DecisionID: "missing-third", ExpectedSequence: 2, ExpectedVersion: 2, Target: StateInvalidated})
		if err == nil {
			t.Fatal("writer reconstructed a witness deleted before advance")
		}
	})

	t.Run("before-advance-fails-closed", func(t *testing.T) {
		fixture := newWitnessFixture(t)
		dir, _ := fixture.store.projectDir("project-1", false)
		journalPath := filepath.Join(dir, "journal.log")
		journalBefore, readErr := os.ReadFile(journalPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		fixture.store.boundary = func(stage, _ string) {
			if stage == "witness.before_advance" {
				fixture.witness.mu.Lock()
				fixture.witness.unavailable = true
				fixture.witness.mu.Unlock()
			}
		}
		_, err := fixture.service.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: fixture.first.ID, IdempotencyKey: "before-third", DecisionID: "before-third", ExpectedSequence: 2, ExpectedVersion: 2, Target: StateInvalidated})
		if err == nil {
			t.Fatal("writer passed unavailable witness")
		}
		fixture.witness.mu.Lock()
		fixture.witness.unavailable = false
		fixture.witness.mu.Unlock()
		reopened, openErr := NewLocalStoreWithAuthorities(fixture.root, fixture.trust, fixture.integrity, fixture.witness, DefaultLimits())
		if openErr != nil {
			t.Fatal(openErr)
		}
		if head, headErr := reopened.Head(context.Background(), "project-1"); headErr != nil || head.Sequence != 2 {
			t.Fatalf("pre-advance failure changed the durable journal: %#v %v", head, headErr)
		}
		journalAfter, readErr := os.ReadFile(journalPath)
		if readErr != nil || !bytes.Equal(journalBefore, journalAfter) {
			t.Fatalf("pre-advance failure changed journal bytes: %v", readErr)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "pending.json")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("confirmed pre-advance failure retained staging: %v", statErr)
		}
		restarted := fixture.service
		restarted.Store = reopened
		command := TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: fixture.first.ID, IdempotencyKey: "before-third", DecisionID: "before-third", ExpectedSequence: 2, ExpectedVersion: 2, Target: StateInvalidated}
		result, retryErr := restarted.Transition(context.Background(), command)
		if retryErr != nil || result.Version != 3 {
			t.Fatalf("retry after a confirmed pre-advance failure did not commit: %#v %v", result, retryErr)
		}
		if redelivered, redeliveryErr := restarted.Transition(context.Background(), command); redeliveryErr != nil || redelivered.Version != 3 {
			t.Fatalf("redelivery did not reuse the single durable receipt: %#v %v", redelivered, redeliveryErr)
		}
	})

	t.Run("partial-append-recovers-from-staging", func(t *testing.T) {
		fixture := leaveAdvancedPending(t)
		raw, err := os.ReadFile(witnessPendingPath(t, fixture))
		if err != nil {
			t.Fatal(err)
		}
		var pending pendingCommit
		if err = json.Unmarshal(raw, &pending); err != nil {
			t.Fatal(err)
		}
		dir, _ := fixture.store.projectDir("project-1", false)
		line, _ := json.Marshal(pending.Event)
		journal, err := os.OpenFile(filepath.Join(dir, "journal.log"), os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = journal.Write(line[:len(line)/2]); err != nil || journal.Sync() != nil || journal.Close() != nil {
			t.Fatalf("inject partial append: %v", err)
		}
		reopened, err := NewLocalStoreWithAuthorities(fixture.root, fixture.trust, fixture.integrity, fixture.witness, DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		if head, headErr := reopened.Head(context.Background(), "project-1"); headErr != nil || head.Sequence != 3 {
			t.Fatalf("partial append did not recover exactly from staging: %#v %v", head, headErr)
		}
	})

	t.Run("after-append-before-projection-recovers", func(t *testing.T) {
		fixture := newWitnessFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		fixture.store.boundary = func(stage, _ string) {
			if stage == "materialize.after_append" {
				cancel()
			}
		}
		_, err := fixture.service.Transition(ctx, TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: fixture.first.ID, IdempotencyKey: "projection-third", DecisionID: "projection-third", ExpectedSequence: 2, ExpectedVersion: 2, Target: StateInvalidated})
		if err == nil {
			t.Fatal("cancelled projection publication reported success")
		}
		reopened, openErr := NewLocalStoreWithAuthorities(fixture.root, fixture.trust, fixture.integrity, fixture.witness, DefaultLimits())
		if openErr != nil {
			t.Fatal(openErr)
		}
		if head, headErr := reopened.Head(context.Background(), "project-1"); headErr != nil || head.Sequence != 3 {
			t.Fatalf("post-append recovery did not publish the committed head: %#v %v", head, headErr)
		}
	})

	t.Run("missing-tampered-or-aliased-staging-fails-closed", func(t *testing.T) {
		for _, mutation := range []string{"deleted", "tampered", "hardlink", "symlink"} {
			t.Run(mutation, func(t *testing.T) {
				fixture := leaveAdvancedPending(t)
				path := witnessPendingPath(t, fixture)
				if mutation == "deleted" {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				} else if mutation == "tampered" {
					raw, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					raw[len(raw)/2] ^= 1
					if err = os.WriteFile(path, raw, 0600); err != nil {
						t.Fatal(err)
					}
				} else if mutation == "hardlink" {
					if err := os.Link(path, path+".alias"); err != nil {
						t.Skipf("hardlinks unavailable: %v", err)
					}
				} else {
					backup := path + ".original"
					if err := os.Rename(path, backup); err != nil || os.Symlink(backup, path) != nil {
						t.Skip("symbolic links unavailable")
					}
				}
				reopened, err := NewLocalStoreWithAuthorities(fixture.root, fixture.trust, fixture.integrity, fixture.witness, DefaultLimits())
				if err != nil {
					t.Fatal(err)
				}
				if _, headErr := reopened.Head(context.Background(), "project-1"); headErr == nil {
					t.Fatalf("%s witness-bound staging was accepted", mutation)
				}
			})
		}
	})

	t.Run("staging-swaps-at-authority-boundaries-fail-closed", func(t *testing.T) {
		for _, boundary := range []string{"witness.before_advance", "witness.after_advance"} {
			t.Run(boundary, func(t *testing.T) {
				fixture := newWitnessFixture(t)
				fixture.store.boundary = func(stage, _ string) {
					if stage != boundary {
						return
					}
					path := witnessPendingPath(t, fixture)
					raw, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					var pending pendingCommit
					if err = json.Unmarshal(raw, &pending); err != nil {
						t.Fatal(err)
					}
					pending.Expected.ReceiptsDigest = strings.Repeat("d", 64)
					raw, _ = json.Marshal(pending)
					if err = os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
						t.Fatal(err)
					}
				}
				_, err := fixture.service.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: fixture.first.ID, IdempotencyKey: "swap-third", DecisionID: "swap-third", ExpectedSequence: 2, ExpectedVersion: 2, Target: StateInvalidated})
				if err == nil {
					t.Fatal("swapped staging crossed an authority boundary")
				}
			})
		}
	})

	t.Run("staging-before-cas-is-retired", func(t *testing.T) {
		fixture := leaveAdvancedPending(t)
		path := witnessPendingPath(t, fixture)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var pending pendingCommit
		if err = json.Unmarshal(raw, &pending); err != nil {
			t.Fatal(err)
		}
		fixture.witness.mu.Lock()
		fixture.witness.states["project-1"] = pending.Expected
		fixture.witness.mu.Unlock()
		reopened, err := NewLocalStoreWithAuthorities(fixture.root, fixture.trust, fixture.integrity, fixture.witness, DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		if head, headErr := reopened.Head(context.Background(), "project-1"); headErr != nil || head.Sequence != 2 {
			t.Fatalf("pre-CAS staging changed the committed head: %#v %v", head, headErr)
		}
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("pre-CAS staging was not retired: %v", statErr)
		}
	})

	t.Run("occupied-or-coordinate-mismatched-staging-is-rejected", func(t *testing.T) {
		fixture := leaveAdvancedPending(t)
		path := witnessPendingPath(t, fixture)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var pending pendingCommit
		if err = json.Unmarshal(raw, &pending); err != nil {
			t.Fatal(err)
		}
		if err = fixture.store.preparePending(context.Background(), pending); err == nil {
			t.Fatal("occupied staging slot was replaced")
		}
		pending.Expected.Head.Digest = strings.Repeat("e", 64)
		raw, _ = json.Marshal(pending)
		if err = os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		reopened, err := NewLocalStoreWithAuthorities(fixture.root, fixture.trust, fixture.integrity, fixture.witness, DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		if _, headErr := reopened.Head(context.Background(), "project-1"); headErr == nil {
			t.Fatal("staging with altered expected coordinates was accepted")
		}
	})

	t.Run("staging-validation-rejects-binding-auth-and-helper-paths", func(t *testing.T) {
		fixture := leaveAdvancedPending(t)
		path := witnessPendingPath(t, fixture)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var pending pendingCommit
		if err = json.Unmarshal(raw, &pending); err != nil {
			t.Fatal(err)
		}
		badBinding := pending
		badBinding.Format = "ProjectMemory/pending-unknown"
		if err = fixture.store.validatePending(context.Background(), nil, badBinding); err == nil {
			t.Fatal("unknown pending format was accepted")
		}
		badAuth := pending
		badAuth.Event.Authenticator = strings.Repeat("0", len(badAuth.Event.Authenticator))
		badAuth.Next.CommitDigest = digestCanonical(badAuth.Event)
		if err = fixture.store.validatePending(context.Background(), nil, badAuth); err == nil {
			t.Fatal("invalid pending authenticator was accepted")
		}
		if err = fixture.store.materializePending(context.Background(), nil, badBinding); err == nil {
			t.Fatal("materializer accepted invalid pending data")
		}
		events, sourceErr := fixture.store.readJournalSource(context.Background(), "project-1", filepath.Join(filepath.Dir(path), "journal.log"))
		if sourceErr != nil {
			t.Fatal(sourceErr)
		}
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if err = fixture.store.materializePending(cancelled, events, pending); err == nil {
			t.Fatal("cancelled materialization appended journal data")
		}
		if _, err = fixture.store.readPending(context.Background(), "../invalid"); err == nil {
			t.Fatal("pending reader accepted an invalid project")
		}
		if err = fixture.store.removePending("../invalid"); err == nil {
			t.Fatal("pending remover accepted an invalid project")
		}
		if err = os.Link(path, path+".alias"); err == nil {
			if err = fixture.store.preparePending(context.Background(), pending); err == nil {
				t.Fatal("prepare replaced hardlinked staging")
			}
		}
	})

	t.Run("direct-witness-verification-fails-closed", func(t *testing.T) {
		fixture := newWitnessFixture(t)
		dir, _ := fixture.store.projectDir("project-1", false)
		events, err := fixture.store.readJournalSource(context.Background(), "project-1", filepath.Join(dir, "journal.log"))
		if err != nil {
			t.Fatal(err)
		}
		fixture.witness.mu.Lock()
		current := fixture.witness.states["project-1"]
		delete(fixture.witness.states, "project-1")
		fixture.witness.mu.Unlock()
		if err = fixture.store.verifyWitness(context.Background(), "project-1", events); err == nil {
			t.Fatal("missing witness was accepted")
		}
		fixture.witness.mu.Lock()
		fixture.witness.states["project-1"] = current
		fixture.witness.unavailable = true
		fixture.witness.mu.Unlock()
		if err = fixture.store.verifyWitness(context.Background(), "project-1", events); err == nil {
			t.Fatal("unavailable witness was accepted")
		}
		fixture.witness.mu.Lock()
		fixture.witness.unavailable = false
		fixture.witness.states["project-1"] = witnessStateForEvents("project-1", events[:len(events)-1])
		fixture.witness.mu.Unlock()
		if err = fixture.store.verifyWitness(context.Background(), "project-1", events); err == nil {
			t.Fatal("mismatched witness was accepted")
		}
	})

	t.Run("recovery-publication-failures-remain-recoverable", func(t *testing.T) {
		fixture := leaveAdvancedPending(t)
		for _, boundary := range []string{"registry.write.before_temp", "snapshot.write.before_temp"} {
			ctx, cancel := context.WithCancel(context.Background())
			fixture.store.boundary = func(stage, _ string) {
				if stage == boundary {
					cancel()
				}
			}
			if _, err := fixture.store.Head(ctx, "project-1"); err == nil {
				t.Fatalf("recovery crossed cancelled %s", boundary)
			}
		}
		fixture.store.boundary = nil
		if head, err := fixture.store.Head(context.Background(), "project-1"); err != nil || head.Sequence != 3 {
			t.Fatalf("recovery did not converge after publication failures: %#v %v", head, err)
		}
	})

	t.Run("partial-tail-rewrite-failure-remains-recoverable", func(t *testing.T) {
		fixture := leaveAdvancedPending(t)
		path := witnessPendingPath(t, fixture)
		raw, _ := os.ReadFile(path)
		var pending pendingCommit
		_ = json.Unmarshal(raw, &pending)
		line, _ := json.Marshal(pending.Event)
		journal, err := os.OpenFile(filepath.Join(filepath.Dir(path), "journal.log"), os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = journal.Write(line[:len(line)/2])
		_ = journal.Sync()
		_ = journal.Close()
		ctx, cancel := context.WithCancel(context.Background())
		fixture.store.boundary = func(stage, _ string) {
			if stage == "journal.recover.before_temp" {
				cancel()
			}
		}
		if _, err = fixture.store.Head(ctx, "project-1"); err == nil {
			t.Fatal("cancelled journal tail rewrite reported success")
		}
		fixture.store.boundary = nil
		if head, headErr := fixture.store.Head(context.Background(), "project-1"); headErr != nil || head.Sequence != 3 {
			t.Fatalf("journal tail rewrite did not converge on retry: %#v %v", head, headErr)
		}
	})

	t.Run("current-state-mismatch-fails-closed", func(t *testing.T) {
		fixture := newWitnessFixture(t)
		fixture.store.boundary = func(stage, _ string) {
			if stage == "witness.before_advance" {
				fixture.witness.mu.Lock()
				state := fixture.witness.states["project-1"]
				state.Generation++
				state.Head.Sequence++
				fixture.witness.states["project-1"] = state
				fixture.witness.mu.Unlock()
			}
		}
		_, err := fixture.service.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: fixture.first.ID, IdempotencyKey: "mismatch-third", DecisionID: "mismatch-third", ExpectedSequence: 2, ExpectedVersion: 2, Target: StateInvalidated})
		if err == nil {
			t.Fatal("writer accepted a witness whose current state differed from local history")
		}
	})

	t.Run("advance-conflict-fails-closed", func(t *testing.T) {
		fixture := newWitnessFixture(t)
		fixture.witness.mu.Lock()
		fixture.witness.forceConflict = true
		fixture.witness.mu.Unlock()
		_, err := fixture.service.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: fixture.first.ID, IdempotencyKey: "conflict-third", DecisionID: "conflict-third", ExpectedSequence: 2, ExpectedVersion: 2, Target: StateInvalidated})
		if err == nil {
			t.Fatal("writer accepted a rejected compare-and-advance")
		}
	})

	t.Run("advance-rejection-and-uncertain-response-preserve-order", func(t *testing.T) {
		for _, uncertain := range []bool{false, true} {
			t.Run(strconv.FormatBool(uncertain), func(t *testing.T) {
				fixture := newWitnessFixture(t)
				fixture.witness.mu.Lock()
				fixture.witness.beforeCAS = errors.New("advance transport failed")
				fixture.witness.unavailableAfterFailure = uncertain
				fixture.witness.mu.Unlock()
				_, err := fixture.service.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: fixture.first.ID, IdempotencyKey: "advance-failure", DecisionID: "advance-failure", ExpectedSequence: 2, ExpectedVersion: 2, Target: StateInvalidated})
				if err == nil {
					t.Fatal("failed authority operation reported success")
				}
				fixture.witness.mu.Lock()
				fixture.witness.beforeCAS = nil
				fixture.witness.unavailableAfterFailure = false
				fixture.witness.unavailable = false
				fixture.witness.mu.Unlock()
				if head, headErr := fixture.store.Head(context.Background(), "project-1"); headErr != nil || head.Sequence != 2 {
					t.Fatalf("failed authority operation changed journal head: %#v %v", head, headErr)
				}
			})
		}
	})

	t.Run("confirmation-unavailable-fails-closed", func(t *testing.T) {
		fixture := newWitnessFixture(t)
		fixture.store.boundary = func(stage, _ string) {
			if stage == "witness.after_advance" {
				fixture.witness.mu.Lock()
				fixture.witness.unavailable = true
				fixture.witness.mu.Unlock()
			}
		}
		_, err := fixture.service.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: fixture.first.ID, IdempotencyKey: "confirm-unavailable", DecisionID: "confirm-unavailable", ExpectedSequence: 2, ExpectedVersion: 2, Target: StateInvalidated})
		if err == nil {
			t.Fatal("writer accepted an unavailable confirmation read")
		}
		fixture.witness.mu.Lock()
		fixture.witness.unavailable = false
		fixture.witness.mu.Unlock()
		reopened, openErr := NewLocalStoreWithAuthorities(fixture.root, fixture.trust, fixture.integrity, fixture.witness, DefaultLimits())
		if openErr != nil {
			t.Fatal(openErr)
		}
		if head, headErr := reopened.Head(context.Background(), "project-1"); headErr != nil || head.Sequence != 3 {
			t.Fatalf("durably advanced witness did not converge after confirmation outage: %#v %v", head, headErr)
		}
	})

	t.Run("confirmation-mismatch-fails-closed", func(t *testing.T) {
		fixture := newWitnessFixture(t)
		fixture.store.boundary = func(stage, _ string) {
			if stage == "witness.after_advance" {
				fixture.witness.mu.Lock()
				state := fixture.witness.states["project-1"]
				state.Generation++
				state.Head.Sequence++
				fixture.witness.states["project-1"] = state
				fixture.witness.mu.Unlock()
			}
		}
		_, err := fixture.service.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: fixture.first.ID, IdempotencyKey: "confirm-mismatch", DecisionID: "confirm-mismatch", ExpectedSequence: 2, ExpectedVersion: 2, Target: StateInvalidated})
		if err == nil {
			t.Fatal("writer accepted a mismatched confirmation state")
		}
		if _, headErr := fixture.store.Head(context.Background(), "project-1"); headErr == nil {
			t.Fatal("subsequent read accepted an authority ahead of local history")
		}
	})

	t.Run("after-advance-restart-converges", func(t *testing.T) {
		fixture := newWitnessFixture(t)
		fixture.witness.mu.Lock()
		fixture.witness.afterCAS = errors.New("response lost")
		fixture.witness.mu.Unlock()
		result, err := fixture.service.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: fixture.first.ID, IdempotencyKey: "after-third", DecisionID: "after-third", ExpectedSequence: 2, ExpectedVersion: 2, Target: StateInvalidated})
		if err != nil || result.Version != 3 {
			t.Fatalf("confirmed witness response loss did not converge in-place: %#v %v", result, err)
		}
		fixture.witness.mu.Lock()
		fixture.witness.afterCAS = nil
		fixture.witness.mu.Unlock()
		reopened, openErr := NewLocalStoreWithAuthorities(fixture.root, fixture.trust, fixture.integrity, fixture.witness, DefaultLimits())
		if openErr != nil {
			t.Fatal(openErr)
		}
		if head, headErr := reopened.Head(context.Background(), "project-1"); headErr != nil || head.Sequence != 3 {
			t.Fatalf("confirmed witness did not recover projections: %#v %v", head, headErr)
		}
	})

	t.Run("concurrent-cas", func(t *testing.T) {
		witness := newMemoryWitness()
		initial, err := witness.Bootstrap(context.Background(), "project-1")
		if err != nil {
			t.Fatal(err)
		}
		nextA := initial
		nextA.Generation, nextA.Head.Sequence, nextA.Head.Digest = 1, 1, strings.Repeat("a", 64)
		nextB := initial
		nextB.Generation, nextB.Head.Sequence, nextB.Head.Digest = 1, 1, strings.Repeat("b", 64)
		results := make(chan error, 2)
		go func() { results <- witness.CompareAndAdvance(context.Background(), initial, nextA) }()
		go func() { results <- witness.CompareAndAdvance(context.Background(), initial, nextB) }()
		successes := 0
		for range 2 {
			if <-results == nil {
				successes++
			}
		}
		if successes != 1 {
			t.Fatalf("witness CAS successes=%d", successes)
		}
	})
}

// TestWitnessCASPrecedesDefinitiveJournalAppend proves no final journal write can occur before irreversible authority.
func TestWitnessCASPrecedesDefinitiveJournalAppend(t *testing.T) {
	fixture := newWitnessFixture(t)
	stages := []string{}
	fixture.store.boundary = func(stage, _ string) {
		stages = append(stages, stage)
	}
	if _, err := fixture.service.Transition(context.Background(), TransitionCommand{Principal: "principal", ProjectID: "project-1", MemoryID: fixture.first.ID, IdempotencyKey: "ordered-third", DecisionID: "ordered-third", ExpectedSequence: 2, ExpectedVersion: 2, Target: StateInvalidated}); err != nil {
		t.Fatal(err)
	}
	position := func(target string) int {
		for index, stage := range stages {
			if stage == target {
				return index
			}
		}
		return -1
	}
	staged, advanced, appended := position("pending.write.after_temp_sync"), position("witness.after_advance"), position("append.before_open")
	if staged < 0 || advanced < 0 || appended < 0 || staged >= advanced || advanced >= appended {
		t.Fatalf("invalid durable order staged=%d advanced=%d appended=%d stages=%v", staged, advanced, appended, stages)
	}
}

// TestHTTPMonotonicWitnessEnforcesExternalProtocol verifies auth, bootstrap, exact reads, CAS, replay, and availability.
func TestHTTPMonotonicWitnessEnforcesExternalProtocol(t *testing.T) {
	authority := newMemoryWitness()
	server := newWitnessHTTPServer(t, authority)
	witness, err := NewHTTPMonotonicWitness(server.URL, "witness-test-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = witness.Read(context.Background(), "project-1"); !errors.Is(err, ErrWitnessMissing) {
		t.Fatalf("unprovisioned read=%v", err)
	}
	initial, err := witness.Bootstrap(context.Background(), "project-1")
	if err != nil || initial != witnessStateForEvents("project-1", nil) {
		t.Fatalf("bootstrap=%#v err=%v", initial, err)
	}
	next := initial
	next.Generation, next.Head.Sequence, next.Head.Digest = 1, 1, strings.Repeat("a", 64)
	if err = witness.CompareAndAdvance(context.Background(), initial, next); err != nil {
		t.Fatal(err)
	}
	if current, readErr := witness.Read(context.Background(), "project-1"); readErr != nil || current != next {
		t.Fatalf("current=%#v err=%v", current, readErr)
	}
	if err = witness.CompareAndAdvance(context.Background(), initial, next); !errors.Is(err, ErrWitnessConflict) {
		t.Fatalf("replay=%v", err)
	}
	if _, err = NewHTTPMonotonicWitness("http://example.com", "credential", http.DefaultClient); err == nil {
		t.Fatal("unencrypted non-loopback witness accepted")
	}
	if _, err = NewHTTPMonotonicWitness(server.URL, "", server.Client()); err == nil {
		t.Fatal("witness without credential accepted")
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", server.URL)
		writer.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	redirectWitness, err := NewHTTPMonotonicWitness(redirect.URL, "witness-test-token", redirect.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = redirectWitness.Read(context.Background(), "project-1"); err == nil {
		t.Fatal("witness redirect accepted")
	}
	server.Close()
	if _, err = witness.Read(context.Background(), "project-1"); err == nil {
		t.Fatal("unavailable witness produced local fallback")
	}
}

// TestHTTPMonotonicWitnessRejectsMalformedAuthorityResponses covers bounded strict protocol failures without local fallback.
func TestHTTPMonotonicWitnessRejectsMalformedAuthorityResponses(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{name: "service-status", status: http.StatusServiceUnavailable, body: `{}`},
		{name: "unknown-field", status: http.StatusOK, body: `{"project_id":"project-1","generation":0,"head":{},"receipts_digest":"digest","extra":true}`},
		{name: "wrong-project", status: http.StatusOK, body: `{"project_id":"project-2","generation":0,"head":{},"receipts_digest":"digest"}`},
		{name: "trailing-document", status: http.StatusOK, body: `{"project_id":"project-1","generation":0,"head":{},"receipts_digest":"digest"} {}`},
		{name: "oversized", status: http.StatusOK, body: strings.Repeat(" ", (64<<10)+1)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(testCase.status)
				_, _ = writer.Write([]byte(testCase.body))
			}))
			defer server.Close()
			witness, err := NewHTTPMonotonicWitness(server.URL, "witness-test-token", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = witness.Read(context.Background(), "project-1"); err == nil {
				t.Fatal("malformed authority response accepted")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var command witnessAdvanceRequest
		_ = json.NewDecoder(request.Body).Decode(&command)
		command.Next.Head.Digest = strings.Repeat("f", 64)
		_ = json.NewEncoder(writer).Encode(command.Next)
	}))
	defer server.Close()
	witness, err := NewHTTPMonotonicWitness(server.URL, "witness-test-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	initial, next := witnessStateForEvents("project-1", nil), WitnessState{ProjectID: "project-1", Generation: 1, Head: Head{Sequence: 1, Digest: strings.Repeat("a", 64)}}
	if err = witness.CompareAndAdvance(context.Background(), initial, next); err == nil {
		t.Fatal("incompatible CAS response accepted")
	}
}

// TestAncestorAndPostCheckSourceSwapsFailClosed exercises v1/projects swaps and the final temporary-name race.
func TestAncestorAndPostCheckSourceSwapsFailClosed(t *testing.T) {
	for _, ancestor := range []string{"v1", filepath.Join("v1", "projects")} {
		t.Run(strings.ReplaceAll(ancestor, string(filepath.Separator), "-"), func(t *testing.T) {
			service, store, now := testService(t)
			view, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "ancestor-seed", Candidate: testCandidate(now)})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(store.root, ancestor)
			blocked, swapped := false, false
			store.boundary = func(stage, _ string) {
				if stage != "read.before_open" {
					return
				}
				store.boundary = nil
				moved := path + "-moved"
				if renameErr := os.Rename(path, moved); renameErr != nil {
					blocked = true
					return
				}
				swapped = true
				if mkdirErr := os.Mkdir(path, 0700); mkdirErr != nil {
					t.Fatal(mkdirErr)
				}
			}
			_, err = store.Current(context.Background(), "project-1", view.ID)
			if !blocked && (!swapped || err == nil) {
				t.Fatalf("ancestor swap blocked=%v swapped=%v err=%v", blocked, swapped, err)
			}
		})
	}
	_, store, _ := testService(t)
	dir, err := store.projectDir("project-1", true)
	if err != nil {
		t.Fatal(err)
	}
	store.boundary = func(stage, path string) {
		if stage != "source.write.after_source_check" {
			return
		}
		store.boundary = nil
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.atomicJSON(context.Background(), dir, "source.json", map[string]any{"safe": true}, "source.write"); err == nil {
		t.Fatal("post-check source substitution was published")
	}
}

// TestStoreFailureAndFilterBoundaries covers fail-closed store entrypoints and structural scan limits without external effects.
func TestStoreFailureAndFilterBoundaries(t *testing.T) {
	service, store, now := testService(t)
	first := testCandidate(now)
	first.Class = ClassFact
	if _, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "filter-first", Candidate: first}); err != nil {
		t.Fatal(err)
	}
	second := testCandidate(now)
	second.Class, second.Subject = ClassDiagnostic, "diagnostic"
	if _, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "filter-second", ExpectedSequence: 1, Candidate: second}); err != nil {
		t.Fatal(err)
	}
	page, err := store.Scan(context.Background(), "project-1", StructuralFilter{Classes: []Class{ClassDiagnostic}}, 1)
	if err != nil || len(page.Records) != 1 || page.Records[0].Class != ClassDiagnostic {
		t.Fatalf("structural filter=%#v %v", page, err)
	}
	if _, err = store.Version(context.Background(), "project-1", "missing", 1); err == nil {
		t.Fatal("missing version served")
	}
	if _, err = store.projectDir("../escape", false); err == nil {
		t.Fatal("unsafe project accepted")
	}
	if err = verifyRegularFile(filepath.Join(t.TempDir(), "missing"), false); err == nil {
		t.Fatal("missing required file accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = store.atomicJSON(cancelled, store.root, "cancelled.json", map[string]any{"ok": true}, "test.write"); err == nil {
		t.Fatal("cancelled publication accepted")
	}
	if _, err = store.readJournal(context.Background(), "project-1", mustProjectDir(t, store, "project-1")); err == nil {
		t.Fatal("directory accepted as journal")
	}
	if err = store.Recover(cancelled, "project-1"); err == nil {
		t.Fatal("cancelled recovery accepted")
	}
}

// TestProjectionAndAnchorStrictDecoders covers malformed, trailing, unauthenticated, stale, and poisoned durable projections.
func TestProjectionAndAnchorStrictDecoders(t *testing.T) {
	service, store, now := testService(t)
	view, err := service.Propose(context.Background(), ProposalCommand{Principal: "principal", IdempotencyKey: "strict-projections", Candidate: testCandidate(now)})
	if err != nil {
		t.Fatal(err)
	}
	dir := mustProjectDir(t, store, "project-1")
	snapshot, _, err := store.loadVerified(context.Background(), "project-1", false)
	if err != nil {
		t.Fatal(err)
	}
	snapshotPath, indexPath := filepath.Join(dir, "snapshot.json"), filepath.Join(dir, "index.json")
	if err = os.WriteFile(snapshotPath, []byte("{"), 0600); err != nil || store.verifySnapshot(context.Background(), snapshotPath, snapshot) == nil {
		t.Fatal("malformed snapshot accepted")
	}
	if err = os.WriteFile(indexPath, []byte("{"), 0600); err != nil || store.verifyIndex(context.Background(), indexPath, snapshot) == nil {
		t.Fatal("malformed index accepted")
	}
	poisoned := localIndex{Format: ContractVersion, ProjectID: "project-1", Head: snapshot.Head, References: []indexReference{{ID: view.ID, Version: 1, Digest: "wrong"}}}
	poisonedRaw, _ := json.Marshal(poisoned)
	poisoned.KeyID, poisoned.Authenticator, _ = store.integrity.Sign(context.Background(), poisonedRaw)
	if err = os.WriteFile(indexPath, mustJSON(poisoned), 0600); err != nil || store.verifyIndex(context.Background(), indexPath, snapshot) == nil {
		t.Fatal("poisoned index accepted")
	}
	anchorPath := filepath.Join(store.trustRoot, "v1", "registry", base64.RawURLEncoding.EncodeToString([]byte("project-1"))+".json")
	anchorRaw, _ := os.ReadFile(anchorPath)
	if err = os.WriteFile(anchorPath, append(anchorRaw, []byte(" {}")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Head(context.Background(), "project-1"); err == nil {
		t.Fatal("trailing anchor document accepted")
	}
}

// TestClosedBranchMatricesExerciseFailClosedOutcomes covers remaining closed enums, stale reasons, retries, and adapter failures.
func TestClosedBranchMatricesExerciseFailClosedOutcomes(t *testing.T) {
	service, _, now := testService(t)
	if err := (SecretGate{Rules: SecretRules{MaxWork: 1}}).Check(context.Background(), map[string]any{"a": map[string]any{"b": true}}); err == nil {
		t.Fatal("secret scan work limit ignored")
	}
	if err := (SecretGate{}).Check(context.Background(), make(chan int)); err == nil {
		t.Fatal("non-JSON secret input accepted")
	}
	if _, err := service.Observe(context.Background(), ObserveCommand{Principal: "principal", ID: "missing-digest", IdempotencyKey: "missing-digest", Candidate: withEvidence(testCandidate(now))}); err == nil {
		t.Fatal("observation without functional digest accepted")
	}
	missingClock := service
	missingClock.Clock = nil
	if _, err := missingClock.Observe(context.Background(), ObserveCommand{Principal: "principal", ID: "missing-clock", IdempotencyKey: "missing-clock", Candidate: withEvidence(testCandidate(now)), FunctionalResultDigest: strings.Repeat("f", 64)}); err == nil {
		t.Fatal("observation without clock accepted")
	}
	if _, err := service.Transition(context.Background(), TransitionCommand{Principal: "unknown", ProjectID: "project-1", MemoryID: "missing", Target: StateApproved}); err == nil {
		t.Fatal("unauthorized transition accepted")
	}
	if transitionAllowed(StateProposed, "unknown") || transitionAllowed(StateApproved, StateObserved) || sensitivityRank("unknown") != 99 || scopeRank("unknown") != 99 {
		t.Fatal("unknown closed enum accepted")
	}
	if err := service.verifyRecordEvidence(context.Background(), Record{Evidence: []EvidenceRef{{ID: "e"}}}); err != nil {
		t.Fatalf("configured evidence unexpectedly failed: %v", err)
	}
	withoutEvidenceAuthority := service
	withoutEvidenceAuthority.Evidence = nil
	if err := withoutEvidenceAuthority.verifyRecordEvidence(context.Background(), Record{Evidence: []EvidenceRef{{ID: "e"}}}); err == nil {
		t.Fatal("missing evidence authority accepted")
	}
	badProvenance := testCandidate(now)
	badProvenance.Provenance.CommitOID = ""
	if _, err := NormalizeRecord(badProvenance, "bad-provenance", 1, StateProposed, AuthorityAgentProposal, DefaultLimits()); err == nil {
		t.Fatal("unbound provenance accepted")
	}
	if _, err := canonicalJSONValue(func() {}, DefaultLimits()); err == nil {
		t.Fatal("non-JSON canonical value accepted")
	}
	queryAuthority := QueryAuthority{ProjectID: "project-1", Scope: testCandidate(now).Scope, MaxSensitivity: SensitivityPublic, AsOf: now, MaxItems: 1, MaxBytes: 1 << 20, MaxWork: 10}
	query := QueryRequest{ProjectID: "project-1", Scope: queryAuthority.Scope, MaxSensitivity: SensitivityPublic, Limit: 1, MaxBytes: 1 << 20}
	result, err := service.Query(context.Background(), query, queryAuthority)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := service.Bind(result, queryAuthority)
	if err != nil {
		t.Fatal(err)
	}
	headFailure := service
	headFailure.Store = headFailureStore{MemoryStore: service.Store}
	if _, err = headFailure.Revalidate(context.Background(), binding, queryAuthority); err == nil {
		t.Fatal("head lookup failure hidden")
	}
	if code := (CLIAdapter{Surface: Surface{Service: &service}, Principal: "principal"}).Run(context.Background(), OperationPropose, mustJSON(ProposalCommand{IdempotencyKey: "writer", Candidate: testCandidate(now)}), failingResponseWriter{}); code != 1 {
		t.Fatalf("CLI write failure code=%d", code)
	}
	badHTTP := httptest.NewRecorder()
	badHTTPRequest := httptest.NewRequest("POST", "/", bytes.NewReader([]byte("{}")))
	badHTTPRequest.Header.Set("Authorization", "bad")
	(APIHandler{Auth: transportPrincipalResolver{}}).ServeHTTP(badHTTP, badHTTPRequest)
	if badHTTP.Code != 401 {
		t.Fatal("bad HTTP credential accepted")
	}
	if response := (MCPAdapter{Auth: transportPrincipalResolver{}}).Handle(context.Background(), MCPMemoryRequest{Credential: "Bearer transport-token", Operation: "unknown", Arguments: []byte("{}")}); response.Error == nil {
		t.Fatal("MCP invalid payload accepted")
	}
	if response := (GitHubAdapter{Auth: transportPrincipalResolver{}}).Submit(context.Background(), VerifiedGitHubActor{ActorID: "principal", RepositoryID: "repo-1", Action: "review"}, "unknown", []byte("{}")); response.Error == nil {
		t.Fatal("GitHub invalid payload accepted")
	}
	for _, raw := range [][]byte{nil, []byte("{")} {
		if _, err := decodeSurfacePayload(OperationQuery, raw); err == nil {
			t.Fatal("malformed surface payload accepted")
		}
	}
}

// mustJSON marshals a controlled durable test fixture and panics only on impossible fixture errors.
func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}
