package replanning

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/asierhr/featureProof/internal/revisionbinding"
	"slices"
	"strings"
	"sync"
	"time"
)

// EventType enumerates safe lineage transitions for a bounded replan cycle.
type EventType string

const (
	EventCycleReserved       EventType = "replan_cycle_reserved"
	EventInitialAssertions   EventType = "replan_initial_assertions_confirmed"
	EventSummaryConfirmed    EventType = "replan_summary_confirmed"
	EventDecisionReceived    EventType = "replan_decision_received"
	EventProposalRejected    EventType = "replan_proposal_rejected"
	EventProposalRepeated    EventType = "replan_proposal_repeated"
	EventProposalPrepared    EventType = "replan_proposal_prepared"
	EventApprovalRequested   EventType = "replan_approval_requested"
	EventApprovalResolved    EventType = "replan_approval_resolved"
	EventBatchStarted        EventType = "replan_batch_execution_started"
	EventBatchCompleted      EventType = "replan_batch_execution_completed"
	EventAssertionsConfirmed EventType = "replan_assertions_confirmed"
	EventTerminated          EventType = "replan_terminated"
	EventCancelled           EventType = "replan_cancelled"
	EventFailed              EventType = "replan_failed"
)

// Event records only allowlisted identities, digests, categories and counts.
type Event struct {
	Version             int                          `json:"version"`
	BindingSHA256       string                       `json:"binding_sha256,omitempty"`
	PlanBinding         *revisionbinding.PlanBinding `json:"plan_binding,omitempty"`
	Sequence            uint64                       `json:"sequence"`
	RunID               string                       `json:"run_id"`
	Cycle               int                          `json:"cycle"`
	Type                EventType                    `json:"type"`
	DecisionID          string                       `json:"decision_id,omitempty"`
	ParentDecisionID    string                       `json:"parent_decision_id,omitempty"`
	SummaryDigest       string                       `json:"summary_digest,omitempty"`
	ObservedEvidenceIDs []string                     `json:"observed_evidence_ids,omitempty"`
	ProposalFingerprint string                       `json:"proposal_fingerprint,omitempty"`
	BatchID             string                       `json:"batch_id,omitempty"`
	PlanID              string                       `json:"plan_id,omitempty"`
	ApprovalRevision    uint64                       `json:"approval_revision,omitempty"`
	ToolCallIDs         []string                     `json:"tool_call_ids,omitempty"`
	NewEvidenceIDs      []string                     `json:"new_evidence_ids,omitempty"`
	AssertionIDs        []string                     `json:"assertion_ids,omitempty"`
	Assertions          []ConfirmedAssertion         `json:"assertions,omitempty"`
	Required            []DeclarationIdentity        `json:"required_declarations,omitempty"`
	Category            Category                     `json:"category,omitempty"`
	Verdict             Verdict                      `json:"verdict,omitempty"`
	BudgetDimension     string                       `json:"budget_dimension,omitempty"`
	BudgetUsed          int64                        `json:"budget_used,omitempty"`
	BudgetLimit         int64                        `json:"budget_limit,omitempty"`
}

// ReplanJournal stores versioned events with per-run CAS and idempotency keys.
type ReplanJournal interface {
	Events(context.Context, string) ([]Event, error)
	CompareAndAppend(context.Context, string, uint64, string, Event) (Event, bool, error)
	Checkpoint(context.Context, string) (CycleCheckpoint, error)
	SaveCheckpoint(context.Context, string, CycleCheckpoint) error
	ClaimRun(context.Context, string, string, time.Time, time.Duration) (RunLease, bool, error)
	RenewRun(context.Context, string, RunLease, time.Time, time.Duration) (RunLease, error)
	ReleaseRun(context.Context, string, RunLease) error
	OwnedEvents(context.Context, string, RunLease, time.Time) ([]Event, error)
	OwnedCheckpoint(context.Context, string, RunLease, time.Time) (CycleCheckpoint, error)
	OwnedSaveCheckpoint(context.Context, string, RunLease, time.Time, CycleCheckpoint) error
	OwnedCompareAndAppend(context.Context, string, RunLease, time.Time, uint64, string, Event) (Event, bool, error)
	ReserveInference(context.Context, string, RunLease, time.Time, uint64, CycleCheckpoint) error
}

// RunLease is the durable generation-fenced authority required before restoring budgets or invoking a model.
type RunLease struct {
	Owner      string    `json:"owner"`
	Generation uint64    `json:"generation"`
	LeaseUntil time.Time `json:"lease_until"`
}

// CycleCheckpoint keeps bounded safe model input, a decoded decision and confirmed results outside audit events.
type CycleCheckpoint struct {
	Cycle               int
	Summary             []byte
	SummaryDigest       string
	InferenceStarted    bool
	Decision            *Decision
	ProposalFingerprint string
	Additional          []ConfirmedCheck
	Confirmed           []ConfirmedCheck
	AssertionsPrepared  bool
	Consumption         BudgetSnapshot
}

// MemoryJournal is a deterministic per-run CAS store for local composition and tests.
type MemoryJournal struct {
	mu          sync.Mutex
	runs        map[string]*journalRun
	checkpoints map[string][]byte
	leases      map[string]RunLease
	generations map[string]uint64
}

// journalRun owns one confirmed prefix and command fingerprint index.
type journalRun struct {
	events []Event
	keys   map[string][]byte
}

// NewMemoryJournal starts isolated run logs without background work.
func NewMemoryJournal() *MemoryJournal {
	return &MemoryJournal{runs: map[string]*journalRun{}, checkpoints: map[string][]byte{}, leases: map[string]RunLease{}, generations: map[string]uint64{}}
}

// ClaimRun atomically grants one live owner a generation-fenced run lease.
func (j *MemoryJournal) ClaimRun(ctx context.Context, runID, owner string, now time.Time, ttl time.Duration) (RunLease, bool, error) {
	if ctx.Err() != nil || !safeEventID(runID) || !safeEventID(owner) || now.IsZero() || ttl <= 0 {
		return RunLease{}, false, failure(CategoryInvalidInput, "lease", "claim")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	current := j.leases[runID]
	if current.Owner != "" && current.LeaseUntil.After(now) {
		return current, current.Owner == owner, nil
	}
	j.generations[runID]++
	lease := RunLease{Owner: owner, Generation: j.generations[runID], LeaseUntil: now.Add(ttl)}
	j.leases[runID] = lease
	return lease, true, nil
}

// RenewRun extends only the exact unexpired generation currently stored for a run.
func (j *MemoryJournal) RenewRun(ctx context.Context, runID string, lease RunLease, now time.Time, ttl time.Duration) (RunLease, error) {
	if ctx.Err() != nil || ttl <= 0 {
		return RunLease{}, failure(CategoryCancelled, "lease", "renew")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	current := j.leases[runID]
	if current.Owner != lease.Owner || current.Generation != lease.Generation || !current.LeaseUntil.After(now) {
		return RunLease{}, failure(CategoryIntegrity, "lease", "lost")
	}
	current.LeaseUntil = now.Add(ttl)
	j.leases[runID] = current
	return current, nil
}

// ReleaseRun clears only the exact generation owned by the caller.
func (j *MemoryJournal) ReleaseRun(ctx context.Context, runID string, lease RunLease) error {
	if ctx.Err() != nil {
		return failure(CategoryCancelled, "lease", "release")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	current := j.leases[runID]
	if current.Owner == "" {
		return nil
	}
	if current.Owner != lease.Owner || current.Generation != lease.Generation {
		return failure(CategoryIntegrity, "lease", "lost")
	}
	delete(j.leases, runID)
	return nil
}

// validateLeaseLocked fences an operation against the exact live owner generation while the store lock is held.
func (j *MemoryJournal) validateLeaseLocked(runID string, lease RunLease, now time.Time) error {
	current := j.leases[runID]
	if now.IsZero() || current.Owner != lease.Owner || current.Generation != lease.Generation || !current.LeaseUntil.After(now) {
		return failure(CategoryIntegrity, "lease", "fenced")
	}
	return nil
}

// OwnedEvents reads a prefix only while the exact durable generation remains live.
func (j *MemoryJournal) OwnedEvents(ctx context.Context, runID string, lease RunLease, now time.Time) ([]Event, error) {
	if ctx.Err() != nil {
		return nil, failure(CategoryCancelled, "journal", "context")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.validateLeaseLocked(runID, lease, now); err != nil {
		return nil, err
	}
	run := j.runs[runID]
	if run == nil {
		return nil, nil
	}
	out := make([]Event, len(run.events))
	for i, event := range run.events {
		out[i] = cloneEvent(event)
	}
	return out, nil
}

// OwnedCheckpoint restores private state only for the current live generation.
func (j *MemoryJournal) OwnedCheckpoint(ctx context.Context, runID string, lease RunLease, now time.Time) (CycleCheckpoint, error) {
	if ctx.Err() != nil {
		return CycleCheckpoint{}, failure(CategoryCancelled, "checkpoint", "context")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.validateLeaseLocked(runID, lease, now); err != nil {
		return CycleCheckpoint{}, err
	}
	return decodeCheckpoint(j.checkpoints[runID])
}

// OwnedSaveCheckpoint validates the lease and checkpoint transition in the same critical section.
func (j *MemoryJournal) OwnedSaveCheckpoint(ctx context.Context, runID string, lease RunLease, now time.Time, value CycleCheckpoint) error {
	if ctx.Err() != nil {
		return failure(CategoryCancelled, "checkpoint", "context")
	}
	raw, err := encodeCheckpoint(runID, value)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.validateLeaseLocked(runID, lease, now); err != nil {
		return err
	}
	if err := validateCheckpointRaw(j.checkpoints[runID], value); err != nil {
		return err
	}
	j.checkpoints[runID] = append([]byte(nil), raw...)
	return nil
}

// OwnedCompareAndAppend atomically fences and appends one event.
func (j *MemoryJournal) OwnedCompareAndAppend(ctx context.Context, runID string, lease RunLease, now time.Time, expected uint64, key string, event Event) (Event, bool, error) {
	if ctx.Err() != nil {
		return Event{}, false, failure(CategoryCancelled, "journal", "context")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.validateLeaseLocked(runID, lease, now); err != nil {
		return Event{}, false, err
	}
	return j.compareAndAppendLocked(runID, expected, key, event)
}

// ReserveInference is the synchronous fencing point immediately before the external model effect.
func (j *MemoryJournal) ReserveInference(ctx context.Context, runID string, lease RunLease, now time.Time, expected uint64, checkpoint CycleCheckpoint) error {
	if ctx.Err() != nil {
		return failure(CategoryCancelled, "inference", "context")
	}
	checkpoint.InferenceStarted = true
	raw, err := encodeCheckpoint(runID, checkpoint)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.validateLeaseLocked(runID, lease, now); err != nil {
		return err
	}
	run := j.runs[runID]
	if run == nil && expected == 0 {
		run = &journalRun{keys: map[string][]byte{}}
		j.runs[runID] = run
	}
	if run == nil || uint64(len(run.events)) != expected {
		return failure(CategoryIntegrity, "inference", "revision")
	}
	if err := validateCheckpointRaw(j.checkpoints[runID], checkpoint); err != nil {
		return err
	}
	j.checkpoints[runID] = append([]byte(nil), raw...)
	return nil
}

func decodeCheckpoint(raw []byte) (CycleCheckpoint, error) {
	if len(raw) == 0 {
		return CycleCheckpoint{}, nil
	}
	var value CycleCheckpoint
	if json.Unmarshal(raw, &value) != nil {
		return CycleCheckpoint{}, failure(CategoryIntegrity, "checkpoint", "decode")
	}
	return value, nil
}

func encodeCheckpoint(runID string, value CycleCheckpoint) ([]byte, error) {
	if runID == "" || value.Cycle < 1 {
		return nil, failure(CategoryInvalidInput, "checkpoint", "identity")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, failure(CategoryInvalidInput, "checkpoint", "value")
	}
	return raw, nil
}

func validateCheckpointRaw(previousRaw []byte, value CycleCheckpoint) error {
	if len(previousRaw) == 0 {
		return nil
	}
	var previous CycleCheckpoint
	if json.Unmarshal(previousRaw, &previous) != nil || !validCheckpointAdvance(previous, value) {
		return failure(CategoryIntegrity, "checkpoint", "revision")
	}
	return nil
}

// Checkpoint returns an isolated copy of the latest confirmed run-cycle recovery data.
func (j *MemoryJournal) Checkpoint(ctx context.Context, runID string) (CycleCheckpoint, error) {
	if ctx.Err() != nil {
		return CycleCheckpoint{}, failure(CategoryCancelled, "checkpoint", "context")
	}
	j.mu.Lock()
	raw := append([]byte(nil), j.checkpoints[runID]...)
	j.mu.Unlock()
	return decodeCheckpoint(raw)
}

// SaveCheckpoint stores a defensive serialized copy before the corresponding audit event is appended.
func (j *MemoryJournal) SaveCheckpoint(ctx context.Context, runID string, value CycleCheckpoint) error {
	if ctx.Err() != nil {
		return failure(CategoryCancelled, "checkpoint", "context")
	}
	if runID == "" || value.Cycle < 1 {
		return failure(CategoryInvalidInput, "checkpoint", "identity")
	}
	raw, err := encodeCheckpoint(runID, value)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := validateCheckpointRaw(j.checkpoints[runID], value); err != nil {
		return err
	}
	j.checkpoints[runID] = append([]byte(nil), raw...)
	return nil
}

// validCheckpointAdvance prevents a stale worker from erasing confirmed recovery state under the same cycle.
func validCheckpointAdvance(previous, next CycleCheckpoint) bool {
	if next.Cycle < previous.Cycle || next.Consumption.AgentSteps < previous.Consumption.AgentSteps || next.Consumption.ToolCalls < previous.Consumption.ToolCalls || next.Consumption.ValidationWork < previous.Consumption.ValidationWork || next.Consumption.Replans < previous.Consumption.Replans || next.Consumption.ModelCalls < previous.Consumption.ModelCalls || next.Consumption.ModelTokens < previous.Consumption.ModelTokens || next.Consumption.Duration < previous.Consumption.Duration {
		return false
	}
	if next.Cycle != previous.Cycle {
		return true
	}
	if len(previous.Summary) > 0 && !bytes.Equal(previous.Summary, next.Summary) || previous.SummaryDigest != "" && previous.SummaryDigest != next.SummaryDigest || previous.InferenceStarted && !next.InferenceStarted || previous.Decision != nil && !sameCheckpointJSON(previous.Decision, next.Decision) || previous.ProposalFingerprint != "" && previous.ProposalFingerprint != next.ProposalFingerprint || len(previous.Additional) > 0 && !sameCheckpointJSON(previous.Additional, next.Additional) || previous.AssertionsPrepared && !next.AssertionsPrepared {
		return false
	}
	if len(next.Confirmed) < len(previous.Confirmed) || len(previous.Confirmed) > 0 && !sameCheckpointJSON(previous.Confirmed, next.Confirmed[:len(previous.Confirmed)]) {
		return false
	}
	return true
}

// sameCheckpointJSON compares typed recovery values by their stable JSON representation.
func sameCheckpointJSON(left, right any) bool {
	a, errA := json.Marshal(left)
	b, errB := json.Marshal(right)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

// Events returns a defensive snapshot of the confirmed prefix.
func (j *MemoryJournal) Events(ctx context.Context, runID string) ([]Event, error) {
	if ctx.Err() != nil {
		return nil, failure(CategoryCancelled, "journal", "context")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	run := j.runs[runID]
	if run == nil {
		return nil, nil
	}
	out := make([]Event, len(run.events))
	for i, e := range run.events {
		out[i] = cloneEvent(e)
	}
	return out, nil
}

// CompareAndAppend atomically confirms one versioned event or returns its identical prior result.
func (j *MemoryJournal) CompareAndAppend(ctx context.Context, runID string, expected uint64, key string, event Event) (Event, bool, error) {
	if ctx.Err() != nil {
		return Event{}, false, failure(CategoryCancelled, "journal", "context")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.compareAndAppendLocked(runID, expected, key, event)
}

func (j *MemoryJournal) compareAndAppendLocked(runID string, expected uint64, key string, event Event) (Event, bool, error) {
	if runID == "" || key == "" || event.RunID != runID || event.Version != 1 || event.Cycle < 0 {
		return Event{}, false, failure(CategoryInvalidInput, "journal", "event")
	}
	copy := cloneEvent(event)
	copy.Sequence = expected + 1
	raw, err := json.Marshal(copy)
	if err != nil {
		return Event{}, false, failure(CategoryInvalidInput, "journal", "event")
	}
	run := j.runs[runID]
	if run == nil {
		run = &journalRun{keys: map[string][]byte{}}
		j.runs[runID] = run
	}
	if old, ok := run.keys[key]; ok {
		if string(old) != string(raw) {
			return Event{}, false, failure(CategoryIntegrity, "journal", "idempotency")
		}
		return cloneEvent(copy), true, nil
	}
	if uint64(len(run.events)) != expected {
		return Event{}, false, failure(CategoryIntegrity, "journal", "revision")
	}
	run.events = append(run.events, copy)
	run.keys[key] = raw
	return cloneEvent(copy), false, nil
}

// cloneEvent isolates all variable-length event fields from caller mutations.
func cloneEvent(e Event) Event {
	if e.PlanBinding != nil {
		copy := *e.PlanBinding
		e.PlanBinding = &copy
	}
	e.ObservedEvidenceIDs = append([]string(nil), e.ObservedEvidenceIDs...)
	e.ToolCallIDs = append([]string(nil), e.ToolCallIDs...)
	e.NewEvidenceIDs = append([]string(nil), e.NewEvidenceIDs...)
	e.AssertionIDs = append([]string(nil), e.AssertionIDs...)
	facts := e.Assertions
	e.Assertions = make([]ConfirmedAssertion, len(facts))
	for i, fact := range facts {
		e.Assertions[i] = fact
		e.Assertions[i].EvidenceIDs = append([]string(nil), fact.EvidenceIDs...)
	}
	e.Required = append([]DeclarationIdentity(nil), e.Required...)
	return e
}

// Projection reconstructs the latest safe lineage and terminal disposition.
type Projection struct {
	Sequence                                    uint64
	Cycle                                       int
	LastType                                    EventType
	DecisionID, ParentDecisionID, SummaryDigest string
	Terminal                                    bool
	Verdict                                     Verdict
	Fingerprints                                map[string]bool
	BatchID, PlanID                             string
	ProposalFingerprint                         string
	ApprovalRevision                            uint64
	LastCategory                                Category
	SummaryEvidenceIDs                          []string
	BatchEvidenceIDs                            []string
	TerminalCategory                            Category
	TerminalEvidenceIDs                         []string
	Assertions                                  []ConfirmedAssertion
	Required                                    []DeclarationIdentity
	PlanBindings                                []revisionbinding.PlanBinding
}

// Project fails closed on missing, reordered, forked, or contradictory lineage events.
func Project(ctx context.Context, events []Event, maximum int) (Projection, error) {
	if maximum <= 0 || len(events) > maximum {
		return Projection{}, failure(CategoryBudget, "project", "events")
	}
	p := Projection{Fingerprints: map[string]bool{}}
	runID := ""
	baseDigest := ""
	assertionFacts := map[AssertionIdentity][]byte{}
	for i, e := range events {
		if ctx.Err() != nil {
			return Projection{}, failure(CategoryCancelled, "project", "context")
		}
		if e.Version != 1 || e.Sequence != uint64(i+1) || e.RunID == "" || (runID != "" && runID != e.RunID) || e.Cycle < 0 || p.Terminal {
			return Projection{}, failure(CategoryIntegrity, "project", "sequence")
		}
		if e.BindingSHA256 != "" {
			if !validDigest(e.BindingSHA256) || (baseDigest != "" && e.BindingSHA256 != baseDigest) || (baseDigest == "" && i > 0) {
				return Projection{}, failure(CategoryIntegrity, "project", "binding")
			}
			baseDigest = e.BindingSHA256
		} else if baseDigest != "" {
			return Projection{}, failure(CategoryIntegrity, "project", "binding")
		}
		if (e.Type == EventInitialAssertions && len(p.PlanBindings) == 0 || e.Type == EventBatchStarted) && baseDigest != "" {
			if e.PlanBinding == nil || e.PlanBinding.Version != 1 || e.PlanBinding.BindingSHA256 != baseDigest || e.PlanBinding.Batch != len(p.PlanBindings)+1 || !validDigest(e.PlanBinding.PlanSHA256) || (e.PlanBinding.Mode != "approved" && e.PlanBinding.Mode != "approval_not_required") || (e.Type == EventBatchStarted && (e.PlanBinding.Mode == "approved") != (p.ApprovalRevision > 0)) {
				return Projection{}, failure(CategoryIntegrity, "project", "plan_binding")
			}
			p.PlanBindings = append(p.PlanBindings, *e.PlanBinding)
		} else if e.PlanBinding != nil {
			return Projection{}, failure(CategoryIntegrity, "project", "plan_binding")
		}
		if !validEventShape(e) {
			return Projection{}, failure(CategoryIntegrity, "project", "schema")
		}
		runID = e.RunID
		if !allowedTransition(p.LastType, e.Type) {
			return Projection{}, failure(CategoryIntegrity, "project", "transition")
		}
		if e.Type == EventCycleReserved {
			if e.Cycle != p.Cycle+1 || e.ParentDecisionID != p.DecisionID {
				return Projection{}, failure(CategoryIntegrity, "project", "cycle")
			}
			p.Cycle = e.Cycle
			p.ParentDecisionID = e.ParentDecisionID
			p.DecisionID = ""
			p.SummaryDigest = ""
			p.BatchID, p.PlanID, p.ApprovalRevision = "", "", 0
			p.ProposalFingerprint = ""
			p.SummaryEvidenceIDs = nil
			p.BatchEvidenceIDs = nil
		} else if e.Cycle != p.Cycle {
			return Projection{}, failure(CategoryIntegrity, "project", "cycle")
		}
		if e.Type == EventSummaryConfirmed {
			if e.SummaryDigest == "" {
				return Projection{}, failure(CategoryIntegrity, "project", "summary")
			}
			p.SummaryDigest = e.SummaryDigest
			p.SummaryEvidenceIDs = append([]string(nil), e.ObservedEvidenceIDs...)
		}
		if e.Type == EventBatchCompleted {
			p.BatchEvidenceIDs = append([]string(nil), e.NewEvidenceIDs...)
		}
		if e.Type == EventInitialAssertions || e.Type == EventAssertionsConfirmed {
			p.Required = append(p.Required, e.Required...)
			for _, fact := range e.Assertions {
				if e.Type == EventAssertionsConfirmed {
					for _, id := range fact.EvidenceIDs {
						if !slices.Contains(p.BatchEvidenceIDs, id) {
							return Projection{}, failure(CategoryIntegrity, "project", "assertion_evidence")
						}
					}
				}
				raw, _ := json.Marshal(fact)
				if prior, exists := assertionFacts[fact.Identity]; exists && string(prior) != string(raw) {
					return Projection{}, failure(CategoryIntegrity, "project", "assertion_replay")
				}
				assertionFacts[fact.Identity] = raw
			}
			p.Assertions = append(p.Assertions, cloneEvent(e).Assertions...)
		}
		if e.Type == EventDecisionReceived {
			if e.DecisionID == "" || e.SummaryDigest != p.SummaryDigest || e.ParentDecisionID != p.ParentDecisionID || !slices.Equal(e.ObservedEvidenceIDs, p.SummaryEvidenceIDs) {
				return Projection{}, failure(CategoryIntegrity, "project", "decision")
			}
			p.DecisionID = e.DecisionID
		}
		if e.Type == EventProposalPrepared || e.Type == EventProposalRepeated || e.Type == EventProposalRejected {
			if e.DecisionID != p.DecisionID || e.ProposalFingerprint == "" {
				return Projection{}, failure(CategoryIntegrity, "project", "proposal")
			}
			if e.Type == EventProposalRepeated && !p.Fingerprints[e.ProposalFingerprint] {
				return Projection{}, failure(CategoryIntegrity, "project", "unknown_repeat")
			}
			if e.Type == EventProposalPrepared || e.Type == EventProposalRejected {
				if p.Fingerprints[e.ProposalFingerprint] {
					return Projection{}, failure(CategoryIntegrity, "project", "duplicate")
				}
				p.Fingerprints[e.ProposalFingerprint] = true
			}
			if e.Type == EventProposalPrepared {
				p.BatchID, p.PlanID = e.BatchID, e.PlanID
				p.ProposalFingerprint = e.ProposalFingerprint
			}
		}
		if e.Type == EventApprovalRequested || e.Type == EventApprovalResolved || e.Type == EventBatchStarted || e.Type == EventBatchCompleted || e.Type == EventAssertionsConfirmed {
			if e.DecisionID != p.DecisionID {
				return Projection{}, failure(CategoryIntegrity, "project", "decision_lineage")
			}
			if e.BatchID != "" && e.BatchID != p.BatchID {
				return Projection{}, failure(CategoryIntegrity, "project", "batch_lineage")
			}
			if e.PlanID != "" && e.PlanID != p.PlanID {
				return Projection{}, failure(CategoryIntegrity, "project", "plan_lineage")
			}
			if e.Type == EventApprovalRequested {
				p.ApprovalRevision = e.ApprovalRevision
			}
			if e.Type == EventApprovalResolved && e.ApprovalRevision != p.ApprovalRevision {
				return Projection{}, failure(CategoryIntegrity, "project", "approval_lineage")
			}
		}
		p.Sequence = e.Sequence
		p.LastType = e.Type
		p.LastCategory = e.Category
		if e.Type == EventTerminated || e.Type == EventCancelled || e.Type == EventFailed {
			p.Terminal = true
			p.Verdict = e.Verdict
			p.TerminalCategory = e.Category
			p.TerminalEvidenceIDs = append([]string(nil), e.NewEvidenceIDs...)
		}
	}
	if p.Terminal && p.LastType != EventFailed {
		ledger, err := NewAssertionLedger(maximum)
		if err != nil {
			return Projection{}, err
		}
		for _, fact := range p.Assertions {
			if err := ledger.Confirm(ctx, fact); err != nil {
				return Projection{}, err
			}
		}
		if ledger.VerdictRequired(p.Required) != p.Verdict {
			return Projection{}, failure(CategoryIntegrity, "project", "terminal_verdict")
		}
	}
	return p, nil
}

// validEventShape rejects payload-like identifiers and contradictory event fields before projection.
func validEventShape(e Event) bool {
	if e.Category == CategoryBudget {
		if !validBudgetDimension(e.BudgetDimension) || e.BudgetUsed < 0 || e.BudgetLimit < 0 || e.Type != EventTerminated {
			return false
		}
	} else if e.BudgetDimension != "" || e.BudgetUsed != 0 || e.BudgetLimit != 0 {
		return false
	}
	if !safeEventID(e.RunID) || (e.DecisionID != "" && !safeEventID(e.DecisionID)) || (e.ParentDecisionID != "" && !safeEventID(e.ParentDecisionID)) || (e.BatchID != "" && !safeEventID(e.BatchID)) || (e.PlanID != "" && !safeEventID(e.PlanID)) {
		return false
	}
	if e.SummaryDigest != "" && !validDigest(e.SummaryDigest) || e.ProposalFingerprint != "" && !validDigest(e.ProposalFingerprint) {
		return false
	}
	for _, group := range [][]string{e.ObservedEvidenceIDs, e.ToolCallIDs, e.NewEvidenceIDs, e.AssertionIDs} {
		for _, id := range group {
			if !safeEventID(id) {
				return false
			}
		}
	}
	if e.Type == EventInitialAssertions || e.Type == EventAssertionsConfirmed {
		if len(e.Required) == 0 || len(e.Assertions) != len(e.AssertionIDs) {
			return false
		}
		for i, fact := range e.Assertions {
			if !validAssertionFact(fact, e.RunID, e.BatchID) || assertionFactID(fact.Identity) != e.AssertionIDs[i] {
				return false
			}
		}
		for _, required := range e.Required {
			if required.RunID != e.RunID || required.BatchID != e.BatchID || !safeEventID(required.CheckID) || !safeEventID(required.StepID) || !safeEventID(required.DeclarationID) {
				return false
			}
		}
	} else if len(e.Assertions) != 0 || len(e.AssertionIDs) != 0 || len(e.Required) != 0 {
		return false
	}
	switch e.Type {
	case EventInitialAssertions:
		return e.Cycle == 0 && e.BatchID != "" && e.DecisionID == "" && e.Verdict == ""
	case EventCycleReserved:
		return e.Cycle > 0 && e.DecisionID == "" && e.SummaryDigest == "" && e.ProposalFingerprint == "" && e.BatchID == "" && e.Verdict == ""
	case EventSummaryConfirmed:
		return e.SummaryDigest != "" && e.DecisionID == "" && e.Verdict == ""
	case EventDecisionReceived:
		return e.DecisionID != "" && e.SummaryDigest != "" && e.Verdict == ""
	case EventProposalPrepared:
		return e.DecisionID != "" && e.ProposalFingerprint != "" && e.BatchID != "" && e.PlanID != "" && e.Verdict == ""
	case EventProposalRejected, EventProposalRepeated:
		return e.DecisionID != "" && e.ProposalFingerprint != "" && e.Verdict == ""
	case EventApprovalRequested, EventApprovalResolved:
		return e.DecisionID != "" && e.BatchID != "" && e.ApprovalRevision > 0 && e.Verdict == ""
	case EventBatchStarted:
		return e.DecisionID != "" && e.BatchID != "" && e.PlanID != "" && e.Verdict == ""
	case EventBatchCompleted:
		return e.DecisionID != "" && e.BatchID != "" && e.Verdict == ""
	case EventAssertionsConfirmed:
		return e.DecisionID != "" && e.Verdict == ""
	case EventTerminated, EventCancelled, EventFailed:
		return e.Verdict == VerdictPassed || e.Verdict == VerdictFailed || e.Verdict == VerdictInconclusive || e.Verdict == VerdictError
	default:
		return false
	}
}

// assertionFactID binds one confirmed assertion identity to an opaque stable SHA-256 journal reference.
func assertionFactID(identity AssertionIdentity) string {
	raw, _ := json.Marshal(identity)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// validAssertionFact rejects forged result identities and untrusted evidence references in audit events.
func validAssertionFact(fact ConfirmedAssertion, runID, batchID string) bool {
	id := fact.Identity
	if id.RunID != runID || id.BatchID != batchID || !safeEventID(id.CheckID) || !safeEventID(id.StepID) || !safeEventID(id.DeclarationID) || id.NodePath == "" || len(id.NodePath) > 256 {
		return false
	}
	if fact.Status != "passed" && fact.Status != "failed" && fact.Status != "inconclusive" {
		return false
	}
	for _, ref := range fact.EvidenceIDs {
		if !safeEventID(ref) {
			return false
		}
	}
	return len(fact.Operator) <= 128 && len(fact.Path) <= 256 && len(fact.SafeCause) <= 128
}

// validBudgetDimension allows only the fixed non-monetary accounting dimensions in terminal audit events.
func validBudgetDimension(value string) bool {
	switch value {
	case "agent_steps", "tool_calls", "validation_work", "model_calls", "replans", "model_tokens", "duration":
		return true
	default:
		return false
	}
}

// validDigest recognizes the lowercase SHA-256 representation used by summary and proposal lineage.
func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}

// safeEventID constrains audit identifiers to an opaque, bounded non-payload alphabet.
func safeEventID(value string) bool {
	if value == "" || len(value) > 128 || strings.Contains(value, "..") {
		return false
	}
	for _, c := range value {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == ':' || c == '.' {
			continue
		}
		return false
	}
	return true
}

// allowedTransition admits only the documented sequential cycle phases.
func allowedTransition(previous, next EventType) bool {
	if next == EventTerminated || next == EventCancelled || next == EventFailed {
		return previous != EventTerminated && previous != EventCancelled && previous != EventFailed
	}
	switch previous {
	case "":
		return next == EventInitialAssertions || next == EventCycleReserved || next == EventTerminated || next == EventCancelled || next == EventFailed
	case EventInitialAssertions, EventAssertionsConfirmed, EventProposalRejected, EventProposalRepeated:
		return next == EventInitialAssertions || next == EventCycleReserved || next == EventTerminated || next == EventCancelled || next == EventFailed
	case EventCycleReserved:
		return next == EventSummaryConfirmed || next == EventCancelled || next == EventFailed
	case EventSummaryConfirmed:
		return next == EventDecisionReceived || next == EventCancelled || next == EventFailed
	case EventDecisionReceived:
		return next == EventProposalPrepared || next == EventProposalRepeated || next == EventProposalRejected || next == EventTerminated || next == EventCancelled || next == EventFailed
	case EventProposalPrepared:
		return next == EventApprovalRequested || next == EventBatchStarted || next == EventCancelled || next == EventFailed
	case EventApprovalRequested:
		return next == EventApprovalResolved || next == EventCancelled || next == EventFailed
	case EventApprovalResolved:
		return next == EventBatchStarted || next == EventCycleReserved || next == EventTerminated || next == EventCancelled || next == EventFailed
	case EventBatchStarted:
		return next == EventBatchCompleted || next == EventCancelled || next == EventFailed
	case EventBatchCompleted:
		return next == EventAssertionsConfirmed || next == EventCancelled || next == EventFailed
	default:
		return false
	}
}
