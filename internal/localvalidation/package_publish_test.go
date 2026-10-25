package localvalidation

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asierhr/featureProof/internal/planmanifest"
)

type packageCommitFault struct {
	FileSystem
	failAt  string
	corrupt bool
}

// TestPublisherRejectsInvalidPromotionIdentities proves hidden and canonical bundle names cannot alias or escape their runs directory.
func TestPublisherRejectsInvalidPromotionIdentities(t *testing.T) {
	root := t.TempDir()
	if _, err := NewPublisherWithFinalName(root, "run", "../escape"); err == nil {
		t.Fatal("publisher accepted an escaping final name")
	}
	path := filepath.Join(root, ".featureproof", "runs", "run")
	if err := PromoteBundleNoReplace(path, path); err == nil {
		t.Fatal("promotion accepted identical staging and final paths")
	}
}

func (f packageCommitFault) CommitNoReplace(from, to string) error {
	if strings.HasSuffix(to, f.failAt) && !f.corrupt {
		return errors.New("injected write failure")
	}
	if err := f.FileSystem.CommitNoReplace(from, to); err != nil {
		return err
	}
	if strings.HasSuffix(to, f.failAt) && f.corrupt {
		return os.WriteFile(filepath.Join(filepath.Dir(to), "report.md"), []byte("corrupt after inventory"), 0600)
	}
	return nil
}

func TestPublisherDoesNotCommitWhenPackageWriteOrValidationFails(t *testing.T) {
	for _, tc := range []struct {
		name, artifact string
		corrupt        bool
	}{{"package write", "package.json", false}, {"manifest write", "manifest.json", false}, {"validation after manifest", "manifest.json", true}} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			fs := packageCommitFault{FileSystem: OSFileSystem{}, failAt: tc.artifact, corrupt: tc.corrupt}
			publisher, err := NewPublisherWithFilesystem(root, "run", fs)
			if err != nil {
				t.Fatal(err)
			}
			if err := publishFixture(t, publisher, context.Background(), RunSnapshot{ID: "run", Status: ResultInconclusive}, planmanifest.Manifest{}, nil); err == nil {
				t.Fatal("publication succeeded")
			}
			if _, err := os.Stat(filepath.Join(root, ".featureproof", "runs", "run")); !os.IsNotExist(err) {
				t.Fatalf("final exposed: %v", err)
			}
		})
	}
}

func TestPublisherCommitsOnlyValidatedPackage(t *testing.T) {
	root := t.TempDir()
	publisher, err := NewPublisher(root, "run")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := RunSnapshot{ID: "run", Status: ResultInconclusive, TargetURL: "https://user:SECRET@example.invalid"}
	if err := publishFixture(t, publisher, context.Background(), snapshot, planmanifest.Manifest{}, nil); err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(root, ".featureproof", "runs", "run")
	index, err := ReadPackage(context.Background(), final)
	if err != nil || index.Version != 1 || index.Status != ResultInconclusive {
		t.Fatalf("index=%+v err=%v", index, err)
	}
	for _, name := range []string{"package.json", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(final, name)); err != nil {
			t.Fatal(err)
		}
	}
	files, err := os.ReadDir(final)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(final, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "example.invalid") {
			t.Fatalf("secret in %s", file.Name())
		}
	}
}

func TestRunnerPublishesOfflineReadableFunctionalPackages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	for _, tc := range []struct {
		name, target   string
		expected, code int
		status         ResultStatus
		failures       bool
	}{{"failed", server.URL, 201, 1, ResultFailed, true}, {"inconclusive", "http://127.0.0.1:1", 200, 2, ResultInconclusive, false}} {
		t.Run(tc.name, func(t *testing.T) {
			runner, root, stdout, stderr := runnerFor(t, tc.target, tc.expected, 1)
			if code := runner.Run(context.Background()); code != tc.code {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout, stderr)
			}
			bundle := filepath.Join(root, ".featureproof", "runs", "id-1")
			index, err := ReadPackage(context.Background(), bundle)
			if err != nil || index.Status != tc.status || ((len(index.Failures) > 0) != tc.failures) {
				t.Fatalf("index=%+v err=%v", index, err)
			}
			if len(index.Reproduction) != 1 || index.Reproduction[0].CommitOID != index.Binding.CommitOID {
				t.Fatalf("reproduction=%+v", index.Reproduction)
			}
			if tc.failures {
				raw, err := os.ReadFile(filepath.Join(bundle, "report.json"))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), `"Expected"`) || strings.Contains(string(raw), `"Observed"`) {
					t.Fatal("report retained payload")
				}
			}
			if err := os.WriteFile(filepath.Join(bundle, "report.md"), []byte("mutated"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadPackage(context.Background(), bundle); !IsPackageError(err, "digest_mismatch") {
				t.Fatal(err)
			}
		})
	}
}
