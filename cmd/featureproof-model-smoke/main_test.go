package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/model"
)

// fakeBackend records whether smoke validation crossed into external composition.
type fakeBackend struct {
	provisionCalls, inferCalls int
	err                        error
	record                     model.AttemptRecord
}

// Provision records one controlled profile action.
func (b *fakeBackend) Provision(context.Context, smokeOptions) error {
	b.provisionCalls++
	return b.err
}

// Infer records one controlled provider action and returns safe metadata.
func (b *fakeBackend) Infer(context.Context, smokeOptions) (model.AttemptRecord, error) {
	b.inferCalls++
	return b.record, b.err
}

// TestSmokeRequiresOptInBeforeProfileOrNetwork proves the first process boundary is opt-in.
func TestSmokeRequiresOptInBeforeProfileOrNetwork(t *testing.T) {
	backend := &fakeBackend{}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"-action=infer", "-provider=openai", "-model=chosen", "-profile=profile", "-profile-store=" + filepath.Join(t.TempDir(), "profiles.json")}, &stdout, &stderr, backend)
	if code == 0 || backend.inferCalls != 0 || backend.provisionCalls != 0 {
		t.Fatalf("code=%d backend=%+v", code, backend)
	}
}

// TestSmokeRejectsIncompleteExplicitSelection proves there is no provider or model fallback.
func TestSmokeRejectsIncompleteExplicitSelection(t *testing.T) {
	backend := &fakeBackend{}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"-opt-in", "-action=infer", "-provider=openai", "-profile=profile", "-profile-store=" + filepath.Join(t.TempDir(), "profiles.json")}, &stdout, &stderr, backend)
	if code == 0 || backend.inferCalls != 0 {
		t.Fatalf("code=%d calls=%d", code, backend.inferCalls)
	}
}

// TestSmokeReportsAuthAndProviderFailureWithoutCanaries proves process output uses safe taxonomy.
func TestSmokeReportsAuthAndProviderFailureWithoutCanaries(t *testing.T) {
	canary := "SECRET_SMOKE_CANARY"
	backend := &fakeBackend{err: &model.Error{Category: model.CategoryAuthentication, Operation: "profile", Cause: errors.New(canary)}}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), validInferArgs(t), &stdout, &stderr, backend)
	if code == 0 || !strings.Contains(stderr.String(), "category=authentication") || strings.Contains(stderr.String(), canary) {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

// TestSmokeSuccessPrintsOnlySafeUsageMetadata proves decisions and credentials stay out of output.
func TestSmokeSuccessPrintsOnlySafeUsageMetadata(t *testing.T) {
	backend := &fakeBackend{record: model.AttemptRecord{Provider: "openai", Model: "chosen", Duration: time.Second, Outcome: "success", Usage: model.Usage{InputTokens: 4, OutputTokens: 2, Confirmed: true}}}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), validInferArgs(t), &stdout, &stderr, backend)
	if code != 0 || backend.inferCalls != 1 || stdout.String() != "provider=openai model=chosen duration=1s input_tokens=4 output_tokens=2 outcome=success\n" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

// TestSmokeProvisionUsesExplicitProfileComposition proves profile setup is a separate action.
func TestSmokeProvisionUsesExplicitProfileComposition(t *testing.T) {
	backend := &fakeBackend{}
	var stdout, stderr bytes.Buffer
	args := []string{"-opt-in", "-action=provision", "-provider=openai", "-profile=profile", "-profile-store=" + filepath.Join(t.TempDir(), "profiles.json"), "-host-id=urn:uuid:test-host", "-redirect-uri=http://127.0.0.1:1455/auth/callback"}
	code := run(context.Background(), args, &stdout, &stderr, backend)
	if code != 0 || backend.provisionCalls != 1 || backend.inferCalls != 0 {
		t.Fatalf("code=%d backend=%+v stderr=%q", code, backend, stderr.String())
	}
}

// validInferArgs returns complete explicit smoke selection without any secret.
func validInferArgs(t *testing.T) []string {
	t.Helper()
	return []string{"-opt-in", "-action=infer", "-provider=openai", "-model=chosen", "-profile=profile", "-profile-store=" + filepath.Join(t.TempDir(), "profiles.json")}
}

// TestProductionCompositionFailsSafelyBeforeExternalWork exercises real wiring without credentials or Internet.
func TestProductionCompositionFailsSafelyBeforeExternalWork(t *testing.T) {
	options := smokeOptions{Provider: "openai", Model: "chosen", Profile: "missing", StorePath: filepath.Join(t.TempDir(), "profiles.json"), Timeout: time.Second, Duration: time.Minute, MaxCalls: 1, MaxTokens: 10}
	if _, err := (productionBackend{}).Infer(context.Background(), options); smokeErrorCategory(err) != model.CategoryAuthentication {
		t.Fatalf("missing profile category=%s error=%v", smokeErrorCategory(err), err)
	}
	options.HostID, options.RedirectURI = "host", "http://localhost/callback"
	if err := (productionBackend{}).Provision(context.Background(), options); err == nil {
		t.Fatal("unsafe callback accepted")
	}
	if _, _, _, _, err := productionOAuth("relative"); err == nil {
		t.Fatal("relative credential path accepted")
	}
}

// smokeErrorCategory extracts the safe category returned by production composition.
func smokeErrorCategory(err error) model.Category {
	var typed *model.Error
	if errors.As(err, &typed) {
		return typed.Category
	}
	return ""
}

// TestSmokeRejectsUnknownActionAndInvalidBudgets verifies process validation without backend work.
func TestSmokeRejectsUnknownActionAndInvalidBudgets(t *testing.T) {
	backend := &fakeBackend{}
	var stdout, stderr bytes.Buffer
	args := []string{"-opt-in", "-action=unknown", "-provider=openai", "-profile=profile", "-profile-store=" + filepath.Join(t.TempDir(), "profiles.json")}
	if code := run(context.Background(), args, &stdout, &stderr, backend); code == 0 {
		t.Fatal("unknown action accepted")
	}
	if _, err := parseOptions(append(validInferArgs(t), "-max-calls=0"), &stderr); err == nil {
		t.Fatal("invalid call budget accepted")
	}
}

// TestSmokeProvisionFailureAndMissingBackendUseSafeExitCodes covers remaining process branches.
func TestSmokeProvisionFailureAndMissingBackendUseSafeExitCodes(t *testing.T) {
	backend := &fakeBackend{err: errors.New("SECRET_FAILURE")}
	var stdout, stderr bytes.Buffer
	args := []string{"-opt-in", "-action=provision", "-provider=openai", "-profile=profile", "-profile-store=" + filepath.Join(t.TempDir(), "profiles.json"), "-host-id=host", "-redirect-uri=http://127.0.0.1:1455/auth/callback"}
	if code := run(context.Background(), args, &stdout, &stderr, backend); code == 0 || strings.Contains(stderr.String(), "SECRET_FAILURE") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), validInferArgs(t), &stdout, &stderr, nil); code == 0 {
		t.Fatal("missing backend accepted")
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"-unknown"}, &stdout, &stderr, backend); code == 0 {
		t.Fatal("parse failure accepted")
	}
}

// TestProductionProvisionComposesThroughCancelableBrowser verifies real OAuth wiring without opening a browser.
func TestProductionProvisionComposesThroughCancelableBrowser(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	_ = probe.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	options := smokeOptions{Provider: "openai", Profile: "profile", StorePath: filepath.Join(t.TempDir(), "profiles.json"), HostID: "host", RedirectURI: "http://" + address + "/auth/callback"}
	if err := (productionBackend{}).Provision(ctx, options); err == nil {
		t.Fatal("cancelled browser flow accepted")
	}
}
