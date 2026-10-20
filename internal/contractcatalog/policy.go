package contractcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// ConflictTreatment controls whether selected conflicts are insufficient or require external review.
type ConflictTreatment string

const (
	// ConflictDeny classifies selected conflicts as insufficient context.
	ConflictDeny ConflictTreatment = "deny"
	// ConflictRequireReview classifies selected conflicts as needing an external resolution snapshot.
	ConflictRequireReview ConflictTreatment = "review_required"
)

// SufficiencyPolicy explicitly defines every trust threshold used before inference and execution.
type SufficiencyPolicy struct {
	ID                string                  `json:"id"`
	Version           string                  `json:"version"`
	Claims            map[ClaimKind]ClaimRule `json:"claims"`
	ConflictTreatment ConflictTreatment       `json:"conflict_treatment"`
}

// ClaimRule explicitly defines the acceptable evidence for one material claim dimension.
type ClaimRule struct {
	AllowedClasses       []SourceClass              `json:"allowed_classes"`
	MinimumConfidence    map[SourceClass]Confidence `json:"minimum_confidence"`
	RequiredCompleteness Completeness               `json:"required_completeness"`
	Required             bool                       `json:"required"`
}

// Policy binds a validated policy to its deterministic digest.
type Policy struct {
	config SufficiencyPolicy
	digest string
}

// Digest returns the immutable canonical policy identity.
func (p Policy) Digest() string { return p.digest }

// Validate recalculates the complete canonical policy identity before a decision can authorize context.
func (p Policy) Validate() error {
	if !validDigest(p.digest) || len(p.config.Claims) != 4 {
		return typedError(CategoryInvalidSource, "policy", nil)
	}
	raw, err := json.Marshal(p.config)
	if err != nil {
		return typedError(CategoryInternal, "policy", nil)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != p.digest {
		return typedError(CategoryInvalidSource, "policy.binding", nil)
	}
	return nil
}

// NewPolicy rejects partial policies so no implicit trust default can enter composition.
func NewPolicy(config SufficiencyPolicy) (Policy, error) {
	if config.ID == "" || config.Version != VersionV1 || len(config.Claims) != 4 || (config.ConflictTreatment != ConflictDeny && config.ConflictTreatment != ConflictRequireReview) {
		return Policy{}, typedError(CategoryInvalidSource, "policy", nil)
	}
	owned := make(map[ClaimKind]ClaimRule, len(config.Claims))
	for _, kind := range []ClaimKind{ClaimOperationIdentity, ClaimSchema, ClaimSecurity, ClaimResponse} {
		rule, ok := config.Claims[kind]
		if !ok || len(rule.AllowedClasses) == 0 || completenessRank(rule.RequiredCompleteness) == 0 || !rule.Required {
			return Policy{}, typedError(CategoryInvalidSource, "policy.claims", nil)
		}
		allowed := map[SourceClass]bool{}
		minimum := make(map[SourceClass]Confidence, len(rule.MinimumConfidence))
		for _, class := range rule.AllowedClasses {
			if !validSourceClass(class) || allowed[class] || confidenceRank(rule.MinimumConfidence[class]) == 0 {
				return Policy{}, typedError(CategoryInvalidSource, "policy.classes", nil)
			}
			allowed[class] = true
			minimum[class] = rule.MinimumConfidence[class]
		}
		if len(rule.MinimumConfidence) != len(allowed) {
			return Policy{}, typedError(CategoryInvalidSource, "policy.confidence", nil)
		}
		rule.AllowedClasses = append([]SourceClass(nil), rule.AllowedClasses...)
		sort.Slice(rule.AllowedClasses, func(i, j int) bool { return rule.AllowedClasses[i] < rule.AllowedClasses[j] })
		rule.MinimumConfidence = minimum
		owned[kind] = rule
	}
	config.Claims = owned
	raw, _ := json.Marshal(config)
	sum := sha256.Sum256(raw)
	return Policy{config: config, digest: hex.EncodeToString(sum[:])}, nil
}

// EligibleSelection carries exact operation keys and the catalog-policy binding that admitted them.
type EligibleSelection struct {
	Operations    []OperationKey
	CatalogDigest string
	PolicyDigest  string
}

// InsufficientContext identifies only safe missing dimensions and affected operation keys.
type InsufficientContext struct {
	MissingKinds []string
	Operations   []OperationKey
}

// ReviewRequired identifies safe conflict IDs requiring an externally authorized resolution.
type ReviewRequired struct {
	ConflictIDs []string
	Operations  []OperationKey
}

// SelectionResult is the closed sufficiency decision union.
type SelectionResult struct {
	Eligible     *EligibleSelection
	Insufficient *InsufficientContext
	Review       *ReviewRequired
}

// Evaluate applies the explicit trust policy to selected operations and never resolves a conflict itself.
func (p Policy) Evaluate(ctx context.Context, selections []ConstraintSelection, catalog OperationCatalog) (SelectionResult, error) {
	if err := catalog.Validate(); err != nil {
		return SelectionResult{}, err
	}
	if err := p.Validate(); err != nil {
		return SelectionResult{}, typedError(CategoryInvalidSource, "policy", nil)
	}
	keys := []OperationKey{}
	missing := []string{}
	reviewIDs := []string{}
	for _, selection := range selections {
		for _, key := range selection.Operations {
			if err := ctx.Err(); err != nil {
				return SelectionResult{}, typedError(CategoryCancelled, "policy", err)
			}
			op, ok := catalog.Lookup(key)
			if !ok {
				missing = append(missing, "operation")
				keys = append(keys, key)
				continue
			}
			keys = append(keys, key)
			missing = append(missing, p.operationMissingKinds(op)...)
			for _, conflict := range catalog.conflicts {
				if containsKey(conflict.OperationKeys, key) {
					if p.config.ConflictTreatment == ConflictRequireReview {
						reviewIDs = append(reviewIDs, conflict.ID)
					} else {
						missing = append(missing, "conflict")
					}
				}
			}
		}
	}
	keys = uniqueKeys(keys)
	if len(reviewIDs) > 0 {
		return SelectionResult{Review: &ReviewRequired{ConflictIDs: sortedUniqueStrings(reviewIDs), Operations: keys}}, nil
	}
	if len(missing) > 0 {
		return SelectionResult{Insufficient: &InsufficientContext{MissingKinds: sortedUniqueStrings(missing), Operations: keys}}, nil
	}
	return SelectionResult{Eligible: &EligibleSelection{Operations: keys, CatalogDigest: catalog.Digest(), PolicyDigest: p.digest}}, nil
}

// operationMissingKinds reports the exact material dimensions lacking one sufficient provenance under their explicit claim rule.
func (p Policy) operationMissingKinds(op OperationDescriptor) []string {
	byKind := map[ClaimKind][]Claim{}
	missing := []string{}
	for _, claim := range op.Claims {
		byKind[claim.Kind] = append(byKind[claim.Kind], claim)
	}
	for kind, rule := range p.config.Claims {
		claims := byKind[kind]
		if rule.Required && len(claims) == 0 {
			missing = append(missing, string(kind))
			continue
		}
		for _, claim := range claims {
			sufficient := false
			for _, prov := range claim.Provenance {
				for _, class := range rule.AllowedClasses {
					if prov.Class == class && confidenceRank(prov.Confidence) >= confidenceRank(rule.MinimumConfidence[class]) && completenessRank(prov.Completeness) >= completenessRank(rule.RequiredCompleteness) {
						sufficient = true
					}
				}
			}
			if !sufficient {
				missing = append(missing, string(kind))
				break
			}
		}
	}
	return sortedUniqueStrings(missing)
}

// containsKey performs exact operation identity matching for conflict membership.
func containsKey(values []OperationKey, want OperationKey) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
