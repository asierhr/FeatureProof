package previewenv

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/asierhr/featureProof/internal/externalagent"
	"github.com/asierhr/featureProof/internal/githubpr"
)

// ConsumerRequest is the typed trusted value stored behind external transport selections.
type ConsumerRequest struct {
	Selection Selection
	Expected  ExpectedRevision
}

// ExternalExecutor adapts the shared preview lifecycle to externalagent.ApprovedExecutor.
type ExternalExecutor struct {
	Coordinator *Coordinator
	Recovery    *RecoverySupervisor
}

// Prepare persists the preview intent synchronously before externalagent reports queued.
func (e ExternalExecutor) Prepare(ctx context.Context, selected externalagent.Selection, runID string) error {
	request, ok := selected.Value.(ConsumerRequest)
	if !ok || e.Coordinator == nil || selected.Alias != request.Selection.Alias {
		return &Error{Code: CodeUnauthorized, Operation: "external_selection"}
	}
	return e.Coordinator.Prepare(ctx, runID, request.Selection, request.Expected)
}

// Execute distinguishes pre-admission errors from admitted operational terminal outcomes.
func (e ExternalExecutor) Execute(ctx context.Context, selected externalagent.Selection, runID string) (externalagent.ExecutionResult, error) {
	request, ok := selected.Value.(ConsumerRequest)
	if !ok || e.Coordinator == nil || selected.Alias != request.Selection.Alias {
		return externalagent.ExecutionResult{Status: externalagent.OutcomeError}, &Error{Code: CodeUnauthorized, Operation: "external_selection"}
	}
	result, err := e.Coordinator.Run(ctx, runID, request.Selection, request.Expected)
	if err != nil {
		var preview *Error
		if errors.As(err, &preview) && preview.Admitted {
			if IsCode(err, CodeCancelled) {
				return externalagent.ExecutionResult{Status: externalagent.OutcomeCancelled, PackageDir: result.PackageDir}, nil
			}
			return externalagent.ExecutionResult{Status: externalagent.OutcomeInconclusive, PackageDir: result.PackageDir}, nil
		}
		return externalagent.ExecutionResult{Status: externalagent.OutcomeError}, err
	}
	return externalagent.ExecutionResult{Status: externalOutcome(result.Status), PackageDir: result.PackageDir}, nil
}

// Close joins scheduled crash recovery owned by the external-agent composition.
func (e ExternalExecutor) Close(ctx context.Context) error { return e.Recovery.Close(ctx) }

// externalOutcome preserves deterministic functional failure while mapping closed status vocabularies.
func externalOutcome(status ResultStatus) externalagent.Outcome {
	switch status {
	case StatusPassed:
		return externalagent.OutcomePassed
	case StatusFailed:
		return externalagent.OutcomeFailed
	case StatusCancelled:
		return externalagent.OutcomeCancelled
	default:
		return externalagent.OutcomeInconclusive
	}
}

// GitHubGateway owns joinable active workers while projecting lifecycle from durable preview records.
type GitHubGateway struct {
	Coordinator *Coordinator
	mu          sync.Mutex
	active      map[string]context.CancelFunc
	wg          sync.WaitGroup
	closed      bool
	recovery    *RecoverySupervisor
}

// AttachRecovery transfers one scheduled recovery worker to the GitHub gateway lifecycle.
func (g *GitHubGateway) AttachRecovery(recovery *RecoverySupervisor) { g.recovery = recovery }

// NewGitHubGateway constructs a durable transport projection over the shared lifecycle.
func NewGitHubGateway(coordinator *Coordinator) (*GitHubGateway, error) {
	if coordinator == nil {
		return nil, &Error{Code: CodeInvalid, Operation: "github_gateway"}
	}
	return &GitHubGateway{Coordinator: coordinator, active: map[string]context.CancelFunc{}}, nil
}

// Submit starts one owned asynchronous lifecycle so GitHub can poll or cancel while it runs.
func (g *GitHubGateway) Submit(ctx context.Context, selected githubpr.Selection, snapshot githubpr.PullRequestSnapshot, runHandle string) (githubpr.RunState, error) {
	request, ok := selected.Value.(ConsumerRequest)
	expected := ExpectedRevision{RepositoryID: strconv.FormatInt(snapshot.RepositoryID, 10), CommitOID: snapshot.HeadOID}
	if !ok || selected.Alias != request.Selection.Alias || request.Expected != expected || (snapshot.Fork && !request.Selection.Secretless) {
		return githubpr.RunState{}, &githubpr.Error{Code: githubpr.CodeUnauthorized, Operation: "preview_selection"}
	}
	if record, err := g.Coordinator.deps.Store.Load(context.Background(), runHandle); err == nil {
		if record.SelectionDigest != selectionDigest(request.Selection) || record.Selection != request.Selection || record.Expected != expected {
			return githubpr.RunState{}, &githubpr.Error{Code: githubpr.CodeConflict, Operation: "preview_identity"}
		}
		return githubState(record), nil
	}
	if err := g.Coordinator.Prepare(ctx, runHandle, request.Selection, expected); err != nil {
		return githubpr.RunState{}, mapGitHubError(err)
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return githubpr.RunState{}, &githubpr.Error{Code: githubpr.CodeCancelled, Operation: "preview_closed"}
	}
	if _, exists := g.active[runHandle]; exists {
		g.mu.Unlock()
		return githubpr.RunState{RunHandle: runHandle, RepositoryID: snapshot.RepositoryID, HeadOID: snapshot.HeadOID, Phase: "running"}, nil
	}
	runCtx, cancel := context.WithCancel(context.Background())
	g.active[runHandle] = cancel
	g.wg.Add(1)
	g.mu.Unlock()
	go func() {
		defer g.wg.Done()
		_, _ = g.Coordinator.Run(runCtx, runHandle, request.Selection, expected)
		g.mu.Lock()
		delete(g.active, runHandle)
		g.mu.Unlock()
	}()
	return githubpr.RunState{RunHandle: runHandle, RepositoryID: snapshot.RepositoryID, HeadOID: snapshot.HeadOID, Phase: "running"}, nil
}

// ResolveExisting verifies durable selection and revision lineage before association.
func (g *GitHubGateway) ResolveExisting(ctx context.Context, lookup githubpr.ExistingRunLookup) (githubpr.RunState, error) {
	record, err := g.Coordinator.deps.Store.Load(ctx, lookup.RunHandle)
	if err != nil || record.Selection.Alias != lookup.SelectionAlias || record.Expected.RepositoryID != strconv.FormatInt(lookup.RepositoryID, 10) || record.Expected.CommitOID != lookup.HeadOID {
		return githubpr.RunState{}, &githubpr.Error{Code: githubpr.CodeUnauthorized, Operation: "preview_lineage"}
	}
	return githubState(record), nil
}

// Status reconstructs active and post-restart state from the durable preview store.
func (g *GitHubGateway) Status(ctx context.Context, runHandle string) (githubpr.RunState, error) {
	record, err := g.Coordinator.deps.Store.Load(ctx, runHandle)
	if err != nil {
		g.mu.Lock()
		_, active := g.active[runHandle]
		g.mu.Unlock()
		if active {
			return githubpr.RunState{RunHandle: runHandle, Phase: "running"}, nil
		}
		return githubpr.RunState{}, &githubpr.Error{Code: githubpr.CodeUnauthorized, Operation: "preview_run"}
	}
	return githubState(record), nil
}

// Cancel durably records cancellation and interrupts the local worker when present.
func (g *GitHubGateway) Cancel(ctx context.Context, runHandle, _ string) error {
	if err := g.Coordinator.deps.Store.RequestCancel(ctx, runHandle, g.Coordinator.deps.Clock.Now()); err != nil {
		return &githubpr.Error{Code: githubpr.CodeUnauthorized, Operation: "preview_run"}
	}
	g.mu.Lock()
	cancel := g.active[runHandle]
	g.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// Close cancels and joins every gateway worker within the caller deadline.
func (g *GitHubGateway) Close(ctx context.Context) error {
	g.mu.Lock()
	g.closed = true
	for _, cancel := range g.active {
		cancel()
	}
	g.mu.Unlock()
	if err := g.Wait(ctx); err != nil {
		return err
	}
	return g.recovery.Close(ctx)
}

// Wait joins current gateway workers without initiating cancellation.
func (g *GitHubGateway) Wait(ctx context.Context) error {
	for {
		g.mu.Lock()
		active := len(g.active)
		g.mu.Unlock()
		if active == 0 {
			g.wg.Wait()
			return nil
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return &Error{Code: CodeTimeout, Operation: "github_shutdown"}
		case <-timer.C:
		}
	}
}

// githubState reconstructs a bounded GitHub projection from durable state.
func githubState(record RunRecord) githubpr.RunState {
	phase := "running"
	if record.Terminal {
		phase = "terminal"
	}
	repositoryID, _ := strconv.ParseInt(record.Expected.RepositoryID, 10, 64)
	state := githubpr.RunState{RunHandle: record.RunID, RepositoryID: repositoryID, HeadOID: record.Expected.CommitOID, Phase: phase, Outcome: string(record.Result.Status), Package: record.Result.PackageDir}
	if record.Result.BindingSHA256 != "" && record.Result.PackageDir != "" {
		state.PackageRunID, state.BindingRunID, state.BindingSHA256 = record.RunID, record.RunID, record.Result.BindingSHA256
	}
	return state
}

// mapGitHubError reduces preview operational failures to GitHub integration categories.
func mapGitHubError(err error) error {
	if IsCode(err, CodeCancelled) {
		return &githubpr.Error{Code: githubpr.CodeCancelled, Operation: "preview"}
	}
	if IsCode(err, CodeUnavailable) || IsCode(err, CodeTimeout) || IsCode(err, CodeReleasePending) {
		return &githubpr.Error{Code: githubpr.CodeTransient, Operation: "preview"}
	}
	if IsCode(err, CodeUnauthorized) {
		return &githubpr.Error{Code: githubpr.CodeUnauthorized, Operation: "preview"}
	}
	return &githubpr.Error{Code: githubpr.CodeIntegrity, Operation: "preview"}
}
