//go:build !postgres_schema_n

package postgres

// embeddedMigrationLag identifies the current binary as schema N; the compat
// fixture overrides it to exercise a real N binary against schema N+1.
const embeddedMigrationLag = 0
