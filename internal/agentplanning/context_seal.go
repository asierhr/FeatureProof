package agentplanning

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
)

const assembledContextVersion = "featureproof.context-snapshot/v1"

// assembledContextEnvelope is the closed canonical model-facing context schema accepted by planning.
type assembledContextEnvelope struct {
	Version       string          `json:"version"`
	Role          string          `json:"role"`
	PlanID        string          `json:"plan_id"`
	Configuration json.RawMessage `json:"configuration"`
	Untrusted     json.RawMessage `json:"untrusted"`
	ContextDigest string          `json:"context_digest"`
	ContextSeal   string          `json:"context_seal"`
}

// assembledContextSeal recomputes the composition seal over both model channels and their exact identity.
func assembledContextSeal(envelope assembledContextEnvelope, instructions string) (string, error) {
	payload := struct {
		Version, Role, PlanID, Instructions string
		Configuration, Untrusted            json.RawMessage
		ContextDigest                       string
	}{Version: envelope.Version, Role: envelope.Role, PlanID: envelope.PlanID, Instructions: instructions, Configuration: envelope.Configuration, Untrusted: envelope.Untrusted, ContextDigest: envelope.ContextDigest}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// decodeAssembledContext rejects duplicate, unknown, trailing and non-canonical representations before inference.
func decodeAssembledContext(raw json.RawMessage) (assembledContextEnvelope, error) {
	if duplicateContextJSON(raw) != nil {
		return assembledContextEnvelope{}, errors.New("duplicate context field")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope assembledContextEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return assembledContextEnvelope{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return assembledContextEnvelope{}, errors.New("trailing context json")
	}
	canonical, err := json.Marshal(envelope)
	if err != nil || !bytes.Equal(canonical, raw) {
		return assembledContextEnvelope{}, errors.New("non-canonical context json")
	}
	return envelope, nil
}

// duplicateContextJSON recursively detects repeated JSON object keys before typed decoding discards them.
func duplicateContextJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, keyErr := decoder.Token()
				key, valid := keyToken.(string)
				if keyErr != nil || !valid || seen[key] {
					return errors.New("duplicate context field")
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("invalid context delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing context json")
	}
	return nil
}
