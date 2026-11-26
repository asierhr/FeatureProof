package tenantsecurity

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// AuditPage is a bounded organization/project journal window with an opaque continuation cursor.
type AuditPage struct {
	Events     []AuditEvent `json:"events"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

// AuditService verifies and queries the authoritative chain without exposing raw resource identities.
type AuditService struct {
	store     DurableStore
	clock     Clock
	maxPage   int
	cursorTTL time.Duration
}

// NewAuditService validates bounded query and cursor expiry configuration.
func NewAuditService(store DurableStore, clock Clock, maxPage int, cursorTTL time.Duration) (*AuditService, error) {
	if store == nil || clock == nil || maxPage <= 0 || maxPage > MaxItems || cursorTTL <= 0 {
		return nil, failure(CategoryInvalid, "audit_new", nil)
	}
	return &AuditService{store: store, clock: clock, maxPage: maxPage, cursorTTL: cursorTTL}, nil
}

// Verify checks sequence, links, event digests and the independent terminal head.
func (service *AuditService) Verify(ctx context.Context, organizationID string) error {
	if ValidateID(organizationID) != nil {
		return failure(CategoryInvalid, "audit_verify", nil)
	}
	snapshot, err := service.store.Snapshot(ctx)
	if err != nil {
		return err
	}
	events := snapshot.Audits[organizationID]
	previous := ""
	for index, event := range events {
		if event.OrganizationID != organizationID || event.Sequence != uint64(index+1) || event.PreviousDigest != previous || event.Digest != auditDigest(event) {
			return failure(CategoryIntegrity, "audit_verify", nil)
		}
		previous = event.Digest
	}
	if len(events) > 0 && snapshot.AuditHeads[organizationID] != sealAuditHead(organizationID, uint64(len(events)), previous) {
		return failure(CategoryIntegrity, "audit_verify", nil)
	}
	return nil
}

// Page returns only events in the authorized project scope and rejects forged or expired cursors.
func (service *AuditService) Page(ctx context.Context, capability Capability, cursor string, limit int) (AuditPage, error) {
	if limit <= 0 || limit > service.maxPage {
		return AuditPage{}, failure(CategoryInvalid, "audit_page", nil)
	}
	snapshot, err := service.store.Snapshot(ctx)
	if err != nil {
		return AuditPage{}, err
	}
	if err = verifyDurableCapability(snapshot, capability, ActionAuditRead, ResourceAudit, service.clock.Now()); err != nil {
		return AuditPage{}, err
	}
	after, err := service.decodeCursor(cursor, capability.Scope)
	if err != nil {
		return AuditPage{}, err
	}
	page := AuditPage{Events: make([]AuditEvent, 0, limit)}
	var last uint64
	for _, event := range snapshot.Audits[capability.Scope.OrganizationID] {
		if event.Sequence <= after || event.ProjectID != "" && event.ProjectID != capability.Scope.ProjectID {
			continue
		}
		if len(page.Events) == limit {
			page.NextCursor = service.encodeCursor(capability.Scope, last)
			break
		}
		page.Events = append(page.Events, event)
		last = event.Sequence
	}
	return page, nil
}

// encodeCursor seals tenant scope, sequence and expiry into URL-safe continuation state.
func (service *AuditService) encodeCursor(scope TenantScope, sequence uint64) string {
	expires := service.clock.Now().Add(service.cursorTTL).UTC().Format(time.RFC3339Nano)
	payload := strings.Join([]string{scope.OrganizationID, scope.ProjectID, scope.Digest, fmt.Sprint(sequence), expires}, "\x00")
	return base64.RawURLEncoding.EncodeToString([]byte(payload + "\x00" + digestStrings(payload)))
}

// decodeCursor rejects cross-scope, malformed, expired and integrity-altered continuation state.
func (service *AuditService) decodeCursor(cursor string, scope TenantScope) (uint64, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	parts := strings.Split(string(raw), "\x00")
	if err != nil || len(parts) != 6 || parts[0] != scope.OrganizationID || parts[1] != scope.ProjectID || parts[2] != scope.Digest || digestStrings(strings.Join(parts[:5], "\x00")) != parts[5] {
		return 0, failure(CategoryInvalid, "audit_cursor", nil)
	}
	sequence, sequenceErr := strconv.ParseUint(parts[3], 10, 64)
	expires, expiryErr := time.Parse(time.RFC3339Nano, parts[4])
	if sequenceErr != nil || expiryErr != nil || !service.clock.Now().Before(expires) {
		return 0, failure(CategoryInvalid, "audit_cursor", nil)
	}
	return sequence, nil
}
