package contractcatalog

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// externalCapabilityProbe is the deliberately forbidden capability vocabulary sought by the adversarial source.
type externalCapabilityProbe interface {
	Network()
	Credentials()
	Process()
	Tool()
}

// countingCapabilityProbe proves the adversarial adapter would observe and invoke every forbidden port if composition supplied one.
type countingCapabilityProbe struct{ calls atomic.Int32 }

// Network records forbidden network availability.
func (p *countingCapabilityProbe) Network() { p.calls.Add(1) }

// Credentials records forbidden credential-store availability.
func (p *countingCapabilityProbe) Credentials() { p.calls.Add(1) }

// Process records forbidden process-runner availability.
func (p *countingCapabilityProbe) Process() { p.calls.Add(1) }

// Tool records forbidden executable-tool availability.
func (p *countingCapabilityProbe) Tool() { p.calls.Add(1) }

// capabilitySeekingSource actively invokes forbidden capabilities when present instead of relying on an inert counter.
type capabilitySeekingSource struct{ snapshot SourceSnapshot }

// Load fails after invoking every discovered forbidden port and otherwise returns one valid passive snapshot.
func (s capabilitySeekingSource) Load(_ context.Context, request SourceRequest) (SourceSnapshot, error) {
	if probe, available := request.Input().(externalCapabilityProbe); available {
		probe.Network()
		probe.Credentials()
		probe.Process()
		probe.Tool()
		return SourceSnapshot{}, errors.New("forbidden capability available")
	}
	return cloneSnapshot(s.snapshot), nil
}

// TestCoreAndAdaptersOwnNoExternalCapabilityPorts combines an executable adversary with an AST import boundary.
func TestCoreAndAdaptersOwnNoExternalCapabilityPorts(t *testing.T) {
	snapshot := testSnapshot("passive", "probe", ClassDerived, ConfidenceMedium, CompletenessPartial, OperationKey{"GET", "/pets"})
	registry, err := NewRegistry(map[string]ContractSource{"probe": capabilitySeekingSource{snapshot: snapshot}})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := descriptorFor(snapshot)
	descriptor.Input = struct{}{}
	if _, err = registry.Load(context.Background(), []SourceRequest{NewSourceRequest(descriptor)}, catalogLimits()); err != nil {
		t.Fatalf("passive descriptor exposed a capability: %v", err)
	}
	probe := &countingCapabilityProbe{}
	descriptor.Input = probe
	if _, err = registry.Load(context.Background(), []SourceRequest{NewSourceRequest(descriptor)}, catalogLimits()); err == nil || probe.calls.Load() != 4 {
		t.Fatalf("adversarial adapter was ineffective: calls=%d err=%v", probe.calls.Load(), err)
	}

	_, current, _, _ := runtime.Caller(0)
	repository := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	files := []string{}
	entries, err := os.ReadDir(filepath.Join(repository, "internal", "contractcatalog"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), "_test.go") {
			files = append(files, filepath.Join(repository, "internal", "contractcatalog", entry.Name()))
		}
	}
	files = append(files, filepath.Join(repository, "internal", "openapi", "contract_source.go"))
	for _, path := range files {
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		for _, spec := range parsed.Imports {
			importPath := strings.Trim(spec.Path.Value, `"`)
			if forbiddenCapabilityImport(importPath) {
				t.Fatalf("external capability import %q in %s", importPath, path)
			}
		}
	}
}

// forbiddenCapabilityImport identifies runtime ports that passive contract core and adapters must never own.
func forbiddenCapabilityImport(path string) bool {
	lower := strings.ToLower(path)
	return path == "net/http" || path == "os/exec" || strings.Contains(path, "/internal/tools") || strings.Contains(path, "/oauth") || strings.Contains(lower, "credential") || strings.Contains(lower, "process")
}
