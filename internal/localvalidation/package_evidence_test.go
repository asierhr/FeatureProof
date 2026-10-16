package localvalidation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asierhr/featureProof/internal/evidence"
)

func TestPackageEvidenceTableRequiresConfirmedMatchingFile(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := evidence.NewFileStore(filepath.Join(root, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := evidence.NewPolicy(evidence.Config{MaxBodyBytes: 5})
	if err != nil {
		t.Fatal(err)
	}
	clean, err := evidence.NewSanitizer(policy).Sanitize(ctx, map[string]any{"body": "123456789"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := evidence.NewRecord(evidence.Record{ID: "ev", RunID: "run", StepID: "step"}, clean, clean)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	record, err = evidence.BindRecord(record, "run", digest)
	if err != nil || store.Save(ctx, record) != nil {
		t.Fatal(err)
	}
	entries, facts, err := collectPackageEvidence(ctx, root, "run", digest, map[string]bool{"ev": true}, store)
	if err != nil || len(entries) != 1 || entries[0].Path != "evidence/ev.json" || len(facts) != 1 {
		t.Fatalf("entries=%+v facts=%+v err=%v", entries, facts, err)
	}
	for _, tc := range []struct {
		id, run, digest string
		want            string
	}{{"../ev", "run", digest, "broken_reference"}, {"ev", "other", digest, "binding_integrity"}, {"ev", "run", strings.Repeat("b", 64), "binding_integrity"}, {"missing", "run", digest, "missing_artifact"}} {
		_, _, err := collectPackageEvidence(ctx, root, tc.run, tc.digest, map[string]bool{tc.id: true}, store)
		if !IsPackageError(err, tc.want) {
			t.Fatalf("case=%+v err=%v", tc, err)
		}
	}
	if err := os.Rename(filepath.Join(root, "evidence", "ev.json"), filepath.Join(root, "evidence", "wrong.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := collectPackageEvidence(ctx, root, "run", digest, map[string]bool{"ev": true}, store); !IsPackageError(err, "missing_artifact") {
		t.Fatal(err)
	}
}
