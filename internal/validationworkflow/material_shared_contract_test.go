package validationworkflow_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/asierhr/featureProof/internal/controlplane/contracttest"
	"github.com/asierhr/featureProof/internal/validationworkflow"
)

// TestLocalWorkflowMaterialSharedContract executes the backend-neutral public
// artifact and private executable suite against reopened filesystem stores.
func TestLocalWorkflowMaterialSharedContract(t *testing.T) {
	root := t.TempDir()
	factory := func() (validationworkflow.PlanArtifactStore, validationworkflow.ExecutablePlanStore, error) {
		artifacts, err := validationworkflow.OpenLocalArtifactStore(filepath.Join(root, "plans"))
		if err != nil {
			return nil, nil, err
		}
		executables, err := validationworkflow.OpenLocalExecutableStore(filepath.Join(root, "private"))
		return artifacts, executables, err
	}
	if err := contracttest.RunWorkflowMaterialStores(context.Background(), factory, "local-material-contract"); err != nil {
		t.Fatal(err)
	}
}
