// Package postgres provides PostgreSQL adapters behind control-plane contracts.
package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationFiles embeds immutable schema assets into every control-plane binary.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationPhase identifies the compatibility role of one schema transition.
type migrationPhase string

const (
	// phaseExpand introduces structures readable by adjacent binaries.
	phaseExpand migrationPhase = "expand"
	// phaseBackfill populates an expanded structure without contracting compatibility.
	phaseBackfill migrationPhase = "backfill"
	// phaseContract enables invariants after the backfill is complete.
	phaseContract migrationPhase = "contract"
	// migrationBoundaryTransaction requires every descriptor to commit atomically on its own.
	migrationBoundaryTransaction = "transaction"
	// supportedServerMajor pins the tested PostgreSQL server contract.
	supportedServerMajor = 16
	// migrationLockKey serializes migration runners at the session boundary.
	migrationLockKey int64 = 764238165042
)

// migration describes one ordered immutable schema transition and its execution boundary.
type migration struct {
	version  int
	name     string
	checksum string
	sql      string
	phase    migrationPhase
	boundary string
}

// migrationLedger is the fail-closed singleton state read with migration history.
type migrationLedger struct {
	currentVersion int
	dirty          bool
	targetVersion  int
	serverMajor    int
	minCompatible  int
	maxCompatible  int
}

// ErrSchema reports incompatible, incomplete or tampered migration history.
var ErrSchema = errors.New("postgres controlplane schema")

// loadMigrations validates a gap-free manifest and computes stable checksums and phases.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	result := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migration name: %w", ErrSchema)
		}
		version, parseErr := strconv.Atoi(prefix)
		if parseErr != nil || version <= 0 {
			return nil, fmt.Errorf("migration version: %w", ErrSchema)
		}
		body, readErr := migrationFiles.ReadFile("migrations/" + entry.Name())
		if readErr != nil {
			return nil, readErr
		}
		sum := sha256.Sum256(body)
		phase := phaseExpand
		if strings.Contains(entry.Name(), "backfill") {
			phase = phaseBackfill
		}
		if strings.Contains(entry.Name(), "contract") {
			phase = phaseContract
		}
		result = append(result, migration{version: version, name: entry.Name(), checksum: hex.EncodeToString(sum[:]), sql: string(body), phase: phase, boundary: migrationBoundaryTransaction})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].version < result[j].version })
	if embeddedMigrationLag < 0 || embeddedMigrationLag >= len(result) {
		return nil, fmt.Errorf("migration build window: %w", ErrSchema)
	}
	result = result[:len(result)-embeddedMigrationLag]
	for index, item := range result {
		if item.version != index+1 || item.boundary != migrationBoundaryTransaction {
			return nil, fmt.Errorf("migration gap or boundary: %w", ErrSchema)
		}
	}
	return result, nil
}

// Migrate applies each embedded descriptor in an independent transaction under a session advisory lock.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return ErrSchema
	}
	manifest, err := loadMigrations()
	if err != nil {
		return err
	}
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return mapOperationError(err, "migration.acquire")
	}
	defer connection.Release()
	if _, err = connection.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return mapOperationError(err, "migration.lock")
	}
	defer func() {
		_, _ = connection.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockKey)
	}()
	serverMajor, err := readServerMajor(ctx, connection)
	if err != nil {
		return err
	}
	if serverMajor != supportedServerMajor {
		return ErrSchema
	}
	if err = bootstrapLedger(ctx, connection, serverMajor); err != nil {
		return err
	}
	ledger, applied, err := readAndValidateLedger(ctx, connection, manifest, serverMajor)
	if err != nil {
		return err
	}
	if ledger.currentVersion > len(manifest) {
		return nil
	}
	for _, descriptor := range manifest[ledger.currentVersion:] {
		if err = markMigrationDirty(ctx, connection, descriptor.version); err != nil {
			return err
		}
		if err = applyMigration(ctx, connection, descriptor, serverMajor, len(manifest)); err != nil {
			return err
		}
		applied++
	}
	if applied != len(manifest) {
		return ErrSchema
	}
	return nil
}

// readServerMajor obtains the server compatibility identity without trusting caller configuration.
func readServerMajor(ctx context.Context, connection interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (int, error) {
	var major int
	if err := connection.QueryRow(ctx, `SELECT current_setting('server_version_num')::integer / 10000`).Scan(&major); err != nil {
		return 0, mapOperationError(err, "migration.server")
	}
	return major, nil
}

// bootstrapLedger creates and upgrades only the migration ledger in its own transaction.
func bootstrapLedger(ctx context.Context, connection *pgxpool.Conn, serverMajor int) error {
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapOperationError(err, "migration.bootstrap")
	}
	defer tx.Rollback(ctx)
	statements := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (version integer PRIMARY KEY, name text NOT NULL, checksum text NOT NULL, phase text NOT NULL DEFAULT 'expand', boundary text NOT NULL DEFAULT 'transaction', applied_at timestamptz NOT NULL DEFAULT now())`,
		`ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS phase text NOT NULL DEFAULT 'expand'`,
		`ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS boundary text NOT NULL DEFAULT 'transaction'`,
		`CREATE TABLE IF NOT EXISTS schema_migration_state (singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), current_version integer NOT NULL, dirty boolean NOT NULL, target_version integer NOT NULL, server_major integer NOT NULL, min_compatible integer NOT NULL, max_compatible integer NOT NULL, updated_at timestamptz NOT NULL DEFAULT now())`,
		`INSERT INTO schema_migration_state(singleton,current_version,dirty,target_version,server_major,min_compatible,max_compatible) SELECT true,COALESCE(max(version),0),false,0,$1,GREATEST(COALESCE(max(version),0)-1,1),GREATEST(COALESCE(max(version),0),1) FROM schema_migrations ON CONFLICT(singleton) DO NOTHING`,
	}
	for _, statement := range statements {
		if strings.Contains(statement, "$1") {
			_, err = tx.Exec(ctx, statement, serverMajor)
		} else {
			_, err = tx.Exec(ctx, statement)
		}
		if err != nil {
			return mapOperationError(err, "migration.bootstrap")
		}
	}
	return mapOperationError(tx.Commit(ctx), "migration.bootstrap")
}

// readAndValidateLedger proves gap-free exact history plus clean compatible singleton state.
func readAndValidateLedger(ctx context.Context, connection interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}, manifest []migration, serverMajor int) (migrationLedger, int, error) {
	var ledger migrationLedger
	if err := connection.QueryRow(ctx, `SELECT current_version,dirty,target_version,server_major,min_compatible,max_compatible FROM schema_migration_state WHERE singleton=true`).Scan(&ledger.currentVersion, &ledger.dirty, &ledger.targetVersion, &ledger.serverMajor, &ledger.minCompatible, &ledger.maxCompatible); err != nil {
		return ledger, 0, mapOperationError(err, "migration.ledger")
	}
	if ledger.dirty || ledger.targetVersion != 0 || ledger.serverMajor != serverMajor || ledger.currentVersion < 0 || ledger.minCompatible <= 0 || ledger.maxCompatible < ledger.minCompatible || ledger.currentVersion > len(manifest) && (len(manifest) < ledger.minCompatible || len(manifest) > ledger.maxCompatible) {
		return ledger, 0, ErrSchema
	}
	rows, err := connection.Query(ctx, `SELECT version,name,checksum,phase,boundary FROM schema_migrations ORDER BY version`)
	if err != nil {
		return ledger, 0, mapOperationError(err, "migration.history")
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		var version int
		var name, checksum, phase, boundary string
		if rows.Scan(&version, &name, &checksum, &phase, &boundary) != nil {
			return ledger, 0, ErrSchema
		}
		if version != index+1 {
			return ledger, 0, ErrSchema
		}
		if index < len(manifest) {
			expected := manifest[index]
			if version != expected.version || name != expected.name || checksum != expected.checksum || phase != string(expected.phase) || boundary != expected.boundary {
				return ledger, 0, ErrSchema
			}
		} else if name == "" || len(checksum) != 64 || (phase != string(phaseExpand) && phase != string(phaseBackfill) && phase != string(phaseContract)) || boundary != migrationBoundaryTransaction {
			return ledger, 0, ErrSchema
		}
		index++
	}
	if rows.Err() != nil {
		return ledger, 0, mapOperationError(rows.Err(), "migration.history")
	}
	if index != ledger.currentVersion {
		return ledger, 0, ErrSchema
	}
	return ledger, index, nil
}

// markMigrationDirty commits intent before executing a descriptor so crashes remain detectable.
func markMigrationDirty(ctx context.Context, connection *pgxpool.Conn, target int) error {
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapOperationError(err, "migration.mark_dirty")
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE schema_migration_state SET dirty=true,target_version=$1,updated_at=now() WHERE singleton=true AND dirty=false AND current_version=$1-1`, target)
	if err != nil {
		return mapOperationError(err, "migration.mark_dirty")
	}
	if tag.RowsAffected() != 1 {
		return ErrSchema
	}
	return mapOperationError(tx.Commit(ctx), "migration.mark_dirty")
}

// applyMigration commits schema, immutable descriptor and clean ledger state atomically.
func applyMigration(ctx context.Context, connection *pgxpool.Conn, descriptor migration, serverMajor, latest int) error {
	if descriptor.boundary != migrationBoundaryTransaction {
		return ErrSchema
	}
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapOperationError(err, "migration.apply")
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, descriptor.sql); err != nil {
		return mapOperationError(err, "migration.apply")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version,name,checksum,phase,boundary) VALUES($1,$2,$3,$4,$5)`, descriptor.version, descriptor.name, descriptor.checksum, descriptor.phase, descriptor.boundary); err != nil {
		return mapOperationError(err, "migration.record")
	}
	minCompatible := descriptor.version
	if descriptor.version == latest && descriptor.version > 1 {
		minCompatible = descriptor.version - 1
	}
	tag, err := tx.Exec(ctx, `UPDATE schema_migration_state SET current_version=$1,dirty=false,target_version=0,server_major=$2,min_compatible=$3,max_compatible=$1,updated_at=now() WHERE singleton=true AND dirty=true AND target_version=$1`, descriptor.version, serverMajor, minCompatible)
	if err != nil || tag.RowsAffected() != 1 {
		return ErrSchema
	}
	return mapOperationError(tx.Commit(ctx), "migration.apply")
}

// Ready verifies exact history and the binary's declared N/N+1 compatibility window.
func (store *Store) Ready(ctx context.Context) error {
	if store == nil || store.pool == nil || store.backendIdentity == "" {
		return controlplane.ErrInvalid
	}
	manifest, err := loadMigrations()
	if err != nil {
		return err
	}
	major, err := readServerMajor(ctx, store.pool)
	if err != nil {
		return err
	}
	ledger, _, err := readAndValidateLedger(ctx, store.pool, manifest, major)
	if err != nil {
		return err
	}
	if ledger.currentVersion < store.minSchema || ledger.currentVersion > store.maxSchema || store.maxSchema < ledger.minCompatible || store.minSchema > ledger.maxCompatible {
		return ErrSchema
	}
	return nil
}
