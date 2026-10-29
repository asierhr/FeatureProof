package controlplane

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"time"
)

// PageCursor carries an authenticated, expiring keyset position for one stable project/filter snapshot.
type PageCursor struct {
	Version           string `json:"version"`
	ProjectID         string `json:"project_id"`
	Filter            string `json:"filter"`
	SnapshotUnixNano  int64  `json:"snapshot_unix_nano"`
	LastUnixNano      int64  `json:"last_unix_nano"`
	LastKey           string `json:"last_key"`
	ExpiresAtUnixNano int64  `json:"expires_at_unix_nano"`
}

// JobQuery bounds one stable PostgreSQL keyset listing without offset semantics.
type JobQuery struct {
	ProjectID string
	State     JobState
	Cursor    string
	Limit     int
	Now       time.Time
	CursorTTL time.Duration
}

// JobPage returns detached jobs plus an authenticated continuation when another page may exist.
type JobPage struct {
	Jobs       []Job
	NextCursor string
}

// CursorCodec owns the composition secret used to authenticate pagination state without persisting it.
type CursorCodec struct {
	key []byte
}

// NewCursorCodec detaches a sufficiently strong composition secret from its caller.
func NewCursorCodec(secret []byte) (*CursorCodec, error) {
	if len(secret) < 32 {
		return nil, ErrInvalid
	}
	return &CursorCodec{key: append([]byte(nil), secret...)}, nil
}

// Seal authenticates one bounded project/filter/snapshot keyset continuation.
func (codec *CursorCodec) Seal(cursor PageCursor) (string, error) {
	if codec == nil || len(codec.key) < 32 {
		return "", ErrInvalid
	}
	if cursor.Version == "" {
		cursor.Version = ModelVersion
	}
	if cursor.Version != ModelVersion || !safeID.MatchString(cursor.ProjectID) || !safeID.MatchString(cursor.Filter) || !safeID.MatchString(cursor.LastKey) || cursor.SnapshotUnixNano <= 0 || cursor.LastUnixNano <= 0 || cursor.LastUnixNano > cursor.SnapshotUnixNano || cursor.ExpiresAtUnixNano <= cursor.SnapshotUnixNano {
		return "", ErrInvalid
	}
	payload, err := json.Marshal(cursor)
	if err != nil || len(payload) > MaxCanonicalBytes {
		return "", ErrInvalid
	}
	mac := hmac.New(sha256.New, codec.key)
	_, _ = mac.Write(payload)
	token := append(append([]byte(nil), payload...), mac.Sum(nil)...)
	return base64.RawURLEncoding.EncodeToString(token), nil
}

// Open verifies authentication, scope, filter, version and expiry before database access.
func (codec *CursorCodec) Open(token, projectID, filter string, now time.Time) (PageCursor, error) {
	if codec == nil || len(codec.key) < 32 || !safeID.MatchString(projectID) || !safeID.MatchString(filter) || now.IsZero() {
		return PageCursor{}, ErrInvalid
	}
	encoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(encoded) <= sha256.Size || len(encoded) > MaxCanonicalBytes {
		return PageCursor{}, ErrInvalid
	}
	payload, supplied := encoded[:len(encoded)-sha256.Size], encoded[len(encoded)-sha256.Size:]
	mac := hmac.New(sha256.New, codec.key)
	_, _ = mac.Write(payload)
	if !hmac.Equal(supplied, mac.Sum(nil)) {
		return PageCursor{}, ErrIntegrity
	}
	var cursor PageCursor
	if StrictDecode(payload, &cursor) != nil || cursor.Version != ModelVersion || cursor.ProjectID != projectID || cursor.Filter != filter {
		return PageCursor{}, ErrUnauthorized
	}
	if cursor.ExpiresAtUnixNano <= now.UTC().UnixNano() {
		return PageCursor{}, ErrDeadline
	}
	return cursor, nil
}

// RetentionPolicy enables bounded terminal-time deletion only when explicitly supplied.
type RetentionPolicy struct {
	BeforeUnix int64
	BatchSize  int
}

// Validate rejects implicit or unbounded retention work.
func (policy RetentionPolicy) Validate() error {
	if policy.BeforeUnix <= 0 || policy.BatchSize <= 0 || policy.BatchSize > 1000 {
		return ErrInvalid
	}
	return nil
}

// Observation is a content-free operational signal that cannot grant authority.
type Observation struct {
	Operation string
	Category  ErrorCategory
	Attempt   int
	Count     int64
	Duration  time.Duration
}

// Observer receives best-effort allowlisted operational signals outside authoritative transactions.
type Observer interface {
	Observe(context.Context, Observation) error
}

// JobQueryReader is the consumer-owned bounded listing port used by the
// authenticated operational controller.
type JobQueryReader interface {
	ListJobs(context.Context, JobQuery, *CursorCodec) (JobPage, error)
}

// QueryController authenticates project scope before cursor verification or
// any database query can reveal existence.
type QueryController struct {
	Store      JobQueryReader
	Authorizer OperatorAuthorizer
	Cursors    *CursorCodec
}

// ProjectRetentionStore deletes only one authenticated project page and never
// broadens an operator request to unrelated projects.
type ProjectRetentionStore interface {
	DeleteProjectTerminalBefore(context.Context, string, int64, int) (int, error)
}

// ListJobs executes one cancellable authenticated keyset page.
func (controller QueryController) ListJobs(ctx context.Context, query JobQuery) (JobPage, error) {
	if controller.Store == nil || controller.Authorizer == nil || controller.Cursors == nil {
		return JobPage{}, ErrInvalid
	}
	if err := controller.Authorizer.Authorize(ctx, "query_jobs", query.ProjectID); err != nil {
		return JobPage{}, ErrUnauthorized
	}
	return controller.Store.ListJobs(ctx, query, controller.Cursors)
}

// RetentionController authenticates explicit composition-owned policy before
// running any bounded terminal deletion page.
type RetentionController struct {
	Store      ProjectRetentionStore
	Authorizer OperatorAuthorizer
}

// Run performs one cancellable retention batch and preserves the no-policy
// no-delete invariant after authentication.
func (controller RetentionController) Run(ctx context.Context, projectID string, policy *RetentionPolicy) (int, error) {
	if controller.Store == nil || controller.Authorizer == nil {
		return 0, ErrInvalid
	}
	if err := controller.Authorizer.Authorize(ctx, "retention", projectID); err != nil {
		return 0, ErrUnauthorized
	}
	if policy == nil {
		return 0, nil
	}
	if err := policy.Validate(); err != nil {
		return 0, err
	}
	return controller.Store.DeleteProjectTerminalBefore(ctx, projectID, policy.BeforeUnix, policy.BatchSize)
}
