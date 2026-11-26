package controlplane

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// ClaimQuotaReceipt is the opaque durable reservation identity created before a queue claim.
type ClaimQuotaReceipt struct{ ID, Digest string }

// ClaimQuotaGate is the scheduler-owned quota port; composition maps it to tenant/project policy.
type ClaimQuotaGate interface {
	ReserveClaim(context.Context, ClaimRequest) (ClaimQuotaReceipt, error)
	ConfirmClaim(context.Context, ClaimQuotaReceipt, []Job) error
	RejectClaim(context.Context, ClaimQuotaReceipt, bool) error
	ReleaseClaim(context.Context, ProjectScope, string, bool) error
}

// JobRecoveryReporter returns the exact durable jobs whose expired leases were
// reconciled so quota release never guesses from an aggregate count.
type JobRecoveryReporter interface {
	RecoverWithJobs(context.Context, ProjectScope, time.Time, int) ([]Job, error)
}

// QuotaJobQueue places a mandatory durable quota reservation before the underlying queue claim.
type QuotaJobQueue struct {
	next  JobQueue
	quota ClaimQuotaGate
	mu    sync.Mutex
	jobs  map[string]ClaimQuotaReceipt
}

// NewQuotaJobQueue rejects scheduler composition that omits either queue persistence or quota admission.
func NewQuotaJobQueue(next JobQueue, quota ClaimQuotaGate) (*QuotaJobQueue, error) {
	if next == nil || quota == nil {
		return nil, ErrInvalid
	}
	return &QuotaJobQueue{next: next, quota: quota, jobs: map[string]ClaimQuotaReceipt{}}, nil
}

// Claim reserves before touching the queue and retains capacity when the queue outcome is unknown.
func (queue *QuotaJobQueue) Claim(ctx context.Context, request ClaimRequest) ([]Job, error) {
	receipt, err := queue.quota.ReserveClaim(ctx, request)
	if err != nil {
		return nil, err
	}
	jobs, claimErr := queue.next.Claim(ctx, request)
	if claimErr != nil {
		if reconcileErr := queue.quota.RejectClaim(ctx, receipt, true); reconcileErr != nil {
			return nil, reconcileErr
		}
		return nil, claimErr
	}
	if err = queue.quota.ConfirmClaim(ctx, receipt, jobs); err != nil {
		return nil, err
	}
	if len(jobs) > 0 {
		queue.mu.Lock()
		for _, job := range jobs {
			queue.jobs[quotaJobKey(ProjectScope{OrganizationID: job.OrganizationID, ProjectID: job.ProjectID}, job.JobID)] = receipt
		}
		queue.mu.Unlock()
	}
	return jobs, nil
}

// Enqueue delegates a non-claim mutation unchanged.
func (queue *QuotaJobQueue) Enqueue(ctx context.Context, job Job) (Job, error) {
	return queue.next.Enqueue(ctx, job)
}

// Heartbeat delegates renewal for capacity already admitted at claim.
func (queue *QuotaJobQueue) Heartbeat(ctx context.Context, scope ProjectScope, jobID, workerID string, fence, revision uint64, now time.Time, lease time.Duration) (Job, error) {
	return queue.next.Heartbeat(ctx, scope, jobID, workerID, fence, revision, now, lease)
}

// Complete delegates terminal publication for capacity already admitted at claim.
func (queue *QuotaJobQueue) Complete(ctx context.Context, scope ProjectScope, jobID, workerID string, fence, revision uint64, now time.Time, state JobState, detail string) (Job, error) {
	job, err := queue.next.Complete(ctx, scope, jobID, workerID, fence, revision, now, state, detail)
	if err == nil {
		err = queue.releaseJob(ctx, scope, jobID)
	}
	return job, err
}

// Retry delegates rescheduling for capacity already admitted at claim.
func (queue *QuotaJobQueue) Retry(ctx context.Context, scope ProjectScope, jobID, workerID string, fence, revision uint64, now time.Time, decision RetryDecision, available time.Time) (Job, error) {
	job, err := queue.next.Retry(ctx, scope, jobID, workerID, fence, revision, now, decision, available)
	if err == nil {
		err = queue.releaseJob(ctx, scope, jobID)
	}
	return job, err
}

// Cancel delegates cancellation without creating new worker capacity.
func (queue *QuotaJobQueue) Cancel(ctx context.Context, scope ProjectScope, jobID string, revision uint64) (Job, error) {
	job, err := queue.next.Cancel(ctx, scope, jobID, revision)
	if err == nil {
		err = queue.releaseJob(ctx, scope, jobID)
	}
	return job, err
}

// Recover delegates lease repair without claiming new work.
func (queue *QuotaJobQueue) Recover(ctx context.Context, scope ProjectScope, now time.Time, limit int) (int, error) {
	if reporter, ok := queue.next.(JobRecoveryReporter); ok {
		jobs, err := reporter.RecoverWithJobs(ctx, scope, now, limit)
		if err != nil {
			return 0, err
		}
		for _, job := range jobs {
			if releaseErr := queue.quota.ReleaseClaim(ctx, scope, job.JobID, false); releaseErr != nil {
				return len(jobs), releaseErr
			}
		}
		return len(jobs), nil
	}
	count, err := queue.next.Recover(ctx, scope, now, limit)
	if err != nil || count == 0 {
		return count, err
	}
	queue.mu.Lock()
	keys := make([]string, 0, len(queue.jobs))
	prefix := scope.OrganizationID + "/" + scope.ProjectID + "/"
	for key := range queue.jobs {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) > count {
		keys = keys[:count]
	}
	queue.mu.Unlock()
	for _, key := range keys {
		if releaseErr := queue.releaseJobKey(ctx, key); releaseErr != nil {
			return count, releaseErr
		}
	}
	return count, nil
}

// releaseJob resolves one claimed job and releases its batch reservation exactly once after the final sibling terminates.
func (queue *QuotaJobQueue) releaseJob(ctx context.Context, scope ProjectScope, jobID string) error {
	key := quotaJobKey(scope, jobID)
	queue.mu.Lock()
	delete(queue.jobs, key)
	queue.mu.Unlock()
	return queue.quota.ReleaseClaim(ctx, scope, jobID, false)
}

// releaseJobKey removes one lifecycle association and invokes quota only when the entire claimed batch is terminal.
func (queue *QuotaJobQueue) releaseJobKey(ctx context.Context, key string) error {
	queue.mu.Lock()
	_, ok := queue.jobs[key]
	if !ok {
		queue.mu.Unlock()
		return nil
	}
	delete(queue.jobs, key)
	queue.mu.Unlock()
	parts := strings.Split(key, "/")
	if len(parts) != 3 {
		return ErrIntegrity
	}
	return queue.quota.ReleaseClaim(ctx, ProjectScope{OrganizationID: parts[0], ProjectID: parts[1]}, parts[2], false)
}

// quotaJobKey isolates one lifecycle association by the same organization/project/job tuple used by the queue.
func quotaJobKey(scope ProjectScope, jobID string) string {
	return scope.OrganizationID + "/" + scope.ProjectID + "/" + jobID
}

var _ JobQueue = (*QuotaJobQueue)(nil)
