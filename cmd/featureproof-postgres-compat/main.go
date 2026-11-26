// Command featureproof-postgres-compat exercises one compiled migration manifest against an explicitly authorized gate database.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
	controlplanepg "github.com/asierhr/featureProof/internal/controlplane/postgres"
	"github.com/jackc/pgx/v5/pgconn"
)

// main reports a bounded compatibility failure without exposing the DSN.
func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, compatibilityDiagnostic(err))
		os.Exit(1)
	}
}

// compatibilityDiagnostic exposes only the closed operation, SQLSTATE and constraint identifier needed to repair an isolated gate migration.
func compatibilityDiagnostic(err error) string {
	var classified *controlplane.Error
	var postgresError *pgconn.PgError
	if errors.As(err, &classified) && errors.As(classified.Cause, &postgresError) {
		return fmt.Sprintf("%s sqlstate=%s table=%s constraint=%s detail=%s", classified.Error(), postgresError.Code, postgresError.TableName, postgresError.ConstraintName, postgresError.Detail)
	}
	return err.Error()
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
	store, err := controlplanepg.Open(ctx, controlplanepg.Config{DSN: dsn, BackendIdentity: "compatibility-gate", MaxConnections: 2, StatementTimeout: 5 * time.Second, MaxLease: time.Minute, IsolatedLoopbackTest: true})
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
