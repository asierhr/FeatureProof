package agentplanning

import (
	"sort"
	"strings"

	"github.com/asierhr/featureProof/internal/planning"
)

// CapabilityCatalog is an immutable exact-name index of non-executable capability descriptors.
type CapabilityCatalog struct {
	entries map[string]CapabilityDescriptor
}

// NewCapabilityCatalog snapshots and validates the complete descriptor set.
func NewCapabilityCatalog(descriptors []CapabilityDescriptor) (*CapabilityCatalog, error) {
	if descriptors == nil {
		return nil, typedError(CategoryInvalidInput, "preflight", "capabilities", nil)
	}
	catalog := &CapabilityCatalog{entries: make(map[string]CapabilityDescriptor, len(descriptors))}
	for i, descriptor := range descriptors {
		if strings.TrimSpace(descriptor.Name) == "" || descriptor.Name != strings.TrimSpace(descriptor.Name) || descriptor.Validate == nil {
			return nil, typedError(CategoryInvalidInput, "preflight", field("capabilities", i), nil)
		}
		if _, exists := catalog.entries[descriptor.Name]; exists {
			return nil, typedError(CategoryInvalidInput, "preflight", field("capabilities", i), nil)
		}
		catalog.entries[descriptor.Name] = descriptor
	}
	return catalog, nil
}

// ResolveDescriptor implements planning's descriptor-only registry boundary.
func (c *CapabilityCatalog) ResolveDescriptor(name string) (planning.CapabilitySpec, bool) {
	descriptor, ok := c.entries[name]
	return planning.CapabilitySpec{Name: descriptor.Name}, ok
}

// descriptor returns an isolated metadata value for semantic and policy validation.
func (c *CapabilityCatalog) descriptor(name string) (CapabilityDescriptor, bool) {
	descriptor, ok := c.entries[name]
	return descriptor, ok
}

// projections returns stable model-facing metadata and deliberately omits validation functions.
func (c *CapabilityCatalog) projections() []capabilityProjection {
	names := make([]string, 0, len(c.entries))
	for name := range c.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]capabilityProjection, 0, len(names))
	for _, name := range names {
		d := c.entries[name]
		out = append(out, capabilityProjection{Name: d.Name, Class: d.Class, Description: d.Description, Effects: d.Effects, Idempotent: d.Idempotent})
	}
	return out
}

// capabilityProjection is the serializable descriptor sent as untrusted planning data.
type capabilityProjection struct {
	Name        string `json:"name"`
	Class       string `json:"class"`
	Description string `json:"description,omitempty"`
	Effects     bool   `json:"effects"`
	Idempotent  bool   `json:"idempotent"`
}

// field creates a stable collection position without copying external values into diagnostics.
func field(base string, position int) string { return base + "[" + itoa(position) + "]" }

// itoa formats bounded structural positions without adding a formatting dependency at call sites.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := [20]byte{}
	pos := len(digits)
	for value > 0 {
		pos--
		digits[pos] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[pos:])
}
