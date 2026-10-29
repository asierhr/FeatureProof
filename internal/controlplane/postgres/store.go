package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config contains explicit non-secret PostgreSQL adapter limits plus its DSN at composition.
type Config struct {
	DSN              string
	BackendIdentity  string
	MaxConnections   int32
	StatementTimeout time.Duration
	MaxLease         time.Duration
	MinSchema        int
	MaxSchema        int
	MaxTxAttempts    int
	Observer         controlplane.Observer
}

// Store owns the private driver pool implementing consumer contracts.
type Store struct {
	pool            *pgxpool.Pool
	backendIdentity string
	maxLease        time.Duration
	minSchema       int
	maxSchema       int
	maxTxAttempts   int
	observer        controlplane.Observer
	fault           statementFault
}

// statementFault is a test-only deterministic transaction-boundary injector.
type statementFault func(string) error

// injectFault fails one named statement boundary without performing external I/O.
func (store *Store) injectFault(boundary string) error {
	if store != nil && store.fault != nil {
		return store.fault(boundary)
	}
	return nil
}

// Open validates composition, connects and verifies the selected PostgreSQL authority.
func Open(ctx context.Context, config Config) (*Store, error) {
	if config.DSN == "" || config.BackendIdentity == "" || config.MaxConnections <= 0 || config.StatementTimeout <= 0 || config.MaxLease <= 0 {
		return nil, controlplane.ErrInvalid
	}
	parsed, err := pgxpool.ParseConfig(config.DSN)
	if err != nil {
		return nil, controlplane.ErrInvalid
	}
	parsed.MaxConns = config.MaxConnections
	parsed.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SET statement_timeout = `+fmt.Sprint(config.StatementTimeout.Milliseconds()))
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, parsed)
	if err != nil {
		return nil, mapError(err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, mapError(err)
	}
	manifest, manifestErr := loadMigrations()
	if manifestErr != nil {
		pool.Close()
		return nil, manifestErr
	}
	if config.MinSchema == 0 {
		config.MinSchema = max(1, len(manifest)-1)
	}
	if config.MaxSchema == 0 {
		config.MaxSchema = len(manifest) + 1
	}
	if config.MaxTxAttempts == 0 {
		config.MaxTxAttempts = 3
	}
	if config.MinSchema <= 0 || config.MaxSchema < config.MinSchema || config.MaxTxAttempts <= 0 {
		pool.Close()
		return nil, controlplane.ErrInvalid
	}
	return &Store{pool: pool, backendIdentity: config.BackendIdentity, maxLease: config.MaxLease, minSchema: config.MinSchema, maxSchema: config.MaxSchema, maxTxAttempts: config.MaxTxAttempts, observer: config.Observer}, nil
}

// Close releases driver resources without owning caller process lifecycle.
func (store *Store) Close() {
	if store != nil && store.pool != nil {
		store.pool.Close()
	}
}

// Migrate applies the embedded compatible schema to this explicit backend.
func (store *Store) Migrate(ctx context.Context) error {
	if store == nil {
		return controlplane.ErrInvalid
	}
	started := time.Now()
	err := Migrate(ctx, store.pool)
	store.observe(ctx, controlplane.Observation{Operation: "migration.run", Category: errorCategory(err), Duration: time.Since(started)})
	return err
}

// canonical marshals a bounded provider-neutral document for persistence.
func canonical(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil || len(data) == 0 || len(data) > controlplane.MaxCanonicalBytes {
		return nil, controlplane.ErrInvalid
	}
	return data, nil
}

// mapError maps SQLSTATE categories without parsing provider message text.
func mapError(err error) error {
	return mapOperationError(err, "database")
}

// mapOperationError maps stable SQLSTATE and constraint identity without inspecting provider messages.
func mapOperationError(err error, operation string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return &controlplane.Error{Category: controlplane.CategoryCancelled, Operation: operation}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &controlplane.Error{Category: controlplane.CategoryDeadline, Operation: operation}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return &controlplane.Error{Category: controlplane.CategoryConflict, Operation: operation}
		case "23503", "23514":
			return &controlplane.Error{Category: controlplane.CategoryIntegrity, Operation: operation}
		case "40001", "40P01":
			return &controlplane.Error{Category: controlplane.CategoryTransient, Operation: operation}
		case "42501":
			return &controlplane.Error{Category: controlplane.CategoryUnauthorized, Operation: operation}
		case "42P01", "42703":
			return fmt.Errorf("%w: sqlstate %s", ErrSchema, pgErr.Code)
		}
	}
	return &controlplane.Error{Category: controlplane.CategoryUnavailable, Operation: operation}
}

// observe emits one best-effort content-free signal whose sink cannot affect authoritative behavior.
func (store *Store) observe(ctx context.Context, observation controlplane.Observation) {
	if store != nil && store.observer != nil {
		_ = store.observer.Observe(ctx, observation)
	}
}
