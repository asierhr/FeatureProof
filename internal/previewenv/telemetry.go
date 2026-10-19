package previewenv

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// allowedPreviewEvents is the closed lifecycle vocabulary persisted by telemetry.
var allowedPreviewEvents = map[string]bool{"preview_selected": true, "preview_reserved": true, "preview_readiness_observed": true, "preview_ready": true, "preview_drift": true, "preview_release_requested": true, "preview_released": true, "preview_release_pending": true, "preview_recovery": true}

// telemetryEntry stores exactly one allowlisted event or metric record.
type telemetryEntry struct {
	Kind   string  `json:"kind"`
	Event  *Event  `json:"event,omitempty"`
	Metric *Metric `json:"metric,omitempty"`
}

// LocalTelemetry appends safe lifecycle observations under the trusted preview state root.
type LocalTelemetry struct {
	root string
	mu   sync.Mutex
}

// OpenLocalTelemetry validates the trusted telemetry directory without accepting a remote path.
func OpenLocalTelemetry(root string) (*LocalTelemetry, error) {
	absolute, err := filepath.Abs(root)
	if err != nil || root == "" {
		return nil, &Error{Code: CodeInvalid, Operation: "telemetry"}
	}
	if err = os.MkdirAll(absolute, 0o700); err != nil {
		return nil, &Error{Code: CodeUnavailable, Operation: "telemetry"}
	}
	return &LocalTelemetry{root: absolute}, nil
}

// Observe persists one closed event with IDs and counters only.
func (t *LocalTelemetry) Observe(ctx context.Context, event Event) error {
	if !allowedPreviewEvents[event.Type] || !safeID.MatchString(event.RunID) || event.Attempt < 0 || event.At.IsZero() {
		return &Error{Code: CodeInvalid, Operation: "telemetry_event"}
	}
	return t.append(ctx, telemetryEntry{Kind: "event", Event: &event})
}

// Measure persists one unit counter paired with an allowlisted lifecycle event.
func (t *LocalTelemetry) Measure(ctx context.Context, metric Metric) error {
	if len(metric.Name) <= len("_total") || !strings.HasSuffix(metric.Name, "_total") {
		return &Error{Code: CodeInvalid, Operation: "telemetry_metric"}
	}
	eventName := metric.Name[:len(metric.Name)-len("_total")]
	if !allowedPreviewEvents[eventName] || metric.Value != 1 || metric.At.IsZero() {
		return &Error{Code: CodeInvalid, Operation: "telemetry_metric"}
	}
	return t.append(ctx, telemetryEntry{Kind: "metric", Metric: &metric})
}

// append writes one bounded JSON line while holding process and OS locks.
func (t *LocalTelemetry) append(ctx context.Context, entry telemetryEntry) error {
	raw, err := json.Marshal(entry)
	if err != nil || len(raw) > 4096 {
		return &Error{Code: CodeInvalid, Operation: "telemetry"}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	release, err := acquirePreviewStoreLock(ctx, filepath.Join(t.root, "telemetry.lock"))
	if err != nil {
		return classifyContext(err, "telemetry")
	}
	defer release()
	file, err := os.OpenFile(filepath.Join(t.root, "telemetry.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return &Error{Code: CodeUnavailable, Operation: "telemetry"}
	}
	_, err = file.Write(append(raw, '\n'))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return &Error{Code: CodeUnavailable, Operation: "telemetry"}
	}
	return nil
}
