// Package local persists project configuration as checksummed atomic snapshots.
package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	pc "github.com/asierhr/featureProof/internal/projectconfig"
)

// Store is a durable single-backend implementation safe across processes.
type Store struct{ root string }

// New creates a store rooted at an explicit private directory.
func New(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) {
		return nil, &pc.Error{Category: pc.CategoryInvalid, Operation: "local.new"}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, &pc.Error{Category: pc.CategoryUnavailable, Operation: "local.new"}
	}
	return &Store{root: filepath.Clean(root)}, nil
}

// state is the complete atomic projection for one validated project key.
type state struct {
	NextVersion    uint64                                `json:"next_version"`
	NextGeneration uint64                                `json:"next_generation"`
	NextSequence   uint64                                `json:"next_sequence"`
	Versions       map[uint64]pc.ConfigurationVersion    `json:"versions"`
	Effective      map[uint64]pc.EffectiveConfiguration  `json:"effective_by_generation"`
	Activations    map[uint64]pc.Activation              `json:"activations"`
	Head           *pc.ConfigurationHead                 `json:"head,omitempty"`
	Receipts       map[string]pc.PublishReceipt          `json:"receipts"`
	Events         []pc.Event                            `json:"events"`
	Bindings       map[string]pc.RunConfigurationBinding `json:"bindings"`
}

// envelope detects truncation or corruption before state is exposed.
type envelope struct {
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

// Publish commits version, effective snapshot, activation, head, event and receipt together.
func (store *Store) Publish(ctx context.Context, command pc.PublishCommand) (pc.PublishReceipt, error) {
	key, validationErr := pc.ValidatePublishCommand(command)
	if validationErr != nil {
		return pc.PublishReceipt{}, validationErr
	}
	var result pc.PublishReceipt
	err := store.update(ctx, key, func(current *state) error {
		if receipt, ok := current.Receipts[command.OperationID]; ok {
			if receipt.CommandDigest != command.CommandDigest {
				return conflict("local.publish.idempotency")
			}
			result = receipt
			return nil
		}
		if generation(current) != command.ExpectedGeneration {
			return stale("local.publish")
		}
		if current.NextVersion >= pc.MaxDurableCounter || current.NextGeneration >= pc.MaxDurableCounter || current.NextSequence >= pc.MaxDurableCounter {
			return exhausted("local.publish")
		}
		versionNumber := current.NextVersion + 1
		generationNumber := current.NextGeneration + 1
		version := pc.ConfigurationVersion{ProjectKey: key, Version: versionNumber, Schema: command.Document.Document.Schema, Canonical: append([]byte(nil), command.Document.Bytes...), DocumentDigest: command.Document.Digest, PolicyDigest: command.Effective.PolicyDigest, CatalogDigest: command.Effective.CatalogDigest, Actor: command.Actor, CreatedAt: command.CreatedAt}
		activation := newActivation(key, generationNumber, versionNumber, command.Document.Digest, current.NextGeneration, "publish", command.Actor, command.CreatedAt)
		current.Versions[versionNumber] = version
		current.Effective[generationNumber] = command.Effective.Clone()
		current.Activations[generationNumber] = activation
		current.Head = &pc.ConfigurationHead{ProjectKey: key, Activation: activation}
		current.NextVersion, current.NextGeneration = versionNumber, generationNumber
		current.NextSequence++
		current.Events = append(current.Events, pc.Event{Sequence: current.NextSequence, Action: "published", Actor: command.Actor, ProjectKey: key, Version: versionNumber, Generation: generationNumber, Digest: activation.Digest, Reason: "publish", CreatedAt: command.CreatedAt})
		result = pc.PublishReceipt{OperationID: command.OperationID, CommandDigest: command.CommandDigest, Version: versionNumber, Generation: generationNumber, HeadDigest: activation.Digest}
		current.Receipts[command.OperationID] = result
		return nil
	})
	return result, err
}

// Rollback creates an activation while retaining the historical version bytes.
func (store *Store) Rollback(ctx context.Context, command pc.RollbackCommand) (pc.PublishReceipt, error) {
	if err := pc.ValidateRollbackCommand(command); err != nil {
		return pc.PublishReceipt{}, err
	}
	var result pc.PublishReceipt
	err := store.update(ctx, command.ProjectKey, func(current *state) error {
		if receipt, ok := current.Receipts[command.OperationID]; ok {
			if receipt.CommandDigest != command.CommandDigest {
				return conflict("local.rollback.idempotency")
			}
			result = receipt
			return nil
		}
		version, ok := current.Versions[command.Version]
		if !ok {
			return notFound("local.rollback.version")
		}
		if generation(current) != command.ExpectedGeneration {
			return stale("local.rollback")
		}
		if current.NextGeneration >= pc.MaxDurableCounter || current.NextSequence >= pc.MaxDurableCounter {
			return exhausted("local.rollback")
		}
		generationNumber := current.NextGeneration + 1
		activation := newActivation(command.ProjectKey, generationNumber, command.Version, version.DocumentDigest, current.NextGeneration, "rollback", command.Actor, command.CreatedAt)
		current.Effective[generationNumber] = command.Effective.Clone()
		current.Activations[generationNumber] = activation
		current.Head = &pc.ConfigurationHead{ProjectKey: command.ProjectKey, Activation: activation}
		current.NextGeneration = generationNumber
		current.NextSequence++
		current.Events = append(current.Events, pc.Event{Sequence: current.NextSequence, Action: "activated", Actor: command.Actor, ProjectKey: command.ProjectKey, Version: command.Version, Generation: generationNumber, Digest: activation.Digest, Reason: "rollback", CreatedAt: command.CreatedAt})
		result = pc.PublishReceipt{OperationID: command.OperationID, CommandDigest: command.CommandDigest, Version: command.Version, Generation: generationNumber, HeadDigest: activation.Digest}
		current.Receipts[command.OperationID] = result
		return nil
	})
	return result, err
}

// Head loads the authoritative active projection under a shared snapshot lock.
func (store *Store) Head(ctx context.Context, key pc.ProjectKey) (pc.ConfigurationHead, error) {
	var result pc.ConfigurationHead
	err := store.read(ctx, key, func(s *state) error {
		if s.Head == nil {
			return notFound("local.head")
		}
		result = *s.Head
		return nil
	})
	return result, err
}

// Version loads defensive immutable version and effective copies.
func (store *Store) Version(ctx context.Context, key pc.ProjectKey, number uint64) (pc.ConfigurationVersion, pc.EffectiveConfiguration, error) {
	var version pc.ConfigurationVersion
	var effective pc.EffectiveConfiguration
	err := store.read(ctx, key, func(s *state) error {
		var ok bool
		version, ok = s.Versions[number]
		if !ok {
			return notFound("local.version")
		}
		for generation, activation := range s.Activations {
			if activation.Version == number && activation.Reason == "publish" {
				effective = s.Effective[generation].Clone()
				break
			}
		}
		version = version.Clone()
		return nil
	})
	return version, effective, err
}

// Effective loads the immutable snapshot created for one activation generation.
func (store *Store) Effective(ctx context.Context, key pc.ProjectKey, generation uint64) (pc.EffectiveConfiguration, error) {
	var result pc.EffectiveConfiguration
	err := store.read(ctx, key, func(s *state) error {
		var ok bool
		result, ok = s.Effective[generation]
		if !ok {
			return notFound("local.effective")
		}
		result = result.Clone()
		return nil
	})
	return result, err
}

// History returns stable newest-first events after an exclusive sequence cursor.
func (store *Store) History(ctx context.Context, key pc.ProjectKey, before uint64, limit int) (pc.HistoryPage, error) {
	var page pc.HistoryPage
	if limit < 1 || limit > 100 {
		return page, &pc.Error{Category: pc.CategoryInvalid, Operation: "local.history"}
	}
	err := store.read(ctx, key, func(s *state) error {
		events := append([]pc.Event(nil), s.Events...)
		sort.Slice(events, func(i, j int) bool { return events[i].Sequence > events[j].Sequence })
		for _, event := range events {
			if before > 0 && event.Sequence >= before {
				continue
			}
			if len(page.Items) == limit {
				page.NextCursor = fmt.Sprint(event.Sequence + 1)
				break
			}
			page.Items = append(page.Items, pc.HistoryItem{Sequence: event.Sequence, Action: event.Action, Version: event.Version, Generation: event.Generation, Digest: event.Digest})
		}
		return nil
	})
	return page, err
}

// Bind atomically freezes the active version and rejects divergent run reuse.
func (store *Store) Bind(ctx context.Context, command pc.BindCommand) (pc.RunConfigurationBinding, error) {
	if err := pc.ValidateBindCommand(command); err != nil {
		return pc.RunConfigurationBinding{}, err
	}
	var result pc.RunConfigurationBinding
	err := store.update(ctx, command.ProjectKey, func(s *state) error {
		if existing, ok := s.Bindings[command.RunID]; ok {
			if err := pc.ValidateRunBinding(existing); err != nil {
				return err
			}
			if command.Candidate != nil && existing.BindingDigest != command.Candidate.Binding.BindingDigest {
				return conflict("local.bind.idempotency")
			}
			result = existing.Clone()
			return nil
		}
		if s.Head == nil {
			return notFound("local.bind.head")
		}
		a := s.Head.Activation
		e, ok := s.Effective[a.Generation]
		if !ok {
			return integrity("local.bind.effective")
		}
		if command.Candidate != nil {
			if command.Candidate.Binding.ActivationGeneration != a.Generation || command.Candidate.Binding.ActivationDigest != a.Digest {
				return stale("local.bind")
			}
			result = command.Candidate.Binding.Clone()
		} else {
			result = pc.RunConfigurationBinding{RunID: command.RunID, ProjectKey: command.ProjectKey, Version: a.Version, VersionDigest: a.VersionDigest, ActivationGeneration: a.Generation, ActivationDigest: a.Digest, PolicyVersion: e.PolicyVersion, PolicyDigest: e.PolicyDigest, CatalogDigest: e.CatalogDigest, EffectiveDigest: e.Digest, Effective: e.Clone()}
			var sealErr error
			result, sealErr = pc.SealRunBinding(result)
			if sealErr != nil {
				return sealErr
			}
		}
		s.Bindings[command.RunID] = result.Clone()
		return nil
	})
	return result, err
}

// Binding reloads one immutable run binding without consulting the active head.
func (store *Store) Binding(ctx context.Context, key pc.ProjectKey, runID string) (pc.RunConfigurationBinding, error) {
	var result pc.RunConfigurationBinding
	err := store.read(ctx, key, func(s *state) error {
		var ok bool
		result, ok = s.Bindings[runID]
		if !ok {
			return notFound("local.binding")
		}
		result = result.Clone()
		if err := pc.ValidateRunBinding(result); err != nil {
			return err
		}
		return nil
	})
	return result, err
}

// update serializes a read-modify-write cycle under the cross-process lock.
func (store *Store) update(ctx context.Context, key pc.ProjectKey, fn func(*state) error) error {
	if err := key.Validate(); err != nil {
		return err
	}
	unlock, err := acquireLock(ctx, store.lockPath(key))
	if err != nil {
		return mapContext(ctx, "local.lock")
	}
	defer unlock()
	current, err := store.load(key)
	if err != nil {
		return err
	}
	if err = fn(&current); err != nil {
		return err
	}
	return store.save(key, current)
}

// read observes one checksum-verified snapshot under the authority lock.
func (store *Store) read(ctx context.Context, key pc.ProjectKey, fn func(*state) error) error {
	if err := key.Validate(); err != nil {
		return err
	}
	unlock, err := acquireLock(ctx, store.lockPath(key))
	if err != nil {
		return mapContext(ctx, "local.lock")
	}
	defer unlock()
	current, err := store.load(key)
	if err != nil {
		return err
	}
	return fn(&current)
}

// lockPath derives a contained filename only from validated identifiers.
func (store *Store) lockPath(key pc.ProjectKey) string {
	return filepath.Join(store.root, key.OrganizationID+"--"+key.ProjectID+"--"+key.RepositoryID+".lock")
}

// dataPath derives the authoritative snapshot path for one project scope.
func (store *Store) dataPath(key pc.ProjectKey) string {
	return filepath.Join(store.root, key.OrganizationID+"--"+key.ProjectID+"--"+key.RepositoryID+".json")
}

// load verifies the envelope before decoding durable projections.
func (store *Store) load(key pc.ProjectKey) (state, error) {
	initial := newState()
	raw, err := os.ReadFile(store.dataPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return initial, nil
	}
	if err != nil {
		return state{}, unavailable("local.load")
	}
	var wrapped envelope
	if json.Unmarshal(raw, &wrapped) != nil || digest(wrapped.Payload) != wrapped.Checksum {
		return state{}, integrity("local.load")
	}
	if json.Unmarshal(wrapped.Payload, &initial) != nil || initial.Versions == nil || initial.Bindings == nil {
		return state{}, integrity("local.load")
	}
	return initial, nil
}

// save fsyncs a private temporary file before atomic replacement.
func (store *Store) save(key pc.ProjectKey, current state) error {
	payload, err := json.Marshal(current)
	if err != nil {
		return integrity("local.save")
	}
	wrapped, _ := json.Marshal(envelope{Payload: payload, Checksum: digest(payload)})
	path := store.dataPath(key)
	temporary, err := os.CreateTemp(store.root, ".project-config-*.tmp")
	if err != nil {
		return unavailable("local.save")
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(wrapped)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err == nil {
		err = syncDirectory(store.root)
	}
	if err != nil {
		return unavailable("local.save")
	}
	return nil
}

// newState initializes every map required by atomic operations.
func newState() state {
	return state{Versions: map[uint64]pc.ConfigurationVersion{}, Effective: map[uint64]pc.EffectiveConfiguration{}, Activations: map[uint64]pc.Activation{}, Receipts: map[string]pc.PublishReceipt{}, Bindings: map[string]pc.RunConfigurationBinding{}}
}

// generation reads the CAS value represented by the current head.
func generation(s *state) uint64 {
	if s.Head == nil {
		return 0
	}
	return s.Head.Activation.Generation
}

// newActivation seals every causal field into a deterministic digest.
func newActivation(key pc.ProjectKey, generation, version uint64, versionDigest string, previous uint64, reason, actor string, at time.Time) pc.Activation {
	value, _ := pc.SealActivation(pc.Activation{ProjectKey: key, Generation: generation, Version: version, VersionDigest: versionDigest, PreviousGeneration: previous, Reason: reason, Actor: actor, CreatedAt: at})
	return value
}

// digest returns the canonical identity used by integrity envelopes.
func digest(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }

// mapContext retains cancellation categories while hiding filesystem details.
func mapContext(ctx context.Context, op string) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return &pc.Error{Category: pc.CategoryCancelled, Operation: op, Cause: context.Canceled}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &pc.Error{Category: pc.CategoryDeadline, Operation: op, Cause: context.DeadlineExceeded}
	}
	return unavailable(op)
}

// conflict maps divergent idempotency reuse without reflecting payloads.
func conflict(op string) error { return &pc.Error{Category: pc.CategoryConflict, Operation: op} }

// stale maps optimistic concurrency failure to the shared contract.
func stale(op string) error { return &pc.Error{Category: pc.CategoryStaleHead, Operation: op} }

// notFound maps authorized local absence to the shared contract.
func notFound(op string) error { return &pc.Error{Category: pc.CategoryNotFound, Operation: op} }

// integrity maps corrupt durable state without recovery fallback.
func integrity(op string) error { return &pc.Error{Category: pc.CategoryIntegrity, Operation: op} }

// unavailable hides filesystem-specific operational details.
func unavailable(op string) error { return &pc.Error{Category: pc.CategoryUnavailable, Operation: op} }

// exhausted prevents monotonic counters from wrapping.
func exhausted(op string) error { return &pc.Error{Category: pc.CategoryExhausted, Operation: op} }
