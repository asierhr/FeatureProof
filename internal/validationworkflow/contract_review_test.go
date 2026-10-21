package validationworkflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/specification"
)

// staticApprovedContract returns one exact reviewed snapshot for reducer semantics tests.
type staticApprovedContract struct {
	snapshot contractcatalog.SourceSnapshot
}

// ResolveApproved returns the configured source without adding epistemic strength.
func (a staticApprovedContract) ResolveApproved(context.Context, string, ApprovedContractReference) (contractcatalog.SourceSnapshot, error) {
	return a.snapshot, nil
}

// mutableApprovedContract simulates durable review authority changing between workflow checkpoints.
type mutableApprovedContract struct {
	mu             sync.Mutex
	calls, driftAt int
	digest         string
}

// ResolveApproved returns exact repository/head authority and injects digest drift at a selected checkpoint.
func (a *mutableApprovedContract) ResolveApproved(_ context.Context, _ string, reference ApprovedContractReference) (contractcatalog.SourceSnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	digest := a.digest
	if a.driftAt > 0 && a.calls >= a.driftAt {
		digest = strings.Repeat("f", 64)
	}
	return contractcatalog.SourceSnapshot{Version: contractcatalog.VersionV1, Source: contractcatalog.SourceSummary{ID: "review-" + reference.ReviewID, RepositoryID: "repo-1", Revision: strings.Repeat("a", 40), Kind: "review-approved-v1", Class: contractcatalog.ClassAuthoritative, Digest: digest, AdapterVersion: "contract-review-v1", Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessComplete}}, nil
}

// TestApprovedContractDriftBeforeHumanDecisionMakesZeroToolCalls proves the durable approval checkpoint precedes plan approval.
func TestApprovedContractDriftBeforeHumanDecisionMakesZeroToolCalls(t *testing.T) {
	service, _, execution, _, _ := workflowFixture(t, validPlanResponse())
	authority := &mutableApprovedContract{digest: strings.Repeat("d", 64)}
	service.deps.ContractReview = authority
	command := validCommand()
	command.ApprovedContract = &ApprovedContractReference{ReviewID: strings.Repeat("a", 24), ExpectedRevision: 1, ProjectionDigest: authority.digest}
	view, err := service.Start(context.Background(), command)
	if err != nil || view.Phase != PhaseAwaitingApproval {
		t.Fatalf("start=%#v %v", view, err)
	}
	authority.driftAt = authority.calls + 1
	_, err = service.Decide(context.Background(), DecisionCommand{Principal: command.Principal, Handle: view.Handle, DecisionID: "approve", Revision: view.ApprovalRevision, Action: DecisionApprove})
	if !IsCode(err, CodeConflict) || execution.callCount() != 0 {
		t.Fatalf("drift err=%v tools=%d", err, execution.callCount())
	}
}

// TestApprovedContractDriftAtPreToolCheckpointTerminatesInconclusive proves a second checkpoint closes the approval-to-execution window.
func TestApprovedContractDriftAtPreToolCheckpointTerminatesInconclusive(t *testing.T) {
	service, _, execution, _, _ := workflowFixture(t, validPlanResponse())
	authority := &mutableApprovedContract{digest: strings.Repeat("d", 64)}
	service.deps.ContractReview = authority
	command := validCommand()
	command.ApprovedContract = &ApprovedContractReference{ReviewID: strings.Repeat("a", 24), ExpectedRevision: 1, ProjectionDigest: authority.digest}
	view, err := service.Start(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	authority.driftAt = authority.calls + 2
	decided, err := service.Decide(context.Background(), DecisionCommand{Principal: command.Principal, Handle: view.Handle, DecisionID: "approve", Revision: view.ApprovalRevision, Action: DecisionApprove})
	if err != nil {
		t.Fatal(err)
	}
	if decided.Phase != PhaseTerminal || decided.Outcome == nil || *decided.Outcome != OutcomeInconclusive || execution.callCount() != 0 {
		t.Fatalf("view=%#v tools=%d calls=%d", decided, execution.callCount(), authority.calls)
	}
}

// TestApprovedContractReferenceIsAcceptedByMCPInput proves the productive MCP surface carries the same durable reference.
func TestApprovedContractReferenceIsAcceptedByMCPInput(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	authority := &mutableApprovedContract{digest: strings.Repeat("d", 64)}
	service.deps.ContractReview = authority
	input := mcpStartCommand{IdempotencyKey: "mcp-review", SpecificationRef: "spec-1", ContractRef: "contract-1", Provider: "fake", Model: "selected", AuthProfile: "profile", RuntimeProfile: "runtime-1", RepositoryID: "repo-1", Revision: strings.Repeat("a", 40), Approval: ApprovalRequired, TargetAlias: "target-1", ApprovedContract: &ApprovedContractReference{ReviewID: strings.Repeat("a", 24), ExpectedRevision: 1, ProjectionDigest: authority.digest}}
	raw, _ := json.Marshal(input)
	response := (MCPServer{Service: service, Principal: "user-1", AuthProfiles: map[string]string{"profile": strings.Repeat("8", 64)}, DirectTargets: map[string]DirectTarget{"target-1": *validCommand().Target.Direct}}).Handle(context.Background(), MCPRequest{Version: ContractVersion, Operation: "workflow.start", Arguments: raw})
	if response.Error != "" || authority.calls < 2 {
		t.Fatalf("response=%#v calls=%d", response, authority.calls)
	}
}

// TestApprovedReviewReducerKeepsPartialAbsenceInconclusive proves approval does not turn missing operations into negative evidence.
func TestApprovedReviewReducerKeepsPartialAbsenceInconclusive(t *testing.T) {
	digest := strings.Repeat("d", 64)
	snapshot := contractcatalog.SourceSnapshot{Version: contractcatalog.VersionV1, Source: contractcatalog.SourceSummary{ID: "review", RepositoryID: "repo-1", Revision: strings.Repeat("a", 40), Kind: "review-approved-v1", Class: contractcatalog.ClassAuthoritative, Digest: digest, AdapterVersion: "contract-review-v1", Confidence: contractcatalog.ConfidenceHigh, Completeness: contractcatalog.CompletenessPartial}, Omissions: contractcatalog.OmissionCounts{Unselected: 1}}
	rules := map[contractcatalog.ClaimKind]contractcatalog.ClaimRule{}
	for _, kind := range []contractcatalog.ClaimKind{contractcatalog.ClaimOperationIdentity, contractcatalog.ClaimSchema, contractcatalog.ClaimSecurity, contractcatalog.ClaimResponse} {
		rules[kind] = contractcatalog.ClaimRule{AllowedClasses: []contractcatalog.SourceClass{contractcatalog.ClassAuthoritative}, MinimumConfidence: map[contractcatalog.SourceClass]contractcatalog.Confidence{contractcatalog.ClassAuthoritative: contractcatalog.ConfidenceHigh}, RequiredCompleteness: contractcatalog.CompletenessComplete, Required: true}
	}
	policy, err := contractcatalog.NewPolicy(contractcatalog.SufficiencyPolicy{ID: "approved", Version: contractcatalog.VersionV1, Claims: rules, ConflictTreatment: contractcatalog.ConflictDeny})
	if err != nil {
		t.Fatal(err)
	}
	reducer := ApprovedReviewReducer{Authority: staticApprovedContract{snapshot: snapshot}, Policy: policy, CatalogLimits: contractcatalog.Limits{MaxSources: 2, MaxOperations: 8, MaxClaims: 64, MaxBytes: 1 << 20, MaxDepth: 16, MaxDiagnostics: 16, MaxLocationBytes: 256}, ReductionLimits: contractcatalog.ReductionLimits{MaxBytes: 1 << 20, MaxOperations: 8, MaxClaims: 64, MaxDepth: 16, MaxDiagnostics: 16}, ProjectionPolicy: contractcatalog.ProjectionPolicy{MaxStringBytes: 256, AllowedURLSchemes: []string{"https"}, SensitiveNames: []string{}, SensitivePaths: []string{}, SensitiveHeaderNames: []string{"authorization"}, ForbiddenValues: []string{}}, Selections: map[string][]contractcatalog.OperationKey{"c1": {{Method: "GET", Path: "/absent"}}}}
	_, err = reducer.ReduceApproved(context.Background(), StartCommand{Principal: "user-1", ApprovedContract: &ApprovedContractReference{ReviewID: strings.Repeat("a", 24), ExpectedRevision: 1, ProjectionDigest: digest}}, specification.Specification{Version: 1, Feature: specification.Feature{ID: "f"}, Constraints: []specification.Constraint{{ID: "c1", Description: "absence"}}, Limits: specification.Limits{MaxSteps: 1, MaxToolCalls: 1}})
	var decision *contractcatalog.DecisionError
	if !errors.As(err, &decision) || decision.Category != contractcatalog.CategoryInsufficient {
		t.Fatalf("partial absence became conclusive: %#v %v", decision, err)
	}
}
