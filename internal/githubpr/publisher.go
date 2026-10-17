package githubpr

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Publisher reconciles FeatureProof-owned checks and one bot-owned summary comment.
type Publisher struct {
	GitHub            PullRequestGateway
	Store             Store
	Retry             RetryPolicy
	AppID, BotID      int64
	MaxPages          int
	DetailsURL, Owner string
	Clock             Clock
	LeaseTTL          time.Duration
}

// RecoverIntent replays the exact durable payload so reconciliation precedes every new publication phase.
func (p Publisher) RecoverIntent(ctx context.Context, a Association, intent PublicationIntent) (Association, error) {
	if p.GitHub == nil || p.Store == nil || p.AppID <= 0 || p.MaxPages <= 0 || p.Owner == "" || p.Clock == nil || !leaseCovers(p.LeaseTTL, p.Retry.Config.MaxDuration) || VerifyAssociation(a) != nil || VerifyPublicationIntent(intent) != nil || intent.AssociationID != a.ID || intent.Owner != p.Owner || intent.State != "prepared" {
		return Association{}, &Error{Code: CodeIntegrity, Operation: "intent_recovery"}
	}
	switch intent.Kind {
	case "check":
		payload := *intent.Check
		if intent.ExpectedID != a.ExternalCheckID || payload.Name != "FeatureProof validation" || payload.HeadOID != a.Snapshot.HeadOID || payload.ExternalID != a.ExternalCheckID {
			return Association{}, &Error{Code: CodeIntegrity, Operation: "intent_recovery"}
		}
		if _, err := projectionFromCheck(payload); err != nil {
			return Association{}, err
		}
		recovered, err := p.applyCheckPayload(ctx, a, payload)
		if err != nil {
			return Association{}, err
		}
		if err := p.Store.ConfirmIntent(ctx, a.ID, "check", "confirmed"); err != nil {
			return Association{}, err
		}
		return recovered, nil
	case "comment":
		if p.BotID <= 0 || intent.ExpectedID != fmt.Sprint(p.BotID) || !hasCanonicalMarker(intent.Comment.Body) {
			return Association{}, &Error{Code: CodeIntegrity, Operation: "intent_recovery"}
		}
		recovered, err := p.applyCommentPayload(ctx, a, *intent.Comment)
		if err != nil {
			return Association{}, err
		}
		if err := p.Store.ConfirmIntent(ctx, a.ID, "comment", "confirmed"); err != nil {
			return Association{}, err
		}
		return recovered, nil
	default:
		return Association{}, &Error{Code: CodeIntegrity, Operation: "intent_recovery"}
	}
}

// projectionFromCheck reconstructs only the projection fields used to reproduce a stored check payload byte-for-byte.
func projectionFromCheck(payload CheckPublication) (Projection, error) {
	outcome := ""
	switch {
	case payload.Status == "queued" && payload.Conclusion == "":
		outcome = "queued"
	case payload.Status == "in_progress" && payload.Conclusion == "":
		outcome = "running"
	case payload.Status == "completed" && payload.Conclusion == "success":
		outcome = "passed"
	case payload.Status == "completed" && payload.Conclusion == "failure":
		outcome = "failed"
	case payload.Status == "completed" && payload.Conclusion == "neutral":
		outcome = "inconclusive"
	case payload.Status == "completed" && payload.Conclusion == "cancelled":
		outcome = "cancelled"
	case payload.Status == "completed" && payload.Conclusion == "stale":
		outcome = "stale"
	case payload.Status == "completed" && payload.Conclusion == "action_required":
		outcome = "error"
	default:
		return Projection{}, &Error{Code: CodeIntegrity, Operation: "intent_check_payload"}
	}
	return Projection{Outcome: outcome, Title: payload.Title, Summary: payload.Summary}, nil
}

// PublishCheck prepares an intent and verifies remote ownership before every update.
func (p Publisher) PublishCheck(ctx context.Context, a Association, v Projection) (Association, error) {
	if p.GitHub == nil || p.Store == nil || p.AppID <= 0 || p.MaxPages <= 0 || p.Owner == "" || p.Clock == nil || !leaseCovers(p.LeaseTTL, p.Retry.Config.MaxDuration) || a.RunHandle == "" || a.ExternalCheckID == "" || VerifyAssociation(a) != nil {
		return Association{}, &Error{Code: CodeInternal, Operation: "publish_check"}
	}
	status, conclusion := mapCheck(v.Outcome)
	payload := CheckPublication{Name: "FeatureProof validation", HeadOID: a.Snapshot.HeadOID, ExternalID: a.ExternalCheckID, Status: status, Conclusion: conclusion, Title: v.Title, Summary: v.Summary, DetailsURL: p.DetailsURL}
	payloadDigest, _ := CanonicalDigest(payload)
	now := p.Clock.Now()
	intent, fresh, err := p.Store.PrepareIntent(ctx, PublicationIntent{AssociationID: a.ID, Kind: "check", PayloadSHA256: payloadDigest, ExpectedID: a.ExternalCheckID, Check: &payload, Owner: p.Owner, LeaseStartedAt: now, LeaseUntil: now.Add(p.LeaseTTL)})
	if err != nil {
		return Association{}, err
	}
	if !fresh && intent.State == "confirmed" {
		return a, nil
	}
	a, err = p.applyCheckPayload(ctx, a, payload)
	if err != nil {
		return Association{}, err
	}
	if err = p.Store.ConfirmIntent(ctx, a.ID, "check", "confirmed"); err != nil {
		return Association{}, err
	}
	return a, nil
}

// PublishComment prepares an intent and verifies bot plus marker before every update.
func (p Publisher) PublishComment(ctx context.Context, a Association, v Projection) (Association, error) {
	if p.BotID <= 0 {
		return a, nil
	}
	if !leaseCovers(p.LeaseTTL, p.Retry.Config.MaxDuration) || a.RunHandle == "" || a.ExternalCheckID == "" || VerifyAssociation(a) != nil {
		return Association{}, &Error{Code: CodeIntegrity, Operation: "comment_owner"}
	}
	body := SummaryMarker + "\n" + v.Summary
	comment := CommentPublication{Body: body}
	payloadDigest, _ := CanonicalDigest(comment)
	now := p.Clock.Now()
	intent, fresh, err := p.Store.PrepareIntent(ctx, PublicationIntent{AssociationID: a.ID, Kind: "comment", PayloadSHA256: payloadDigest, ExpectedID: fmt.Sprint(p.BotID), Comment: &comment, Owner: p.Owner, LeaseStartedAt: now, LeaseUntil: now.Add(p.LeaseTTL)})
	if err != nil {
		return Association{}, err
	}
	if !fresh && intent.State == "confirmed" {
		return a, nil
	}
	a, err = p.applyCommentPayload(ctx, a, comment)
	if err != nil {
		return Association{}, err
	}
	if err = p.Store.ConfirmIntent(ctx, a.ID, "comment", "confirmed"); err != nil {
		return Association{}, err
	}
	return a, nil
}

// applyCheckPayload reconciles or writes the exact already-validated payload without consulting mutable configuration.
func (p Publisher) applyCheckPayload(ctx context.Context, a Association, payload CheckPublication) (Association, error) {
	var record CheckRecord
	err := p.Retry.Do(ctx, func(callCtx context.Context) error {
		if a.CheckID == 0 {
			found, findErr := p.reconcileCheck(callCtx, a)
			if findErr == nil {
				record = found
				return nil
			}
			if !errorsNotFound(findErr) {
				return findErr
			}
			created, createErr := p.GitHub.CreateCheck(callCtx, a.Snapshot.InstallationID, a.Snapshot.RepositoryID, payload)
			if createErr != nil {
				return createErr
			}
			record = created
			return nil
		}
		owned, getErr := p.GitHub.GetCheck(callCtx, a.Snapshot.InstallationID, a.Snapshot.RepositoryID, a.CheckID)
		if getErr != nil {
			return getErr
		}
		if !p.ownedCheck(a, owned) {
			return &Error{Code: CodeIntegrity, Operation: "check_owner"}
		}
		updated, updateErr := p.GitHub.UpdateCheck(callCtx, a.Snapshot.InstallationID, a.Snapshot.RepositoryID, a.CheckID, payload)
		if updateErr != nil {
			return updateErr
		}
		record = updated
		return nil
	})
	if err != nil {
		return Association{}, err
	}
	if !p.ownedCheck(a, record) {
		return Association{}, &Error{Code: CodeIntegrity, Operation: "check_owner"}
	}
	if a.CheckID == 0 {
		prior := a.VersionCAS
		a.CheckID = record.ID
		return p.Store.UpdateAssociation(ctx, a, prior)
	}
	return a, nil
}

// applyCommentPayload reconciles or writes the exact durable comment body after marker and bot validation.
func (p Publisher) applyCommentPayload(ctx context.Context, a Association, payload CommentPublication) (Association, error) {
	var record CommentRecord
	err := p.Retry.Do(ctx, func(callCtx context.Context) error {
		if a.CommentID == 0 {
			found, findErr := p.reconcileComment(callCtx, a)
			if findErr == nil {
				if found.Body == payload.Body {
					record = found
					return nil
				}
				prior := a.VersionCAS
				a.CommentID = found.ID
				a, findErr = p.Store.UpdateAssociation(callCtx, a, prior)
				if findErr != nil {
					return findErr
				}
				updated, updateErr := p.GitHub.UpdateComment(callCtx, a.Snapshot.InstallationID, a.Snapshot.RepositoryID, a.CommentID, payload.Body)
				if updateErr != nil {
					return updateErr
				}
				record = updated
				return nil
			}
			if !errorsNotFound(findErr) {
				return findErr
			}
			created, createErr := p.GitHub.CreateComment(callCtx, a.Snapshot.InstallationID, a.Snapshot.RepositoryID, a.Snapshot.Number, payload.Body)
			if createErr != nil {
				return createErr
			}
			record = created
			return nil
		}
		owned, getErr := p.GitHub.GetComment(callCtx, a.Snapshot.InstallationID, a.Snapshot.RepositoryID, a.CommentID)
		if getErr != nil {
			return getErr
		}
		if !p.ownedComment(a, owned) {
			return &Error{Code: CodeIntegrity, Operation: "comment_owner"}
		}
		if owned.Body == payload.Body {
			record = owned
			return nil
		}
		updated, updateErr := p.GitHub.UpdateComment(callCtx, a.Snapshot.InstallationID, a.Snapshot.RepositoryID, a.CommentID, payload.Body)
		if updateErr != nil {
			return updateErr
		}
		record = updated
		return nil
	})
	if err != nil {
		return Association{}, err
	}
	if !p.ownedComment(a, record) || record.Body != payload.Body {
		return Association{}, &Error{Code: CodeIntegrity, Operation: "comment_owner"}
	}
	if a.CommentID == 0 {
		prior := a.VersionCAS
		a.CommentID = record.ID
		return p.Store.UpdateAssociation(ctx, a, prior)
	}
	return a, nil
}

// reconcileCheck searches all bounded pages and accepts one exact app/head/external identity.
func (p Publisher) reconcileCheck(ctx context.Context, a Association) (CheckRecord, error) {
	records, err := p.GitHub.FindChecks(ctx, a.Snapshot.InstallationID, a.Snapshot.RepositoryID, a.Snapshot.HeadOID, a.ExternalCheckID, p.MaxPages)
	if err != nil {
		return CheckRecord{}, err
	}
	matches := []CheckRecord{}
	for _, record := range records {
		if p.ownedCheck(a, record) {
			matches = append(matches, record)
		}
	}
	if len(matches) == 0 {
		return CheckRecord{}, ErrNotFound
	}
	if len(matches) > 1 {
		return CheckRecord{}, &Error{Code: CodeConflict, Operation: "check_reconcile"}
	}
	return matches[0], nil
}

// reconcileComment searches all bounded pages and accepts one exact bot/marker identity.
func (p Publisher) reconcileComment(ctx context.Context, a Association) (CommentRecord, error) {
	records, err := p.GitHub.FindComments(ctx, a.Snapshot.InstallationID, a.Snapshot.RepositoryID, a.Snapshot.Number, SummaryMarker, p.MaxPages)
	if err != nil {
		return CommentRecord{}, err
	}
	matches := []CommentRecord{}
	for _, record := range records {
		if p.ownedComment(a, record) {
			matches = append(matches, record)
		}
	}
	if len(matches) == 0 {
		return CommentRecord{}, ErrNotFound
	}
	if len(matches) > 1 {
		return CommentRecord{}, &Error{Code: CodeConflict, Operation: "comment_reconcile"}
	}
	return matches[0], nil
}

// ownedCheck validates every remote identity before FeatureProof mutates a check.
func (p Publisher) ownedCheck(a Association, r CheckRecord) bool {
	return r.ID > 0 && r.AppID == p.AppID && r.HeadOID == a.Snapshot.HeadOID && r.ExternalID == a.ExternalCheckID && (a.CheckID == 0 || r.ID == a.CheckID)
}

// ownedComment validates numeric bot ownership, exact marker, and persisted ID before mutation.
func (p Publisher) ownedComment(a Association, r CommentRecord) bool {
	return r.ID > 0 && r.AuthorID == p.BotID && hasCanonicalMarker(r.Body) && (a.CommentID == 0 || r.ID == a.CommentID)
}

// hasCanonicalMarker accepts only the exact versioned marker as the complete first line.
func hasCanonicalMarker(body string) bool {
	first, _, found := strings.Cut(body, "\n")
	return found && first == SummaryMarker
}

// mapCheck preserves FeatureProof outcome while selecting GitHub lifecycle and conclusion.
func mapCheck(outcome string) (string, string) {
	switch outcome {
	case "queued":
		return "queued", ""
	case "running", "finalizing":
		return "in_progress", ""
	case "passed":
		return "completed", "success"
	case "failed":
		return "completed", "failure"
	case "inconclusive":
		return "completed", "neutral"
	case "cancelled":
		return "completed", "cancelled"
	case "stale":
		return "completed", "stale"
	default:
		return "completed", "action_required"
	}
}

// errorsNotFound isolates local absence checks from remote failure categories.
func errorsNotFound(err error) bool { return err == ErrNotFound }
