package revisionbinding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// SafeValue is a classified semantic node; unclassified raw literals have no representation.
type SafeValue struct {
	kind     string
	text     string
	number   int64
	boolean  bool
	children []SafeValue
	fields   map[string]SafeValue
}

// PublicString declares one non-secret semantic string for hashing.
func PublicString(value string) SafeValue { return SafeValue{kind: "public_string", text: value} }

// PublicInt declares one non-secret integer for hashing.
func PublicInt(value int64) SafeValue { return SafeValue{kind: "integer", number: value} }

// PublicBool declares one non-secret boolean for hashing.
func PublicBool(value bool) SafeValue { return SafeValue{kind: "boolean", boolean: value} }

// CredentialReference substitutes an opaque configured identity for secret bytes.
func CredentialReference(id string) SafeValue {
	return SafeValue{kind: "credential_reference", text: id}
}

// SafeArray preserves the semantic order of classified children.
func SafeArray(values ...SafeValue) SafeValue {
	children := make([]SafeValue, len(values))
	for i, value := range values {
		children[i] = cloneSafe(value)
	}
	return SafeValue{kind: "array", children: children}
}

// SafeObject copies a map of classified children; JSON canonicalizes insertion order.
func SafeObject(fields map[string]SafeValue) SafeValue {
	copy := make(map[string]SafeValue, len(fields))
	for key, value := range fields {
		copy[key] = cloneSafe(value)
	}
	return SafeValue{kind: "object", fields: copy}
}

// cloneSafe transfers ownership of a classified projection tree to its parent.
func cloneSafe(value SafeValue) SafeValue {
	if value.children != nil {
		children := make([]SafeValue, len(value.children))
		for i, child := range value.children {
			children[i] = cloneSafe(child)
		}
		value.children = children
	}
	if value.fields != nil {
		fields := make(map[string]SafeValue, len(value.fields))
		for key, child := range value.fields {
			fields[key] = cloneSafe(child)
		}
		value.fields = fields
	}
	return value
}

// HashSafe hashes a complete, classified, bounded semantic projection under a domain version.
func HashSafe(ctx context.Context, kind string, value SafeValue) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", &Error{Cancelled, "hash"}
	}
	if kind != "constraints" && kind != "plan" && kind != "contract" && kind != "configuration" {
		return "", &Error{BindingIntegrity, "hash"}
	}
	canonical, err := safeProjection(ctx, value, 0)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(struct {
		Version int    `json:"version"`
		Kind    string `json:"kind"`
		Value   any    `json:"value"`
	}{1, kind, canonical})
	if err != nil || len(raw) > 1<<20 {
		return "", &Error{BindingIntegrity, "hash"}
	}
	if err := ctx.Err(); err != nil {
		return "", &Error{Cancelled, "hash"}
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// safeProjection serializes only declared safe primitives and checks cancellation/size/depth.
func safeProjection(ctx context.Context, value SafeValue, depth int) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, &Error{Cancelled, "hash"}
	}
	if depth > 32 {
		return nil, &Error{BindingIntegrity, "hash"}
	}
	switch value.kind {
	case "public_string":
		if len(value.text) > 8192 {
			return nil, &Error{BindingIntegrity, "hash"}
		}
		return map[string]any{"public_string": value.text}, nil
	case "integer":
		return map[string]any{"integer": value.number}, nil
	case "boolean":
		return map[string]any{"boolean": value.boolean}, nil
	case "credential_reference":
		if !ValidID(value.text) {
			return nil, &Error{BindingIntegrity, "hash"}
		}
		return map[string]any{"credential_reference": value.text}, nil
	case "array":
		if len(value.children) > 4096 {
			return nil, &Error{BindingIntegrity, "hash"}
		}
		out := make([]any, len(value.children))
		for i, child := range value.children {
			var err error
			out[i], err = safeProjection(ctx, child, depth+1)
			if err != nil {
				return nil, err
			}
		}
		return map[string]any{"array": out}, nil
	case "object":
		if len(value.fields) > 4096 {
			return nil, &Error{BindingIntegrity, "hash"}
		}
		out := make(map[string]any, len(value.fields))
		for key, child := range value.fields {
			if !ValidID(key) || forbiddenKey(key) {
				return nil, &Error{BindingIntegrity, "hash"}
			}
			var err error
			out[key], err = safeProjection(ctx, child, depth+1)
			if err != nil {
				return nil, err
			}
		}
		return map[string]any{"object": out}, nil
	default:
		return nil, &Error{BindingIntegrity, "hash"}
	}
}

// forbiddenKey prevents a raw credential field from masquerading as public material.
func forbiddenKey(key string) bool {
	key = strings.ToLower(key)
	for _, fragment := range []string{"secret", "password", "token", "credential", "authorization", "literal", "env_var", "connection_string"} {
		if strings.Contains(key, fragment) {
			return true
		}
	}
	return false
}
