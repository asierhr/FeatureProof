package environment_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane/contracttest"
	"github.com/asierhr/featureProof/internal/environment"
)

// TestLocalEnvironmentStoreSharedContract executes the backend-neutral
// lifecycle suite against independently reopened filesystem handles.
func TestLocalEnvironmentStoreSharedContract(t *testing.T) {
	root := filepath.Join(t.TempDir(), "environment")
	factory := func() (contracttest.EnvironmentStore, error) { return environment.OpenLocalStore(root) }
	if err := contracttest.RunEnvironmentStore(context.Background(), factory, "local-environment-contract", "contract-project", time.Now().UTC().Truncate(time.Microsecond)); err != nil {
		t.Fatal(err)
	}
}
