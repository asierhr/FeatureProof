package agentplanning

import (
	"context"

	"github.com/asierhr/featureProof/internal/model"
)

// SessionModel adapts feature 9's shared budget session to the planning consumer port.
type SessionModel struct{ Session *model.Session }

// Generate performs one structured session call without retries or replan reservations.
func (s SessionModel) Generate(ctx context.Context, request StructuredRequest) (ModelResponse, error) {
	if s.Session == nil {
		return ModelResponse{}, typedError(CategoryInvalidInput, "model", "session", nil)
	}
	result, err := s.Session.InferStructured(ctx, model.StructuredRequest{RunID: request.RunID, Instructions: request.Instructions, Untrusted: append([]byte(nil), request.Untrusted...), Bounds: model.IOBounds{MaxOutputBytes: request.MaxOutputBytes}})
	if err != nil {
		return ModelResponse{}, err
	}
	provider, selectedModel := s.Session.Selection()
	return ModelResponse{JSON: append([]byte(nil), result.Output...), Provider: provider, Model: selectedModel}, nil
}
