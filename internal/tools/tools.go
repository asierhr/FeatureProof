package tools

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/asierhr/featureProof/internal/harness"
)

// Registry is the explicit capability boundary that prevents arbitrary tool execution.
type Registry struct{ specs map[string]harness.ToolSpec }

// NewRegistry validates and indexes the complete set of allowed tool capabilities.
func NewRegistry(specs ...harness.ToolSpec) (*Registry, error) {
	r := &Registry{specs: map[string]harness.ToolSpec{}}
	for _, s := range specs {
		if s.Name == "" || s.Tool == nil || s.Validate == nil {
			return nil, fmt.Errorf("tool name, validator and implementation are required")
		}
		if _, ok := r.specs[s.Name]; ok {
			return nil, fmt.Errorf("duplicate tool %s", s.Name)
		}
		r.specs[s.Name] = s
	}
	return r, nil
}

// Resolve returns a capability only when its exact registered name exists.
func (r *Registry) Resolve(name string) (harness.ToolSpec, bool) {
	s, ok := r.specs[name]
	return s, ok
}

// Descriptors returns a stable model-facing capability list without implementations.
func (r *Registry) Descriptors() []harness.ToolDescriptor {
	names := make([]string, 0, len(r.specs))
	for n := range r.specs {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]harness.ToolDescriptor, 0, len(names))
	for _, n := range names {
		out = append(out, harness.ToolDescriptor{Name: n})
	}
	return out
}

// Fake is a programmable tool implementation for hermetic harness tests.
type Fake struct {
	mu        sync.Mutex
	Results   []harness.ToolResult
	Errors    []error
	Calls     []harness.ToolCall
	Block     <-chan struct{}
	OnExecute func()
}

// Execute records one authorized call and returns its programmed result or error.
func (f *Fake) Execute(ctx context.Context, c harness.ToolCall) (harness.ToolResult, error) {
	if f.OnExecute != nil {
		f.OnExecute()
	}
	if f.Block != nil {
		select {
		case <-ctx.Done():
			return harness.ToolResult{}, ctx.Err()
		case <-f.Block:
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, c)
	var result harness.ToolResult
	if len(f.Results) > 0 {
		result = f.Results[0]
		f.Results = f.Results[1:]
	}
	var err error
	if len(f.Errors) > 0 {
		err = f.Errors[0]
		f.Errors = f.Errors[1:]
	}
	return result, err
}

// CallCount reports how many tool executions reached this adapter.
func (f *Fake) CallCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.Calls) }
