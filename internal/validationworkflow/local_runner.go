package validationworkflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/revisionbinding"
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

// Execute invokes localvalidation.NewPreparedRunner with a hidden final name and returns a private complete bundle.
func (p LocalExecutionPort) Execute(ctx context.Context, request ExecutionRequest) (ExecutionPreparation, error) {
	configuration, ok := p.Configurations[request.Snapshot.RuntimeDigest]
	if !ok || configuration.Digest != request.Snapshot.RuntimeDigest {
		return ExecutionPreparation{}, &Error{Code: CodeUnauthorized, Operation: "runtime_profile"}
	}
	prepared := configuration.Request
	targetDigest, targetErr := TargetContextDigest(request.Snapshot.Binding.Target)
	if targetErr != nil || targetDigest != request.Snapshot.TargetDigest {
		return ExecutionPreparation{}, &Error{Code: CodeUnauthorized, Operation: "execution_target"}
	}
	prepared.Target = request.Snapshot.Binding.Target.URL
	prepared.RepositoryID = request.Snapshot.Binding.Revision.RepositoryID
	prepared.Revision = request.Snapshot.Binding.Revision.CommitOID
	prepared.EnvironmentID = request.Snapshot.Binding.Target.EnvironmentID
	prepared.Plans = append([]planning.ValidationPlan(nil), request.Plans...)
	prepared.ApprovalMode, prepared.ApprovalReference = request.ApprovalMode, request.ApprovalReference
	prepared.PlanArtifactSHA256 = request.ArtifactSHA256
	prepared.PlanCommitmentKey = append([]byte(nil), request.CommitmentKey...)
	deps := configuration.Dependencies
	deps.EnvironmentAttestor = snapshotTargetAttestor{target: request.Snapshot.Binding.Target}
	deps.IDs = &workflowRunIDs{first: request.Handle, next: deps.IDs}
	hiddenName := "." + request.Handle + ".workflow-primary"
	preparation := ExecutionPreparation{StagedDir: filepath.Join(deps.Root, ".featureproof", "runs", hiddenName), FinalDir: filepath.Join(deps.Root, ".featureproof", "runs", request.Handle)}
	if _, stagedErr := os.Stat(preparation.StagedDir); stagedErr == nil {
		return preparation, nil
	} else if !errors.Is(stagedErr, os.ErrNotExist) {
		return ExecutionPreparation{}, &Error{Code: CodeUnavailable, Operation: "runner_stage"}
	}
	if _, finalErr := os.Stat(preparation.FinalDir); finalErr == nil {
		return preparation, nil
	} else if !errors.Is(finalErr, os.ErrNotExist) {
		return ExecutionPreparation{}, &Error{Code: CodeUnavailable, Operation: "runner_stage"}
	}
	deps.PublisherFactory = func(root, runID string) (*localvalidation.Publisher, error) {
		return localvalidation.NewPublisherWithFinalName(root, runID, hiddenName)
	}
	runner, err := localvalidation.NewPreparedRunner(ctx, prepared, deps)
	if err != nil {
		return ExecutionPreparation{}, &Error{Code: CodeInvalid, Operation: "runner_composition"}
	}
	code := runner.Run(ctx)
	if code != 0 && code != 1 && code != 2 && code != 130 {
		return ExecutionPreparation{}, &Error{Code: CodeUnavailable, Operation: "runner"}
	}
	return preparation, nil
}

// snapshotTargetAttestor revalidates the execution runner against the same acquired target already sealed into the workflow snapshot.
type snapshotTargetAttestor struct{ target TargetContext }

// Attest projects the acquired target proof only for its exact environment identity.
func (a snapshotTargetAttestor) Attest(ctx context.Context, environmentID string) (revisionbinding.Attestation, error) {
	if err := ctx.Err(); err != nil {
		return revisionbinding.Attestation{}, err
	}
	if environmentID != a.target.EnvironmentID || !a.target.Attestation.Verified {
		return revisionbinding.Attestation{}, errors.New("target attestation unavailable")
	}
	return revisionbinding.Attestation{EnvironmentID: a.target.Attestation.EnvironmentID, RepositoryID: a.target.Attestation.RepositoryID, CommitOID: a.target.Attestation.CommitOID, Reference: a.target.Attestation.Reference, ValidUntil: a.target.Attestation.ValidUntil, Verified: true}, nil
}

// Promote atomically exposes a complete hidden runner bundle under its canonical run identity.
func (p LocalExecutionPort) Promote(ctx context.Context, preparation ExecutionPreparation) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := localvalidation.PromoteBundleNoReplace(preparation.StagedDir, preparation.FinalDir); err != nil {
		return "", &Error{Code: CodeUnavailable, Operation: "runner_promote"}
	}
	return preparation.FinalDir, nil
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
