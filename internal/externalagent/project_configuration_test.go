package externalagent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/projectconfig"
)

// rotatingExternalConfiguration models two active heads admitted by one long-lived service.
type rotatingExternalConfiguration struct {
	mu       sync.Mutex
	versions []uint64
	next     int
}

// BindForWorkflow seals the next head to the exact external-agent internal run identity.
func (resolver *rotatingExternalConfiguration) BindForWorkflow(_ context.Context, repositoryID, runID string) (projectconfig.RunConfigurationBinding, error) {
	resolver.mu.Lock()
	version := resolver.versions[resolver.next]
	resolver.next++
	resolver.mu.Unlock()
	return externalBinding(repositoryID, runID, version)
}

// PrepareForWorkflow returns the next head without persisting a binding outside the causal run reservation.
func (resolver *rotatingExternalConfiguration) PrepareForWorkflow(ctx context.Context, repositoryID, runID string) (projectconfig.AdmissionCandidate, error) {
	binding, err := resolver.BindForWorkflow(ctx, repositoryID, runID)
	return projectconfig.AdmissionCandidate{Binding: binding}, err
}

// recordingBoundExecutor represents an executor constructed for one immutable configuration version.
type recordingBoundExecutor struct {
	mu       sync.Mutex
	versions []uint64
	version  uint64
}

// Execute records the version-specific consumer selected before worker execution.
func (executor *recordingBoundExecutor) Execute(context.Context, Selection, string) (ExecutionResult, error) {
	executor.mu.Lock()
	executor.versions = append(executor.versions, executor.version)
	executor.mu.Unlock()
	return ExecutionResult{Status: OutcomePassed}, nil
}

// externalBoundMaterializer derives the only authorized alias and limits from a sealed binding.
type externalBoundMaterializer struct{ tracker *externalAdmissionTracker }

// externalAdmissionTracker observes snapshot and compensation counts across concurrent submissions.
type externalAdmissionTracker struct {
	mu       sync.Mutex
	prepared int
}

// claimedReserveFailure keeps the real idempotency claim but injects the fault immediately before causal commit.
type claimedReserveFailure struct{ *LocalRegistry }

// Reserve fails after preparation so the service must compensate the speculative snapshot.
func (claimedReserveFailure) Reserve(context.Context, RunRecord) (ReserveResult, error) {
	return ReserveResult{}, errors.New("commit unavailable")
}

// ReserveConfigured injects the same failure at the atomic configured commit boundary.
func (claimedReserveFailure) ReserveConfigured(context.Context, RunRecord, ProjectResourceSnapshot) (ReserveResult, error) {
	return ReserveResult{}, errors.New("configured commit unavailable")
}

// PrepareAdmission builds one in-memory snapshot and its consumers without durable side effects.
func (materializer externalBoundMaterializer) PrepareAdmission(ctx context.Context, binding projectconfig.RunConfigurationBinding) (ProjectResourceSnapshot, ProjectBoundResources, error) {
	if materializer.tracker != nil {
		materializer.tracker.mu.Lock()
		materializer.tracker.prepared++
		materializer.tracker.mu.Unlock()
	}
	descriptor := binding.Effective.Descriptors[len(binding.Effective.Descriptors)-1]
	resources, err := materializer.Materialize(ctx, binding)
	return ProjectResourceSnapshot{Descriptor: descriptor, Payload: []byte("snapshot")}, resources, err
}

// Materialize constructs isolated run consumers rather than reusing process-start authority.
func (materializer externalBoundMaterializer) Materialize(_ context.Context, binding projectconfig.RunConfigurationBinding) (ProjectBoundResources, error) {
	alias := ""
	for _, descriptor := range binding.Effective.Descriptors {
		if descriptor.Kind == projectconfig.ResourceEnvironment {
			alias = descriptor.Selection
		}
	}
	executor := &recordingBoundExecutor{version: binding.Version}
	catalog := fixedCatalog{allowed: map[string]map[string]bool{"alice": {alias: true}}}
	return ProjectBoundResources{Catalog: catalog, Executor: executor, Retention: time.Duration(binding.Effective.Constraints.RetentionDays) * 24 * time.Hour, RunTimeout: time.Duration(binding.Effective.Constraints.Limits.DurationSeconds) * time.Second}, nil
}

// controlledExternalResolver injects one valid, corrupt or unavailable binding at the admission boundary.
type controlledExternalResolver struct {
	binding projectconfig.RunConfigurationBinding
	err     error
	raw     bool
}

// BindForWorkflow ties a prebuilt test binding to the generated run ID without changing its resource epoch.
func (resolver controlledExternalResolver) BindForWorkflow(_ context.Context, repositoryID, runID string) (projectconfig.RunConfigurationBinding, error) {
	if resolver.err != nil {
		return projectconfig.RunConfigurationBinding{}, resolver.err
	}
	if resolver.raw {
		return resolver.binding, nil
	}
	binding := resolver.binding.Clone()
	binding.RunID = runID
	binding.ProjectKey.RepositoryID = repositoryID
	binding.BindingDigest = ""
	return projectconfig.SealRunBinding(binding)
}

// PrepareForWorkflow exposes the controlled binding as a non-persistent admission candidate.
func (resolver controlledExternalResolver) PrepareForWorkflow(ctx context.Context, repositoryID, runID string) (projectconfig.AdmissionCandidate, error) {
	binding, err := resolver.BindForWorkflow(ctx, repositoryID, runID)
	return projectconfig.AdmissionCandidate{Binding: binding}, err
}

// controlledExternalMaterializer exposes fail-closed materialization and historical-read branches.
type controlledExternalMaterializer struct {
	resources ProjectBoundResources
	err       error
}

// PrepareAdmission exposes preparation failures independently from historical reads.
func (materializer *controlledExternalMaterializer) PrepareAdmission(_ context.Context, binding projectconfig.RunConfigurationBinding) (ProjectResourceSnapshot, ProjectBoundResources, error) {
	for _, descriptor := range binding.Effective.Descriptors {
		if descriptor.Kind == projectconfig.ResourceEnvironment {
			return ProjectResourceSnapshot{Descriptor: descriptor, Payload: []byte("controlled")}, materializer.resources, materializer.err
		}
	}
	return ProjectResourceSnapshot{}, materializer.resources, materializer.err
}

// Materialize returns only explicitly configured consumers or the injected safe failure.
func (materializer *controlledExternalMaterializer) Materialize(context.Context, projectconfig.RunConfigurationBinding) (ProjectBoundResources, error) {
	return materializer.resources, materializer.err
}

// TestExternalAgentCapturesEachHeadAndReadsHistoricalBinding proves one live service adopts a new head without rewriting its first run.
func TestExternalAgentCapturesEachHeadAndReadsHistoricalBinding(t *testing.T) {
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	registry, err := NewLocalRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolver := &rotatingExternalConfiguration{versions: []uint64{1, 2}}
	service, err := NewService(ServiceDependencies{Identity: identity, Catalog: fixedCatalog{allowed: map[string]map[string]bool{"alice": {"environment-1": true, "environment-2": true}}}, Registry: registry, Executor: &fakeExecutor{}, Packages: fakePackages{}, Clock: fixedClock{now: time.Unix(1000, 0).UTC()}, IDs: &sequenceIDs{}, Owner: "bound-owner", Retention: time.Hour, RunTimeout: time.Minute, RepositoryID: "repository", ProjectConfig: resolver, ProjectResources: externalBoundMaterializer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	first, err := service.Submit(context.Background(), SubmitInput{SelectionAlias: "environment-1", IdempotencyKey: "first-request-key"})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Wait(context.Background(), first.RunHandle); err != nil {
		t.Fatal(err)
	}
	second, err := service.Submit(context.Background(), SubmitInput{SelectionAlias: "environment-2", IdempotencyKey: "second-request-key"})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Wait(context.Background(), second.RunHandle); err != nil {
		t.Fatal(err)
	}
	firstRecord, _ := registry.Get(context.Background(), first.RunHandle)
	secondRecord, _ := registry.Get(context.Background(), second.RunHandle)
	if firstRecord.Configuration == nil || secondRecord.Configuration == nil || firstRecord.Configuration.Version != 1 || secondRecord.Configuration.Version != 2 || firstRecord.Configuration.EffectiveDigest == secondRecord.Configuration.EffectiveDigest {
		t.Fatalf("bindings first=%+v second=%+v", firstRecord.Configuration, secondRecord.Configuration)
	}
	if _, err = service.Status(context.Background(), HandleInput{RunHandle: first.RunHandle}); err != nil {
		t.Fatalf("historical read: %v", err)
	}
}

// TestExternalAgentReplayAndConcurrencyClaimBeforeHead proves one idempotency key never consults or snapshots a later head.
func TestExternalAgentReplayAndConcurrencyClaimBeforeHead(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		name := "redelivery"
		if concurrent {
			name = "concurrent"
		}
		t.Run(name, func(t *testing.T) {
			identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
			registry, err := NewLocalRegistry(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			resolver := &rotatingExternalConfiguration{versions: []uint64{1, 2}}
			tracker := &externalAdmissionTracker{}
			service, err := NewService(ServiceDependencies{Identity: identity, Catalog: fixedCatalog{allowed: map[string]map[string]bool{"alice": {"environment-1": true}}}, Registry: registry, Executor: &fakeExecutor{}, Packages: fakePackages{}, Clock: fixedClock{now: time.Unix(1000, 0).UTC()}, IDs: RandomIDs{}, Owner: "claim-owner", Retention: time.Hour, RunTimeout: time.Minute, RepositoryID: "repository", ProjectConfig: resolver, ProjectResources: externalBoundMaterializer{tracker: tracker}})
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close(context.Background())
			count := 1
			if concurrent {
				count = 12
			}
			results := make(chan RunView, count)
			errorsFound := make(chan error, count)
			var wait sync.WaitGroup
			for index := 0; index < count; index++ {
				wait.Add(1)
				go func() {
					defer wait.Done()
					view, submitErr := service.Submit(context.Background(), SubmitInput{SelectionAlias: "environment-1", IdempotencyKey: "causal-redelivery-key"})
					results <- view
					errorsFound <- submitErr
				}()
			}
			wait.Wait()
			close(results)
			close(errorsFound)
			for submitErr := range errorsFound {
				if submitErr != nil {
					t.Fatal(submitErr)
				}
			}
			canonical := ""
			for view := range results {
				if canonical == "" {
					canonical = view.RunHandle
				} else if view.RunHandle != canonical {
					t.Fatalf("redelivery diverged: %s != %s", view.RunHandle, canonical)
				}
			}
			if !concurrent {
				replay, replayErr := service.Submit(context.Background(), SubmitInput{SelectionAlias: "environment-1", IdempotencyKey: "causal-redelivery-key"})
				if replayErr != nil || replay.RunHandle != canonical {
					t.Fatalf("replay=%+v err=%v", replay, replayErr)
				}
			}
			tracker.mu.Lock()
			prepared := tracker.prepared
			tracker.mu.Unlock()
			if resolver.next != 1 || prepared != 1 {
				t.Fatalf("resolver=%d prepared=%d", resolver.next, prepared)
			}
			record, getErr := registry.Get(context.Background(), canonical)
			if getErr != nil || record.Configuration == nil || record.Configuration.Version != 1 {
				t.Fatalf("historical record=%+v err=%v", record.Configuration, getErr)
			}
		})
	}
}

// TestExternalAgentCommitFaultLeavesNeitherRunNorSnapshot proves compensation closes the prepare-to-commit fault window.
func TestExternalAgentCommitFaultLeavesNeitherRunNorSnapshot(t *testing.T) {
	registry, err := NewLocalRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolver := &rotatingExternalConfiguration{versions: []uint64{1}}
	tracker := &externalAdmissionTracker{}
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	service, err := NewService(ServiceDependencies{Identity: identity, Catalog: fixedCatalog{allowed: map[string]map[string]bool{"alice": {"environment-1": true}}}, Registry: claimedReserveFailure{LocalRegistry: registry}, Executor: &fakeExecutor{}, Packages: fakePackages{}, Clock: fixedClock{now: time.Unix(1000, 0).UTC()}, IDs: RandomIDs{}, Owner: "fault-owner", Retention: time.Hour, RunTimeout: time.Minute, RepositoryID: "repository", ProjectConfig: resolver, ProjectResources: externalBoundMaterializer{tracker: tracker}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Submit(context.Background(), SubmitInput{SelectionAlias: "environment-1", IdempotencyKey: "fault-before-commit"}); err == nil {
		t.Fatal("commit fault accepted")
	}
	document, err := registry.loadLocked()
	if err != nil {
		t.Fatal(err)
	}
	tracker.mu.Lock()
	prepared := tracker.prepared
	tracker.mu.Unlock()
	if len(document.Runs) != 0 || len(document.Events) != 0 || len(document.Artifacts) != 0 || resolver.next != 1 || prepared != 1 {
		t.Fatalf("runs=%d events=%d artifacts=%d resolver=%d prepared=%d", len(document.Runs), len(document.Events), len(document.Artifacts), resolver.next, prepared)
	}
}

// TestLocalConfiguredAdmissionRecoversOnlyAtomicBoundaries proves process restart observes either no admission or the complete run, event and artifact unit.
func TestLocalConfiguredAdmissionRecoversOnlyAtomicBoundaries(t *testing.T) {
	root := t.TempDir()
	binding, err := externalBinding("repository", "atomic-internal-run", 1)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := binding.Effective.Descriptors[len(binding.Effective.Descriptors)-1]
	snapshot := ProjectResourceSnapshot{Descriptor: descriptor, Payload: []byte("atomic-snapshot")}
	registry, err := NewLocalRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	// Preparing a candidate is deliberately non-durable; a restart before commit must see neither half.
	restartedBeforeCommit, err := NewLocalRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := restartedBeforeCommit.loadLocked()
	if err != nil || len(empty.Runs) != 0 || len(empty.Events) != 0 || len(empty.Artifacts) != 0 {
		t.Fatalf("pre-commit restart runs=%d events=%d artifacts=%d err=%v", len(empty.Runs), len(empty.Events), len(empty.Artifacts), err)
	}
	record := RunRecord{Handle: "local-atomic-handle", InternalID: binding.RunID, PrincipalID: "alice", SelectionAlias: descriptor.Selection, RequestHash: digest("request"), SubmitKeyHash: digest("key"), Phase: PhaseQueued, Owner: "atomic-owner", UpdatedAt: time.Unix(1000, 0).UTC(), Configuration: &binding, RetentionNS: int64(time.Hour), RunTimeoutNS: int64(time.Minute)}
	claim, err := registry.ClaimSubmit(context.Background(), record.PrincipalID, record.SubmitKeyHash, record.RequestHash)
	if err != nil || claim.Existing != nil {
		t.Fatalf("initial claim=%+v err=%v", claim, err)
	}
	result, err := registry.ReserveConfigured(context.Background(), record, snapshot)
	claim.Release()
	if err != nil || !result.New {
		t.Fatalf("commit=%+v err=%v", result, err)
	}
	registry.ReleaseOwner(record.Handle)
	restartedAfterCommit, err := NewLocalRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := restartedAfterCommit.loadLocked()
	if err != nil || len(committed.Runs) != 1 || len(committed.Events) != 1 || len(committed.Artifacts) != 1 {
		t.Fatalf("post-commit restart runs=%d events=%d artifacts=%d err=%v", len(committed.Runs), len(committed.Events), len(committed.Artifacts), err)
	}
	replay, err := restartedAfterCommit.ClaimSubmit(context.Background(), record.PrincipalID, record.SubmitKeyHash, record.RequestHash)
	if err != nil || replay.Existing == nil || replay.Existing.Handle != record.Handle || replay.Existing.Configuration == nil || replay.Existing.Configuration.BindingDigest != binding.BindingDigest {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	replay.Release()
	payload, err := restartedAfterCommit.GetArtifact(context.Background(), descriptor)
	if err != nil || string(payload) != string(snapshot.Payload) {
		t.Fatalf("artifact=%q err=%v", payload, err)
	}
}

// TestLocalConfiguredAdmissionRejectsInvalidReplayAndArtifactState covers the fail-closed local contract around the causal unit.
func TestLocalConfiguredAdmissionRejectsInvalidReplayAndArtifactState(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	registry, err := NewLocalRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := externalBinding("repository", "negative-internal-run", 1)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := binding.Effective.Descriptors[len(binding.Effective.Descriptors)-1]
	snapshot := ProjectResourceSnapshot{Descriptor: descriptor, Payload: []byte("negative-snapshot")}
	record := RunRecord{Handle: "local-negative-handle", InternalID: binding.RunID, PrincipalID: "alice", SelectionAlias: descriptor.Selection, RequestHash: digest("negative-request"), SubmitKeyHash: digest("negative-key"), Phase: PhaseQueued, Owner: "negative-owner", UpdatedAt: time.Unix(1100, 0).UTC(), Configuration: &binding, RetentionNS: int64(time.Hour), RunTimeoutNS: int64(time.Minute)}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = registry.ClaimSubmit(cancelled, record.PrincipalID, record.SubmitKeyHash, record.RequestHash); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled claim=%v", err)
	}
	if _, err = registry.ClaimSubmit(ctx, "", "short", "short"); !errors.Is(err, ErrConflict) {
		t.Fatalf("invalid claim=%v", err)
	}
	invalidSnapshot := snapshot
	invalidSnapshot.Descriptor.ID = "outside-binding"
	if _, err = registry.ReserveConfigured(ctx, record, invalidSnapshot); err == nil {
		t.Fatal("outside-binding artifact admitted")
	}
	claim, err := registry.ClaimSubmit(ctx, record.PrincipalID, record.SubmitKeyHash, record.RequestHash)
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.ReserveConfigured(ctx, record, snapshot)
	claim.Release()
	if err != nil || !result.New {
		t.Fatalf("reserve=%+v err=%v", result, err)
	}
	registry.ReleaseOwner(record.Handle)
	conflict, err := registry.ClaimSubmit(ctx, record.PrincipalID, record.SubmitKeyHash, digest("different-request"))
	if conflict != nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("divergent claim=%+v err=%v", conflict, err)
	}
	replay, err := registry.ReserveConfigured(ctx, record, snapshot)
	if err != nil || replay.New || replay.Record.Handle != record.Handle {
		t.Fatalf("direct replay=%+v err=%v", replay, err)
	}
	exact := record
	exact.Handle = "local-negative-exact"
	exact.InternalID = "local-negative-exact-internal"
	exact.SubmitKeyHash = digest("negative-exact-key")
	exact.RequestHash = digest("negative-exact-request")
	exactBinding := binding.Clone()
	exactBinding.RunID = exact.InternalID
	exactBinding.BindingDigest = ""
	exactBinding, err = projectconfig.SealRunBinding(exactBinding)
	if err != nil {
		t.Fatal(err)
	}
	exact.Configuration = &exactBinding
	if result, reserveErr := registry.ReserveConfigured(ctx, exact, snapshot); reserveErr != nil || !result.New {
		t.Fatalf("existing artifact=%+v err=%v", result, reserveErr)
	}
	registry.ReleaseOwner(exact.Handle)
	divergent := exact
	divergent.Handle = "local-negative-divergent"
	divergent.InternalID = "local-negative-divergent-internal"
	divergent.SubmitKeyHash = digest("negative-divergent-key")
	divergent.RequestHash = digest("negative-divergent-request")
	divergentBinding := binding.Clone()
	divergentBinding.RunID = divergent.InternalID
	divergentBinding.BindingDigest = ""
	divergentBinding, err = projectconfig.SealRunBinding(divergentBinding)
	if err != nil {
		t.Fatal(err)
	}
	divergent.Configuration = &divergentBinding
	if _, err = registry.ReserveConfigured(ctx, divergent, ProjectResourceSnapshot{Descriptor: descriptor, Payload: []byte("divergent-snapshot")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("divergent artifact=%v", err)
	}
	if _, err = registry.GetArtifact(cancelled, descriptor); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled artifact=%v", err)
	}
	missing := descriptor
	missing.Version++
	if _, err = registry.GetArtifact(ctx, missing); !projectconfig.IsCategory(err, projectconfig.CategoryMissingReference) {
		t.Fatalf("missing artifact=%v", err)
	}
	document, err := registry.loadLocked()
	if err != nil {
		t.Fatal(err)
	}
	for key, artifact := range document.Artifacts {
		artifact.Payload = []byte("corrupt")
		document.Artifacts[key] = artifact
	}
	if err = registry.saveLocked(document); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.GetArtifact(ctx, descriptor); !projectconfig.IsCategory(err, projectconfig.CategoryIntegrity) {
		t.Fatalf("corrupt artifact=%v", err)
	}
}

// TestExternalAgentProjectBindingFailuresStayClosed covers incomplete composition, corrupt bindings and unavailable historical resources.
func TestExternalAgentProjectBindingFailuresStayClosed(t *testing.T) {
	binding, err := externalBinding("repository", "placeholder", 1)
	if err != nil {
		t.Fatal(err)
	}
	identity := &fixedIdentity{principal: Principal{ID: "alice", Scopes: allScopes()}}
	catalog := fixedCatalog{allowed: map[string]map[string]bool{"alice": {"environment-1": true}}}
	validResources := ProjectBoundResources{Catalog: catalog, Executor: &fakeExecutor{result: ExecutionResult{Status: OutcomePassed}}, Retention: time.Hour, RunTimeout: time.Minute}
	for name, configure := range map[string]func(*Service){
		"partial composition": func(service *Service) {
			service.deps.RepositoryID = "repository"
			service.deps.ProjectConfig = controlledExternalResolver{binding: binding}
		},
		"binding unavailable": func(service *Service) {
			service.deps.RepositoryID = "repository"
			service.deps.ProjectConfig = controlledExternalResolver{err: errors.New("unavailable")}
			service.deps.ProjectResources = &controlledExternalMaterializer{resources: validResources}
		},
		"materializer unavailable": func(service *Service) {
			service.deps.RepositoryID = "repository"
			service.deps.ProjectConfig = controlledExternalResolver{binding: binding}
			service.deps.ProjectResources = &controlledExternalMaterializer{err: errors.New("unavailable")}
		},
		"corrupt binding": func(service *Service) {
			service.deps.RepositoryID = "repository"
			service.deps.ProjectConfig = controlledExternalResolver{raw: true}
			service.deps.ProjectResources = &controlledExternalMaterializer{resources: validResources}
		},
		"partial resources": func(service *Service) {
			service.deps.RepositoryID = "repository"
			service.deps.ProjectConfig = controlledExternalResolver{binding: binding}
			service.deps.ProjectResources = &controlledExternalMaterializer{resources: ProjectBoundResources{Catalog: catalog, Retention: time.Hour, RunTimeout: time.Minute}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			service, _ := testService(t, identity, &fakeExecutor{}, fakePackages{})
			configure(service)
			if _, submitErr := service.Submit(context.Background(), SubmitInput{SelectionAlias: "environment-1", IdempotencyKey: "binding-failure-key"}); submitErr == nil {
				t.Fatal("binding failure accepted")
			}
		})
	}
	service, registry := testService(t, identity, &fakeExecutor{}, fakePackages{})
	materializer := &controlledExternalMaterializer{resources: validResources}
	service.deps.RepositoryID = "repository"
	service.deps.ProjectConfig = controlledExternalResolver{binding: binding}
	service.deps.ProjectResources = materializer
	view, err := service.Submit(context.Background(), SubmitInput{SelectionAlias: "environment-1", IdempotencyKey: "historical-binding-key"})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Wait(context.Background(), view.RunHandle); err != nil {
		t.Fatal(err)
	}
	if record, getErr := registry.Get(context.Background(), view.RunHandle); getErr != nil || record.Configuration == nil {
		t.Fatalf("binding not persisted: %+v %v", record.Configuration, getErr)
	}
	materializer.err = errors.New("historical artifact unavailable")
	if _, err = service.Status(context.Background(), HandleInput{RunHandle: view.RunHandle}); err == nil {
		t.Fatal("historical read accepted unavailable resources")
	}
}

// externalBinding builds a valid immutable head whose environment selector changes with its version.
func externalBinding(repositoryID, runID string, version uint64) (projectconfig.RunConfigurationBinding, error) {
	key, _ := projectconfig.NewProjectKey("organization", "project", repositoryID)
	digest := strings.Repeat(string(rune('a'+version-1)), 64)
	alias := "environment-" + string(rune('0'+version))
	references := []projectconfig.ResourceReference{{Kind: projectconfig.ResourceProvider, ID: "provider", Version: version, Digest: digest}, {Kind: projectconfig.ResourceModelProfile, ID: "model", Version: version, Digest: digest}, {Kind: projectconfig.ResourceRuntimeProfile, ID: "runtime", Version: version, Digest: digest}, {Kind: projectconfig.ResourceContract, ID: "contract", Version: version, Digest: digest}, {Kind: projectconfig.ResourceEnvironment, ID: "environment", Version: version, Digest: digest}}
	document, err := projectconfig.Canonicalize(projectconfig.Document{Schema: projectconfig.SchemaV1, OrganizationID: key.OrganizationID, ProjectID: key.ProjectID, RepositoryID: key.RepositoryID, References: references, Constraints: projectconfig.Constraints{Limits: projectconfig.Limits{DurationSeconds: 30}, Approval: "optional", RetentionDays: 7}})
	if err != nil {
		return projectconfig.RunConfigurationBinding{}, err
	}
	descriptors := make([]projectconfig.ResourceDescriptor, len(references))
	for index, reference := range references {
		descriptors[index] = projectconfig.ResourceDescriptor{Kind: reference.Kind, ID: reference.ID, Version: version, Digest: digest, ProjectKey: key, Selection: reference.ID}
	}
	descriptors[len(descriptors)-1].Selection = alias
	effective, err := projectconfig.ValidateAndMerge(document, projectconfig.OrganizationPolicySnapshot{Version: version, Digest: digest, MaximumLimits: projectconfig.Limits{DurationSeconds: 100, ToolCalls: 100, ModelTokens: 1000, Replans: 100}, MinimumRetentionDays: 1, MaximumRetentionDays: 30}, projectconfig.ResourceCatalogSnapshot{ProjectKey: key, Digest: digest, Descriptors: descriptors, Limits: projectconfig.CatalogLimits{MaxDescriptors: 16, MaxOperationsPerDescriptor: 16, MaxCapabilitiesPerDescriptor: 16, MaxDependenciesPerDescriptor: 16, MaxDependencyEdges: 64, MaxGraphWork: 80}})
	if err != nil {
		return projectconfig.RunConfigurationBinding{}, err
	}
	return projectconfig.SealRunBinding(projectconfig.RunConfigurationBinding{RunID: runID, ProjectKey: key, Version: version, VersionDigest: document.Digest, ActivationGeneration: version, ActivationDigest: digest, PolicyVersion: effective.PolicyVersion, PolicyDigest: effective.PolicyDigest, CatalogDigest: effective.CatalogDigest, EffectiveDigest: effective.Digest, Effective: effective})
}
