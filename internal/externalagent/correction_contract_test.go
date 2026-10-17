package externalagent

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// fakeCorrectionUseCases models the bounded correction application boundary for transport parity.
type fakeCorrectionUseCases struct {
	secret         string
	limitDimension string
}

// SubmitCorrection returns a bounded lifecycle view for transport parity tests.
func (f fakeCorrectionUseCases) SubmitCorrection(_ context.Context, _ string, input CorrectionSubmitInput) (CorrectionView, error) {
	if f.limitDimension != "" {
		return CorrectionView{CorrectionHandle: "correction_limit_0001", ParentRunHandle: input.ParentRunHandle, Phase: "terminal", Status: OutcomeInconclusive, TerminalReason: "limit_exhausted", LimitDimension: f.limitDimension, UpdatedAt: time.Unix(1, 0).UTC()}, nil
	}
	return CorrectionView{CorrectionHandle: "correction_handle_0001", ParentRunHandle: input.ParentRunHandle, ChildRunHandle: "child_handle_000001", Phase: "reserved", UpdatedAt: time.Unix(1, 0).UTC()}, nil
}

// CorrectionStatus returns a stable terminal view without backend text.
func (f fakeCorrectionUseCases) CorrectionStatus(_ context.Context, _ string, input CorrectionHandleInput) (CorrectionView, error) {
	return CorrectionView{CorrectionHandle: input.CorrectionHandle, ParentRunHandle: "parent_handle_00001", ChildRunHandle: "child_handle_000001", Phase: "terminal", Status: OutcomeFailed, UpdatedAt: time.Unix(2, 0).UTC()}, nil
}

// CorrectionResult returns one safe comparison entry with opaque evidence.
func (f fakeCorrectionUseCases) CorrectionResult(_ context.Context, _ string, input CorrectionHandleInput) (CorrectionResultView, error) {
	return CorrectionResultView{CorrectionView: CorrectionView{CorrectionHandle: input.CorrectionHandle, ParentRunHandle: "parent_handle_00001", ChildRunHandle: "child_handle_000001", Phase: "terminal", Status: OutcomeFailed, UpdatedAt: time.Unix(2, 0).UTC()}, Corrected: []ComparisonEntryView{}, Persistent: []ComparisonEntryView{{Scenario: ComparisonScenarioView{ConstraintID: "constraint", Scenario: "unspecified", BatchID: "1", StepID: "step", DeclarationID: "declaration"}, Class: "persistent", EvidenceHandles: []string{"evidence_handle_001"}}}, New: []ComparisonEntryView{}, NotApplicable: []ComparisonEntryView{}, Inconclusive: []ComparisonEntryView{}}, nil
}

// CancelCorrection returns a replay-safe cancelled lifecycle view.
func (f fakeCorrectionUseCases) CancelCorrection(_ context.Context, _ string, input CorrectionCancelInput) (CorrectionView, error) {
	return CorrectionView{CorrectionHandle: input.CorrectionHandle, ParentRunHandle: "parent_handle_00001", Phase: "terminal", Status: OutcomeCancelled, UpdatedAt: time.Unix(3, 0).UTC()}, nil
}

// TestCorrectionCLIAndMCPContractParity proves both transports invoke the same v2 application envelope.
func TestCorrectionCLIAndMCPContractParity(t *testing.T) {
	service := correctionContractService(t, map[string]bool{ScopeCorrectionCreate: true, ScopeCorrectionRead: true, ScopeCorrectionCancel: true})
	server, err := NewMCPServer(service)
	if err != nil {
		t.Fatal(err)
	}
	var cli bytes.Buffer
	if code := RunCLI(context.Background(), []string{"correction", "status", "--correction", "correction_handle_0001", "--json"}, &cli, service); code != 0 {
		t.Fatalf("code=%d body=%s", code, cli.String())
	}
	var cliEnvelope Envelope
	if err := json.Unmarshal(cli.Bytes(), &cliEnvelope); err != nil {
		t.Fatal(err)
	}
	mcp := mcpEnvelope(t, invokeMCP(t, server, 1, "featureproof.correction_status", CorrectionHandleInput{CorrectionHandle: "correction_handle_0001"}))
	cliRaw, _ := json.Marshal(cliEnvelope.Data)
	mcpRaw, _ := json.Marshal(mcp.Data)
	var cliValue, mcpValue any
	_ = json.Unmarshal(cliRaw, &cliValue)
	_ = json.Unmarshal(mcpRaw, &mcpValue)
	if !reflect.DeepEqual(cliValue, mcpValue) || cliEnvelope.ContractVersion != 2 || mcp.ContractVersion != 2 {
		t.Fatalf("cli=%s mcp=%s", cliRaw, mcpRaw)
	}
}

// TestCorrectionScopesAndSecretProjection proves discovery follows scopes and responses omit hostile bytes.
func TestCorrectionScopesAndSecretProjection(t *testing.T) {
	service := correctionContractService(t, map[string]bool{ScopeCorrectionRead: true})
	capabilities, err := service.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, operation := range capabilities.Operations {
		names[operation.Name] = true
	}
	raw, _ := json.Marshal(capabilities)
	if names["correction_submit"] || !names["correction_result"] || bytes.Contains(raw, []byte("SECRET")) {
		t.Fatalf("capabilities=%s", raw)
	}
	envelope := service.Invoke(context.Background(), "correction_result", []byte(`{"correction_handle":"correction_handle_0001"}`))
	raw, _ = json.Marshal(envelope)
	if envelope.Error != nil || bytes.Contains(raw, []byte("SECRET")) || bytes.Contains(raw, []byte("parent.ev")) {
		t.Fatalf("result=%s", raw)
	}
}

// TestCorrectionLimitDimensionsHaveCLIAndMCPParity proves every exhausted admission axis stays typed application data.
func TestCorrectionLimitDimensionsHaveCLIAndMCPParity(t *testing.T) {
	for _, dimension := range []string{"attempts", "duration", "model_calls", "model_tokens"} {
		t.Run(dimension, func(t *testing.T) {
			service := correctionContractService(t, map[string]bool{ScopeCorrectionCreate: true}, dimension)
			server, err := NewMCPServer(service)
			if err != nil {
				t.Fatal(err)
			}
			var cli bytes.Buffer
			arguments := []string{"correction", "submit", "--parent-run", "parent_handle_00001", "--selection", "allowed", "--idempotency-key", "limit-key-12345", "--json"}
			if code := RunCLI(context.Background(), arguments, &cli, service); code != 0 {
				t.Fatalf("code=%d body=%s", code, cli.String())
			}
			var cliEnvelope Envelope
			if err := json.Unmarshal(cli.Bytes(), &cliEnvelope); err != nil {
				t.Fatal(err)
			}
			mcp := mcpEnvelope(t, invokeMCP(t, server, 1, "featureproof.correction_submit", CorrectionSubmitInput{ParentRunHandle: "parent_handle_00001", SelectionAlias: "allowed", IdempotencyKey: "limit-key-12345"}))
			cliRaw, _ := json.Marshal(cliEnvelope.Data)
			mcpRaw, _ := json.Marshal(mcp.Data)
			var cliValue, mcpValue any
			_ = json.Unmarshal(cliRaw, &cliValue)
			_ = json.Unmarshal(mcpRaw, &mcpValue)
			if !reflect.DeepEqual(cliValue, mcpValue) || !bytes.Contains(cliRaw, []byte(`"terminal_reason":"limit_exhausted"`)) || !bytes.Contains(cliRaw, []byte(`"limit_dimension":"`+dimension+`"`)) {
				t.Fatalf("cli=%s mcp=%s", cliRaw, mcpRaw)
			}
			capabilities, err := service.Capabilities(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range capabilities.Operations {
				if operation.Name == "correction_submit" {
					if err := compileAdvertisedSchema(t, operation.OutputSchema).Validate(jsonValue(t, cliEnvelope)); err != nil {
						t.Fatalf("schema rejected %s: %v", dimension, err)
					}
				}
			}
		})
	}
}

// correctionContractService composes a scoped service with a deterministic correction fake.
func correctionContractService(t *testing.T, scopes map[string]bool, limitDimension ...string) *Service {
	t.Helper()
	registry, err := NewLocalRegistry(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	dimension := ""
	if len(limitDimension) == 1 {
		dimension = limitDimension[0]
	}
	service, err := NewService(ServiceDependencies{Identity: &fixedIdentity{principal: Principal{ID: "principal", Scopes: scopes}}, Catalog: fixedCatalog{allowed: map[string]map[string]bool{"principal": {"allowed": true}}}, Registry: registry, Executor: &fakeExecutor{}, Packages: fakePackages{}, Clock: fixedClock{now: time.Unix(1, 0).UTC()}, IDs: &sequenceIDs{}, Owner: "owner-correction-contract", Retention: time.Hour, RunTimeout: time.Second, Corrections: fakeCorrectionUseCases{secret: "SECRET", limitDimension: dimension}})
	if err != nil {
		t.Fatal(err)
	}
	return service
}
