// Package springinspector derives conservative Spring MVC contract facts from immutable Java source bytes.
package springinspector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
)

// SourceKind is the exact registry route for the hermetic Spring source adapter.
const SourceKind = "spring-static-v1"

// AdapterVersion binds facts to the closed Spring syntax contract implemented here.
const AdapterVersion = "spring-static-v1"

// SupportedFrameworkIdentities returns an isolated exact Spring/Jakarta API surface for trusted composition.
func SupportedFrameworkIdentities() []string { return frameworkIdentitiesV1() }

// frameworkIdentitiesV1 constructs the immutable-by-construction closed framework identity contract.
func frameworkIdentitiesV1() []string {
	return []string{"jakarta.annotation.security.RolesAllowed", "org.springframework.http.HttpStatus", "org.springframework.http.ResponseEntity", "org.springframework.security.access.annotation.Secured", "org.springframework.security.access.prepost.PreAuthorize", "org.springframework.stereotype.Controller", "org.springframework.web.bind.annotation.DeleteMapping", "org.springframework.web.bind.annotation.GetMapping", "org.springframework.web.bind.annotation.PatchMapping", "org.springframework.web.bind.annotation.PathVariable", "org.springframework.web.bind.annotation.PostMapping", "org.springframework.web.bind.annotation.PutMapping", "org.springframework.web.bind.annotation.RequestBody", "org.springframework.web.bind.annotation.RequestHeader", "org.springframework.web.bind.annotation.RequestMapping", "org.springframework.web.bind.annotation.RequestParam", "org.springframework.web.bind.annotation.ResponseBody", "org.springframework.web.bind.annotation.ResponseStatus", "org.springframework.web.bind.annotation.RestController"}
}

// GeneratedPolicy controls whether explicitly authorized generated source sets may contribute facts.
type GeneratedPolicy string

const (
	// GeneratedExclude omits generated source sets from analysis.
	GeneratedExclude GeneratedPolicy = "exclude"
	// GeneratedInclude admits generated source sets already named by trusted composition.
	GeneratedInclude GeneratedPolicy = "include"
)

// ErrorCategory classifies failures without exposing source text or host paths.
type ErrorCategory string

const (
	// CategoryInvalidInput rejects unsealed authority or malformed trusted configuration.
	CategoryInvalidInput ErrorCategory = "invalid_input"
	// CategoryInsufficientContext reports that no safe operation identity was provable.
	CategoryInsufficientContext ErrorCategory = "insufficient_context"
	// CategoryLimitExceeded reports exhaustion of an explicit inspection budget.
	CategoryLimitExceeded ErrorCategory = "limit_exceeded"
	// CategoryCancelled preserves caller cancellation and deadlines.
	CategoryCancelled ErrorCategory = "cancelled"
)

// Error is the redacted package boundary for every inspection stage.
type Error struct {
	Category ErrorCategory `json:"category"`
	Stage    string        `json:"stage"`
	Cause    error         `json:"-"`
}

// Error emits only closed safe fields.
func (e *Error) Error() string { return fmt.Sprintf("spring inspector: %s at %s", e.Category, e.Stage) }

// Unwrap retains cancellation identity while hiding arbitrary causes.
func (e *Error) Unwrap() error { return e.Cause }

// DiagnosticCode classifies bounded unsupported or incomplete source units.
type DiagnosticCode string

const (
	// DiagnosticSyntax marks one independently invalid Java unit.
	DiagnosticSyntax DiagnosticCode = "syntax_error"
	// DiagnosticDynamicMapping marks a mapping whose identity is not statically provable.
	DiagnosticDynamicMapping DiagnosticCode = "dynamic_mapping"
	// DiagnosticUnsupported marks syntax outside the closed adapter contract.
	DiagnosticUnsupported DiagnosticCode = "unsupported_construct"
	// DiagnosticMissingType marks an unresolved local schema dependency.
	DiagnosticMissingType DiagnosticCode = "missing_type"
	// DiagnosticSchemaLimit marks a schema omitted at its atomic boundary.
	DiagnosticSchemaLimit DiagnosticCode = "schema_limit"
	// DiagnosticPartial marks response or security metadata that cannot be completed statically.
	DiagnosticPartial DiagnosticCode = "partial"
)

// Diagnostic is a safe, source-relative explanation suitable for audit surfaces.
type Diagnostic struct {
	Code      DiagnosticCode `json:"code"`
	Path      string         `json:"path,omitempty"`
	Line      int            `json:"line,omitempty"`
	Symbol    string         `json:"symbol,omitempty"`
	Framework string         `json:"framework,omitempty"`
}

// Limits bounds every source, parser, mapping, schema and diagnostic dimension.
type Limits struct {
	MaxSourceSets   int           `json:"max_source_sets"`
	MaxRoots        int           `json:"max_roots"`
	MaxFiles        int           `json:"max_files"`
	MaxBytes        int           `json:"max_bytes"`
	MaxUnits        int           `json:"max_units"`
	MaxTokens       int           `json:"max_tokens"`
	MaxDepth        int           `json:"max_depth"`
	MaxTypes        int           `json:"max_types"`
	MaxSymbols      int           `json:"max_symbols"`
	MaxSchemaNodes  int           `json:"max_schema_nodes"`
	MaxOperations   int           `json:"max_operations"`
	MaxCrossProduct int           `json:"max_cross_product"`
	MaxDiagnostics  int           `json:"max_diagnostics"`
	MaxStringBytes  int           `json:"max_string_bytes"`
	MaxDuration     time.Duration `json:"max_duration"`
}

// SourceSet is one explicitly trusted Java root and its generated classification.
type SourceSet struct {
	ID        string `json:"id"`
	Root      string `json:"root"`
	Generated bool   `json:"generated"`
}

// Configuration contains all authority-bearing choices made outside inspected source.
type Configuration struct {
	RepositoryID        string
	Revision            string
	RootIdentity        string
	FrameworkIdentities []string
	SourceSets          []SourceSet
	ChangedPaths        []string
	JavaRelease         int
	Generated           GeneratedPolicy
	Limits              Limits
	Confidence          contractcatalog.Confidence
	Completeness        contractcatalog.Completeness
	Projection          contractcatalog.ProjectionPolicy
}

// Snapshot owns an immutable copy of authorized repository bytes.
type Snapshot struct {
	repositoryID string
	revision     string
	files        map[string][]byte
	digest       string
}

// Input binds immutable bytes, trusted analysis choices and one absolute deadline.
type Input struct {
	snapshot   Snapshot
	config     Configuration
	digest     string
	deadlineAt time.Time
}

// Result is the canonical neutral projection plus safe inspector diagnostics.
type Result struct {
	Snapshot           contractcatalog.SourceSnapshot `json:"snapshot"`
	Diagnostics        []Diagnostic                   `json:"diagnostics"`
	OmittedDiagnostics int                            `json:"omitted_diagnostics"`
	AffectedOperations []contractcatalog.OperationKey `json:"affected_operations"`
	Digest             string                         `json:"digest"`
}

// NewSnapshot validates identities and paths before taking ownership of every byte slice.
func NewSnapshot(repositoryID, revision string, files map[string][]byte) (Snapshot, error) {
	if !safeToken(repositoryID, 128) || !validRevision(revision) || len(files) == 0 {
		return Snapshot{}, typedError(CategoryInvalidInput, "snapshot", nil)
	}
	owned, names := make(map[string][]byte, len(files)), make([]string, 0, len(files))
	for name, content := range files {
		clean, ok := safeRelativePath(name)
		if !ok || clean != name {
			return Snapshot{}, typedError(CategoryInvalidInput, "snapshot.path", nil)
		}
		owned[name] = append([]byte(nil), content...)
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(owned[name])
		h.Write([]byte{0})
	}
	return Snapshot{repositoryID: repositoryID, revision: strings.ToLower(revision), files: owned, digest: hex.EncodeToString(h.Sum(nil))}, nil
}

// NewInput seals an already materialized snapshot without consulting the host environment.
func NewInput(snapshot Snapshot, config Configuration) (Input, error) {
	return newInput(snapshot, config, time.Time{})
}

// newInput validates and deep-copies all trusted mutable values into one digest-bound request.
func newInput(snapshot Snapshot, config Configuration, deadline time.Time) (Input, error) {
	if err := validateConfiguration(config); err != nil {
		return Input{}, err
	}
	if snapshot.digest == "" || snapshot.repositoryID != config.RepositoryID || snapshot.revision != strings.ToLower(config.Revision) {
		return Input{}, typedError(CategoryInvalidInput, "input.authority", nil)
	}
	config.SourceSets = append([]SourceSet(nil), config.SourceSets...)
	config.FrameworkIdentities = append([]string(nil), config.FrameworkIdentities...)
	config.ChangedPaths = append([]string(nil), config.ChangedPaths...)
	projection, err := contractcatalog.NewProjectionPolicy(config.Projection)
	if err != nil {
		return Input{}, typedError(CategoryInvalidInput, "input.projection", nil)
	}
	config.Projection = cloneProjection(projection)
	sort.Slice(config.SourceSets, func(i, j int) bool {
		return config.SourceSets[i].ID+"\x00"+config.SourceSets[i].Root < config.SourceSets[j].ID+"\x00"+config.SourceSets[j].Root
	})
	sort.Strings(config.ChangedPaths)
	sort.Strings(config.FrameworkIdentities)
	raw, _ := json.Marshal(struct {
		Snapshot string
		Config   Configuration
	}{snapshot.digest, config})
	sum := sha256.Sum256(raw)
	return Input{snapshot: cloneSnapshot(snapshot), config: config, digest: hex.EncodeToString(sum[:]), deadlineAt: deadline.UTC()}, nil
}

// Digest returns the immutable source/configuration identity expected by registry composition.
func (i Input) Digest() string { return i.digest }

// validateConfiguration rejects incomplete or ambiguous authority before any repository read.
func validateConfiguration(config Configuration) error {
	if !safeToken(config.RepositoryID, 128) || !safeToken(config.RootIdentity, 128) || !validRevision(config.Revision) || len(config.SourceSets) == 0 || len(config.SourceSets) > config.Limits.MaxSourceSets || len(config.SourceSets) > config.Limits.MaxRoots || config.JavaRelease < 8 || config.JavaRelease > 25 || !validLimits(config.Limits) || (config.Generated != GeneratedExclude && config.Generated != GeneratedInclude) || !validConfidence(config.Confidence) || config.Completeness != contractcatalog.CompletenessPartial || !equalStringSets(config.FrameworkIdentities, frameworkIdentitiesV1()) {
		return typedError(CategoryInvalidInput, "input.authority", nil)
	}
	if _, err := contractcatalog.NewProjectionPolicy(config.Projection); err != nil {
		return typedError(CategoryInvalidInput, "input.projection", nil)
	}
	ids, roots := map[string]bool{}, []string{}
	for _, sourceSet := range config.SourceSets {
		clean, ok := safeRelativePath(sourceSet.Root)
		if !safeToken(sourceSet.ID, 64) || ids[sourceSet.ID] || !ok || clean != sourceSet.Root {
			return typedError(CategoryInvalidInput, "input.source_set", nil)
		}
		ids[sourceSet.ID] = true
		roots = append(roots, clean)
	}
	for i := range roots {
		for j := i + 1; j < len(roots); j++ {
			if withinRoots(roots[i], []string{roots[j]}) || withinRoots(roots[j], []string{roots[i]}) {
				return typedError(CategoryInvalidInput, "input.source_set_overlap", nil)
			}
		}
	}
	for _, changed := range config.ChangedPaths {
		clean, ok := safeRelativePath(changed)
		if !ok || clean != changed || !withinRoots(clean, roots) {
			return typedError(CategoryInvalidInput, "input.changed", nil)
		}
	}
	return nil
}

// equalStringSets requires the complete closed framework identity contract without duplicates.
func equalStringSets(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	a, b := append([]string(nil), actual...), append([]string(nil), expected...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] || (i > 0 && a[i] == a[i-1]) {
			return false
		}
	}
	return true
}

// validLimits rejects implicit or unbounded traversal capacity.
func validLimits(v Limits) bool {
	return v.MaxSourceSets > 0 && v.MaxRoots > 0 && v.MaxFiles > 0 && v.MaxBytes > 0 && v.MaxUnits > 0 && v.MaxTokens > 0 && v.MaxDepth > 0 && v.MaxTypes > 0 && v.MaxSymbols > 0 && v.MaxSchemaNodes > 0 && v.MaxOperations > 0 && v.MaxCrossProduct > 0 && v.MaxDiagnostics > 0 && v.MaxStringBytes > 0 && v.MaxDuration > 0
}

// validConfidence enforces the source-neutral closed confidence union.
func validConfidence(v contractcatalog.Confidence) bool {
	return v == contractcatalog.ConfidenceLow || v == contractcatalog.ConfidenceMedium || v == contractcatalog.ConfidenceHigh
}

// cloneSnapshot isolates every file buffer for a single inspection.
func cloneSnapshot(value Snapshot) Snapshot {
	files := make(map[string][]byte, len(value.files))
	for name, data := range value.files {
		files[name] = append([]byte(nil), data...)
	}
	return Snapshot{repositoryID: value.repositoryID, revision: value.revision, files: files, digest: value.digest}
}

// cloneProjection owns policy slices so callers cannot mutate sealed redaction authority.
func cloneProjection(v contractcatalog.ProjectionPolicy) contractcatalog.ProjectionPolicy {
	return contractcatalog.ProjectionPolicy{MaxStringBytes: v.MaxStringBytes, AllowedURLSchemes: cloneStrings(v.AllowedURLSchemes), SensitiveNames: cloneStrings(v.SensitiveNames), SensitivePaths: cloneStrings(v.SensitivePaths), SensitiveHeaderNames: cloneStrings(v.SensitiveHeaderNames), ForbiddenValues: cloneStrings(v.ForbiddenValues)}
}

// cloneStrings preserves explicit empty policy lists while taking ownership of their storage.
func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

// typedError drops all causes except caller cancellation identities.
func typedError(category ErrorCategory, stage string, cause error) error {
	if !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
		cause = nil
	}
	return &Error{Category: category, Stage: stage, Cause: cause}
}

// safeRelativePath normalizes slash paths and rejects absolute, volume and parent authority.
func safeRelativePath(value string) (string, bool) {
	value = strings.ReplaceAll(value, "\\", "/")
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, ":") {
		return "", false
	}
	clean := path.Clean(value)
	return clean, clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

// withinRoots proves a path belongs to at least one explicit source root.
func withinRoots(name string, roots []string) bool {
	for _, root := range roots {
		if name == root || strings.HasPrefix(name, strings.TrimSuffix(root, "/")+"/") {
			return true
		}
	}
	return false
}

// validRevision accepts immutable full hexadecimal object identities only.
func validRevision(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

// safeToken admits bounded portable identifiers while excluding path and URL delimiters.
func safeToken(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && !strings.ContainsAny(value, "\\/?#:")
}
