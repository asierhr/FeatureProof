package agenteval

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

// FileSystem is the failure-injectable publication boundary owned by the bundle writer.
type FileSystem interface {
	Mkdir(string, os.FileMode) error
	OpenFile(string, int, os.FileMode) (SyncFile, error)
	Rename(string, string) error
}

// SyncFile is the minimum durable file contract used before atomic rename.
type SyncFile interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

// osFS implements durable local publication without ambient network or credential access.
type osFS struct{}

// Mkdir creates exactly one bundle directory so an existing run is never overwritten.
func (osFS) Mkdir(path string, mode os.FileMode) error { return os.Mkdir(path, mode) }

// OpenFile opens one temporary artifact using exclusive creation.
func (osFS) OpenFile(path string, flag int, mode os.FileMode) (SyncFile, error) {
	return os.OpenFile(path, flag, mode)
}

// Rename commits one fully synced artifact into its final bundle name.
func (osFS) Rename(old, new string) error { return os.Rename(old, new) }

// Writer publishes complete bundles with manifest.json as the final completion marker.
type Writer struct{ fs FileSystem }

// NewWriter composes an injectable filesystem; nil selects the local OS implementation.
func NewWriter(fs FileSystem) *Writer {
	if fs == nil {
		fs = osFS{}
	}
	return &Writer{fs: fs}
}

// Manifest is the only baseline eligibility marker and contains hashes for every prior artifact.
type Manifest struct {
	SchemaVersion     int               `json:"schema_version"`
	Status            string            `json:"status"`
	Verdict           string            `json:"verdict"`
	RunID             string            `json:"run_id"`
	Dataset           DatasetIdentity   `json:"dataset"`
	CaseDigests       map[string]string `json:"case_digests"`
	MetricSchema      string            `json:"metric_schema"`
	PlanningProtocol  string            `json:"planning_protocol"`
	CriteriaSchema    string            `json:"criteria_schema"`
	CriteriaID        string            `json:"criteria_id"`
	CriteriaVersion   string            `json:"criteria_version"`
	CriteriaDigest    string            `json:"criteria_digest"`
	PromptText        string            `json:"prompt_text"`
	PromptVersion     string            `json:"prompt_version"`
	PromptSHA256      string            `json:"prompt_sha256"`
	Provider          string            `json:"provider"`
	Model             string            `json:"model"`
	Authentication    string            `json:"authentication"`
	StartedAt         string            `json:"started_at"`
	FinishedAt        string            `json:"finished_at"`
	ResultDigest      string            `json:"result_digest"`
	Files             map[string]string `json:"files"`
	Reasons           []string          `json:"reasons"`
	SafeConfiguration SafeConfiguration `json:"safe_configuration"`
}

// Write publishes all payload files durably and renames manifest.json last.
func (w *Writer) Write(ctx context.Context, output, runID string, run RunResult, criteria Criteria, comparison *Comparison, started, finished time.Time, forbidden []string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", contextEvalError("publish", err)
	}
	if !stableID.MatchString(runID) {
		return "", typedError(InvalidInput, "publish", "run_id", nil)
	}
	dir := filepath.Join(output, runID)
	if err := w.fs.Mkdir(dir, 0o700); err != nil {
		return "", typedError(ArtifactFailure, "publish", "directory", nil)
	}
	caseBytes, err := encodeCases(run.Cases)
	if err != nil {
		return "", typedError(ArtifactFailure, "publish", "cases", nil)
	}
	metricsBytes, _ := canonicalJSON(run.Metrics)
	files := map[string][]byte{"cases.jsonl": caseBytes, "metrics.json": metricsBytes}
	if comparison != nil {
		data, _ := canonicalJSON(comparison)
		files["comparison.json"] = data
	}
	var total int64
	hashes := map[string]string{}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		data := files[name]
		if (name == "cases.jsonl" || name == "metrics.json") && int64(len(data)) > criteria.Limits.MaxResultBytes {
			return "", typedError(LimitExceeded, "publish", "result_bytes", nil)
		}
		if containsForbidden(data, forbidden) {
			return "", typedError(ArtifactFailure, "publish", name, nil)
		}
		if total > criteria.Limits.MaxArtifactBytes-int64(len(data)) {
			return "", typedError(LimitExceeded, "publish", "artifact_bytes", nil)
		}
		total += int64(len(data))
		if err := w.writeAtomic(ctx, dir, name, data); err != nil {
			return "", err
		}
		hashes[name] = digestBytes(data)
	}
	manifest := Manifest{SchemaVersion: 1, Status: "complete", Verdict: run.Verdict, RunID: runID, Dataset: run.Dataset, CaseDigests: map[string]string{}, MetricSchema: "agent-eval-metrics/v1", PlanningProtocol: "agentplanning/v1", CriteriaSchema: "agent-eval-criteria/v1", CriteriaID: criteria.ID, CriteriaVersion: criteria.Version, CriteriaDigest: criteria.Digest, PromptText: run.PromptText, PromptVersion: run.PromptVersion, PromptSHA256: run.PromptHash, Provider: run.Provider, Model: run.Model, Authentication: "not_configured", StartedAt: started.UTC().Format(time.RFC3339Nano), FinishedAt: finished.UTC().Format(time.RFC3339Nano), ResultDigest: run.Metrics.ResultDigest, Files: hashes, Reasons: append([]string(nil), run.Reasons...), SafeConfiguration: run.SafeConfiguration}
	if run.SafeConfiguration.Source.Kind == "live" {
		manifest.Authentication = "configured"
	}
	for _, c := range run.Cases {
		manifest.CaseDigests[c.CaseID] = c.CaseDigest
	}
	manifestBytes, _ := canonicalJSON(manifest)
	if containsForbidden(manifestBytes, forbidden) || total > criteria.Limits.MaxArtifactBytes-int64(len(manifestBytes)) {
		return "", typedError(ArtifactFailure, "publish", "manifest", nil)
	}
	if err := w.writeAtomic(ctx, dir, "manifest.json", manifestBytes); err != nil {
		return "", err
	}
	return dir, nil
}

// writeAtomic syncs a temporary file before its same-directory atomic rename.
func (w *Writer) writeAtomic(ctx context.Context, dir, name string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return contextEvalError("publish", err)
	}
	tmp := filepath.Join(dir, "."+name+".tmp")
	final := filepath.Join(dir, name)
	file, err := w.fs.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return typedError(ArtifactFailure, "publish", name, nil)
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	n, err := file.Write(data)
	if err != nil || n != len(data) {
		return typedError(ArtifactFailure, "publish", name, nil)
	}
	if err := file.Sync(); err != nil {
		return typedError(ArtifactFailure, "publish", name, nil)
	}
	if err := file.Close(); err != nil {
		return typedError(ArtifactFailure, "publish", name, nil)
	}
	closed = true
	if err := ctx.Err(); err != nil {
		return contextEvalError("publish", err)
	}
	if err := w.fs.Rename(tmp, final); err != nil {
		return typedError(ArtifactFailure, "publish", name, nil)
	}
	return nil
}

// encodeCases emits one compact JSON value per line in manifest order.
func encodeCases(cases []CaseResult) ([]byte, error) {
	var buffer bytes.Buffer
	for _, c := range cases {
		data, err := canonicalJSON(c)
		if err != nil {
			return nil, err
		}
		buffer.Write(data)
		buffer.WriteByte('\n')
	}
	return buffer.Bytes(), nil
}

// containsForbidden scans complete serialized artifacts for synthetic canaries and composition markers.
func containsForbidden(data []byte, values []string) bool {
	for _, v := range values {
		if v != "" && bytes.Contains(data, []byte(v)) {
			return true
		}
	}
	return false
}

// LoadBundle verifies the completion marker, every referenced hash and strict schemas before baseline use.
func LoadBundle(ctx context.Context, dir string, maxBytes int64) (BundleSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return BundleSnapshot{}, contextEvalError("baseline", err)
	}
	if maxBytes <= 0 {
		return BundleSnapshot{}, typedError(InvalidInput, "baseline", "max_bytes", nil)
	}
	manifestData, err := readBounded(filepath.Join(dir, "manifest.json"), maxBytes)
	if err != nil {
		return BundleSnapshot{}, typedError(IncompatibleBaseline, "baseline", "manifest", nil)
	}
	var manifest Manifest
	if err := decodeStrict(manifestData, &manifest); err != nil || manifest.SchemaVersion != 1 || manifest.Status != "complete" || manifest.SafeConfiguration.SchemaVersion != 1 {
		return BundleSnapshot{}, typedError(IncompatibleBaseline, "baseline", "manifest", nil)
	}
	if len(manifest.Files) < 2 || len(manifest.Files) > 3 || manifest.Files["cases.jsonl"] == "" || manifest.Files["metrics.json"] == "" {
		return BundleSnapshot{}, typedError(IncompatibleBaseline, "baseline", "files", nil)
	}
	artifacts := map[string][]byte{}
	total := int64(len(manifestData))
	for name, digest := range manifest.Files {
		if name != "cases.jsonl" && name != "metrics.json" && name != "comparison.json" {
			return BundleSnapshot{}, typedError(IncompatibleBaseline, "baseline", "files", nil)
		}
		data, err := readBounded(filepath.Join(dir, name), maxBytes)
		if err != nil || !sha256Text.MatchString(digest) || digestBytes(data) != digest || total > maxBytes-int64(len(data)) {
			return BundleSnapshot{}, typedError(IncompatibleBaseline, "baseline", "files", nil)
		}
		total += int64(len(data))
		artifacts[name] = data
	}
	cases, err := decodeCases(artifacts["cases.jsonl"])
	if err != nil {
		return BundleSnapshot{}, typedError(IncompatibleBaseline, "baseline", "cases", nil)
	}
	var metrics Metrics
	if err := decodeStrict(artifacts["metrics.json"], &metrics); err != nil || metrics.SchemaVersion != 1 || metrics.ResultDigest != manifest.ResultDigest {
		return BundleSnapshot{}, typedError(IncompatibleBaseline, "baseline", "metrics", nil)
	}
	if len(cases) == 0 || len(manifest.CaseDigests) != len(cases) {
		return BundleSnapshot{}, typedError(IncompatibleBaseline, "baseline", "cases", nil)
	}
	seen := map[string]bool{}
	for _, item := range cases {
		if item.CaseID == "" || seen[item.CaseID] || manifest.CaseDigests[item.CaseID] != item.CaseDigest {
			return BundleSnapshot{}, typedError(IncompatibleBaseline, "baseline", "cases", nil)
		}
		seen[item.CaseID] = true
	}
	recomputed, aggregateErr := Aggregate(ctx, cases)
	if aggregateErr != nil || !reflect.DeepEqual(recomputed, metrics) {
		return BundleSnapshot{}, typedError(IncompatibleBaseline, "baseline", "metrics", nil)
	}
	if comparisonData, ok := artifacts["comparison.json"]; ok {
		var comparison Comparison
		if err := decodeStrict(comparisonData, &comparison); err != nil || comparison.SchemaVersion != 1 {
			return BundleSnapshot{}, typedError(IncompatibleBaseline, "baseline", "comparison", nil)
		}
	}
	return BundleSnapshot{Dataset: manifest.Dataset, CaseDigests: manifest.CaseDigests, MetricSchema: manifest.MetricSchema, PlanningProtocol: manifest.PlanningProtocol, CriteriaSchema: manifest.CriteriaSchema, Cases: cases, Metrics: metrics}, nil
}

// readBounded rejects nonregular and oversized baseline files before allocation.
func readBounded(path string, max int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > max {
		return nil, errors.New("invalid baseline file")
	}
	return os.ReadFile(path)
}

// decodeCases strictly decodes each JSONL case and rejects blank or trailing malformed records.
func decodeCases(data []byte) ([]CaseResult, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024), int(min64(int64(len(data))+1, 16<<20)))
	out := []CaseResult{}
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			return nil, errors.New("blank case")
		}
		var c CaseResult
		if err := decodeStrict(scanner.Bytes(), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, scanner.Err()
}

// min64 returns the smaller safe scanner allocation ceiling.
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

var _ = json.RawMessage{}
