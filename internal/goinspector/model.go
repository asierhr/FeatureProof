// Package goinspector derives conservative HTTP contract facts from an immutable Go repository snapshot.
package goinspector

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

// SourceKind is the exact registry route for the hermetic Go static inspector.
const SourceKind = "go-static-v1"

// AdapterVersion binds derived facts to the implementation contract that produced them.
const AdapterVersion = "go-static-v1"

// ErrorCategory lets composition distinguish safe inspector failures without parsing source text.
type ErrorCategory string

const (
	// CategoryInvalidInput marks authority, revision, path, or configuration that was not safely sealed.
	CategoryInvalidInput ErrorCategory = "invalid_input"
	// CategoryInsufficientContext marks analysis that proved no safe operation identity.
	CategoryInsufficientContext ErrorCategory = "insufficient_context"
	// CategoryLimitExceeded marks a bounded traversal that exhausted trusted capacity.
	CategoryLimitExceeded ErrorCategory = "limit_exceeded"
	// CategoryCancelled preserves caller cancellation as an operational cause.
	CategoryCancelled ErrorCategory = "cancelled"
)

// Error is the redacted error boundary returned by every inspector stage.
type Error struct {
	Category ErrorCategory `json:"category"`
	Stage    string        `json:"stage"`
	Cause    error         `json:"-"`
}

// Error emits only closed category and stage values so source literals and host paths never leak.
func (e *Error) Error() string { return fmt.Sprintf("go inspector: %s at %s", e.Category, e.Stage) }

// Unwrap preserves only cancellation and deadline causes for standard errors.Is checks.
func (e *Error) Unwrap() error { return e.Cause }

// DiagnosticCode classifies unsupported syntax without retaining collaborator-controlled messages.
type DiagnosticCode string

const (
	// DiagnosticSyntax records an isolated file that could not be parsed.
	DiagnosticSyntax DiagnosticCode = "syntax_error"
	// DiagnosticDynamicRoute records an operation identity that is not statically provable.
	DiagnosticDynamicRoute DiagnosticCode = "dynamic_route"
	// DiagnosticUnsupported records a construct outside the explicit v1 pattern set.
	DiagnosticUnsupported DiagnosticCode = "unsupported_construct"
	// DiagnosticMissingType records a schema whose local declaration could not be resolved.
	DiagnosticMissingType DiagnosticCode = "missing_type"
	// DiagnosticSchemaLimit records a schema unit omitted atomically at its traversal boundary.
	DiagnosticSchemaLimit DiagnosticCode = "schema_limit"
	// DiagnosticControlFlow records a route or response affected by unresolved branching.
	DiagnosticControlFlow DiagnosticCode = "control_flow_partial"
)

// Diagnostic is a bounded safe explanation attached to a relative source location.
type Diagnostic struct {
	Code      DiagnosticCode `json:"code"`
	Path      string         `json:"path,omitempty"`
	Line      int            `json:"line,omitempty"`
	Symbol    string         `json:"symbol,omitempty"`
	Framework string         `json:"framework,omitempty"`
}

// Limits bounds every potentially long or memory-growing inspection dimension.
type Limits struct {
	MaxFiles       int           `json:"max_files"`
	MaxBytes       int           `json:"max_bytes"`
	MaxPackages    int           `json:"max_packages"`
	MaxASTNodes    int           `json:"max_ast_nodes"`
	MaxDepth       int           `json:"max_depth"`
	MaxTypes       int           `json:"max_types"`
	MaxOperations  int           `json:"max_operations"`
	MaxDiagnostics int           `json:"max_diagnostics"`
	MaxDuration    time.Duration `json:"max_duration"`
}

// BuildContext is the explicit, environment-independent selector for Go build constraints.
type BuildContext struct {
	GOOS   string
	GOARCH string
	Tags   []string
}

// AuthIdentityKind domain-separates local declarations from explicit unresolved external aliases.
type AuthIdentityKind string

const (
	// AuthIdentityLocal identifies a declaration by canonical local import path and symbol.
	AuthIdentityLocal AuthIdentityKind = "local"
	// AuthIdentityExternal identifies unavailable code by literal import path, explicit alias, and symbol.
	AuthIdentityExternal AuthIdentityKind = "external_explicit"
)

// AuthIdentity is the typed collision-resistant authority for one middleware fact.
type AuthIdentity struct {
	Kind       AuthIdentityKind `json:"kind"`
	ImportPath string           `json:"import_path"`
	Alias      string           `json:"alias,omitempty"`
	Symbol     string           `json:"symbol"`
}

// AuthMatcher binds one exact typed middleware identity to a trusted neutral scheme.
type AuthMatcher struct {
	Identity AuthIdentity                   `json:"identity"`
	Scheme   contractcatalog.SecurityScheme `json:"scheme"`
}

// Configuration contains only authority-bearing values supplied by trusted composition.
type Configuration struct {
	RepositoryID     string
	Revision         string
	Roots            []string
	ChangedPaths     []string
	Build            BuildContext
	Auth             []AuthMatcher
	Limits           Limits
	Confidence       contractcatalog.Confidence
	Completeness     contractcatalog.Completeness
	IncludeGenerated bool
	EntrySymbols     []string
	Projection       contractcatalog.ProjectionPolicy
}

// Snapshot is an immutable in-memory repository capability with no host filesystem, process, network, or credential port.
type Snapshot struct {
	repositoryID string
	revision     string
	files        map[string][]byte
	digest       string
}

// Input is the fully sealed analysis request consumed by Inspector and DerivedSource.
type Input struct {
	snapshot   Snapshot
	config     Configuration
	digest     string
	deadlineAt time.Time
}

// Result is the canonical immutable inspection projection before registry trust verification.
type Result struct {
	Snapshot           contractcatalog.SourceSnapshot `json:"snapshot"`
	Diagnostics        []Diagnostic                   `json:"diagnostics"`
	OmittedDiagnostics int                            `json:"omitted_diagnostics"`
	AffectedOperations []contractcatalog.OperationKey `json:"affected_operations"`
	Digest             string                         `json:"digest"`
}

// NewSnapshot validates and deep-copies repository bytes so later caller mutation cannot change an inspection.
func NewSnapshot(repositoryID, revision string, files map[string][]byte) (Snapshot, error) {
	if !safeToken(repositoryID, 128) || !validRevision(revision) || len(files) == 0 {
		return Snapshot{}, typedError(CategoryInvalidInput, "snapshot", nil)
	}
	owned := make(map[string][]byte, len(files))
	keys := make([]string, 0, len(files))
	for name, content := range files {
		clean, ok := safeRelativePath(name)
		if !ok || clean != name {
			return Snapshot{}, typedError(CategoryInvalidInput, "snapshot.path", nil)
		}
		owned[name] = append([]byte(nil), content...)
		keys = append(keys, name)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, name := range keys {
		hash.Write([]byte(name))
		hash.Write([]byte{0})
		hash.Write(owned[name])
		hash.Write([]byte{0})
	}
	return Snapshot{repositoryID: repositoryID, revision: strings.ToLower(revision), files: owned, digest: hex.EncodeToString(hash.Sum(nil))}, nil
}

// newInput seals an optional absolute execution deadline without retaining a context or mutable clock.
func newInput(snapshot Snapshot, config Configuration, deadlineAt time.Time) (Input, error) {
	if err := validateConfiguration(config); err != nil {
		return Input{}, err
	}
	if snapshot.digest == "" || config.RepositoryID != snapshot.repositoryID || strings.ToLower(config.Revision) != snapshot.revision {
		return Input{}, typedError(CategoryInvalidInput, "input.authority", nil)
	}
	config.Roots = append([]string(nil), config.Roots...)
	config.ChangedPaths = append([]string(nil), config.ChangedPaths...)
	config.Build.Tags = append([]string(nil), config.Build.Tags...)
	config.EntrySymbols = append([]string(nil), config.EntrySymbols...)
	sealedProjection, projectionErr := contractcatalog.NewProjectionPolicy(config.Projection)
	if projectionErr != nil {
		return Input{}, typedError(CategoryInvalidInput, "input.projection", nil)
	}
	config.Projection = cloneProjection(sealedProjection)
	authRaw, authErr := json.Marshal(config.Auth)
	if authErr != nil {
		return Input{}, typedError(CategoryInvalidInput, "input.auth", nil)
	}
	var ownedAuth []AuthMatcher
	if json.Unmarshal(authRaw, &ownedAuth) != nil {
		return Input{}, typedError(CategoryInvalidInput, "input.auth", nil)
	}
	config.Auth = ownedAuth
	if len(config.Roots) == 0 || len(config.EntrySymbols) == 0 || !validBuild(config.Build) {
		return Input{}, typedError(CategoryInvalidInput, "input.config", nil)
	}
	for i := range config.Roots {
		clean, ok := safeRelativePath(config.Roots[i])
		if !ok {
			return Input{}, typedError(CategoryInvalidInput, "input.root", nil)
		}
		config.Roots[i] = clean
	}
	for i := range config.ChangedPaths {
		clean, ok := safeRelativePath(config.ChangedPaths[i])
		if !ok || !withinRoots(clean, config.Roots) {
			return Input{}, typedError(CategoryInvalidInput, "input.changed", nil)
		}
		config.ChangedPaths[i] = clean
	}
	for _, matcher := range config.Auth {
		if _, valid := validateAuthIdentity(matcher.Identity); !valid || matcher.Scheme.Name == "" || matcher.Scheme.Type == "" {
			return Input{}, typedError(CategoryInvalidInput, "input.auth", nil)
		}
	}
	for _, symbol := range config.EntrySymbols {
		if !safeSymbol(symbol) {
			return Input{}, typedError(CategoryInvalidInput, "input.entry", nil)
		}
	}
	sort.Slice(config.Auth, func(i, j int) bool {
		return authIdentityKey(config.Auth[i].Identity)+"\x00"+config.Auth[i].Scheme.Name < authIdentityKey(config.Auth[j].Identity)+"\x00"+config.Auth[j].Scheme.Name
	})
	sort.Strings(config.Roots)
	sort.Strings(config.ChangedPaths)
	sort.Strings(config.Build.Tags)
	sort.Strings(config.EntrySymbols)
	config.Roots = uniqueStrings(config.Roots)
	config.ChangedPaths = uniqueStrings(config.ChangedPaths)
	config.Build.Tags = uniqueStrings(config.Build.Tags)
	config.EntrySymbols = uniqueStrings(config.EntrySymbols)
	raw, _ := json.Marshal(struct {
		Repository, Revision, Snapshot string
		Roots, Changed, Tags           []string
		Build                          BuildContext
		Auth                           []AuthMatcher
		Limits                         Limits
		Confidence                     contractcatalog.Confidence
		Completeness                   contractcatalog.Completeness
		IncludeGenerated               bool
		EntrySymbols                   []string
		Projection                     contractcatalog.ProjectionPolicy
		DeadlineAt                     time.Time
	}{config.RepositoryID, strings.ToLower(config.Revision), snapshot.digest, config.Roots, config.ChangedPaths, config.Build.Tags, config.Build, config.Auth, config.Limits, config.Confidence, config.Completeness, config.IncludeGenerated, config.EntrySymbols, config.Projection, deadlineAt.UTC()})
	sum := sha256.Sum256(raw)
	return Input{snapshot: cloneSnapshot(snapshot), config: config, digest: hex.EncodeToString(sum[:]), deadlineAt: deadlineAt.UTC()}, nil
}

// validateConfiguration rejects every malformed authority-bearing setting before a repository capability can observe a request.
func validateConfiguration(config Configuration) error {
	if !safeToken(config.RepositoryID, 128) || !validRevision(config.Revision) || !validConfidence(config.Confidence) || config.Completeness != contractcatalog.CompletenessPartial || !validLimits(config.Limits) {
		return typedError(CategoryInvalidInput, "input.authority", nil)
	}
	if len(config.Roots) == 0 || len(config.EntrySymbols) == 0 || !validBuild(config.Build) {
		return typedError(CategoryInvalidInput, "input.config", nil)
	}
	if _, err := contractcatalog.NewProjectionPolicy(config.Projection); err != nil {
		return typedError(CategoryInvalidInput, "input.projection", nil)
	}
	for _, root := range config.Roots {
		if clean, ok := safeRelativePath(root); !ok || clean != root {
			return typedError(CategoryInvalidInput, "input.root", nil)
		}
	}
	for _, changed := range config.ChangedPaths {
		clean, ok := safeRelativePath(changed)
		if !ok || clean != changed || !withinRoots(clean, config.Roots) {
			return typedError(CategoryInvalidInput, "input.changed", nil)
		}
	}
	seenAuthIdentity, seenAuthScheme := map[string]bool{}, map[string]bool{}
	for _, matcher := range config.Auth {
		identityKey, valid := validateAuthIdentity(matcher.Identity)
		if !valid || matcher.Scheme.Name == "" || matcher.Scheme.Type == "" {
			return typedError(CategoryInvalidInput, "input.auth", nil)
		}
		if seenAuthIdentity[identityKey] || seenAuthScheme[matcher.Scheme.Name] {
			return typedError(CategoryInvalidInput, "input.auth_collision", nil)
		}
		seenAuthIdentity[identityKey], seenAuthScheme[matcher.Scheme.Name] = true, true
	}
	for _, symbol := range config.EntrySymbols {
		if !safeSymbol(symbol) {
			return typedError(CategoryInvalidInput, "input.entry", nil)
		}
	}
	return nil
}

// validateAuthIdentity canonicalizes and validates one domain-separated trusted matcher identity.
func validateAuthIdentity(identity AuthIdentity) (string, bool) {
	if !safeSymbol(identity.ImportPath) || !safeGoIdentifier(identity.Symbol) {
		return "", false
	}
	switch identity.Kind {
	case AuthIdentityLocal:
		if identity.Alias != "" {
			return "", false
		}
	case AuthIdentityExternal:
		if !safeGoIdentifier(identity.Alias) {
			return "", false
		}
	default:
		return "", false
	}
	return authIdentityKey(identity), true
}

// authIdentityKey serializes typed fields with length prefixes so domains and components cannot collide.
func authIdentityKey(identity AuthIdentity) string {
	return fmt.Sprintf("%d:%s%d:%s%d:%s%d:%s", len(identity.Kind), identity.Kind, len(identity.ImportPath), identity.ImportPath, len(identity.Alias), identity.Alias, len(identity.Symbol), identity.Symbol)
}

// safeGoIdentifier accepts one Go identifier-shaped authority component.
func safeGoIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || index > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// Digest returns the content-and-configuration binding expected by trusted registry composition.
func (i Input) Digest() string { return i.digest }

// cloneSnapshot owns all repository byte slices for a single immutable input boundary.
func cloneSnapshot(value Snapshot) Snapshot {
	files := make(map[string][]byte, len(value.files))
	for name, content := range value.files {
		files[name] = append([]byte(nil), content...)
	}
	return Snapshot{repositoryID: value.repositoryID, revision: value.revision, files: files, digest: value.digest}
}

// typedError restricts causes to cancellation so source or host error text cannot cross the boundary.
func typedError(category ErrorCategory, stage string, cause error) error {
	if !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
		cause = nil
	}
	return &Error{Category: category, Stage: stage, Cause: cause}
}

// safeRelativePath canonicalizes slash-separated paths and rejects absolute or escaping authority.
func safeRelativePath(value string) (string, bool) {
	value = strings.ReplaceAll(value, "\\", "/")
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, ":") {
		return "", false
	}
	clean := path.Clean(value)
	return clean, clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

// withinRoots proves that a changed path stays under an explicitly authorized root.
func withinRoots(name string, roots []string) bool {
	for _, root := range roots {
		if name == root || strings.HasPrefix(name, strings.TrimSuffix(root, "/")+"/") {
			return true
		}
	}
	return false
}

// validRevision accepts only full hexadecimal object identities to prevent mutable revision aliases.
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

// safeToken bounds repository identifiers without admitting host path or secret-shaped delimiters.
func safeToken(value string, max int) bool {
	return value != "" && len(value) <= max && !strings.ContainsAny(value, "\\/?#:")
}

// safeSymbol admits exact Go selector identities used by trusted middleware matching.
func safeSymbol(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if !(r == '.' || r == '/' || r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// validLimits rejects every implicit or unbounded traversal budget.
func validLimits(v Limits) bool {
	return v.MaxFiles > 0 && v.MaxBytes > 0 && v.MaxPackages > 0 && v.MaxASTNodes > 0 && v.MaxDepth > 0 && v.MaxTypes > 0 && v.MaxOperations > 0 && v.MaxDiagnostics > 0 && v.MaxDuration > 0
}

// validBuild accepts only explicit portable build selectors without reading process environment.
func validBuild(v BuildContext) bool { return safeToken(v.GOOS, 32) && safeToken(v.GOARCH, 32) }

// validConfidence enforces the closed contract-catalog confidence union at the composition boundary.
func validConfidence(value contractcatalog.Confidence) bool {
	return value == contractcatalog.ConfidenceLow || value == contractcatalog.ConfidenceMedium || value == contractcatalog.ConfidenceHigh
}

// cloneProjection freezes every redaction-policy slice before untrusted source names are inspected.
func cloneProjection(value contractcatalog.ProjectionPolicy) contractcatalog.ProjectionPolicy {
	return contractcatalog.ProjectionPolicy{MaxStringBytes: value.MaxStringBytes, AllowedURLSchemes: clonePolicyValues(value.AllowedURLSchemes), SensitiveNames: clonePolicyValues(value.SensitiveNames), SensitivePaths: clonePolicyValues(value.SensitivePaths), SensitiveHeaderNames: clonePolicyValues(value.SensitiveHeaderNames), ForbiddenValues: clonePolicyValues(value.ForbiddenValues)}
}

// clonePolicyValues preserves explicit empty policy lists while taking ownership of their storage.
func clonePolicyValues(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

// uniqueStrings removes duplicate sorted configuration values deterministically.
func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}
