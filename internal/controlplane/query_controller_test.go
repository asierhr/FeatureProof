package controlplane

import (
	"context"
	"errors"
	"testing"
	"time"
)

// queryReaderProbe proves authorization occurs before the database-facing port.
type queryReaderProbe struct {
	called bool
}

// ListJobs records the authorized query without supplying persisted content.
func (probe *queryReaderProbe) ListJobs(_ context.Context, _ JobQuery, _ *CursorCodec) (JobPage, error) {
	probe.called = true
	return JobPage{}, nil
}

// projectRetentionProbe records the exact project-scoped deletion request.
type projectRetentionProbe struct {
	called    bool
	projectID string
}

// DeleteProjectTerminalBefore records one bounded authorized retention page.
func (probe *projectRetentionProbe) DeleteProjectTerminalBefore(_ context.Context, projectID string, _ int64, _ int) (int, error) {
	probe.called = true
	probe.projectID = projectID
	return 1, nil
}

// TestQueryControllerAuthorizesBeforeStore proves denied operators cannot use
// cursor or persistence behavior as an existence oracle.
func TestQueryControllerAuthorizesBeforeStore(t *testing.T) {
	reader := &queryReaderProbe{}
	authorizer := &importAuthorizerProbe{err: ErrUnauthorized}
	codec, err := NewCursorCodec(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	_, err = (QueryController{Store: reader, Authorizer: authorizer, Cursors: codec}).ListJobs(context.Background(), JobQuery{ProjectID: "project-1", Limit: 1, Now: time.Now().UTC(), CursorTTL: time.Minute})
	if !errors.Is(err, ErrUnauthorized) || reader.called {
		t.Fatalf("denied query reached store: called=%v err=%v", reader.called, err)
	}
}

// TestRetentionControllerIsProjectScopedAndNoPolicySafe proves absence of an
// explicit policy cannot delete and an enabled policy preserves project scope.
func TestRetentionControllerIsProjectScopedAndNoPolicySafe(t *testing.T) {
	store := &projectRetentionProbe{}
	authorizer := &importAuthorizerProbe{}
	controller := RetentionController{Store: store, Authorizer: authorizer}
	if count, err := controller.Run(context.Background(), "project-1", nil); err != nil || count != 0 || store.called {
		t.Fatalf("no-policy retention mutated: count=%d called=%v err=%v", count, store.called, err)
	}
	count, err := controller.Run(context.Background(), "project-1", &RetentionPolicy{BeforeUnix: time.Now().UTC().Unix(), BatchSize: 1})
	if err != nil || count != 1 || !store.called || store.projectID != "project-1" {
		t.Fatalf("project scope lost: count=%d project=%q err=%v", count, store.projectID, err)
	}
}

// TestRetentionControllerDenialPrecedesStore proves rejected authority cannot
// trigger even a valid deletion policy.
func TestRetentionControllerDenialPrecedesStore(t *testing.T) {
	store := &projectRetentionProbe{}
	controller := RetentionController{Store: store, Authorizer: &importAuthorizerProbe{err: ErrUnauthorized}}
	_, err := controller.Run(context.Background(), "project-1", &RetentionPolicy{BeforeUnix: time.Now().UTC().Unix(), BatchSize: 1})
	if !errors.Is(err, ErrUnauthorized) || store.called {
		t.Fatalf("denied retention reached store: called=%v err=%v", store.called, err)
	}
}
