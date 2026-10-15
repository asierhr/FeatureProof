package agentplanning

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/asierhr/featureProof/internal/planning"
)

// maxModelValueDepth caps recursive model-owned structures even with an extremely large work budget.
const maxModelValueDepth = 128

// validationWork accounts for every deterministic visit under one shared, overflow-safe budget.
type validationWork struct {
	ctx  context.Context
	max  int
	used int
}

// newValidationWork starts a candidate-wide validation budget.
func newValidationWork(ctx context.Context, maximum int) *validationWork {
	return &validationWork{ctx: ctx, max: maximum}
}

// visit charges concrete visits without ever overflowing the running total.
func (w *validationWork) visit(count int, stage string) error {
	if err := w.ctx.Err(); err != nil {
		return contextError(stage, err)
	}
	if count < 0 || w.used > w.max || count > w.max-w.used {
		return typedError(CategoryLimitExceeded, stage, "work", nil)
	}
	w.used += count
	return nil
}

// ensureBreadth rejects a collection that cannot fit before allocating traversal scratch space.
func (w *validationWork) ensureBreadth(count int, stage string) error {
	if err := w.ctx.Err(); err != nil {
		return contextError(stage, err)
	}
	if count < 0 || w.used > w.max || count > w.max-w.used {
		return typedError(CategoryLimitExceeded, stage, "work", nil)
	}
	return nil
}

// checkDepth caps recursion independently from very large configured work budgets.
func (w *validationWork) checkDepth(depth int, stage string) error {
	if err := w.visit(1, stage); err != nil {
		return err
	}
	if depth > maxModelValueDepth {
		return typedError(CategoryLimitExceeded, stage, "depth", nil)
	}
	return nil
}

// scanInputMap deterministically validates every input key, node, array member and reference path.
func scanInputMap(w *validationWork, input map[string]planning.InputValue, depth int, stage string) error {
	if err := w.checkDepth(depth, stage); err != nil {
		return err
	}
	if err := w.ensureBreadth(len(input), stage); err != nil {
		return err
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := w.visit(1, stage); err != nil {
			return err
		}
		if forbiddenField(key) {
			return typedError(CategorySemanticRejection, stage, "forbidden_value", nil)
		}
		if err := scanInputValue(w, input[key], depth+1, stage); err != nil {
			return err
		}
	}
	return nil
}

// scanInputValue inspects one complete planning input union before any collaborator sees it.
func scanInputValue(w *validationWork, value planning.InputValue, depth int, stage string) error {
	if err := w.checkDepth(depth, stage); err != nil {
		return err
	}
	if value.Literal != nil {
		if err := w.visit(1, stage); err != nil {
			return err
		}
		if text, ok := value.Literal.Value.(string); ok && forbiddenText(text) {
			return typedError(CategorySemanticRejection, stage, "forbidden_value", nil)
		}
	}
	if value.Object != nil {
		if err := scanInputMap(w, value.Object, depth+1, stage); err != nil {
			return err
		}
	}
	if err := w.ensureBreadth(len(value.Array), stage); err != nil {
		return err
	}
	for i := range value.Array {
		if err := scanInputValue(w, value.Array[i], depth+1, stage); err != nil {
			return err
		}
	}
	if value.Reference != nil {
		if err := w.visit(1, stage); err != nil {
			return err
		}
		for range value.Reference.Path {
			if err := w.visit(1, stage); err != nil {
				return err
			}
		}
	}
	return nil
}

// scanStructuredMap deterministically validates opaque assertion and path-binding values.
func scanStructuredMap(w *validationWork, values map[string]any, depth int, stage string) error {
	if err := w.checkDepth(depth, stage); err != nil {
		return err
	}
	if err := w.ensureBreadth(len(values), stage); err != nil {
		return err
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := w.visit(1, stage); err != nil {
			return err
		}
		if forbiddenField(key) {
			return typedError(CategorySemanticRejection, stage, "forbidden_value", nil)
		}
		if err := scanStructuredValue(w, values[key], depth+1, stage); err != nil {
			return err
		}
	}
	return nil
}

// scanStructuredValue walks JSON-compatible opaque model values with depth and work bounds.
func scanStructuredValue(w *validationWork, value any, depth int, stage string) error {
	if err := w.checkDepth(depth, stage); err != nil {
		return err
	}
	switch typed := value.(type) {
	case string:
		if forbiddenText(typed) {
			return typedError(CategorySemanticRejection, stage, "forbidden_value", nil)
		}
	case map[string]any:
		return scanStructuredMap(w, typed, depth+1, stage)
	case []any:
		if err := w.ensureBreadth(len(typed), stage); err != nil {
			return err
		}
		for i := range typed {
			if err := scanStructuredValue(w, typed[i], depth+1, stage); err != nil {
				return err
			}
		}
	case nil, bool, json.Number, float64:
		return nil
	default:
		return typedError(CategorySemanticRejection, stage, "value_type", nil)
	}
	return nil
}

// forbiddenField identifies capability, command, endpoint and credential-bearing model fields.
func forbiddenField(field string) bool {
	normalized := strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(strings.ToLower(field))
	for _, forbidden := range []string{"url", "uri", "authorization", "cookie", "credential", "password", "passwd", "token", "secret", "command", "shell", "exec", "tool", "capability"} {
		if strings.Contains(normalized, forbidden) {
			return true
		}
	}
	return false
}

// forbiddenText rejects absolute URL values after trimming, including userinfo-bearing URLs.
func forbiddenText(value string) bool {
	trimmed := strings.TrimSpace(value)
	parsed, err := url.Parse(trimmed)
	if err == nil && parsed.IsAbs() && parsed.Scheme != "" {
		return true
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "://") {
		return true
	}
	for _, prefix := range []string{"sh -c ", "bash -c ", "cmd /c ", "cmd.exe /c ", "powershell ", "pwsh ", "curl ", "wget ", "sudo ", "rm -", "python -c ", "node -e "} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// forbiddenInput reports whether an isolated input tree violates the same bounded structural policy used by generation.
func forbiddenInput(input map[string]planning.InputValue) bool {
	maximum := int(^uint(0) >> 1)
	return scanInputMap(newValidationWork(context.Background(), maximum), input, 0, "semantic") != nil
}
