package previewenv

import (
	"context"
	"sort"
)

// CatalogEntry binds one explicit alias to trusted provider and policy configuration.
type CatalogEntry struct {
	Selection   Selection
	Principals  []string
	ForkAllowed bool
}

// Catalog snapshots trusted entries and refuses duplicate or implicit selection.
type Catalog struct{ entries map[string]CatalogEntry }

// NewCatalog validates and defensively copies the trusted preview catalog.
func NewCatalog(entries []CatalogEntry) (*Catalog, error) {
	result := &Catalog{entries: make(map[string]CatalogEntry, len(entries))}
	for _, entry := range entries {
		s := entry.Selection
		if !safeID.MatchString(s.Alias) || !safeID.MatchString(s.ProviderID) || !safeID.MatchString(s.EnvironmentRef) || !safeID.MatchString(s.ProfileID) || !safeID.MatchString(s.NetworkPolicyID) || (!s.Secretless && s.CredentialReference == "") || (s.Secretless && s.CredentialReference != "") || len(entry.Principals) == 0 {
			return nil, &Error{Code: CodeInvalid, Operation: "catalog"}
		}
		if _, duplicate := result.entries[s.Alias]; duplicate {
			return nil, &Error{Code: CodeConflict, Operation: "catalog"}
		}
		copyEntry := entry
		copyEntry.Principals = append([]string(nil), entry.Principals...)
		sort.Strings(copyEntry.Principals)
		result.entries[s.Alias] = copyEntry
	}
	return result, nil
}

// Resolve authorizes one exact alias for a trusted principal and fork policy.
func (c *Catalog) Resolve(ctx context.Context, principal, alias string, fork bool) (Selection, error) {
	if err := ctx.Err(); err != nil {
		return Selection{}, &Error{Code: CodeCancelled, Operation: "catalog"}
	}
	entry, ok := c.entries[alias]
	if !ok || alias == "" || !contains(entry.Principals, principal) || (fork && (!entry.ForkAllowed || !entry.Selection.Secretless)) {
		return Selection{}, &Error{Code: CodeUnauthorized, Operation: "catalog"}
	}
	return entry.Selection, nil
}

// contains performs an exact principal authorization check.
func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
