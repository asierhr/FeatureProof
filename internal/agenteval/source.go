package agenteval

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/model"
)

// replayWire is the closed versioned container for local scripted responses.
type replayWire struct {
	SchemaVersion int           `json:"schema_version"`
	Entries       []replayEntry `json:"entries"`
}

// replayEntry binds one case to bounded structured output and synthetic usage.
type replayEntry struct {
	CaseID       string          `json:"case_id"`
	Output       json.RawMessage `json:"output"`
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	InputTokens  int64           `json:"input_tokens"`
	OutputTokens int64           `json:"output_tokens"`
}

// ReplaySource returns verified local responses in case order and never owns network dependencies.
type ReplaySource struct {
	mu            sync.Mutex
	entries       map[string]replayEntry
	calls         int
	tokens        int64
	provider      string
	model         string
	maxBytes      int64
	maximumTokens int64
}

// LoadReplay verifies a local replay file and its explicit digest under a byte ceiling.
func LoadReplay(ctx context.Context, path, expectedDigest string, maxBytes int64) (*ReplaySource, error) {
	if err := ctx.Err(); err != nil {
		return nil, contextEvalError("replay", err)
	}
	if maxBytes <= 0 || !sha256Text.MatchString(expectedDigest) {
		return nil, typedError(InvalidInput, "replay", "config", nil)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxBytes {
		return nil, typedError(InvalidInput, "replay", "file", nil)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, typedError(InvalidInput, "replay", "file", nil)
	}
	if digestBytes(data) != expectedDigest {
		return nil, typedError(IntegrityFailure, "replay", "digest", nil)
	}
	var wire replayWire
	if err := decodeStrict(data, &wire); err != nil {
		return nil, typedError(InvalidInput, "replay", "document", nil)
	}
	if wire.SchemaVersion != 1 {
		return nil, typedError(UnsupportedVersion, "replay", "schema_version", nil)
	}
	entries := map[string]replayEntry{}
	provider, selectedModel := "", ""
	var maximumTokens int64
	for i, e := range wire.Entries {
		if !stableID.MatchString(e.CaseID) || entries[e.CaseID].CaseID != "" || len(e.Output) == 0 || e.InputTokens < 0 || e.OutputTokens < 0 {
			return nil, typedError(InvalidInput, "replay", "entries["+itoa(i)+"]", nil)
		}
		if e.InputTokens > int64(^uint64(0)>>1)-e.OutputTokens || maximumTokens > int64(^uint64(0)>>1)-(e.InputTokens+e.OutputTokens) {
			return nil, typedError(LimitExceeded, "replay", "usage", nil)
		}
		maximumTokens += e.InputTokens + e.OutputTokens
		entries[e.CaseID] = e
		entryProvider, entryModel := e.Provider, e.Model
		if entryProvider == "" {
			entryProvider = "replay"
		}
		if entryModel == "" {
			entryModel = "scripted"
		}
		if provider == "" {
			provider, selectedModel = entryProvider, entryModel
		} else if provider != entryProvider || selectedModel != entryModel {
			return nil, typedError(InvalidInput, "replay", "selection", nil)
		}
	}
	return &ReplaySource{entries: entries, provider: provider, model: selectedModel, maxBytes: maxBytes, maximumTokens: maximumTokens}, nil
}

// Model reserves one scripted entry for the requested case without performing I/O.
func (s *ReplaySource) Model(ctx context.Context, c Case) (agentplanning.PlanningModel, error) {
	if err := ctx.Err(); err != nil {
		return nil, contextEvalError("replay", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[c.id]
	if !ok {
		return nil, typedError(InvalidInput, "replay", "case", nil)
	}
	return &replayModel{source: s, entry: e}, nil
}

// Metadata reports that the source is deterministic local replay.
func (s *ReplaySource) Metadata() (string, string) { return s.provider, s.model }

// Snapshot returns replay accounting without exposing response bytes.
func (s *ReplaySource) Snapshot() SourceSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SourceSnapshot{Calls: s.calls, Tokens: s.tokens}
}

// SafeBudget reports the replay byte ceiling without exposing fixture contents or paths.
func (s *ReplaySource) SafeBudget() SourceBudget {
	return SourceBudget{Kind: "replay", Provider: s.provider, Model: s.model, MaxCalls: len(s.entries), MaxTokens: s.maximumTokens, MaxReplans: 0, MaxRequestBytes: s.maxBytes, MaxOutputBytes: s.maxBytes}
}

// replayModel enforces single consumption of one already verified response.
type replayModel struct {
	source *ReplaySource
	entry  replayEntry
	used   bool
}

// Generate consumes one replay response at most once and reports allowlisted metadata.
func (m *replayModel) Generate(ctx context.Context, _ agentplanning.StructuredRequest) (agentplanning.ModelResponse, error) {
	if err := ctx.Err(); err != nil {
		return agentplanning.ModelResponse{}, err
	}
	if m.used {
		return agentplanning.ModelResponse{}, errors.New("replay entry reused")
	}
	m.used = true
	m.source.mu.Lock()
	defer m.source.mu.Unlock()
	m.source.calls++
	if m.entry.InputTokens > int64(^uint64(0)>>1)-m.entry.OutputTokens {
		return agentplanning.ModelResponse{}, errors.New("invalid replay usage")
	}
	delta := m.entry.InputTokens + m.entry.OutputTokens
	if m.source.tokens > int64(^uint64(0)>>1)-delta {
		return agentplanning.ModelResponse{}, errors.New("replay usage overflow")
	}
	m.source.tokens += delta
	provider := m.entry.Provider
	if provider == "" {
		provider = "replay"
	}
	selected := m.entry.Model
	if selected == "" {
		selected = "scripted"
	}
	return agentplanning.ModelResponse{JSON: append([]byte(nil), m.entry.Output...), Provider: provider, Model: selected}, nil
}

// LiveSource adapts one explicitly composed feature-9 session without retaining an auth profile.
type LiveSource struct {
	session *model.Session
	budget  SourceBudget
}

// NewLiveSource accepts only an authenticated neutral session and its complete safe non-secret budget record.
func NewLiveSource(session *model.Session, budget SourceBudget) (*LiveSource, error) {
	if session == nil || !validLiveSourceBudget(budget) {
		return nil, typedError(InvalidInput, "live", "session", nil)
	}
	budget.Kind = "live"
	budget.Provider, budget.Model = session.Selection()
	return &LiveSource{session: session, budget: budget}, nil
}

// validLiveSourceBudget requires every call, runtime and I/O ceiling serialized by safe_configuration.
func validLiveSourceBudget(value SourceBudget) bool {
	return value.CallTimeoutMillis > 0 && value.MaxCalls > 0 && value.MaxTokens > 0 && value.MaxDurationMillis > 0 && value.MaxReplans >= 0 && value.MaxRequestBytes > 0 && value.MaxHeaderBytes > 0 && value.MaxStreamBytes > 0 && value.MaxOutputBytes > 0
}

// Model exposes the same bounded session through the feature-10 consumer adapter.
func (s *LiveSource) Model(ctx context.Context, _ Case) (agentplanning.PlanningModel, error) {
	if err := ctx.Err(); err != nil {
		return nil, contextEvalError("live", err)
	}
	return agentplanning.SessionModel{Session: s.session}, nil
}

// Metadata exposes provider and model identifiers while omitting authentication identity.
func (s *LiveSource) Metadata() (string, string) { return s.session.Selection() }

// Snapshot returns feature-9 call/token/duration/replan accounting.
func (s *LiveSource) Snapshot() SourceSnapshot {
	v := s.session.Snapshot()
	return SourceSnapshot{Calls: v.Calls, Tokens: v.Tokens, Replans: v.Replans, Duration: v.Duration}
}

// SafeBudget returns only public selection and non-monetary session ceilings.
func (s *LiveSource) SafeBudget() SourceBudget { return s.budget }

// SessionClock supplies wall time when production composition creates a live model session.
type SessionClock struct{}

// Now returns current UTC wall time for live session budgets.
func (SessionClock) Now() time.Time { return time.Now().UTC() }

// itoa formats bounded indexes for safe structural diagnostics.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := [20]byte{}
	p := len(digits)
	for value > 0 {
		p--
		digits[p] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[p:])
}
