package validationworkflow

import (
	"context"
	"path/filepath"
	"sync"

	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/planning"
)

// LocalRunnerConfiguration binds one safe runtime digest to existing runner inputs and dependencies.
type LocalRunnerConfiguration struct {
	Digest       string
	Request      localvalidation.PreparedRequest
	Dependencies localvalidation.RuntimeDependencies
}

// LocalExecutionPort resolves trusted runtime configurations immediately before deterministic execution.
type LocalExecutionPort struct {
	Configurations map[string]LocalRunnerConfiguration
}

// Execute invokes localvalidation.NewPreparedRunner and returns its canonical package directory.
func (p LocalExecutionPort) Execute(ctx context.Context, request ExecutionRequest) (string, error) {
	configuration, ok := p.Configurations[request.Snapshot.RuntimeDigest]
	if !ok || configuration.Digest != request.Snapshot.RuntimeDigest {
		return "", &Error{Code: CodeUnauthorized, Operation: "runtime_profile"}
	}
	prepared := configuration.Request
	prepared.Plans = append([]planning.ValidationPlan(nil), request.Plans...)
	prepared.ApprovalMode, prepared.ApprovalReference = request.ApprovalMode, request.ApprovalReference
	prepared.PlanArtifactSHA256 = request.ArtifactSHA256
	prepared.PlanCommitmentKey = append([]byte(nil), request.CommitmentKey...)
	deps := configuration.Dependencies
	deps.IDs = &workflowRunIDs{first: request.Handle, next: deps.IDs}
	runner, err := localvalidation.NewPreparedRunner(ctx, prepared, deps)
	if err != nil {
		return "", &Error{Code: CodeInvalid, Operation: "runner_composition"}
	}
	code := runner.Run(ctx)
	if code != 0 && code != 1 && code != 2 && code != 130 {
		return "", &Error{Code: CodeUnavailable, Operation: "runner"}
	}
	return filepath.Join(deps.Root, ".featureproof", "runs", request.Handle), nil
}

// workflowRunIDs fixes the package run ID to the workflow handle and delegates subsequent evidence IDs.
type workflowRunIDs struct {
	mu    sync.Mutex
	first string
	used  bool
	next  harness.IDGenerator
}

// Next returns the workflow handle once so package lineage matches orchestration identity.
func (i *workflowRunIDs) Next() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.used {
		i.used = true
		return i.first
	}
	if i.next == nil {
		return ""
	}
	return i.next.Next()
}
