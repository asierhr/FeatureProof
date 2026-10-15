package agentplanning

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/asierhr/featureProof/internal/openapi"
)

// provenanceCapability excludes executable validators while preserving every descriptor field used during generation.
type provenanceCapability struct {
	Name, Class, Description string
	Effects, Idempotent      bool
}

// provenanceEnvelope is the closed canonical payload sealed into every successfully prepared plan.
type provenanceEnvelope struct {
	Version     int
	Constraints []Constraint
	OpenAPI     struct {
		MediaType string
		Untrusted bool
		Bytes     []byte
		Omissions openapi.OmissionCounts
		Retained  []openapi.RetainedOperation
	}
	Capabilities []provenanceCapability
}

// generationProvenance hashes the exact immutable generation inputs without retaining callbacks or credentials.
func generationProvenance(constraints []Constraint, reduced openapi.ReducedContext, capabilities []CapabilityDescriptor) (string, error) {
	envelope := provenanceEnvelope{Version: 1, Constraints: append([]Constraint(nil), constraints...)}
	envelope.OpenAPI.MediaType, envelope.OpenAPI.Untrusted = reduced.MediaType, reduced.Untrusted
	envelope.OpenAPI.Bytes = append([]byte(nil), reduced.Bytes...)
	envelope.OpenAPI.Omissions = reduced.Omissions
	envelope.OpenAPI.Retained = reduced.RetainedOperations()
	envelope.Capabilities = make([]provenanceCapability, len(capabilities))
	for i, capability := range capabilities {
		envelope.Capabilities[i] = provenanceCapability{Name: capability.Name, Class: capability.Class, Description: capability.Description, Effects: capability.Effects, Idempotent: capability.Idempotent}
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
