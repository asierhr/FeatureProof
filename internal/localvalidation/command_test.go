package localvalidation

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestValidateCommandRequiresExactFlagsBeforeEffects traces R1 through the side-effect-free parser and command boundary.
func TestValidateCommandRequiresExactFlagsBeforeEffects(t *testing.T) {
	valid, err := ParseRequest([]string{"validate", "--target", "http://127.0.0.1", "--spec", "s", "--plan", "p"})
	if err != nil || valid.SpecPath != "s" || valid.PlanPath != "p" {
		t.Fatalf("valid parse: %#v %v", valid, err)
	}
	invalid := [][]string{{}, {"other"}, {"validate"}, {"validate", "--spec", "s", "--spec", "x", "--plan", "p", "--target", "t"}, {"validate", "--unknown", "x"}, {"validate", "positional"}, {"validate", "--spec", ""}}
	for _, args := range invalid {
		if _, err := ParseRequest(args); err == nil {
			t.Fatalf("accepted %#v", args)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := RunCommand(context.Background(), []string{"validate"}, strings.NewReader(""), &stdout, &stderr); code != 3 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "featureproof_error=arguments") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

// TestRunCommandCompletesWithProductionBoundaries traces R1, R9, and R11 through the real entrypoint composition.
func TestRunCommandCompletesWithProductionBoundaries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	spec, plan := writeInputs(t, 200, 1)
	root := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(previous)
	var stdout, stderr bytes.Buffer
	code := RunCommand(context.Background(), []string{"validate", "--spec", spec, "--plan", plan, "--target", server.URL}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	entries, err := os.ReadDir(filepath.Join(root, ".featureproof", "runs"))
	if err != nil || len(entries) != 1 || strings.HasSuffix(entries[0].Name(), ".incomplete") {
		t.Fatalf("bundle=%v err=%v", entries, err)
	}
}

// TestProductionBoundaryUtilitiesRemainCancelable covers runtime helpers used only by the binary path.
func TestProductionBoundaryUtilitiesRemainCancelable(t *testing.T) {
	if (&ArgumentError{Field: "--spec", Category: "required"}).Error() == "" {
		t.Fatal("empty diagnostic")
	}
	if (systemClock{}).Now().IsZero() {
		t.Fatal("zero clock")
	}
	ids := randomIDs{}
	if first, second := ids.Next(), ids.Next(); first == "" || second == "" || first == second {
		t.Fatalf("ids %q %q", first, second)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (contextWaiter{}).Wait(ctx, time.Hour); err == nil {
		t.Fatal("wait ignored cancellation")
	}
	deps := DefaultDependencies(t.TempDir(), &bytes.Buffer{}, &bytes.Buffer{})
	if deps.Clock == nil || deps.Resolver == nil {
		t.Fatal("default dependencies incomplete")
	}
}

// TestCommandExitCodesForFunctionalOutcomes traces R9's stable mapping independently of rendering.
func TestCommandExitCodesForFunctionalOutcomes(t *testing.T) {
	for status, expected := range map[ResultStatus]int{ResultPassed: 0, ResultFailed: 1, ResultInconclusive: 2, ResultError: 3, ResultCancelled: 130} {
		if actual := ExitCode(status); actual != expected {
			t.Fatalf("%s: got %d want %d", status, actual, expected)
		}
	}
}
