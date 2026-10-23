package fixture

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/asierhr/featureProof/internal/harness"
	httptool "github.com/asierhr/featureProof/internal/tools/http"
)

// EvidenceSink sanitizes and durably confirms bounded HTTP output before captures may inspect it.
type EvidenceSink interface {
	Confirm(context.Context, string, string, map[string]any) (map[string]any, string, error)
}

// HTTPAdapterConfig contains trusted adapter wiring and an optional contract-approved idempotency header.
type HTTPAdapterConfig struct {
	Tool              *httptool.Tool
	Evidence          EvidenceSink
	IdempotencyHeader string
}

// HTTPAdapter executes fixture requests through the existing target-scoped authenticated HTTP capability.
type HTTPAdapter struct {
	tool              *httptool.Tool
	evidence          EvidenceSink
	idempotencyHeader string
}

// NewHTTPAdapter validates trusted composition before any fixture request can reach DNS or credentials.
func NewHTTPAdapter(config HTTPAdapterConfig) (*HTTPAdapter, error) {
	if config.Tool == nil || config.Evidence == nil || config.IdempotencyHeader != "" && !safeHeaderName(config.IdempotencyHeader) {
		return nil, &Error{Category: CategoryInvalid, Phase: "http_composition"}
	}
	return &HTTPAdapter{tool: config.Tool, evidence: config.Evidence, idempotencyHeader: config.IdempotencyHeader}, nil
}

// Execute expands only declared path parameters, adds a system-owned idempotency key, and confirms sanitized evidence.
func (a *HTTPAdapter) Execute(ctx context.Context, request HTTPRequest) (HTTPResult, error) {
	if a == nil || a.tool == nil || request.Method == "" || request.PathTemplate == "" || request.BindingDigest == "" || request.IdempotencyKey == "" || request.Attempt <= 0 {
		return HTTPResult{}, &ExecutionError{Category: CategoryDenied}
	}
	path, err := expandPath(request.PathTemplate, request.PathParameters)
	if err != nil {
		return HTTPResult{}, &ExecutionError{Category: CategoryInvalid, Cause: err}
	}
	query, err := httpQuery(request.Query)
	if err != nil {
		return HTTPResult{}, &ExecutionError{Category: CategoryInvalid, Cause: err}
	}
	input := map[string]any{"method": request.Method, "path": path}
	if len(query) > 0 {
		input["query"] = query
	}
	if request.Body != nil {
		body, cloneErr := cloneJSON(request.Body)
		if cloneErr != nil {
			return HTTPResult{}, &ExecutionError{Category: CategoryInvalid, Cause: cloneErr}
		}
		input["body"] = body
	}
	if request.IdempotencyHeader != "" {
		if a.idempotencyHeader == "" || !strings.EqualFold(a.idempotencyHeader, request.IdempotencyHeader) {
			return HTTPResult{}, &ExecutionError{Category: CategoryDenied}
		}
		input["headers"] = map[string][]string{request.IdempotencyHeader: {request.IdempotencyKey}}
	}
	binding := request.Identity
	toolResult, executeErr := a.tool.Execute(ctx, harness.ToolCall{ID: request.FixtureID + ":" + request.Phase, Tool: "http.request", RunID: request.RunID, Attempt: request.Attempt, Input: input, IdempotencyKey: request.IdempotencyKey, IdentityBinding: &binding})
	if executeErr != nil {
		return HTTPResult{}, classifyHTTPError(ctx, executeErr)
	}
	safe, evidenceID, confirmErr := a.evidence.Confirm(ctx, request.RunID, request.BindingDigest, toolResult.Output)
	if confirmErr != nil {
		return HTTPResult{CrossedNetwork: true}, &ExecutionError{Category: CategoryInternal, CrossedNetwork: true, Cause: confirmErr}
	}
	status, ok := integerStatus(safe["status"])
	if !ok {
		return HTTPResult{CrossedNetwork: true, EvidenceID: evidenceID}, &ExecutionError{Category: CategoryInternal, CrossedNetwork: true}
	}
	truncated, _ := safe["body_truncated"].(bool)
	body, cloneErr := cloneJSON(safe["body"])
	if cloneErr != nil {
		return HTTPResult{CrossedNetwork: true, EvidenceID: evidenceID}, &ExecutionError{Category: CategoryInternal, CrossedNetwork: true, Cause: cloneErr}
	}
	return HTTPResult{Status: status, Body: body, EvidenceID: evidenceID, CrossedNetwork: true, Truncated: truncated}, nil
}

// expandPath replaces each whole template segment exactly once and rejects unused or missing parameters.
func expandPath(template string, parameters map[string]any) (string, error) {
	segments := strings.Split(strings.TrimPrefix(template, "/"), "/")
	used := map[string]bool{}
	for i, segment := range segments {
		if len(segment) >= 3 && segment[0] == '{' && segment[len(segment)-1] == '}' {
			name := segment[1 : len(segment)-1]
			value, ok := parameters[name].(string)
			if !ok || value == "" {
				return "", errors.New("path parameter unavailable")
			}
			segments[i] = url.PathEscape(value)
			used[name] = true
		}
	}
	if len(used) != len(parameters) {
		return "", errors.New("unused path parameter")
	}
	return "/" + strings.Join(segments, "/"), nil
}

// httpQuery converts scalar JSON values into the existing tool's explicit list representation without coercing containers.
func httpQuery(values map[string]any) (map[string][]string, error) {
	out := map[string][]string{}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		switch value := values[key].(type) {
		case string:
			out[key] = []string{value}
		case bool:
			out[key] = []string{strconv.FormatBool(value)}
		case float64:
			out[key] = []string{strconv.FormatFloat(value, 'g', -1, 64)}
		case nil:
			out[key] = []string{"null"}
		default:
			return nil, errors.New("query value is not scalar")
		}
	}
	return out, nil
}

// classifyHTTPError distinguishes safe pre-network rejection from uncertain transport failure and preserves retry hints.
func classifyHTTPError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return &ExecutionError{Category: CategoryCancelled, Cause: ctx.Err()}
	}
	var input *httptool.InputError
	if errors.As(err, &input) {
		return &ExecutionError{Category: CategoryInvalid, Cause: input}
	}
	var operation *httptool.OperationError
	if errors.As(err, &operation) {
		crossed := operation.Category != "encode" && operation.Category != "identity_unavailable" && operation.Category != "destination_denied" && operation.Category != "dns"
		return &ExecutionError{Category: CategoryUnavailable, CrossedNetwork: crossed, Transient: harness.IsTransient(err), Cause: operation}
	}
	return &ExecutionError{Category: CategoryInternal, Cause: err}
}

// integerStatus accepts the integer representations produced by bounded JSON decoding without rounding.
func integerStatus(value any) (int, bool) {
	switch status := value.(type) {
	case int:
		return status, status >= 100 && status <= 599
	case float64:
		converted := int(status)
		return converted, converted >= 100 && converted <= 599 && float64(converted) == status
	case json.Number:
		value, err := status.Int64()
		return int(value), err == nil && value >= 100 && value <= 599
	}
	return 0, false
}

// safeHeaderName validates the trusted idempotency header as an ASCII token and excludes authentication/framing names.
func safeHeaderName(value string) bool {
	if value == "" {
		return false
	}
	lower := strings.ToLower(value)
	switch lower {
	case "authorization", "cookie", "set-cookie", "proxy-authorization", "host", "content-length", "connection", "transfer-encoding":
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", character)) {
			return false
		}
	}
	return true
}
