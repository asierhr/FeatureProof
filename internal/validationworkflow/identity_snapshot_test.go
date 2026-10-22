package validationworkflow

import (
	"context"
	"testing"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/testidentity"
)

// blockingIdentityGeneration captures the service-owned snapshot and holds generation at a deterministic mutation checkpoint.
type blockingIdentityGeneration struct {
	entered chan AdmissionSnapshot
	proceed chan struct{}
}

// Generate exposes the isolated snapshot, waits for caller mutation, and returns bounded missing context without executable work.
func (g *blockingIdentityGeneration) Generate(ctx context.Context, snapshot AdmissionSnapshot) (agentplanning.Result, ModelUsage, error) {
	select {
	case g.entered <- snapshot:
	case <-ctx.Done():
		return agentplanning.Result{}, ModelUsage{}, ctx.Err()
	}
	select {
	case <-g.proceed:
	case <-ctx.Done():
		return agentplanning.Result{}, ModelUsage{}, ctx.Err()
	}
	return agentplanning.Result{Missing: &agentplanning.MissingContext{Items: []agentplanning.MissingItem{{Kind: agentplanning.MissingQuestion, ConstraintIDs: []string{"c1"}, Request: "bounded context"}}}}, ModelUsage{}, nil
}

// workflowIdentityDescriptors returns one canonical safe authorization snapshot for service mutation tests.
func workflowIdentityDescriptors() []testidentity.Descriptor {
	return []testidentity.Descriptor{{ID: "customer", Mechanism: testidentity.MechanismBearer, Operations: []testidentity.Operation{{Capability: "http.request", TargetID: "target-1", Method: "GET", Path: "/pets"}}}}
}

// TestAdmissionSnapshotDeepCopiesAndSealsIdentityDescriptors proves context digests and clones include complete canonical identity authorization.
func TestAdmissionSnapshotDeepCopiesAndSealsIdentityDescriptors(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Generation.Identities = workflowIdentityDescriptors()
	clone := snapshot.Clone()
	before, err := canonicalContextDigest(clone)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Generation.Identities[0].Operations[0].Path = "/mutated"
	after, err := canonicalContextDigest(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if clone.Generation.Identities[0].Operations[0].Path != "/pets" || before == after {
		t.Fatal("identity descriptors were aliased or omitted from the context seal")
	}
	nonCanonical := clone.Clone()
	nonCanonical.Generation.Identities = append(nonCanonical.Generation.Identities, testidentity.Descriptor{ID: "admin", Mechanism: testidentity.MechanismBearer, Operations: []testidentity.Operation{{Capability: "http.request", TargetID: "target-1", Method: "GET", Path: "/admin"}}})
	nonCanonical.Generation.Identities[0], nonCanonical.Generation.Identities[1] = nonCanonical.Generation.Identities[1], nonCanonical.Generation.Identities[0]
	if validateSnapshot(nonCanonical, validCommand()) == nil {
		t.Fatal("non-canonical identity snapshot accepted")
	}
}

// TestServiceIsolationSurvivesCallerIdentityMutation exercises the complete admission-to-generation copy boundary under the race detector.
func TestServiceIsolationSurvivesCallerIdentityMutation(t *testing.T) {
	service, admission, _, _, _ := workflowFixture(t, validPlanResponse())
	admission.snapshot.Generation.Identities = workflowIdentityDescriptors()
	generation := &blockingIdentityGeneration{entered: make(chan AdmissionSnapshot), proceed: make(chan struct{})}
	service.deps.Generation = generation
	done := make(chan error, 1)
	go func() {
		_, err := service.Start(context.Background(), validCommand())
		done <- err
	}()
	captured := <-generation.entered
	admission.snapshot.Generation.Identities[0].Operations[0].Path = "/caller-mutated"
	if captured.Generation.Identities[0].Operations[0].Path != "/pets" {
		t.Fatal("service generation snapshot shared caller identity state")
	}
	close(generation.proceed)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// TestApprovalRecoveryRejectsIdentityAuthorizationDrift proves the approved artifact cannot be executed after catalog mutation.
func TestApprovalRecoveryRejectsIdentityAuthorizationDrift(t *testing.T) {
	service, admission := identityWorkflowService(t)
	view, err := service.Start(context.Background(), validCommand())
	if err != nil {
		t.Fatal(err)
	}
	approval, err := service.ApprovalView(context.Background(), view.Handle)
	if err != nil {
		t.Fatal(err)
	}
	admission.mu.Lock()
	admission.snapshot.Generation.Identities[0].Operations[0].Path = "/drifted"
	admission.mu.Unlock()
	_, err = service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, DecisionID: "identity-drift", Revision: approval.Revision, Action: DecisionApprove})
	if !IsCode(err, CodeConflict) {
		t.Fatalf("identity authorization drift was accepted: %v", err)
	}
	if service.deps.Execution.(*fakeExecution).callCount() != 0 {
		t.Fatal("execution crossed drifted identity approval boundary")
	}
}
