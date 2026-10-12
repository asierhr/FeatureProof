package assertion

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/asierhr/featureProof/internal/planning"
)

var numericAppearance = regexp.MustCompile(`^[+-]?[0-9]+$`)
var canonicalIndex = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

// parsePathContext validates tokens while observing cancellation during lexical traversal.
func parsePathContext(ctx context.Context, path string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("non-root path must begin with slash")
	}
	rawTokens := strings.Split(path[1:], "/")
	tokens := make([]string, len(rawTokens))
	for i, raw := range rawTokens {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var builder strings.Builder
		for position := 0; position < len(raw); position++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
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
			return nil, fmt.Errorf("non-canonical numeric token %q", token)
		}
		tokens[i] = token
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return tokens, nil
}

// resolvePathContext walks one token per cancellation boundary without coercing runtime values.
func resolvePathContext(ctx context.Context, output any, tokens []string) (planning.AssertionValue, error) {
	selected := output
	for _, token := range tokens {
		if err := ctx.Err(); err != nil {
			return planning.AssertionValue{}, err
		}
		switch current := selected.(type) {
		case map[string]any:
			var exists bool
			selected, exists = current[token]
			if !exists {
				return planning.AssertionValue{}, nil
			}
		case []any:
			if !canonicalIndex.MatchString(token) {
				return planning.AssertionValue{}, nil
			}
			index, err := strconv.ParseUint(token, 10, 63)
			if err != nil || index >= uint64(len(current)) {
				return planning.AssertionValue{}, nil
			}
			selected = current[index]
		default:
			return planning.AssertionValue{}, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return planning.AssertionValue{}, err
	}
	return planning.AssertionValue{Present: true, Value: selected}, nil
}
