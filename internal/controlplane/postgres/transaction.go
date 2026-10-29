package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
	"github.com/asierhr/featureProof/internal/correction"
	"github.com/asierhr/featureProof/internal/environment"
	cleanup "github.com/asierhr/featureProof/internal/environment/cleanup"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// retryTransaction reruns a complete short database callback on serialization/deadlock only.
func retryTransaction(ctx context.Context, pool *pgxpool.Pool, maxAttempts int, callback func(pgx.Tx) error) error {
	return retryTransactionObserved(ctx, pool, maxAttempts, "transaction", nil, callback)
}

// retryTransactionObserved retries the entire serializable callback and emits only allowlisted metadata after each attempt.
func retryTransactionObserved(ctx context.Context, pool *pgxpool.Pool, maxAttempts int, operation string, observer controlplane.Observer, callback func(pgx.Tx) error) error {
	if pool == nil || maxAttempts <= 0 || callback == nil {
		return controlplane.ErrInvalid
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		started := time.Now()
		if err := ctx.Err(); err != nil {
			return mapOperationError(err, operation)
		}
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			return mapError(err)
		}
		callbackErr := callback(tx)
		if callbackErr == nil {
			callbackErr = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
		if callbackErr == nil {
			if observer != nil {
				_ = observer.Observe(ctx, controlplane.Observation{Operation: operation, Attempt: attempt, Duration: time.Since(started)})
			}
			return nil
		}
		if isDomainError(callbackErr) {
			if observer != nil {
				_ = observer.Observe(ctx, controlplane.Observation{Operation: operation, Category: errorCategory(callbackErr), Attempt: attempt, Duration: time.Since(started)})
			}
			return callbackErr
		}
		var pgErr *pgconn.PgError
		if !errors.As(callbackErr, &pgErr) || (pgErr.Code != "40001" && pgErr.Code != "40P01") {
			mapped := mapOperationError(callbackErr, operation)
			if observer != nil {
				_ = observer.Observe(ctx, controlplane.Observation{Operation: operation, Category: errorCategory(mapped), Attempt: attempt, Duration: time.Since(started)})
			}
			return mapped
		}
		if observer != nil {
			_ = observer.Observe(ctx, controlplane.Observation{Operation: operation, Category: controlplane.CategoryTransient, Attempt: attempt, Duration: time.Since(started)})
		}
	}
	return &controlplane.Error{Category: controlplane.CategoryExhausted, Operation: "transaction_retry"}
}

// isDomainError preserves already-classified contract failures instead of remapping them as transport failures.
func isDomainError(err error) bool {
	var correctionErr *correction.Error
	if errors.As(err, &correctionErr) {
		return true
	}
	var environmentErr *environment.Error
	if errors.As(err, &environmentErr) {
		return true
	}
	var cleanupErr *cleanup.Error
	if errors.As(err, &cleanupErr) {
		return true
	}
	for _, sentinel := range []error{controlplane.ErrInvalid, controlplane.ErrNotFound, controlplane.ErrConflict, controlplane.ErrUnauthorized, controlplane.ErrIntegrity, controlplane.ErrCancelled, controlplane.ErrDeadline, controlplane.ErrTransient, controlplane.ErrUnavailable, controlplane.ErrExhausted, controlplane.ErrInternal} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}

// errorCategory extracts only the closed redacted category for telemetry.
func errorCategory(err error) controlplane.ErrorCategory {
	if err == nil {
		return ""
	}
	var value *controlplane.Error
	if errors.As(err, &value) {
		return value.Category
	}
	return controlplane.CategoryInternal
}
