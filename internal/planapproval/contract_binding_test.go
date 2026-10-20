package planapproval

import (
	"context"
	"testing"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// TestNeutralContractBindingInvalidatesCatalogPolicyAndRetainedEpistemicDrift proves approval seals every authority dimension.
func TestNeutralContractBindingInvalidatesCatalogPolicyAndRetainedEpistemicDrift(t *testing.T) {
	_, base := approvalFixture(t)
	original, err := fingerprintContext(context.Background(), base, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		contract contractcatalog.ReducedContext
	}{
		{name: "catalog_and_operation", contract: approvalContract(t, "/other", "policy", contractcatalog.ConfidenceHigh)},
		{name: "policy", contract: approvalContract(t, "/pets", "policy-v2", contractcatalog.ConfidenceHigh)},
		{name: "provenance_confidence", contract: approvalContract(t, "/pets", "policy", contractcatalog.ConfidenceMedium)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := cloneContext(base)
			changed.Contract = tc.contract
			fingerprint, fingerprintErr := fingerprintContext(context.Background(), changed, testLimits())
			if fingerprintErr != nil {
				t.Fatal(fingerprintErr)
			}
			if original.All == fingerprint.All || staleReason("plan", "plan", original, fingerprint) != ReasonContractChanged {
				t.Fatalf("drift was not bound: %#v %#v", original, fingerprint)
			}
		})
	}
}

// TestVerifiedContractSurvivesRestageRevisionViewAndAuthorizationRecovery exercises every approval read path with the same sealed contract authority.
func TestVerifiedContractSurvivesRestageRevisionViewAndAuthorizationRecovery(t *testing.T) {
	ctx := context.Background()
	plan, snapshot := singleApprovalFixture(t)
	workflow, _ := newTestWorkflow(t)
	view, err := workflow.Stage(ctx, StageCommand{Plan: plan, Context: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	restaged, err := workflow.Restage(ctx, StageCommand{Plan: plan, Context: snapshot})
	if err != nil || restaged.Revision != view.Revision {
		t.Fatalf("restage=%#v err=%v", restaged, err)
	}
	decision, err := workflow.Decide(ctx, DecisionCommand{PlanID: plan.ID(), DecisionID: "contract-approve", ExpectedRevision: view.Revision, Actor: Actor{Subject: "human", Source: "test"}, Action: ActionApprove, CurrentPlan: plan, CurrentContext: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	revisionView, err := workflow.ViewRevision(ctx, "run-1", view.Revision)
	if err != nil || revisionView.Revision != view.Revision {
		t.Fatalf("revision view=%#v err=%v", revisionView, err)
	}
	recovered, err := workflow.RecoverAuthorization(ctx, "run-1", view.Revision, plan, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Reference() == "" || decision.Authorization.Reference() != recovered.Reference() {
		t.Fatalf("authorization reference mismatch: %q %q", decision.Authorization.Reference(), recovered.Reference())
	}
}
