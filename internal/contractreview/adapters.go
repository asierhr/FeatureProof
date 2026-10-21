package contractreview

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"github.com/asierhr/featureProof/internal/githubpr"
)

// Application is the minimal shared use-case port consumed by every transport adapter.
type Application interface {
	Create(context.Context, CreateCommand) (ReviewView, error)
	Get(context.Context, string, string) (ReviewView, error)
	Decide(context.Context, DecisionCommand) (ReviewView, error)
	Export(context.Context, ExportCommand) (ExportReceipt, error)
}

// SurfaceConfig holds trusted principal, policy, info and budgets that untrusted inputs cannot override.
type SurfaceConfig struct {
	PrincipalID, PolicyDigest string
	Limits                    Limits
	Info                      ExportInfo
}

// CreateInput is the versioned closed transport request for review creation.
type CreateInput struct {
	Version        string `json:"version"`
	CandidateRef   string `json:"candidate_ref"`
	BaselineRef    string `json:"baseline_ref"`
	IdempotencyKey string `json:"idempotency_key"`
}

// ShowInput is the versioned closed transport request for a bounded review view.
type ShowInput struct {
	Version  string `json:"version"`
	ReviewID string `json:"review_id"`
}

// DecideInput is the versioned closed transport request for whole or selective decisions.
type DecideInput struct {
	Version          string         `json:"version"`
	ReviewID         string         `json:"review_id"`
	IdempotencyKey   string         `json:"idempotency_key"`
	ExpectedRevision uint64         `json:"expected_revision"`
	Whole            WholeAction    `json:"whole,omitempty"`
	Items            []ItemDecision `json:"items,omitempty"`
}

// ExportInput is the versioned closed transport request for guarded publication.
type ExportInput struct {
	Version                string `json:"version"`
	ReviewID               string `json:"review_id"`
	IdempotencyKey         string `json:"idempotency_key"`
	Target                 string `json:"target"`
	ExpectedExistingDigest string `json:"expected_existing_digest,omitempty"`
	ExpectedRevision       uint64 `json:"expected_revision"`
	Replace                bool   `json:"replace,omitempty"`
}

// APIAdapter projects authenticated API requests onto the single shared application service.
type APIAdapter struct {
	Service Application
	Config  SurfaceConfig
}

// Create validates the transport version and injects all trusted policy fields.
func (a APIAdapter) Create(ctx context.Context, input CreateInput) (ReviewView, error) {
	if err := a.valid(input.Version); err != nil {
		return ReviewView{}, err
	}
	return a.Service.Create(ctx, CreateCommand{PrincipalID: a.Config.PrincipalID, CandidateRef: input.CandidateRef, BaselineRef: input.BaselineRef, IdempotencyKey: input.IdempotencyKey, Limits: a.Config.Limits, PolicyDigest: a.Config.PolicyDigest})
}

// createTrustedGitHub injects only a snapshot obtained and sealed by the authenticated GitHub adapter.
func (a APIAdapter) createTrustedGitHub(ctx context.Context, input CreateInput, snapshot *githubpr.PullRequestSnapshot) (ReviewView, error) {
	return a.createTrustedGitHubActor(ctx, input, snapshot, 0)
}

// createTrustedGitHubActor carries the authenticated event actor into canonical durable authority.
func (a APIAdapter) createTrustedGitHubActor(ctx context.Context, input CreateInput, snapshot *githubpr.PullRequestSnapshot, actorID int64) (ReviewView, error) {
	if err := a.valid(input.Version); err != nil {
		return ReviewView{}, err
	}
	return a.Service.Create(ctx, CreateCommand{PrincipalID: a.Config.PrincipalID, CandidateRef: input.CandidateRef, BaselineRef: input.BaselineRef, IdempotencyKey: input.IdempotencyKey, Limits: a.Config.Limits, PolicyDigest: a.Config.PolicyDigest, GitHub: deepClone(snapshot), GitHubActorID: actorID})
}

// Show delegates exact review lookup without accepting authority from the request.
func (a APIAdapter) Show(ctx context.Context, input ShowInput) (ReviewView, error) {
	if err := a.valid(input.Version); err != nil {
		return ReviewView{}, err
	}
	return a.Service.Get(ctx, a.Config.PrincipalID, input.ReviewID)
}

// Decide delegates one explicit CAS decision with the composition-owned principal.
func (a APIAdapter) Decide(ctx context.Context, input DecideInput) (ReviewView, error) {
	if err := a.valid(input.Version); err != nil {
		return ReviewView{}, err
	}
	return a.Service.Decide(ctx, DecisionCommand{PrincipalID: a.Config.PrincipalID, ReviewID: input.ReviewID, Decision: DecisionInput{ExpectedRevision: input.ExpectedRevision, Whole: input.Whole, Items: deepClone(input.Items), IdempotencyKey: input.IdempotencyKey}})
}

// Export delegates one explicit relative target and protected overwrite expectation.
func (a APIAdapter) Export(ctx context.Context, input ExportInput) (ExportReceipt, error) {
	if err := a.valid(input.Version); err != nil {
		return ExportReceipt{}, err
	}
	return a.Service.Export(ctx, ExportCommand{PrincipalID: a.Config.PrincipalID, ReviewID: input.ReviewID, IdempotencyKey: input.IdempotencyKey, ExpectedRevision: input.ExpectedRevision, Target: PublishRequest{Target: input.Target, Replace: input.Replace, ExpectedDigest: input.ExpectedExistingDigest}, Info: a.Config.Info})
}

// valid rejects unsupported versions and incomplete adapter composition before service work.
func (a APIAdapter) valid(version string) error {
	if a.Service == nil || version != VersionV1 || !safeReference(a.Config.PrincipalID) || !validDigest(a.Config.PolicyDigest) || a.Config.Limits.Validate() != nil {
		return typedError(CategoryInvalidInput, "adapter", nil)
	}
	return nil
}

// MCPAdapter exposes the identical closed inputs without model-supplied principal, root or credentials.
type MCPAdapter struct{ API APIAdapter }

// Call routes one allowlisted versioned MCP tool to the shared API projection.
func (m MCPAdapter) Call(ctx context.Context, tool string, raw json.RawMessage) (any, error) {
	switch tool {
	case "contract_review_create_v1":
		var input CreateInput
		if decodeClosed(raw, &input) != nil {
			return nil, typedError(CategoryInvalidInput, "mcp.input", nil)
		}
		return m.API.Create(ctx, input)
	case "contract_review_show_v1":
		var input ShowInput
		if decodeClosed(raw, &input) != nil {
			return nil, typedError(CategoryInvalidInput, "mcp.input", nil)
		}
		return m.API.Show(ctx, input)
	case "contract_review_decide_v1":
		var input DecideInput
		if decodeClosed(raw, &input) != nil {
			return nil, typedError(CategoryInvalidInput, "mcp.input", nil)
		}
		return m.API.Decide(ctx, input)
	case "contract_review_export_v1":
		var input ExportInput
		if decodeClosed(raw, &input) != nil {
			return nil, typedError(CategoryInvalidInput, "mcp.input", nil)
		}
		return m.API.Export(ctx, input)
	default:
		return nil, typedError(CategoryInvalidInput, "mcp.tool", nil)
	}
}

// CLIAdapter parses a closed flag grammar and prints only the shared safe result projection.
type CLIAdapter struct{ API APIAdapter }

// Close releases composition-owned durable adapter descriptors when the command runtime ends.
func (c CLIAdapter) Close() error {
	if closer, ok := c.API.Service.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

// Run executes create, show, decide or export without accepting raw catalogs or identity overrides.
func (c CLIAdapter) Run(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 {
		return typedError(CategoryInvalidInput, "cli.command", nil)
	}
	flags, err := parseFlags(args[0], args[1:])
	if err != nil {
		return err
	}
	var result any
	switch args[0] {
	case "create":
		result, err = c.API.Create(ctx, CreateInput{Version: VersionV1, CandidateRef: flags["candidate"], BaselineRef: flags["baseline"], IdempotencyKey: flags["idempotency-key"]})
	case "show":
		result, err = c.API.Show(ctx, ShowInput{Version: VersionV1, ReviewID: flags["review"]})
	case "decide":
		revision, parseErr := strconv.ParseUint(flags["revision"], 10, 64)
		if parseErr != nil {
			return typedError(CategoryInvalidInput, "cli.revision", nil)
		}
		result, err = c.API.Decide(ctx, DecideInput{Version: VersionV1, ReviewID: flags["review"], IdempotencyKey: flags["idempotency-key"], ExpectedRevision: revision, Whole: WholeAction(flags["action"])})
	case "export":
		revision, parseErr := strconv.ParseUint(flags["revision"], 10, 64)
		if parseErr != nil {
			return typedError(CategoryInvalidInput, "cli.revision", nil)
		}
		result, err = c.API.Export(ctx, ExportInput{Version: VersionV1, ReviewID: flags["review"], IdempotencyKey: flags["idempotency-key"], ExpectedRevision: revision, Target: flags["target"], Replace: flags["replace"] == "true", ExpectedExistingDigest: flags["expected-digest"]})
	default:
		return typedError(CategoryInvalidInput, "cli.command", nil)
	}
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(true)
	if encoder.Encode(result) != nil {
		return typedError(CategoryUnavailable, "cli.output", nil)
	}
	return nil
}

// parseFlags accepts only the exact paired long flags owned by one subcommand and rejects duplicates or surplus inputs.
func parseFlags(command string, args []string) (map[string]string, error) {
	if len(args)%2 != 0 {
		return nil, typedError(CategoryInvalidInput, "cli.flags", nil)
	}
	allowedByCommand := map[string]map[string]bool{
		"create": {"--candidate": true, "--baseline": true, "--idempotency-key": true},
		"show":   {"--review": true},
		"decide": {"--review": true, "--revision": true, "--action": true, "--idempotency-key": true},
		"export": {"--review": true, "--revision": true, "--target": true, "--replace": true, "--expected-digest": true, "--idempotency-key": true},
	}
	allowed, known := allowedByCommand[command]
	if !known {
		return nil, typedError(CategoryInvalidInput, "cli.command", nil)
	}
	out := map[string]string{}
	for index := 0; index < len(args); index += 2 {
		name := strings.TrimLeft(args[index], "-")
		if !allowed[args[index]] || out[name] != "" {
			return nil, typedError(CategoryInvalidInput, "cli.flags", nil)
		}
		out[name] = args[index+1]
	}
	requiredByCommand := map[string][]string{
		"create": {"candidate", "baseline", "idempotency-key"},
		"show":   {"review"},
		"decide": {"review", "revision", "action", "idempotency-key"},
		"export": {"review", "revision", "target", "idempotency-key"},
	}
	for _, name := range requiredByCommand[command] {
		if out[name] == "" {
			return nil, typedError(CategoryInvalidInput, "cli.flags", nil)
		}
	}
	return out, nil
}

// decodeClosed rejects unknown transport fields so surfaces cannot smuggle authority into core.
func decodeClosed(raw []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return typedError(CategoryInvalidInput, "input.trailing", nil)
	}
	return nil
}
