package cleanup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
)

// durableSnapshotReceipt is one immutable versioned state publication referenced by a small CAS head.
type durableSnapshotReceipt struct {
	Kind, PreviousDigest string
	Version              uint64
	Payload              json.RawMessage
	Digest               string
}

// durableSnapshotHead selects exactly one immutable receipt after its file and directory are synced.
type durableSnapshotHead struct {
	Kind, Receipt, ReceiptDigest, Digest string
	Version                              uint64
}

// durableSnapshotAnchor distinguishes a never-published store from loss or rollback of a previously selected head.
type durableSnapshotAnchor struct {
	Kind, ReceiptDigest, Digest string
	Version                     uint64
	Published                   bool
}

// durableSnapshotState describes the currently committed payload and its monotonic durable version.
type durableSnapshotState struct {
	Payload       []byte
	ReceiptDigest string
	Version       uint64
}

// durableSequence gives temporary files process-local uniqueness without participating in authority.
var durableSequence atomic.Uint64

// ensureDurableParent creates only the private authority directory required for its lock, receipts and head.
func ensureDurableParent(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0o700)
}

// loadDurableSnapshot validates the anchor-selected chain and rolls a missing or older head forward under the caller's file lock.
func loadDurableSnapshot(path, kind string) (durableSnapshotState, error) {
	anchor, anchorExists, anchorErr := readDurableAnchor(path, kind)
	if anchorErr != nil {
		return durableSnapshotState{}, anchorErr
	}
	headRaw, headErr := os.ReadFile(path + ".head.json")
	if errors.Is(headErr, os.ErrNotExist) {
		if anchorExists && anchor.Published {
			state, receiptErr := loadDurableReceiptChain(path, kind, anchor.Version, anchor.ReceiptDigest)
			if receiptErr != nil {
				return durableSnapshotState{}, receiptErr
			}
			if headErr = writeDurableHead(path, kind, state.Version, state.ReceiptDigest); headErr != nil {
				return durableSnapshotState{}, headErr
			}
			return state, nil
		}
		if !anchorExists {
			hasReceipts, receiptErr := durableReceiptsExist(path)
			if receiptErr != nil {
				return durableSnapshotState{}, receiptErr
			}
			if hasReceipts {
				return durableSnapshotState{}, &Error{Category: CategoryIntegrity, Operation: "durable_anchor_missing"}
			}
			if anchorErr = writeDurableAnchor(path, durableSnapshotAnchor{Kind: kind}); anchorErr != nil {
				return durableSnapshotState{}, anchorErr
			}
		}
		legacy, legacyErr := os.ReadFile(path)
		if errors.Is(legacyErr, os.ErrNotExist) {
			return durableSnapshotState{}, nil
		}
		if legacyErr != nil {
			return durableSnapshotState{}, legacyErr
		}
		return durableSnapshotState{Payload: legacy}, nil
	}
	if headErr != nil {
		return durableSnapshotState{}, headErr
	}
	if !anchorExists || !anchor.Published {
		return durableSnapshotState{}, &Error{Category: CategoryIntegrity, Operation: "durable_anchor_missing"}
	}
	head := durableSnapshotHead{}
	if decodeDurableJSON(headRaw, &head) != nil || head.Kind != kind || head.Version == 0 || head.Receipt == "" || !digestPattern.MatchString(head.ReceiptDigest) {
		return durableSnapshotState{}, &Error{Category: CategoryIntegrity, Operation: "durable_head"}
	}
	candidateHead := head
	candidateHead.Digest = ""
	if head.Digest != digestOf(candidateHead) || head.Receipt != fmt.Sprintf("%020d-%s.json", head.Version, head.ReceiptDigest) {
		return durableSnapshotState{}, &Error{Category: CategoryIntegrity, Operation: "durable_head"}
	}
	if head.Version > anchor.Version || head.Version == anchor.Version && head.ReceiptDigest != anchor.ReceiptDigest {
		return durableSnapshotState{}, &Error{Category: CategoryIntegrity, Operation: "durable_head_conflict"}
	}
	state, receiptErr := loadDurableReceiptChain(path, kind, anchor.Version, anchor.ReceiptDigest)
	if receiptErr != nil {
		return durableSnapshotState{}, receiptErr
	}
	if head.Version < anchor.Version {
		if headErr = writeDurableHead(path, kind, state.Version, state.ReceiptDigest); headErr != nil {
			return durableSnapshotState{}, headErr
		}
	}
	return state, nil
}

// loadDurableReceiptChain verifies the exact anchor-selected receipt, every predecessor and the empty genesis link.
func loadDurableReceiptChain(path, kind string, selectedVersion uint64, selectedDigest string) (durableSnapshotState, error) {
	if selectedVersion == 0 || !digestPattern.MatchString(selectedDigest) {
		return durableSnapshotState{}, &Error{Category: CategoryIntegrity, Operation: "durable_anchor_selection"}
	}
	selectedName := fmt.Sprintf("%020d-%s.json", selectedVersion, selectedDigest)
	receiptRaw, err := os.ReadFile(filepath.Join(path+".receipts", selectedName))
	if err != nil {
		return durableSnapshotState{}, err
	}
	receipt := durableSnapshotReceipt{}
	if decodeDurableJSON(receiptRaw, &receipt) != nil || receipt.Kind != kind || receipt.Version != selectedVersion || receipt.Digest != selectedDigest {
		return durableSnapshotState{}, &Error{Category: CategoryIntegrity, Operation: "durable_receipt"}
	}
	candidateReceipt := receipt
	candidateReceipt.Digest = ""
	if receipt.Digest != digestOf(candidateReceipt) {
		return durableSnapshotState{}, &Error{Category: CategoryIntegrity, Operation: "durable_receipt"}
	}
	previous := receipt.PreviousDigest
	for version := receipt.Version - 1; version > 0; version-- {
		if !digestPattern.MatchString(previous) {
			return durableSnapshotState{}, &Error{Category: CategoryIntegrity, Operation: "durable_chain"}
		}
		priorName := fmt.Sprintf("%020d-%s.json", version, previous)
		priorRaw, readErr := os.ReadFile(filepath.Join(path+".receipts", priorName))
		if readErr != nil {
			return durableSnapshotState{}, readErr
		}
		prior := durableSnapshotReceipt{}
		if decodeDurableJSON(priorRaw, &prior) != nil || prior.Kind != kind || prior.Version != version || prior.Digest != previous {
			return durableSnapshotState{}, &Error{Category: CategoryIntegrity, Operation: "durable_chain"}
		}
		candidatePrior := prior
		candidatePrior.Digest = ""
		if prior.Digest != digestOf(candidatePrior) {
			return durableSnapshotState{}, &Error{Category: CategoryIntegrity, Operation: "durable_chain"}
		}
		previous = prior.PreviousDigest
	}
	if previous != "" {
		return durableSnapshotState{}, &Error{Category: CategoryIntegrity, Operation: "durable_genesis"}
	}
	return durableSnapshotState{Payload: append([]byte(nil), receipt.Payload...), ReceiptDigest: receipt.Digest, Version: receipt.Version}, nil
}

// writeDurableHead reconstructs or advances only the head selected by a fully verified anchor and receipt chain.
func writeDurableHead(path, kind string, version uint64, receiptDigest string) error {
	head := durableSnapshotHead{Kind: kind, Version: version, Receipt: fmt.Sprintf("%020d-%s.json", version, receiptDigest), ReceiptDigest: receiptDigest}
	head.Digest = digestOf(head)
	raw, err := json.Marshal(head)
	if err != nil {
		return err
	}
	if err = writeSyncedReplace(path+".head.json", raw); err != nil {
		return err
	}
	return syncCleanupDirectory(filepath.Dir(path))
}

// readDurableAnchor validates the independent publication marker without inferring authority from receipts or legacy state.
func readDurableAnchor(path, kind string) (durableSnapshotAnchor, bool, error) {
	raw, err := os.ReadFile(path + ".anchor.json")
	if errors.Is(err, os.ErrNotExist) {
		return durableSnapshotAnchor{}, false, nil
	}
	if err != nil {
		return durableSnapshotAnchor{}, false, err
	}
	anchor := durableSnapshotAnchor{}
	if decodeDurableJSON(raw, &anchor) != nil || anchor.Kind != kind || anchor.Published != (anchor.Version > 0) || anchor.Published != digestPattern.MatchString(anchor.ReceiptDigest) || !anchor.Published && anchor.ReceiptDigest != "" {
		return durableSnapshotAnchor{}, false, &Error{Category: CategoryIntegrity, Operation: "durable_anchor"}
	}
	candidate := anchor
	candidate.Digest = ""
	if anchor.Digest != digestOf(candidate) {
		return durableSnapshotAnchor{}, false, &Error{Category: CategoryIntegrity, Operation: "durable_anchor"}
	}
	return anchor, true, nil
}

// decodeDurableJSON rejects null, unknown fields, duplicate object keys and trailing values before a wrapper can carry authority.
func decodeDurableJSON(raw []byte, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("empty durable wrapper")
	}
	duplicateDecoder := json.NewDecoder(bytes.NewReader(raw))
	if err := consumeUniqueJSONValue(duplicateDecoder); err != nil {
		return err
	}
	if token, err := duplicateDecoder.Token(); err != io.EOF || token != nil {
		return errors.New("trailing durable wrapper")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("trailing durable wrapper")
	}
	return nil
}

// consumeUniqueJSONValue walks one JSON value and rejects duplicate keys at every object depth, including the receipt payload.
func consumeUniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return keyErr
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("non-string durable key")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate durable key")
			}
			seen[key] = struct{}{}
			if valueErr := consumeUniqueJSONValue(decoder); valueErr != nil {
				return valueErr
			}
		}
	case '[':
		for decoder.More() {
			if valueErr := consumeUniqueJSONValue(decoder); valueErr != nil {
				return valueErr
			}
		}
	default:
		return errors.New("invalid durable delimiter")
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim(map[json.Delim]json.Delim{'{': '}', '[': ']'}[delimiter]) {
		return errors.New("invalid durable closing delimiter")
	}
	return nil
}

// writeDurableAnchor publishes and syncs the independent marker after validating its closed initialized-or-published state.
func writeDurableAnchor(path string, anchor durableSnapshotAnchor) error {
	anchor.Digest = ""
	if anchor.Kind == "" || anchor.Published != (anchor.Version > 0) || anchor.Published != digestPattern.MatchString(anchor.ReceiptDigest) || !anchor.Published && anchor.ReceiptDigest != "" {
		return &Error{Category: CategoryIntegrity, Operation: "durable_anchor"}
	}
	anchor.Digest = digestOf(anchor)
	raw, err := json.Marshal(anchor)
	if err != nil {
		return err
	}
	if err = writeSyncedReplace(path+".anchor.json", raw); err != nil {
		return err
	}
	return syncCleanupDirectory(filepath.Dir(path))
}

// durableReceiptsExist detects unanchored history while allowing receipts created after an initialized anchor but before head publication.
func durableReceiptsExist(path string) (bool, error) {
	entries, err := os.ReadDir(path + ".receipts")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return true, nil
		}
	}
	return false, nil
}

// publishDurableSnapshot syncs an immutable receipt, seals its rollback anchor, then atomically advances the selected head.
func publishDurableSnapshot(ctx context.Context, path, kind string, current durableSnapshotState, payload []byte, fault func(string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fault != nil {
		if err := fault("before_write"); err != nil {
			return err
		}
		if err := fault("before_receipt"); err != nil {
			return err
		}
	}
	anchor, anchorExists, err := readDurableAnchor(path, kind)
	if err != nil {
		return err
	}
	if !anchorExists {
		if current.Version != 0 {
			return &Error{Category: CategoryIntegrity, Operation: "durable_anchor_missing"}
		}
		if err = writeDurableAnchor(path, durableSnapshotAnchor{Kind: kind}); err != nil {
			return err
		}
	} else if anchor.Published && (anchor.Version > current.Version || anchor.Version == current.Version && anchor.ReceiptDigest != current.ReceiptDigest) {
		return &Error{Category: CategoryIntegrity, Operation: "durable_anchor_conflict"}
	}
	receipt := durableSnapshotReceipt{Kind: kind, Version: current.Version + 1, PreviousDigest: current.ReceiptDigest, Payload: append(json.RawMessage(nil), payload...)}
	receipt.Digest = digestOf(receipt)
	receiptRaw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	receiptsDir := path + ".receipts"
	if err = os.MkdirAll(receiptsDir, 0o700); err != nil {
		return err
	}
	receiptName := fmt.Sprintf("%020d-%s.json", receipt.Version, receipt.Digest)
	receiptPath := filepath.Join(receiptsDir, receiptName)
	if err = writeSyncedImmutable(receiptPath, receiptRaw); err != nil {
		return err
	}
	if err = syncCleanupDirectory(receiptsDir); err != nil {
		return err
	}
	if fault != nil {
		if err = fault("after_receipt"); err != nil {
			return err
		}
		if err = fault("before_publish"); err != nil {
			return err
		}
	}
	head := durableSnapshotHead{Kind: kind, Version: receipt.Version, Receipt: receiptName, ReceiptDigest: receipt.Digest}
	head.Digest = digestOf(head)
	headRaw, err := json.Marshal(head)
	if err != nil {
		return err
	}
	if err = writeDurableAnchor(path, durableSnapshotAnchor{Kind: kind, Version: head.Version, ReceiptDigest: head.ReceiptDigest, Published: true}); err != nil {
		return err
	}
	if fault != nil {
		if err = fault("after_anchor"); err != nil {
			return err
		}
		if err = fault("before_head"); err != nil {
			return err
		}
	}
	if err = writeSyncedReplace(path+".head.json", headRaw); err != nil {
		return err
	}
	if err = syncCleanupDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if fault != nil {
		if err = fault("after_head"); err != nil {
			return err
		}
		if err = fault("after_publish"); err != nil {
			return err
		}
	}
	return nil
}

// writeSyncedImmutable creates one receipt once and accepts only byte-identical retry after a lost response.
func writeSyncedImmutable(path string, payload []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		current, readErr := os.ReadFile(path)
		if readErr != nil || string(current) != string(payload) {
			return &Error{Category: CategoryConflict, Operation: "durable_receipt_redelivery", Cause: readErr}
		}
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = file.Write(payload); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

// writeSyncedReplace publishes one head through a synced temporary file and atomic rename.
func writeSyncedReplace(path string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := fmt.Sprintf("%s.tmp-%d-%d", path, os.Getpid(), durableSequence.Add(1))
	defer func() { _ = os.Remove(temporary) }()
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(payload); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
