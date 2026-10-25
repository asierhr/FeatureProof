package githubcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// LocalStore publishes immutable objects before one atomic content-free idempotency receipt.
type LocalStore struct{ root, contexts, bindings, receipts string }

// receipt is the only addressable association from a caller key to confirmed immutable objects.
type receipt struct{ Version, Fingerprint, ContextDigest, BindingDigest string }

// OpenLocalStore creates private fixed namespaces without accepting paths from acquisition input.
func OpenLocalStore(root string) (*LocalStore, error) {
	if root == "" {
		return nil, &Error{Category: CategoryInvalid, Stage: "store_root"}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, &Error{Category: CategoryInvalid, Stage: "store_root", Cause: err}
	}
	store := &LocalStore{root: abs, contexts: filepath.Join(abs, "contexts"), bindings: filepath.Join(abs, "bindings"), receipts: filepath.Join(abs, "receipts")}
	for _, directory := range []string{store.root, store.contexts, store.bindings, store.receipts} {
		if err = os.MkdirAll(directory, 0700); err != nil {
			return nil, &Error{Category: CategoryPersistence, Stage: "store_open", Cause: err}
		}
		if err = os.Chmod(directory, 0700); err != nil {
			return nil, &Error{Category: CategoryPersistence, Stage: "store_permissions", Cause: err}
		}
	}
	return store, nil
}

// Load resolves only a fully published receipt and revalidates both immutable object digests.
func (s *LocalStore) Load(ctx context.Context, key string) (string, Context, Binding, bool, error) {
	if ctx.Err() != nil {
		return "", Context{}, Binding{}, false, contextError(ctx, "store_load")
	}
	if !safeID.MatchString(key) {
		return "", Context{}, Binding{}, false, &Error{Category: CategoryInvalid, Stage: "store_key"}
	}
	raw, err := readBounded(filepath.Join(s.receipts, digestBytes([]byte(key))+".json"), 1<<20)
	if ctx.Err() != nil {
		return "", Context{}, Binding{}, false, contextError(ctx, "store_load")
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", Context{}, Binding{}, false, nil
	}
	if err != nil {
		return "", Context{}, Binding{}, false, err
	}
	var value receipt
	if decodeStrict(raw, &value) != nil || value.Version != "github-context-receipt/v1" || !digestPattern.MatchString(value.Fingerprint) || !digestPattern.MatchString(value.ContextDigest) || !digestPattern.MatchString(value.BindingDigest) {
		return "", Context{}, Binding{}, false, &Error{Category: CategoryIntegrity, Stage: "receipt"}
	}
	contextRaw, err := readBounded(filepath.Join(s.contexts, value.ContextDigest+".json"), 8<<20)
	if ctx.Err() != nil {
		return "", Context{}, Binding{}, false, contextError(ctx, "store_load")
	}
	if err != nil {
		return "", Context{}, Binding{}, false, err
	}
	snapshot, err := DecodeContext(contextRaw)
	if err != nil || snapshot.Digest != value.ContextDigest {
		return "", Context{}, Binding{}, false, &Error{Category: CategoryIntegrity, Stage: "context_object", Cause: err}
	}
	bindingRaw, err := readBounded(filepath.Join(s.bindings, value.BindingDigest+".json"), 1<<20)
	if ctx.Err() != nil {
		return "", Context{}, Binding{}, false, contextError(ctx, "store_load")
	}
	if err != nil {
		return "", Context{}, Binding{}, false, err
	}
	var binding Binding
	if decodeStrict(bindingRaw, &binding) != nil || VerifyBinding(binding) != nil || binding.Digest != value.BindingDigest || binding.ContextDigest != snapshot.Digest || binding.RunID != snapshot.RunID || binding.ObservationDigest != snapshot.ObservationDigest || binding.PolicyDigest != snapshot.PolicyDigest || binding.CatalogDigest != snapshot.CatalogDigest || binding.Authority != snapshot.Authority {
		return "", Context{}, Binding{}, false, &Error{Category: CategoryIntegrity, Stage: "binding_object"}
	}
	return value.Fingerprint, snapshot, binding, true, nil
}

// Commit publishes content-addressed objects and then atomically claims the idempotency receipt.
func (s *LocalStore) Commit(ctx context.Context, key, fingerprint string, snapshot Context, binding Binding) error {
	if ctx.Err() != nil {
		return contextError(ctx, "store_commit")
	}
	if !safeID.MatchString(key) || !digestPattern.MatchString(fingerprint) || VerifyContext(snapshot) != nil || VerifyBinding(binding) != nil || binding.ContextDigest != snapshot.Digest || binding.RunID != snapshot.RunID || binding.ObservationDigest != snapshot.ObservationDigest || binding.PolicyDigest != snapshot.PolicyDigest || binding.CatalogDigest != snapshot.CatalogDigest || binding.Authority != snapshot.Authority {
		return &Error{Category: CategoryInvalid, Stage: "store_commit"}
	}
	contextRaw, _ := json.Marshal(snapshot)
	bindingRaw, _ := json.Marshal(binding)
	if err := publishImmutable(ctx, s.contexts, snapshot.Digest+".json", contextRaw); err != nil {
		if ctx.Err() != nil {
			return contextError(ctx, "store_commit")
		}
		return err
	}
	if err := publishImmutable(ctx, s.bindings, binding.Digest+".json", bindingRaw); err != nil {
		if ctx.Err() != nil {
			return contextError(ctx, "store_commit")
		}
		return err
	}
	value := receipt{Version: "github-context-receipt/v1", Fingerprint: fingerprint, ContextDigest: snapshot.Digest, BindingDigest: binding.Digest}
	receiptRaw, _ := json.Marshal(value)
	receiptName := digestBytes([]byte(key)) + ".json"
	if err := publishImmutable(ctx, s.receipts, receiptName, receiptRaw); err != nil {
		if ctx.Err() != nil {
			return contextError(ctx, "store_commit")
		}
		return err
	}
	storedFingerprint, storedContext, storedBinding, found, err := s.Load(ctx, key)
	if ctx.Err() != nil {
		return contextError(ctx, "store_commit")
	}
	if err != nil {
		if ctx.Err() != nil {
			return contextError(ctx, "store_stage")
		}
		return err
	}
	if !found || storedFingerprint != fingerprint || storedContext.Digest != snapshot.Digest || storedBinding.Digest != binding.Digest {
		return &Error{Category: CategoryConflict, Stage: "store_commit"}
	}
	return nil
}

// publishImmutable uses an exclusive hard-link claim so competing writers cannot replace confirmed bytes.
func publishImmutable(ctx context.Context, directory, name string, raw []byte) error {
	if ctx.Err() != nil {
		return contextError(ctx, "store_publish")
	}
	temporary, err := os.CreateTemp(directory, ".candidate-*")
	if err != nil {
		return &Error{Category: CategoryPersistence, Stage: "store_stage", Cause: err}
	}
	if ctx.Err() != nil {
		_ = temporary.Close()
		return contextError(ctx, "store_stage")
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.Write(raw)
	}
	if err == nil && ctx.Err() != nil {
		err = contextError(ctx, "store_stage")
	}
	if err == nil {
		err = temporary.Sync()
	}
	if err == nil && ctx.Err() != nil {
		err = contextError(ctx, "store_stage")
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		if ctx.Err() != nil {
			return contextError(ctx, "store_stage")
		}
		return &Error{Category: CategoryPersistence, Stage: "store_stage", Cause: err}
	}
	if ctx.Err() != nil {
		return contextError(ctx, "store_publish")
	}
	target := filepath.Join(directory, name)
	if err = os.Link(temporaryName, target); err != nil {
		if ctx.Err() != nil {
			return contextError(ctx, "store_publish")
		}
		if !errors.Is(err, os.ErrExist) {
			return &Error{Category: CategoryPersistence, Stage: "store_publish", Cause: err}
		}
		existing, readErr := readBounded(target, int64(len(raw))+1)
		if ctx.Err() != nil {
			return contextError(ctx, "store_publish")
		}
		if readErr != nil || !bytes.Equal(existing, raw) {
			return &Error{Category: CategoryConflict, Stage: "store_publish", Cause: readErr}
		}
	}
	if err = syncDirectory(directory); err != nil {
		if ctx.Err() != nil {
			return contextError(ctx, "store_sync")
		}
		return &Error{Category: CategoryPersistence, Stage: "store_sync", Cause: err}
	}
	if ctx.Err() != nil {
		return contextError(ctx, "store_sync")
	}
	return nil
}

// readBounded rejects missing, oversized, non-regular or unreadable store objects.
func readBounded(name string, maximum int64) ([]byte, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum {
		return nil, &Error{Category: CategoryIntegrity, Stage: "store_object", Cause: err}
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(raw)) > maximum {
		return nil, &Error{Category: CategoryIntegrity, Stage: "store_object", Cause: err}
	}
	return raw, nil
}

// decodeStrict prevents unknown fields or trailing bytes from changing durable semantics.
func decodeStrict(raw []byte, target any) error {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing json")
	}
	return nil
}
