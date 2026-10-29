package githubpr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/asierhr/featureProof/internal/controlplane"
)

// ProductionConfig collects non-secret integration limits and exact GitHub authority.
type ProductionConfig struct {
	AppID, BotID                                                                                 int64
	Permissions                                                                                  PermissionSet
	APIOrigin, APIVersion, EvidenceOrigin                                                        string
	MaxWebhookBytes, MaxResponseBytes                                                            int64
	MaxPages, MaxChangedPaths, MaxAttempts                                                       int
	RunTimeout, CloseTimeout                                                                     time.Duration
	LeaseTTL, PollInterval, RetryMaxDuration, RetryMaxWait, RetryInitialBackoff, RetryMaxBackoff time.Duration
	StoreDir                                                                                     string
	Authorization                                                                                AuthorizationConfig
}

// Validate rejects incomplete, overprivileged, unbounded, or non-HTTPS production composition.
func (c ProductionConfig) Validate() error {
	if c.AppID <= 0 || c.BotID <= 0 || ValidatePermissions(c.Permissions) != nil || c.APIVersion == "" || c.StoreDir == "" || c.MaxWebhookBytes <= 0 || c.MaxResponseBytes <= 0 || c.MaxPages <= 0 || c.MaxChangedPaths <= 0 || c.MaxAttempts <= 0 || c.RunTimeout <= 0 || c.CloseTimeout <= 0 || c.PollInterval <= 0 || c.RetryMaxDuration <= 0 || c.RetryMaxWait < 0 || c.RetryInitialBackoff <= 0 || c.RetryMaxBackoff < c.RetryInitialBackoff || !leaseCovers(c.LeaseTTL, c.RunTimeout, c.CloseTimeout, c.RetryMaxDuration) {
		return &Error{Code: CodeInvalid, Operation: "production_config"}
	}
	if _, err := NewEvidenceLinker(c.EvidenceOrigin, 2048); err != nil {
		return err
	}
	if _, err := NewRESTAdapter(RESTConfig{BaseURL: c.APIOrigin, APIVersion: c.APIVersion, AppID: c.AppID, Tokens: failClosedCredentials{}, HTTP: failClosedHTTP{}, Clock: WallClock{}, MaxResponse: c.MaxResponseBytes, MaxPages: c.MaxPages, MaxChangedPaths: c.MaxChangedPaths}); err != nil {
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
	// Review receives only commands authenticated and translated by the webhook boundary.
	Review func(context.Context, WebhookCommand) (string, error)
	// ReviewOwner transfers lifecycle ownership of the review service into the composed runtime.
	ReviewOwner  interface{ Close() error }
	ControlPlane controlplane.IngressStore
	// ControlPlaneAdmission constructs the causal epoch/job only after static webhook scope checks pass.
	ControlPlaneAdmission func(context.Context, WebhookCommand) (controlplane.IngressAdmission, error)
	// ControlPlaneQueue exposes the same selected backend for caller-owned worker scheduling.
	ControlPlaneQueue controlplane.JobQueue
	// ControlPlaneAuthority reloads composition-owned grants and persists a live envelope before the worker enters product code.
	ControlPlaneAuthority controlplane.AuthorityEnvelopeStore
	// ControlPlaneOutbox is the only productive authority for external effects after durable intent creation.
	ControlPlaneOutbox controlplane.EffectOutbox
	// ControlPlaneSecrets is the private JIT boundary used only after the durable envelope passes its final live check.
	ControlPlaneSecrets controlplane.SecretResolver
	// ControlPlaneEffect builds and uses an ephemeral capability from the just-resolved secret set.
	ControlPlaneEffect ControlPlaneEffect
	// ControlPlaneDomains owns the PostgreSQL workflow checkpoint and complete backend-homogeneous domain graph.
	ControlPlaneDomains interface {
		Ready(context.Context) error
	}
	// ControlPlaneProjectID and ControlPlaneWorkerID bind scheduler claims to composition-owned identities.
	ControlPlaneProjectID, ControlPlaneWorkerID string
}

// ControlPlaneEffectRequest carries durable operation identity and ephemeral credentials to exactly one effect boundary.
type ControlPlaneEffectRequest struct {
	Command WebhookCommand
	Intent  controlplane.EffectIntent
	Claim   controlplane.EffectClaim
	Secrets map[string]string
}

// ControlPlaneEffect reconciles response loss before executing one newly claimed external operation.
type ControlPlaneEffect interface {
	Reconcile(context.Context, ControlPlaneEffectRequest) (string, bool, error)
	Execute(context.Context, ControlPlaneEffectRequest) (string, error)
}

// Runtime owns the composed webhook boundary and its joinable coordinator.
type Runtime struct {
	handler      http.Handler
	coordinator  *Coordinator
	validation   ValidationGateway
	reviewOwner  interface{ Close() error }
	controlPlane interface{ Close() }
	worker       *controlplane.WorkerRuntime
	closeTimeout time.Duration
}

// ComposeRuntime wires store, REST, retry, projection, publisher, coordinator, and webhook without leaking secrets downstream.
func ComposeRuntime(ctx context.Context, config ProductionConfig, secrets CompositionSecrets, deps RuntimeDependencies) (*Runtime, error) {
	owned := deps.ReviewOwner
	committed := false
	defer func() {
		if !committed && owned != nil {
			_ = owned.Close()
		}
	}()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	distributed := deps.ControlPlane != nil || deps.ControlPlaneAdmission != nil
	if len(secrets.WebhookSecret) < 16 || deps.Clock == nil || deps.Jitter == nil || deps.IDs == nil || (!distributed && (deps.Credentials == nil || deps.HTTP == nil || deps.Packages == nil)) {
		return nil, &Error{Code: CodeInvalid, Operation: "composition"}
	}
	if deps.Validation == nil {
		deps.Validation = FailClosedValidation{}
	}
	owner, err := deps.IDs.Next()
	if err != nil {
		return nil, &Error{Code: CodeInternal, Operation: "owner"}
	}
	var store *LocalStore
	var coordinator *Coordinator
	if !distributed {
		store, err = OpenLocalStore(config.StoreDir)
		if err != nil {
			return nil, err
		}
		adapter, adapterErr := NewRESTAdapter(RESTConfig{BaseURL: config.APIOrigin, APIVersion: config.APIVersion, AppID: config.AppID, Tokens: deps.Credentials, HTTP: deps.HTTP, Clock: deps.Clock, MaxResponse: config.MaxResponseBytes, MaxPages: config.MaxPages, MaxChangedPaths: config.MaxChangedPaths})
		if adapterErr != nil {
			return nil, adapterErr
		}
		linker, linkerErr := NewEvidenceLinker(config.EvidenceOrigin, 2048)
		if linkerErr != nil {
			return nil, linkerErr
		}
		retry := RetryPolicy{Config: RetryConfig{MaxAttempts: config.MaxAttempts, MaxDuration: config.RetryMaxDuration, MaxWait: config.RetryMaxWait, InitialBackoff: config.RetryInitialBackoff, MaxBackoff: config.RetryMaxBackoff}, Clock: deps.Clock, Jitter: deps.Jitter}
		publisher := Publisher{GitHub: adapter, Store: store, Retry: retry, AppID: config.AppID, BotID: config.BotID, MaxPages: config.MaxPages, Owner: owner, Clock: deps.Clock, LeaseTTL: config.LeaseTTL}
		coordinator, err = NewCoordinator(ctx, CoordinatorDependencies{GitHub: adapter, Authorizer: StaticAuthorizer{Config: config.Authorization}, Validation: deps.Validation, Store: store, Publisher: publisher, Projector: Projector{Packages: deps.Packages, Links: linker, MaxItems: 50, MaxSummary: 60000}, Retry: retry, IDs: deps.IDs, Clock: deps.Clock, Owner: owner, RunTimeout: config.RunTimeout, CloseTimeout: config.CloseTimeout, LeaseTTL: config.LeaseTTL, PollInterval: config.PollInterval})
		if err != nil {
			return nil, err
		}
	}
	var admission func(context.Context, WebhookCommand) (controlplane.IngressAdmission, error)
	var worker *controlplane.WorkerRuntime
	if deps.ControlPlane != nil || deps.ControlPlaneAdmission != nil {
		reader, readable := deps.ControlPlane.(controlplane.IngressReader)
		if deps.ControlPlane == nil || deps.ControlPlaneAdmission == nil || deps.ControlPlaneQueue == nil || deps.ControlPlaneAuthority == nil || deps.ControlPlaneOutbox == nil || deps.ControlPlaneEffect == nil || deps.ControlPlaneDomains == nil || !readable || deps.ControlPlaneDomains.Ready(ctx) != nil {
			return nil, &Error{Code: CodeInvalid, Operation: "control_plane"}
		}
		admission = func(admissionContext context.Context, command WebhookCommand) (controlplane.IngressAdmission, error) {
			if !webhookCommandInScope(config.Authorization, command) {
				return controlplane.IngressAdmission{}, &Error{Code: CodeUnauthorized, Operation: "control_plane_scope"}
			}
			return deps.ControlPlaneAdmission(admissionContext, command)
		}
		productHandler := controlPlaneJobHandler{reader: reader, outbox: deps.ControlPlaneOutbox, effect: deps.ControlPlaneEffect, clock: deps.Clock, waiter: deps.Clock, owner: deps.ControlPlaneWorkerID + "-outbox"}
		workerHandler := authorizedControlPlaneJobHandler{authority: deps.ControlPlaneAuthority, secrets: deps.ControlPlaneSecrets, clock: deps.Clock, next: productHandler}
		scheduler, schedulerErr := controlplane.NewScheduler(deps.ControlPlaneQueue, deps.Clock, deps.Clock, workerHandler, controlPlaneErrorClassifier{}, controlplane.SchedulerConfig{ProjectID: deps.ControlPlaneProjectID, WorkerID: deps.ControlPlaneWorkerID, Lease: 2 * time.Minute, HeartbeatInterval: 30 * time.Second, BatchSize: 8, IdleWait: config.PollInterval})
		if schedulerErr != nil {
			return nil, &Error{Code: CodeInvalid, Operation: "control_plane_worker"}
		}
		worker, schedulerErr = controlplane.NewWorkerRuntime(scheduler)
		if schedulerErr != nil {
			return nil, &Error{Code: CodeInvalid, Operation: "control_plane_worker"}
		}
	}
	var localAdmit func(context.Context, WebhookCommand) (string, error)
	if coordinator != nil {
		localAdmit = coordinator.Admit
	}
	handler := &WebhookHandler{Secret: append([]byte(nil), secrets.WebhookSecret...), MaxBody: config.MaxWebhookBytes, Store: store, Admit: localAdmit, Review: deps.Review, Owner: owner, Clock: deps.Clock, LeaseTTL: config.LeaseTTL, ControlPlane: deps.ControlPlane, Admission: admission}
	committed = true
	controlPlane, _ := deps.ControlPlane.(interface{ Close() })
	return &Runtime{handler: handler, coordinator: coordinator, validation: deps.Validation, reviewOwner: owned, controlPlane: controlPlane, worker: worker, closeTimeout: config.CloseTimeout}, nil
}

// Start launches optional distributed workers only when called by executable composition.
func (r *Runtime) Start(ctx context.Context) error {
	if r.worker == nil {
		return nil
	}
	return r.worker.Start(ctx)
}

// webhookCommandInScope rejects installation, repository, actor and selection mismatches before durable inbox admission.
func webhookCommandInScope(config AuthorizationConfig, command WebhookCommand) bool {
	return config.Installations[command.InstallationID][command.RepositoryID] && config.Actors[command.ActorID] && config.Selections[command.SelectionAlias].Alias == command.SelectionAlias
}

// Handler exposes only the authenticated webhook boundary to the HTTP server.
func (r *Runtime) Handler() http.Handler { return r.handler }

// Close cancels and joins the coordinator within the configured shutdown bound.
func (r *Runtime) Close(ctx context.Context) error {
	bounded, cancel := context.WithTimeout(ctx, r.closeTimeout)
	defer cancel()
	var closeErrors []error
	if r.worker != nil {
		r.worker.Stop()
		closeErrors = append(closeErrors, r.worker.JoinContext(bounded))
	}
	if r.coordinator != nil {
		closeErrors = append(closeErrors, r.coordinator.Close(bounded))
	}
	if closer, ok := r.validation.(interface{ Close(context.Context) error }); ok {
		closeErrors = append(closeErrors, closer.Close(bounded))
	}
	if r.reviewOwner != nil {
		closeErrors = append(closeErrors, r.reviewOwner.Close())
	}
	if r.controlPlane != nil {
		r.controlPlane.Close()
	}
	return errors.Join(closeErrors...)
}

// controlPlaneJobHandler reloads only allowlisted command fields and invokes the existing authorized use case after ACK.
type controlPlaneJobHandler struct {
	reader controlplane.IngressReader
	outbox controlplane.EffectOutbox
	effect ControlPlaneEffect
	clock  controlplane.Clock
	waiter controlplane.Waiter
	owner  string
}

// HandleAuthorized reconstructs one authenticated command and executes it only under an outbox claim using the JIT capability.
func (handler controlPlaneJobHandler) HandleAuthorized(ctx context.Context, job controlplane.Job, secrets map[string]string) error {
	admission, err := handler.reader.LoadIngress(ctx, job.RunID)
	if err != nil {
		return err
	}
	command, err := webhookCommandFromFields(admission.Envelope.CommandFields)
	if err != nil {
		return err
	}
	now := handler.clock.Now().UTC()
	if _, err = handler.outbox.RecoverOutbox(ctx, now, 8); err != nil {
		return err
	}
	operationID := job.JobID + "-dispatch"
	claim, err := handler.outbox.ClaimEffect(ctx, operationID, handler.owner, now, time.Minute)
	if err != nil {
		return err
	}
	intent, err := handler.outbox.LoadEffect(ctx, claim)
	if err != nil || intent.OperationID != operationID || intent.IdempotencyKey != job.JobID {
		return controlplane.ErrConflict
	}
	leaseUntil := boundedEffectLease(now, job.Deadline)
	if err = handler.outbox.HeartbeatOutbox(ctx, claim, leaseUntil); err != nil {
		return err
	}
	claim.LeaseUntil = leaseUntil
	request := ControlPlaneEffectRequest{Command: command, Intent: intent, Claim: claim, Secrets: cloneResolvedSecrets(secrets)}
	effectContext, stopHeartbeat := context.WithCancel(ctx)
	heartbeatResult := make(chan error, 1)
	if handler.waiter != nil {
		go handler.maintainEffectLease(effectContext, stopHeartbeat, claim, job.Deadline, heartbeatResult)
	} else {
		heartbeatResult <- nil
	}
	receipt, reconciled, err := handler.effect.Reconcile(effectContext, request)
	if err == nil && !reconciled {
		receipt, err = handler.effect.Execute(effectContext, request)
	}
	stopHeartbeat()
	heartbeatErr := <-heartbeatResult
	clearResolvedSecrets(request.Secrets)
	if heartbeatErr != nil {
		return heartbeatErr
	}
	if err != nil {
		_ = handler.outbox.RetryOutbox(context.WithoutCancel(ctx), claim, handler.clock.Now().UTC().Add(time.Second), "effect_failed")
		return err
	}
	if causal, ok := handler.outbox.(controlplane.EffectJobCompleter); ok {
		if err = causal.ConfirmEffectAndJob(ctx, claim, job, receipt, handler.clock.Now().UTC()); err != nil {
			return err
		}
	} else if err = handler.outbox.ConfirmOutbox(ctx, claim, receipt); err != nil {
		return err
	}
	return nil
}

// maintainEffectLease owns the dispatcher heartbeat until the effect returns or a live-fence failure cancels its context.
func (handler controlPlaneJobHandler) maintainEffectLease(ctx context.Context, cancel context.CancelFunc, claim controlplane.EffectClaim, deadline time.Time, result chan<- error) {
	for {
		if err := handler.waiter.Wait(ctx, 30*time.Second); err != nil {
			if ctx.Err() != nil {
				result <- nil
				return
			}
			result <- err
			return
		}
		now := handler.clock.Now().UTC()
		if err := handler.outbox.HeartbeatOutbox(ctx, claim, boundedEffectLease(now, deadline)); err != nil {
			cancel()
			result <- err
			return
		}
	}
}

// boundedEffectLease prevents an outbox heartbeat from extending authority beyond the job deadline.
func boundedEffectLease(now, deadline time.Time) time.Time {
	until := now.Add(90 * time.Second)
	if !deadline.IsZero() && deadline.Before(until) {
		return deadline
	}
	return until
}

// cloneResolvedSecrets gives one effect an isolated ephemeral credential map.
func cloneResolvedSecrets(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

// clearResolvedSecrets drops all references immediately after the effect boundary returns.
func clearResolvedSecrets(values map[string]string) {
	for key := range values {
		delete(values, key)
	}
}

// authorizedControlPlaneJobHandler requires a persisted phase grant and live envelope before delegating to product code.
type authorizedControlPlaneJobHandler struct {
	authority controlplane.AuthorityEnvelopeStore
	secrets   controlplane.SecretResolver
	clock     controlplane.Clock
	next      interface {
		HandleAuthorized(context.Context, controlplane.Job, map[string]string) error
	}
}

// Handle persists the minimal live envelope and crosses the private JIT secret boundary only after every fence check.
func (handler authorizedControlPlaneJobHandler) Handle(ctx context.Context, job controlplane.Job) error {
	if handler.authority == nil || handler.clock == nil || handler.next == nil {
		return &Error{Code: CodeUnauthorized, Operation: "worker_authority"}
	}
	now := handler.clock.Now().UTC()
	envelopeID := job.JobID + "-" + strconv.FormatUint(job.Attempt, 10) + "-" + strconv.FormatUint(job.Fence, 10)
	envelope, err := controlplane.DeriveAuthority(ctx, handler.authority, job, envelopeID, job.Owner, job.LeaseUntil, now)
	if err != nil || handler.authority.SaveAuthority(ctx, envelope, job, job.Owner, now) != nil {
		return &Error{Code: CodeUnauthorized, Operation: "worker_authority"}
	}
	resolved := map[string]string{}
	if len(envelope.SecretReferenceIDs) > 0 {
		if resolved, err = controlplane.ResolvePersistedSecrets(ctx, handler.authority, envelope, job.Owner, now, handler.secrets); err != nil {
			return &Error{Code: CodeUnauthorized, Operation: "worker_secret"}
		}
	}
	defer clearResolvedSecrets(resolved)
	return handler.next.HandleAuthorized(ctx, job, resolved)
}

// controlPlaneErrorClassifier maps safe GitHub categories to bounded durable retry decisions.
type controlPlaneErrorClassifier struct{}

// Classify retries only transient/rate-limited work and exhausts all other categories immediately.
func (controlPlaneErrorClassifier) Classify(err error) controlplane.RetryDecision {
	if IsCode(err, CodeTransient) || IsCode(err, CodeRateLimited) {
		return controlplane.RetryDecision{Category: "transient", Backoff: time.Second, MaxAttempts: 4}
	}
	return controlplane.RetryDecision{Category: "permanent", MaxAttempts: 1}
}

// webhookCommandFromFields strictly reconstructs the payload-minimal command persisted by executable composition.
func webhookCommandFromFields(fields map[string]string) (WebhookCommand, error) {
	parse := func(name string) (int64, error) { return strconv.ParseInt(fields[name], 10, 64) }
	installation, err := parse("installation_id")
	if err != nil {
		return WebhookCommand{}, &Error{Code: CodeIntegrity, Operation: "control_plane_command"}
	}
	repository, err := parse("repository_id")
	if err != nil {
		return WebhookCommand{}, &Error{Code: CodeIntegrity, Operation: "control_plane_command"}
	}
	pullRequest, err := parse("pull_request_number")
	if err != nil {
		return WebhookCommand{}, &Error{Code: CodeIntegrity, Operation: "control_plane_command"}
	}
	actor, err := parse("actor_id")
	if err != nil {
		return WebhookCommand{}, &Error{Code: CodeIntegrity, Operation: "control_plane_command"}
	}
	reviewRevision, _ := strconv.ParseUint(fields["review_revision"], 10, 64)
	memorySequence, _ := strconv.ParseUint(fields["memory_expected_sequence"], 10, 64)
	memoryVersion, _ := strconv.ParseUint(fields["memory_expected_version"], 10, 64)
	return WebhookCommand{DeliveryID: fields["delivery_id"], Event: fields["event"], InstallationID: installation, RepositoryID: repository, PullRequestNumber: pullRequest, ActorID: actor, SelectionAlias: fields["selection_alias"], PayloadDigest: fields["payload_digest"], ReviewOperation: fields["review_operation"], ReviewID: fields["review_id"], ReviewAction: fields["review_action"], ReviewRevision: reviewRevision, MemoryOperation: fields["memory_operation"], MemoryID: fields["memory_id"], MemoryDecisionID: fields["memory_decision_id"], MemoryExpectedSequence: memorySequence, MemoryExpectedVersion: memoryVersion}, nil
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
