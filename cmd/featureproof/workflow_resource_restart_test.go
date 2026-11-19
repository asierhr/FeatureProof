package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/externalagent"
	"github.com/asierhr/featureProof/internal/previewenv"
	"github.com/asierhr/featureProof/internal/projectconfig"
)

// inertExternalExecutor supplies the productive materializer's execution port without performing external work.
type inertExternalExecutor struct{}

// Execute is unreachable in artifact reconstruction tests and returns a deterministic terminal result if invoked.
func (inertExternalExecutor) Execute(context.Context, externalagent.Selection, string) (externalagent.ExecutionResult, error) {
	return externalagent.ExecutionResult{Status: externalagent.OutcomePassed}, nil
}

// externalArtifactReaderFunc injects exact historical payload and failure branches without a second persistence implementation.
type externalArtifactReaderFunc func(context.Context, projectconfig.ResourceDescriptor) ([]byte, error)

// GetArtifact delegates the historical read to the test-owned function.
func (reader externalArtifactReaderFunc) GetArtifact(ctx context.Context, descriptor projectconfig.ResourceDescriptor) ([]byte, error) {
	return reader(ctx, descriptor)
}

// TestWorkflowResourceMaterializerRestartsFromHistoricalArtifact proves mutable selector backing cannot replace an admitted resource after restart.
func TestWorkflowResourceMaterializerRestartsFromHistoricalArtifact(t *testing.T) {
	root := t.TempDir()
	configPath, _, contractPath := writeWorkflowCompositionFixture(t, root, workflowContractFixture(), "/pets", workflowProjectionFixture())
	t.Setenv("FEATUREPROOF_WORKFLOW_CONFIG", configPath)
	configuration, err := composeWorkflowProjectConfiguration(context.Background(), root, "test", "repo", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := configuration.Resolver.BindForWorkflow(context.Background(), "repo", "historical-run")
	if err != nil {
		t.Fatal(err)
	}
	registryRoot := filepath.Join(root, "durable-artifacts")
	store, err := projectconfig.NewLocalResourceArtifactStore(registryRoot)
	if err != nil {
		t.Fatal(err)
	}
	first := &workflowProjectResourceMaterializer{artifacts: store, artifactRoot: filepath.Join(root, "materialized-one")}
	if err = first.SnapshotForAdmission(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	oldConfig, err := first.loadBoundConfig(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	oldBytes, err := os.ReadFile(oldConfig.ContractPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(contractPath, []byte(`{"openapi":"3.0.0","paths":{"/mutated":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := projectconfig.NewLocalResourceArtifactStore(registryRoot)
	if err != nil {
		t.Fatal(err)
	}
	second := &workflowProjectResourceMaterializer{artifacts: reopened, artifactRoot: filepath.Join(root, "materialized-two")}
	recoveredConfig, err := second.loadBoundConfig(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := os.ReadFile(recoveredConfig.ContractPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(recovered) != string(oldBytes) || string(recovered) == `{"openapi":"3.0.0","paths":{"/mutated":{}}}` {
		t.Fatalf("historical artifact changed across restart: %s", recovered)
	}
}

// TestWorkflowResourceMaterializerFailsClosedWithoutExactHistoricalArtifacts covers missing backing and descriptor-payload mismatch.
func TestWorkflowResourceMaterializerFailsClosedWithoutExactHistoricalArtifacts(t *testing.T) {
	root := t.TempDir()
	configPath, _, contractPath := writeWorkflowCompositionFixture(t, root, workflowContractFixture(), "/pets", workflowProjectionFixture())
	t.Setenv("FEATUREPROOF_WORKFLOW_CONFIG", configPath)
	configuration, err := composeWorkflowProjectConfiguration(context.Background(), root, "test", "repo", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := configuration.Resolver.BindForWorkflow(context.Background(), "repo", "missing-artifact-run")
	if err != nil {
		t.Fatal(err)
	}
	missingRoot := filepath.Join(root, "missing-store")
	missingStore, err := projectconfig.NewLocalResourceArtifactStore(missingRoot)
	if err != nil {
		t.Fatal(err)
	}
	admission := &workflowProjectResourceMaterializer{artifacts: missingStore, artifactRoot: filepath.Join(root, "admission-materialized")}
	if err = admission.SnapshotForAdmission(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(missingRoot); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(contractPath, []byte(`{"openapi":"3.0.0","paths":{"/current-valid-and-distinct":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	missingStore, err = projectconfig.NewLocalResourceArtifactStore(missingRoot)
	if err != nil {
		t.Fatal(err)
	}
	missing := &workflowProjectResourceMaterializer{artifacts: missingStore, artifactRoot: filepath.Join(root, "missing-materialized")}
	if _, err = missing.loadBoundConfig(context.Background(), binding); err == nil {
		t.Fatal("missing historical artifact adopted valid current backing")
	}
	anchor := workflowContractDescriptor(binding.Effective)
	if anchor == nil {
		t.Fatal("missing contract descriptor")
	}
	if _, err = missingStore.Get(context.Background(), *anchor); !projectconfig.IsCategory(err, projectconfig.CategoryMissingReference) {
		t.Fatalf("historical read repopulated artifact: %v", err)
	}
	corruptStore, err := projectconfig.NewLocalResourceArtifactStore(filepath.Join(root, "corrupt-store"))
	if err != nil {
		t.Fatal(err)
	}
	current, err := loadWorkflowConfig()
	if err != nil {
		t.Fatal(err)
	}
	specification, _ := os.ReadFile(current.SpecificationPath)
	runtime, _ := os.ReadFile(current.RuntimeManifestPath)
	contract, _ := os.ReadFile(current.ContractPath)
	bundle, _ := json.Marshal(workflowResourceBundle{Config: current, Specification: specification, Runtime: runtime, Contract: contract})
	for _, descriptor := range binding.Effective.Descriptors {
		payload, _ := json.Marshal(descriptor)
		if descriptor.Kind == projectconfig.ResourceContract {
			payload = bundle
		} else if descriptor.Kind == projectconfig.ResourceProvider {
			payload = []byte(`{}`)
		}
		if err = corruptStore.Put(context.Background(), descriptor, payload); err != nil {
			t.Fatal(err)
		}
	}
	corrupt := &workflowProjectResourceMaterializer{artifacts: corruptStore, artifactRoot: filepath.Join(root, "corrupt-materialized")}
	if _, err = corrupt.loadBoundConfig(context.Background(), binding); err == nil {
		t.Fatal("descriptor payload mismatch accepted")
	}
}

// TestExternalResourceMaterializerDoesNotRepopulateHistoricalArtifact proves reads after restart never serialize the current selection.
func TestExternalResourceMaterializerDoesNotRepopulateHistoricalArtifact(t *testing.T) {
	root := t.TempDir()
	configPath, _, _ := writeWorkflowCompositionFixture(t, root, workflowContractFixture(), "/pets", workflowProjectionFixture())
	t.Setenv("FEATUREPROOF_WORKFLOW_CONFIG", configPath)
	configuration, err := composeWorkflowProjectConfiguration(context.Background(), root, "test", "repo", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := configuration.Resolver.BindForWorkflow(context.Background(), "repo", "external-artifact-run")
	if err != nil {
		t.Fatal(err)
	}
	descriptor := externalEnvironmentDescriptor(binding)
	if descriptor == nil {
		t.Fatal("missing environment descriptor")
	}
	registryRoot := filepath.Join(root, "external-artifacts")
	store, err := externalagent.NewLocalRegistry(registryRoot)
	if err != nil {
		t.Fatal(err)
	}
	first := externalProjectResourceMaterializer{principalID: "principal", selection: externalagent.Selection{Alias: descriptor.Selection, Value: previewenv.ConsumerRequest{Selection: previewenv.Selection{Alias: descriptor.Selection, ProviderID: "admitted"}}}, executor: inertExternalExecutor{}, artifacts: store}
	snapshot, resources, err := first.PrepareAdmission(context.Background(), binding)
	if err != nil || resources.Catalog == nil {
		t.Fatal(err)
	}
	record := externalagent.RunRecord{Handle: "external-handle-01", InternalID: binding.RunID, PrincipalID: "principal", SelectionAlias: descriptor.Selection, RequestHash: strings.Repeat("a", 64), SubmitKeyHash: strings.Repeat("b", 64), Phase: externalagent.PhaseQueued, Owner: "owner", UpdatedAt: time.Now().UTC(), Configuration: &binding, RetentionNS: int64(time.Hour), RunTimeoutNS: int64(time.Minute)}
	if _, err = store.ReserveConfigured(context.Background(), record, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = first.Materialize(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	store.ReleaseOwner(record.Handle)
	if err = os.RemoveAll(registryRoot); err != nil {
		t.Fatal(err)
	}
	reopened, err := externalagent.NewLocalRegistry(registryRoot)
	if err != nil {
		t.Fatal(err)
	}
	second := externalProjectResourceMaterializer{principalID: "principal", selection: externalagent.Selection{Alias: descriptor.Selection, Value: previewenv.ConsumerRequest{Selection: previewenv.Selection{Alias: descriptor.Selection, ProviderID: "current-distinct"}}}, executor: inertExternalExecutor{}, artifacts: reopened}
	if _, err = second.Materialize(context.Background(), binding); err == nil {
		t.Fatal("external historical read adopted current selection")
	}
	if _, err = reopened.GetArtifact(context.Background(), *descriptor); !projectconfig.IsCategory(err, projectconfig.CategoryMissingReference) {
		t.Fatalf("external historical read repopulated artifact: %v", err)
	}
}

// TestExternalResourceMaterializerRejectsInvalidCompositionAndHistoricalPayloads covers every closed product-materialization boundary.
func TestExternalResourceMaterializerRejectsInvalidCompositionAndHistoricalPayloads(t *testing.T) {
	root := t.TempDir()
	configPath, _, _ := writeWorkflowCompositionFixture(t, root, workflowContractFixture(), "/pets", workflowProjectionFixture())
	t.Setenv("FEATUREPROOF_WORKFLOW_CONFIG", configPath)
	configuration, err := composeWorkflowProjectConfiguration(context.Background(), root, "test", "repo", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := configuration.Resolver.BindForWorkflow(context.Background(), "repo", "external-negative-run")
	if err != nil {
		t.Fatal(err)
	}
	descriptor := externalEnvironmentDescriptor(binding)
	if descriptor == nil {
		t.Fatal("environment descriptor unavailable")
	}
	failingReader := externalArtifactReaderFunc(func(context.Context, projectconfig.ResourceDescriptor) ([]byte, error) {
		return nil, errors.New("artifact unavailable")
	})
	if _, _, err = (externalProjectResourceMaterializer{}).PrepareAdmission(context.Background(), binding); err == nil {
		t.Fatal("empty composition admitted")
	}
	mismatch := externalProjectResourceMaterializer{principalID: "principal", selection: externalagent.Selection{Alias: "other"}, executor: inertExternalExecutor{}, artifacts: failingReader}
	if _, _, err = mismatch.PrepareAdmission(context.Background(), binding); err == nil {
		t.Fatal("selection mismatch admitted")
	}
	unencodable := externalProjectResourceMaterializer{principalID: "principal", selection: externalagent.Selection{Alias: descriptor.Selection, Value: make(chan int)}, executor: inertExternalExecutor{}, artifacts: failingReader}
	if _, _, err = unencodable.PrepareAdmission(context.Background(), binding); err == nil {
		t.Fatal("unencodable selection admitted")
	}
	valid := externalProjectResourceMaterializer{principalID: "principal", selection: externalagent.Selection{Alias: descriptor.Selection}, executor: inertExternalExecutor{}, artifacts: failingReader}
	if _, err = valid.Materialize(context.Background(), binding); err == nil {
		t.Fatal("missing historical artifact accepted")
	}
	invalidReader := externalArtifactReaderFunc(func(context.Context, projectconfig.ResourceDescriptor) ([]byte, error) {
		return []byte("not-json"), nil
	})
	valid.artifacts = invalidReader
	if _, err = valid.Materialize(context.Background(), binding); err == nil {
		t.Fatal("invalid historical payload accepted")
	}
	payload, _ := json.Marshal(previewenv.ConsumerRequest{Selection: previewenv.Selection{Alias: "other"}})
	if _, err = valid.resourcesFromPayload(binding, *descriptor, payload); err == nil {
		t.Fatal("historical alias mismatch accepted")
	}
	payload, _ = json.Marshal(previewenv.ConsumerRequest{Selection: previewenv.Selection{Alias: descriptor.Selection}})
	valid.principalID = ""
	if _, err = valid.resourcesFromPayload(binding, *descriptor, payload); err == nil {
		t.Fatal("empty principal accepted")
	}
	invalidBinding := binding.Clone()
	invalidBinding.BindingDigest = ""
	if _, err = valid.Materialize(context.Background(), invalidBinding); err == nil {
		t.Fatal("invalid binding accepted")
	}
	withoutEnvironment := binding.Clone()
	withoutEnvironment.Effective.Descriptors = nil
	if externalEnvironmentDescriptor(withoutEnvironment) != nil {
		t.Fatal("invented environment descriptor")
	}
}
