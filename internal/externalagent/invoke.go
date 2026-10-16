package externalagent

import (
	"context"
	"errors"
)

// Invoke is the sole transport-neutral dispatcher for the application contract.
func (s *Service) Invoke(ctx context.Context, operation string, raw []byte) Envelope {
	envelope := Envelope{ContractVersion: ContractVersion}
	var err error
	switch operation {
	case "capabilities":
		var input struct{}
		if err = DecodeStrict(raw, &input); err == nil {
			envelope.Data, err = s.Capabilities(ctx)
		}
	case "submit":
		var input SubmitInput
		if err = DecodeStrict(raw, &input); err == nil {
			envelope.Data, err = s.Submit(ctx, input)
		}
	case "status":
		var input HandleInput
		if err = DecodeStrict(raw, &input); err == nil {
			envelope.Data, err = s.Status(ctx, input)
		}
	case "result":
		var input HandleInput
		if err = DecodeStrict(raw, &input); err == nil {
			envelope.Data, err = s.Result(ctx, input)
		}
	case "cancel":
		var input CancelInput
		if err = DecodeStrict(raw, &input); err == nil {
			envelope.Data, err = s.Cancel(ctx, input)
		}
	default:
		err = &AppError{Code: CodeInvalidRequest, Field: "operation"}
	}
	if err != nil {
		var app *AppError
		if !errors.As(err, &app) {
			app = &AppError{Code: CodeInternal}
		}
		envelope.Data = nil
		envelope.Error = app
	}
	return envelope
}
