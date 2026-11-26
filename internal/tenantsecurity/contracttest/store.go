// Package contracttest defines behavior shared by local and PostgreSQL tenant-security stores.
package contracttest

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/tenantsecurity"
)

// Factory returns a new adapter handle over one isolated durable backend.
type Factory func() (tenantsecurity.DurableStore, error)

// Run exercises tenant isolation, CAS, idempotency, restart and cancellation.
func Run(ctx context.Context, factory Factory, suffix string) error {
	store, err := factory()
	if err != nil {
		return err
	}
	first := tenantsecurity.NewMemoryStore().Snapshot()
	addTenant(&first, "org-a-"+suffix, "shared-project", "principal")
	first.Epoch = 1
	command := tenantsecurity.StoreCommand{OrganizationID: "org-a-" + suffix, OperationID: "bootstrap-a", CommandDigest: strings.Repeat("a", 64), ExpectedEpoch: 0, Snapshot: first}
	receipt, err := store.Commit(ctx, command)
	if err != nil || receipt.Epoch != 1 {
		return fmt.Errorf("first commit: %w", err)
	}
	store, err = factory()
	if err != nil {
		return err
	}
	replayed, err := store.Commit(ctx, command)
	if err != nil || replayed != receipt {
		return fmt.Errorf("exact replay: %w", err)
	}
	divergent := command
	divergent.CommandDigest = strings.Repeat("b", 64)
	if _, err = store.Commit(ctx, divergent); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryConflict) {
		return fmt.Errorf("divergent replay: %w", err)
	}
	second, err := store.Snapshot(ctx)
	if err != nil {
		return err
	}
	addTenant(&second, "org-b-"+suffix, "shared-project", "principal")
	second.Epoch = 2
	secondCommand := tenantsecurity.StoreCommand{OrganizationID: "org-b-" + suffix, OperationID: "bootstrap-b", CommandDigest: strings.Repeat("c", 64), ExpectedEpoch: 1, Snapshot: second}
	if _, err = store.Commit(ctx, secondCommand); err != nil {
		return fmt.Errorf("second tenant same IDs: %w", err)
	}
	loaded, err := store.Snapshot(ctx)
	if err != nil || len(loaded.Projects) != 2 || loaded.Projects["org-a-"+suffix+"/shared-project"].OrganizationID == loaded.Projects["org-b-"+suffix+"/shared-project"].OrganizationID {
		return fmt.Errorf("cross-tenant same-id isolation: %w", err)
	}
	stale := secondCommand
	stale.OperationID = "stale"
	stale.CommandDigest = strings.Repeat("d", 64)
	if _, err = store.Commit(ctx, stale); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryStale) {
		return fmt.Errorf("stale CAS: %w", err)
	}
	if resolved, receiptErr := store.Receipt(ctx, command.OrganizationID, command.OperationID); receiptErr != nil || resolved != receipt {
		return fmt.Errorf("receipt reload: %w", receiptErr)
	}
	rewritten, err := store.Snapshot(ctx)
	if err != nil {
		return err
	}
	delete(rewritten.Receipts, command.OrganizationID+"/"+command.OperationID)
	rewritten.Epoch++
	if _, err = store.Commit(ctx, tenantsecurity.StoreCommand{OrganizationID: secondCommand.OrganizationID, OperationID: "rewrite-receipt", CommandDigest: strings.Repeat("e", 64), ExpectedEpoch: 2, Snapshot: rewritten}); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryIntegrity) {
		return fmt.Errorf("receipt rewrite: %w", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = store.Snapshot(cancelled); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryCancelled) {
		return fmt.Errorf("cancelled snapshot: %w", err)
	}
	overflow := command
	overflow.OperationID = "overflow"
	overflow.ExpectedEpoch = tenantsecurity.MaxDurableCounter
	overflow.Snapshot.Epoch = 0
	if _, err = store.Commit(ctx, overflow); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryExhausted) {
		return fmt.Errorf("counter overflow: %w", err)
	}
	return nil
}

// addTenant installs one valid isolated relationship graph into a candidate snapshot.
func addTenant(snapshot *tenantsecurity.Snapshot, organizationID, projectID, principalID string) {
	policyDigest := strings.Repeat("1", 64)
	subjectSum := sha256.Sum256([]byte(organizationID))
	subjectDigest := fmt.Sprintf("%x", subjectSum)
	organization := tenantsecurity.Organization{ID: organizationID, State: tenantsecurity.StateActive, Version: 1, SecurityRevision: 1, PolicyDigest: policyDigest}
	project := tenantsecurity.ProjectRegistration{OrganizationID: organizationID, ProjectID: projectID, RepositoryID: "repository", State: tenantsecurity.StateActive, Version: 1}
	principal := tenantsecurity.Principal{ID: principalID, Kind: tenantsecurity.PrincipalHuman, SubjectDigest: subjectDigest, State: tenantsecurity.StateActive, Version: 1}
	role, _ := tenantsecurity.CompileRole(tenantsecurity.Role{OrganizationID: organizationID, ID: "reader", Actions: []tenantsecurity.Action{tenantsecurity.ActionRunRead}, Resources: []tenantsecurity.ResourceClass{tenantsecurity.ResourceRun}, ProjectIDs: []string{projectID}, Version: 1}, tenantsecurity.DefaultLimits())
	snapshot.Organizations[organizationID] = organization
	snapshot.Projects[organizationID+"/"+projectID] = project
	snapshot.Principals[organizationID+"/"+principalID] = principal
	snapshot.Subjects[organizationID+"/"+subjectDigest] = principalID
	snapshot.Memberships[organizationID+"/"+principalID] = tenantsecurity.Membership{OrganizationID: organizationID, PrincipalID: principalID, RoleIDs: []string{role.ID}, ProjectIDs: []string{projectID}, State: tenantsecurity.StateActive, ValidFrom: time.Unix(1, 0).UTC(), Version: 1}
	snapshot.Roles[organizationID+"/"+role.ID] = role
}
