package main

import (
	"context"
	"strings"
	"testing"
)

// TestRunRejectsInvalidInvocation proves the compatibility executable cannot
// touch a database without one closed operation and an environment reader.
func TestRunRejectsInvalidInvocation(t *testing.T) {
	for _, arguments := range [][]string{nil, {"unknown"}, {"ready", "extra"}} {
		if err := run(context.Background(), arguments, func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Fatalf("arguments %v produced %v", arguments, err)
		}
	}
	if err := run(context.Background(), []string{"ready"}, nil); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("nil environment reader produced %v", err)
	}
}

// TestRunRequiresExplicitGateAuthorization proves both the secret DSN and the
// opt-in flag are mandatory and rejects malformed DSNs before compatibility work.
func TestRunRequiresExplicitGateAuthorization(t *testing.T) {
	tests := []map[string]string{
		{"FEATUREPROOF_POSTGRES_GATE_AUTHORIZED": "true"},
		{"FEATUREPROOF_POSTGRES_TEST_DSN": "postgres://database.example.test/featureproof"},
		{"FEATUREPROOF_POSTGRES_TEST_DSN": "postgres://database.example.test/featureproof", "FEATUREPROOF_POSTGRES_GATE_AUTHORIZED": "false"},
	}
	for _, environment := range tests {
		err := run(context.Background(), []string{"ready"}, func(key string) string { return environment[key] })
		if err == nil || !strings.Contains(err.Error(), "explicit authorized") {
			t.Fatalf("environment %#v produced %v", environment, err)
		}
	}
	environment := map[string]string{"FEATUREPROOF_POSTGRES_TEST_DSN": "://malformed", "FEATUREPROOF_POSTGRES_GATE_AUTHORIZED": "true"}
	if err := run(context.Background(), []string{"migrate"}, func(key string) string { return environment[key] }); err == nil {
		t.Fatal("malformed authorized DSN was accepted")
	}
}

// TestRunHonorsCancellation proves an authorized compatibility attempt remains
// bounded by its caller context instead of retrying or falling back locally.
func TestRunHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	environment := map[string]string{"FEATUREPROOF_POSTGRES_TEST_DSN": "postgres://localhost:5432/featureproof", "FEATUREPROOF_POSTGRES_GATE_AUTHORIZED": "true"}
	if err := run(ctx, []string{"ready"}, func(key string) string { return environment[key] }); err == nil {
		t.Fatal("cancelled compatibility attempt succeeded")
	}
}
