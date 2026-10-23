package projectmemory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// CredentialPrincipalResolver authenticates transport credentials into one server-owned principal identity.
type CredentialPrincipalResolver interface {
	ResolvePrincipal(context.Context, string) (string, error)
}

// CLIAdapter exposes the closed memory grammar to a locally composed authenticated principal.
type CLIAdapter struct {
	Surface   Surface
	Principal string
}

// Run decodes one exact operation payload and writes the shared safe response.
func (adapter CLIAdapter) Run(ctx context.Context, operation SurfaceOperation, raw []byte, output io.Writer) int {
	request, err := decodeSurfacePayload(operation, raw)
	if err != nil || adapter.Principal == "" || output == nil {
		return 2
	}
	request.Principal = adapter.Principal
	response := adapter.Surface.Invoke(ctx, request)
	if json.NewEncoder(output).Encode(response) != nil || response.Error != nil {
		return 1
	}
	return 0
}

// APIHandler authenticates HTTP credentials before dispatching the shared memory operation grammar.
type APIHandler struct {
	Surface Surface
	Auth    CredentialPrincipalResolver
}

// ServeHTTP accepts a bounded JSON operation and never derives authority from request fields.
func (handler APIHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if handler.Auth == nil || request == nil {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	principal, err := handler.Auth.ResolvePrincipal(request.Context(), request.Header.Get("Authorization"))
	if err != nil {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, int64(DefaultLimits().MaxDocumentBytes)+1))
	operation := SurfaceOperation(request.Header.Get("FeatureProof-Operation"))
	surfaceRequest, decodeErr := decodeSurfacePayload(operation, raw)
	if err != nil || decodeErr != nil || len(raw) > DefaultLimits().MaxDocumentBytes {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	surfaceRequest.Principal = principal
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(handler.Surface.Invoke(request.Context(), surfaceRequest))
}

// MCPMemoryRequest is the strict transport envelope accepted by the real MCP adapter.
type MCPMemoryRequest struct {
	Credential string
	Operation  SurfaceOperation
	Arguments  json.RawMessage
}

// MCPAdapter authenticates an MCP connection identity before decoding any memory arguments.
type MCPAdapter struct {
	Surface Surface
	Auth    CredentialPrincipalResolver
}

// MCPServer is the productive bounded JSON-RPC entrypoint for memory tools.
type MCPServer struct {
	Adapter MCPAdapter
	MaxLine int
}

// memoryRPCRequest is the closed JSON-RPC envelope accepted on the MCP stream.
type memoryRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  struct {
		Credential string           `json:"credential"`
		Name       SurfaceOperation `json:"name"`
		Arguments  json.RawMessage  `json:"arguments"`
	} `json:"params"`
}

// Serve owns the MCP line loop and dispatches tool calls through the composed adapter.
func (server MCPServer) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	if server.MaxLine <= 0 || server.Adapter.Auth == nil || output == nil {
		return fieldError("unavailable", "mcp.serve", "config", nil)
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 1024), server.MaxLine)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return fieldError("cancelled", "mcp.serve", "context", err)
		}
		var request memoryRPCRequest
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || request.JSONRPC != "2.0" {
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32600}})
			continue
		}
		switch request.Method {
		case "initialize":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}}})
		case "tools/list":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": memoryTools()}})
		case "tools/call":
			response := server.Adapter.Handle(ctx, MCPMemoryRequest{Credential: request.Params.Credential, Operation: request.Params.Name, Arguments: request.Params.Arguments})
			raw, _ := json.Marshal(response)
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"isError": response.Error != nil, "content": []any{map[string]any{"type": "text", "text": string(raw)}}}})
		default:
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601}})
		}
	}
	if err := scanner.Err(); err != nil {
		return fieldError("limit", "mcp.serve", "line", err)
	}
	return nil
}

// memoryTools advertises the closed operation names consumed by the common decoder.
func memoryTools() []map[string]any {
	return []map[string]any{{"name": string(OperationPropose)}, {"name": string(OperationQuery)}, {"name": string(OperationTransition)}}
}

// Handle returns the same safe DTO used by CLI and API after trusted principal mapping.
func (adapter MCPAdapter) Handle(ctx context.Context, request MCPMemoryRequest) SurfaceResponse {
	if adapter.Auth == nil {
		return errorResponse(fieldError("policy_denied", "mcp", "principal", nil))
	}
	principal, err := adapter.Auth.ResolvePrincipal(ctx, request.Credential)
	if err != nil {
		return errorResponse(fieldError("policy_denied", "mcp", "principal", err))
	}
	surfaceRequest, err := decodeSurfacePayload(request.Operation, request.Arguments)
	if err != nil {
		return errorResponse(err)
	}
	surfaceRequest.Principal = principal
	return adapter.Surface.Invoke(ctx, surfaceRequest)
}

// VerifiedGitHubActor carries identity and repository coordinates already verified from a signed webhook snapshot.
type VerifiedGitHubActor struct {
	ActorID, RepositoryID, Action string
}

// GitHubPrincipalResolver maps only verified webhook coordinates to an allowlisted principal.
type GitHubPrincipalResolver interface {
	ResolveGitHubPrincipal(context.Context, VerifiedGitHubActor, SurfaceOperation, State) (string, error)
}

// GitHubAdapter dispatches structured operations without interpreting comment prose as authority.
type GitHubAdapter struct {
	Surface Surface
	Auth    GitHubPrincipalResolver
}

// Submit resolves the verified actor and repository before invoking a mutation or review query.
func (adapter GitHubAdapter) Submit(ctx context.Context, actor VerifiedGitHubActor, operation SurfaceOperation, raw []byte) SurfaceResponse {
	if adapter.Auth == nil {
		return errorResponse(fieldError("policy_denied", "github", "principal", nil))
	}
	request, err := decodeSurfacePayload(operation, raw)
	if err != nil {
		return errorResponse(err)
	}
	target := State("")
	if request.Transition != nil {
		target = request.Transition.Target
	}
	principal, err := adapter.Auth.ResolveGitHubPrincipal(ctx, actor, operation, target)
	if err != nil {
		return errorResponse(fieldError("policy_denied", "github", "principal", err))
	}
	request.Principal = principal
	return adapter.Surface.Invoke(ctx, request)
}

// decodeSurfacePayload strictly decodes the operation-specific union while excluding principal and authority claims.
func decodeSurfacePayload(operation SurfaceOperation, raw []byte) (SurfaceRequest, error) {
	if len(raw) == 0 || len(raw) > DefaultLimits().MaxDocumentBytes {
		return SurfaceRequest{}, fieldError("invalid_input", "surface.decode", "$", nil)
	}
	request := SurfaceRequest{Operation: operation}
	var target any
	switch operation {
	case OperationPropose:
		request.Proposal = &ProposalCommand{}
		target = request.Proposal
	case OperationQuery:
		request.Query = &QueryRequest{}
		target = request.Query
	case OperationTransition:
		request.Transition = &TransitionCommand{}
		target = request.Transition
	default:
		return SurfaceRequest{}, fieldError("invalid_input", "surface.decode", "operation", nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return SurfaceRequest{}, fieldError("invalid_input", "surface.decode", "$", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return SurfaceRequest{}, fieldError("invalid_input", "surface.decode", "$", err)
	}
	return request, nil
}
