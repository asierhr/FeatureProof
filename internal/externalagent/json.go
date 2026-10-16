package externalagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const maxRequestBytes = 64 << 10

// DecodeStrict rejects oversized, deeply nested, duplicate, unknown, or trailing JSON.
func DecodeStrict(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > maxRequestBytes {
		return &AppError{Code: CodeInvalidRequest, Field: "request"}
	}
	check := json.NewDecoder(bytes.NewReader(raw))
	check.UseNumber()
	if err := inspectJSON(check, 0); err != nil {
		return &AppError{Code: CodeInvalidRequest, Field: "request"}
	}
	var trailing any
	if err := check.Decode(&trailing); err != io.EOF {
		return &AppError{Code: CodeInvalidRequest, Field: "request"}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return &AppError{Code: CodeInvalidRequest, Field: "request"}
	}
	return nil
}

// inspectJSON validates structural limits and duplicate object names recursively.
func inspectJSON(decoder *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("depth")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("key")
			}
			if _, duplicate := seen[name]; duplicate {
				return errors.New("duplicate")
			}
			seen[name] = struct{}{}
			if err := inspectJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := inspectJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("delimiter")
	}
	_, err = decoder.Token()
	return err
}
