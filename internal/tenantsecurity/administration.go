package tenantsecurity

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// AdministrationKind selects one closed tenant relationship mutation.
type AdministrationKind string

const (
	// AdministrationProject creates or updates one project registration.
	AdministrationProject AdministrationKind = "project"
	// AdministrationMembership creates or updates one principal membership.
	AdministrationMembership AdministrationKind = "membership"
	// AdministrationRole creates or updates one compiled role snapshot.
	AdministrationRole AdministrationKind = "role"
	// AdministrationServiceIdentity creates or updates one accountable service principal.
	AdministrationServiceIdentity AdministrationKind = "service_identity"
)

// AdministrationMutation carries exactly one bounded administrative candidate.
type AdministrationMutation struct {
	Kind            AdministrationKind   `json:"kind"`
	Project         *ProjectRegistration `json:"project,omitempty"`
	Principal       *Principal           `json:"principal,omitempty"`
	Membership      *Membership          `json:"membership,omitempty"`
	Role            *Role                `json:"role,omitempty"`
	ServiceIdentity *ServiceIdentity     `json:"service_identity,omitempty"`
}

// AdministrationResult exposes the durable receipt and causal audit link of one mutation.
type AdministrationResult struct {
	Receipt StoreReceipt
	Audit   AuditEvent
}

// Administration applies security administration through one durable aggregate boundary.
type Administration struct {
	store  DurableStore
	clock  Clock
	limits Limits
}

// NewAdministration validates the durable authority and structural limits.
func NewAdministration(store DurableStore, clock Clock, limits Limits) (*Administration, error) {
	if store == nil || clock == nil || ValidateLimits(limits) != nil {
		return nil, failure(CategoryInvalid, "administration_new", nil)
	}
	return &Administration{store: store, clock: clock, limits: limits}, nil
}

// Apply confirms mutation, organization security revision, audit and receipt atomically.
func (service *Administration) Apply(ctx context.Context, capability Capability, operationID string, expectedVersion uint64, mutation AdministrationMutation) (AdministrationResult, error) {
	if err := ctx.Err(); err != nil {
		return AdministrationResult{}, classifyContext("administration_apply", err)
	}
	if ValidateID(operationID) != nil || expectedVersion >= MaxDurableCounter {
		return AdministrationResult{}, failure(CategoryInvalid, "administration_apply", nil)
	}
	commandDigest, err := administrationDigest(expectedVersion, mutation)
	if err != nil {
		return AdministrationResult{}, err
	}
	if receipt, receiptErr := service.store.Receipt(ctx, capability.Scope.OrganizationID, operationID); receiptErr == nil {
		if receipt.CommandDigest != commandDigest {
			return AdministrationResult{}, failure(CategoryConflict, "administration_replay", nil)
		}
		return AdministrationResult{Receipt: receipt}, nil
	} else if !IsCategory(receiptErr, CategoryNotFoundHidden) {
		return AdministrationResult{}, receiptErr
	}
	snapshot, err := service.store.Snapshot(ctx)
	if err != nil {
		return AdministrationResult{}, err
	}
	if err = verifyAdministrativeCapability(snapshot, capability, service.clock.Now()); err != nil {
		return AdministrationResult{}, err
	}
	if err = service.applyMutation(&snapshot, capability.Scope.OrganizationID, expectedVersion, mutation); err != nil {
		return AdministrationResult{}, err
	}
	organization := snapshot.Organizations[capability.Scope.OrganizationID]
	if organization.Version >= MaxDurableCounter || organization.SecurityRevision >= MaxDurableCounter || snapshot.Epoch >= MaxDurableCounter {
		return AdministrationResult{}, failure(CategoryExhausted, "administration_apply", nil)
	}
	organization.Version++
	organization.SecurityRevision++
	snapshot.Organizations[organization.ID] = organization
	event, err := appendAdministrativeAudit(&snapshot, capability, operationID, service.clock.Now())
	if err != nil {
		return AdministrationResult{}, err
	}
	snapshot.Epoch++
	receipt, err := service.store.Commit(ctx, StoreCommand{OrganizationID: organization.ID, OperationID: operationID, CommandDigest: commandDigest, ExpectedEpoch: snapshot.Epoch - 1, Snapshot: snapshot})
	if err != nil {
		if resolved, resolveErr := service.store.Receipt(ctx, organization.ID, operationID); resolveErr == nil && resolved.CommandDigest == commandDigest {
			return AdministrationResult{Receipt: resolved, Audit: event}, nil
		}
		return AdministrationResult{}, err
	}
	return AdministrationResult{Receipt: receipt, Audit: event}, nil
}

// applyMutation validates relationship ownership and optimistic version before changing the candidate snapshot.
func (service *Administration) applyMutation(snapshot *Snapshot, organizationID string, expectedVersion uint64, mutation AdministrationMutation) error {
	switch mutation.Kind {
	case AdministrationProject:
		if mutation.Project == nil || mutation.Principal != nil || mutation.Membership != nil || mutation.Role != nil || mutation.ServiceIdentity != nil {
			return failure(CategoryInvalid, "administration_project", nil)
		}
		candidate := *mutation.Project
		if candidate.OrganizationID != organizationID || ValidateID(candidate.ProjectID) != nil || ValidateID(candidate.RepositoryID) != nil || !validState(candidate.State) {
			return failure(CategoryUnauthorized, "administration_project", nil)
		}
		return replaceVersioned(snapshot.Projects, projectKey(organizationID, candidate.ProjectID), expectedVersion, candidate.Version, candidate)
	case AdministrationMembership:
		if mutation.Principal == nil || mutation.Membership == nil || mutation.Project != nil || mutation.Role != nil || mutation.ServiceIdentity != nil {
			return failure(CategoryInvalid, "administration_membership", nil)
		}
		principal, membership := *mutation.Principal, cloneMembership(*mutation.Membership)
		if membership.OrganizationID != organizationID || membership.PrincipalID != principal.ID || principal.Kind != PrincipalHuman || !validPrincipal(principal) || !validMembership(snapshot, membership, service.clock.Now(), service.limits) {
			return failure(CategoryUnauthorized, "administration_membership", nil)
		}
		key := membershipKey(organizationID, principal.ID)
		if err := replaceVersioned(snapshot.Memberships, key, expectedVersion, membership.Version, membership); err != nil {
			return err
		}
		snapshot.Principals[principalKey(organizationID, principal.ID)] = principal
		snapshot.Subjects[subjectKey(organizationID, principal.SubjectDigest)] = principal.ID
		return nil
	case AdministrationRole:
		if mutation.Role == nil || mutation.Project != nil || mutation.Principal != nil || mutation.Membership != nil || mutation.ServiceIdentity != nil {
			return failure(CategoryInvalid, "administration_role", nil)
		}
		compiled, err := CompileRole(*mutation.Role, service.limits)
		if err != nil || compiled.OrganizationID != organizationID {
			return failure(CategoryUnauthorized, "administration_role", err)
		}
		return replaceVersioned(snapshot.Roles, roleKey(organizationID, compiled.ID), expectedVersion, compiled.Version, compiled)
	case AdministrationServiceIdentity:
		if mutation.Principal == nil || mutation.ServiceIdentity == nil || mutation.Project != nil || mutation.Membership != nil || mutation.Role != nil {
			return failure(CategoryInvalid, "administration_service_identity", nil)
		}
		principal, identity := *mutation.Principal, *mutation.ServiceIdentity
		if identity.OrganizationID != organizationID || identity.PrincipalID != principal.ID || principal.Kind != PrincipalService || !validPrincipal(principal) || !validServiceIdentity(*snapshot, identity, service.clock.Now()) {
			return failure(CategoryUnauthorized, "administration_service_identity", nil)
		}
		key := principalKey(organizationID, principal.ID)
		if err := replaceVersioned(snapshot.ServiceIdentities, key, expectedVersion, identity.Version, identity); err != nil {
			return err
		}
		snapshot.Principals[key] = principal
		snapshot.Subjects[subjectKey(organizationID, principal.SubjectDigest)] = principal.ID
		return nil
	default:
		return failure(CategoryInvalid, "administration_apply", nil)
	}
}

// administrationDigest seals the mutation kind, expected version and complete payload identity.
func administrationDigest(expectedVersion uint64, mutation AdministrationMutation) (string, error) {
	payload, err := json.Marshal(mutation)
	if err != nil || len(payload) == 0 || len(payload) > MaxPayloadBytes {
		return "", failure(CategoryInvalid, "administration_digest", err)
	}
	return digestStrings(fmt.Sprint(expectedVersion), string(payload)), nil
}

// verifyAdministrativeCapability rejects stale, expired or non-admin authority before entity lookup.
func verifyAdministrativeCapability(snapshot Snapshot, capability Capability, now time.Time) error {
	organization, ok := snapshot.Organizations[capability.Scope.OrganizationID]
	if !ok || capability.Action != ActionTenantAdmin || capability.Resource != ResourceTenant || capability.Digest != digestStrings(capability.Scope.Digest, string(capability.Action), string(capability.Resource)) || capability.Scope.SecurityRevision != organization.SecurityRevision || capability.Scope.PolicyDigest != organization.PolicyDigest || !now.Before(capability.Scope.ExpiresAt) {
		return failure(CategoryUnauthorized, "administration_authorize", nil)
	}
	return nil
}

// appendAdministrativeAudit adds one allowlisted causal link to the command snapshot.
func appendAdministrativeAudit(snapshot *Snapshot, capability Capability, operationID string, at time.Time) (AuditEvent, error) {
	events := snapshot.Audits[capability.Scope.OrganizationID]
	if uint64(len(events)) >= MaxDurableCounter {
		return AuditEvent{}, failure(CategoryExhausted, "administration_audit", nil)
	}
	previous := ""
	if len(events) != 0 {
		previous = events[len(events)-1].Digest
	}
	event := AuditEvent{OrganizationID: capability.Scope.OrganizationID, ProjectID: capability.Scope.ProjectID, Sequence: uint64(len(events) + 1), ActorDigest: digestStrings(capability.Scope.PrincipalID), Action: ActionTenantAdmin, Resource: ResourceTenant, OperationID: operationID, Outcome: "committed", PolicyDigest: capability.Scope.PolicyDigest, PreviousDigest: previous, At: at.UTC()}
	event.Digest = auditDigest(event)
	snapshot.Audits[event.OrganizationID] = append(events, event)
	snapshot.AuditHeads[event.OrganizationID] = sealAuditHead(event.OrganizationID, event.Sequence, event.Digest)
	return event, nil
}

// replaceVersioned applies create/update CAS to one organization-scoped map.
func replaceVersioned[T any](values map[string]T, key string, expectedVersion, candidateVersion uint64, candidate T) error {
	_, exists := values[key]
	if expectedVersion == 0 && exists || expectedVersion != 0 && !exists || candidateVersion != expectedVersion+1 {
		return failure(CategoryStale, "administration_version", nil)
	}
	if exists {
		currentPayload, _ := json.Marshal(values[key])
		var current struct {
			Version uint64 `json:"version"`
		}
		if json.Unmarshal(currentPayload, &current) != nil || current.Version != expectedVersion {
			return failure(CategoryStale, "administration_version", nil)
		}
	}
	values[key] = candidate
	return nil
}

// validState recognizes only lifecycle states admitted by tenant administration.
func validState(state State) bool {
	return state == StateActive || state == StateSuspended || state == StateRevoked
}

// validPrincipal validates identity state and canonical subject material.
func validPrincipal(principal Principal) bool {
	return ValidateID(principal.ID) == nil && validDigest(principal.SubjectDigest) && validState(principal.State) && principal.Version > 0
}

// validMembership verifies bounded role/project references and temporal ordering.
func validMembership(snapshot *Snapshot, membership Membership, now time.Time, limits Limits) bool {
	if !validState(membership.State) || membership.Version == 0 || membership.ValidFrom.After(now) || !membership.ValidUntil.IsZero() && !membership.ValidUntil.After(membership.ValidFrom) || len(membership.RoleIDs) > limits.MaxItems || len(membership.ProjectIDs) > limits.MaxItems || hasDuplicates(membership.RoleIDs) || hasDuplicates(membership.ProjectIDs) {
		return false
	}
	for _, roleID := range membership.RoleIDs {
		if _, ok := snapshot.Roles[roleKey(membership.OrganizationID, roleID)]; !ok {
			return false
		}
	}
	for _, projectID := range membership.ProjectIDs {
		if _, ok := snapshot.Projects[projectKey(membership.OrganizationID, projectID)]; !ok {
			return false
		}
	}
	return true
}

// validServiceIdentity checks owner linkage, expiry and non-human lifecycle state.
func validServiceIdentity(snapshot Snapshot, identity ServiceIdentity, now time.Time) bool {
	_, ownerOK := snapshot.Principals[principalKey(identity.OrganizationID, identity.OwnerPrincipalID)]
	return ownerOK && ValidateID(identity.Purpose) == nil && validState(identity.State) && identity.Version > 0 && identity.ExpiresAt.After(now)
}
