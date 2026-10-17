package githubpr

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// WebhookCommand is the strictly translated, payload-minimal admission request.
type WebhookCommand struct {
	DeliveryID, Event                                        string
	InstallationID, RepositoryID, PullRequestNumber, ActorID int64
	SelectionAlias                                           string
	PayloadDigest                                            string
}

// WebhookResult is the replayable safe response recorded for a delivery.
type WebhookResult struct {
	State, AssociationID string
}

// DeliveryStore reserves and completes authenticated deliveries atomically.
type DeliveryStore interface {
	ReserveDelivery(context.Context, DeliveryReceipt, string, time.Time, time.Duration) (DeliveryReceipt, bool, error)
	CompleteDelivery(context.Context, string, uint64, string, string) (DeliveryReceipt, error)
}

// WebhookHandler authenticates raw bytes before decoding and delegates admitted commands.
type WebhookHandler struct {
	Secret   []byte
	MaxBody  int64
	Store    DeliveryStore
	Admit    func(context.Context, WebhookCommand) (string, error)
	Owner    string
	Clock    Clock
	LeaseTTL time.Duration
}

// ServeHTTP enforces body/header/signature bounds and durable replay semantics.
func (h *WebhookHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if h == nil || len(h.Secret) < 16 || h.MaxBody <= 0 || h.Store == nil || h.Admit == nil || h.Owner == "" || h.Clock == nil || h.LeaseTTL <= 0 {
		http.Error(writer, "unavailable", http.StatusServiceUnavailable)
		return
	}
	reader := http.MaxBytesReader(writer, request.Body, h.MaxBody)
	body, err := io.ReadAll(reader)
	if err != nil {
		http.Error(writer, "invalid request", http.StatusRequestEntityTooLarge)
		return
	}
	deliveryID, event := request.Header.Get("X-GitHub-Delivery"), request.Header.Get("X-GitHub-Event")
	if !safeName.MatchString(deliveryID) || event == "" || !verifySignature(h.Secret, body, request.Header.Get("X-Hub-Signature-256")) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	receipt := DeliveryReceipt{Version: SchemaVersion, DeliveryID: deliveryID, Event: event, PayloadSHA256: digest(body), State: "received", VersionCAS: 1}
	reserved, fresh, err := h.Store.ReserveDelivery(request.Context(), receipt, h.Owner, h.Clock.Now(), h.LeaseTTL)
	if err != nil {
		writeWebhookError(writer, err)
		return
	}
	if !fresh {
		writeWebhookJSON(writer, WebhookResult{State: reserved.State, AssociationID: reserved.AssociationID})
		return
	}
	command, ignored, err := decodeCommand(deliveryID, event, receipt.PayloadSHA256, body)
	if err != nil {
		_, _ = h.Store.CompleteDelivery(request.Context(), deliveryID, reserved.VersionCAS, "rejected", "")
		writeWebhookError(writer, err)
		return
	}
	if ignored {
		completed, completeErr := h.Store.CompleteDelivery(request.Context(), deliveryID, reserved.VersionCAS, "ignored", "")
		if completeErr != nil {
			writeWebhookError(writer, completeErr)
			return
		}
		writeWebhookJSON(writer, WebhookResult{State: completed.State})
		return
	}
	associationID, err := h.Admit(request.Context(), command)
	state := "accepted"
	if err != nil {
		state = "rejected"
	}
	completed, completeErr := h.Store.CompleteDelivery(request.Context(), deliveryID, reserved.VersionCAS, state, associationID)
	if completeErr != nil {
		writeWebhookError(writer, completeErr)
		return
	}
	if err != nil {
		writeWebhookError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(writer).Encode(WebhookResult{State: completed.State, AssociationID: completed.AssociationID})
}

// verifySignature compares the canonical GitHub HMAC using constant-time equality.
func verifySignature(secret, body []byte, signature string) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hmac.Equal(mac.Sum(nil), provided)
}

// webhookPayload confines GitHub webhook field names to the authenticated adapter boundary.
type webhookPayload struct {
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		ID int64 `json:"id"`
	} `json:"repository"`
	Issue struct {
		Number      int64     `json:"number"`
		PullRequest *struct{} `json:"pull_request"`
	} `json:"issue"`
	Comment struct {
		Body string `json:"body"`
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	} `json:"comment"`
}

// decodeCommand translates only exact issue_comment.created PR commands after authentication.
func decodeCommand(deliveryID, event, payloadDigest string, body []byte) (WebhookCommand, bool, error) {
	if event != "issue_comment" {
		return WebhookCommand{}, true, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var payload webhookPayload
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return WebhookCommand{}, false, &Error{Code: CodeInvalid, Operation: "payload"}
	}
	if payload.Action != "created" || payload.Issue.PullRequest == nil || !normalizeCommand(payload.Comment.Body) {
		return WebhookCommand{}, true, nil
	}
	if payload.Installation.ID <= 0 || payload.Repository.ID <= 0 || payload.Issue.Number <= 0 || payload.Comment.User.ID <= 0 {
		return WebhookCommand{}, false, &Error{Code: CodeInvalid, Operation: "payload"}
	}
	return WebhookCommand{DeliveryID: deliveryID, Event: event, InstallationID: payload.Installation.ID, RepositoryID: payload.Repository.ID, PullRequestNumber: payload.Issue.Number, ActorID: payload.Comment.User.ID, SelectionAlias: "default", PayloadDigest: payloadDigest}, false, nil
}

// writeWebhookError maps safe error categories to bounded HTTP responses.
func writeWebhookError(writer http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if IsCode(err, CodeUnauthorized) {
		status = http.StatusForbidden
	}
	if IsCode(err, CodeConflict) || IsCode(err, CodeIntegrity) {
		status = http.StatusConflict
	}
	if IsCode(err, CodeInvalid) {
		status = http.StatusBadRequest
	}
	http.Error(writer, http.StatusText(status), status)
}

// writeWebhookJSON emits a replay response without echoing request data.
func writeWebhookJSON(writer http.ResponseWriter, value WebhookResult) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}
