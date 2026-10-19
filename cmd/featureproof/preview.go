package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/asierhr/featureProof/internal/externalagent"
	"github.com/asierhr/featureProof/internal/githubpr"
	"github.com/asierhr/featureProof/internal/localvalidation"
	"github.com/asierhr/featureProof/internal/previewenv"
)

// configuredPreviewFile is the bounded trusted local composition schema for one explicit preview alias.
type configuredPreviewFile struct {
	Alias                 string    `json:"alias"`
	ProviderID            string    `json:"provider_id"`
	EnvironmentRef        string    `json:"environment_ref"`
	ProfileID             string    `json:"profile_id"`
	NetworkPolicyID       string    `json:"network_policy_id"`
	RepositoryID          string    `json:"repository_id"`
	CommitOID             string    `json:"commit_oid"`
	EnvironmentID         string    `json:"environment_id"`
	Target                string    `json:"target"`
	AttestationRef        string    `json:"attestation_ref"`
	AttestationValidUntil time.Time `json:"attestation_valid_until"`
	SpecPath              string    `json:"spec_path"`
	PlanPath              string    `json:"plan_path"`
	Scheme                string    `json:"scheme"`
	Host                  string    `json:"host"`
	Port                  int       `json:"port"`
	BasePath              string    `json:"base_path"`
	Secretless            bool      `json:"secretless"`
}

// configuredPreview contains the ready coordinator and typed transport selection built by the command root.
type configuredPreview struct {
	Coordinator *previewenv.Coordinator
	Request     previewenv.ConsumerRequest
	Recovery    *previewenv.RecoverySupervisor
	Config      configuredPreviewFile
}

// composeConfiguredPreview builds the production preview boundary only when explicit complete configuration exists.
func composeConfiguredPreview(ctx context.Context, root string) (*configuredPreview, bool, error) {
	path := os.Getenv("FEATUREPROOF_PREVIEW_CONFIG")
	if path == "" {
		return nil, false, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 || len(raw) > 1<<20 {
		return nil, true, errors.New("invalid preview configuration")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var config configuredPreviewFile
	if decoder.Decode(&config) != nil || decoder.Decode(&struct{}{}) != io.EOF || !config.Secretless {
		return nil, true, errors.New("invalid preview configuration")
	}
	selection := previewenv.Selection{Alias: config.Alias, ProviderID: config.ProviderID, EnvironmentRef: config.EnvironmentRef, ProfileID: config.ProfileID, NetworkPolicyID: config.NetworkPolicyID, Secretless: true}
	expected := previewenv.ExpectedRevision{RepositoryID: config.RepositoryID, CommitOID: config.CommitOID}
	snapshot, err := previewenv.SealSnapshot(previewenv.Snapshot{Version: 1, ProviderID: config.ProviderID, EnvironmentID: config.EnvironmentID, ReservationID: "unreserved", Target: config.Target, Availability: previewenv.AvailabilityReady, RepositoryID: config.RepositoryID, CommitOID: config.CommitOID, AttestationRef: config.AttestationRef, AttestationValidUntil: config.AttestationValidUntil, ObservedVersion: "configured-v1"})
	if err != nil {
		return nil, true, errors.New("invalid preview configuration")
	}
	provider, err := previewenv.NewStaticProvider(config.ProviderID, map[string]previewenv.Snapshot{config.EnvironmentRef: snapshot})
	if err != nil {
		return nil, true, err
	}
	stateRoot := filepath.Join(root, ".featureproof", "preview")
	store, err := previewenv.OpenLocalStore(stateRoot)
	if err != nil {
		return nil, true, err
	}
	telemetry, err := previewenv.OpenLocalTelemetry(stateRoot)
	if err != nil {
		return nil, true, err
	}
	owner, err := (externalagent.RandomIDs{}).Next()
	if err != nil {
		return nil, true, err
	}
	clock := githubpr.WallClock{}
	deps := localvalidation.DefaultDependencies(root, io.Discard, io.Discard)
	deps.SafeProjector = previewenv.SafeProjector{PolicyID: config.NetworkPolicyID}
	executor := previewenv.LocalRunnerExecutor{Profiles: map[string]previewenv.LocalRunnerProfile{config.ProfileID: {SpecPath: config.SpecPath, PlanPath: config.PlanPath, Root: root, Dependencies: deps}}}
	coordinator, err := previewenv.NewCoordinator(previewenv.Config{ReserveTimeout: 10 * time.Second, ReadinessTimeout: 10 * time.Second, ReadinessWait: time.Second, ReleaseTimeout: 10 * time.Second, MaxReadiness: 60, MaxAttempts: 4, InitialBackoff: 100 * time.Millisecond, MaxBackoff: 2 * time.Second, MaxCumulativeWait: 5 * time.Second}, previewenv.Dependencies{Providers: map[string]previewenv.Provider{config.ProviderID: provider}, Policies: map[string]previewenv.NetworkPolicy{config.NetworkPolicyID: {ID: config.NetworkPolicyID, Schemes: []string{config.Scheme}, Hosts: []string{config.Host}, Ports: []int{config.Port}, BasePaths: []string{config.BasePath}, MaxURLBytes: 2048}}, Executor: executor, Store: store, Clock: clock, Observer: telemetry, Owner: owner, LeaseTTL: 32 * time.Minute})
	if err != nil {
		return nil, true, err
	}
	return &configuredPreview{Coordinator: coordinator, Request: previewenv.ConsumerRequest{Selection: selection, Expected: expected}, Config: config}, true, nil
}
