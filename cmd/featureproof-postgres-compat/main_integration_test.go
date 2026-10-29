//go:build postgres_integration

package main

import (
	"context"
	"os"
	"testing"
)

// TestRunExecutesAuthorizedCompatibilityOperations exercises both compiled
// migration commands against the gate-owned PostgreSQL database.
func TestRunExecutesAuthorizedCompatibilityOperations(t *testing.T) {
	environment := map[string]string{
		"FEATUREPROOF_POSTGRES_TEST_DSN":        os.Getenv("FEATUREPROOF_POSTGRES_TEST_DSN"),
		"FEATUREPROOF_POSTGRES_GATE_AUTHORIZED": "true",
	}
	if environment["FEATUREPROOF_POSTGRES_TEST_DSN"] == "" {
		t.Fatal("FEATUREPROOF_POSTGRES_TEST_DSN is required")
	}
	getenv := func(key string) string { return environment[key] }
	if err := run(context.Background(), []string{"migrate"}, getenv); err != nil {
		t.Fatal("migrate", err)
	}
	if err := run(context.Background(), []string{"ready"}, getenv); err != nil {
		t.Fatal("ready", err)
	}
}
