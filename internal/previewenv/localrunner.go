package previewenv

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/revisionbinding"
)

// LocalRunnerProfile is trusted composition data for the shared deterministic runner.
type LocalRunnerProfile struct {
	SpecPath, PlanPath, Root string
	Dependencies             localvalidation.RuntimeDependencies
}

// LocalRunnerExecutor builds the existing runner exclusively from trusted profile and authorized preview data.
type LocalRunnerExecutor struct{ Profiles map[string]LocalRunnerProfile }

// Execute composes a bound localvalidation runner without accepting target or paths from transport input.
func (e LocalRunnerExecutor) Execute(ctx context.Context, execution AuthorizedExecution) (ExecutionResult, error) {
	profile, ok := e.Profiles[execution.Selection.ProfileID]
	if !ok || profile.SpecPath == "" || profile.PlanPath == "" || profile.Root == "" || execution.Attestor == nil {
		return ExecutionResult{}, &Error{Code: CodeUnauthorized, Operation: "runner_profile"}
	}
	deps := profile.Dependencies
	deps.Root = profile.Root
	deps.IDs = &ownedRunIDs{runID: execution.RunID}
	deps.RevisionResolver = fixedRevisionResolver{expected: execution.Expected}
	deps.EnvironmentAttestor = execution.Attestor
	runner, err := localvalidation.NewRunner(ctx, localvalidation.Request{SpecPath: profile.SpecPath, PlanPath: profile.PlanPath, Target: execution.Target.BaseURL, RepositoryID: execution.Expected.RepositoryID, Revision: execution.Expected.CommitOID, EnvironmentID: execution.Snapshot.EnvironmentID}, deps)
	if err != nil {
		return ExecutionResult{}, &Error{Code: CodeIntegrity, Operation: "runner_admission"}
	}
	status := runner.Run(ctx)
	result := ExecutionResult{PackageDir: filepath.Join(profile.Root, ".featureproof", "runs", execution.RunID)}
	switch status {
	case 0:
		result.Status = StatusPassed
	case 1:
		result.Status = StatusFailed
	case 2:
		result.Status = StatusInconclusive
	case 130:
		result.Status = StatusCancelled
	default:
		return ExecutionResult{}, &Error{Code: CodeUnavailable, Operation: "runner"}
	}
	index, err := localvalidation.ReadPackage(ctx, result.PackageDir)
	if err != nil || index.RunID != execution.RunID || index.Binding.RepositoryID != execution.Expected.RepositoryID || index.Binding.CommitOID != execution.Expected.CommitOID {
		return ExecutionResult{}, &Error{Code: CodeIntegrity, Operation: "package"}
	}
	result.BindingSHA256 = index.Binding.BindingSHA256
	return result, nil
}

// ownedRunIDs makes the first generated identity match the lifecycle and derives later local artifact IDs.
type ownedRunIDs struct {
	mu    sync.Mutex
	runID string
	next  int
}

// Next returns the authorized run identity first and bounded unique child identities thereafter.
func (id *ownedRunIDs) Next() string {
	id.mu.Lock()
	defer id.mu.Unlock()
	id.next++
	if id.next == 1 {
		return id.runID
	}
	return fmt.Sprintf("%s-%d", id.runID, id.next)
}

// fixedRevisionResolver proves the trusted expected revision without repository mutation.
type fixedRevisionResolver struct{ expected ExpectedRevision }

// Resolve accepts only the exact repository and immutable commit selected by the consumer.
func (r fixedRevisionResolver) Resolve(ctx context.Context, repositoryID, ref string) (revisionbinding.Revision, error) {
	if err := ctx.Err(); err != nil {
		return revisionbinding.Revision{}, err
	}
	if repositoryID != r.expected.RepositoryID || ref != r.expected.CommitOID {
		return revisionbinding.Revision{}, &Error{Code: CodeIntegrity, Operation: "revision"}
	}
	return revisionbinding.Revision{RepositoryID: repositoryID, CommitOID: ref}, nil
}
