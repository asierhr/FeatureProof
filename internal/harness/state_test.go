package harness

import "testing"

// TestRunConfigurationAndTransitions protects the run lifecycle and explicit budget preconditions.
func TestRunConfigurationAndTransitions(t *testing.T) {
	tests := []struct {
		name   string
		limits Limits
		bad    bool
	}{
		{"valid", Limits{MaxSteps: 1, MaxToolCalls: 1, MaxRetries: 0}, false},
		{"negative steps", Limits{MaxSteps: -1}, true},
		{"negative calls", Limits{MaxToolCalls: -1}, true},
		{"negative retries", Limits{MaxRetries: -1}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRun("run-1", "goal", "constraint-1", tt.limits)
			if (err != nil) != tt.bad {
				t.Fatalf("error=%v, want bad=%v", err, tt.bad)
			}
		})
	}
	r, _ := NewRun("run-1", "goal", "constraint-1", Limits{MaxSteps: 1})
	if err := r.Transition(StatusPlanning); err != nil {
		t.Fatal(err)
	}
	if err := r.Transition(StatusCompleted); err == nil {
		t.Fatal("invalid transition accepted")
	}
}

// TestApprovalTransitionsPermitOnlyTheApprovedLifecycleEdges keeps execution unreachable except through the approval phase.
func TestApprovalTransitionsPermitOnlyTheApprovedLifecycleEdges(t *testing.T) {
	for _, destination := range []Status{StatusExecuting, StatusPlanning, StatusCancelled} {
		run, _ := NewRun("run", "goal", "constraint", Limits{})
		if err := run.Transition(StatusPlanning); err != nil {
			t.Fatal(err)
		}
		if err := run.Transition(StatusAwaitingApproval); err != nil {
			t.Fatal(err)
		}
		if err := run.Transition(destination); err != nil {
			t.Fatalf("destination %s rejected: %v", destination, err)
		}
	}
	for _, destination := range []Status{StatusCreated, StatusReviewing, StatusCompleted, StatusAwaitingApproval, StatusFailed, StatusInconclusive} {
		run, _ := NewRun("run", "goal", "constraint", Limits{})
		_ = run.Transition(StatusPlanning)
		_ = run.Transition(StatusAwaitingApproval)
		if err := run.Transition(destination); err == nil {
			t.Fatalf("destination %s accepted", destination)
		}
	}
}

func TestRunRequiresIdentityObjectiveAndConstraint(t *testing.T) {
	for _, values := range [][3]string{{"", "goal", "c"}, {"r", "", "c"}, {"r", "goal", ""}} {
		if _, err := NewRun(values[0], values[1], values[2], Limits{}); err == nil {
			t.Fatalf("accepted %#v", values)
		}
	}
}

func TestRetryBudgetNeverBecomesNegative(t *testing.T) {
	r, _ := NewRun("r", "goal", "c", Limits{MaxRetries: 1})
	if !r.ReserveRetry() || r.ReserveRetry() || r.Consumed.Retries != 1 {
		t.Fatalf("consumption=%+v", r.Consumed)
	}
}

func TestRetryAndToolBudgetsReserveAtomically(t *testing.T) {
	tests := []struct {
		name       string
		limits     Limits
		consumed   Consumption
		wantReason RejectReason
	}{
		{"retry exhausted", Limits{MaxToolCalls: 2, MaxRetries: 0}, Consumption{ToolCalls: 1}, RejectRetryBudgetExhausted},
		{"tool exhausted", Limits{MaxToolCalls: 1, MaxRetries: 1}, Consumption{ToolCalls: 1}, RejectBudgetExhausted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run, _ := NewRun("r", "goal", "c", test.limits)
			run.Consumed = test.consumed
			reason, ok := run.ReserveRetryToolCall()
			if ok || reason != test.wantReason || run.Consumed != test.consumed {
				t.Fatalf("reason=%s ok=%v consumed=%+v", reason, ok, run.Consumed)
			}
		})
	}
}

func TestHarnessReservesToolBudgetBeforeExecution(t *testing.T) {
	r, _ := NewRun("run-1", "goal", "constraint-1", Limits{MaxToolCalls: 1})
	if !r.ReserveToolCall() || r.Consumed.ToolCalls != 1 {
		t.Fatalf("first reservation failed: %+v", r.Consumed)
	}
	if r.ReserveToolCall() || r.Consumed.ToolCalls != 1 {
		t.Fatalf("exhausted reservation changed budget: %+v", r.Consumed)
	}
}

func TestHarnessEmitsExactlyOneTerminalEvent(t *testing.T) {
	r, _ := NewRun("run-1", "goal", "constraint-1", Limits{})
	if err := r.Transition(StatusInconclusive); err != nil {
		t.Fatal(err)
	}
	if err := r.Transition(StatusFailed); err == nil {
		t.Fatal("terminal run transitioned again")
	}
}
