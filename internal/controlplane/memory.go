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
	if current, ok := store.jobs[sealed.JobID]; ok {
		if current.Digest != sealed.Digest {
			return Job{}, ErrConflict
		}
		return current, nil
	}
	store.jobs[sealed.JobID] = sealed
	return sealed, nil
}

// Claim leases due jobs in stable order and increments their fencing token.
func (store *MemoryStore) Claim(ctx context.Context, request ClaimRequest) ([]Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !safeID.MatchString(request.ProjectID) || !safeID.MatchString(request.WorkerID) || request.Lease <= 0 || request.Limit <= 0 {
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
		job := store.jobs[queued.JobID]
		if len(claimed) == request.Limit {
			break
		}
		if job.ProjectID != request.ProjectID || (job.State != JobQueued && job.State != JobRetryWait) || job.AvailableAt.After(request.Now) || !job.Deadline.After(request.Now) {
			continue
		}
		leaseUntil := request.Now.Add(request.Lease)
		if leaseUntil.After(job.Deadline) {
			leaseUntil = job.Deadline
		}
		job.State, job.Owner, job.LeaseUntil, job.Attempt, job.Fence = JobLeased, request.WorkerID, leaseUntil, job.Attempt+1, job.Fence+1
		job, _ = SealJob(job)
		store.jobs[job.JobID] = job
		claimed = append(claimed, job)
	}
	return claimed, nil
}

// Heartbeat extends only the currently owned live lease.
func (store *MemoryStore) Heartbeat(ctx context.Context, id, owner string, fence, cancelRevision uint64, now time.Time, lease time.Duration) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	job, ok := store.jobs[id]
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
	store.jobs[id] = job
	return job, nil
}

// Complete commits a terminal only for the current owner and fence.
func (store *MemoryStore) Complete(ctx context.Context, id, owner string, fence, cancelRevision uint64, now time.Time, state JobState, category string) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	if state != JobSucceeded && state != JobFailed {
		return Job{}, ErrInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	job, ok := store.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	if job.State != JobLeased || job.Owner != owner || job.Fence != fence || job.CancelRevision != cancelRevision || !job.LeaseUntil.After(now) {
		return Job{}, ErrConflict
	}
	job.State, job.Owner, job.LeaseUntil, job.LastCategory = state, "", time.Time{}, category
	job, _ = SealJob(job)
	store.jobs[id] = job
	return job, nil
}

// Retry persists delay and category before releasing the current lease.
func (store *MemoryStore) Retry(ctx context.Context, id, owner string, fence, cancelRevision uint64, now time.Time, decision RetryDecision, available time.Time) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	if decision.MaxAttempts == 0 || decision.Backoff < 0 {
		return Job{}, ErrInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	job, ok := store.jobs[id]
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
	store.jobs[id] = job
	return job, nil
}

// Cancel advances the durable cancellation revision and rejects stale commands.
func (store *MemoryStore) Cancel(ctx context.Context, id string, revision uint64) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	job, ok := store.jobs[id]
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
	store.jobs[id] = job
	return job, nil
}

// Recover releases expired leases while preserving their newer fence.
func (store *MemoryStore) Recover(ctx context.Context, now time.Time, limit int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if limit <= 0 {
		return 0, ErrInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	count := 0
	for id, job := range store.jobs {
		if count == limit {
			break
		}
		if job.State == JobLeased && !job.LeaseUntil.After(now) {
			job.State, job.Owner, job.LeaseUntil, job.AvailableAt = JobRetryWait, "", time.Time{}, now
			job, _ = SealJob(job)
			store.jobs[id] = job
			count++
		}
	}
	return count, nil
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
	if head, ok := store.heads[sealed.RunID]; ok {
		if store.epochs[sealed.RunID][0].Digest == sealed.Digest {
			return head, nil
		}
		return BindingHead{}, ErrConflict
	}
	head, _ := SealBindingHead(BindingHead{RunID: sealed.RunID, ProjectID: sealed.ProjectID, Epoch: 1, EpochDigest: sealed.Digest, Revision: 1})
	store.epochs[sealed.RunID], store.heads[sealed.RunID] = []BackendEpoch{sealed}, head
	return head, nil
}

// Load returns detached current head and epoch without fallback.
func (store *MemoryStore) Load(ctx context.Context, runID string) (BindingHead, BackendEpoch, error) {
	if err := ctx.Err(); err != nil {
		return BindingHead{}, BackendEpoch{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	head, ok := store.heads[runID]
	if !ok {
		return BindingHead{}, BackendEpoch{}, ErrNotFound
	}
	epochs := store.epochs[runID]
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
	current, ok := store.heads[expected.RunID]
	if !ok {
		return BindingHead{}, ErrNotFound
	}
	if current.Digest != expected.Digest || sealed.RunID != current.RunID || sealed.ProjectID != current.ProjectID || sealed.Epoch != current.Epoch+1 || sealed.PreviousDigest != current.EpochDigest {
		return BindingHead{}, ErrConflict
	}
	head, _ := SealBindingHead(BindingHead{RunID: sealed.RunID, ProjectID: sealed.ProjectID, Epoch: sealed.Epoch, EpochDigest: sealed.Digest, Revision: current.Revision + 2})
	store.epochs[sealed.RunID] = append(store.epochs[sealed.RunID], sealed)
	store.heads[sealed.RunID] = head
	return head, nil
}

// Accept atomically records ingress identity and its initial job.
func (store *MemoryStore) Accept(ctx context.Context, envelope InboxEnvelope, job Job) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	envelope, err := SealInboxEnvelope(envelope)
	if err != nil || envelope.ProjectID != job.ProjectID {
		return false, ErrInvalid
	}
	sealed, err := SealJob(job)
	if err != nil {
		return false, err
	}
	key := envelope.Source + ":" + envelope.IdempotencyKey
	store.mu.Lock()
	defer store.mu.Unlock()
	if current, ok := store.inbox[key]; ok {
		if current != envelope.RequestDigest {
			return false, ErrConflict
		}
		return false, nil
	}
	if _, exists := store.jobs[sealed.JobID]; exists {
		return false, ErrConflict
	}
	store.inbox[key], store.jobs[sealed.JobID] = envelope.RequestDigest, sealed
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
	if job.RunID != epoch.RunID || job.ProjectID != epoch.ProjectID || job.BackendEpoch != epoch.Epoch || job.BindingDigest != epoch.Digest {
		return false, ErrInvalid
	}
	sealed, err := SealJob(job)
	if err != nil {
		return false, err
	}
	envelope, err := SealInboxEnvelope(admission.Envelope)
	if err != nil || envelope.ProjectID != epoch.ProjectID {
		return false, ErrInvalid
	}
	key := envelope.Source + ":" + envelope.IdempotencyKey
	store.mu.Lock()
	defer store.mu.Unlock()
	if current, ok := store.inbox[key]; ok {
		if current != envelope.RequestDigest {
			return false, ErrConflict
		}
		return false, nil
	}
	if _, exists := store.heads[epoch.RunID]; exists {
		return false, ErrConflict
	}
	head, _ := SealBindingHead(BindingHead{RunID: epoch.RunID, ProjectID: epoch.ProjectID, Epoch: epoch.Epoch, EpochDigest: epoch.Digest, Revision: 1})
	store.inbox[key], store.jobs[sealed.JobID] = envelope.RequestDigest, sealed
	store.epochs[epoch.RunID], store.heads[epoch.RunID] = []BackendEpoch{epoch}, head
	store.admissions[epoch.RunID] = IngressAdmission{Envelope: envelope, Epoch: epoch, Job: sealed}
	return true, nil
}

// LoadIngress returns a detached allowlisted command and verifies its active local epoch.
func (store *MemoryStore) LoadIngress(ctx context.Context, runID string) (IngressAdmission, error) {
	if err := ctx.Err(); err != nil {
		return IngressAdmission{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	value, ok := store.admissions[runID]
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
	if !safeID.MatchString(intent.OperationID) || !safeID.MatchString(intent.IdempotencyKey) || !safeID.MatchString(intent.ProjectID) || !safeID.MatchString(intent.RunID) || !digestPattern.MatchString(intent.BindingDigest) || !digestPattern.MatchString(intent.InputDigest) {
		return EffectIntent{}, ErrInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if current, ok := store.effects[intent.OperationID]; ok {
		if digest(current) != digest(intent) {
			return EffectIntent{}, ErrConflict
		}
		return current, nil
	}
	store.effects[intent.OperationID] = intent
	return intent, nil
}

// Confirm records a redacted receipt exactly once for a prepared effect.
func (store *MemoryStore) Confirm(ctx context.Context, operationID, receiptDigest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !digestPattern.MatchString(receiptDigest) {
		return ErrInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.effects[operationID]; !ok {
		return ErrNotFound
	}
	if current, ok := store.done[operationID]; ok && current != receiptDigest {
		return ErrConflict
	}
	store.done[operationID] = receiptDigest
	return nil
}

// IsTerminal classifies immutable queue states for recovery and tests.
func IsTerminal(state JobState) bool {
	return state == JobSucceeded || state == JobFailed || state == JobDead || state == JobCancelled
}
