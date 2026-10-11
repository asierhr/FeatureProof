package evidence

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// sample creates a fully related plan-attempt record for persistence tests.
func sample() Record {
	return Record{ID: "ev-1", RunID: "run-1", PlanID: "plan-1", StepID: "step-1", Step: 1, ToolCallID: "call-1", Attempt: 1, ToolName: "probe", StartedAt: time.Unix(1, 0).UTC(), Duration: time.Millisecond, Input: map[string]any{"token": "[REDACTED]"}, Output: map[string]any{"status": "ok"}}
}

func TestFileEvidenceStoreDoesNotRewriteConfirmedEvidence(t *testing.T) {
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := sample()
	if err := s.Save(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	r.Output["status"] = "changed"
	if err := s.Save(context.Background(), r); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("got %v", err)
	}
	got, err := s.Get(context.Background(), "ev-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Output["status"] != "ok" {
		t.Fatal("confirmed evidence was rewritten")
	}
}

func TestEvidenceStoreDetectsTamperingAndRelatesIDs(t *testing.T) {
	s, _ := NewFileStore(t.TempDir())
	r := sample()
	if err := s.Save(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID != r.RunID || got.PlanID != r.PlanID || got.StepID != r.StepID || got.ToolCallID != r.ToolCallID || got.IntegrityHash == "" {
		t.Fatalf("bad relation: %+v", got)
	}
}

func TestEvidenceStoreRejectsTamperedRecord(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewFileStore(dir)
	r := sample()
	if err := s.Save(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, r.ID+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range raw {
		if raw[i] == 'o' {
			raw[i] = 'x'
			break
		}
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), r.ID); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("got %v", err)
	}
}

func TestSanitizerRedactsSensitiveFields(t *testing.T) {
	s := Sanitizer{Sensitive: map[string]bool{"token": true}}
	got := s.Map(map[string]any{"token": "secret", "status": "ok"})
	if got["token"] != "[REDACTED]" || got["status"] != "ok" {
		t.Fatalf("unexpected sanitized map: %#v", got)
	}
}

func TestFileStoreObservesContextAndInvalidData(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewFileStore(dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Save(ctx, sample()); !errors.Is(err, context.Canceled) {
		t.Fatalf("save: %v", err)
	}
	if _, err := s.Get(ctx, "ev-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("get: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), "bad"); err == nil {
		t.Fatal("invalid evidence accepted")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(filepath.Join(file, "child")); err == nil {
		t.Fatal("invalid store directory accepted")
	}
}
