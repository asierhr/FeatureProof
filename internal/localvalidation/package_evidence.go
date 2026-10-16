package localvalidation

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/asierhr/featureProof/internal/evidence"
)

var safePackageID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// collectPackageEvidence closes the ID-to-file table over Store.Get-confirmed records.
func collectPackageEvidence(ctx context.Context, staging, runID, digest string, referenced map[string]bool, store evidence.Store) ([]PackageEvidence, []PackageFact, error) {
	ids := make([]string, 0, len(referenced))
	for id := range referenced {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	entries := make([]PackageEvidence, 0, len(ids))
	facts := make([]PackageFact, 0, len(ids))
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if !safePackageID.MatchString(id) || store == nil {
			return nil, nil, &PackageError{Category: "broken_reference", Path: "package.json"}
		}
		path := "evidence/" + id + ".json"
		info, err := os.Lstat(filepath.Join(staging, filepath.FromSlash(path)))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, &PackageError{Category: "missing_artifact", Path: path}
		}
		record, err := store.Get(ctx, id)
		if err != nil || record.ID != id || record.RunID != runID || record.BindingSHA256 != digest {
			return nil, nil, &PackageError{Category: "binding_integrity", Path: path}
		}
		entries = append(entries, PackageEvidence{ID: id, Path: path})
		status := "observed"
		if record.ErrorClass != "" {
			status = "error"
		}
		facts = append(facts, PackageFact{Kind: "tool", PlanID: record.PlanID, StepID: record.StepID, Status: status, EvidenceIDs: []string{id}})
	}
	return entries, facts, nil
}
