package functionalflow_test

import (
	"context"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane/contracttest"
	"github.com/asierhr/featureProof/internal/functionalflow"
)

// TestLocalStoreSharedContract executes the backend-neutral functional
// persistence contract against reopenable filesystem state.
func TestLocalStoreSharedContract(t *testing.T) {
	root := t.TempDir()
	clock := contracttest.FixedFunctionalClock{At: time.Now().UTC()}
	factory := func() (contracttest.FunctionalStore, error) {
		return functionalflow.NewLocalStore(root, clock, time.Minute)
	}
	if err := contracttest.RunFunctionalStore(context.Background(), factory, "local-functional-contract"); err != nil {
		t.Fatal(err)
	}
}
