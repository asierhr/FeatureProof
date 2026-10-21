package githubpr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestWebhookRoutesAuthenticatedContractReviewAndReplaysReceipt proves productive GitHub routing preserves actor and idempotency.
func TestWebhookRoutesAuthenticatedContractReviewAndReplaysReceipt(t *testing.T) {
	secret := []byte("0123456789abcdef")
	store := testStore(t)
	calls := 0
	handler := &WebhookHandler{Secret: secret, MaxBody: 4096, Store: store, Owner: "webhook-review", Clock: &fakeClock{now: time.Unix(1, 0)}, LeaseTTL: 2 * time.Minute, Admit: func(context.Context, WebhookCommand) (string, error) {
		t.Fatal("validation route used")
		return "", nil
	}, Review: func(_ context.Context, command WebhookCommand) (string, error) {
		calls++
		if command.ActorID != 44 || command.ReviewOperation != "decide" || command.ReviewID != "review-1" || command.ReviewRevision != 7 || command.ReviewAction != "accept_all" {
			t.Fatalf("untrusted translation: %#v", command)
		}
		return command.ReviewID, nil
	}}
	body := []byte(`{"action":"created","installation":{"id":1},"repository":{"id":2},"issue":{"number":3,"pull_request":{}},"comment":{"body":"/featureproof contract-review decide review-1 7 accept_all","user":{"id":44}}}`)
	for range 2 {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, signedRequest(t, secret, "review-delivery", "issue_comment", body))
		if recorder.Code != http.StatusAccepted && recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("redelivery invoked review %d times", calls)
	}
}
