package tenantsecurity

import (
	"encoding/json"
	"slices"
)

// CompileTenantPolicy validates, defensively copies and seals one organization policy snapshot.
func CompileTenantPolicy(policy TenantPolicy, limits Limits) (TenantPolicy, error) {
	if ValidateLimits(limits) != nil || ValidateID(policy.OrganizationID) != nil || policy.Version == 0 || policy.MaximumRetentionDays != 0 && policy.MinimumRetentionDays > policy.MaximumRetentionDays || len(policy.Allowed) > limits.MaxItems || len(policy.AllowedCapabilities) > limits.MaxItems {
		return TenantPolicy{}, failure(CategoryInvalid, "tenant_policy_compile", nil)
	}
	policy = cloneTenantPolicy(policy)
	slices.Sort(policy.AllowedCapabilities)
	if hasDuplicates(policy.AllowedCapabilities) {
		return TenantPolicy{}, failure(CategoryInvalid, "tenant_policy_compile", nil)
	}
	for kind, values := range policy.Allowed {
		if !slices.Contains([]string{"contract", "environment", "provider", "identity", "fixture", "runtime_profile", "model_profile", "auth_profile", "policy"}, kind) || len(values) > limits.MaxItems {
			return TenantPolicy{}, failure(CategoryInvalid, "tenant_policy_compile", nil)
		}
		slices.Sort(values)
		if hasDuplicates(values) {
			return TenantPolicy{}, failure(CategoryInvalid, "tenant_policy_compile", nil)
		}
		for _, value := range values {
			if ValidateID(value) != nil {
				return TenantPolicy{}, failure(CategoryInvalid, "tenant_policy_compile", nil)
			}
		}
		policy.Allowed[kind] = values
	}
	policy.Digest = ""
	payload, err := json.Marshal(policy)
	if err != nil || len(payload) > limits.MaxPayloadBytes {
		return TenantPolicy{}, failure(CategoryInvalid, "tenant_policy_compile", err)
	}
	policy.Digest = shortDigest(string(payload))
	return policy, nil
}

// ValidateTenantPolicy rejects altered canonical policy payloads after reload.
func ValidateTenantPolicy(policy TenantPolicy, limits Limits) error {
	compiled, err := CompileTenantPolicy(policy, limits)
	if err != nil || compiled.Digest != policy.Digest {
		return failure(CategoryIntegrity, "tenant_policy_validate", err)
	}
	return nil
}

// cloneTenantPolicy isolates all policy maps and slices from callers.
func cloneTenantPolicy(policy TenantPolicy) TenantPolicy {
	clone := policy
	clone.Allowed = make(map[string][]string, len(policy.Allowed))
	for kind, values := range policy.Allowed {
		clone.Allowed[kind] = slices.Clone(values)
	}
	clone.AllowedCapabilities = slices.Clone(policy.AllowedCapabilities)
	return clone
}
