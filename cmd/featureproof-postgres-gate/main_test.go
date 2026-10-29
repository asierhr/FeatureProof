package main

import (
	"context"
	"testing"
)

// TestRunRejectsImplicitOrBroadTargets proves destructive authority cannot be inferred from host defaults.
func TestRunRejectsImplicitOrBroadTargets(t *testing.T) {
	getenv := func(string) string { return "" }
	for _, arguments := range [][]string{nil, {"drop", "postgres", "token"}, {"drop", "featureproof_gate_0123456789abcdef0123456789abcdef", "short"}} {
		if err := run(context.Background(), arguments, getenv); err == nil {
			t.Fatalf("accepted %#v", arguments)
		}
	}
}

// TestGateHelpersRejectUnsupportedOperationsAndUnavailableConnections covers fail-closed provisioning diagnostics without external state.
func TestGateHelpersRejectUnsupportedOperationsAndUnavailableConnections(t *testing.T) {
	ctx := context.Background()
	database := "featureproof_gate_0123456789abcdef0123456789abcdef"
	token := "0123456789abcdef0123456789abcdef"
	missingTarget := func(name string) string {
		if name == "FEATUREPROOF_POSTGRES_ADMIN_DSN" {
			return "postgres://127.0.0.1:1/postgres?connect_timeout=1"
		}
		return ""
	}
	if err := run(ctx, []string{"create", database, token}, missingTarget); err == nil {
		t.Fatal("missing target DSN accepted")
	}
	unavailable := func(string) string { return "postgres://127.0.0.1:1/postgres?connect_timeout=1" }
	if err := run(ctx, []string{"unknown", database, token}, unavailable); err == nil {
		t.Fatal("unknown action accepted")
	}
	if err := createOwnedDatabase(ctx, unavailable(""), unavailable(""), database, token); err == nil {
		t.Fatal("unavailable admin accepted for creation")
	}
	if err := verifyOwnedDatabase(ctx, unavailable(""), database, token); err == nil {
		t.Fatal("unavailable target accepted for verification")
	}
	if err := dropOwnedDatabase(ctx, unavailable(""), "postgres"); err == nil {
		t.Fatal("unsafe cleanup identity accepted")
	}
	if err := dropOwnedDatabase(ctx, unavailable(""), database); err == nil {
		t.Fatal("unavailable admin accepted for cleanup")
	}
}
