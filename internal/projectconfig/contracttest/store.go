// Package contracttest defines storage behavior shared by local and PostgreSQL adapters.
package contracttest

import (
	"context"
	"fmt"
	"sync"
	"time"

	pc "github.com/asierhr/featureProof/internal/projectconfig"
)

// fixtureDigest is a deterministic valid digest used across adapter suites.
const fixtureDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// fixtureCatalogLimits supplies adapter-neutral finite catalog budgets.
func fixtureCatalogLimits() pc.CatalogLimits {
	return pc.CatalogLimits{MaxDescriptors: 16, MaxOperationsPerDescriptor: 16, MaxCapabilitiesPerDescriptor: 16, MaxDependenciesPerDescriptor: 16, MaxDependencyEdges: 64, MaxGraphWork: 80}
}

// Factory returns a clean adapter instance for one isolated project contract run.
type Factory func() (pc.Store, error)

// ResourceArtifactFactory returns an adapter instance sharing one isolated durable backend across calls.
type ResourceArtifactFactory func() (pc.ResourceArtifactStore, error)

// RunResourceArtifacts verifies project isolation, validation and immutable redelivery for every durable adapter.
func RunResourceArtifacts(ctx context.Context, factory ResourceArtifactFactory, suffix string) error {
	store, err := factory()
	if err != nil {
		return err
	}
	firstKey, err := pc.NewProjectKey("artifact-"+suffix, "repository-a")
	if err != nil {
		return err
	}
	secondKey, err := pc.NewProjectKey("artifact-"+suffix, "repository-b")
	if err != nil {
		return err
	}
	first := pc.ResourceDescriptor{Kind: pc.ResourceContract, ID: "contract", Version: 1, Digest: fixtureDigest, ProjectKey: firstKey}
	second := first
	second.ProjectKey = secondKey
	if err = store.Put(ctx, first, []byte("project-a")); err != nil {
		return fmt.Errorf("first project put: %w", err)
	}
	if err = store.Put(ctx, second, []byte("project-b")); err != nil {
		return fmt.Errorf("second project put: %w", err)
	}
	store, err = factory()
	if err != nil {
		return err
	}
	for name, fixture := range map[string]struct {
		descriptor pc.ResourceDescriptor
		expected   string
	}{
		"first":  {descriptor: first, expected: "project-a"},
		"second": {descriptor: second, expected: "project-b"},
	} {
		payload, getErr := store.Get(ctx, fixture.descriptor)
		if getErr != nil || string(payload) != fixture.expected {
			return fmt.Errorf("%s project isolation: payload=%q err=%w", name, payload, getErr)
		}
	}
	if err = store.Put(ctx, first, []byte("project-a")); err != nil {
		return fmt.Errorf("exact redelivery: %w", err)
	}
	if err = store.Put(ctx, first, []byte("divergent")); !pc.IsCategory(err, pc.CategoryIntegrity) {
		return fmt.Errorf("divergent redelivery: %w", err)
	}
	invalid := first
	invalid.ProjectKey = pc.ProjectKey{ProjectID: "../invalid", RepositoryID: "repository"}
	if err = store.Put(ctx, invalid, []byte("invalid")); !pc.IsCategory(err, pc.CategoryInvalid) {
		return fmt.Errorf("invalid project put: %w", err)
	}
	if _, err = store.Get(ctx, invalid); !pc.IsCategory(err, pc.CategoryInvalid) {
		return fmt.Errorf("invalid project get: %w", err)
	}
	return nil
}

// Run exercises atomic publish, CAS, idempotency, history and immutable binding.
func Run(ctx context.Context, factory Factory, suffix string) error {
	store, err := factory()
	if err != nil {
		return err
	}
	key, err := pc.NewProjectKey("contract-"+suffix, "repo")
	if err != nil {
		return err
	}
	document := pc.Document{Schema: pc.SchemaV1, ProjectID: key.ProjectID, RepositoryID: key.RepositoryID, References: []pc.ResourceReference{{Kind: pc.ResourceContract, ID: "contract", Version: 1, Digest: fixtureDigest}}, Constraints: pc.Constraints{Limits: pc.Limits{DurationSeconds: 1}, Approval: "required", RetentionDays: 1}}
	canonical, err := pc.Canonicalize(document)
	if err != nil {
		return err
	}
	effective, err := pc.ValidateAndMerge(canonical, pc.OrganizationPolicySnapshot{Version: 1, Digest: fixtureDigest}, pc.ResourceCatalogSnapshot{Limits: fixtureCatalogLimits(), ProjectKey: key, Digest: fixtureDigest, Descriptors: []pc.ResourceDescriptor{{Kind: pc.ResourceContract, ID: "contract", Version: 1, Digest: fixtureDigest, ProjectKey: key}}})
	if err != nil {
		return err
	}
	command := pc.PublishCommand{OperationID: "operation", CommandDigest: fixtureDigest, Actor: "actor", Document: canonical, Effective: effective, CreatedAt: time.Unix(100, 0).UTC()}
	receipt, err := store.Publish(ctx, command)
	if err != nil {
		return err
	}
	if receipt.Version != 1 || receipt.Generation != 1 {
		return fmt.Errorf("unexpected receipt")
	}
	store, err = factory()
	if err != nil {
		return err
	}
	replay, err := store.Publish(ctx, command)
	if err != nil || replay != receipt {
		return fmt.Errorf("idempotency failed: %w", err)
	}
	command.OperationID = "stale"
	if _, err = store.Publish(ctx, command); !pc.IsCategory(err, pc.CategoryStaleHead) {
		return fmt.Errorf("CAS failed: %w", err)
	}
	binding, err := store.Bind(ctx, pc.BindCommand{RunID: "run", ProjectKey: key, CreatedAt: time.Unix(101, 0).UTC()})
	if err != nil {
		return err
	}
	if binding.Version != 1 || binding.EffectiveDigest != effective.Digest {
		return fmt.Errorf("binding mismatch")
	}
	loaded, err := store.Binding(ctx, key, "run")
	if err != nil || loaded.ActivationDigest != binding.ActivationDigest {
		return fmt.Errorf("binding reload failed: %w", err)
	}
	rollback := pc.RollbackCommand{OperationID: "rollback", CommandDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Actor: "actor", ProjectKey: key, Version: 1, ExpectedGeneration: 1, Effective: effective, CreatedAt: time.Unix(102, 0).UTC()}
	rollbackReceipt, err := store.Rollback(ctx, rollback)
	if err != nil {
		return err
	}
	if rollbackReceipt.Generation != 2 || rollbackReceipt.Version != 1 {
		return fmt.Errorf("rollback mismatch")
	}
	if replay, err := store.Rollback(ctx, rollback); err != nil || replay != rollbackReceipt {
		return fmt.Errorf("rollback idempotency failed: %w", err)
	}
	rollback.CommandDigest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if _, err = store.Rollback(ctx, rollback); !pc.IsCategory(err, pc.CategoryConflict) {
		return fmt.Errorf("rollback conflict failed: %w", err)
	}
	command.OperationID = "operation-2"
	command.CommandDigest = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	command.ExpectedGeneration = 2
	if _, err = store.Publish(ctx, command); err != nil {
		return err
	}
	stable, err := store.Binding(ctx, key, "run")
	if err != nil || stable.BindingDigest != binding.BindingDigest {
		return fmt.Errorf("bound run followed head: %w", err)
	}
	page, err := store.History(ctx, key, 0, 10)
	if err != nil || len(page.Items) != 3 {
		return fmt.Errorf("history mismatch: %w", err)
	}
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			competing := command
			competing.OperationID = fmt.Sprintf("competing-%d", index)
			competing.CommandDigest = fmt.Sprintf("%064d", index+1)
			competing.ExpectedGeneration = 3
			_, publishErr := store.Publish(ctx, competing)
			results <- publishErr
		}(index)
	}
	wait.Wait()
	close(results)
	var successful, stale int
	for publishErr := range results {
		if publishErr == nil {
			successful++
		} else if pc.IsCategory(publishErr, pc.CategoryStaleHead) {
			stale++
		} else {
			return fmt.Errorf("competing publish: %w", publishErr)
		}
	}
	if successful != 1 || stale != 1 {
		return fmt.Errorf("competing publish convergence %d/%d", successful, stale)
	}
	return nil
}
