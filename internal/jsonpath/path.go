// Package jsonpath owns the strict JSON Pointer subset shared by assertions and evidence redaction.
package jsonpath

import (
	"fmt"
	"regexp"
	"strings"
)

var numericAppearance = regexp.MustCompile(`^[+-]?[0-9]+$`)
var canonicalIndex = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

// Parse validates a JSON Pointer and returns its decoded, immutable token sequence.
func Parse(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("non-root path must begin with slash")
	}
	rawTokens := strings.Split(path[1:], "/")
	tokens := make([]string, len(rawTokens))
	for i, raw := range rawTokens {
		var builder strings.Builder
		for position := 0; position < len(raw); position++ {
			if raw[position] != '~' {
				builder.WriteByte(raw[position])
				continue
			}
			if position+1 >= len(raw) || (raw[position+1] != '0' && raw[position+1] != '1') {
				return nil, fmt.Errorf("invalid escape in token %d", i)
			}
			if raw[position+1] == '0' {
				builder.WriteByte('~')
			} else {
				builder.WriteByte('/')
			}
			position++
		}
		token := builder.String()
		if numericAppearance.MatchString(token) && !canonicalIndex.MatchString(token) {
			return nil, fmt.Errorf("non-canonical numeric token")
		}
		tokens[i] = token
	}
	return tokens, nil
}

// Overlaps reports equality or an ancestor/descendant relationship between two token paths.
func Overlaps(left, right []string) bool {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for i := 0; i < limit; i++ {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// Escape encodes one decoded token for safe JSON Pointer metadata.
func Escape(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

// Join renders decoded tokens as their canonical JSON Pointer.
func Join(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	escaped := make([]string, len(tokens))
	for i, token := range tokens {
		escaped[i] = Escape(token)
	}
	return "/" + strings.Join(escaped, "/")
}
