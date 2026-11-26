package validationworkflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/projectconfig"
)

// rotatingWorkflowConfigurationResolver publishes one test-owned immutable head per admission without restarting the service.
type rotatingWorkflowConfigurationResolver struct {
	mu         sync.Mutex
	selections []string
	versions   []uint64
	next       int
}

// BindForWorkflow captures the next effective snapshot as though an activation happened between requests.
func (resolver *rotatingWorkflowConfigurationResolver) BindForWorkflow(_ context.Context, repositoryID, runID string) (projectconfig.RunConfigurationBinding, error) {
	resolver.mu.Lock()
	index := resolver.next
	resolver.next++
	resolver.mu.Unlock()
	if index >= len(resolver.selections) {
		return projectconfig.RunConfigurationBinding{}, errors.New("configuration fixture exhausted")
	}
	return rotatingWorkflowBinding(repositoryID, runID, resolver.selections[index], resolver.versions[index])
}

// PrepareForWorkflow exposes the same candidate to atomic workflow stores without a second binding write.
func (resolver *rotatingWorkflowConfigurationResolver) PrepareForWorkflow(ctx context.Context, repositoryID, runID string) (projectconfig.AdmissionCandidate, error) {
	binding, err := resolver.BindForWorkflow(ctx, repositoryID, runID)
	return projectconfig.AdmissionCandidate{Binding: binding}, err
}

// rotatingWorkflowMaterializer records the effective epoch and derives project-owned coordinates from its descriptors.
type rotatingWorkflowMaterializer struct {
	mu        sync.Mutex
	resources ProjectBoundResources
	digests   []string
	contracts []string
	snapshots int
}

// SnapshotForAdmission models a successful durable snapshot before the binding enters workflow state.
func (materializer *rotatingWorkflowMaterializer) SnapshotForAdmission(context.Context, projectconfig.RunConfigurationBinding) error {
	materializer.mu.Lock()
	materializer.snapshots++
	materializer.mu.Unlock()
	return nil
}

// Materialize proves each call sees its captured binding rather than process-start state.
func (materializer *rotatingWorkflowMaterializer) Materialize(_ context.Context, binding projectconfig.RunConfigurationBinding, command StartCommand) (ProjectBoundResources, StartCommand, error) {
	contract := ""
	for _, descriptor := range binding.Effective.Descriptors {
		if descriptor.Kind == projectconfig.ResourceContract {
			contract = descriptor.Selection
		}
	}
	command.ContractRef = contract
	materializer.mu.Lock()
	materializer.digests = append(materializer.digests, binding.EffectiveDigest)
	materializer.contracts = append(materializer.contracts, contract)
	materializer.mu.Unlock()
	return materializer.resources, command, nil
}

// rotatingWorkflowBinding constructs one valid effective epoch with a selectable contract descriptor.
func rotatingWorkflowBinding(repositoryID, runID, contract string, version uint64) (projectconfig.RunConfigurationBinding, error) {
	key, _ := projectconfig.NewProjectKey("organization", "project", repositoryID)
	digest := strings.Repeat(string(rune('a'+version-1)), 64)
	references := []projectconfig.ResourceReference{{Kind: projectconfig.ResourceProvider, ID: "fake", Version: version, Digest: digest}, {Kind: projectconfig.ResourceModelProfile, ID: "selected", Version: version, Digest: digest}, {Kind: projectconfig.ResourceRuntimeProfile, ID: "runtime-1", Version: version, Digest: digest}, {Kind: projectconfig.ResourceContract, ID: contract, Version: version, Digest: digest}, {Kind: projectconfig.ResourceEnvironment, ID: "target-1", Version: version, Digest: digest}}
	document, err := projectconfig.Canonicalize(projectconfig.Document{Schema: projectconfig.SchemaV1, OrganizationID: key.OrganizationID, ProjectID: key.ProjectID, RepositoryID: key.RepositoryID, References: references, Constraints: projectconfig.Constraints{Approval: "optional", RetentionDays: 7}})
	if err != nil {
		return projectconfig.RunConfigurationBinding{}, err
	}
	descriptors := make([]projectconfig.ResourceDescriptor, len(references))
	for index, reference := range references {
		descriptors[index] = projectconfig.ResourceDescriptor{Kind: reference.Kind, ID: reference.ID, Version: reference.Version, Digest: reference.Digest, ProjectKey: key, Selection: reference.ID}
	}
	effective, err := projectconfig.ValidateAndMerge(document, projectconfig.OrganizationPolicySnapshot{Version: version, Digest: digest, MaximumLimits: projectconfig.Limits{DurationSeconds: 100, ToolCalls: 100, ModelTokens: 1000, Replans: 100}, MinimumRetentionDays: 1, MaximumRetentionDays: 30}, projectconfig.ResourceCatalogSnapshot{ProjectKey: key, Digest: digest, Descriptors: descriptors, Limits: projectconfig.CatalogLimits{MaxDescriptors: 16, MaxOperationsPerDescriptor: 16, MaxCapabilitiesPerDescriptor: 16, MaxDependenciesPerDescriptor: 16, MaxDependencyEdges: 64, MaxGraphWork: 80}})
	if err != nil {
		return projectconfig.RunConfigurationBinding{}, err
	}
	binding := projectconfig.RunConfigurationBinding{RunID: runID, ProjectKey: key, Version: version, VersionDigest: document.Digest, ActivationGeneration: version, ActivationDigest: digest, PolicyVersion: effective.PolicyVersion, PolicyDigest: effective.PolicyDigest, CatalogDigest: effective.CatalogDigest, EffectiveDigest: effective.Digest, Effective: effective}
	return projectconfig.SealRunBinding(binding)
}

// workflowConfigurationResolver returns a safe binding tied to the requested run.
type workflowConfigurationResolver struct {
	calls     atomic.Int32
	omitModel bool
	limits    projectconfig.Limits
	approval  string
}

// BindForWorkflow records the causal boundary and creates a deterministic fixture binding.
func (resolver *workflowConfigurationResolver) BindForWorkflow(_ context.Context, repositoryID, runID string) (projectconfig.RunConfigurationBinding, error) {
	resolver.calls.Add(1)
	key, _ := projectconfig.NewProjectKey("organization", "project", repositoryID)
	digest := strings.Repeat("a", 64)
	references := []projectconfig.ResourceReference{{Kind: projectconfig.ResourceProvider, ID: "fake", Version: 1, Digest: digest}, {Kind: projectconfig.ResourceRuntimeProfile, ID: "runtime-1", Version: 1, Digest: digest}, {Kind: projectconfig.ResourceContract, ID: "contract-1", Version: 1, Digest: digest}, {Kind: projectconfig.ResourceEnvironment, ID: "target-1", Version: 1, Digest: digest}, {Kind: projectconfig.ResourceEnvironment, ID: "preview-1", Version: 1, Digest: digest}}
	if !resolver.omitModel {
		references = append(references, projectconfig.ResourceReference{Kind: projectconfig.ResourceModelProfile, ID: "selected", Version: 1, Digest: digest})
		references = append(references, projectconfig.ResourceReference{Kind: projectconfig.ResourceModelProfile, ID: "other", Version: 1, Digest: digest})
	}
	approval := resolver.approval
	if approval == "" {
		approval = "optional"
	}
	document, _ := projectconfig.Canonicalize(projectconfig.Document{Schema: projectconfig.SchemaV1, OrganizationID: key.OrganizationID, ProjectID: key.ProjectID, RepositoryID: key.RepositoryID, References: references, Constraints: projectconfig.Constraints{Limits: resolver.limits, Approval: approval, RetentionDays: 7}})
	descriptors := make([]projectconfig.ResourceDescriptor, len(references))
	for index, reference := range references {
		descriptors[index] = projectconfig.ResourceDescriptor{Kind: reference.Kind, ID: reference.ID, Version: reference.Version, Digest: reference.Digest, ProjectKey: key}
	}
	effective, _ := projectconfig.ValidateAndMerge(document, projectconfig.OrganizationPolicySnapshot{Version: 1, Digest: digest, MaximumLimits: projectconfig.Limits{DurationSeconds: 100, ToolCalls: 100, ModelTokens: 1000, Replans: 100}, MinimumRetentionDays: 1, MaximumRetentionDays: 30}, projectconfig.ResourceCatalogSnapshot{ProjectKey: key, Digest: digest, Descriptors: descriptors, Limits: projectconfig.CatalogLimits{MaxDescriptors: 16, MaxOperationsPerDescriptor: 16, MaxCapabilitiesPerDescriptor: 16, MaxDependenciesPerDescriptor: 16, MaxDependencyEdges: 64, MaxGraphWork: 80}})
	binding := projectconfig.RunConfigurationBinding{RunID: runID, ProjectKey: key, Version: 1, VersionDigest: document.Digest, ActivationGeneration: 1, ActivationDigest: digest, PolicyVersion: effective.PolicyVersion, PolicyDigest: effective.PolicyDigest, CatalogDigest: effective.CatalogDigest, EffectiveDigest: effective.Digest, Effective: effective}
	return projectconfig.SealRunBinding(binding)
}

// PrepareForWorkflow returns the same sealed fixture without creating a parallel checkpoint.
func (resolver *workflowConfigurationResolver) PrepareForWorkflow(ctx context.Context, repositoryID, runID string) (projectconfig.AdmissionCandidate, error) {
	binding, err := resolver.BindForWorkflow(ctx, repositoryID, runID)
	return projectconfig.AdmissionCandidate{Binding: binding}, err
}

// TestStartRejectsAliasesOutsideConfiguration proves request selections cannot expand authority.
func TestStartRejectsAliasesOutsideConfiguration(t *testing.T) {
	t.Parallel()
	service, admission, execution, _, _ := workflowFixture(t, validPlanResponse())
	service.deps.ProjectConfig = &workflowConfigurationResolver{omitModel: true}
	if _, err := service.Start(context.Background(), validCommand()); !IsCode(err, CodeUnauthorized) {
		t.Fatalf("got %v", err)
	}
	if admission.calls != 0 || execution.calls != 0 {
		t.Fatal("work started for unbound model alias")
	}
}

// TestStartPersistsProjectConfigurationBinding proves recovery never needs the active head.
func TestStartPersistsProjectConfigurationBinding(t *testing.T) {
	t.Parallel()
	service, _, _, store, _ := workflowFixture(t, validPlanResponse())
	resolver := &workflowConfigurationResolver{}
	service.deps.ProjectConfig = resolver
	view, err := service.Start(context.Background(), validCommand())
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(context.Background(), view.Handle)
	if err != nil {
		t.Fatal(err)
	}
	if resolver.calls.Load() != 1 || record.ProjectConfigurationBinding == nil || record.ProjectConfigurationBinding.RunID != view.Handle || !record.RetainUntil.Equal(record.UpdatedAt.Add(time.Duration(record.ProjectConfigurationBinding.Effective.Constraints.RetentionDays)*24*time.Hour)) {
		t.Fatal("configuration binding was not persisted with workflow")
	}
}

// failingWorkflowConfigurationResolver simulates unavailable authoritative configuration.
type failingWorkflowConfigurationResolver struct{}

// BindForWorkflow fails before environment, model or harness work can begin.
func (failingWorkflowConfigurationResolver) BindForWorkflow(context.Context, string, string) (projectconfig.RunConfigurationBinding, error) {
	return projectconfig.RunConfigurationBinding{}, context.Canceled
}

// PrepareForWorkflow fails before the local atomic workflow checkpoint is created.
func (failingWorkflowConfigurationResolver) PrepareForWorkflow(context.Context, string, string) (projectconfig.AdmissionCandidate, error) {
	return projectconfig.AdmissionCandidate{}, context.Canceled
}

// TestStartFailsClosedWithoutConfigurationBinding proves admission errors stop downstream work.
func TestStartFailsClosedWithoutConfigurationBinding(t *testing.T) {
	t.Parallel()
	service, admission, execution, _, _ := workflowFixture(t, validPlanResponse())
	service.deps.ProjectConfig = failingWorkflowConfigurationResolver{}
	if _, err := service.Start(context.Background(), validCommand()); !IsCode(err, CodeCancelled) {
		t.Fatalf("got %v", err)
	}
	if admission.calls != 0 || execution.calls != 0 {
		t.Fatal("work started without configuration binding")
	}
}

// TestBoundConfigurationEnforcesApprovalAndConfirmedUsage proves consumers use immutable run limits.
func TestBoundConfigurationEnforcesApprovalAndConfirmedUsage(t *testing.T) {
	t.Parallel()
	service, admission, execution, _, _ := workflowFixture(t, validPlanResponse())
	service.deps.ProjectConfig = &workflowConfigurationResolver{limits: projectconfig.Limits{ToolCalls: 1, ModelTokens: 10}, approval: "required"}
	command := validCommand()
	command.Approval = ApprovalDisabled
	if _, err := service.Start(context.Background(), command); !IsCode(err, CodeUnauthorized) {
		t.Fatalf("approval got %v", err)
	}
	if admission.calls != 0 || execution.calls != 0 {
		t.Fatal("work started before bound approval")
	}
	tests := []struct {
		usage ModelUsage
		want  bool
	}{{ModelUsage{Calls: 1, InputTokens: 5, OutputTokens: 5, Replans: 1}, true}, {ModelUsage{Calls: 2}, false}, {ModelUsage{Calls: 1, InputTokens: 6, OutputTokens: 5}, false}, {ModelUsage{Calls: 1, InputTokens: -1}, false}, {ModelUsage{Replans: 2}, false}, {ModelUsage{Replans: -1}, false}}
	for _, test := range tests {
		if got := usageWithinProjectConfiguration(test.usage, projectconfig.Limits{ToolCalls: 1, ModelTokens: 10, Replans: 1}); got != test.want {
			t.Fatalf("usage %+v got %t", test.usage, got)
		}
	}
}

// TestRunningServiceMaterializesEveryCapturedHead proves distinct selections and same-ID digest changes need no restart.
func TestRunningServiceMaterializesEveryCapturedHead(t *testing.T) {
	tests := []struct {
		name      string
		contracts []string
		want      []string
	}{{name: "distinct selections", contracts: []string{"contract-1", "contract-2"}, want: []string{"contract-1", "contract-2"}}, {name: "same id new digest", contracts: []string{"contract-1", "contract-1"}, want: []string{"contract-1", "contract-1"}}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _, _, _, _ := workflowFixture(t, validPlanResponse())
			service.deps.ProjectConfig = &rotatingWorkflowConfigurationResolver{selections: test.contracts, versions: []uint64{1, 2}}
			resources := ProjectBoundResources{Admission: service.deps.Admission, Generation: service.deps.Generation, Preparation: service.deps.Preparation, Approval: service.deps.Approval, Execution: service.deps.Execution, Packages: service.deps.Packages, Authorization: service.deps.Authorization, Environment: &fakePreview{acquireErr: errors.New("stop after admission")}}
			materializer := &rotatingWorkflowMaterializer{resources: resources}
			service.deps.ProjectResources = materializer
			for index := range test.contracts {
				command := validCommand()
				command.IdempotencyKey = "request-" + string(rune('1'+index))
				if _, err := service.Start(context.Background(), command); err != nil {
					t.Fatal(err)
				}
			}
			if strings.Join(materializer.contracts, ",") != strings.Join(test.want, ",") || len(materializer.digests) != 2 || materializer.digests[0] == materializer.digests[1] {
				t.Fatalf("materialization contracts=%v digests=%v", materializer.contracts, materializer.digests)
			}
		})
	}
}

// TestRecoveryMaterializesTheStoredBinding proves later lifecycle work never resolves a future active head.
func TestRecoveryMaterializesTheStoredBinding(t *testing.T) {
	service, _, _, _, _ := workflowFixture(t, validPlanResponse())
	resolver := &rotatingWorkflowConfigurationResolver{selections: []string{"contract-1"}, versions: []uint64{1}}
	service.deps.ProjectConfig = resolver
	resources := ProjectBoundResources{Admission: service.deps.Admission, Generation: service.deps.Generation, Preparation: service.deps.Preparation, Approval: service.deps.Approval, Execution: service.deps.Execution, Packages: service.deps.Packages, Authorization: service.deps.Authorization, Environment: service.deps.Environment}
	materializer := &rotatingWorkflowMaterializer{resources: resources}
	service.deps.ProjectResources = materializer
	view, err := service.Start(context.Background(), validCommand())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Decide(context.Background(), DecisionCommand{Principal: "user-1", Handle: view.Handle, Revision: view.ApprovalRevision, DecisionID: "reject-old-binding", Action: DecisionReject}); err != nil {
		t.Fatal(err)
	}
	if resolver.next != 1 || materializer.snapshots != 1 || len(materializer.digests) != 2 || materializer.digests[0] != materializer.digests[1] {
		t.Fatalf("recovery rebound or resnapshotted active configuration: resolver=%d snapshots=%d digests=%v", resolver.next, materializer.snapshots, materializer.digests)
	}
}
