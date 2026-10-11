package tools

import (
	"context"
	"errors"
	"github.com/asierhr/featureProof/internal/harness"
	"testing"
)

func TestRegistryIsExplicitAndValidatesInputs(t *testing.T) {
	fake := &Fake{}
	r, err := NewRegistry(harness.ToolSpec{Name: "probe", Tool: fake, Validate: func(in map[string]any) error {
		if in["target"] == nil {
			return context.Canceled
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Resolve("missing"); ok {
		t.Fatal("unknown tool resolved")
	}
	spec, ok := r.Resolve("probe")
	if !ok || spec.Validate(map[string]any{}) == nil {
		t.Fatal("input contract not enforced")
	}
}

func TestRegistryRejectsInvalidAndDuplicateSpecs(t *testing.T) {
	fake := &Fake{}
	if _, err := NewRegistry(harness.ToolSpec{}); err == nil {
		t.Fatal("invalid spec accepted")
	}
	spec := harness.ToolSpec{Name: "probe", Tool: fake, Validate: func(map[string]any) error { return nil }}
	if _, err := NewRegistry(spec, spec); err == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestRegistryDescriptorsAreStable(t *testing.T) {
	fake := &Fake{}
	validate := func(map[string]any) error { return nil }
	r, _ := NewRegistry(harness.ToolSpec{Name: "z", Tool: fake, Validate: validate}, harness.ToolSpec{Name: "a", Tool: fake, Validate: validate})
	d := r.Descriptors()
	if len(d) != 2 || d[0].Name != "a" || d[1].Name != "z" {
		t.Fatalf("descriptors=%v", d)
	}
}

func TestFakeToolReturnsProgramAndCancellation(t *testing.T) {
	f := &Fake{Results: []harness.ToolResult{{Output: map[string]any{"ok": true}}}, Errors: []error{nil}}
	got, err := f.Execute(context.Background(), harness.ToolCall{ID: "c"})
	if err != nil || got.Output["ok"] != true || f.CallCount() != 1 {
		t.Fatalf("got=%+v calls=%d err=%v", got, f.CallCount(), err)
	}
	f.Block = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = f.Execute(ctx, harness.ToolCall{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
