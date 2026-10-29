// Command featureproof-postgres-gate provisions and removes one ownership-verified PostgreSQL test database.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
)

// gateDatabasePattern limits destructive operations to UUID-named databases created by the gate.
var gateDatabasePattern = regexp.MustCompile(`^featureproof_gate_[a-f0-9]{32}$`)

// main exposes only create, verify and ownership-checked drop operations.
func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run validates explicit environment authority before performing one bounded gate lifecycle operation.
func run(ctx context.Context, arguments []string, getenv func(string) string) error {
	if len(arguments) != 3 || getenv == nil {
		return errors.New("usage: featureproof-postgres-gate <create|verify|drop> <database> <ownership-token>")
	}
	action, database, token := arguments[0], arguments[1], arguments[2]
	if !gateDatabasePattern.MatchString(database) || len(token) < 32 || len(token) > 128 {
		return errors.New("invalid owned gate identity")
	}
	adminDSN, targetDSN := getenv("FEATUREPROOF_POSTGRES_ADMIN_DSN"), getenv("FEATUREPROOF_POSTGRES_TEST_DSN")
	if adminDSN == "" || targetDSN == "" {
		return errors.New("explicit admin and target DSNs are required")
	}
	switch action {
	case "create":
		return createOwnedDatabase(ctx, adminDSN, targetDSN, database, token)
	case "verify":
		return verifyOwnedDatabase(ctx, targetDSN, database, token)
	case "drop":
		if err := verifyOwnedDatabase(ctx, targetDSN, database, token); err != nil {
			return err
		}
		return dropOwnedDatabase(ctx, adminDSN, database)
	default:
		return errors.New("unsupported gate operation")
	}
}

// createOwnedDatabase requires PostgreSQL 16, creates one exact database and records its unprivileged ownership token.
func createOwnedDatabase(ctx context.Context, adminDSN, targetDSN, database, token string) error {
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return errors.New("admin connection unavailable")
	}
	defer admin.Close(ctx)
	var major int
	if err = admin.QueryRow(ctx, `SELECT current_setting('server_version_num')::integer / 10000`).Scan(&major); err != nil || major != 16 {
		return errors.New("PostgreSQL 16 is required")
	}
	if _, err = admin.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{database}.Sanitize()+` TEMPLATE template0`); err != nil {
		return errors.New("owned database creation failed")
	}
	created := true
	defer func() {
		if created {
			_ = dropOwnedDatabase(context.Background(), adminDSN, database)
		}
	}()
	target, err := pgx.Connect(ctx, targetDSN)
	if err != nil {
		return errors.New("owned database connection failed")
	}
	defer target.Close(ctx)
	if _, err = target.Exec(ctx, `CREATE TABLE featureproof_gate_ownership (database_name text PRIMARY KEY, ownership_token text NOT NULL, created_at timestamptz NOT NULL)`); err != nil {
		return errors.New("ownership registration failed")
	}
	if _, err = target.Exec(ctx, `INSERT INTO featureproof_gate_ownership(database_name,ownership_token,created_at) VALUES($1,$2,$3)`, database, token, time.Now().UTC()); err != nil {
		return errors.New("ownership registration failed")
	}
	created = false
	return nil
}

// verifyOwnedDatabase proves the target database and token before tests or teardown can proceed.
func verifyOwnedDatabase(ctx context.Context, targetDSN, database, token string) error {
	target, err := pgx.Connect(ctx, targetDSN)
	if err != nil {
		return errors.New("owned database connection unavailable")
	}
	defer target.Close(ctx)
	var currentDatabase, recordedToken string
	if err = target.QueryRow(ctx, `SELECT current_database(),ownership_token FROM featureproof_gate_ownership WHERE database_name=$1`, database).Scan(&currentDatabase, &recordedToken); err != nil || currentDatabase != database || recordedToken != token {
		return errors.New("database ownership verification failed")
	}
	return nil
}

// dropOwnedDatabase terminates connections and drops only the previously pattern- and token-verified database.
func dropOwnedDatabase(ctx context.Context, adminDSN, database string) error {
	if !gateDatabasePattern.MatchString(database) {
		return errors.New("unsafe database identity")
	}
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return errors.New("admin connection unavailable during cleanup")
	}
	defer admin.Close(ctx)
	if _, err = admin.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid<>pg_backend_pid()`, database); err != nil {
		return errors.New("owned database connection cleanup failed")
	}
	if _, err = admin.Exec(ctx, `DROP DATABASE `+pgx.Identifier{database}.Sanitize()); err != nil {
		return errors.New("owned database cleanup failed")
	}
	return nil
}
