package evidence

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestEvidenceBindingIsSealedAndVerified covers the metadata integrity boundary.
func TestEvidenceBindingIsSealedAndVerified(t *testing.T) {
	clean := SanitizedValue{value: map[string]any{"status": 200}, complete: true}
	record, err := NewRecord(Record{ID: "e1", RunID: "run"}, clean, clean)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	bound, err := BindRecord(record, "run", digest)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), bound); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Get(context.Background(), "e1")
	if err != nil || loaded.BindingSHA256 != digest {
		t.Fatalf("loaded binding=%q err=%v", loaded.BindingSHA256, err)
	}
	bound.BindingSHA256 = strings.Repeat("b", 64)
	if err := store.Save(context.Background(), bound); !errors.Is(err, ErrUntreated) {
		t.Fatalf("mutated binding accepted: %v", err)
	}
	if _, err := BindRecord(record, "foreign", digest); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("foreign run accepted: %v", err)
	}
}
