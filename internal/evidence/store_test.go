package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// sanitizedForRecord obtains an authentic completed transformation for record tests.
func sanitizedForRecord(t *testing.T, value any) SanitizedValue {
	t.Helper()
	policy, err := NewPolicy(Config{MaxBodyBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewSanitizer(policy).Sanitize(context.Background(), value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// recordForTest seals authentic sanitizer outputs and fails the owning test on invariant errors.
func recordForTest(t *testing.T, base Record, input, output any) Record {
	t.Helper()
	record, err := NewRecord(base, sanitizedForRecord(t, input), sanitizedForRecord(t, output))
	if err != nil {
		t.Fatal(err)
	}
	return record
}

// sample creates a fully related plan-attempt record for persistence tests.
func sample(t *testing.T) Record {
	return recordForTest(t, Record{ID: "ev-1", RunID: "run-1", PlanID: "plan-1", StepID: "step-1", Step: 1, ToolCallID: "call-1", Attempt: 1, ToolName: "probe", StartedAt: time.Unix(1, 0).UTC(), Duration: time.Millisecond}, map[string]any{"token": "[REDACTED]"}, map[string]any{"status": "ok"})
}

func TestFileEvidenceStoreDoesNotRewriteConfirmedEvidence(t *testing.T) {
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := sample(t)
	if err := s.Save(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	r.Output.(map[string]any)["status"] = "changed"
	if err := s.Save(context.Background(), r); !errors.Is(err, ErrUntreated) {
		t.Fatalf("got %v", err)
	}
	got, err := s.Get(context.Background(), "ev-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Output.(map[string]any)["status"] != "ok" {
		t.Fatal("confirmed evidence was rewritten")
	}
}

// TestFileEvidenceStoreRejectsUntreatedRecord proves persistence cannot accept raw payload directly.
func TestFileEvidenceStoreRejectsUntreatedRecord(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = store.Save(context.Background(), Record{ID: "raw", Input: map[string]any{"secret": "raw"}})
	if !errors.Is(err, ErrUntreated) {
		t.Fatalf("untreated record accepted: %v", err)
	}
}

// TestFileEvidenceStorePersistsRedactedRoot proves records can safely represent a fully hidden root.
func TestFileEvidenceStorePersistsRedactedRoot(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record := recordForTest(t, Record{ID: "root"}, Redacted, Redacted)
	if err := store.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Get(context.Background(), "root")
	if err != nil || loaded.Input != Redacted || loaded.Output != Redacted {
		t.Fatalf("redacted root lost: %#v %v", loaded, err)
	}
}

func TestEvidenceStoreDetectsTamperingAndRelatesIDs(t *testing.T) {
	s, _ := NewFileStore(t.TempDir())
	r := sample(t)
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
	r := sample(t)
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

// TestFileStorePreservesExactJSONNumbersAndRejectsTrailingData traces R7's exact stream contract.
func TestFileStorePreservesExactJSONNumbersAndRejectsTrailingData(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"integer":         json.Number("9007199254740993"),
		"decimal":         json.Number("1.2300"),
		"large_exponent":  json.Number("1e999"),
		"small_exponent":  json.Number("-9.1E-999"),
		"nested_numerics": []any{json.Number("7.000"), json.Number("2e+400")},
	}
	record := recordForTest(t, Record{ID: "numbers"}, want, want)
	if err := store.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Get(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Output.(map[string]any)
	for key, token := range want {
		if key == "nested_numerics" {
			continue
		}
		number, ok := got[key].(json.Number)
		if !ok || number.String() != token.(json.Number).String() {
			t.Fatalf("numeric token %s changed: %#v", key, got[key])
		}
	}
	nested := got["nested_numerics"].([]any)
	if nested[0].(json.Number).String() != "7.000" || nested[1].(json.Number).String() != "2e+400" || loaded.IntegrityHash == "" {
		t.Fatalf("nested numeric token or integrity lost: %#v", loaded)
	}
	path := filepath.Join(dir, record.ID+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, suffix := range map[string]string{"second_value": "\n{}", "garbage": " trailing"} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, append(append([]byte(nil), raw...), []byte(suffix)...), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Get(context.Background(), record.ID); err == nil {
				t.Fatal("trailing content accepted")
			}
		})
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
	if err := s.Save(ctx, sample(t)); !errors.Is(err, context.Canceled) {
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
