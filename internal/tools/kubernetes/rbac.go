package kubernetes

import (
	"reflect"
	"sort"
)

// RBACRule describes one exact Kubernetes permission composition must grant to the tool identity.
type RBACRule struct {
	Scope         string   `json:"scope"`
	APIGroup      string   `json:"api_group"`
	Resource      string   `json:"resource"`
	Verbs         []string `json:"verbs"`
	ResourceNames []string `json:"resource_names,omitempty"`
}

// RequiredRBACRules returns the minimal immutable permissions used by APIReader for one namespace.
func RequiredRBACRules(namespace string) []RBACRule {
	rules := []RBACRule{
		{Scope: "cluster", APIGroup: "", Resource: "namespaces", Verbs: []string{"get"}, ResourceNames: []string{namespace}},
		{Scope: "namespace", APIGroup: "apps", Resource: "deployments", Verbs: []string{"get"}},
		{Scope: "namespace", APIGroup: "", Resource: "pods", Verbs: []string{"list"}},
		{Scope: "namespace", APIGroup: "batch", Resource: "jobs", Verbs: []string{"get"}},
		{Scope: "namespace", APIGroup: "", Resource: "services", Verbs: []string{"get"}},
		{Scope: "namespace", APIGroup: "discovery.k8s.io", Resource: "endpointslices", Verbs: []string{"list"}},
		{Scope: "namespace", APIGroup: "", Resource: "events", Verbs: []string{"list"}},
		{Scope: "namespace", APIGroup: "networking.k8s.io", Resource: "networkpolicies", Verbs: []string{"get"}},
	}
	return cloneRBACRules(rules)
}

// cloneRBACRules prevents callers from broadening shared permission templates in memory.
func cloneRBACRules(values []RBACRule) []RBACRule {
	output := make([]RBACRule, len(values))
	for index, value := range values {
		output[index] = value
		output[index].Verbs = append([]string(nil), value.Verbs...)
		output[index].ResourceNames = append([]string(nil), value.ResourceNames...)
	}
	return output
}

// SealIdentityBinding validates exact least-privilege composition and fingerprints it for registration.
func SealIdentityBinding(value IdentityBinding) (IdentityBinding, error) {
	value.Digest = ""
	if !boundedID.MatchString(value.Namespace) || !boundedID.MatchString(value.ServiceAccount) {
		return IdentityBinding{}, &ConfigError{Field: "identity"}
	}
	value.Rules = cloneRBACRules(value.Rules)
	for index := range value.Rules {
		sort.Strings(value.Rules[index].Verbs)
		sort.Strings(value.Rules[index].ResourceNames)
	}
	sort.Slice(value.Rules, func(left, right int) bool { return rbacRuleKey(value.Rules[left]) < rbacRuleKey(value.Rules[right]) })
	expected := RequiredRBACRules(value.Namespace)
	for index := range expected {
		sort.Strings(expected[index].Verbs)
		sort.Strings(expected[index].ResourceNames)
	}
	sort.Slice(expected, func(left, right int) bool { return rbacRuleKey(expected[left]) < rbacRuleKey(expected[right]) })
	if !reflect.DeepEqual(value.Rules, expected) {
		return IdentityBinding{}, &ConfigError{Field: "identity.rules"}
	}
	value.Digest = digest(value)
	return value, nil
}

// rbacRuleKey provides a total canonical order for effective permissions.
func rbacRuleKey(value RBACRule) string {
	return value.Scope + "\x00" + value.APIGroup + "\x00" + value.Resource
}
