package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/agenteval"
	"github.com/asierhr/featureProof/internal/model"
)

// TestRunDelegatesInvalidArgumentsWithoutStartingAValidation covers the executable signal wrapper.
func TestRunDelegatesInvalidArgumentsWithoutStartingAValidation(t *testing.T) {
	_ = processArgs()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"validate"}, strings.NewReader(""), &stdout, &stderr); code != 3 || stdout.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

// TestRunDispatchesHermeticAgentEvaluation proves the executable exposes evals without an implicit provider.
func TestRunDispatchesHermeticAgentEvaluation(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(repo, "evals", "agentplanning", "v1")
	sha, err := os.ReadFile(filepath.Join(base, "replay.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"eval", "agent", "-dataset", filepath.Join(base, "dataset.json"), "-criteria", filepath.Join(base, "criteria.json"), "-output", t.TempDir(), "-source", "replay", "-replay", filepath.Join(base, "replay.json"), "-replay-sha256", strings.TrimSpace(string(sha)), "-run-id", "command-eval"}
	var stdout, stderr bytes.Buffer
	if code := run(args, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
}

// TestProductionEvalComposerDefersCredentialResolution proves composition succeeds without network or token access.
func TestProductionEvalComposerDefersCredentialResolution(t *testing.T) {
	options := agenteval.CommandOptions{Provider: "openai", Model: "gpt-test", Profile: "profile-test", ProfileStore: filepath.Join(t.TempDir(), "profiles.json"), CallTimeout: time.Second, MaxDuration: time.Minute, MaxCalls: 1, MaxTokens: 100, MaxReplans: 0, MaxRequestBytes: 1 << 20, MaxHeaderBytes: 1 << 15, MaxStreamBytes: 1 << 20, MaxOutputBytes: 1 << 16}
	source, err := (productionEvalComposer{}).Compose(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	provider, selected := source.Metadata()
	if provider != "openai" || selected != "gpt-test" {
		t.Fatalf("selection=%s/%s", provider, selected)
	}
	budgeted, ok := source.(interface{ SafeBudget() agenteval.SourceBudget })
	if !ok || budgeted.SafeBudget().CallTimeoutMillis != options.CallTimeout.Milliseconds() {
		t.Fatalf("call timeout was not preserved in safe configuration: %#v", source)
	}
	if _, err := (productionEvalComposer{}).Compose(context.Background(), agenteval.CommandOptions{Provider: "other"}); err == nil {
		t.Fatal("unsupported provider accepted")
	}
	if _, err := (productionEvalComposer{}).Compose(context.Background(), agenteval.CommandOptions{Provider: "openai"}); err == nil {
		t.Fatal("invalid profile store accepted")
	}
	invalid := options
	invalid.Profile = ""
	if _, err := (productionEvalComposer{}).Compose(context.Background(), invalid); err == nil {
		t.Fatal("empty profile accepted")
	}
	invalid = options
	invalid.MaxRequestBytes = 0
	if _, err := (productionEvalComposer{}).Compose(context.Background(), invalid); err == nil {
		t.Fatal("invalid adapter bounds accepted")
	}
	invalid = options
	invalid.MaxCalls = -1
	if _, err := (productionEvalComposer{}).Compose(context.Background(), invalid); err == nil {
		t.Fatal("invalid session budget accepted")
	}
	if (evalClock{}).Now().Location() != time.UTC {
		t.Fatal("eval clock is not UTC")
	}
	if err := (evalObserver{}).Observe(context.Background(), model.AttemptRecord{}); err != nil {
		t.Fatal(err)
	}
}

// TestMainPropagatesTheCommandExitCode covers the process shell without terminating the test process.
func TestMainPropagatesTheCommandExitCode(t *testing.T) {
	oldArgs, oldInput, oldOutput, oldError, oldExit := processArgs, processInput, processOutput, processError, processExit
	defer func() {
		processArgs, processInput, processOutput, processError, processExit = oldArgs, oldInput, oldOutput, oldError, oldExit
	}()
	var stdout, stderr bytes.Buffer
	code := -1
	processArgs = func() []string { return []string{"validate"} }
	processInput = strings.NewReader("")
	processOutput = &stdout
	processError = &stderr
	processExit = func(value int) { code = value }
	main()
	if code != 3 {
		t.Fatalf("code=%d", code)
	}
}
