package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var ErrAlreadyExists = errors.New("evidence already exists")
var ErrIntegrity = errors.New("evidence integrity mismatch")

type Record struct {
	ID, RunID, ToolCallID, ToolName string
	Step, Attempt                   int
	StartedAt                       time.Time
	Duration                        time.Duration
	Input, Output                   map[string]any
	ErrorClass                      string
	IntegrityHash                   string
}

type Store interface {
	Save(context.Context, Record) error
	Get(context.Context, string) (Record, error)
}

type Sanitizer struct{ Sensitive map[string]bool }

func (s Sanitizer) Map(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if s.Sensitive[k] {
			out[k] = "[REDACTED]"
		} else {
			out[k] = v
		}
	}
	return out
}

type FileStore struct{ dir string }

func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create evidence directory: %w", err)
	}
	return &FileStore{dir: dir}, nil
}

func (s *FileStore) Save(ctx context.Context, r Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
func (s *FileStore) Get(ctx context.Context, id string) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, id+".json"))
	if err != nil {
		return Record{}, fmt.Errorf("read evidence %s: %w", id, err)
	}
	var r Record
	if err = json.Unmarshal(raw, &r); err != nil {
		return Record{}, err
	}
	want := r.IntegrityHash
	r.IntegrityHash = ""
	canonical, _ := json.Marshal(r)
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != want {
		return Record{}, ErrIntegrity
	}
	r.IntegrityHash = want
	return r, nil
}
