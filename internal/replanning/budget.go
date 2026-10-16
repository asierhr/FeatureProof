package replanning

import (
	"context"
	"sync"
	"time"

	"github.com/asierhr/featureProof/internal/model"
)

// BudgetDimension identifies a count charged before its corresponding work starts.
type BudgetDimension string

const (
	BudgetAgentStep      BudgetDimension = "agent_steps"
	BudgetToolCall       BudgetDimension = "tool_calls"
	BudgetValidationWork BudgetDimension = "validation_work"
)

// BudgetSnapshot is an isolated accounting view with explicit non-monetary dimensions.
type BudgetSnapshot struct {
	AgentSteps     int           `json:"agent_steps"`
	ToolCalls      int           `json:"tool_calls"`
	ValidationWork int           `json:"validation_work"`
	ModelCalls     int           `json:"model_calls"`
	Replans        int           `json:"replans"`
	ModelTokens    int64         `json:"model_tokens"`
	Duration       time.Duration `json:"duration"`
}

// ModelBudget reserves replan units and exposes the feature-9 session snapshot.
type ModelBudget interface {
	ReserveReplan(context.Context) error
	Snapshot() model.Snapshot
}

// BudgetController owns atomic deterministic reservations alongside a model session.
type BudgetController struct {
	mu        sync.Mutex
	limits    Limits
	model     ModelBudget
	used      BudgetSnapshot
	prior     BudgetSnapshot
	base      model.Snapshot
	restored  bool
	restoring bool
}

// NewBudgetController validates positive structural limits and a model session dependency.
func NewBudgetController(limits Limits, session ModelBudget) (*BudgetController, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	if session == nil {
		return nil, failure(CategoryInvalidInput, "compose_budget", "model")
	}
	return &BudgetController{limits: limits, model: session}, nil
}

// Reserve charges a count/work unit under one short critical section before any callback.
func (b *BudgetController) Reserve(ctx context.Context, dimension BudgetDimension, units int) error {
	if ctx.Err() != nil {
		return failure(CategoryCancelled, "reserve", "context")
	}
	if units <= 0 {
		return failure(CategoryInvalidInput, "reserve", "units")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.restoring {
		return failure(CategoryIntegrity, "reserve", "restore_in_progress")
	}
	var used *int
	var limit int
	switch dimension {
	case BudgetAgentStep:
		used, limit = &b.used.AgentSteps, b.limits.MaxAgentSteps
	case BudgetToolCall:
		used, limit = &b.used.ToolCalls, b.limits.MaxToolCalls
	case BudgetValidationWork:
		used, limit = &b.used.ValidationWork, b.limits.MaxValidationWork
	default:
		return failure(CategoryInvalidInput, "reserve", "dimension")
	}
	if *used > limit || units > limit-*used {
		return failure(CategoryBudget, "reserve", string(dimension))
	}
	*used += units
	return nil
}

// Snapshot combines deterministic counters with confirmed feature-9 session consumption.
func (b *BudgetController) Snapshot() BudgetSnapshot {
	b.mu.Lock()
	snapshot := b.used
	prior := b.prior
	base := b.base
	restored := b.restored
	b.mu.Unlock()
	modelSnapshot := b.model.Snapshot()
	if restored {
		snapshot.ModelCalls = prior.ModelCalls + modelSnapshot.Calls - base.Calls
		snapshot.Replans = prior.Replans + modelSnapshot.Replans - base.Replans
		snapshot.ModelTokens = prior.ModelTokens + modelSnapshot.Tokens - base.Tokens
		snapshot.Duration = prior.Duration + modelSnapshot.Duration - base.Duration
	} else {
		snapshot.ModelCalls, snapshot.Replans, snapshot.ModelTokens, snapshot.Duration = modelSnapshot.Calls, modelSnapshot.Replans, modelSnapshot.Tokens, modelSnapshot.Duration
	}
	return snapshot
}

// Restore seeds confirmed run-wide consumption when a new coordinator adopts a durable checkpoint.
func (b *BudgetController) Restore(snapshot BudgetSnapshot) error {
	if snapshot.AgentSteps < 0 || snapshot.AgentSteps > b.limits.MaxAgentSteps || snapshot.ToolCalls < 0 || snapshot.ToolCalls > b.limits.MaxToolCalls || snapshot.ValidationWork < 0 || snapshot.ValidationWork > b.limits.MaxValidationWork || snapshot.ModelCalls < 0 || snapshot.ModelCalls > b.limits.MaxModelCalls || snapshot.Replans < 0 || snapshot.Replans > b.limits.MaxReplans || snapshot.ModelTokens < 0 || snapshot.Duration < 0 {
		return failure(CategoryIntegrity, "budget", "checkpoint")
	}
	b.mu.Lock()
	if b.used.AgentSteps != 0 || b.used.ToolCalls != 0 || b.used.ValidationWork != 0 || b.restored {
		b.mu.Unlock()
		return nil
	}
	if b.restoring {
		b.mu.Unlock()
		return failure(CategoryIntegrity, "budget", "restore_in_progress")
	}
	b.restoring = true
	b.mu.Unlock()
	// ModelBudget is an injected dependency: it must run outside the state mutex.
	current := b.model.Snapshot()
	b.mu.Lock()
	defer b.mu.Unlock()
	defer func() { b.restoring = false }()
	if b.used.AgentSteps != 0 || b.used.ToolCalls != 0 || b.used.ValidationWork != 0 || b.restored {
		return failure(CategoryIntegrity, "budget", "concurrent_restore")
	}
	// The same model session may already include the checkpoint's consumption.
	// Use the larger confirmed value as baseline and count only future deltas.
	snapshot.ModelCalls = max(snapshot.ModelCalls, current.Calls)
	snapshot.Replans = max(snapshot.Replans, current.Replans)
	snapshot.ModelTokens = max(snapshot.ModelTokens, current.Tokens)
	snapshot.Duration = max(snapshot.Duration, current.Duration)
	if snapshot.ModelCalls > b.limits.MaxModelCalls || snapshot.Replans > b.limits.MaxReplans || snapshot.ModelTokens > b.limits.MaxModelTokens || snapshot.Duration > b.limits.MaxDuration {
		return failure(CategoryIntegrity, "budget", "session")
	}
	b.used.AgentSteps, b.used.ToolCalls, b.used.ValidationWork = snapshot.AgentSteps, snapshot.ToolCalls, snapshot.ValidationWork
	b.prior, b.base, b.restored = snapshot, current, true
	return nil
}

// Remaining subtracts confirmed consumption without wrapping any budget dimension.
func (b *BudgetController) Remaining() BudgetSnapshot {
	used := b.Snapshot()
	return BudgetSnapshot{AgentSteps: nonNegative(b.limits.MaxAgentSteps - used.AgentSteps), ToolCalls: nonNegative(b.limits.MaxToolCalls - used.ToolCalls), ValidationWork: nonNegative(b.limits.MaxValidationWork - used.ValidationWork), ModelCalls: nonNegative(b.limits.MaxModelCalls - used.ModelCalls), Replans: nonNegative(b.limits.MaxReplans - used.Replans), ModelTokens: nonNegative64(b.limits.MaxModelTokens - used.ModelTokens), Duration: nonNegativeDuration(b.limits.MaxDuration - used.Duration)}
}

// AdmitInference checks all model dimensions and reserves agent-step and replan units before auth or transport.
func (b *BudgetController) AdmitInference(ctx context.Context) error {
	if ctx.Err() != nil {
		return failure(CategoryCancelled, "admit_model", "context")
	}
	remaining := b.Remaining()
	for _, exhausted := range []struct {
		remaining int64
		name      string
	}{
		{int64(remaining.AgentSteps), "agent_steps"},
		{int64(remaining.Replans), "replans"},
		{int64(remaining.ModelCalls), "model_calls"},
		{remaining.ModelTokens, "model_tokens"},
		{int64(remaining.Duration), "duration"},
	} {
		if exhausted.remaining == 0 {
			return failure(CategoryBudget, "admit_model", exhausted.name)
		}
	}
	if err := b.Reserve(ctx, BudgetAgentStep, 1); err != nil {
		return err
	}
	if err := b.model.ReserveReplan(ctx); err != nil {
		return failure(CategoryBudget, "admit_model", "replans")
	}
	return nil
}

// nonNegative clamps exhausted count budgets to zero for model-facing snapshots.
func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

// nonNegative64 clamps exhausted token budgets without losing confirmed overrun.
func nonNegative64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

// nonNegativeDuration clamps expired deadlines in the model-facing view.
func nonNegativeDuration(value time.Duration) time.Duration {
	if value < 0 {
		return 0
	}
	return value
}
