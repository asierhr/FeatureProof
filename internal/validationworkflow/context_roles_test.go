package validationworkflow

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/contextassembly"
)

// workflowRoleConsumer retains the detached snapshot delivered by the productive role adapter.
type workflowRoleConsumer struct {
	snapshot contextassembly.Snapshot
	calls    int
}

// Consume records one isolated non-executable role projection.
func (c *workflowRoleConsumer) Consume(_ context.Context, snapshot contextassembly.Snapshot) error {
	c.calls++
	c.snapshot = snapshot
	return nil
}

// workflowRoleProducer seals one neutral item for application-level role integration tests.
func workflowRoleProducer(t *testing.T, authority contextassembly.AuthorityBinding, kind contextassembly.SourceKind, id string, domainAuthority contextassembly.Authority) contextassembly.Producer {
	t.Helper()
	item := contextassembly.Item{ID: id, Kind: kind, Trust: contextassembly.TrustUntrusted, Redaction: contextassembly.RedactionVerified, Authority: domainAuthority, SafeReference: string(kind) + ":" + id, ProjectID: authority.ProjectID, RepositoryID: authority.RepositoryID, Revision: authority.Revision, Scope: "revision", State: "active", Payload: contextassembly.SafeJSON(map[string]string{"value": "safe"})}
	projection, err := contextassembly.NewProjection(kind, "workflow-role", "v1", contextassembly.CompletenessComplete, []contextassembly.Item{item})
	if err != nil {
		t.Fatal(err)
	}
	return &contextassembly.StaticProducer{Source: kind, Projection: projection, Current: projection.Binding}
}

// TestProductAnalyzerAndDiagnosticUseSharedAssembly verifies productive role consumers remain least-privileged.
func TestProductAnalyzerAndDiagnosticUseSharedAssembly(t *testing.T) {
	profiles, err := contextassembly.DefaultProfiles()
	if err != nil {
		t.Fatal(err)
	}
	adapter := &ContextAssemblyAdapter{Profiles: profiles, Secrets: contextassembly.NewCanaryGate(nil), Tokens: contextassembly.ByteTokenCounter{Selections: map[string]bool{"provider/model": true}}, Store: contextassembly.NewMemoryStore(), Observer: ContextObserver{}, Clock: testContextClock{}}
	consume := func(role contextassembly.Role, kinds ...contextassembly.SourceKind) *workflowRoleConsumer {
		profile, _ := profiles.Profile(role)
		authority := contextassembly.AuthorityBinding{ProjectID: "project", RepositoryID: "repository", Revision: strings.Repeat("a", 40), ConfigurationDigest: strings.Repeat("b", 64), ProfileVersion: profile.Version, InstructionVersion: profile.InstructionVersion, Provider: "provider", Model: "model"}
		producers := make([]contextassembly.Producer, len(kinds))
		for index, kind := range kinds {
			domainAuthority := contextassembly.AuthorityVerified
			if kind == contextassembly.SourceHistory || kind == contextassembly.SourceEvidence {
				domainAuthority = contextassembly.AuthorityObserved
			}
			producers[index] = workflowRoleProducer(t, authority, kind, string(kind), domainAuthority)
		}
		consumer := &workflowRoleConsumer{}
		runID := "run-" + string(role)
		assembler, composeErr := contextassembly.NewAssembler(contextassembly.Config{ObserverTimeout: time.Second}, contextassembly.Dependencies{Profiles: profiles, Producers: producers, Secrets: adapter.Secrets, Tokens: adapter.Tokens, Store: adapter.Store, Observer: adapter.Observer, Clock: adapter.Clock})
		if composeErr != nil {
			t.Fatal(composeErr)
		}
		request := contextassembly.Request{Version: contextassembly.SnapshotVersion, IdempotencyKey: "role-" + string(role), RunID: runID, PlanID: "plan-" + string(role), Role: role, Authority: authority, Limits: profile.Budgets, MaxSensitivity: profile.MaxSensitivity}
		assembled, assembleErr := assembler.Assemble(context.Background(), request)
		if assembleErr != nil {
			t.Fatal(assembleErr)
		}
		digest, consumeErr := adapter.ConsumeRole(context.Background(), RoleContextRequest{Key: "role-" + string(role), RunID: runID, PlanID: "plan-" + string(role), ContextDigest: assembled.Digest, Role: role}, consumer)
		if consumeErr != nil || len(digest) != 64 || consumer.snapshot.Role != role {
			t.Fatalf("role=%s digest=%s snapshot=%#v err=%v", role, digest, consumer.snapshot, consumeErr)
		}
		return consumer
	}
	analyzer := consume(contextassembly.RoleAnalyzer, contextassembly.SourceConstraints, contextassembly.SourcePlan, contextassembly.SourceEvidence)
	diagnostic := consume(contextassembly.RoleDiagnostic, contextassembly.SourceConstraints, contextassembly.SourceDiagnostic, contextassembly.SourceHistory)
	for _, section := range analyzer.snapshot.Sections {
		for _, item := range section.Items {
			if item.Kind == contextassembly.SourceDiagnostic || item.Kind == contextassembly.SourceMemory {
				t.Fatalf("analyzer received forbidden source %s", item.Kind)
			}
		}
	}
	for _, section := range diagnostic.snapshot.Sections {
		for _, item := range section.Items {
			if item.Kind == contextassembly.SourceEvidence || item.Kind == contextassembly.SourcePlan || item.Kind == contextassembly.SourceMemory {
				t.Fatalf("diagnostic received forbidden source %s", item.Kind)
			}
		}
	}
}

// TestProductRoleConsumerRejectsCallerAuthority covers invalid roles, partial composition and profile substitution.
func TestProductRoleConsumerRejectsCallerAuthority(t *testing.T) {
	profiles, _ := contextassembly.DefaultProfiles()
	consumer := &workflowRoleConsumer{}
	partial := &ContextAssemblyAdapter{}
	if _, err := partial.ConsumeRole(context.Background(), RoleContextRequest{Role: contextassembly.RoleAnalyzer}, consumer); !IsCode(err, CodeInvalid) {
		t.Fatalf("partial role adapter accepted: %v", err)
	}
	adapter := &ContextAssemblyAdapter{Profiles: profiles, Secrets: contextassembly.NewCanaryGate(nil), Tokens: contextassembly.ByteTokenCounter{Selections: map[string]bool{"provider/model": true}}, Store: contextassembly.NewMemoryStore(), Observer: ContextObserver{}, Clock: testContextClock{}}
	if _, err := adapter.ConsumeRole(context.Background(), RoleContextRequest{Role: contextassembly.RolePlanner}, consumer); !IsCode(err, CodeInvalid) {
		t.Fatalf("planner crossed non-planner boundary: %v", err)
	}
	requestType := reflect.TypeOf(RoleContextRequest{})
	if _, exposed := requestType.FieldByName("Authority"); exposed {
		t.Fatal("role request exposes authority")
	}
	if _, exposed := requestType.FieldByName("Producers"); exposed {
		t.Fatal("role request exposes producer registry")
	}
	if _, err := adapter.ConsumeRole(context.Background(), RoleContextRequest{Key: "missing", RunID: "caller-selected", PlanID: "plan", Role: contextassembly.RoleAnalyzer}, consumer); !IsCode(err, CodeInvalid) || consumer.snapshot.Role != "" {
		t.Fatalf("caller-selected registry reached consumer: %v %#v", err, consumer.snapshot)
	}
	profile, _ := profiles.Profile(contextassembly.RoleDiagnostic)
	authority := contextassembly.AuthorityBinding{ProjectID: "project", RepositoryID: "repository", Revision: strings.Repeat("a", 40), ConfigurationDigest: strings.Repeat("b", 64), ProfileVersion: profile.Version, InstructionVersion: profile.InstructionVersion, Provider: "provider", Model: "model"}
	producers := []contextassembly.Producer{workflowRoleProducer(t, authority, contextassembly.SourceConstraints, "constraint", contextassembly.AuthorityVerified), workflowRoleProducer(t, authority, contextassembly.SourceDiagnostic, "diagnostic", contextassembly.AuthorityObserved), workflowRoleProducer(t, authority, contextassembly.SourceHistory, "history", contextassembly.AuthorityObserved)}
	assembler, composeErr := contextassembly.NewAssembler(contextassembly.Config{ObserverTimeout: time.Second}, contextassembly.Dependencies{Profiles: profiles, Producers: producers, Secrets: adapter.Secrets, Tokens: adapter.Tokens, Store: adapter.Store, Observer: adapter.Observer, Clock: adapter.Clock})
	if composeErr != nil {
		t.Fatal(composeErr)
	}
	request := contextassembly.Request{Version: contextassembly.SnapshotVersion, IdempotencyKey: "cross-authority", RunID: "cross-authority", PlanID: "plan", Role: contextassembly.RoleDiagnostic, Authority: authority, Limits: profile.Budgets, MaxSensitivity: profile.MaxSensitivity}
	assembled, assembleErr := assembler.Assemble(context.Background(), request)
	if assembleErr != nil {
		t.Fatal(assembleErr)
	}
	if _, err := adapter.ConsumeRole(context.Background(), RoleContextRequest{Key: "cross-authority", RunID: "other-run", PlanID: "plan", ContextDigest: assembled.Digest, Role: contextassembly.RoleDiagnostic}, consumer); !IsCode(err, CodeInvalid) || consumer.calls != 0 {
		t.Fatalf("cross-authority denial reached consumer: err=%v calls=%d", err, consumer.calls)
	}
	if _, err := adapter.ConsumeRole(context.Background(), RoleContextRequest{Key: "cross-source", RunID: "cross-authority", PlanID: "plan", ContextDigest: assembled.Digest, Role: contextassembly.RoleAnalyzer}, consumer); err == nil || consumer.calls != 0 {
		t.Fatalf("cross-source denial reached consumer: err=%v calls=%d", err, consumer.calls)
	}
}
