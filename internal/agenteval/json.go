package agenteval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
)

// decodeStrict rejects duplicate keys, trailing values and unknown fields before typed composition.
func decodeStrict(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := scanValue(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return errors.New("trailing value")
		}
		return err
	}
	dec = json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

// scanValue recursively validates object-key uniqueness without allocating an unbounded generic tree.
func scanValue(dec *json.Decoder) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch d {
	case '{':
		seen := map[string]struct{}{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return err
			}
			k, ok := kt.(string)
			if !ok {
				return errors.New("non-string key")
			}
			if _, ok := seen[k]; ok {
				return errors.New("duplicate key")
			}
			seen[k] = struct{}{}
			if err := scanValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("invalid object")
		}
	case '[':
		for dec.More() {
			if err := scanValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("invalid array")
		}
	default:
		return errors.New("invalid delimiter")
	}
	return nil
}

// canonicalJSON returns compact deterministic JSON; encoding/json orders string map keys.
func canonicalJSON(value any) ([]byte, error) { return json.Marshal(value) }

// digestBytes returns a lowercase SHA-256 digest used for all local integrity links.
func digestBytes(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// typedError constructs a safe evaluation error and retains only context cancellation causes.
func typedError(category ErrorCategory, stage, path string, cause error) error {
	if category != Cancelled && category != Timeout {
		cause = nil
	}
	return &EvalError{Category: category, Stage: stage, Path: path, Cause: cause}
}
