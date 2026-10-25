package githubcontext

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
)

// LocalRecordObserver durably publishes the first content-free terminal record for each acquisition identity.
type LocalRecordObserver struct{ records string }

// OpenLocalRecordObserver creates the private append-only record namespace used by product composition.
func OpenLocalRecordObserver(root string) (*LocalRecordObserver, error) {
	if root == "" {
		return nil, &Error{Category: CategoryInvalid, Stage: "observer_root"}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, &Error{Category: CategoryInvalid, Stage: "observer_root", Cause: err}
	}
	records := filepath.Join(abs, "records")
	if err = os.MkdirAll(records, 0700); err != nil {
		return nil, &Error{Category: CategoryPersistence, Stage: "observer_open", Cause: err}
	}
	if err = os.Chmod(records, 0700); err != nil {
		return nil, &Error{Category: CategoryPersistence, Stage: "observer_permissions", Cause: err}
	}
	return &LocalRecordObserver{records: records}, nil
}

// Observe publishes one immutable record and treats a previously confirmed identity as exactly-once redelivery.
func (o *LocalRecordObserver) Observe(ctx context.Context, record Record) error {
	if ctx.Err() != nil {
		return contextError(ctx, "observer")
	}
	if o == nil || o.records == "" || VerifyRecord(record) != nil {
		return &Error{Category: CategoryInvalid, Stage: "observer_record"}
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return &Error{Category: CategoryInternal, Stage: "observer_encode", Cause: err}
	}
	name := record.ID + ".json"
	err = publishImmutable(ctx, o.records, name, raw)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return contextError(ctx, "observer_publish")
	}
	if !IsCategory(err, CategoryConflict) {
		return err
	}
	existing, readErr := o.Read(ctx, record.ID)
	if readErr != nil || existing.ID != record.ID {
		return &Error{Category: CategoryIntegrity, Stage: "observer_redelivery", Cause: readErr}
	}
	return nil
}

// Read returns one strictly decoded confirmed record for recovery and audit tests.
func (o *LocalRecordObserver) Read(ctx context.Context, id string) (Record, error) {
	if ctx.Err() != nil {
		return Record{}, contextError(ctx, "observer_read")
	}
	if o == nil || !digestPattern.MatchString(id) {
		return Record{}, &Error{Category: CategoryInvalid, Stage: "observer_id"}
	}
	raw, err := readBounded(filepath.Join(o.records, id+".json"), 1<<20)
	if ctx.Err() != nil {
		return Record{}, contextError(ctx, "observer_read")
	}
	if err != nil {
		return Record{}, err
	}
	var record Record
	if decodeStrict(raw, &record) != nil || VerifyRecord(record) != nil || record.ID != id {
		return Record{}, &Error{Category: CategoryIntegrity, Stage: "observer_record"}
	}
	return record, nil
}

// VerifyRecord validates the closed content-free terminal event vocabulary and canonical order.
func VerifyRecord(record Record) error {
	if record.Version != RecordVersion || !digestPattern.MatchString(record.ID) || !digestPattern.MatchString(record.Digest) || !digestPattern.MatchString(record.AcquisitionDigest) || record.ID != terminalRecordID(record) || record.Digest != terminalRecordDigest(record) || record.RepositoryID < 0 || record.PullRequestNumber < 0 || record.Attempts < 0 || record.Duration < 0 || record.OmissionReasons == nil || !validCounts(record.Counts) {
		return &Error{Category: CategoryIntegrity, Stage: "record_semantics"}
	}
	if record.RunID != "" && !safeID.MatchString(record.RunID) || record.HeadOID != "" && !fullOID.MatchString(record.HeadOID) || record.BaseOID != "" && !fullOID.MatchString(record.BaseOID) || !optionalDigest(record.PolicyDigest) || !optionalDigest(record.ObservationDigest) || !optionalDigest(record.ContextDigest) {
		return &Error{Category: CategoryIntegrity, Stage: "record_identity"}
	}
	previous := OmissionReason("")
	for _, summary := range record.OmissionReasons {
		if !validOmissionReason(summary.Reason) || summary.Count <= 0 || summary.Reason <= previous {
			return &Error{Category: CategoryIntegrity, Stage: "record_omissions"}
		}
		previous = summary.Reason
	}
	if record.Outcome == "success" {
		if record.Category != "" || record.RepositoryID <= 0 || record.PullRequestNumber <= 0 || !safeID.MatchString(record.RunID) || !fullOID.MatchString(record.HeadOID) || !fullOID.MatchString(record.BaseOID) || !digestPattern.MatchString(record.PolicyDigest) || !digestPattern.MatchString(record.ObservationDigest) || !digestPattern.MatchString(record.ContextDigest) || !validCompleteness(record.Completeness) {
			return &Error{Category: CategoryIntegrity, Stage: "record_success"}
		}
		return nil
	}
	if !validCategory(record.Category) || record.Outcome != string(record.Category) && record.Outcome != "error" {
		return &Error{Category: CategoryIntegrity, Stage: "record_outcome"}
	}
	return nil
}

// optionalDigest accepts an absent failure dimension or one complete SHA-256 identity.
func optionalDigest(value string) bool { return value == "" || digestPattern.MatchString(value) }

// validCounts rejects negative cardinalities in records before they enter the audit journal.
func validCounts(value Counts) bool {
	return value.Pages >= 0 && value.FilesObserved >= 0 && value.FilesProjected >= 0 && value.Hunks >= 0 && value.Lines >= 0 && value.Constraints >= 0 && value.Contracts >= 0 && value.Omissions >= 0
}
