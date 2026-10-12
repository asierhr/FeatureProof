package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ErrAlreadyExists prevents confirmed evidence identifiers from being rewritten.
var ErrAlreadyExists = errors.New("evidence already exists")

// ErrIntegrity reports that persisted evidence no longer matches its recorded hash.
var ErrIntegrity = errors.New("evidence integrity mismatch")

// ErrUntreated prevents persistence from becoming the first place raw payload is observed.
var ErrUntreated = errors.New("evidence was not fully sanitized")

// Record is the immutable, fully sanitized fact produced by one tool execution attempt.
type Record struct {
	ID, RunID, PlanID, StepID, ToolCallID, ToolName string
	Step, Attempt                                   int
	StartedAt                                       time.Time
	Duration                                        time.Duration
	Input, Output                                   any
	InputTruncations, OutputTruncations             []Truncation
	ErrorClass                                      string
	IntegrityHash                                   string
	treated                                         bool
	seal                                            string
}

// NewRecord seals fully transformed roots and metadata into a record eligible for persistence.
func NewRecord(base Record, input, output SanitizedValue) (Record, error) {
	if !input.complete || !output.complete {
		return Record{}, ErrUntreated
	}
	var err error
	base.Input, err = cloneTreated(input.value)
	if err != nil {
		return Record{}, ErrUntreated
	}
	base.Output, err = cloneTreated(output.value)
	if err != nil {
		return Record{}, ErrUntreated
	}
	base.InputTruncations = append([]Truncation(nil), input.truncations...)
	base.OutputTruncations = append([]Truncation(nil), output.truncations...)
	base.treated = true
	base.seal, err = recordSeal(base)
	if err != nil {
		return Record{}, ErrUntreated
	}
	return base, nil
}

// cloneTreated transfers ownership without retaining aliases to sanitizer or caller collections.
func cloneTreated(value any) (any, error) {
	switch current := value.(type) {
	case nil, string, bool, json.Number, float32, float64,
		int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return current, nil
	case map[string]any:
		out := make(map[string]any, len(current))
		for key, child := range current {
			copy, err := cloneTreated(child)
			if err != nil {
				return nil, err
			}
			out[key] = copy
		}
		return out, nil
	case []any:
		out := make([]any, len(current))
		for i, child := range current {
			copy, err := cloneTreated(child)
			if err != nil {
				return nil, err
			}
			out[i] = copy
		}
		return out, nil
	default:
		return nil, ErrUntreated
	}
}

// recordSeal fingerprints treated content so post-construction mutation invalidates persistence eligibility.
func recordSeal(record Record) (string, error) {
	record.IntegrityHash = ""
	record.seal = ""
	raw, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// validTreatment verifies that a record still matches the sanitizer-sealed snapshot.
func (r Record) validTreatment() bool {
	if !r.treated {
		return false
	}
	seal, err := recordSeal(r)
	return err == nil && seal == r.seal
}

// Store is the persistence boundary the harness uses before trusting an observation.
type Store interface {
	Save(context.Context, Record) error
	Get(context.Context, string) (Record, error)
}

// FileStore persists immutable evidence records under a run-controlled directory.
type FileStore struct{ dir string }

// NewFileStore prepares the local evidence directory with restrictive permissions.
func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create evidence directory: %w", err)
	}
	return &FileStore{dir: dir}, nil
}

// Save hashes and atomically confirms a record without allowing ID replacement.
func (s *FileStore) Save(ctx context.Context, r Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !r.validTreatment() {
		return ErrUntreated
	}
	var err error
	p := filepath.Join(s.dir, r.ID+".json")
	if _, err := os.Stat(p); err == nil {
		return ErrAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	r.IntegrityHash = ""
	raw, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encode evidence: %w", err)
	}
	sum := sha256.Sum256(raw)
	r.IntegrityHash = hex.EncodeToString(sum[:])
	raw, err = json.Marshal(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".evidence-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(raw)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write evidence: %w", err)
	}
	if err = os.Rename(name, p); err != nil {
		return fmt.Errorf("commit evidence: %w", err)
	}
	return nil
}

// Get decodes one exact-number record, rejects trailing content, and verifies integrity before confirmation.
func (s *FileStore) Get(ctx context.Context, id string) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, id+".json"))
	if err != nil {
		return Record{}, fmt.Errorf("read evidence %s: %w", id, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var r Record
	if err = decoder.Decode(&r); err != nil {
		return Record{}, fmt.Errorf("decode evidence %s: %w", id, err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Record{}, fmt.Errorf("decode evidence %s: trailing data", id)
		}
		return Record{}, fmt.Errorf("decode evidence %s: %w", id, err)
	}
	want := r.IntegrityHash
	r.IntegrityHash = ""
	canonical, err := json.Marshal(r)
	if err != nil {
		return Record{}, ErrIntegrity
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != want {
		return Record{}, ErrIntegrity
	}
	r.IntegrityHash = want
	r.treated = true
	r.seal, err = recordSeal(r)
	if err != nil {
		return Record{}, ErrIntegrity
	}
	return r, nil
}
