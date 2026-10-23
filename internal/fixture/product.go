package fixture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/asierhr/featureProof/internal/harness"
)

// SanitizerFunc adapts a trusted redaction function to the fixture sanitizer port.
type SanitizerFunc func(context.Context, any) (any, error)

// Sanitize delegates to the configured redaction function.
func (f SanitizerFunc) Sanitize(ctx context.Context, value any) (any, error) { return f(ctx, value) }

// WallClock supplies real time only at the productive composition boundary.
type WallClock struct{}

// Now returns current wall time for finite lease ownership.
func (WallClock) Now() time.Time { return time.Now() }

// ContextWaiter performs retry delay synchronously and observes cancellation without goroutines.
type ContextWaiter struct{}

// Wait blocks for the configured finite delay or returns the context cause.
func (ContextWaiter) Wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// DigestKeys derives stable opaque idempotency keys from non-secret logical coordinates.
type DigestKeys struct{ Namespace string }

// Key returns a SHA-256 key stable across retry and recovery of one logical phase.
func (k DigestKeys) Key(runID, fixtureID, phase string) string {
	sum := sha256.Sum256([]byte(k.Namespace + "\x00" + runID + "\x00" + fixtureID + "\x00" + phase))
	return hex.EncodeToString(sum[:])
}

// FiniteScopes creates cleanup-only contexts with explicit deadline, call, and retry budgets.
type FiniteScopes struct {
	Parent     context.Context
	Timeout    time.Duration
	MaxCalls   int
	MaxRetries int
}

// New returns a fresh scope independent of the cancelled primary run context.
func (s FiniteScopes) New(string) (CleanupScope, error) {
	if s.Parent == nil || s.Timeout <= 0 || s.MaxCalls < 0 || s.MaxRetries < 0 {
		return CleanupScope{}, &Error{Category: CategoryInvalid, Phase: "cleanup_scope"}
	}
	ctx, cancel := context.WithTimeout(s.Parent, s.Timeout)
	return CleanupScope{Context: ctx, Cancel: cancel, MaxCalls: s.MaxCalls, MaxRetries: s.MaxRetries}, nil
}

// SealedRevalidator rejects any drift from the recomputed binding immediately before effects.
type SealedRevalidator struct{}

// Revalidate checks the canonical set and exact operation binding for a closed phase.
func (SealedRevalidator) Revalidate(ctx context.Context, bound BoundSet, item BoundFixture, phase string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateBoundSet(bound); err != nil {
		return err
	}
	current, ok := boundFixtureByID(bound, item.Declaration.ID)
	if !ok || bindingForPhase(current, phase) == "invalid" || bindingForPhase(current, phase) != bindingForPhase(item, phase) {
		return &Error{Category: CategoryDenied, Phase: "revalidate", FixtureID: item.Declaration.ID}
	}
	return nil
}

// ProductRevalidator combines sealed registry authority, productive policy evaluation, and an atomic run budget.
type ProductRevalidator struct {
	PolicyFactory func() harness.Policy
	Limits        harness.Limits
	runs          map[string]*harness.Run
	mu            sync.Mutex
}

// Revalidate rejects drift, missing capability, policy denial, or budget exhaustion before the caller may cross network.
func (r *ProductRevalidator) Revalidate(ctx context.Context, bound BoundSet, item BoundFixture, phase string) error {
	operation := item.Create
	if phase == "cleanup" {
		operation = item.Cleanup
	}
	return r.RevalidateRequest(ctx, bound, item, phase, HTTPRequest{RunID: bound.RunID, FixtureID: item.Declaration.ID, Phase: phase, Method: operation.Method, PathTemplate: operation.Path, Identity: operation.Identity, BindingDigest: operation.BindingDigest, Attempt: 1})
}

// RevalidateRequest evaluates the exact resolved path, query, body, identity, and binding before reserving budget.
func (r *ProductRevalidator) RevalidateRequest(ctx context.Context, bound BoundSet, item BoundFixture, phase string, request HTTPRequest) error {
	if err := (SealedRevalidator{}).Revalidate(ctx, bound, item, phase); err != nil {
		return err
	}
	if r == nil || r.PolicyFactory == nil || r.Limits.MaxToolCalls <= 0 {
		return &Error{Category: CategoryDenied, Phase: "fixture_registry", FixtureID: item.Declaration.ID}
	}
	policy := r.PolicyFactory()
	if policy == nil {
		return &Error{Category: CategoryDenied, Phase: "fixture_policy", FixtureID: item.Declaration.ID}
	}
	path, err := expandPath(request.PathTemplate, request.PathParameters)
	if err != nil {
		if request.PathParameters != nil {
			return &Error{Category: CategoryDenied, Phase: "fixture_policy", FixtureID: item.Declaration.ID, Cause: err}
		}
		path = request.PathTemplate
	}
	input := map[string]any{"method": request.Method, "path": path, "query": request.Query, "body": request.Body, "binding_digest": request.BindingDigest}
	binding := request.Identity
	run, runErr := r.runFor(request.RunID)
	if runErr != nil {
		return runErr
	}
	decision, err := policy.Evaluate(ctx, run, harness.ToolCall{ID: item.Declaration.ID + ":" + phase, Tool: "http.request", RunID: bound.RunID, Attempt: request.Attempt, Input: input, IdentityBinding: &binding})
	if err != nil || !decision.Allowed {
		return &Error{Category: CategoryDenied, Phase: "fixture_policy", FixtureID: item.Declaration.ID, Cause: err}
	}
	r.mu.Lock()
	reserved := false
	if request.Attempt > 1 {
		_, reserved = run.ReserveRetryToolCall()
	} else {
		reserved = run.ReserveToolCall()
	}
	r.mu.Unlock()
	if !reserved {
		return &Error{Category: CategoryBudget, Phase: "fixture_budget", FixtureID: item.Declaration.ID}
	}
	return nil
}

// runFor isolates productive budget consumption by workflow handle instead of sharing an environment-wide aggregate.
func (r *ProductRevalidator) runFor(runID string) (*harness.Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runs == nil {
		r.runs = map[string]*harness.Run{}
	}
	if existing := r.runs[runID]; existing != nil {
		return existing, nil
	}
	run, err := harness.NewRun(runID, "fixture lifecycle", "fixture-policy", r.Limits)
	if err != nil {
		return nil, &Error{Category: CategoryDenied, Phase: "fixture_budget", Cause: err}
	}
	r.runs[runID] = run
	return run, nil
}

// FileObserver appends only allowlisted fixture events to one consumer-owned journal.
type FileObserver struct {
	Path string
	mu   sync.Mutex
}

// Record appends and synchronizes one payload-free event before returning confirmation.
func (o *FileObserver) Record(ctx context.Context, event Event) error {
	if o == nil || o.Path == "" || ctx.Err() != nil {
		return errors.New("fixture journal unavailable")
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(o.Path), 0o700); err != nil {
		return err
	}
	if event.ID != "" {
		if existing, err := os.ReadFile(o.Path); err == nil && bytes.Contains(existing, []byte(`"ID":"`+event.ID+`"`)) {
			return nil
		}
	}
	file, err := os.OpenFile(o.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err = file.Write(append(raw, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

// FileEvidenceSink sanitizes and atomically confirms bounded response material outside public projections.
type FileEvidenceSink struct {
	Root      string
	Sanitizer Sanitizer
}

// Confirm persists only sanitized evidence and returns its content digest as stable identifier.
func (s FileEvidenceSink) Confirm(ctx context.Context, runID, binding string, value map[string]any) (map[string]any, string, error) {
	if s.Root == "" || s.Sanitizer == nil || !safeRunID.MatchString(runID) || binding == "" {
		return nil, "", &Error{Category: CategoryInvalid, Phase: "evidence"}
	}
	safe, err := s.Sanitizer.Sanitize(ctx, value)
	if err != nil {
		return nil, "", err
	}
	projected, ok := safe.(map[string]any)
	if !ok {
		return nil, "", &Error{Category: CategoryInvalid, Phase: "evidence"}
	}
	raw, err := json.Marshal(projected)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(append([]byte(binding+"\x00"), raw...))
	id := hex.EncodeToString(sum[:])
	directory := filepath.Join(s.Root, runID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, "", err
	}
	temporary, err := os.CreateTemp(directory, ".evidence-*.tmp")
	if err != nil {
		return nil, "", err
	}
	name := temporary.Name()
	defer os.Remove(name)
	_ = temporary.Chmod(0o600)
	if _, err = temporary.Write(raw); err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, "", err
	}
	if err := replaceFile(name, filepath.Join(directory, id+".json")); err != nil {
		return nil, "", err
	}
	cloned, err := cloneJSON(projected)
	if err != nil {
		return nil, "", err
	}
	return cloned.(map[string]any), id, nil
}
