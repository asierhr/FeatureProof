package githubpr

import (
	"context"
	"time"
)

// RetryConfig bounds attempts, total duration, cumulative wait, and exponential backoff.
type RetryConfig struct {
	MaxAttempts                                      int
	MaxDuration, MaxWait, InitialBackoff, MaxBackoff time.Duration
}

// RetryPolicy retries only classified transient operations with injected time and jitter.
type RetryPolicy struct {
	Config RetryConfig
	Clock  Clock
	Jitter Jitter
}

// Do runs one cancelable operation while respecting all retry budgets.
func (p RetryPolicy) Do(ctx context.Context, operation func(context.Context) error) error {
	if p.Clock == nil || p.Jitter == nil || p.Config.MaxAttempts < 1 || p.Config.MaxDuration <= 0 || p.Config.MaxWait < 0 || p.Config.InitialBackoff <= 0 || p.Config.MaxBackoff < p.Config.InitialBackoff {
		return &Error{Code: CodeInternal, Operation: "retry_config"}
	}
	started := p.Clock.Now()
	budgetCtx, cancelBudget := context.WithTimeout(ctx, p.Config.MaxDuration)
	defer cancelBudget()
	waited := time.Duration(0)
	backoff := p.Config.InitialBackoff
	for attempt := 1; attempt <= p.Config.MaxAttempts; attempt++ {
		if err := budgetCtx.Err(); err != nil {
			if ctx.Err() == nil {
				return &Error{Code: CodeTransient, Operation: "retry_deadline"}
			}
			return &Error{Code: CodeCancelled, Operation: "retry"}
		}
		attemptCtx, cancelAttempt := context.WithDeadline(budgetCtx, time.Now().Add(p.Config.MaxDuration))
		err := operation(attemptCtx)
		attemptExpired := attemptCtx.Err()
		cancelAttempt()
		if err == nil {
			return nil
		}
		if attemptExpired != nil {
			if ctx.Err() != nil {
				return &Error{Code: CodeCancelled, Operation: "retry"}
			}
			return &Error{Code: CodeTransient, Operation: "retry_deadline"}
		}
		var wait time.Duration
		var typed *Error
		if !asGitHubError(err, &typed) || (typed.Code != CodeTransient && typed.Code != CodeRateLimited) || attempt == p.Config.MaxAttempts {
			return err
		}
		if typed.RetryAfter > 0 {
			wait = typed.RetryAfter
		} else if !typed.ResetAt.IsZero() && typed.ResetAt.After(p.Clock.Now()) {
			wait = typed.ResetAt.Sub(p.Clock.Now())
		} else {
			wait = p.Jitter.Duration(backoff)
		}
		if wait < 0 || waited+wait > p.Config.MaxWait || p.Clock.Now().Sub(started)+wait > p.Config.MaxDuration {
			return &Error{Code: typed.Code, Operation: "retry_budget"}
		}
		if err := p.Clock.Wait(budgetCtx, wait); err != nil {
			if ctx.Err() == nil && budgetCtx.Err() != nil {
				return &Error{Code: typed.Code, Operation: "retry_budget"}
			}
			return &Error{Code: CodeCancelled, Operation: "retry_wait"}
		}
		waited += wait
		backoff *= 2
		if backoff > p.Config.MaxBackoff {
			backoff = p.Config.MaxBackoff
		}
	}
	return &Error{Code: CodeInternal, Operation: "retry"}
}

// asGitHubError extracts typed adapter errors without comparing text.
func asGitHubError(err error, target **Error) bool {
	typed, ok := err.(*Error)
	if ok {
		*target = typed
	}
	return ok
}
