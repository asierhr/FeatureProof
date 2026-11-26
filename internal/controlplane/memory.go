package controlplane

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore is a concurrency-safe contractual fake, not a distributed backend.
type MemoryStore struct {
	mu         sync.Mutex
	jobs       map[string]Job
	epochs     map[string][]BackendEpoch
	heads      map[string]BindingHead
	inbox      map[string]string
	effects    map[string]EffectIntent
	done       map[string]string
	admissions map[string]IngressAdmission
}

// NewMemoryStore constructs an empty deterministic control-plane fake.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{jobs: map[string]Job{}, epochs: map[string][]BackendEpoch{}, heads: map[string]BindingHead{}, inbox: map[string]string{}, effects: map[string]EffectIntent{}, done: map[string]string{}, admissions: map[string]IngressAdmission{}}
}

// scopedMemoryKey prevents identical tenant-owned identifiers from aliasing in the in-memory contractual store.
func scopedMemoryKey(scope ProjectScope, id string) string {
	return scope.OrganizationID + "\x00" + scope.ProjectID + "\x00" + id
}

// jobScope extracts the validated composite coordinate sealed into a job.
func jobScope(job Job) ProjectScope {
	return ProjectScope{OrganizationID: job.OrganizationID, ProjectID: job.ProjectID}
}

// Enqueue inserts one canonical job or returns its identical prior reservation.
func (store *MemoryStore) Enqueue(ctx context.Context, job Job) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	sealed, err := SealJob(job)
	if err != nil {
		return Job{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if current, ok := store.jobs[scopedMemoryKey(jobScope(sealed), sealed.JobID)]; ok {
		if current.Digest != sealed.Digest {
			return Job{}, ErrConflict
		}
		return current, nil
	}
	store.jobs[scopedMemoryKey(jobScope(sealed), sealed.JobID)] = sealed
	return sealed, nil
}

// Claim leases due jobs in stable order and increments their fencing token.
func (store *MemoryStore) Claim(ctx context.Context, request ClaimRequest) ([]Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.Scope.Validate() != nil || !safeID.MatchString(request.WorkerID) || request.Lease <= 0 || request.Limit <= 0 {
		return nil, ErrInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	ordered := make([]Job, 0, len(store.jobs))
	for _, job := range store.jobs {
		ordered = append(ordered, job)
	}
	sort.Slice(ordered, func(left, right int) bool {
		if !ordered[left].AvailableAt.Equal(ordered[right].AvailableAt) {
			return ordered[left].AvailableAt.Before(ordered[right].AvailableAt)
		}
		if !ordered[left].CreatedAt.Equal(ordered[right].CreatedAt) {
			return ordered[left].CreatedAt.Before(ordered[right].CreatedAt)
		}
		return ordered[left].JobID < ordered[right].JobID
	})
	claimed := make([]Job, 0, request.Limit)
	for _, queued := range ordered {
		job := store.jobs[scopedMemoryKey(jobScope(queued), queued.JobID)]
		if len(claimed) == request.Limit {
			break
		}
		if job.OrganizationID != request.Scope.OrganizationID || job.ProjectID != request.Scope.ProjectID || (job.State != JobQueued && job.State != JobRetryWait) || job.AvailableAt.After(request.Now) || !job.Deadline.After(request.Now) {
			continue
		}
		leaseUntil := request.Now.Add(request.Lease)
		if leaseUntil.After(job.Deadline) {
			leaseUntil = job.Deadline
		}
		job.State, job.Owner, job.LeaseUntil, job.Attempt, job.Fence = JobLeased, request.WorkerID, leaseUntil, job.Attempt+1, job.Fence+1
		job, _ = SealJob(job)
		store.jobs[scopedMemoryKey(jobScope(job), job.JobID)] = job
		claimed = append(claimed, job)
	}
	return claimed, nil
}

// Heartbeat extends only the currently owned live lease.
func (store *MemoryStore) Heartbeat(ctx context.Context, scope ProjectScope, id, owner string, fence, cancelRevision uint64, now time.Time, lease time.Duration) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	job, ok := store.jobs[scopedMemoryKey(scope, id)]
	if !ok {
		return Job{}, ErrNotFound
	}
	if lease <= 0 || job.State != JobLeased || job.Owner != owner || job.Fence != fence || job.CancelRevision != cancelRevision || !job.LeaseUntil.After(now) {
		return Job{}, ErrConflict
	}
	until := now.Add(lease)
	if until.After(job.Deadline) {
		until = job.Deadline
	}
	if !until.After(now) {
		return Job{}, ErrDeadline
	}
	job.LeaseUntil = until
	job, _ = SealJob(job)
	store.jobs[scopedMemoryKey(scope, id)] = job
	return job, nil
}

// Complete commits a terminal only for the current owner and fence.
func (store *MemoryStore) Complete(ctx context.Context, scope ProjectScope, id, owner string, fence, cancelRevision uint64, now time.Time, state JobState, category string) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	if state != JobSucceeded && state != JobFailed {
		return Job{}, ErrInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	job, ok := store.jobs[scopedMemoryKey(scope, id)]
	if !ok {
		return Job{}, ErrNotFound
	}
	if job.State != JobLeased || job.Owner != owner || job.Fence != fence || job.CancelRevision != cancelRevision || !job.LeaseUntil.After(now) {
		return Job{}, ErrConflict
	}
	job.State, job.Owner, job.LeaseUntil, job.LastCategory = state, "", time.Time{}, category
	job, _ = SealJob(job)
	store.jobs[scopedMemoryKey(scope, id)] = job
	return job, nil
}

// Retry persists delay and category before releasing the current lease.
func (store *MemoryStore) Retry(ctx context.Context, scope ProjectScope, id, owner string, fence, cancelRevision uint64, now time.Time, decision RetryDecision, available time.Time) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	if decision.MaxAttempts == 0 || decision.Backoff < 0 {
		return Job{}, ErrInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	job, ok := store.jobs[scopedMemoryKey(scope, id)]
	if !ok {
		return Job{}, ErrNotFound
	}
	if job.State != JobLeased || job.Owner != owner || job.Fence != fence || job.CancelRevision != cancelRevision || !job.LeaseUntil.After(now) {
		return Job{}, ErrConflict
	}
	if job.Attempt >= decision.MaxAttempts || !available.Before(job.Deadline) {
		job.State = JobDead
	} else {
		job.State, job.AvailableAt = JobRetryWait, available
	}
	job.Owner, job.LeaseUntil, job.LastCategory = "", time.Time{}, decision.Category
	job, _ = SealJob(job)
	store.jobs[scopedMemoryKey(scope, id)] = job
	return job, nil
}

// Cancel advances the durable cancellation revision and rejects stale commands.
func (store *MemoryStore) Cancel(ctx context.Context, scope ProjectScope, id string, revision uint64) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	job, ok := store.jobs[scopedMemoryKey(scope, id)]
	if !ok {
		return Job{}, ErrNotFound
	}
	if revision <= job.CancelRevision {
		if revision == job.CancelRevision {
			return job, nil
		}
		return Job{}, ErrConflict
	}
	job.CancelRevision = revision
	job.State, job.Owner, job.LeaseUntil, job.Fence = JobCancelled, "", time.Time{}, job.Fence+1
	job, _ = SealJob(job)
	store.jobs[scopedMemoryKey(scope, id)] = job
	return job, nil
}

// Recover releases expired leases while preserving their newer fence.
func (store *MemoryStore) Recover(ctx context.Context, scope ProjectScope, now time.Time, limit int) (int, error) {
	jobs, err := store.RecoverWithJobs(ctx, scope, now, limit)
	return len(jobs), err
}

// RecoverWithJobs requeues expired leases and returns their exact durable
// identities for downstream quota reconciliation.
func (store *MemoryStore) RecoverWithJobs(ctx context.Context, scope ProjectScope, now time.Time, limit int) ([]Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, ErrInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	var recovered []Job
	for id, job := range store.jobs {
		if len(recovered) == limit {
			break
		}
		if job.OrganizationID == scope.OrganizationID && job.ProjectID == scope.ProjectID && job.State == JobLeased && !job.LeaseUntil.After(now) {
			job.State, job.Owner, job.LeaseUntil, job.AvailableAt = JobRetryWait, "", time.Time{}, now
			job, _ = SealJob(job)
			store.jobs[id] = job
			recovered = append(recovered, job)
		}
	}
	return recovered, nil
}

// ReserveInitial atomically publishes epoch one and its active head.
func (store *MemoryStore) ReserveInitial(ctx context.Context, epoch BackendEpoch) (BindingHead, error) {
	if err := ctx.Err(); err != nil {
		return BindingHead{}, err
	}
	sealed, err := SealBackendEpoch(epoch)
	if err != nil || sealed.Epoch != 1 {
		return BindingHead{}, ErrInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	key := scopedMemoryKey(ProjectScope{OrganizationID: sealed.OrganizationID, ProjectID: sealed.ProjectID}, sealed.RunID)
	if head, ok := store.heads[key]; ok {
		if store.epochs[key][0].Digest == sealed.Digest {
			return head, nil
		}
		return BindingHead{}, ErrConflict
	}
	head, _ := SealBindingHead(BindingHead{OrganizationID: sealed.OrganizationID, RunID: sealed.RunID, ProjectID: sealed.ProjectID, Epoch: 1, EpochDigest: sealed.Digest, Revision: 1})
	store.epochs[key], store.heads[key] = []BackendEpoch{sealed}, head
	return head, nil
}

// Load returns detached current head and epoch without fallback.
func (store *MemoryStore) Load(ctx context.Context, scope ProjectScope, runID string) (BindingHead, BackendEpoch, error) {
	if err := ctx.Err(); err != nil {
		return BindingHead{}, BackendEpoch{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	key := scopedMemoryKey(scope, runID)
	head, ok := store.heads[key]
	if !ok {
		return BindingHead{}, BackendEpoch{}, ErrNotFound
	}
	epochs := store.epochs[key]
	epoch := epochs[len(epochs)-1]
	if VerifyBindingHead(head, epoch) != nil {
		return BindingHead{}, BackendEpoch{}, ErrIntegrity
	}
	return head, epoch, nil
}

// Cutover compare-and-swaps a verified head to one new immutable epoch.
func (store *MemoryStore) Cutover(ctx context.Context, expected BindingHead, next BackendEpoch) (BindingHead, error) {
	if err := ctx.Err(); err != nil {
		return BindingHead{}, err
	}
	sealed, err := SealBackendEpoch(next)
	if err != nil {
		return BindingHead{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	key := scopedMemoryKey(ProjectScope{OrganizationID: expected.OrganizationID, ProjectID: expected.ProjectID}, expected.RunID)
	current, ok := store.heads[key]
	if !ok {
		return BindingHead{}, ErrNotFound
	}
	if current.Digest != expected.Digest || sealed.RunID != current.RunID || sealed.OrganizationID != current.OrganizationID || sealed.ProjectID != current.ProjectID || sealed.Epoch != current.Epoch+1 || sealed.PreviousDigest != current.EpochDigest {
		return BindingHead{}, ErrConflict
	}
	head, _ := SealBindingHead(BindingHead{OrganizationID: sealed.OrganizationID, RunID: sealed.RunID, ProjectID: sealed.ProjectID, Epoch: sealed.Epoch, EpochDigest: sealed.Digest, Revision: current.Revision + 2})
	store.epochs[key] = append(store.epochs[key], sealed)
	store.heads[key] = head
	return head, nil
}

// Accept atomically records ingress identity and its initial job.
func (store *MemoryStore) Accept(ctx context.Context, envelope InboxEnvelope, job Job) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	envelope, err := SealInboxEnvelope(envelope)
	if err != nil || envelope.OrganizationID != job.OrganizationID || envelope.ProjectID != job.ProjectID {
		return false, ErrInvalid
	}
	sealed, err := SealJob(job)
	if err != nil {
		return false, err
	}
	key := scopedMemoryKey(ProjectScope{OrganizationID: envelope.OrganizationID, ProjectID: envelope.ProjectID}, envelope.Source+":"+envelope.IdempotencyKey)
	store.mu.Lock()
	defer store.mu.Unlock()
	if current, ok := store.inbox[key]; ok {
		if current != envelope.RequestDigest {
			return false, ErrConflict
		}
		return false, nil
	}
	if _, exists := store.jobs[scopedMemoryKey(jobScope(sealed), sealed.JobID)]; exists {
		return false, ErrConflict
	}
	store.inbox[key], store.jobs[scopedMemoryKey(jobScope(sealed), sealed.JobID)] = envelope.RequestDigest, sealed
	return true, nil
}

// Admit publishes inbox, initial local epoch/head and first job under the fake's single lock.
func (store *MemoryStore) Admit(ctx context.Context, admission IngressAdmission) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	epoch, err := SealBackendEpoch(admission.Epoch)
	if err != nil || epoch.Mode != BackendLocal || epoch.Epoch != 1 {
		return false, ErrInvalid
	}
	job := admission.Job
	if job.RunID != epoch.RunID || job.OrganizationID != epoch.OrganizationID || job.ProjectID != epoch.ProjectID || job.BackendEpoch != epoch.Epoch || job.BindingDigest != epoch.Digest {
		return false, ErrInvalid
	}
	sealed, err := SealJob(job)
	if err != nil {
		return false, err
	}
	envelope, err := SealInboxEnvelope(admission.Envelope)
	if err != nil || envelope.OrganizationID != epoch.OrganizationID || envelope.ProjectID != epoch.ProjectID {
		return false, ErrInvalid
	}
	key := scopedMemoryKey(ProjectScope{OrganizationID: envelope.OrganizationID, ProjectID: envelope.ProjectID}, envelope.Source+":"+envelope.IdempotencyKey)
	store.mu.Lock()
	defer store.mu.Unlock()
	if current, ok := store.inbox[key]; ok {
		if current != envelope.RequestDigest {
			return false, ErrConflict
		}
		return false, nil
	}
	if _, exists := store.heads[scopedMemoryKey(ProjectScope{OrganizationID: epoch.OrganizationID, ProjectID: epoch.ProjectID}, epoch.RunID)]; exists {
		return false, ErrConflict
	}
	head, _ := SealBindingHead(BindingHead{OrganizationID: epoch.OrganizationID, RunID: epoch.RunID, ProjectID: epoch.ProjectID, Epoch: epoch.Epoch, EpochDigest: epoch.Digest, Revision: 1})
	store.inbox[key], store.jobs[scopedMemoryKey(jobScope(sealed), sealed.JobID)] = envelope.RequestDigest, sealed
	store.epochs[scopedMemoryKey(ProjectScope{OrganizationID: epoch.OrganizationID, ProjectID: epoch.ProjectID}, epoch.RunID)], store.heads[scopedMemoryKey(ProjectScope{OrganizationID: epoch.OrganizationID, ProjectID: epoch.ProjectID}, epoch.RunID)] = []BackendEpoch{epoch}, head
	store.admissions[scopedMemoryKey(ProjectScope{OrganizationID: epoch.OrganizationID, ProjectID: epoch.ProjectID}, epoch.RunID)] = IngressAdmission{Envelope: envelope, Epoch: epoch, Job: sealed}
	return true, nil
}

// LoadIngress returns a detached allowlisted command and verifies its active local epoch.
func (store *MemoryStore) LoadIngress(ctx context.Context, scope ProjectScope, runID string) (IngressAdmission, error) {
	if err := ctx.Err(); err != nil {
		return IngressAdmission{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	value, ok := store.admissions[scopedMemoryKey(scope, runID)]
	if !ok {
		return IngressAdmission{}, ErrNotFound
	}
	fields := map[string]string{}
	for key, item := range value.Envelope.CommandFields {
		fields[key] = item
	}
	value.Envelope.CommandFields = fields
	return value, nil
}

// Prepare reserves one stable external operation before the effect.
func (store *MemoryStore) Prepare(ctx context.Context, intent EffectIntent) (EffectIntent, error) {
	if err := ctx.Err(); err != nil {
		return EffectIntent{}, err
	}
	if !safeID.MatchString(intent.OperationID) || !safeID.MatchString(intent.IdempotencyKey) || !safeID.MatchString(intent.OrganizationID) || !safeID.MatchString(intent.ProjectID) || !safeID.MatchString(intent.RunID) || !digestPattern.MatchString(intent.BindingDigest) || !digestPattern.MatchString(intent.InputDigest) {
		return EffectIntent{}, ErrInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if current, ok := store.effects[scopedMemoryKey(ProjectScope{OrganizationID: intent.OrganizationID, ProjectID: intent.ProjectID}, intent.OperationID)]; ok {
		if digest(current) != digest(intent) {
			return EffectIntent{}, ErrConflict
		}
		return current, nil
	}
	store.effects[scopedMemoryKey(ProjectScope{OrganizationID: intent.OrganizationID, ProjectID: intent.ProjectID}, intent.OperationID)] = intent
	return intent, nil
}

// Confirm records a redacted receipt exactly once for a prepared effect.
func (store *MemoryStore) Confirm(ctx context.Context, scope ProjectScope, operationID, receiptDigest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !digestPattern.MatchString(receiptDigest) {
		return ErrInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.effects[scopedMemoryKey(scope, operationID)]; !ok {
		return ErrNotFound
	}
	if current, ok := store.done[scopedMemoryKey(scope, operationID)]; ok && current != receiptDigest {
		return ErrConflict
	}
	store.done[scopedMemoryKey(scope, operationID)] = receiptDigest
	return nil
}

// IsTerminal classifies immutable queue states for recovery and tests.
func IsTerminal(state JobState) bool {
	return state == JobSucceeded || state == JobFailed || state == JobDead || state == JobCancelled
}
