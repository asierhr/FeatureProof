package projectconfig

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ResourceCatalog captures a bounded, non-secret catalog snapshot for validation.
type ResourceCatalog interface {
	Snapshot(context.Context, ProjectKey) (ResourceCatalogSnapshot, error)
}

// Version returns one exact immutable version after project-scoped authorization.
func (service *Service) Version(ctx context.Context, authority ConfigurationAuthority, key ProjectKey, number uint64) (ConfigurationVersion, EffectiveConfiguration, error) {
	if err := authority.authorize(ActionRead, key); err != nil {
		service.observeResult(ctx, "version", authority, key, err)
		return ConfigurationVersion{}, EffectiveConfiguration{}, err
	}
	version, effective, err := service.store.Version(ctx, key, number)
	err = classifyContext(ctx, err, "version")
	service.observeResult(ctx, "version", authority, key, err)
	return version, effective, err
}

// History returns a bounded page using an opaque project-bound cursor.
func (service *Service) History(ctx context.Context, authority ConfigurationAuthority, key ProjectKey, cursor string, limit int) (HistoryPage, error) {
	if err := authority.authorize(ActionRead, key); err != nil {
		service.observeResult(ctx, "history", authority, key, err)
		return HistoryPage{}, err
	}
	before, err := service.decodeCursor(key, cursor)
	if err != nil {
		service.observeResult(ctx, "history", authority, key, err)
		return HistoryPage{}, err
	}
	page, err := service.store.History(ctx, key, before, limit)
	if err != nil {
		err = classifyContext(ctx, err, "history")
		service.observeResult(ctx, "history", authority, key, err)
		return HistoryPage{}, err
	}
	if page.NextCursor != "" {
		value, parseErr := strconv.ParseUint(page.NextCursor, 10, 64)
		if parseErr != nil {
			err = &Error{Category: CategoryIntegrity, Operation: "history.cursor"}
			service.observeResult(ctx, "history", authority, key, err)
			return HistoryPage{}, err
		}
		page.NextCursor = service.encodeCursor(key, value)
	}
	service.observeResult(ctx, "history", authority, key, nil)
	return page, nil
}

// Export returns only strict canonical document bytes, never resolved credentials.
func (service *Service) Export(ctx context.Context, authority ConfigurationAuthority, key ProjectKey, number uint64) ([]byte, error) {
	if err := authority.authorize(ActionExport, key); err != nil {
		service.observeResult(ctx, "export", authority, key, err)
		return nil, err
	}
	version, _, err := service.store.Version(ctx, key, number)
	if err != nil {
		err = classifyContext(ctx, err, "export")
		service.observeResult(ctx, "export", authority, key, err)
		return nil, err
	}
	service.observeResult(ctx, "export", authority, key, nil)
	return append([]byte(nil), version.Canonical...), nil
}

// Binding reloads the immutable configuration authority of an existing run.
func (service *Service) Binding(ctx context.Context, authority ConfigurationAuthority, key ProjectKey, runID string) (RunConfigurationBinding, error) {
	if err := authority.authorize(ActionAdmit, key); err != nil {
		service.observeResult(ctx, "binding", authority, key, err)
		return RunConfigurationBinding{}, err
	}
	binding, err := service.store.Binding(ctx, key, runID)
	err = classifyContext(ctx, err, "binding")
	service.observeResult(ctx, "binding", authority, key, err)
	return binding, err
}

// encodeCursor binds pagination state to one project without exposing raw identifiers.
func (service *Service) encodeCursor(key ProjectKey, sequence uint64) string {
	payload := key.ProjectID + ":" + key.RepositoryID + ":" + strconv.FormatUint(sequence, 10) + ":" + strconv.FormatInt(service.now().UTC().Add(service.cursorTTL).Unix(), 10)
	return base64.RawURLEncoding.EncodeToString([]byte(payload + ":" + service.cursorSignature(payload)))
}

// decodeCursor rejects malformed and cross-project pagination state.
func (service *Service) decodeCursor(key ProjectKey, cursor string) (uint64, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, invalid("history", "cursor")
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 5 || parts[0] != key.ProjectID || parts[1] != key.RepositoryID || !hmac.Equal([]byte(service.cursorSignature(strings.Join(parts[:4], ":"))), []byte(parts[4])) {
		return 0, invalid("history", "cursor")
	}
	value, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil {
		return 0, invalid("history", "cursor")
	}
	expires, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || !service.now().UTC().Before(time.Unix(expires, 0)) {
		return 0, invalid("history", "cursor")
	}
	return value, nil
}

// cursorSignature authenticates pagination scope, boundary and expiry with private composition key material.
func (service *Service) cursorSignature(payload string) string {
	mac := hmac.New(sha256.New, service.cursorKey)
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// OrganizationPolicySource captures the trusted monotonic policy for a project.
type OrganizationPolicySource interface {
	Snapshot(context.Context, ProjectKey) (OrganizationPolicySnapshot, error)
}

// Store is the consumer-owned persistence contract for atomic configuration state.
type Store interface {
	Publish(context.Context, PublishCommand) (PublishReceipt, error)
	Rollback(context.Context, RollbackCommand) (PublishReceipt, error)
	Head(context.Context, ProjectKey) (ConfigurationHead, error)
	Version(context.Context, ProjectKey, uint64) (ConfigurationVersion, EffectiveConfiguration, error)
	Effective(context.Context, ProjectKey, uint64) (EffectiveConfiguration, error)
	History(context.Context, ProjectKey, uint64, int) (HistoryPage, error)
	Bind(context.Context, BindCommand) (RunConfigurationBinding, error)
	Binding(context.Context, ProjectKey, string) (RunConfigurationBinding, error)
}

// PublishCommand contains only canonical state so stores perform no external I/O.
type PublishCommand struct {
	OperationID        string
	CommandDigest      string
	Actor              string
	ExpectedGeneration uint64
	Document           CanonicalDocument
	Effective          EffectiveConfiguration
	CreatedAt          time.Time
}

// RollbackCommand requests a new activation of an immutable historical version.
type RollbackCommand struct {
	OperationID        string
	CommandDigest      string
	Actor              string
	ProjectKey         ProjectKey
	Version            uint64
	ExpectedGeneration uint64
	Effective          EffectiveConfiguration
	CreatedAt          time.Time
}

// BindCommand atomically resolves the current head into one immutable run binding.
type BindCommand struct {
	RunID      string
	ProjectKey ProjectKey
	CreatedAt  time.Time
	Candidate  *AdmissionCandidate
}

// Observer receives allowlisted post-commit facts and is never authoritative.
type Observer interface{ Observe(context.Context, Event) }

// Service coordinates authority, trusted snapshots and atomic persistence.
type Service struct {
	store     Store
	catalog   ResourceCatalog
	policies  OrganizationPolicySource
	observer  Observer
	now       func() time.Time
	cursorKey []byte
	cursorTTL time.Duration
}

// ServiceConfig supplies private cursor sealing and deterministic time without domain defaults.
type ServiceConfig struct {
	CursorKey []byte
	CursorTTL time.Duration
	Now       func() time.Time
}

// NewService wires one selected backend and the trusted snapshot providers.
func NewService(store Store, catalog ResourceCatalog, policies OrganizationPolicySource, observer Observer, config ServiceConfig) (*Service, error) {
	if store == nil || catalog == nil || policies == nil || len(config.CursorKey) < 32 || config.CursorTTL <= 0 {
		return nil, invalid("service", "dependencies")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Service{store: store, catalog: catalog, policies: policies, observer: observer, now: config.Now, cursorKey: append([]byte(nil), config.CursorKey...), cursorTTL: config.CursorTTL}, nil
}

// Validate checks a document against snapshots without changing durable state.
func (service *Service) Validate(ctx context.Context, authority ConfigurationAuthority, key ProjectKey, raw []byte) (EffectiveConfiguration, error) {
	if err := authority.authorize(ActionValidate, key); err != nil {
		service.observeResult(ctx, "validate", authority, key, err)
		return EffectiveConfiguration{}, err
	}
	canonical, err := LoadStrict(bytes.NewReader(raw), MaxCanonicalBytes)
	if err != nil {
		service.observeResult(ctx, "validate", authority, key, err)
		return EffectiveConfiguration{}, classifyContext(ctx, err, "validate")
	}
	if canonical.Document.ProjectID != key.ProjectID || canonical.Document.RepositoryID != key.RepositoryID {
		err = &Error{Category: CategoryUnauthorized, Operation: "validate.scope"}
		service.observeResult(ctx, "validate", authority, key, err)
		return EffectiveConfiguration{}, err
	}
	effective, err := service.resolve(ctx, key, canonical)
	service.observeResult(ctx, "validate", authority, key, err)
	return effective, err
}

// PublishAndActivate validates and atomically advances version, activation and head.
func (service *Service) PublishAndActivate(ctx context.Context, authority ConfigurationAuthority, key ProjectKey, operationID string, expectedGeneration uint64, raw []byte) (PublishReceipt, error) {
	if err := authority.authorize(ActionPublish, key); err != nil {
		service.observeResult(ctx, "publish", authority, key, err)
		return PublishReceipt{}, err
	}
	if !canonicalID.MatchString(operationID) {
		err := invalid("publish", "operation_id")
		service.observeResult(ctx, "publish", authority, key, err)
		return PublishReceipt{}, err
	}
	canonical, err := LoadStrict(bytes.NewReader(raw), MaxCanonicalBytes)
	if err != nil {
		service.observeResult(ctx, "publish", authority, key, err)
		return PublishReceipt{}, classifyContext(ctx, err, "publish")
	}
	if canonical.Document.ProjectID != key.ProjectID || canonical.Document.RepositoryID != key.RepositoryID {
		err = &Error{Category: CategoryUnauthorized, Operation: "publish.scope"}
		service.observeResult(ctx, "publish", authority, key, err)
		return PublishReceipt{}, err
	}
	effective, err := service.resolve(ctx, key, canonical)
	if err != nil {
		service.observeResult(ctx, "publish", authority, key, err)
		return PublishReceipt{}, err
	}
	commandDigest := canonicalDigest([]byte(operationID + ":" + canonical.Digest + ":" + effective.Digest))
	receipt, err := service.store.Publish(ctx, PublishCommand{OperationID: operationID, CommandDigest: commandDigest, Actor: authority.Actor(), ExpectedGeneration: expectedGeneration, Document: canonical, Effective: effective, CreatedAt: service.now().UTC()})
	if err != nil {
		service.observeResult(ctx, "publish", authority, key, err)
		return PublishReceipt{}, classifyContext(ctx, err, "publish")
	}
	service.observe(ctx, Event{Action: "publish", Reason: "success", Actor: authority.Actor(), ProjectKey: key, Version: receipt.Version, Generation: receipt.Generation, Digest: receipt.HeadDigest})
	return receipt, nil
}

// Rollback revalidates a historical version and creates a new activation.
func (service *Service) Rollback(ctx context.Context, authority ConfigurationAuthority, key ProjectKey, operationID string, version, expectedGeneration uint64) (PublishReceipt, error) {
	if err := authority.authorize(ActionRollback, key); err != nil {
		service.observeResult(ctx, "rollback", authority, key, err)
		return PublishReceipt{}, err
	}
	stored, _, err := service.store.Version(ctx, key, version)
	if err != nil {
		err = classifyContext(ctx, err, "rollback")
		service.observeResult(ctx, "rollback", authority, key, err)
		return PublishReceipt{}, err
	}
	canonical, err := LoadStrict(bytes.NewReader(stored.Canonical), MaxCanonicalBytes)
	if err != nil || canonical.Digest != stored.DocumentDigest {
		err = &Error{Category: CategoryIntegrity, Operation: "rollback.version"}
		service.observeResult(ctx, "rollback", authority, key, err)
		return PublishReceipt{}, err
	}
	effective, err := service.resolve(ctx, key, canonical)
	if err != nil {
		service.observeResult(ctx, "rollback", authority, key, err)
		return PublishReceipt{}, err
	}
	digest := canonicalDigest([]byte(operationID + ":" + canonical.Digest + ":" + effective.Digest + ":rollback"))
	receipt, err := service.store.Rollback(ctx, RollbackCommand{OperationID: operationID, CommandDigest: digest, Actor: authority.Actor(), ProjectKey: key, Version: version, ExpectedGeneration: expectedGeneration, Effective: effective, CreatedAt: service.now().UTC()})
	err = classifyContext(ctx, err, "rollback")
	service.observeResult(ctx, "rollback", authority, key, err)
	return receipt, err
}

// Active returns the immutable active version and effective snapshot.
func (service *Service) Active(ctx context.Context, authority ConfigurationAuthority, key ProjectKey) (ConfigurationHead, ConfigurationVersion, EffectiveConfiguration, error) {
	if err := authority.authorize(ActionRead, key); err != nil {
		service.observeResult(ctx, "active", authority, key, err)
		return ConfigurationHead{}, ConfigurationVersion{}, EffectiveConfiguration{}, err
	}
	head, err := service.store.Head(ctx, key)
	if err != nil {
		err = classifyContext(ctx, err, "active")
		service.observeResult(ctx, "active", authority, key, err)
		return ConfigurationHead{}, ConfigurationVersion{}, EffectiveConfiguration{}, err
	}
	version, _, err := service.store.Version(ctx, key, head.Activation.Version)
	if err != nil {
		err = classifyContext(ctx, err, "active")
		service.observeResult(ctx, "active", authority, key, err)
		return ConfigurationHead{}, ConfigurationVersion{}, EffectiveConfiguration{}, err
	}
	effective, err := service.store.Effective(ctx, key, head.Activation.Generation)
	err = classifyContext(ctx, err, "active")
	service.observeResult(ctx, "active", authority, key, err)
	return head, version, effective, err
}

// ResolveForAdmission persists and returns the configuration binding for a run.
func (service *Service) ResolveForAdmission(ctx context.Context, authority ConfigurationAuthority, key ProjectKey, runID string) (RunConfigurationBinding, error) {
	candidate, err := service.PrepareForAdmission(ctx, authority, key, runID)
	if err != nil {
		return RunConfigurationBinding{}, err
	}
	binding, err := service.store.Bind(ctx, BindCommand{RunID: runID, ProjectKey: key, CreatedAt: service.now().UTC(), Candidate: &candidate})
	err = classifyContext(ctx, err, "admit.bind")
	service.observeResult(ctx, "admit", authority, key, err)
	return binding, err
}

// PrepareForAdmission revalidates the active version and creates a head-fenced candidate without persisting it.
func (service *Service) PrepareForAdmission(ctx context.Context, authority ConfigurationAuthority, key ProjectKey, runID string) (AdmissionCandidate, error) {
	if err := authority.authorize(ActionAdmit, key); err != nil {
		service.observeResult(ctx, "ingress", authority, key, err)
		return AdmissionCandidate{}, err
	}
	if !canonicalID.MatchString(runID) {
		err := invalid("admit", "run_id")
		service.observeResult(ctx, "ingress", authority, key, err)
		return AdmissionCandidate{}, err
	}
	head, err := service.store.Head(ctx, key)
	if err != nil {
		err = classifyContext(ctx, err, "admit.head")
		service.observeResult(ctx, "ingress", authority, key, err)
		return AdmissionCandidate{}, err
	}
	version, _, err := service.store.Version(ctx, key, head.Activation.Version)
	if err != nil {
		err = classifyContext(ctx, err, "admit.version")
		service.observeResult(ctx, "ingress", authority, key, err)
		return AdmissionCandidate{}, err
	}
	canonical, err := LoadStrict(bytes.NewReader(version.Canonical), MaxCanonicalBytes)
	if err != nil || canonical.Digest != version.DocumentDigest {
		err = &Error{Category: CategoryIntegrity, Operation: "admit.version"}
		service.observeResult(ctx, "ingress", authority, key, err)
		return AdmissionCandidate{}, err
	}
	current, err := service.resolve(ctx, key, canonical)
	if err != nil {
		service.observeResult(ctx, "ingress", authority, key, err)
		return AdmissionCandidate{}, err
	}
	stored, err := service.store.Effective(ctx, key, head.Activation.Generation)
	if err != nil {
		err = classifyContext(ctx, err, "admit.effective")
		service.observeResult(ctx, "ingress", authority, key, err)
		return AdmissionCandidate{}, err
	}
	if stored.Digest != current.Digest || stored.PolicyDigest != current.PolicyDigest || stored.CatalogDigest != current.CatalogDigest {
		err = &Error{Category: CategoryIncompatibleReference, Operation: "admit.drift"}
		service.observeResult(ctx, "ingress", authority, key, err)
		return AdmissionCandidate{}, err
	}
	binding, err := SealRunBinding(RunConfigurationBinding{RunID: runID, ProjectKey: key, Version: version.Version, VersionDigest: version.DocumentDigest, ActivationGeneration: head.Activation.Generation, ActivationDigest: head.Activation.Digest, PolicyVersion: stored.PolicyVersion, PolicyDigest: stored.PolicyDigest, CatalogDigest: stored.CatalogDigest, EffectiveDigest: stored.Digest, Effective: stored.Clone()})
	if err != nil {
		service.observeResult(ctx, "ingress", authority, key, err)
		return AdmissionCandidate{}, err
	}
	service.observeResult(ctx, "ingress", authority, key, nil)
	return AdmissionCandidate{Binding: binding}, nil
}

// resolve loads trusted snapshots only after authority and structural checks.
func (service *Service) resolve(ctx context.Context, key ProjectKey, document CanonicalDocument) (EffectiveConfiguration, error) {
	policy, err := service.policies.Snapshot(ctx, key)
	if err != nil {
		return EffectiveConfiguration{}, classifyContext(ctx, err, "policy.snapshot")
	}
	catalog, err := service.catalog.Snapshot(ctx, key)
	if err != nil {
		return EffectiveConfiguration{}, classifyContext(ctx, err, "catalog.snapshot")
	}
	return ValidateAndMerge(document, policy.Clone(), catalog.Clone())
}

// observe emits only safe post-commit metadata and cannot change the result.
func (service *Service) observe(ctx context.Context, event Event) {
	if service.observer != nil {
		func() {
			defer func() { _ = recover() }()
			service.observer.Observe(ctx, event)
		}()
	}
}

// observeResult projects only a closed success/error category; raw payloads and collaborator errors never reach telemetry.
func (service *Service) observeResult(ctx context.Context, action string, authority ConfigurationAuthority, key ProjectKey, err error) {
	reason := "success"
	if err != nil {
		reason = "failure"
		var safe *Error
		if errors.As(err, &safe) {
			reason = string(safe.Category)
		}
	}
	actor := authority.Actor()
	if !safeActor(actor) {
		actor = ""
	}
	service.observe(ctx, Event{Action: action, Reason: reason, Actor: actor, ProjectKey: key})
}

// classifyContext preserves cancellation semantics without reflecting collaborator errors.
func classifyContext(ctx context.Context, err error, operation string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return &Error{Category: CategoryCancelled, Operation: operation, Cause: context.Canceled}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{Category: CategoryDeadline, Operation: operation, Cause: context.DeadlineExceeded}
	}
	var safe *Error
	if errors.As(err, &safe) {
		return err
	}
	return &Error{Category: CategoryUnavailable, Operation: operation}
}
