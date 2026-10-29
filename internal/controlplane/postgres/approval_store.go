package postgres

import "github.com/asierhr/featureProof/internal/planapproval"

// ApprovalStore returns the approval-owned PostgreSQL adapter over the shared pool.
func (store *Store) ApprovalStore() (*planapproval.PostgresApprovalStore, error) {
	return planapproval.NewFencedPostgresApprovalStore(store.pool, store.backendIdentity)
}
