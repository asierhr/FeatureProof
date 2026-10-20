package validationworkflow

import (
	"sort"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/planmanifest"
)

// RevisionContext is the complete safe source identity observed by the binding boundary.
type RevisionContext struct {
	RepositoryID string   `json:"repository_id"`
	CommitOID    string   `json:"commit_oid"`
	Dirty        bool     `json:"dirty"`
	Roots        []string `json:"roots,omitempty"`
	ChangedPaths []string `json:"changed_paths,omitempty"`
}

// AttestationContext is the complete safe deployment proof used to authorize execution.
type AttestationContext struct {
	EnvironmentID string    `json:"environment_id"`
	RepositoryID  string    `json:"repository_id"`
	CommitOID     string    `json:"commit_oid"`
	Reference     string    `json:"reference"`
	ValidUntil    time.Time `json:"valid_until"`
	Verified      bool      `json:"verified"`
}

// NetworkContext records the effective allowlist rules applied to the selected target.
type NetworkContext struct {
	PolicyID    string   `json:"policy_id"`
	Schemes     []string `json:"schemes"`
	Hosts       []string `json:"hosts"`
	Ports       []int    `json:"ports"`
	BasePaths   []string `json:"base_paths"`
	MaxURLBytes int      `json:"max_url_bytes"`
}

// TargetContext records the effective direct or acquired preview target without credentials.
type TargetContext struct {
	Kind, Alias, URL                                           string
	ProviderID, EnvironmentRef, ProfileID                      string
	EnvironmentID, ReservationID, ObservedVersion, Fingerprint string
	Attestation                                                AttestationContext
	Network                                                    NetworkContext
}

// BindingContext contains the effective source and deployment objects returned by trusted composition.
type BindingContext struct {
	Revision RevisionContext `json:"revision"`
	Target   TargetContext   `json:"target"`
}

// ValidatorContext versions the executable input and assertion validators whose code is selected by composition.
type ValidatorContext struct {
	Tool, ToolVersion, AssertionEngine, AssertionVersion string
}

// SchemaContext versions the fixed executable input union and its accepted top-level fields.
type SchemaContext struct {
	Version, ReferenceVersion string
	Fields                    []string
}

// RegistryContext records the exact executable capability names installed in the runner registry.
type RegistryContext struct {
	Version string
	Tools   []string
}

// PolicyContext records the effective capability decision table selected by composition.
type PolicyContext struct {
	ID           string
	AllowedTools []string
	Reason       string
}

// RuntimeContext records the complete safe runtime, schema, redaction, validator, policy and network configuration.
type RuntimeContext struct {
	Profile         string
	ManifestVersion int
	HTTP            planmanifest.HTTPConfig
	Redaction       evidence.Config
	Registry        RegistryContext
	Schema          SchemaContext
	Validator       ValidatorContext
	Policy          PolicyContext
	Network         NetworkContext
}

// canonicalBindingContext normalizes set-like fields and timestamps before stable hashing.
// feature22:coverage validationworkflow.canonicalBindingContext
func canonicalBindingContext(value BindingContext) BindingContext {
	value.Revision.Roots = sortedStrings(value.Revision.Roots, nil)
	value.Revision.ChangedPaths = sortedStrings(value.Revision.ChangedPaths, nil)
	value.Target.Attestation.ValidUntil = value.Target.Attestation.ValidUntil.UTC()
	value.Target.Network = canonicalNetworkContext(value.Target.Network)
	return value
}

// canonicalRuntimeContext normalizes every set-like manifest, redaction, policy and network field.
func canonicalRuntimeContext(value RuntimeContext) RuntimeContext {
	value.HTTP.AllowedMethods = sortedStrings(value.HTTP.AllowedMethods, strings.ToUpper)
	value.HTTP.AllowedRequestHeaders = sortedStrings(value.HTTP.AllowedRequestHeaders, strings.ToLower)
	value.HTTP.AllowedResponseHeaders = sortedStrings(value.HTTP.AllowedResponseHeaders, strings.ToLower)
	value.HTTP.FollowRedirectsForMethods = sortedStrings(value.HTTP.FollowRedirectsForMethods, strings.ToUpper)
	value.Redaction.AdditionalHeaders = sortedStrings(value.Redaction.AdditionalHeaders, strings.ToLower)
	value.Redaction.SensitiveJSONPaths = sortedStrings(value.Redaction.SensitiveJSONPaths, nil)
	value.Redaction.ModelSensitiveJSONPaths = sortedStrings(value.Redaction.ModelSensitiveJSONPaths, nil)
	value.Registry.Tools = sortedStrings(value.Registry.Tools, nil)
	value.Schema.Fields = sortedStrings(value.Schema.Fields, nil)
	value.Policy.AllowedTools = sortedStrings(value.Policy.AllowedTools, nil)
	value.Network = canonicalNetworkContext(value.Network)
	return value
}

// canonicalNetworkContext normalizes network allowlist sets while preserving bounded scalar limits.
func canonicalNetworkContext(value NetworkContext) NetworkContext {
	value.Schemes = sortedStrings(value.Schemes, strings.ToLower)
	value.Hosts = sortedStrings(value.Hosts, strings.ToLower)
	value.BasePaths = sortedStrings(value.BasePaths, nil)
	value.Ports = append([]int(nil), value.Ports...)
	sort.Ints(value.Ports)
	return value
}

// sortedStrings copies, normalizes and sorts one semantic set for deterministic JSON.
func sortedStrings(values []string, normalize func(string) string) []string {
	out := append([]string(nil), values...)
	if normalize != nil {
		for index := range out {
			out[index] = normalize(out[index])
		}
	}
	sort.Strings(out)
	return out
}

// bindingDigests derives immutable revision and target identities from effective objects rather than caller hashes.
func bindingDigests(value BindingContext) (string, string, error) {
	canonical := canonicalBindingContext(value)
	revision, err := safeDigest(canonical.Revision)
	if err != nil {
		return "", "", err
	}
	target, err := safeDigest(canonical.Target)
	return revision, target, err
}

// runtimeContextDigest derives the runtime identity from its complete effective safe configuration.
func runtimeContextDigest(value RuntimeContext) (string, error) {
	return safeDigest(canonicalRuntimeContext(value))
}

// RuntimeContextDigest exposes the deterministic effective runtime identity needed by execution composition.
func RuntimeContextDigest(value RuntimeContext) (string, error) { return runtimeContextDigest(value) }

// TargetContextDigest exposes the deterministic target identity needed by trusted CLI alias registration.
func TargetContextDigest(value TargetContext) (string, error) {
	return safeDigest(canonicalBindingContext(BindingContext{Target: value}).Target)
}
