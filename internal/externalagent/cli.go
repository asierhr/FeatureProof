package externalagent

import (
	"context"
	"encoding/json"
	"io"
)

// RunCLI parses the JSON-only external-agent commands and invokes the shared service.
func RunCLI(ctx context.Context, args []string, output io.Writer, service *Service) int {
	operation, input, waitHandle, parseErr := parseCLI(args)
	if parseErr != nil {
		return writeCLI(output, Envelope{ContractVersion: ContractVersion, Error: parseErr})
	}
	envelope := service.Invoke(ctx, operation, input)
	code := writeCLI(output, envelope)
	if code == 0 && operation == "submit" && waitHandle {
		if view, ok := envelope.Data.(RunView); ok {
			_ = service.Wait(ctx, view.RunHandle)
		}
	}
	return code
}

// parseCLI recognizes a closed flag grammar and creates transport-neutral JSON input.
func parseCLI(args []string) (string, []byte, bool, *AppError) {
	if len(args) == 2 && args[0] == "capabilities" && args[1] == "--json" {
		return "capabilities", []byte(`{}`), false, nil
	}
	if len(args) < 3 || (args[0] != "run" && args[0] != "correction") || args[len(args)-1] != "--json" {
		return "", nil, false, &AppError{Code: CodeInvalidRequest, Field: "arguments"}
	}
	operation := args[1]
	correctionCommand := args[0] == "correction"
	if correctionCommand {
		operation = "correction_" + operation
	}
	values := map[string]string{}
	for index := 2; index < len(args)-1; index += 2 {
		if index+1 >= len(args)-1 || values[args[index]] != "" {
			return "", nil, false, &AppError{Code: CodeInvalidRequest, Field: "arguments"}
		}
		values[args[index]] = args[index+1]
	}
	var input any
	switch operation {
	case "submit":
		if len(values) != 2 || values["--selection"] == "" || values["--idempotency-key"] == "" {
			return "", nil, false, &AppError{Code: CodeInvalidRequest, Field: "arguments"}
		}
		input = SubmitInput{SelectionAlias: values["--selection"], IdempotencyKey: values["--idempotency-key"]}
	case "status", "result":
		if len(values) != 1 || values["--run"] == "" {
			return "", nil, false, &AppError{Code: CodeInvalidRequest, Field: "arguments"}
		}
		input = HandleInput{RunHandle: values["--run"]}
	case "cancel":
		if len(values) != 2 || values["--run"] == "" || values["--idempotency-key"] == "" {
			return "", nil, false, &AppError{Code: CodeInvalidRequest, Field: "arguments"}
		}
		input = CancelInput{RunHandle: values["--run"], IdempotencyKey: values["--idempotency-key"]}
	case "correction_submit":
		if len(values) != 3 || values["--parent-run"] == "" || values["--selection"] == "" || values["--idempotency-key"] == "" {
			return "", nil, false, &AppError{Code: CodeInvalidRequest, Field: "arguments"}
		}
		input = CorrectionSubmitInput{ParentRunHandle: values["--parent-run"], SelectionAlias: values["--selection"], IdempotencyKey: values["--idempotency-key"]}
	case "correction_status", "correction_result":
		if len(values) != 1 || values["--correction"] == "" {
			return "", nil, false, &AppError{Code: CodeInvalidRequest, Field: "arguments"}
		}
		input = CorrectionHandleInput{CorrectionHandle: values["--correction"]}
	case "correction_cancel":
		if len(values) != 2 || values["--correction"] == "" || values["--idempotency-key"] == "" {
			return "", nil, false, &AppError{Code: CodeInvalidRequest, Field: "arguments"}
		}
		input = CorrectionCancelInput{CorrectionHandle: values["--correction"], IdempotencyKey: values["--idempotency-key"]}
	default:
		return "", nil, false, &AppError{Code: CodeInvalidRequest, Field: "operation"}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return "", nil, false, &AppError{Code: CodeInternal}
	}
	return operation, raw, operation == "submit", nil
}

// writeCLI emits exactly one canonical object and maps closed errors to stable exits.
func writeCLI(output io.Writer, envelope Envelope) int {
	raw, err := CanonicalJSON(envelope)
	if err != nil {
		return 3
	}
	if _, err := output.Write(append(raw, '\n')); err != nil {
		return 3
	}
	if envelope.Error == nil {
		return 0
	}
	switch envelope.Error.Code {
	case CodeInvalidRequest:
		return 2
	case CodeUnauthorized, CodeNotFound:
		return 4
	case CodeConflict, CodeInProgress:
		return 5
	default:
		return 3
	}
}
