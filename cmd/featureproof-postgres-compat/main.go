// Command featureproof-postgres-compat exercises one compiled migration manifest against an explicitly authorized gate database.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	controlplanepg "github.com/asierhr/featureProof/internal/controlplane/postgres"
)

// main reports a bounded compatibility failure without exposing the DSN.
func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run opens only the gate-authorized DSN and executes this artifact's compiled Migrate or Ready behavior.
func run(ctx context.Context, arguments []string, getenv func(string) string) error {
	if len(arguments) != 1 || getenv == nil || arguments[0] != "migrate" && arguments[0] != "ready" {
		return errors.New("usage: featureproof-postgres-compat <migrate|ready>")
	}
	dsn := getenv("FEATUREPROOF_POSTGRES_TEST_DSN")
	if dsn == "" || getenv("FEATUREPROOF_POSTGRES_GATE_AUTHORIZED") != "true" {
		return errors.New("explicit authorized gate DSN required")
	}
	store, err := controlplanepg.Open(ctx, controlplanepg.Config{DSN: dsn, BackendIdentity: "compatibility-gate", MaxConnections: 2, StatementTimeout: 5 * time.Second, MaxLease: time.Minute})
	if err != nil {
		return err
	}
	defer store.Close()
	if arguments[0] == "migrate" {
		if err = store.Migrate(ctx); err != nil {
			return err
		}
	}
	return store.Ready(ctx)
}
