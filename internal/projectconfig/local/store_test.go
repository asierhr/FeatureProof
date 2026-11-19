package local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	pc "github.com/asierhr/featureProof/internal/projectconfig"
	pccontract "github.com/asierhr/featureProof/internal/projectconfig/contracttest"
)

// digestA is a syntactically valid deterministic contract fixture identity.
const digestA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// localCatalogLimits supplies explicit finite budgets for durable-store fixtures.
func localCatalogLimits() pc.CatalogLimits {
	return pc.CatalogLimits{MaxDescriptors: 16, MaxOperationsPerDescriptor: 16, MaxCapabilitiesPerDescriptor: 16, MaxDependenciesPerDescriptor: 16, MaxDependencyEdges: 64, MaxGraphWork: 80}
}

// publishCommand creates canonical store input without external dependencies.
func publishCommand(t *testing.T, operation string, generation uint64) pc.PublishCommand {
	t.Helper()
	document := pc.Document{Schema: pc.SchemaV1, ProjectID: "project", RepositoryID: "repo", References: []pc.ResourceReference{{Kind: pc.ResourceContract, ID: "contract", Version: 1, Digest: digestA}}, Constraints: pc.Constraints{Limits: pc.Limits{DurationSeconds: 1}, Approval: "required", RetentionDays: 1}}
	canonical, err := pc.Canonicalize(document)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := pc.NewProjectKey("project", "repo")
	effective, err := pc.ValidateAndMerge(canonical, pc.OrganizationPolicySnapshot{Version: 1, Digest: digestA}, pc.ResourceCatalogSnapshot{Limits: localCatalogLimits(), ProjectKey: key, Digest: digestA, Descriptors: []pc.ResourceDescriptor{{Kind: pc.ResourceContract, ID: "contract", Version: 1, Digest: digestA, ProjectKey: key}}})
	if err != nil {
		t.Fatal(err)
	}
	return pc.PublishCommand{OperationID: operation, CommandDigest: digestA, Actor: "actor", ExpectedGeneration: generation, Document: canonical, Effective: effective, CreatedAt: time.Unix(100, 0).UTC()}
}

// TestStoreSecondProcess proves the filesystem lock serializes competing publishers.
func TestStoreSecondProcess(t *testing.T) {
	root := t.TempDir()
	var wait sync.WaitGroup
	results := make(chan string, 2)
	for _, operation := range []string{"process-a", "process-b"} {
		wait.Add(1)
		go func(operation string) {
			defer wait.Done()
			command := exec.Command(os.Args[0], "-test.run=^TestStoreProcessHelper$")
			command.Env = append(os.Environ(), "FEATUREPROOF_PROJECTCONFIG_CHILD_ROOT="+root, "FEATUREPROOF_PROJECTCONFIG_CHILD_OPERATION="+operation)
			output, err := command.CombinedOutput()
			results <- fmt.Sprintf("%s:%s", err, strings.TrimSpace(string(output)))
		}(operation)
	}
	wait.Wait()
	close(results)
	var published, stale int
	for result := range results {
		if strings.Contains(result, "published") {
			published++
		}
		if strings.Contains(result, "stale_head") {
			stale++
		}
	}
	if published != 1 || stale != 1 {
		t.Fatalf("unexpected process results: published=%d stale=%d", published, stale)
	}
}

// TestStoreProcessHelper publishes once when invoked by the cross-process contract.
func TestStoreProcessHelper(t *testing.T) {
	root := os.Getenv("FEATUREPROOF_PROJECTCONFIG_CHILD_ROOT")
	if root == "" {
		return
	}
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Publish(context.Background(), publishCommand(t, os.Getenv("FEATUREPROOF_PROJECTCONFIG_CHILD_OPERATION"), 0))
	if err == nil {
		fmt.Print("published")
		return
	}
	if pc.IsCategory(err, pc.CategoryStaleHead) {
		fmt.Print("stale_head")
		return
	}
	t.Fatal(err)
}

// TestSharedStoreContract runs the same adapter-neutral suite used by PostgreSQL.
func TestSharedStoreContract(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := pccontract.Run(context.Background(), func() (pc.Store, error) { return New(root) }, "local"); err != nil {
		t.Fatal(err)
	}
}

// TestSharedResourceArtifactContract applies the same project-scoped immutable contract as PostgreSQL.
func TestSharedResourceArtifactContract(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := pccontract.RunResourceArtifacts(context.Background(), func() (pc.ResourceArtifactStore, error) {
		return pc.NewLocalResourceArtifactStore(root)
	}, "local"); err != nil {
		t.Fatal(err)
	}
}

// TestSharedStoreContractFailsClosed verifies setup, restart and cancellation errors are
// propagated by the adapter-neutral contract instead of being mistaken for success.
func TestSharedStoreContractFailsClosed(t *testing.T) {
	t.Parallel()
	factoryFailure := errors.New("factory unavailable")
	if err := pccontract.Run(context.Background(), func() (pc.Store, error) { return nil, factoryFailure }, "factory"); !errors.Is(err, factoryFailure) {
		t.Fatalf("expected initial factory failure, got %v", err)
	}
	if err := pccontract.Run(context.Background(), func() (pc.Store, error) { return New(t.TempDir()) }, "invalid/suffix"); !pc.IsCategory(err, pc.CategoryInvalid) {
		t.Fatalf("expected invalid project identity, got %v", err)
	}
	restartCalls := 0
	if err := pccontract.Run(context.Background(), func() (pc.Store, error) {
		restartCalls++
		if restartCalls == 2 {
			return nil, factoryFailure
		}
		return New(t.TempDir())
	}, "restart-failure"); !errors.Is(err, factoryFailure) {
		t.Fatalf("expected restart factory failure, got %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pccontract.Run(cancelled, func() (pc.Store, error) { return New(t.TempDir()) }, "cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancelled publish, got %v", err)
	}
}

// contractFaultStore injects one deterministic adapter fault at a named shared-contract boundary.
type contractFaultStore struct {
	pc.Store
	mu        sync.Mutex
	fault     string
	publishes int
	rollbacks int
	bindings  int
}

// next increments a method-local call count without introducing races in competing-publish cases.
func (store *contractFaultStore) next(counter *int) int {
	store.mu.Lock()
	defer store.mu.Unlock()
	*counter++
	return *counter
}

// Publish delegates unless the selected scenario targets the current publish checkpoint.
func (store *contractFaultStore) Publish(ctx context.Context, command pc.PublishCommand) (pc.PublishReceipt, error) {
	call := store.next(&store.publishes)
	if store.fault == "idempotency" && call == 2 || store.fault == "cas" && call == 3 || store.fault == "second publish" && call == 4 || store.fault == "competing error" && call >= 5 {
		return pc.PublishReceipt{}, errors.New("injected publish failure")
	}
	if store.fault == "competing convergence" && call >= 5 {
		return pc.PublishReceipt{Version: 2, Generation: 4}, nil
	}
	receipt, err := store.Store.Publish(ctx, command)
	if store.fault == "receipt" && call == 1 && err == nil {
		receipt.Version++
	}
	return receipt, err
}

// Rollback delegates unless the selected scenario targets one rollback checkpoint.
func (store *contractFaultStore) Rollback(ctx context.Context, command pc.RollbackCommand) (pc.PublishReceipt, error) {
	call := store.next(&store.rollbacks)
	if store.fault == "rollback" && call == 1 || store.fault == "rollback replay" && call == 2 || store.fault == "rollback conflict" && call == 3 {
		return pc.PublishReceipt{}, errors.New("injected rollback failure")
	}
	receipt, err := store.Store.Rollback(ctx, command)
	if store.fault == "rollback receipt" && call == 1 && err == nil {
		receipt.Generation++
	}
	return receipt, err
}

// Bind delegates and can corrupt only the returned projection to test contract detection.
func (store *contractFaultStore) Bind(ctx context.Context, command pc.BindCommand) (pc.RunConfigurationBinding, error) {
	if store.fault == "bind" {
		return pc.RunConfigurationBinding{}, errors.New("injected bind failure")
	}
	binding, err := store.Store.Bind(ctx, command)
	if store.fault == "binding projection" && err == nil {
		binding.Version++
	}
	return binding, err
}

// Binding delegates unless the selected scenario targets the first or historical reload.
func (store *contractFaultStore) Binding(ctx context.Context, key pc.ProjectKey, runID string) (pc.RunConfigurationBinding, error) {
	call := store.next(&store.bindings)
	if store.fault == "binding reload" && call == 1 || store.fault == "stable binding" && call == 2 {
		return pc.RunConfigurationBinding{}, errors.New("injected binding failure")
	}
	return store.Store.Binding(ctx, key, runID)
}

// History delegates unless the shared contract must prove query failures remain visible.
func (store *contractFaultStore) History(ctx context.Context, key pc.ProjectKey, after uint64, limit int) (pc.HistoryPage, error) {
	if store.fault == "history" {
		return pc.HistoryPage{}, errors.New("injected history failure")
	}
	return store.Store.History(ctx, key, after, limit)
}

// TestSharedStoreContractDetectsAdapterDrift proves every durable checkpoint fails closed.
func TestSharedStoreContractDetectsAdapterDrift(t *testing.T) {
	for _, fault := range []string{"receipt", "idempotency", "cas", "bind", "binding projection", "binding reload", "rollback", "rollback receipt", "rollback replay", "rollback conflict", "second publish", "stable binding", "history", "competing error", "competing convergence"} {
		t.Run(fault, func(t *testing.T) {
			underlying, err := New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			store := &contractFaultStore{Store: underlying, fault: fault}
			if runErr := pccontract.Run(context.Background(), func() (pc.Store, error) { return store, nil }, "fault-"+strings.ReplaceAll(fault, " ", "-")); runErr == nil {
				t.Fatalf("shared contract accepted %s drift", fault)
			}
		})
	}
}

// TestStoreContract covers restart, CAS, idempotency, copies and immutable binding.
func TestStoreContract(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	receipt, err := store.Publish(ctx, publishCommand(t, "op-1", 0))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Version != 1 || receipt.Generation != 1 {
		t.Fatalf("unexpected receipt %+v", receipt)
	}
	key, _ := pc.NewProjectKey("project", "repo")
	head, err := store.Head(ctx, key)
	if err != nil || head.Activation.Digest != receipt.HeadDigest {
		t.Fatalf("head: %v", err)
	}
	version, effective, err := store.Version(ctx, key, 1)
	if err != nil || version.Version != 1 || effective.Digest == "" {
		t.Fatalf("version: %v", err)
	}
	activationEffective, err := store.Effective(ctx, key, 1)
	if err != nil || activationEffective.Digest != effective.Digest {
		t.Fatalf("effective: %v", err)
	}
	replayed, err := store.Publish(ctx, publishCommand(t, "op-1", 0))
	if err != nil || replayed != receipt {
		t.Fatalf("idempotency: %+v %v", replayed, err)
	}
	if _, err := store.Publish(ctx, publishCommand(t, "op-2", 0)); !pc.IsCategory(err, pc.CategoryStaleHead) {
		t.Fatalf("expected stale head, got %v", err)
	}
	binding, err := store.Bind(ctx, pc.BindCommand{RunID: "run-1", ProjectKey: key})
	if err != nil {
		t.Fatal(err)
	}
	if binding.Version != 1 || binding.ActivationGeneration != 1 {
		t.Fatalf("unexpected binding %+v", binding)
	}
	expectedEffective := string(binding.Effective.Bytes)
	binding.Effective.Bytes[0] = 'x'
	restarted, _ := New(root)
	reloaded, err := restarted.Binding(ctx, key, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(reloaded.Effective.Bytes) != expectedEffective {
		t.Fatal("binding was not defensively copied")
	}
	if _, err := restarted.Publish(ctx, publishCommand(t, "op-3", 1)); err != nil {
		t.Fatal(err)
	}
	again, err := restarted.Bind(ctx, pc.BindCommand{RunID: "run-1", ProjectKey: key})
	if err != nil || again.Version != 1 {
		t.Fatalf("existing run followed head: %+v %v", again, err)
	}
}

// TestStoreRejectsCorruption proves durable integrity failures never become empty state.
func TestStoreRejectsCorruption(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, _ := New(root)
	if _, err := store.Publish(context.Background(), publishCommand(t, "op-1", 0)); err != nil {
		t.Fatal(err)
	}
	key, _ := pc.NewProjectKey("project", "repo")
	if err := overwriteForTest(store.dataPath(key), []byte(`{"payload":{},"checksum":"bad"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Head(context.Background(), key); !pc.IsCategory(err, pc.CategoryIntegrity) {
		t.Fatalf("got %v", err)
	}
}

// TestStoreCancellationAndBoundaries covers lock cancellation and closed query limits.
func TestStoreCancellationAndBoundaries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, _ := New(root)
	key, _ := pc.NewProjectKey("project", "repo")
	unlock, err := acquireLock(context.Background(), store.lockPath(key))
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = store.Publish(cancelled, publishCommand(t, "cancelled", 0)); !pc.IsCategory(err, pc.CategoryCancelled) {
		unlock()
		t.Fatalf("cancel got %v", err)
	}
	deadline, deadlineCancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer deadlineCancel()
	if _, err = store.Publish(deadline, publishCommand(t, "deadline", 0)); !pc.IsCategory(err, pc.CategoryDeadline) {
		unlock()
		t.Fatalf("deadline got %v", err)
	}
	unlock()
	if _, err = store.History(context.Background(), key, 0, 0); !pc.IsCategory(err, pc.CategoryInvalid) {
		t.Fatalf("history limit got %v", err)
	}
	if _, err = store.Binding(context.Background(), key, "missing"); !pc.IsCategory(err, pc.CategoryNotFound) {
		t.Fatalf("binding got %v", err)
	}
	if _, err = New("relative"); err == nil {
		t.Fatal("relative root accepted")
	}
}

// TestStoreRejectsOverflowAndDivergentBinding proves counters and run identity fail closed.
func TestStoreRejectsOverflowAndDivergentBinding(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"version", "generation", "sequence"} {
		t.Run("publish_"+field, func(t *testing.T) {
			store, _ := New(t.TempDir())
			key, _ := pc.NewProjectKey("project", "repo")
			current := newState()
			switch field {
			case "version":
				current.NextVersion = pc.MaxDurableCounter
			case "generation":
				current.NextGeneration = pc.MaxDurableCounter
			case "sequence":
				current.NextSequence = pc.MaxDurableCounter
			}
			if err := store.save(key, current); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Publish(context.Background(), publishCommand(t, "overflow", generation(&current))); !pc.IsCategory(err, pc.CategoryExhausted) {
				t.Fatalf("overflow got %v", err)
			}
		})
	}
	for _, field := range []string{"generation", "sequence"} {
		t.Run("rollback_"+field, func(t *testing.T) {
			store, _ := New(t.TempDir())
			key, _ := pc.NewProjectKey("project", "repo")
			published := publishCommand(t, "seed", 0)
			if _, err := store.Publish(context.Background(), published); err != nil {
				t.Fatal(err)
			}
			current, err := store.load(key)
			if err != nil {
				t.Fatal(err)
			}
			if field == "generation" {
				current.NextGeneration = pc.MaxDurableCounter
			} else {
				current.NextSequence = pc.MaxDurableCounter
			}
			if err = store.save(key, current); err != nil {
				t.Fatal(err)
			}
			rollback := pc.RollbackCommand{OperationID: "rollback", CommandDigest: digestA, Actor: "actor", ProjectKey: key, Version: 1, ExpectedGeneration: generation(&current), Effective: published.Effective, CreatedAt: time.Unix(2, 0)}
			if _, err = store.Rollback(context.Background(), rollback); !pc.IsCategory(err, pc.CategoryExhausted) {
				t.Fatalf("overflow got %v", err)
			}
		})
	}
	store, _ := New(t.TempDir())
	key, _ := pc.NewProjectKey("project", "repo")
	store, _ = New(t.TempDir())
	if _, err := store.Publish(context.Background(), publishCommand(t, "publish", 0)); err != nil {
		t.Fatal(err)
	}
	first, err := store.Bind(context.Background(), pc.BindCommand{RunID: "run", ProjectKey: key})
	if err != nil {
		t.Fatal(err)
	}
	candidate := first.Clone()
	candidate.ActivationDigest = strings.Repeat("b", 64)
	candidate, err = pc.SealRunBinding(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Bind(context.Background(), pc.BindCommand{RunID: "run", ProjectKey: key, Candidate: &pc.AdmissionCandidate{Binding: candidate}}); !pc.IsCategory(err, pc.CategoryConflict) {
		t.Fatalf("binding conflict got %v", err)
	}
}

// overwriteForTest corrupts only the test-owned temporary store.
func overwriteForTest(path string, value []byte) error { return writeTestFile(path, value) }
