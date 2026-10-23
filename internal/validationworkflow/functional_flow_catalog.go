package validationworkflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/asierhr/featureProof/internal/functionalflow"
)

// LocalProvidedFlowCatalog owns bounded provided documents and resolves only configured roots, aliases, or opaque IDs.
type LocalProvidedFlowCatalog struct {
	root         string
	allowedRoots []string
	aliases      map[string]string
	limits       functionalflow.Limits
}

// OpenLocalProvidedFlowCatalog creates a private immutable catalog and canonicalizes every authorized local root.
func OpenLocalProvidedFlowCatalog(root string, allowedRoots []string, aliases map[string]string, limits functionalflow.Limits) (*LocalProvidedFlowCatalog, error) {
	absolute, err := filepath.Abs(root)
	if err != nil || root == "" || limits.MaxDocumentBytes <= 0 || len(allowedRoots) == 0 {
		return nil, &Error{Code: CodeInvalid, Operation: "functional_flow_catalog"}
	}
	if err = os.MkdirAll(absolute, 0o700); err != nil {
		return nil, &Error{Code: CodeUnavailable, Operation: "functional_flow_catalog"}
	}
	trustedRoots := make([]string, 0, len(allowedRoots))
	for _, candidate := range allowedRoots {
		resolved, resolveErr := filepath.Abs(candidate)
		if resolveErr != nil || candidate == "" {
			return nil, &Error{Code: CodeInvalid, Operation: "functional_flow_catalog"}
		}
		trustedRoots = append(trustedRoots, filepath.Clean(resolved))
	}
	trustedAliases := make(map[string]string, len(aliases))
	for alias, artifactID := range aliases {
		if !safeID.MatchString(alias) || !safeID.MatchString(artifactID) {
			return nil, &Error{Code: CodeInvalid, Operation: "functional_flow_catalog"}
		}
		trustedAliases[alias] = artifactID
	}
	return &LocalProvidedFlowCatalog{root: absolute, allowedRoots: trustedRoots, aliases: trustedAliases, limits: limits}, nil
}

// Resolve imports one CLI-only regular file below an authorized root and returns its content-addressed catalog ID.
func (c *LocalProvidedFlowCatalog) Resolve(ctx context.Context, path string) (string, error) {
	if c == nil || ctx.Err() != nil || path == "" || strings.Contains(path, "://") {
		return "", &Error{Code: CodeInvalid, Operation: "functional_flow_manifest"}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", &Error{Code: CodeInvalid, Operation: "functional_flow_manifest"}
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || !c.authorizedPath(resolved) {
		return "", &Error{Code: CodeUnauthorized, Operation: "functional_flow_manifest"}
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > int64(c.limits.MaxDocumentBytes) {
		return "", &Error{Code: CodeInvalid, Operation: "functional_flow_manifest"}
	}
	raw, err := os.ReadFile(resolved)
	if err != nil || len(raw) > c.limits.MaxDocumentBytes {
		return "", &Error{Code: CodeUnavailable, Operation: "functional_flow_manifest"}
	}
	flow, err := functionalflow.DecodeProvided(raw, c.limits)
	if err != nil {
		return "", &Error{Code: CodeInvalid, Operation: "functional_flow_manifest"}
	}
	canonical, err := json.Marshal(flow)
	if err != nil {
		return "", &Error{Code: CodeInternal, Operation: "functional_flow_manifest"}
	}
	digest := sha256.Sum256(canonical)
	artifactID := hex.EncodeToString(digest[:])
	if err = writeImmutableJSON(filepath.Join(c.root, artifactID+".json"), flow, "functional_flow_manifest"); err != nil {
		return "", err
	}
	return artifactID, nil
}

// Load returns a detached bounded provided document selected by one authorized alias or artifact ID.
func (c *LocalProvidedFlowCatalog) Load(ctx context.Context, selection FunctionalFlowSelection) ([]byte, error) {
	if c == nil || ctx.Err() != nil || (selection.Alias == "") == (selection.ArtifactID == "") {
		return nil, &Error{Code: CodeInvalid, Operation: "functional_flow_catalog"}
	}
	artifactID := selection.ArtifactID
	if selection.Alias != "" {
		artifactID = c.aliases[selection.Alias]
	}
	if !safeID.MatchString(artifactID) {
		return nil, &Error{Code: CodeUnauthorized, Operation: "functional_flow_catalog"}
	}
	raw, err := os.ReadFile(filepath.Join(c.root, artifactID+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil || len(raw) == 0 || len(raw) > c.limits.MaxDocumentBytes {
		return nil, &Error{Code: CodeUnavailable, Operation: "functional_flow_catalog"}
	}
	flow, err := functionalflow.DecodeProvided(raw, c.limits)
	if err != nil {
		return nil, &Error{Code: CodeConflict, Operation: "functional_flow_catalog"}
	}
	canonical, err := json.Marshal(flow)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Operation: "functional_flow_catalog"}
	}
	digest := sha256.Sum256(canonical)
	if hex.EncodeToString(digest[:]) != artifactID {
		return nil, &Error{Code: CodeConflict, Operation: "functional_flow_catalog"}
	}
	return append([]byte(nil), raw...), nil
}

// authorizedPath proves a resolved manifest remains within one configured local root.
func (c *LocalProvidedFlowCatalog) authorizedPath(path string) bool {
	clean := filepath.Clean(path)
	for _, root := range c.allowedRoots {
		relative, err := filepath.Rel(root, clean)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && relative != "." {
			return true
		}
	}
	return false
}
