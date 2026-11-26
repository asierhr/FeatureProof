package controlplane

import (
	"context"
	"errors"
	"testing"
	"time"
)

// denyingClaimQuota proves a rejected reservation prevents the underlying queue lookup.
type denyingClaimQuota struct{ err error }

// ReserveClaim returns the configured admission denial.
func (quota denyingClaimQuota) ReserveClaim(context.Context, ClaimRequest) (ClaimQuotaReceipt, error) {
	return ClaimQuotaReceipt{}, quota.err
}

// ConfirmClaim is unreachable after admission denial.
func (denyingClaimQuota) ConfirmClaim(context.Context, ClaimQuotaReceipt, []Job) error { return nil }

// RejectClaim is unreachable when no reservation was created.
func (denyingClaimQuota) RejectClaim(context.Context, ClaimQuotaReceipt, bool) error { return nil }

// ReleaseClaim is unreachable when no reservation was created.
func (denyingClaimQuota) ReleaseClaim(context.Context, ProjectScope, string, bool) error { return nil }

// countingJobQueue records whether quota denial leaked into the real queue consumer.
type countingJobQueue struct {
	JobQueue
	claims     int
	jobs       []Job
	claimErr   error
	terminal   bool
	recovered  []Job
	recoverErr error
}

// RecoverWithJobs returns exact durable recovery identities independently of
// the wrapper instance's transient claim cache.
func (queue *countingJobQueue) RecoverWithJobs(context.Context, ProjectScope, time.Time, int) ([]Job, error) {
	return append([]Job(nil), queue.recovered...), queue.recoverErr
}

// Claim records an underlying queue invocation.
func (queue *countingJobQueue) Claim(context.Context, ClaimRequest) ([]Job, error) {
	queue.claims++
	return queue.jobs, queue.claimErr
}

// Complete returns a scripted terminal proof without requiring a fully seeded queue fixture.
func (queue *countingJobQueue) Complete(context.Context, ProjectScope, string, string, uint64, uint64, time.Time, JobState, string) (Job, error) {
	queue.terminal = true
	return queue.jobs[0], nil
}

// Retry returns a scripted terminal reschedule so the wrapper must release the claim.
func (queue *countingJobQueue) Retry(context.Context, ProjectScope, string, string, uint64, uint64, time.Time, RetryDecision, time.Time) (Job, error) {
	queue.terminal = true
	return queue.jobs[0], nil
}

// Cancel returns a scripted terminal cancellation so the wrapper must release the claim.
func (queue *countingJobQueue) Cancel(context.Context, ProjectScope, string, uint64) (Job, error) {
	queue.terminal = true
	return queue.jobs[0], nil
}

// legacyRecoveryQueue models adapters that predate RecoverWithJobs while the
// compatibility path still reconciles only jobs claimed by this wrapper.
type legacyRecoveryQueue struct {
	JobQueue
	jobs       []Job
	recoverN   int
	recoverErr error
}

// Claim returns the scripted durable jobs.
func (queue *legacyRecoveryQueue) Claim(context.Context, ClaimRequest) ([]Job, error) {
	return append([]Job(nil), queue.jobs...), nil
}

// Recover returns only the legacy aggregate count.
func (queue *legacyRecoveryQueue) Recover(context.Context, ProjectScope, time.Time, int) (int, error) {
	return queue.recoverN, queue.recoverErr
}

// recordingClaimQuota captures causal resolution after the queue result.
type recordingClaimQuota struct {
	reserveErr, confirmErr, rejectErr, releaseErr error
	confirmed, rejected, released                 bool
	uncertain                                     bool
	claimed                                       int
}

// ReserveClaim returns one stable opaque receipt or the scripted denial.
func (quota *recordingClaimQuota) ReserveClaim(context.Context, ClaimRequest) (ClaimQuotaReceipt, error) {
	return ClaimQuotaReceipt{ID: "receipt", Digest: "digest"}, quota.reserveErr
}

// ConfirmClaim records the observed batch size.
func (quota *recordingClaimQuota) ConfirmClaim(_ context.Context, _ ClaimQuotaReceipt, jobs []Job) error {
	quota.confirmed, quota.claimed = true, len(jobs)
	return quota.confirmErr
}

// RejectClaim records conservative uncertainty after a queue error.
func (quota *recordingClaimQuota) RejectClaim(_ context.Context, _ ClaimQuotaReceipt, uncertain bool) error {
	quota.rejected, quota.uncertain = true, uncertain
	return quota.rejectErr
}

// ReleaseClaim records terminal capacity reconciliation.
func (quota *recordingClaimQuota) ReleaseClaim(_ context.Context, _ ProjectScope, _ string, uncertain bool) error {
	quota.released, quota.uncertain = true, uncertain
	return quota.releaseErr
}

// TestQuotaJobQueueRequiresGuardAndDeniesBeforeClaim covers mandatory composition and call order.
func TestQuotaJobQueueRequiresGuardAndDeniesBeforeClaim(t *testing.T) {
	if _, err := NewQuotaJobQueue(NewMemoryStore(), nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing quota=%v", err)
	}
	denial := errors.New("quota denied")
	next := &countingJobQueue{JobQueue: NewMemoryStore()}
	queue, _ := NewQuotaJobQueue(next, denyingClaimQuota{err: denial})
	if _, err := queue.Claim(context.Background(), ClaimRequest{}); !errors.Is(err, denial) || next.claims != 0 {
		t.Fatalf("claim=%v calls=%d", err, next.claims)
	}
}

// TestQuotaJobQueueResolvesClaimsAndDelegatesLifecycle covers successful,
// uncertain and reconciliation-failure outcomes plus non-claim forwarding.
func TestQuotaJobQueueResolvesClaimsAndDelegatesLifecycle(t *testing.T) {
	base := NewMemoryStore()
	scope := ProjectScope{OrganizationID: "org", ProjectID: "project"}
	job := Job{JobID: "job", OrganizationID: scope.OrganizationID, ProjectID: scope.ProjectID}
	quota := &recordingClaimQuota{}
	next := &countingJobQueue{JobQueue: base, jobs: []Job{job}}
	queue, err := NewQuotaJobQueue(next, quota)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := queue.Claim(context.Background(), ClaimRequest{})
	if err != nil || len(jobs) != 1 || !quota.confirmed || quota.claimed != 1 {
		t.Fatalf("jobs=%+v quota=%+v err=%v", jobs, quota, err)
	}
	if _, err = queue.Complete(context.Background(), scope, job.JobID, "worker", 1, 1, time.Unix(1, 0), JobSucceeded, "done"); err != nil || !quota.released || quota.uncertain || !next.terminal {
		t.Fatalf("terminal quota=%+v queue=%+v err=%v", quota, next, err)
	}

	claimFailure := errors.New("queue unavailable")
	quota = &recordingClaimQuota{}
	next.claimErr = claimFailure
	queue, _ = NewQuotaJobQueue(next, quota)
	if _, err = queue.Claim(context.Background(), ClaimRequest{}); !errors.Is(err, claimFailure) || !quota.rejected || !quota.uncertain {
		t.Fatalf("claim failure quota=%+v err=%v", quota, err)
	}
	reconcileFailure := errors.New("quota reconciliation unavailable")
	quota = &recordingClaimQuota{rejectErr: reconcileFailure}
	queue, _ = NewQuotaJobQueue(next, quota)
	if _, err = queue.Claim(context.Background(), ClaimRequest{}); !errors.Is(err, reconcileFailure) {
		t.Fatalf("reconciliation precedence=%v", err)
	}
	quota = &recordingClaimQuota{confirmErr: reconcileFailure}
	next.claimErr, next.jobs = nil, nil
	queue, _ = NewQuotaJobQueue(next, quota)
	if _, err = queue.Claim(context.Background(), ClaimRequest{}); !errors.Is(err, reconcileFailure) {
		t.Fatalf("confirm failure=%v", err)
	}

	now := time.Unix(1, 0).UTC()
	next.jobs = []Job{job}
	_, _ = queue.Enqueue(context.Background(), job)
	_, _ = queue.Heartbeat(context.Background(), scope, job.JobID, "worker", 1, 1, now, time.Minute)
	_, _ = queue.Complete(context.Background(), scope, job.JobID, "worker", 1, 1, now, JobSucceeded, "done")
	_, _ = queue.Retry(context.Background(), scope, job.JobID, "worker", 1, 1, now, RetryDecision{}, now)
	_, _ = queue.Cancel(context.Background(), scope, job.JobID, 1)
	_, _ = queue.Recover(context.Background(), scope, now, 1)
}

// TestQuotaJobQueueRestartReleasesExactRecoveredJobs proves recovery uses the
// durable queue result rather than the prior wrapper's in-memory claim map.
func TestQuotaJobQueueRestartReleasesExactRecoveredJobs(t *testing.T) {
	scope := ProjectScope{OrganizationID: "org", ProjectID: "project"}
	job := Job{JobID: "job-recovered", OrganizationID: scope.OrganizationID, ProjectID: scope.ProjectID}
	next := &countingJobQueue{JobQueue: NewMemoryStore(), jobs: []Job{job}}
	quota := &recordingClaimQuota{}
	first, _ := NewQuotaJobQueue(next, quota)
	if _, err := first.Claim(context.Background(), ClaimRequest{}); err != nil {
		t.Fatal(err)
	}
	next.recovered = []Job{job}
	restarted, _ := NewQuotaJobQueue(next, quota)
	quota.released = false
	if count, err := restarted.Recover(context.Background(), scope, time.Unix(10, 0), 1); err != nil || count != 1 || !quota.released {
		t.Fatalf("count=%d released=%v err=%v", count, quota.released, err)
	}
}

// TestQuotaJobQueueRecoveryCompatibilityFailsClosed proves the legacy
// aggregate path releases only its own scoped associations and propagates
// recovery and quota failures instead of guessing across tenants.
func TestQuotaJobQueueRecoveryCompatibilityFailsClosed(t *testing.T) {
	scope := ProjectScope{OrganizationID: "org", ProjectID: "project"}
	other := ProjectScope{OrganizationID: "other", ProjectID: "project"}
	jobs := []Job{{JobID: "job-b", OrganizationID: scope.OrganizationID, ProjectID: scope.ProjectID}, {JobID: "job-a", OrganizationID: scope.OrganizationID, ProjectID: scope.ProjectID}, {JobID: "job-other", OrganizationID: other.OrganizationID, ProjectID: other.ProjectID}}
	next := &legacyRecoveryQueue{JobQueue: NewMemoryStore(), jobs: jobs, recoverN: 1}
	quota := &recordingClaimQuota{}
	queue, _ := NewQuotaJobQueue(next, quota)
	if _, err := queue.Claim(context.Background(), ClaimRequest{}); err != nil {
		t.Fatal(err)
	}
	if count, err := queue.Recover(context.Background(), scope, time.Unix(10, 0), 10); err != nil || count != 1 || !quota.released {
		t.Fatalf("compat recovery count=%d quota=%+v err=%v", count, quota, err)
	}
	next.recoverErr = errors.New("recovery unavailable")
	if _, err := queue.Recover(context.Background(), scope, time.Unix(11, 0), 10); !errors.Is(err, next.recoverErr) {
		t.Fatalf("recovery failure=%v", err)
	}
	next.recoverErr, next.recoverN = nil, 0
	if count, err := queue.Recover(context.Background(), scope, time.Unix(12, 0), 10); err != nil || count != 0 {
		t.Fatalf("zero recovery count=%d err=%v", count, err)
	}

	next.recoverN = 2
	quota.releaseErr = errors.New("quota release unavailable")
	if _, err := queue.Recover(context.Background(), scope, time.Unix(13, 0), 10); !errors.Is(err, quota.releaseErr) {
		t.Fatalf("quota failure=%v", err)
	}

	reporter := &countingJobQueue{JobQueue: NewMemoryStore(), recovered: jobs[:1], recoverErr: errors.New("report unavailable")}
	reporterQueue, _ := NewQuotaJobQueue(reporter, &recordingClaimQuota{})
	if _, err := reporterQueue.Recover(context.Background(), scope, time.Unix(14, 0), 10); !errors.Is(err, reporter.recoverErr) {
		t.Fatalf("reporter failure=%v", err)
	}
}
