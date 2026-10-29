package controlplane

import (
	"context"
	"time"
)

// BackendCoordinate is the immutable authority observed before consumer work;
// PostgreSQL mutations present it again so a cutover cannot adopt stale work.
type BackendCoordinate struct {
	RunID, BackendIdentity, BindingDigest string
	Epoch                                 uint64
	Owner                                 string
	Fence, CancelRevision                 uint64
	LeaseUntil                            time.Time
}

// backendCoordinateKey isolates the epoch proof from unrelated context values
// while an operation crosses consumer-owned store boundaries.
type backendCoordinateKey struct{}

// WithBackendCoordinate attaches an immutable read/prepare coordinate to all
// downstream consumer-owned mutations performed by the same operation.
func WithBackendCoordinate(ctx context.Context, coordinate BackendCoordinate) context.Context {
	return context.WithValue(ctx, backendCoordinateKey{}, coordinate)
}

// BackendCoordinateFromContext returns the operation authority supplied by
// composition without allowing adapters to silently replace it with the head.
func BackendCoordinateFromContext(ctx context.Context) (BackendCoordinate, bool) {
	coordinate, ok := ctx.Value(backendCoordinateKey{}).(BackendCoordinate)
	return coordinate, ok
}

// ClaimRequest bounds one durable queue scan owned by a worker.
type ClaimRequest struct {
	ProjectID string
	WorkerID  string
	Now       time.Time
	Lease     time.Duration
	Limit     int
}

// JobQueue is the scheduler-owned persistence contract for fenced work.
type JobQueue interface {
	Enqueue(context.Context, Job) (Job, error)
	Claim(context.Context, ClaimRequest) ([]Job, error)
	Heartbeat(context.Context, string, string, uint64, uint64, time.Time, time.Duration) (Job, error)
	Complete(context.Context, string, string, uint64, uint64, time.Time, JobState, string) (Job, error)
	Retry(context.Context, string, string, uint64, uint64, time.Time, RetryDecision, time.Time) (Job, error)
	Cancel(context.Context, string, uint64) (Job, error)
	Recover(context.Context, time.Time, int) (int, error)
}

// BackendBindings owns immutable backend epochs and the single active head.
type BackendBindings interface {
	ReserveInitial(context.Context, BackendEpoch) (BindingHead, error)
	Load(context.Context, string) (BindingHead, BackendEpoch, error)
	Cutover(context.Context, BindingHead, BackendEpoch) (BindingHead, error)
}

// IngressStore persists an allowlisted request before the transport acknowledges it.
type IngressStore interface {
	Admit(context.Context, IngressAdmission) (bool, error)
}

// IngressReader reloads the allowlisted command needed by a restarted worker without retaining raw webhook bytes.
type IngressReader interface {
	LoadIngress(context.Context, string) (IngressAdmission, error)
}

// IngressAdmission binds one authenticated inbox identity to its initial backend epoch, run event, job, authority grant, and first external-effect intent.
type IngressAdmission struct {
	Envelope InboxEnvelope
	Epoch    BackendEpoch
	Job      Job
	Grant    AuthorityGrant
	Intent   EffectIntent
}

// EffectStore persists and fences side-effect intent before external I/O.
type EffectStore interface {
	Prepare(context.Context, EffectIntent) (EffectIntent, error)
	Confirm(context.Context, string, string) error
}

// EffectClaim is the durable owner/fence lease that alone authorizes one external effect attempt.
type EffectClaim struct {
	OperationID string
	ProjectID   string
	Owner       string
	LeaseUntil  time.Time
	Fence       uint64
	Attempts    uint64
}

// EffectOutbox is the productive dispatcher protocol; callers cannot execute an effect without a live claim.
type EffectOutbox interface {
	ClaimEffect(context.Context, string, string, time.Time, time.Duration) (EffectClaim, error)
	ClaimOutbox(context.Context, string, time.Time, time.Duration, int) ([]EffectClaim, error)
	LoadEffect(context.Context, EffectClaim) (EffectIntent, error)
	HeartbeatOutbox(context.Context, EffectClaim, time.Time) error
	RetryOutbox(context.Context, EffectClaim, time.Time, string) error
	ConfirmOutbox(context.Context, EffectClaim, string) error
	RecoverOutbox(context.Context, time.Time, int) (int, error)
}

// EffectJobCompleter atomically confirms an external effect and the exact
// leased job whose fence authorized it, eliminating a response-loss gap.
type EffectJobCompleter interface {
	ConfirmEffectAndJob(context.Context, EffectClaim, Job, string, time.Time) error
}

// Clock supplies deterministic scheduler time without hidden timers.
type Clock interface {
	Now() time.Time
}

// Waiter transfers ownership of blocking and cancellation to composition.
type Waiter interface {
	Wait(context.Context, time.Duration) error
}

// JobHandler owns one claimed job until it reaches a fenced terminal or a
// retry decision; schedulers never execute product work without this port.
type JobHandler interface {
	Handle(context.Context, Job) error
}

// ErrorClassifier converts worker failures into closed retry decisions.
type ErrorClassifier interface {
	Classify(error) RetryDecision
}

// SecretResolver resolves references only after authority verification succeeds.
type SecretResolver interface {
	Resolve(context.Context, []string) (map[string]string, error)
}

// AuthorityGrantStore loads composition-owned catalog, policy and binding grants without caller-supplied capabilities.
type AuthorityGrantStore interface {
	LoadAuthorityGrant(context.Context, string, Phase) (AuthorityGrant, error)
}

// AuthorityEnvelopeStore persists and revalidates the exact worker envelope before product work or private secret resolution.
type AuthorityEnvelopeStore interface {
	AuthorityGrantStore
	LiveAuthorityVerifier
	SaveAuthority(context.Context, AuthorityEnvelope, Job, string, time.Time) error
}

// RetentionReferenceWriter records and retires authenticated live references
// so retention cannot delete queue state still used by a product operation.
type RetentionReferenceWriter interface {
	SaveRetentionReference(context.Context, Job, string, string, bool, string, time.Time) error
}

// LiveAuthorityVerifier rechecks persisted head, project, phase, owner, fence and expiry immediately before JIT secret access.
type LiveAuthorityVerifier interface {
	VerifyLiveAuthority(context.Context, AuthorityEnvelope, string, time.Time) error
}

// SchedulerConfig bounds claims, leases, retries and idle waits.
type SchedulerConfig struct {
	ProjectID         string
	WorkerID          string
	Lease             time.Duration
	HeartbeatInterval time.Duration
	BatchSize         int
	IdleWait          time.Duration
}
