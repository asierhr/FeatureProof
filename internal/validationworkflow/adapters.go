package validationworkflow

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/githubpr"
)

// MCPProtocolVersion identifies the bounded JSON-RPC protocol understood by workflow MCP.
const MCPProtocolVersion = "2026-07-28"

// mcpMaxRequestBytes bounds each JSON-RPC line before decoding untrusted input.
const mcpMaxRequestBytes = 64 << 10

// GeneratorAdapter delegates generation to the existing all-or-nothing planning validator.
type GeneratorAdapter struct{ Generator *agentplanning.Generator }

// Generate preserves PreparedPlan authority and returns only safe accounting owned by composition.
func (a GeneratorAdapter) Generate(ctx context.Context, snapshot AdmissionSnapshot) (agentplanning.Result, ModelUsage, error) {
	if a.Generator == nil {
		return agentplanning.Result{}, ModelUsage{}, &Error{Code: CodeInternal, Operation: "generator"}
	}
	result, err := a.Generator.Generate(ctx, snapshot.Generation)
	return result, ModelUsage{Calls: 1}, err
}

// PreparationAdapter restores private plans through the same assertion and generation-policy gates.
type PreparationAdapter struct {
	Assertions agentplanning.AssertionValidator
	Policy     agentplanning.GenerationPolicy
}

// Restore delegates restart reconstruction to agentplanning's full validation pipeline.
func (a PreparationAdapter) Restore(ctx context.Context, snapshot AdmissionSnapshot, private agentplanning.PreparedSnapshot) (agentplanning.PreparedPlan, error) {
	return agentplanning.RestorePrepared(ctx, snapshot.Generation, private, a.Assertions, a.Policy)
}

// CLI translates workflow subcommands into the shared Service without owning orchestration rules.
type CLI struct {
	Service        *Service
	Principal      string
	AuthProfiles   map[string]string
	DirectTargets  map[string]DirectTarget
	PreviewTargets map[string]PreviewTarget
}

// Run parses one exact workflow command and writes a canonical safe JSON projection.
func (c CLI) Run(ctx context.Context, args []string, output io.Writer) int {
	if c.Service == nil || !safeID.MatchString(c.Principal) || len(args) == 0 || output == nil {
		return 2
	}
	var value any
	var err error
	switch args[0] {
	case "start":
		command, parseErr := parseStartCLI(args[1:], c.AuthProfiles, c.DirectTargets, c.PreviewTargets)
		if parseErr != nil {
			return 2
		}
		command.Principal = c.Principal
		value, err = c.Service.Start(ctx, command)
	case "status":
		if len(args) != 2 {
			return 2
		}
		value, err = c.Service.Status(ctx, args[1])
	case "view":
		if len(args) != 2 {
			return 2
		}
		value, err = c.Service.ApprovalView(ctx, args[1])
	case "approve", "reject":
		if len(args) != 4 {
			return 2
		}
		revision, ok := parseUint(args[2])
		if !ok {
			return 2
		}
		action := DecisionApprove
		if args[0] == "reject" {
			action = DecisionReject
		}
		value, err = c.Service.Decide(ctx, DecisionCommand{Principal: c.Principal, Handle: args[1], Revision: revision, DecisionID: args[3], Action: action})
	case "cancel":
		if len(args) != 2 {
			return 2
		}
		value, err = c.Service.Cancel(ctx, c.Principal, args[1])
	case "result":
		if len(args) != 2 {
			return 2
		}
		value, err = c.Service.Result(ctx, args[1])
	default:
		return 2
	}
	if err != nil {
		value = struct {
			ContractVersion string           `json:"contract_version"`
			Error           ErrorCode        `json:"error"`
			Decision        *ContextDecision `json:"decision,omitempty"`
		}{ContractVersion, errorCode(err), errorDecision(err)}
	}
	raw, marshalErr := json.Marshal(value)
	if marshalErr != nil {
		return 3
	}
	_, _ = output.Write(append(raw, '\n'))
	if err != nil {
		return 3
	}
	return 0
}

// parseStartCLI requires every explicit workflow selection and resolves target aliases from trusted catalogs.
func parseStartCLI(args []string, profiles map[string]string, direct map[string]DirectTarget, previews map[string]PreviewTarget) (StartCommand, error) {
	values := map[string]string{}
	allowed := map[string]bool{"--spec": true, "--contract": true, "--provider": true, "--model": true, "--profile": true, "--runtime-profile": true, "--repository": true, "--revision": true, "--approval": true, "--idempotency-key": true, "--target": true, "--preview": true}
	for index := 0; index < len(args); index += 2 {
		if index+1 >= len(args) || !allowed[args[index]] || args[index+1] == "" || values[args[index]] != "" {
			return StartCommand{}, &Error{Code: CodeInvalid, Operation: "cli_start"}
		}
		values[args[index]] = args[index+1]
	}
	for _, name := range []string{"--spec", "--contract", "--provider", "--model", "--profile", "--runtime-profile", "--repository", "--revision", "--approval", "--idempotency-key"} {
		if values[name] == "" {
			return StartCommand{}, &Error{Code: CodeInvalid, Operation: "cli_start"}
		}
	}
	compositionRef, profileOK := profiles[values["--profile"]]
	if !profileOK || len(compositionRef) != 64 {
		return StartCommand{}, &Error{Code: CodeUnauthorized, Operation: "cli_profile"}
	}
	command := StartCommand{SpecificationRef: values["--spec"], ContractRef: values["--contract"], Provider: values["--provider"], Model: values["--model"], CompositionRef: compositionRef, RuntimeProfile: values["--runtime-profile"], RepositoryID: values["--repository"], Revision: values["--revision"], Approval: ApprovalMode(values["--approval"]), IdempotencyKey: values["--idempotency-key"]}
	directTarget, directOK := direct[values["--target"]]
	previewTarget, previewOK := previews[values["--preview"]]
	if directOK == previewOK {
		return StartCommand{}, &Error{Code: CodeInvalid, Operation: "cli_target"}
	}
	if directOK {
		command.Target.Direct = &directTarget
	} else {
		command.Target.Preview = &previewTarget
	}
	return command, nil
}

// MCPServer exposes versioned workflow tools using the same service calls and principal authorization.
type MCPServer struct {
	Service        *Service
	Principal      string
	AuthProfiles   map[string]string
	DirectTargets  map[string]DirectTarget
	PreviewTargets map[string]PreviewTarget
}

// MCPRequest is one bounded transport-neutral MCP workflow call.
type MCPRequest struct {
	Version   string          `json:"version"`
	Operation string          `json:"operation"`
	Arguments json.RawMessage `json:"arguments"`
}

// MCPResponse wraps the same application value or safe error code returned by other adapters.
type MCPResponse struct {
	Version  string           `json:"version"`
	Value    any              `json:"value,omitempty"`
	Error    ErrorCode        `json:"error,omitempty"`
	Decision *ContextDecision `json:"decision,omitempty"`
}

// workflowRPCRequest is the strict JSON-RPC envelope accepted from workflow stdio.
type workflowRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// workflowRPCParams carries protocol discovery or one workflow tool call.
type workflowRPCParams struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Name            string          `json:"name,omitempty"`
	Arguments       json.RawMessage `json:"arguments,omitempty"`
}

// workflowRPCResponse separates JSON-RPC failures from workflow application failures.
type workflowRPCResponse struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id,omitempty"`
	Result  any               `json:"result,omitempty"`
	Error   *workflowRPCError `json:"error,omitempty"`
}

// workflowRPCError contains only fixed protocol-safe error data.
type workflowRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// workflowMCPResult exposes the versioned application response as structured MCP content.
type workflowMCPResult struct {
	StructuredContent MCPResponse      `json:"structuredContent"`
	Content           []map[string]any `json:"content"`
	IsError           bool             `json:"isError,omitempty"`
}

// workflowMCPTool describes one stable workflow operation for MCP discovery.
type workflowMCPTool struct {
	Name        string         `json:"name"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Serve processes bounded JSON-RPC lines and owns no detached worker lifecycle.
func (m MCPServer) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	if m.Service == nil || input == nil || output == nil {
		return &Error{Code: CodeInvalid, Operation: "mcp_serve"}
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), mcpMaxRequestBytes)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := encoder.Encode(m.handleRPC(ctx, append([]byte(nil), scanner.Bytes()...))); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return scanner.Err()
}

// handleRPC validates protocol metadata before dispatching discovery or a workflow operation.
func (m MCPServer) handleRPC(ctx context.Context, raw []byte) workflowRPCResponse {
	request := workflowRPCRequest{}
	if len(raw) == 0 || len(raw) > mcpMaxRequestBytes || decodeStrict(raw, &request) != nil || request.JSONRPC != "2.0" || !validWorkflowRPCID(request.ID) {
		return workflowRPCResponse{JSONRPC: "2.0", ID: request.ID, Error: &workflowRPCError{Code: -32600, Message: "Invalid Request"}}
	}
	params := workflowRPCParams{}
	if decodeStrict(request.Params, &params) != nil || params.ProtocolVersion != MCPProtocolVersion {
		return workflowRPCResponse{JSONRPC: "2.0", ID: request.ID, Error: &workflowRPCError{Code: -32602, Message: "Invalid params"}}
	}
	switch request.Method {
	case "server/discover":
		return workflowRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"protocolVersion": MCPProtocolVersion, "applicationContractVersion": ContractVersion, "capabilities": map[string]any{"tools": map[string]any{}}}}
	case "tools/list":
		return workflowRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"tools": workflowTools()}}
	case "tools/call":
		operation, ok := workflowToolOperation(params.Name)
		if !ok {
			return workflowRPCResponse{JSONRPC: "2.0", ID: request.ID, Error: &workflowRPCError{Code: -32601, Message: "Method not found"}}
		}
		if len(params.Arguments) == 0 {
			params.Arguments = json.RawMessage(`{}`)
		}
		response := m.Handle(ctx, MCPRequest{Version: ContractVersion, Operation: operation, Arguments: params.Arguments})
		rawResponse, err := json.Marshal(response)
		if err != nil {
			response = MCPResponse{Version: ContractVersion, Error: CodeInternal}
			rawResponse, _ = json.Marshal(response)
		}
		return workflowRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: workflowMCPResult{StructuredContent: response, Content: []map[string]any{{"type": "text", "text": string(rawResponse)}}, IsError: response.Error != ""}}
	default:
		return workflowRPCResponse{JSONRPC: "2.0", ID: request.ID, Error: &workflowRPCError{Code: -32601, Message: "Method not found"}}
	}
}

// workflowTools advertises the closed workflow MCP operation set.
func workflowTools() []workflowMCPTool {
	object := func(required ...string) map[string]any {
		return map[string]any{"type": "object", "required": required, "additionalProperties": false}
	}
	return []workflowMCPTool{
		{Name: "featureproof.workflow.start", InputSchema: object("idempotency_key", "specification_ref", "contract_ref", "provider", "model", "auth_profile", "runtime_profile", "repository_id", "revision", "approval")},
		{Name: "featureproof.workflow.status", InputSchema: object("handle")},
		{Name: "featureproof.workflow.view", InputSchema: object("handle")},
		{Name: "featureproof.workflow.decide", InputSchema: object("handle", "decision_id", "revision", "action")},
		{Name: "featureproof.workflow.cancel", InputSchema: object("handle")},
		{Name: "featureproof.workflow.result", InputSchema: object("handle")},
	}
}

// workflowToolOperation maps advertised MCP names to the transport-neutral operation contract.
func workflowToolOperation(name string) (string, bool) {
	switch name {
	case "featureproof.workflow.start", "featureproof.workflow.status", "featureproof.workflow.view", "featureproof.workflow.decide", "featureproof.workflow.cancel", "featureproof.workflow.result":
		return strings.TrimPrefix(name, "featureproof."), true
	default:
		return "", false
	}
}

// validWorkflowRPCID permits only non-null scalar correlation identifiers.
func validWorkflowRPCID(raw json.RawMessage) bool {
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
	default:
		return false
	}
}

// Handle validates a v1 operation and delegates it without weakening application authorization.
func (m MCPServer) Handle(ctx context.Context, request MCPRequest) MCPResponse {
	if m.Service == nil || request.Version != ContractVersion || !safeID.MatchString(m.Principal) {
		return MCPResponse{Version: ContractVersion, Error: CodeInvalid}
	}
	var value any
	var err error
	switch request.Operation {
	case "workflow.start":
		var input mcpStartCommand
		if decodeStrict(request.Arguments, &input) != nil {
			err = &Error{Code: CodeInvalid, Operation: "mcp"}
		} else {
			compositionRef, profileOK := m.AuthProfiles[input.AuthProfile]
			command := StartCommand{Principal: m.Principal, IdempotencyKey: input.IdempotencyKey, SpecificationRef: input.SpecificationRef, ContractRef: input.ContractRef, Provider: input.Provider, Model: input.Model, CompositionRef: compositionRef, RuntimeProfile: input.RuntimeProfile, RepositoryID: input.RepositoryID, Revision: input.Revision, Approval: input.Approval}
			direct, directOK := m.DirectTargets[input.TargetAlias]
			preview, previewOK := m.PreviewTargets[input.PreviewAlias]
			if !profileOK || len(compositionRef) != 64 || directOK == previewOK {
				err = &Error{Code: CodeInvalid, Operation: "mcp_target"}
			} else {
				if directOK {
					command.Target.Direct = &direct
				} else {
					command.Target.Preview = &preview
				}
				value, err = m.Service.Start(ctx, command)
			}
		}
	case "workflow.status":
		var command struct {
			Handle string `json:"handle"`
		}
		if decodeStrict(request.Arguments, &command) != nil {
			err = &Error{Code: CodeInvalid, Operation: "mcp"}
		} else {
			value, err = m.Service.Status(ctx, command.Handle)
		}
	case "workflow.view":
		var command struct {
			Handle string `json:"handle"`
		}
		if decodeStrict(request.Arguments, &command) != nil {
			err = &Error{Code: CodeInvalid, Operation: "mcp"}
		} else {
			value, err = m.Service.ApprovalView(ctx, command.Handle)
		}
	case "workflow.decide":
		var command DecisionCommand
		if decodeStrict(request.Arguments, &command) != nil {
			err = &Error{Code: CodeInvalid, Operation: "mcp"}
		} else {
			command.Principal = m.Principal
			value, err = m.Service.Decide(ctx, command)
		}
	case "workflow.cancel":
		var command struct {
			Handle string `json:"handle"`
		}
		if decodeStrict(request.Arguments, &command) != nil {
			err = &Error{Code: CodeInvalid, Operation: "mcp"}
		} else {
			value, err = m.Service.Cancel(ctx, m.Principal, command.Handle)
		}
	case "workflow.result":
		var command struct {
			Handle string `json:"handle"`
		}
		if decodeStrict(request.Arguments, &command) != nil {
			err = &Error{Code: CodeInvalid, Operation: "mcp"}
		} else {
			value, err = m.Service.Result(ctx, command.Handle)
		}
	default:
		err = &Error{Code: CodeInvalid, Operation: "mcp"}
	}
	if err != nil {
		return MCPResponse{Version: ContractVersion, Error: errorCode(err), Decision: errorDecision(err)}
	}
	return MCPResponse{Version: ContractVersion, Value: value}
}

// mcpStartCommand accepts only aliases while composition supplies trusted target values.
type mcpStartCommand struct {
	IdempotencyKey   string       `json:"idempotency_key"`
	SpecificationRef string       `json:"specification_ref"`
	ContractRef      string       `json:"contract_ref"`
	Provider         string       `json:"provider"`
	Model            string       `json:"model"`
	AuthProfile      string       `json:"auth_profile"`
	RuntimeProfile   string       `json:"runtime_profile"`
	RepositoryID     string       `json:"repository_id"`
	Revision         string       `json:"revision"`
	Approval         ApprovalMode `json:"approval"`
	TargetAlias      string       `json:"target_alias,omitempty"`
	PreviewAlias     string       `json:"preview_alias,omitempty"`
}

// GitHubGateway maps an already authorized GitHub selection onto a complete configured workflow template.
type GitHubGateway struct {
	Service   *Service
	Principal string
	Commands  map[string]StartCommand
}

// githubContractAuthorityKey marks PR authority created only inside the authenticated GitHub adapter.
type githubContractAuthorityKey struct{}

// GitHubContractAuthority returns the trusted PR head and changed paths attached by GitHubGateway.
// feature22:coverage validationworkflow.GitHubContractAuthority
func GitHubContractAuthority(ctx context.Context) (RevisionContext, bool) {
	value, ok := ctx.Value(githubContractAuthorityKey{}).(RevisionContext)
	return value, ok
}

// Submit starts planning for the exact refreshed pull-request head and never auto-approves the generated plan.
// feature22:coverage validationworkflow.GitHubGateway.Submit
func (g GitHubGateway) Submit(ctx context.Context, selection githubpr.Selection, snapshot githubpr.PullRequestSnapshot, handle string) (githubpr.RunState, error) {
	command, ok := g.Commands[selection.Alias]
	if !ok || g.Service == nil || command.Approval != ApprovalRequired || githubpr.VerifySnapshot(snapshot) != nil {
		return githubpr.RunState{}, &githubpr.Error{Code: githubpr.CodeUnauthorized, Operation: "workflow_selection"}
	}
	command.RepositoryID, command.Revision, command.IdempotencyKey = itoaSigned(snapshot.RepositoryID), snapshot.HeadOID, handle
	command.GitHubAuthority = githubAuthorityReference(snapshot)
	ctx = context.WithValue(ctx, githubContractAuthorityKey{}, RevisionContext{RepositoryID: command.RepositoryID, CommitOID: snapshot.HeadOID, ChangedPaths: append([]string{}, snapshot.ChangedPaths...)})
	view, err := g.Service.Start(ctx, command)
	if err != nil {
		return githubpr.RunState{}, githubError(err)
	}
	return githubState(view, snapshot), nil
}

// ResolveExisting verifies the durable workflow still belongs to the requested exact revision lineage.
func (g GitHubGateway) ResolveExisting(ctx context.Context, lookup githubpr.ExistingRunLookup) (githubpr.RunState, error) {
	view, err := g.Service.Status(ctx, lookup.RunHandle)
	if err != nil {
		return githubpr.RunState{}, githubError(err)
	}
	if view.RepositoryID != itoaSigned(lookup.RepositoryID) || view.Revision != lookup.HeadOID {
		return githubpr.RunState{}, &githubpr.Error{Code: githubpr.CodeConflict, Operation: "workflow_lineage"}
	}
	return g.githubState(ctx, view)
}

// Status projects one workflow lifecycle for GitHub check polling.
func (g GitHubGateway) Status(ctx context.Context, handle string) (githubpr.RunState, error) {
	view, err := g.Service.Status(ctx, handle)
	if err != nil {
		return githubpr.RunState{}, githubError(err)
	}
	return g.githubState(ctx, view)
}

// Cancel delegates durable cancellation to the shared workflow service.
func (g GitHubGateway) Cancel(ctx context.Context, handle, reason string) error {
	_, err := g.Service.Cancel(ctx, g.Principal, handle)
	return githubError(err)
}

// decodeStrict rejects trailing tokens and unknown fields at local adapter boundaries.
func decodeStrict(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(destination) != nil {
		return &Error{Code: CodeInvalid, Operation: "decode"}
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return &Error{Code: CodeInvalid, Operation: "decode"}
	}
	return nil
}

// parseUint parses exact decimal approval revisions without signs or whitespace.
func parseUint(value string) (uint64, bool) {
	if value == "" || strings.Trim(value, "0123456789") != "" {
		return 0, false
	}
	var result uint64
	for _, digit := range value {
		next := result*10 + uint64(digit-'0')
		if next < result {
			return 0, false
		}
		result = next
	}
	return result, result > 0
}

// errorCode closes unexpected collaborator errors at adapter boundaries.
func errorCode(err error) ErrorCode {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return CodeInternal
}

// errorDecision returns an isolated safe contract decision for CLI and MCP adapters.
func errorDecision(err error) *ContextDecision {
	var typed *Error
	if !errors.As(err, &typed) || typed.Decision == nil {
		return nil
	}
	return &ContextDecision{MissingKinds: append([]string(nil), typed.Decision.MissingKinds...), Operations: append([]contractcatalog.OperationKey(nil), typed.Decision.Operations...), ConflictIDs: append([]string(nil), typed.Decision.ConflictIDs...)}
}

// githubError maps workflow categories to the closed GitHub integration vocabulary.
func githubError(err error) error {
	if err == nil {
		return nil
	}
	switch errorCode(err) {
	case CodeInvalid, CodeInsufficientContext, CodeReviewRequired:
		return &githubpr.Error{Code: githubpr.CodeInvalid, Operation: "workflow"}
	case CodeUnauthorized:
		return &githubpr.Error{Code: githubpr.CodeUnauthorized, Operation: "workflow"}
	case CodeConflict:
		return &githubpr.Error{Code: githubpr.CodeConflict, Operation: "workflow"}
	case CodeCancelled:
		return &githubpr.Error{Code: githubpr.CodeCancelled, Operation: "workflow"}
	default:
		return &githubpr.Error{Code: githubpr.CodeInternal, Operation: "workflow"}
	}
}

// githubState binds the workflow handle and lifecycle to one refreshed pull-request snapshot.
func githubState(view WorkflowView, snapshot githubpr.PullRequestSnapshot) githubpr.RunState {
	return githubpr.RunState{RunHandle: view.Handle, RepositoryID: snapshot.RepositoryID, HeadOID: snapshot.HeadOID, Phase: githubPhase(view), Outcome: outcomeText(view)}
}

// githubState returns exact lineage and verified package metadata for GitHub terminal projection.
func (g GitHubGateway) githubState(ctx context.Context, view WorkflowView) (githubpr.RunState, error) {
	repositoryID, ok := parsePositiveInt64(view.RepositoryID)
	if !ok {
		return githubpr.RunState{}, &githubpr.Error{Code: githubpr.CodeIntegrity, Operation: "workflow_lineage"}
	}
	state := githubpr.RunState{RunHandle: view.Handle, RepositoryID: repositoryID, HeadOID: view.Revision, Phase: githubPhase(view), Outcome: outcomeText(view)}
	if view.Phase != PhaseTerminal {
		return state, nil
	}
	result, err := g.Service.Result(ctx, view.Handle)
	if err != nil {
		return githubpr.RunState{}, githubError(err)
	}
	if result.Package != nil {
		state.Package = result.Package.PackageDir
		state.PackageRunID = result.Package.RunID
		state.BindingRunID = result.Package.RunID
		state.BindingSHA256 = result.Package.BindingSHA256
	}
	return state, nil
}

// parsePositiveInt64 parses the GitHub repository identity without signs or overflow.
func parsePositiveInt64(value string) (int64, bool) {
	parsed, ok := parseUint(value)
	if !ok || parsed > uint64(^uint64(0)>>1) {
		return 0, false
	}
	return int64(parsed), true
}

// githubPhase reduces detailed workflow phases to the GitHub gateway's running or terminal contract.
func githubPhase(view WorkflowView) string {
	if view.Phase == PhaseTerminal {
		return "terminal"
	}
	return "running"
}

// outcomeText returns an empty nonterminal outcome or the closed terminal value.
func outcomeText(view WorkflowView) string {
	if view.Outcome == nil {
		return ""
	}
	return string(*view.Outcome)
}

// itoaSigned renders positive provider identities without platform-width assumptions.
func itoaSigned(value int64) string {
	if value <= 0 {
		return ""
	}
	return itoa(uint64(value))
}
