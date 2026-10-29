//go:build postgres_integration

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
)

// TestOwnedDatabaseLifecycle exercises create, verify and ownership-checked drop against the explicit gate server.
func TestOwnedDatabaseLifecycle(t *testing.T) {
	adminDSN := os.Getenv("FEATUREPROOF_POSTGRES_ADMIN_DSN")
	if adminDSN == "" {
		t.Fatal("FEATUREPROOF_POSTGRES_ADMIN_DSN is required")
	}
	identityBytes := make([]byte, 16)
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(identityBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(tokenBytes); err != nil {
		t.Fatal(err)
	}
	database := "featureproof_gate_" + hex.EncodeToString(identityBytes)
	token := hex.EncodeToString(tokenBytes)
	parsed, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + database
	targetDSN := parsed.String()
	getenv := func(key string) string {
		if key == "FEATUREPROOF_POSTGRES_ADMIN_DSN" {
			return adminDSN
		}
		if key == "FEATUREPROOF_POSTGRES_TEST_DSN" {
			return targetDSN
		}
		return ""
	}
	created := false
	defer func() {
		if created {
			_ = dropOwnedDatabase(context.Background(), adminDSN, database)
		}
	}()
	if err = run(context.Background(), []string{"create", database, token}, getenv); err != nil {
		t.Fatal(err)
	}
	created = true
	if err = run(context.Background(), []string{"create", database, token}, getenv); err == nil {
		t.Fatal("existing owned database recreated")
	}
	if err = run(context.Background(), []string{"verify", database, token}, getenv); err != nil {
		t.Fatal(err)
	}
	if err = run(context.Background(), []string{"verify", database, token + "x"}, getenv); err == nil {
		t.Fatal("wrong ownership token accepted")
	}
	if err = run(context.Background(), []string{"drop", database, token + "x"}, getenv); err == nil {
		t.Fatal("wrong ownership token dropped database")
	}
	if err = run(context.Background(), []string{"drop", database, token}, getenv); err != nil {
		t.Fatal(err)
	}
	created = false
	if err = run(context.Background(), []string{"verify", database, token}, getenv); err == nil {
		t.Fatal("dropped database verified")
	}
}
