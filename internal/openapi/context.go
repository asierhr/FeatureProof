package openapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
)

// contextEnvelope is the allowlisted canonical representation given to future model adapters.
type contextEnvelope struct {
	Version     string              `json:"version"`
	Untrusted   bool                `json:"untrusted"`
	Constraints []contextConstraint `json:"constraints"`
	Operations  []contextOperation  `json:"operations"`
	Schemas     map[string]*Schema  `json:"schemas"`
	Omissions   OmissionCounts      `json:"omissions"`
}

// contextConstraint preserves the explicit caller-owned selection relationship.
type contextConstraint struct {
	ID         string         `json:"id"`
	Operations []OperationKey `json:"operations"`
}

// contextOperation keeps one selected operation as an indivisible size-budget unit.
type contextOperation struct {
	Key         OperationKey               `json:"key"`
	OperationID string                     `json:"operation_id,omitempty"`
	Parameters  []contextParameter         `json:"parameters,omitempty"`
	RequestBody map[string]string          `json:"request_body,omitempty"`
	Responses   map[string]contextResponse `json:"responses"`
	Security    []SecurityRequirement      `json:"security"`
}

// contextParameter references a deduplicated reachable schema.
type contextParameter struct {
	Name     string            `json:"name"`
	In       string            `json:"in"`
	Required bool              `json:"required,omitempty"`
	Schema   string            `json:"schema,omitempty"`
	Content  map[string]string `json:"content,omitempty"`
}

// contextResponse references deduplicated response schemas and declared headers.
type contextResponse struct {
	Headers map[string]string `json:"headers,omitempty"`
	Content map[string]string `json:"content,omitempty"`
}

// minimumContextEnvelopeBytes returns the exact canonical budget needed for an empty envelope.
func minimumContextEnvelopeBytes() int {
	b, _ := json.Marshal(contextEnvelope{Version: "3.1.0", Untrusted: true, Constraints: []contextConstraint{}, Operations: []contextOperation{}, Schemas: map[string]*Schema{}})
	return len(b)
}

// BuildContext returns a canonical bounded envelope containing only explicitly selected operations.
func (c *Contract) BuildContext(ctx context.Context, request ContextRequest) (ReducedContext, error) {
	if err := ctx.Err(); err != nil {
		return ReducedContext{}, cancelledError("context", err)
	}
	constraints, keys, err := c.validateSelection(request)
	if err != nil {
		return ReducedContext{}, err
	}
	omitted := c.omitted
	omitted.Unselected = len(c.operations) - len(keys)
	envelope := contextEnvelope{Version: c.version, Untrusted: true, Constraints: constraints, Operations: []contextOperation{}, Schemas: map[string]*Schema{}, Omissions: omitted}
	ordered := make([]OperationKey, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Path != ordered[j].Path {
			return ordered[i].Path < ordered[j].Path
		}
		return methodOrder[ordered[i].Method] < methodOrder[ordered[j].Method]
	})
	for _, key := range ordered {
		if err := ctx.Err(); err != nil {
			return ReducedContext{}, cancelledError("context", err)
		}
		op, _ := c.findOperation(key)
		unit, schemas := makeContextOperation(op)
		candidate := envelope
		candidate.Operations = append(append([]contextOperation(nil), envelope.Operations...), unit)
		candidate.Schemas = cloneSchemaRegistry(envelope.Schemas)
		for id, s := range schemas {
			candidate.Schemas[id] = s
		}
		b, err := json.Marshal(candidate)
		if err != nil {
			return ReducedContext{}, typedError(InvalidDocument, "context", err)
		}
		if err := ctx.Err(); err != nil {
			return ReducedContext{}, cancelledError("context", err)
		}
		if len(b) > c.maxContext {
			omitted.SizeLimit++
			envelope.Omissions = omitted
			continue
		}
		envelope = candidate
	}
	b, err := json.Marshal(envelope)
	if err != nil {
		return ReducedContext{}, typedError(InvalidDocument, "context", err)
	}
	if len(b) > c.maxContext {
		return ReducedContext{}, typedError(LimitExceeded, "context", errors.New("context envelope exceeds configured budget"))
	}
	if err := ctx.Err(); err != nil {
		return ReducedContext{}, cancelledError("context", err)
	}
	return ReducedContext{MediaType: "application/vnd.featureproof.openapi-context+json;v=1", Untrusted: true, Bytes: append([]byte(nil), b...), Omissions: omitted}, nil
}

// validateSelection rejects duplicate constraint IDs, duplicate keys and unknown operations.
func (c *Contract) validateSelection(request ContextRequest) ([]contextConstraint, map[OperationKey]bool, error) {
	ids := map[string]bool{}
	keys := map[OperationKey]bool{}
	out := make([]contextConstraint, 0, len(request.Constraints))
	for _, constraint := range request.Constraints {
		if constraint.ConstraintID == "" || ids[constraint.ConstraintID] {
			return nil, nil, typedError(InvalidInput, "context.constraints", errors.New("empty or duplicate constraint id"))
		}
		ids[constraint.ConstraintID] = true
		entry := contextConstraint{ID: constraint.ConstraintID, Operations: make([]OperationKey, 0, len(constraint.Operations))}
		local := map[OperationKey]bool{}
		for _, key := range constraint.Operations {
			key.Method = normalizeMethod(key.Method)
			if local[key] {
				return nil, nil, typedError(InvalidInput, "context.constraints.operations", errors.New("duplicate operation key"))
			}
			if _, ok := c.findOperation(key); !ok {
				return nil, nil, typedError(InvalidInput, "context.constraints.operations", errors.New("unknown operation key"))
			}
			local[key] = true
			keys[key] = true
			entry.Operations = append(entry.Operations, key)
		}
		sort.Slice(entry.Operations, func(i, j int) bool {
			if entry.Operations[i].Path != entry.Operations[j].Path {
				return entry.Operations[i].Path < entry.Operations[j].Path
			}
			return methodOrder[entry.Operations[i].Method] < methodOrder[entry.Operations[j].Method]
		})
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, keys, nil
}

// makeContextOperation allowlists fields and extracts reachable schemas into a deduplicated registry.
func makeContextOperation(op OperationDescriptor) (contextOperation, map[string]*Schema) {
	schemas := map[string]*Schema{}
	out := contextOperation{Key: op.Key, OperationID: op.OperationID, Responses: map[string]contextResponse{}, Security: cloneSecurity(op.Security)}
	for _, p := range op.Parameters {
		parameter := contextParameter{Name: p.Name, In: p.In, Required: p.Required, Schema: registerSchema(p.Schema, schemas)}
		if len(p.Content) > 0 {
			parameter.Content = map[string]string{}
			for _, mediaType := range sortedKeys(p.Content) {
				parameter.Content[mediaType] = registerSchema(p.Content[mediaType].Schema, schemas)
			}
		}
		out.Parameters = append(out.Parameters, parameter)
	}
	if op.RequestBody != nil {
		out.RequestBody = map[string]string{}
		for _, mt := range sortedKeys(op.RequestBody.Content) {
			out.RequestBody[mt] = registerSchema(op.RequestBody.Content[mt].Schema, schemas)
		}
	}
	for _, selector := range sortedKeys(op.Responses) {
		response := op.Responses[selector]
		item := contextResponse{Headers: map[string]string{}, Content: map[string]string{}}
		for _, name := range sortedKeys(response.Headers) {
			header := response.Headers[name]
			headerSchema := header.Schema
			if headerSchema == nil {
				for _, mediaType := range sortedKeys(header.Content) {
					headerSchema = header.Content[mediaType].Schema
					break
				}
			}
			item.Headers[name] = registerSchema(headerSchema, schemas)
		}
		for _, mt := range sortedKeys(response.Content) {
			item.Content[mt] = registerSchema(response.Content[mt].Schema, schemas)
		}
		out.Responses[selector] = item
	}
	return out, schemas
}

// registerSchema assigns the same content hash to equivalent reachable schemas.
func registerSchema(schema *Schema, registry map[string]*Schema) string {
	if schema == nil {
		return ""
	}
	b, _ := json.Marshal(schema)
	sum := sha256.Sum256(b)
	id := "schema-" + hex.EncodeToString(sum[:8])
	if _, ok := registry[id]; !ok {
		registry[id] = cloneSchema(schema)
	}
	return id
}

// cloneSchemaRegistry isolates candidate packing attempts from retained envelope state.
func cloneSchemaRegistry(in map[string]*Schema) map[string]*Schema {
	out := make(map[string]*Schema, len(in))
	for k, v := range in {
		out[k] = cloneSchema(v)
	}
	return out
}

// normalizeMethod canonicalizes operation methods at the public context boundary.
func normalizeMethod(method string) string {
	for lower, order := range methodOrder {
		_ = order
		if equalFoldASCII(lower, method) {
			return lower
		}
	}
	return method
}

// equalFoldASCII compares HTTP methods without locale-dependent transformations.
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if cb >= 'a' && cb <= 'z' {
			cb -= 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}
