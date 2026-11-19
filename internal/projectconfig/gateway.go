package projectconfig

import "context"

// Operation is the closed administrative command vocabulary shared by CLI, API and MCP adapters.
type Operation string

const (
	// OperationValidate performs non-mutating validation.
	OperationValidate Operation = "validate"
	// OperationPublish publishes and activates one canonical document.
	OperationPublish Operation = "publish"
	// OperationRollback creates an activation for one historical version.
	OperationRollback Operation = "rollback"
	// OperationShowActive returns the current head, version and effective snapshot.
	OperationShowActive Operation = "show"
	// OperationShowVersion returns one immutable version.
	OperationShowVersion Operation = "version"
	// OperationHistory returns one bounded history page.
	OperationHistory Operation = "history"
	// OperationExport returns canonical non-secret document bytes.
	OperationExport Operation = "export"
)

// GatewayRequest contains transport-decoded values after trusted identity derived project scope.
type GatewayRequest struct {
	Operation          Operation
	Authority          ConfigurationAuthority
	ProjectKey         ProjectKey
	OperationID        string
	ExpectedGeneration uint64
	Version            uint64
	Cursor             string
	Limit              int
	Document           []byte
}

// GatewayResult is the safe union returned identically to administrative adapters.
type GatewayResult struct {
	Effective *EffectiveConfiguration
	Receipt   *PublishReceipt
	Head      *ConfigurationHead
	Version   *ConfigurationVersion
	History   *HistoryPage
	Export    []byte
}

// Gateway is the sole administrative transport boundary over Service.
type Gateway struct{ Service *Service }

// Execute dispatches one closed operation without granting direct store access.
func (gateway Gateway) Execute(ctx context.Context, request GatewayRequest) (GatewayResult, error) {
	if gateway.Service == nil {
		return GatewayResult{}, &Error{Category: CategoryUnavailable, Operation: "gateway"}
	}
	switch request.Operation {
	case OperationValidate:
		effective, err := gateway.Service.Validate(ctx, request.Authority, request.ProjectKey, request.Document)
		return GatewayResult{Effective: &effective}, err
	case OperationPublish:
		receipt, err := gateway.Service.PublishAndActivate(ctx, request.Authority, request.ProjectKey, request.OperationID, request.ExpectedGeneration, request.Document)
		return GatewayResult{Receipt: &receipt}, err
	case OperationRollback:
		receipt, err := gateway.Service.Rollback(ctx, request.Authority, request.ProjectKey, request.OperationID, request.Version, request.ExpectedGeneration)
		return GatewayResult{Receipt: &receipt}, err
	case OperationShowActive:
		head, version, effective, err := gateway.Service.Active(ctx, request.Authority, request.ProjectKey)
		return GatewayResult{Head: &head, Version: &version, Effective: &effective}, err
	case OperationShowVersion:
		version, effective, err := gateway.Service.Version(ctx, request.Authority, request.ProjectKey, request.Version)
		return GatewayResult{Version: &version, Effective: &effective}, err
	case OperationHistory:
		page, err := gateway.Service.History(ctx, request.Authority, request.ProjectKey, request.Cursor, request.Limit)
		return GatewayResult{History: &page}, err
	case OperationExport:
		value, err := gateway.Service.Export(ctx, request.Authority, request.ProjectKey, request.Version)
		return GatewayResult{Export: value}, err
	default:
		return GatewayResult{}, invalid("gateway", "operation")
	}
}
