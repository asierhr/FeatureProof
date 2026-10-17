package githubpr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"
)

// ProductionConfig collects non-secret integration limits and exact GitHub authority.
type ProductionConfig struct {
	AppID, BotID                                                                                 int64
	Permissions                                                                                  PermissionSet
	APIOrigin, APIVersion, EvidenceOrigin                                                        string
	MaxWebhookBytes, MaxResponseBytes                                                            int64
	MaxPages, MaxAttempts                                                                        int
	RunTimeout, CloseTimeout                                                                     time.Duration
	LeaseTTL, PollInterval, RetryMaxDuration, RetryMaxWait, RetryInitialBackoff, RetryMaxBackoff time.Duration
	StoreDir                                                                                     string
	Authorization                                                                                AuthorizationConfig
}

// Validate rejects incomplete, overprivileged, unbounded, or non-HTTPS production composition.
func (c ProductionConfig) Validate() error {
	if c.AppID <= 0 || c.BotID <= 0 || ValidatePermissions(c.Permissions) != nil || c.APIVersion == "" || c.StoreDir == "" || c.MaxWebhookBytes <= 0 || c.MaxResponseBytes <= 0 || c.MaxPages <= 0 || c.MaxAttempts <= 0 || c.RunTimeout <= 0 || c.CloseTimeout <= 0 || c.PollInterval <= 0 || c.RetryMaxDuration <= 0 || c.RetryMaxWait < 0 || c.RetryInitialBackoff <= 0 || c.RetryMaxBackoff < c.RetryInitialBackoff || !leaseCovers(c.LeaseTTL, c.RunTimeout, c.CloseTimeout, c.RetryMaxDuration) {
		return &Error{Code: CodeInvalid, Operation: "production_config"}
	}
	if _, err := NewEvidenceLinker(c.EvidenceOrigin, 2048); err != nil {
		return err
	}
	if _, err := NewRESTAdapter(RESTConfig{BaseURL: c.APIOrigin, APIVersion: c.APIVersion, AppID: c.AppID, Tokens: failClosedCredentials{}, HTTP: failClosedHTTP{}, Clock: WallClock{}, MaxResponse: c.MaxResponseBytes}); err != nil {
		return err
	}
	return nil
}

// CompositionSecrets contains secret bytes resolved only by the executable composition root.
type CompositionSecrets struct{ WebhookSecret []byte }

// RuntimeDependencies inject transport credentials, verified packages, and the preview-gated validation use case.
type RuntimeDependencies struct {
	Credentials CredentialSource
	HTTP        HTTPDoer
	Packages    PackageReader
	Validation  ValidationGateway
	Clock       Clock
	Jitter      Jitter
	IDs         IDSource
}

// Runtime owns the composed webhook boundary and its joinable coordinator.
type Runtime struct {
	handler      http.Handler
	coordinator  *Coordinator
	closeTimeout time.Duration
}

// ComposeRuntime wires store, REST, retry, projection, publisher, coordinator, and webhook without leaking secrets downstream.
func ComposeRuntime(ctx context.Context, config ProductionConfig, secrets CompositionSecrets, deps RuntimeDependencies) (*Runtime, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if len(secrets.WebhookSecret) < 16 || deps.Credentials == nil || deps.HTTP == nil || deps.Packages == nil || deps.Clock == nil || deps.Jitter == nil || deps.IDs == nil {
		return nil, &Error{Code: CodeInvalid, Operation: "composition"}
	}
	if deps.Validation == nil {
		deps.Validation = FailClosedValidation{}
	}
	store, err := OpenLocalStore(config.StoreDir)
	if err != nil {
		return nil, err
	}
	adapter, err := NewRESTAdapter(RESTConfig{BaseURL: config.APIOrigin, APIVersion: config.APIVersion, AppID: config.AppID, Tokens: deps.Credentials, HTTP: deps.HTTP, Clock: deps.Clock, MaxResponse: config.MaxResponseBytes})
	if err != nil {
		return nil, err
	}
	linker, err := NewEvidenceLinker(config.EvidenceOrigin, 2048)
	if err != nil {
		return nil, err
	}
	owner, err := deps.IDs.Next()
	if err != nil {
		return nil, &Error{Code: CodeInternal, Operation: "owner"}
	}
	retry := RetryPolicy{Config: RetryConfig{MaxAttempts: config.MaxAttempts, MaxDuration: config.RetryMaxDuration, MaxWait: config.RetryMaxWait, InitialBackoff: config.RetryInitialBackoff, MaxBackoff: config.RetryMaxBackoff}, Clock: deps.Clock, Jitter: deps.Jitter}
	publisher := Publisher{GitHub: adapter, Store: store, Retry: retry, AppID: config.AppID, BotID: config.BotID, MaxPages: config.MaxPages, Owner: owner, Clock: deps.Clock, LeaseTTL: config.LeaseTTL}
	coordinator, err := NewCoordinator(ctx, CoordinatorDependencies{GitHub: adapter, Authorizer: StaticAuthorizer{Config: config.Authorization}, Validation: deps.Validation, Store: store, Publisher: publisher, Projector: Projector{Packages: deps.Packages, Links: linker, MaxItems: 50, MaxSummary: 60000}, Retry: retry, IDs: deps.IDs, Clock: deps.Clock, Owner: owner, RunTimeout: config.RunTimeout, CloseTimeout: config.CloseTimeout, LeaseTTL: config.LeaseTTL, PollInterval: config.PollInterval})
	if err != nil {
		return nil, err
	}
	handler := &WebhookHandler{Secret: append([]byte(nil), secrets.WebhookSecret...), MaxBody: config.MaxWebhookBytes, Store: store, Admit: coordinator.Admit, Owner: owner, Clock: deps.Clock, LeaseTTL: config.LeaseTTL}
	return &Runtime{handler: handler, coordinator: coordinator, closeTimeout: config.CloseTimeout}, nil
}

// Handler exposes only the authenticated webhook boundary to the HTTP server.
func (r *Runtime) Handler() http.Handler { return r.handler }

// Close cancels and joins the coordinator within the configured shutdown bound.
func (r *Runtime) Close(ctx context.Context) error {
	bounded, cancel := context.WithTimeout(ctx, r.closeTimeout)
	defer cancel()
	return r.coordinator.Close(bounded)
}

// WallClock supplies production UTC time and cancelable timer waits.
type WallClock struct{}

// Now returns current UTC wall time for expiry, leases, and retry budgets.
func (WallClock) Now() time.Time { return time.Now().UTC() }

// Wait blocks on an owned timer while honoring caller cancellation.
func (WallClock) Wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RandomIDs produces opaque 128-bit association identities.
type RandomIDs struct{}

// Next returns a non-semantic lowercase hexadecimal identifier.
func (RandomIDs) Next() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// RandomJitter varies production backoff within a bounded 75–125 percent window.
type RandomJitter struct{}

// Duration derives bounded jitter from cryptographic randomness without shared mutable state.
func (RandomJitter) Duration(base time.Duration) time.Duration {
	raw := []byte{0}
	if _, err := rand.Read(raw); err != nil {
		return base
	}
	factor := 0.75 + float64(raw[0])/255*0.5
	return time.Duration(float64(base) * factor)
}

// FailClosedValidation rejects preview-backed work until feature 19 supplies trusted composition.
type FailClosedValidation struct{}

// Submit prevents a GitHub command from selecting an unverified deployment.
func (FailClosedValidation) Submit(context.Context, Selection, PullRequestSnapshot, string) (RunState, error) {
	return RunState{}, &Error{Code: CodeUnauthorized, Operation: "preview_unavailable"}
}

// ResolveExisting prevents existing-run association when preview authority cannot verify its lineage.
func (FailClosedValidation) ResolveExisting(context.Context, ExistingRunLookup) (RunState, error) {
	return RunState{}, &Error{Code: CodeUnauthorized, Operation: "preview_unavailable"}
}

// Status prevents lookup through a composition that cannot establish preview authority.
func (FailClosedValidation) Status(context.Context, string) (RunState, error) {
	return RunState{}, &Error{Code: CodeUnauthorized, Operation: "preview_unavailable"}
}

// Cancel is idempotently safe because fail-closed composition never starts a run.
func (FailClosedValidation) Cancel(context.Context, string, string) error { return nil }

// failClosedCredentials prevents configuration validation from resolving a live installation token.
type failClosedCredentials struct{}

// Token proves config parsing never resolves real credentials during validation.
func (failClosedCredentials) Token(context.Context, int64, int64, PermissionSet) (InstallationToken, error) {
	return InstallationToken{}, &Error{Code: CodeUnauthorized, Operation: "credentials_unavailable"}
}

// failClosedHTTP prevents configuration validation from crossing the network boundary.
type failClosedHTTP struct{}

// Do proves config validation never performs network access.
func (failClosedHTTP) Do(*http.Request) (*http.Response, error) {
	return nil, &Error{Code: CodeUnauthorized, Operation: "network_unavailable"}
}
