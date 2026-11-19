package projectconfig

import "context"

// SnapshotCatalog is a composition-owned immutable catalog adapter for typed resource providers.
type SnapshotCatalog struct {
	snapshots map[ProjectKey]ResourceCatalogSnapshot
}

// NewSnapshotCatalog validates and copies trusted catalog snapshots at bootstrap.
func NewSnapshotCatalog(values []ResourceCatalogSnapshot) (*SnapshotCatalog, error) {
	result := &SnapshotCatalog{snapshots: make(map[ProjectKey]ResourceCatalogSnapshot, len(values))}
	for _, value := range values {
		if value.ProjectKey.Validate() != nil || !validDigest(value.Digest) || value.Limits.Validate() != nil || len(value.Descriptors) == 0 || len(value.Descriptors) > value.Limits.MaxDescriptors {
			return nil, &Error{Category: CategoryIntegrity, Operation: "catalog.compose"}
		}
		if _, exists := result.snapshots[value.ProjectKey]; exists {
			return nil, invalid("catalog.compose", "project_key")
		}
		if err := validateCatalogBounds(value); err != nil {
			return nil, err
		}
		clone := value.Clone()
		for _, descriptor := range value.Descriptors {
			if descriptor.ProjectKey != clone.ProjectKey || !validDigest(descriptor.Digest) || descriptor.Version == 0 || !canonicalID.MatchString(descriptor.ID) {
				return nil, &Error{Category: CategoryIntegrity, Operation: "catalog.compose"}
			}
		}
		result.snapshots[clone.ProjectKey] = clone
	}
	return result, nil
}

// Snapshot returns a defensive catalog copy or scoped absence without fallback.
func (catalog *SnapshotCatalog) Snapshot(ctx context.Context, key ProjectKey) (ResourceCatalogSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return ResourceCatalogSnapshot{}, err
	}
	if catalog == nil {
		return ResourceCatalogSnapshot{}, &Error{Category: CategoryUnavailable, Operation: "catalog.snapshot"}
	}
	value, ok := catalog.snapshots[key]
	if !ok {
		return ResourceCatalogSnapshot{}, &Error{Category: CategoryNotFound, Operation: "catalog.snapshot"}
	}
	return value.Clone(), nil
}

// SnapshotPolicies is a composition-owned immutable organization policy adapter.
type SnapshotPolicies struct {
	snapshots map[ProjectKey]OrganizationPolicySnapshot
}

// PolicyForProject pairs one trusted policy snapshot with its exact project scope.
type PolicyForProject struct {
	ProjectKey ProjectKey
	Snapshot   OrganizationPolicySnapshot
}

// NewSnapshotPolicies validates and copies trusted policies without embedded defaults.
func NewSnapshotPolicies(values []PolicyForProject) (*SnapshotPolicies, error) {
	result := &SnapshotPolicies{snapshots: make(map[ProjectKey]OrganizationPolicySnapshot, len(values))}
	for _, value := range values {
		if value.ProjectKey.Validate() != nil || value.Snapshot.Version == 0 || !validDigest(value.Snapshot.Digest) {
			return nil, &Error{Category: CategoryIntegrity, Operation: "policy.compose"}
		}
		if _, exists := result.snapshots[value.ProjectKey]; exists {
			return nil, invalid("policy.compose", "project_key")
		}
		result.snapshots[value.ProjectKey] = value.Snapshot.Clone()
	}
	return result, nil
}

// Snapshot returns a defensive policy copy and never substitutes another scope.
func (policies *SnapshotPolicies) Snapshot(ctx context.Context, key ProjectKey) (OrganizationPolicySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return OrganizationPolicySnapshot{}, err
	}
	if policies == nil {
		return OrganizationPolicySnapshot{}, &Error{Category: CategoryUnavailable, Operation: "policy.snapshot"}
	}
	value, ok := policies.snapshots[key]
	if !ok {
		return OrganizationPolicySnapshot{}, &Error{Category: CategoryNotFound, Operation: "policy.snapshot"}
	}
	return value.Clone(), nil
}
