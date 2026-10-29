package validationworkflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asierhr/featureProof/internal/functionalflow"
)

// PackageImportItem is one canonical filesystem-independent package prepared
// for binding to the new PostgreSQL epoch during atomic cutover.
type PackageImportItem struct {
	RunID          string `json:"run_id"`
	PackageID      string `json:"package_id"`
	MediaType      string `json:"media_type"`
	SchemaVersion  string `json:"schema_version"`
	ArtifactDigest string `json:"artifact_digest"`
	Digest         string `json:"digest"`
	Document       []byte `json:"document"`
}

// PackageImportSnapshot contains only packages whose consumer-owned integrity
// verifier accepts their complete lifecycle projection.
type PackageImportSnapshot struct {
	Items []PackageImportItem `json:"items"`
}

// ExportPackageImportSnapshot verifies finalized functional packages and emits
// canonical bytes without filesystem locations or private signing material.
func ExportPackageImportSnapshot(ctx context.Context, root string) ([]byte, error) {
	snapshot := PackageImportSnapshot{Items: []PackageImportItem{}}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() != "functional-flow-package.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var pkg functionalflow.Package
		if json.Unmarshal(data, &pkg) != nil || functionalflow.VerifyPackage(pkg) != nil || pkg.RunID == "" || pkg.Artifact.ArtifactDigest == "" {
			return &Error{Code: CodeConflict, Operation: "package_import"}
		}
		canonical, err := json.Marshal(pkg)
		if err != nil || !json.Valid(canonical) {
			return &Error{Code: CodeConflict, Operation: "package_import"}
		}
		digest := sha256.Sum256(canonical)
		snapshot.Items = append(snapshot.Items, PackageImportItem{RunID: pkg.RunID, PackageID: pkg.RunID + "-functional", MediaType: "application/vnd.featureproof.functional-flow+json", SchemaVersion: pkg.Version, ArtifactDigest: pkg.Artifact.ArtifactDigest, Digest: hex.EncodeToString(digest[:]), Document: canonical})
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return json.Marshal(snapshot)
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(snapshot.Items, func(i, j int) bool {
		return strings.Compare(snapshot.Items[i].PackageID, snapshot.Items[j].PackageID) < 0
	})
	return json.Marshal(snapshot)
}

// PackageImportClaimedPaths suppresses the private package directory after its
// canonical documents have been exported.
func PackageImportClaimedPaths(root string) []string { return []string{root} }
