package contractreview

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// HTTPAuthenticator derives one trusted principal from request authentication material.
type HTTPAuthenticator interface {
	Authenticate(*http.Request) (string, error)
}

// BearerAuthenticator validates one composition-owned bearer secret without exposing it downstream.
type BearerAuthenticator struct {
	Token     []byte
	Principal string
}

// Authenticate performs constant-time token comparison and returns only the configured principal.
func (a BearerAuthenticator) Authenticate(request *http.Request) (string, error) {
	provided := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	if len(a.Token) < 16 || !safeReference(a.Principal) || len(provided) != len(a.Token) || subtle.ConstantTimeCompare([]byte(provided), a.Token) != 1 {
		return "", typedError(CategoryUnauthorized, "http.authentication", nil)
	}
	return a.Principal, nil
}

// HTTPHandler registers the complete review lifecycle on one authenticated, composition-owned API adapter.
type HTTPHandler struct {
	API     APIAdapter
	MaxBody int64
	Auth    HTTPAuthenticator
}

// Handler returns a closed HTTP mux whose routes cannot select authority or credentials.
func (h HTTPHandler) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/contract-reviews", h.create)
	mux.HandleFunc("GET /v1/contract-reviews/{review}", h.show)
	mux.HandleFunc("POST /v1/contract-reviews/{review}/decisions", h.decide)
	mux.HandleFunc("POST /v1/contract-reviews/{review}/exports", h.export)
	return mux
}

// create decodes one bounded creation request and delegates to the shared application authority.
func (h HTTPHandler) create(writer http.ResponseWriter, request *http.Request) {
	api, ok := h.authenticated(writer, request)
	if !ok {
		return
	}
	var input CreateInput
	if !h.decode(writer, request, &input) {
		return
	}
	value, err := api.Create(request.Context(), input)
	h.write(writer, value, err)
}

// show injects the path identity into the closed shared read input.
func (h HTTPHandler) show(writer http.ResponseWriter, request *http.Request) {
	api, ok := h.authenticated(writer, request)
	if !ok {
		return
	}
	value, err := api.Show(request.Context(), ShowInput{Version: VersionV1, ReviewID: request.PathValue("review")})
	h.write(writer, value, err)
}

// decide rejects body/path disagreement before applying one CAS decision.
func (h HTTPHandler) decide(writer http.ResponseWriter, request *http.Request) {
	api, ok := h.authenticated(writer, request)
	if !ok {
		return
	}
	var input DecideInput
	if !h.decode(writer, request, &input) {
		return
	}
	if input.ReviewID != request.PathValue("review") {
		h.write(writer, ReviewView{}, typedError(CategoryInvalidInput, "http.review", nil))
		return
	}
	value, err := api.Decide(request.Context(), input)
	h.write(writer, value, err)
}

// export rejects body/path disagreement before guarded publication.
func (h HTTPHandler) export(writer http.ResponseWriter, request *http.Request) {
	api, ok := h.authenticated(writer, request)
	if !ok {
		return
	}
	var input ExportInput
	if !h.decode(writer, request, &input) {
		return
	}
	if input.ReviewID != request.PathValue("review") {
		h.write(writer, ExportReceipt{}, typedError(CategoryInvalidInput, "http.review", nil))
		return
	}
	value, err := api.Export(request.Context(), input)
	h.write(writer, value, err)
}

// authenticated creates an isolated request adapter whose principal came from the authentication boundary.
func (h HTTPHandler) authenticated(writer http.ResponseWriter, request *http.Request) (APIAdapter, bool) {
	if h.Auth == nil {
		h.write(writer, nil, typedError(CategoryUnavailable, "http.authentication", nil))
		return APIAdapter{}, false
	}
	principal, err := h.Auth.Authenticate(request)
	if err != nil {
		h.write(writer, nil, typedError(CategoryUnauthorized, "http.authentication", nil))
		return APIAdapter{}, false
	}
	api := h.API
	api.Config.PrincipalID = principal
	return api, true
}

// decode enforces the configured body limit and the transport's closed JSON schema.
func (h HTTPHandler) decode(writer http.ResponseWriter, request *http.Request, target any) bool {
	if h.MaxBody <= 0 {
		h.write(writer, nil, typedError(CategoryUnavailable, "http.config", nil))
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, h.MaxBody))
	if err != nil || decodeClosed(raw, target) != nil {
		h.write(writer, nil, typedError(CategoryInvalidInput, "http.input", nil))
		return false
	}
	return true
}

// write emits only the shared safe projection and a stable category-derived status.
func (h HTTPHandler) write(writer http.ResponseWriter, value any, err error) {
	writer.Header().Set("Content-Type", "application/json")
	if err != nil {
		status := http.StatusInternalServerError
		switch errorCategoryOf(err) {
		case CategoryInvalidInput:
			status = http.StatusBadRequest
		case CategoryUnauthorized:
			status = http.StatusForbidden
		case CategoryConflict, CategoryInvalidated, CategoryIncompatibleBaseline, CategoryTargetExists:
			status = http.StatusConflict
		case CategoryLimitExceeded:
			status = http.StatusRequestEntityTooLarge
		case CategoryCancelled:
			status = http.StatusRequestTimeout
		case CategoryUnavailable:
			status = http.StatusServiceUnavailable
		}
		writer.WriteHeader(status)
		_ = json.NewEncoder(writer).Encode(map[string]any{"version": VersionV1, "error": errorCategoryOf(err)})
		return
	}
	_ = json.NewEncoder(writer).Encode(value)
}

// errorCategoryOf extracts only the bounded public failure category.
func errorCategoryOf(err error) ErrorCategory {
	if typed, ok := err.(*Error); ok {
		return typed.Category
	}
	return CategoryInternal
}

// MCPServer exposes the complete review lifecycle over bounded JSON-RPC lines.
type MCPServer struct {
	Adapter MCPAdapter
	MaxLine int
}

// mcpRequest is the minimal closed JSON-RPC request accepted by the review server.
type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"params"`
}

// Serve processes newline-delimited initialize, tools/list and tools/call requests until EOF or cancellation.
func (m MCPServer) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	if m.MaxLine <= 0 || m.Adapter.API.Service == nil {
		return typedError(CategoryUnavailable, "mcp.config", nil)
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 1024), m.MaxLine)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		if err := cancellation(ctx, "mcp.serve"); err != nil {
			return err
		}
		var request mcpRequest
		if decodeClosed(scanner.Bytes(), &request) != nil || request.JSONRPC != "2.0" {
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32600}})
			continue
		}
		switch request.Method {
		case "initialize":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}}})
		case "tools/list":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": reviewTools()}})
		case "tools/call":
			value, err := m.Adapter.Call(ctx, request.Params.Name, request.Params.Arguments)
			if err != nil {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": string(errorCategoryOf(err))}}}})
				continue
			}
			raw, _ := json.Marshal(value)
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": string(raw)}}}})
		default:
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601}})
		}
	}
	if err := scanner.Err(); err != nil {
		return typedError(CategoryLimitExceeded, "mcp.line", err)
	}
	return nil
}

// reviewTools advertises exactly the lifecycle operations routed by MCPAdapter.
func reviewTools() []map[string]any {
	stringProperty := map[string]any{"type": "string"}
	integerProperty := map[string]any{"type": "integer", "minimum": 0}
	booleanProperty := map[string]any{"type": "boolean"}
	closed := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	return []map[string]any{
		{"name": "contract_review_create_v1", "inputSchema": closed(map[string]any{"version": stringProperty, "candidate_ref": stringProperty, "baseline_ref": stringProperty, "idempotency_key": stringProperty}, "version", "candidate_ref", "baseline_ref", "idempotency_key")},
		{"name": "contract_review_show_v1", "inputSchema": closed(map[string]any{"version": stringProperty, "review_id": stringProperty}, "version", "review_id")},
		{"name": "contract_review_decide_v1", "inputSchema": closed(map[string]any{"version": stringProperty, "review_id": stringProperty, "idempotency_key": stringProperty, "expected_revision": integerProperty, "whole": map[string]any{"type": "string", "enum": []string{string(WholeAccept), string(WholeReject)}}, "items": map[string]any{"type": "array", "items": closed(map[string]any{"item_id": stringProperty, "value": map[string]any{"type": "string", "enum": []string{string(DecisionAccept), string(DecisionReject)}}}, "item_id", "value")}}, "version", "review_id", "idempotency_key", "expected_revision")},
		{"name": "contract_review_export_v1", "inputSchema": closed(map[string]any{"version": stringProperty, "review_id": stringProperty, "idempotency_key": stringProperty, "target": stringProperty, "expected_existing_digest": stringProperty, "expected_revision": integerProperty, "replace": booleanProperty}, "version", "review_id", "idempotency_key", "target", "expected_revision")},
	}
}
