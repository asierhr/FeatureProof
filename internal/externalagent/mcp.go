package externalagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
)

// MCPServer serves the stateless 2026-07-28 tool surface over bounded stdio messages.
type MCPServer struct {
	service *Service
}

// NewMCPServer binds MCP translation to the same application service as CLI.
func NewMCPServer(service *Service) (*MCPServer, error) {
	if service == nil {
		return nil, &AppError{Code: CodeInternal}
	}
	return &MCPServer{service: service}, nil
}

// mcpRequest is the strict JSON-RPC request envelope accepted from stdio.
type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// mcpResponse separates protocol failures from successful tool-call envelopes.
type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

// mcpError carries only standard JSON-RPC code and fixed safe text.
type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// mcpParams represents the shared stateless metadata and tool-call fields.
type mcpParams struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Name            string          `json:"name,omitempty"`
	Arguments       json.RawMessage `json:"arguments,omitempty"`
	Meta            json.RawMessage `json:"_meta,omitempty"`
	ClientInfo      json.RawMessage `json:"clientInfo,omitempty"`
}

// mcpTool translates one application operation into MCP discovery casing.
type mcpTool struct {
	Name         string         `json:"name"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema"`
}

// mcpCallResult carries the application envelope as MCP structured content.
type mcpCallResult struct {
	StructuredContent Envelope         `json:"structuredContent"`
	Content           []map[string]any `json:"content"`
	IsError           bool             `json:"isError,omitempty"`
}

// Serve reads one bounded JSON-RPC object per line and joins workers on shutdown.
func (s *MCPServer) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), maxRequestBytes)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			break
		}
		response := s.Handle(ctx, append([]byte(nil), scanner.Bytes()...))
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.service.deps.RunTimeout)
	defer cancel()
	if err := s.service.Close(closeCtx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return scanner.Err()
}

// Handle validates one stateless request and separates protocol errors from tool failures.
func (s *MCPServer) Handle(ctx context.Context, raw []byte) mcpResponse {
	request := mcpRequest{}
	if err := DecodeStrict(raw, &request); err != nil || request.JSONRPC != "2.0" || !validRPCID(request.ID) || request.Method == "" {
		return mcpResponse{JSONRPC: "2.0", ID: request.ID, Error: &mcpError{Code: -32600, Message: "Invalid Request"}}
	}
	params := mcpParams{}
	if len(request.Params) == 0 || DecodeStrict(request.Params, &params) != nil || params.ProtocolVersion != MCPVersion {
		return mcpResponse{JSONRPC: "2.0", ID: request.ID, Error: &mcpError{Code: -32602, Message: "Invalid params"}}
	}
	switch request.Method {
	case "server/discover":
		return mcpResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"protocolVersion": MCPVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "applicationContractVersion": ContractVersion}}
	case "tools/list":
		capabilities, err := s.service.Capabilities(ctx)
		if err != nil {
			return s.applicationResponse(request.ID, Envelope{ContractVersion: ContractVersion, Error: asAppError(err)})
		}
		tools := make([]mcpTool, 0, len(capabilities.Operations))
		for _, operation := range capabilities.Operations {
			tools = append(tools, mcpTool{Name: "featureproof." + operation.Name, InputSchema: operation.InputSchema, OutputSchema: operation.OutputSchema})
		}
		return mcpResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"tools": tools}}
	case "tools/call":
		if len(params.Arguments) == 0 {
			params.Arguments = json.RawMessage(`{}`)
		}
		operation, ok := toolOperation(params.Name)
		if !ok {
			return mcpResponse{JSONRPC: "2.0", ID: request.ID, Error: &mcpError{Code: -32601, Message: "Method not found"}}
		}
		return s.applicationResponse(request.ID, s.service.Invoke(ctx, operation, params.Arguments))
	default:
		return mcpResponse{JSONRPC: "2.0", ID: request.ID, Error: &mcpError{Code: -32601, Message: "Method not found"}}
	}
}

// applicationResponse places the identical application envelope in structured and text content.
func (s *MCPServer) applicationResponse(id json.RawMessage, envelope Envelope) mcpResponse {
	raw, err := CanonicalJSON(envelope)
	if err != nil {
		envelope = Envelope{ContractVersion: ContractVersion, Error: &AppError{Code: CodeInternal}}
		raw, _ = CanonicalJSON(envelope)
	}
	return mcpResponse{JSONRPC: "2.0", ID: id, Result: mcpCallResult{StructuredContent: envelope, Content: []map[string]any{{"type": "text", "text": string(raw)}}, IsError: envelope.Error != nil}}
}

// toolOperation accepts only the five advertised FeatureProof names.
func toolOperation(name string) (string, bool) {
	switch name {
	case "featureproof.capabilities":
		return "capabilities", true
	case "featureproof.submit":
		return "submit", true
	case "featureproof.status":
		return "status", true
	case "featureproof.result":
		return "result", true
	case "featureproof.cancel":
		return "cancel", true
	case "featureproof.correction_submit":
		return "correction_submit", true
	case "featureproof.correction_status":
		return "correction_status", true
	case "featureproof.correction_result":
		return "correction_result", true
	case "featureproof.correction_cancel":
		return "correction_cancel", true
	default:
		return "", false
	}
}

// validRPCID restricts echoable IDs to JSON-RPC scalar forms.
func validRPCID(raw json.RawMessage) bool {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return false
	}
	switch value.(type) {
	case string, json.Number:
		return true
	}
	return false
}

// asAppError closes unexpected identity or capability errors before serialization.
func asAppError(err error) *AppError {
	var app *AppError
	if errors.As(err, &app) {
		return app
	}
	return &AppError{Code: CodeInternal}
}
