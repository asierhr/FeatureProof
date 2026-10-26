// Package kubernetes implements the managed EnvironmentProvider using a closed Kubernetes vocabulary.
package kubernetes

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/environment"
	distributionreference "github.com/distribution/reference"
	ocidigest "github.com/opencontainers/go-digest"
)

// ProviderID is the composition identity used to register the Kubernetes adapter.
const ProviderID = "kubernetes"

// ProviderVersion is the first stable Kubernetes environment adapter contract.
const ProviderVersion = "v1"

// CatalogVersion identifies the private Kubernetes catalog schema.
const CatalogVersion = 1

// boundedID constrains every composition identifier before it reaches cluster metadata.
var boundedID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// fullOID accepts only complete lowercase Git SHA-1 or SHA-256 object identities.
var fullOID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// NetworkRule is one composition-owned and direction-specific CIDR/port permission.
type NetworkRule struct {
	CIDR string `json:"cidr"`
	Port int    `json:"port"`
}

// SecretReference selects one composition-owned application or image-pull secret bundle entry.
type SecretReference struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

// Entry is one immutable template, project policy, cluster capability, and image relation.
type Entry struct {
	TemplateReference   string            `json:"template_reference"`
	TemplateDigest      string            `json:"template_digest"`
	ProfileID           string            `json:"profile_id"`
	NetworkPolicyID     string            `json:"network_policy_id"`
	ClusterID           string            `json:"cluster_id"`
	Image               string            `json:"image"`
	Replicas            int32             `json:"replicas"`
	ContainerPort       int32             `json:"container_port"`
	ServicePort         int32             `json:"service_port"`
	Scheme              string            `json:"scheme"`
	BasePath            string            `json:"base_path"`
	ReadinessPath       string            `json:"readiness_path"`
	InitialDelay        int32             `json:"readiness_initial_delay_seconds"`
	Period              int32             `json:"readiness_period_seconds"`
	Timeout             int32             `json:"readiness_timeout_seconds"`
	FailureThreshold    int32             `json:"readiness_failure_threshold"`
	CPURequestMilli     int64             `json:"cpu_request_milli"`
	CPULimitMilli       int64             `json:"cpu_limit_milli"`
	MemoryRequest       int64             `json:"memory_request_bytes"`
	MemoryLimit         int64             `json:"memory_limit_bytes"`
	QuotaCPUMilli       int64             `json:"quota_cpu_milli"`
	QuotaMemory         int64             `json:"quota_memory_bytes"`
	MaxPods             int64             `json:"max_pods"`
	MaxServices         int64             `json:"max_services"`
	TTL                 time.Duration     `json:"ttl"`
	MaxTTL              time.Duration     `json:"max_ttl"`
	PodSecurity         string            `json:"pod_security"`
	NetworkSupported    bool              `json:"network_supported"`
	Ingress             []NetworkRule     `json:"ingress"`
	Egress              []NetworkRule     `json:"egress"`
	SecretReferences    []SecretReference `json:"secret_references"`
	CredentialReference string            `json:"credential_reference,omitempty"`
	Capabilities        []string          `json:"capabilities"`
	Digest              string            `json:"digest"`
}

// Catalog is the sealed composition-owned set of Kubernetes environment authorities.
type Catalog struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
	Digest  string  `json:"digest"`
}

// SealCatalog validates, canonicalizes, detaches, and fingerprints all Kubernetes authority.
func SealCatalog(value Catalog) (Catalog, error) {
	value.Digest = ""
	if value.Version != CatalogVersion || len(value.Entries) == 0 {
		return Catalog{}, environmentError(environment.CategoryInvalid, "kubernetes_catalog", nil)
	}
	entries := make([]Entry, len(value.Entries))
	seen := make(map[string]struct{}, len(value.Entries))
	for index, entry := range value.Entries {
		sealed, err := sealEntry(entry)
		key := entryKey(sealed.TemplateReference, sealed.ProfileID, sealed.NetworkPolicyID)
		if err != nil {
			return Catalog{}, err
		}
		if _, duplicate := seen[key]; duplicate {
			return Catalog{}, environmentError(environment.CategoryInvalid, "kubernetes_catalog_duplicate", nil)
		}
		seen[key] = struct{}{}
		entries[index] = sealed
	}
	sort.Slice(entries, func(left, right int) bool {
		return entryKey(entries[left].TemplateReference, entries[left].ProfileID, entries[left].NetworkPolicyID) < entryKey(entries[right].TemplateReference, entries[right].ProfileID, entries[right].NetworkPolicyID)
	})
	value.Entries = entries
	value.Digest = digest(value)
	return value, nil
}

// DecodeCatalog rejects ambiguous or extensible JSON before sealing trusted composition.
func DecodeCatalog(raw []byte) (Catalog, error) {
	var value Catalog
	if len(raw) == 0 || duplicateJSONKey(raw) {
		return Catalog{}, environmentError(environment.CategoryInvalid, "kubernetes_catalog_decode", nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return Catalog{}, environmentError(environment.CategoryInvalid, "kubernetes_catalog_decode", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Catalog{}, environmentError(environment.CategoryInvalid, "kubernetes_catalog_decode", err)
	}
	return SealCatalog(value)
}

// Resolve authenticates the catalog and returns the exact entry selected by neutral authority.
func (c Catalog) Resolve(selection environment.Selection) (Entry, error) {
	sealed, err := SealCatalog(c)
	trusted, selectionErr := environment.SealSelection(selection)
	if err != nil || selectionErr != nil || sealed.Digest != c.Digest || trusted.Digest != selection.Digest || selection.Kind != environment.KindManaged || selection.ProviderID != ProviderID || selection.ProviderVersion != ProviderVersion {
		return Entry{}, environmentError(environment.CategoryIntegrity, "kubernetes_catalog_authority", nil)
	}
	wanted := entryKey(selection.TemplateReference, selection.ProfileID, selection.NetworkPolicyID)
	for _, entry := range sealed.Entries {
		secretAuthorityMatches := selection.Secretless == (len(entry.SecretReferences) == 0) && selection.CredentialReference == entry.CredentialReference
		if entryKey(entry.TemplateReference, entry.ProfileID, entry.NetworkPolicyID) == wanted && equalStrings(entry.Capabilities, selection.Capabilities) && secretAuthorityMatches {
			return cloneEntry(entry), nil
		}
	}
	return Entry{}, environmentError(environment.CategoryUnauthorized, "kubernetes_catalog_selection", nil)
}

// sealEntry enforces the closed template and policy vocabulary without introducing defaults.
func sealEntry(value Entry) (Entry, error) {
	value.Digest = ""
	ingressCount, egressCount, secretCount, capabilityCount := len(value.Ingress), len(value.Egress), len(value.SecretReferences), len(value.Capabilities)
	value.Ingress = sortedRules(value.Ingress)
	value.Egress = sortedRules(value.Egress)
	value.SecretReferences = sortedSecretReferences(value.SecretReferences)
	value.Capabilities = sortedStrings(value.Capabilities)
	validIDs := boundedID.MatchString(value.TemplateReference) && digestPattern(value.TemplateDigest) && boundedID.MatchString(value.ProfileID) && boundedID.MatchString(value.NetworkPolicyID) && boundedID.MatchString(value.ClusterID)
	validResources := value.Replicas > 0 && value.ContainerPort > 0 && value.ContainerPort <= 65535 && value.ServicePort > 0 && value.ServicePort <= 65535 && value.CPURequestMilli > 0 && value.CPULimitMilli >= value.CPURequestMilli && value.MemoryRequest > 0 && value.MemoryLimit >= value.MemoryRequest && value.QuotaCPUMilli >= value.CPULimitMilli*int64(value.Replicas) && value.QuotaMemory >= value.MemoryLimit*int64(value.Replicas) && value.MaxPods >= int64(value.Replicas) && value.MaxServices >= 1
	validSecretAuthority := len(value.SecretReferences) == 0 && value.CredentialReference == "" || len(value.SecretReferences) > 0 && boundedID.MatchString(value.CredentialReference)
	validPolicy := value.TTL > 0 && value.MaxTTL > 0 && value.TTL <= value.MaxTTL && value.PodSecurity == "restricted" && value.NetworkSupported && (value.Scheme == "http" || value.Scheme == "https") && strings.HasPrefix(value.BasePath, "/") && strings.HasPrefix(value.ReadinessPath, "/") && value.InitialDelay >= 0 && value.Period > 0 && value.Timeout > 0 && value.FailureThreshold > 0 && validImmutableImage(value.Image) && validSecretAuthority && len(value.Capabilities) > 0
	if !validIDs || !validResources || !validPolicy || len(value.Ingress) != ingressCount || len(value.Egress) != egressCount || len(value.SecretReferences) != secretCount || len(value.Capabilities) != capabilityCount || !validRules(value.Ingress) || !validRules(value.Egress) || !validSecretReferences(value.SecretReferences) || !validIDsOnly(value.Capabilities) {
		return Entry{}, environmentError(environment.CategoryInvalid, "kubernetes_catalog_entry", nil)
	}
	value.Digest = digest(value)
	return value, nil
}

// validImmutableImage requires the official distribution parser to round-trip one canonical SHA-256 reference exactly.
func validImmutableImage(value string) bool {
	named, err := distributionreference.ParseNormalizedNamed(value)
	if err != nil || named.String() != value || named.Name() != strings.ToLower(named.Name()) {
		return false
	}
	if _, tagged := named.(distributionreference.NamedTagged); tagged {
		return false
	}
	canonical, ok := named.(distributionreference.Canonical)
	if !ok || canonical.Digest().Algorithm() != ocidigest.SHA256 || canonical.Digest().Validate() != nil {
		return false
	}
	return len(canonical.Digest().Encoded()) == sha256.Size*2 && strings.ToLower(canonical.Digest().Encoded()) == canonical.Digest().Encoded()
}

// cloneEntry prevents callers from mutating sealed slice authority.
func cloneEntry(value Entry) Entry {
	value.Ingress = append([]NetworkRule(nil), value.Ingress...)
	value.Egress = append([]NetworkRule(nil), value.Egress...)
	value.SecretReferences = append([]SecretReference(nil), value.SecretReferences...)
	value.Capabilities = append([]string(nil), value.Capabilities...)
	return value
}

// entryKey forms a local lookup identity without granting new authority.
func entryKey(template, profile, network string) string {
	return template + "\x00" + profile + "\x00" + network
}

// digest fingerprints canonical secret-free projections used for drift detection.
func digest(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// digestPattern checks complete lowercase SHA-256 identities.
func digestPattern(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

// validIDsOnly rejects empty, duplicate, or unbounded authority lists.
func validIDsOnly(values []string) bool {
	if len(values) != len(sortedStrings(values)) {
		return false
	}
	for _, value := range values {
		if !boundedID.MatchString(value) {
			return false
		}
	}
	return true
}

// validRules ensures every allowlisted network edge is explicit and canonical.
func validRules(values []NetworkRule) bool {
	if len(values) != len(sortedRules(values)) {
		return false
	}
	for _, value := range values {
		_, network, err := net.ParseCIDR(value.CIDR)
		if err != nil || network.String() != value.CIDR || value.Port < 1 || value.Port > 65535 {
			return false
		}
	}
	return true
}

// sortedStrings returns a detached canonical set while collapsing duplicates for validation.
func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	write := 0
	for _, value := range result {
		if write == 0 || result[write-1] != value {
			result[write] = value
			write++
		}
	}
	return result[:write]
}

// sortedRules returns deterministic network policy order while collapsing duplicates.
func sortedRules(values []NetworkRule) []NetworkRule {
	result := append([]NetworkRule(nil), values...)
	sort.Slice(result, func(left, right int) bool {
		if result[left].CIDR == result[right].CIDR {
			return result[left].Port < result[right].Port
		}
		return result[left].CIDR < result[right].CIDR
	})
	write := 0
	for _, value := range result {
		if write == 0 || result[write-1] != value {
			result[write] = value
			write++
		}
	}
	return result[:write]
}

// sortedSecretReferences returns deterministic secret authority while collapsing exact duplicates.
func sortedSecretReferences(values []SecretReference) []SecretReference {
	result := append([]SecretReference(nil), values...)
	sort.Slice(result, func(left, right int) bool {
		if result[left].ID == result[right].ID {
			return result[left].Kind < result[right].Kind
		}
		return result[left].ID < result[right].ID
	})
	write := 0
	for _, value := range result {
		if write == 0 || result[write-1] != value {
			result[write] = value
			write++
		}
	}
	return result[:write]
}

// validSecretReferences restricts secret use to application injection or image-pull authentication.
func validSecretReferences(values []SecretReference) bool {
	for _, value := range values {
		if !boundedID.MatchString(value.ID) || (value.Kind != "application" && value.Kind != "image_pull") {
			return false
		}
	}
	return true
}

// equalStrings compares two canonical authority sets without order widening.
func equalStrings(left, right []string) bool {
	left, right = sortedStrings(left), sortedStrings(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// duplicateJSONKey detects ambiguity at every object depth before typed decoding.
func duplicateJSONKey(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var scan func() bool
	scan = func() bool {
		token, err := decoder.Token()
		if err != nil {
			return true
		}
		delimiter, composite := token.(json.Delim)
		if !composite {
			return false
		}
		switch delimiter {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, keyErr := decoder.Token()
				key, keyOK := keyToken.(string)
				if keyErr != nil || !keyOK {
					return true
				}
				if _, exists := seen[key]; exists {
					return true
				}
				seen[key] = struct{}{}
				if scan() {
					return true
				}
			}
			_, err = decoder.Token()
			return err != nil
		case '[':
			for decoder.More() {
				if scan() {
					return true
				}
			}
			_, err = decoder.Token()
			return err != nil
		default:
			return true
		}
	}
	return scan()
}

// environmentError constructs a content-free lifecycle error at the adapter boundary.
func environmentError(category environment.ErrorCategory, operation string, cause error) error {
	return &environment.Error{Category: category, Operation: operation, Cause: cause}
}
