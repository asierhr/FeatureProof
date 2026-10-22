package testidentity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/asierhr/featureProof/internal/specification"
)

// CloneDescriptors deep-copies the only identity representation allowed beyond trusted composition.
func CloneDescriptors(descriptors []Descriptor) []Descriptor {
	out := make([]Descriptor, len(descriptors))
	for i, descriptor := range descriptors {
		out[i] = cloneDescriptor(descriptor)
	}
	return out
}

// CanonicalDescriptors validates safe descriptor shape and returns identity and operation order independent of callers.
func CanonicalDescriptors(descriptors []Descriptor) ([]Descriptor, error) {
	out := CloneDescriptors(descriptors)
	seenIDs := map[string]bool{}
	for i := range out {
		descriptor := &out[i]
		if descriptor.ID == "" || seenIDs[descriptor.ID] || descriptor.Mechanism != MechanismBearer && descriptor.Mechanism != MechanismHeader && descriptor.Mechanism != MechanismCookie || len(descriptor.Operations) == 0 {
			return nil, typedError(CategoryInternal, "descriptor", "identity", nil)
		}
		seenIDs[descriptor.ID] = true
		seenOperations := map[Operation]bool{}
		for _, operation := range descriptor.Operations {
			if operation.Capability != "http.request" || operation.TargetID == "" || operation.Method == "" || operation.Method != strings.ToUpper(operation.Method) || !strings.HasPrefix(operation.Path, "/") || strings.ContainsAny(operation.Path, "?#\\%") || seenOperations[operation] {
				return nil, typedError(CategoryInternal, "descriptor", "operation", nil)
			}
			seenOperations[operation] = true
		}
		sortOperations(descriptor.Operations)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ValidateDescriptors rejects non-canonical or malformed snapshots before planning, approval, recovery, or execution.
func ValidateDescriptors(descriptors []Descriptor) error {
	if len(descriptors) == 0 {
		return nil
	}
	canonical, err := CanonicalDescriptors(descriptors)
	if err != nil {
		return err
	}
	actual, _ := json.Marshal(descriptors)
	want, _ := json.Marshal(canonical)
	if !bytes.Equal(actual, want) {
		return typedError(CategoryInternal, "descriptor", "canonical_order", nil)
	}
	return nil
}

// DescriptorDigest seals canonical non-secret identity authorization without credential references or material.
func DescriptorDigest(descriptors []Descriptor) (string, error) {
	canonical, err := CanonicalDescriptors(descriptors)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", typedError(CategoryInternal, "descriptor", "digest", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// catalogEntry retains the opaque credential reference only inside the trusted identity boundary.
type catalogEntry struct {
	descriptor    Descriptor
	credentialRef string
	name          string
	jsonPaths     []string
}

// Catalog is an immutable exact-name index used to validate selections and resolve private composition metadata.
type Catalog struct {
	entries map[string]catalogEntry
	digest  string
}

// NewCatalog validates and snapshots already validated specification declarations without resolving credential material.
func NewCatalog(identities []specification.TestIdentity) (*Catalog, error) {
	entries := make(map[string]catalogEntry, len(identities))
	for _, identity := range identities {
		if identity.ID == "" || identity.CredentialRef == "" {
			return nil, typedError(CategoryInternal, "catalog", "identities", nil)
		}
		if _, exists := entries[identity.ID]; exists {
			return nil, typedError(CategoryInternal, "catalog", "identities", nil)
		}
		operations := make([]Operation, len(identity.Operations))
		for j, operation := range identity.Operations {
			operations[j] = Operation{Capability: operation.Capability, TargetID: identity.TargetID, Method: operation.Method, Path: operation.Path}
		}
		sortOperations(operations)
		entries[identity.ID] = catalogEntry{descriptor: Descriptor{ID: identity.ID, Mechanism: Mechanism(identity.Mechanism.Type), Operations: operations}, credentialRef: identity.CredentialRef, name: identity.Mechanism.Name, jsonPaths: append([]string(nil), identity.Mechanism.SensitiveJSONPaths...)}
	}
	digest, err := catalogDigest(entries)
	if err != nil {
		return nil, typedError(CategoryInternal, "catalog", "digest", err)
	}
	return &Catalog{entries: entries, digest: digest}, nil
}

// Digest returns the canonical authorization snapshot fingerprint used to detect drift without including secret material.
func (c *Catalog) Digest() string {
	if c == nil {
		return ""
	}
	return c.digest
}

// Descriptors returns canonical deep copies filtered to identities that structurally authorize the requested operation.
func (c *Catalog) Descriptors(operation Operation) []Descriptor {
	if c == nil {
		return nil
	}
	ids := make([]string, 0, len(c.entries))
	for id, entry := range c.entries {
		if authorizes(entry.descriptor.Operations, operation) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]Descriptor, 0, len(ids))
	for _, id := range ids {
		out = append(out, cloneDescriptor(c.entries[id].descriptor))
	}
	return out
}

// DescriptorsFor returns canonical descriptors containing only permissions relevant to the requested operation set.
func (c *Catalog) DescriptorsFor(operations []Operation) []Descriptor {
	if c == nil {
		return nil
	}
	byID := map[string]Descriptor{}
	for _, requested := range operations {
		for id, entry := range c.entries {
			for _, allowed := range entry.descriptor.Operations {
				if _, matches := matchingTemplate([]Operation{allowed}, requested); !matches {
					continue
				}
				descriptor := byID[id]
				descriptor.ID, descriptor.Mechanism = id, entry.descriptor.Mechanism
				duplicate := false
				for _, existing := range descriptor.Operations {
					if existing == allowed {
						duplicate = true
						break
					}
				}
				if !duplicate {
					descriptor.Operations = append(descriptor.Operations, allowed)
				}
				byID[id] = descriptor
			}
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Descriptor, 0, len(ids))
	for _, id := range ids {
		descriptor := byID[id]
		sortOperations(descriptor.Operations)
		out = append(out, cloneDescriptor(descriptor))
	}
	return out
}

// Bind proves an explicit identity selection authorizes the concrete operation and returns a safe canonical binding.
func (c *Catalog) Bind(identityID string, operation Operation) (Binding, error) {
	if c == nil {
		return Binding{}, typedError(CategoryUnknownIdentity, "binding", "identity_id", nil)
	}
	entry, exists := c.entries[identityID]
	if !exists {
		return Binding{}, typedError(CategoryUnknownIdentity, "binding", "identity_id", nil)
	}
	template, exists := matchingTemplate(entry.descriptor.Operations, operation)
	if !exists {
		return Binding{}, typedError(CategoryIdentityNotAllowed, "binding", "operation", nil)
	}
	canonical := Operation{Capability: template.Capability, TargetID: template.TargetID, Method: template.Method, Path: template.Path}
	digest := bindingDigest(identityID, canonical)
	return Binding{IdentityID: identityID, Operation: canonical, Digest: digest}, nil
}

// BindDescriptor validates an explicit model-visible descriptor selection and creates the same canonical safe binding as Catalog.Bind.
func BindDescriptor(descriptor Descriptor, operation Operation) (Binding, error) {
	template, exists := matchingTemplate(descriptor.Operations, operation)
	if descriptor.ID == "" || !exists {
		return Binding{}, typedError(CategoryIdentityNotAllowed, "binding", "descriptor", nil)
	}
	canonical := Operation{Capability: template.Capability, TargetID: template.TargetID, Method: template.Method, Path: template.Path}
	return Binding{IdentityID: descriptor.ID, Operation: canonical, Digest: bindingDigest(descriptor.ID, canonical)}, nil
}

// Revalidate proves an earlier safe binding still belongs to the current immutable catalog and exact operation.
func (c *Catalog) Revalidate(binding Binding, concrete Operation) error {
	current, err := c.Bind(binding.IdentityID, concrete)
	if err != nil {
		return err
	}
	if current.Digest != binding.Digest || current.Operation != binding.Operation {
		return typedError(CategoryIdentityNotAllowed, "revalidate", "binding", nil)
	}
	return nil
}

// privateEntry retrieves resolver-only metadata after a binding has been revalidated inside the package.
func (c *Catalog) privateEntry(identityID string) (catalogEntry, bool) {
	entry, ok := c.entries[identityID]
	return entry, ok
}

// matchingTemplate returns the exact authorized template that structurally matches one concrete operation.
func matchingTemplate(allowed []Operation, concrete Operation) (Operation, bool) {
	for _, candidate := range allowed {
		if candidate.Capability == concrete.Capability && candidate.TargetID == concrete.TargetID && candidate.Method == concrete.Method && matchPath(candidate.Path, concrete.Path) {
			return candidate, true
		}
	}
	return Operation{}, false
}

// authorizes reports whether any declared template structurally contains the supplied operation.
func authorizes(allowed []Operation, operation Operation) bool {
	_, ok := matchingTemplate(allowed, operation)
	return ok
}

// matchPath compares decoded path segments and treats a whole template segment as exactly one parameter.
func matchPath(template, concrete string) bool {
	if template == "/" || concrete == "/" {
		return template == concrete
	}
	if strings.ContainsAny(concrete, "?#\\") || strings.Contains(concrete, "%") || !strings.HasPrefix(concrete, "/") {
		return false
	}
	templateSegments := strings.Split(strings.TrimPrefix(template, "/"), "/")
	concreteSegments := strings.Split(strings.TrimPrefix(concrete, "/"), "/")
	if len(templateSegments) != len(concreteSegments) {
		return false
	}
	for i, expected := range templateSegments {
		actual := concreteSegments[i]
		if actual == "" || actual == "." || actual == ".." {
			return false
		}
		parameter := len(expected) >= 3 && expected[0] == '{' && expected[len(expected)-1] == '}'
		if !parameter && expected != actual {
			return false
		}
	}
	return true
}

// cloneDescriptor isolates model-facing collections from the catalog's immutable authorization snapshot.
func cloneDescriptor(descriptor Descriptor) Descriptor {
	descriptor.Operations = append([]Operation(nil), descriptor.Operations...)
	return descriptor
}

// sortOperations imposes canonical authorization order independent of specification declaration order.
func sortOperations(operations []Operation) {
	sort.Slice(operations, func(i, j int) bool {
		left, right := operations[i], operations[j]
		if left.TargetID != right.TargetID {
			return left.TargetID < right.TargetID
		}
		if left.Capability != right.Capability {
			return left.Capability < right.Capability
		}
		if left.Method != right.Method {
			return left.Method < right.Method
		}
		return left.Path < right.Path
	})
}

// bindingDigest fingerprints only safe selection and permission facts used by approval, execution, and audit.
func bindingDigest(identityID string, operation Operation) string {
	raw, _ := json.Marshal(struct {
		IdentityID string    `json:"identity_id"`
		Operation  Operation `json:"operation"`
	}{identityID, operation})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// catalogDigest fingerprints canonical descriptors while deliberately excluding credential references and material.
func catalogDigest(entries map[string]catalogEntry) (string, error) {
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	descriptors := make([]Descriptor, 0, len(ids))
	for _, id := range ids {
		descriptors = append(descriptors, cloneDescriptor(entries[id].descriptor))
	}
	raw, err := json.Marshal(descriptors)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
