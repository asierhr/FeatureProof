package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/tenantsecurity"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestMapTenantSecurityErrorUsesSQLSTATE verifies the adapter never classifies
// database failures by localized or driver-controlled message text.
func TestMapTenantSecurityErrorUsesSQLSTATE(t *testing.T) {
	cases := []struct {
		code     string
		category tenantsecurity.Category
	}{
		{code: "23505", category: tenantsecurity.CategoryConflict},
		{code: "23503", category: tenantsecurity.CategoryIntegrity},
		{code: "42501", category: tenantsecurity.CategoryUnauthorized},
		{code: "40001", category: tenantsecurity.CategoryStale},
		{code: "40P01", category: tenantsecurity.CategoryStale},
		{code: "22P02", category: tenantsecurity.CategoryInvalid},
		{code: "08006", category: tenantsecurity.CategoryUnavailable},
	}
	for _, test := range cases {
		t.Run(test.code, func(t *testing.T) {
			err := mapTenantSecurityError(context.Background(), "tenant_operation", &pgconn.PgError{Code: test.code, Message: "secret-canary"})
			if !tenantsecurity.IsCategory(err, test.category) {
				t.Fatalf("expected %s for SQLSTATE %s, got %v", test.category, test.code, err)
			}
			if err.Error() != "tenant security tenant_operation: "+string(test.category) {
				t.Fatalf("driver message escaped redaction: %q", err.Error())
			}
		})
	}
}

// TestTenantSecurityErrorMappingCoversContextAndTypedFailures proves every
// non-message classification path is stable and redacted.
func TestTenantSecurityErrorMappingCoversContextAndTypedFailures(t *testing.T) {
	if mapTenantSecurityError(context.Background(), "nil", nil) != nil {
		t.Fatal("nil error was wrapped")
	}
	typed := &tenantsecurity.Error{Category: tenantsecurity.CategoryConflict, Operation: "typed"}
	if mapped := mapTenantSecurityError(context.Background(), "ignored", typed); mapped != typed {
		t.Fatalf("typed error identity changed: %v", mapped)
	}
	for _, code := range []string{"23514", "23000", "22000", "22001"} {
		if err := mapTenantSecurityError(context.Background(), "sqlstate", &pgconn.PgError{Code: code}); !tenantsecurity.IsCategory(err, map[string]tenantsecurity.Category{"23514": tenantsecurity.CategoryIntegrity, "23000": tenantsecurity.CategoryIntegrity, "22000": tenantsecurity.CategoryInvalid, "22001": tenantsecurity.CategoryInvalid}[code]) {
			t.Fatalf("code=%s err=%v", code, err)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mapTenantSecurityError(cancelled, "cancel", &pgconn.PgError{Code: "57014"}); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryCancelled) {
		t.Fatalf("cancel=%v", err)
	}
	deadline, stop := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer stop()
	if err := mapTenantSecurityError(deadline, "deadline", errors.New("provider-canary")); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryDeadline) || err.Error() != "tenant security deadline: deadline" {
		t.Fatalf("deadline=%v", err)
	}
	if err := mapTenantSecurityError(context.Background(), "unavailable", errors.New("provider-canary")); !tenantsecurity.IsCategory(err, tenantsecurity.CategoryUnavailable) || err.Error() != "tenant security unavailable: unavailable" {
		t.Fatalf("unavailable=%v", err)
	}
	if nilTime(time.Time{}) != nil || nilTime(time.Unix(1, 0)).(time.Time).IsZero() || len(tenantSecurityDigest([]byte("payload"))) != 64 {
		t.Fatal("tenant SQL projection helper changed")
	}
}
