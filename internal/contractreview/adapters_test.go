package contractreview

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// adapterFixture composes one independent service and identical trusted surface configuration.
func adapterFixture(t *testing.T) APIAdapter {
	t.Helper()
	service, _, _, _ := serviceFixture(t, NewMemoryStore())
	return APIAdapter{Service: service, Config: SurfaceConfig{PrincipalID: "actor", PolicyDigest: strings.Repeat("d", 64), Limits: reviewLimits(), Info: ExportInfo{Title: "FeatureProof contract", Version: "1"}}}
}

// TestCLIAPIMCPCreateParity traces R12 identity and state equality through three independent transports.
func TestCLIAPIMCPCreateParity(t *testing.T) {
	input := CreateInput{Version: VersionV1, CandidateRef: "candidate", BaselineRef: "baseline", IdempotencyKey: "create"}
	apiView, err := adapterFixture(t).Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	mcpRaw, _ := json.Marshal(input)
	mcpResult, err := (MCPAdapter{API: adapterFixture(t)}).Call(context.Background(), "contract_review_create_v1", mcpRaw)
	if err != nil {
		t.Fatal(err)
	}
	mcpView := mcpResult.(ReviewView)
	var output bytes.Buffer
	err = (CLIAdapter{API: adapterFixture(t)}).Run(context.Background(), []string{"create", "--candidate", "candidate", "--baseline", "baseline", "--idempotency-key", "create"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	var cliView ReviewView
	if json.Unmarshal(output.Bytes(), &cliView) != nil || apiView != mcpView || apiView != cliView {
		t.Fatalf("surface parity failed: api=%+v mcp=%+v cli=%+v", apiView, mcpView, cliView)
	}
}

// TestAdaptersRejectAuthoritySmugglingAndUnknownFields traces closed schemas and trusted principal injection.
func TestAdaptersRejectAuthoritySmugglingAndUnknownFields(t *testing.T) {
	adapter := adapterFixture(t)
	_, err := (MCPAdapter{API: adapter}).Call(context.Background(), "contract_review_create_v1", json.RawMessage(`{"version":"1","candidate_ref":"candidate","baseline_ref":"baseline","idempotency_key":"key","principal":"attacker"}`))
	errorCategory(t, err, CategoryInvalidInput)
	var output bytes.Buffer
	err = (CLIAdapter{API: adapter}).Run(context.Background(), []string{"create", "--candidate", "candidate", "--baseline", "baseline", "--principal", "attacker"}, &output)
	errorCategory(t, err, CategoryInvalidInput)
	_, err = adapter.Create(context.Background(), CreateInput{Version: "2", CandidateRef: "candidate", BaselineRef: "baseline", IdempotencyKey: "key"})
	errorCategory(t, err, CategoryInvalidInput)
	_, err = adapter.Show(context.Background(), ShowInput{Version: "2", ReviewID: strings.Repeat("a", 24)})
	errorCategory(t, err, CategoryInvalidInput)
	_, err = adapter.Decide(context.Background(), DecideInput{Version: "2"})
	errorCategory(t, err, CategoryInvalidInput)
	_, err = adapter.Export(context.Background(), ExportInput{Version: "2"})
	errorCategory(t, err, CategoryInvalidInput)
}

// TestAPIAndMCPExposeCompleteSharedLifecycle covers show, decide and export routes without divergent DTO rules.
func TestAPIAndMCPExposeCompleteSharedLifecycle(t *testing.T) {
	api := adapterFixture(t)
	created, err := api.Create(context.Background(), CreateInput{Version: VersionV1, CandidateRef: "candidate", BaselineRef: "baseline", IdempotencyKey: "create"})
	if err != nil {
		t.Fatal(err)
	}
	shown, err := api.Show(context.Background(), ShowInput{Version: VersionV1, ReviewID: created.ID})
	if err != nil || shown != created {
		t.Fatalf("show parity failed: %v", err)
	}
	decision := DecideInput{Version: VersionV1, ReviewID: created.ID, IdempotencyKey: "approve", ExpectedRevision: 0, Whole: WholeAccept}
	rawDecision, _ := json.Marshal(decision)
	decisionValue, err := (MCPAdapter{API: api}).Call(context.Background(), "contract_review_decide_v1", rawDecision)
	if err != nil {
		t.Fatal(err)
	}
	decided := decisionValue.(ReviewView)
	rawShow, _ := json.Marshal(ShowInput{Version: VersionV1, ReviewID: created.ID})
	value, err := (MCPAdapter{API: api}).Call(context.Background(), "contract_review_show_v1", rawShow)
	if err != nil || value.(ReviewView) != decided {
		t.Fatalf("MCP show failed: %v", err)
	}
	rawExport, _ := json.Marshal(ExportInput{Version: VersionV1, ReviewID: created.ID, IdempotencyKey: "export", ExpectedRevision: decided.Revision, Target: "contract.json"})
	value, err = (MCPAdapter{API: api}).Call(context.Background(), "contract_review_export_v1", rawExport)
	if err != nil || value.(ExportReceipt).OutputDigest == "" {
		t.Fatalf("MCP export failed: %v", err)
	}
	_, err = (MCPAdapter{API: api}).Call(context.Background(), "unknown", json.RawMessage(`{}`))
	errorCategory(t, err, CategoryInvalidInput)
}

// TestCLIExercisesShowDecideExportAndRejectsMalformedFlags covers every command branch in the terminal adapter.
func TestCLIExercisesShowDecideExportAndRejectsMalformedFlags(t *testing.T) {
	api := adapterFixture(t)
	cli := CLIAdapter{API: api}
	var output bytes.Buffer
	if err := cli.Run(context.Background(), []string{"create", "--candidate", "candidate", "--baseline", "baseline", "--idempotency-key", "create"}, &output); err != nil {
		t.Fatal(err)
	}
	var created ReviewView
	_ = json.Unmarshal(output.Bytes(), &created)
	output.Reset()
	if err := cli.Run(context.Background(), []string{"show", "--review", created.ID}, &output); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := cli.Run(context.Background(), []string{"decide", "--review", created.ID, "--revision", "0", "--action", "accept_all", "--idempotency-key", "approve"}, &output); err != nil {
		t.Fatal(err)
	}
	var decided ReviewView
	_ = json.Unmarshal(output.Bytes(), &decided)
	output.Reset()
	if err := cli.Run(context.Background(), []string{"export", "--review", created.ID, "--revision", strconv.FormatUint(decided.Revision, 10), "--target", "contract.json", "--replace", "false", "--expected-digest", strings.Repeat("a", 64), "--idempotency-key", "export"}, &output); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{}, {"show", "--review"}, {"decide", "--review", created.ID, "--revision", "bad"}, {"export", "--review", created.ID, "--revision", "bad"}, {"unknown", "--review", created.ID}} {
		output.Reset()
		if err := cli.Run(context.Background(), args, &output); err == nil {
			t.Fatalf("malformed CLI accepted: %v", args)
		}
	}
	for _, args := range [][]string{
		{"show", "--review", created.ID, "--candidate", "ignored"},
		{"create", "--candidate", "candidate", "--baseline", "baseline", "--idempotency-key", "other", "--target", "ignored.json"},
		{"show"},
		{"decide", "--review", created.ID, "--revision", "0", "--action", "accept_all"},
		{"export", "--review", created.ID, "--revision", "1", "--idempotency-key", "missing-target"},
	} {
		output.Reset()
		if err := cli.Run(context.Background(), args, &output); errorCategoryOf(err) != CategoryInvalidInput {
			t.Fatalf("cross-command or incomplete flags accepted: %v err=%v", args, err)
		}
	}
}
