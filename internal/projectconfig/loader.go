package projectconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

// LoadStrict decodes, validates and canonicalizes one bounded untrusted document.
func LoadStrict(reader io.Reader, maxBytes int64) (CanonicalDocument, error) {
	if reader == nil || maxBytes <= 0 || maxBytes > MaxCanonicalBytes {
		return CanonicalDocument{}, invalid("load", "document")
	}
	limited := io.LimitReader(reader, maxBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil || len(raw) == 0 || int64(len(raw)) > maxBytes {
		return CanonicalDocument{}, invalid("load", "document")
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		return CanonicalDocument{}, err
	}
	if err := rejectSecretValueFields(raw); err != nil {
		return CanonicalDocument{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return CanonicalDocument{}, invalid("decode", "document")
	}
	if err := ensureEOF(decoder); err != nil {
		return CanonicalDocument{}, err
	}
	return Canonicalize(document)
}

// Canonicalize validates structural invariants and produces one stable JSON representation.
func Canonicalize(document Document) (CanonicalDocument, error) {
	document = document.Clone()
	if document.Schema != SchemaV1 {
		return CanonicalDocument{}, &Error{Category: CategoryUnsupported, Operation: "canonicalize", Location: "schema"}
	}
	key, err := NewProjectKey(document.ProjectID, document.RepositoryID)
	if err != nil {
		return CanonicalDocument{}, err
	}
	if len(document.References) == 0 || len(document.References) > MaxReferences {
		return CanonicalDocument{}, invalid("canonicalize", "references")
	}
	seen := map[string]struct{}{}
	for index := range document.References {
		reference := &document.References[index]
		if err := validateReference(key, *reference); err != nil {
			return CanonicalDocument{}, err
		}
		identity := string(reference.Kind) + ":" + reference.ID
		if _, exists := seen[identity]; exists {
			return CanonicalDocument{}, invalid("canonicalize", "references")
		}
		seen[identity] = struct{}{}
	}
	slices.SortFunc(document.References, func(left, right ResourceReference) int {
		return strings.Compare(string(left.Kind)+":"+left.ID, string(right.Kind)+":"+right.ID)
	})
	document.Constraints.Capabilities, err = canonicalStrings(document.Constraints.Capabilities)
	if err != nil {
		return CanonicalDocument{}, err
	}
	if len(document.Constraints.Capabilities) > MaxDocumentCapabilities {
		return CanonicalDocument{}, invalid("canonicalize", "constraints.capabilities")
	}
	if document.Constraints.Approval != "required" && document.Constraints.Approval != "optional" {
		return CanonicalDocument{}, invalid("canonicalize", "constraints.approval")
	}
	if document.Constraints.RetentionDays == 0 {
		return CanonicalDocument{}, invalid("canonicalize", "constraints.retention_days")
	}
	encoded, err := json.Marshal(document)
	if err != nil || len(encoded) > MaxCanonicalBytes {
		return CanonicalDocument{}, invalid("canonicalize", "document")
	}
	return CanonicalDocument{Document: document, Bytes: encoded, Digest: canonicalDigest(encoded)}, nil
}

// ensureEOF rejects concatenated JSON values and trailing non-whitespace data.
func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return invalid("decode", "trailing_data")
	}
	return nil
}

// rejectDuplicateKeys walks JSON tokens so shadowed authority fields fail closed.
func rejectDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return invalid("decode", "document")
		}
		delimiter, compound := token.(json.Delim)
		if !compound {
			return nil
		}
		switch delimiter {
		case '{':
			keys := map[string]struct{}{}
			for decoder.More() {
				keyToken, keyErr := decoder.Token()
				if keyErr != nil {
					return invalid("decode", "document")
				}
				key, ok := keyToken.(string)
				if !ok {
					return invalid("decode", "document")
				}
				if _, exists := keys[key]; exists {
					return invalid("decode", "duplicate_field")
				}
				keys[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			if _, err = decoder.Token(); err != nil {
				return invalid("decode", "document")
			}
			return nil
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			if _, err = decoder.Token(); err != nil {
				return invalid("decode", "document")
			}
			return nil
		default:
			return invalid("decode", "document")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	return nil
}

// rejectSecretValueFields blocks credential-shaped values before typed decoding or diagnostics.
func rejectSecretValueFields(raw []byte) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return invalid("decode", "document")
	}
	forbidden := map[string]struct{}{"token": {}, "password": {}, "private_key": {}, "authorization": {}, "credential": {}, "secret_value": {}}
	var inspect func(any) error
	inspect = func(current any) error {
		switch typed := current.(type) {
		case map[string]any:
			for key, nested := range typed {
				if _, blocked := forbidden[strings.ToLower(key)]; blocked {
					return &Error{Category: CategoryInvalid, Operation: "decode", Location: "forbidden_secret_field"}
				}
				if err := inspect(nested); err != nil {
					return err
				}
			}
		case []any:
			for _, nested := range typed {
				if err := inspect(nested); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := inspect(value); err != nil {
		return err
	}
	return nil
}

// validateReference validates stable catalog identity without resolving secret material.
func validateReference(key ProjectKey, reference ResourceReference) error {
	_ = key
	switch reference.Kind {
	case ResourceContract, ResourceEnvironment, ResourceProvider, ResourceIdentity, ResourceFixture, ResourceRuntimeProfile, ResourceModelProfile, ResourceAuthProfile, ResourcePolicy:
	default:
		return invalid("canonicalize", "references.kind")
	}
	if !canonicalID.MatchString(reference.ID) || reference.Version == 0 || !validDigest(reference.Digest) {
		return invalid("canonicalize", "references.identity")
	}
	if reference.SecretRef != "" && !canonicalID.MatchString(reference.SecretRef) {
		return invalid("canonicalize", "references.secret_ref")
	}
	return nil
}

// formatLocation creates a bounded deterministic diagnostic path.
func formatLocation(parts ...any) string { return fmt.Sprint(parts...) }
