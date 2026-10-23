package functionalflow

// RevisionSet retains immutable artifacts and semantic fingerprints for bounded monotonic replanning.
type RevisionSet struct {
	artifacts []Artifact
	seen      map[string]bool
	confirmed map[uint64]string
	max       int
}

// NewRevisionSet validates the initial artifact and establishes finite revision ownership.
func NewRevisionSet(initial Artifact, max int) (RevisionSet, error) {
	if max <= 0 || initial.Revision != 1 {
		return RevisionSet{}, flowError(CategoryLimit, "revisions", nil)
	}
	if err := ValidateArtifact(initial); err != nil {
		return RevisionSet{}, err
	}
	return RevisionSet{artifacts: []Artifact{cloneArtifact(initial)}, seen: map[string]bool{initial.SemanticDigest: true}, confirmed: map[uint64]string{}, max: max}, nil
}

// Confirm binds one immutable primary result to its exact artifact before another revision may be admitted.
func (s RevisionSet) Confirm(result Result) (RevisionSet, error) {
	if len(s.artifacts) == 0 {
		return s, flowError(CategoryIntegrity, "revision.confirm", nil)
	}
	current := s.artifacts[len(s.artifacts)-1]
	if result.FlowID != current.FlowID || result.Revision != current.Revision || result.ArtifactDigest != current.ArtifactDigest {
		return s, flowError(CategoryIntegrity, "revision.confirm", nil)
	}
	digest := digestValue(result)
	if prior := s.confirmed[result.Revision]; prior != "" && prior != digest {
		return s, flowError(CategoryIntegrity, "revision.confirm", nil)
	}
	out := s.clone()
	out.confirmed[result.Revision] = digest
	return out, nil
}

// Add atomically accepts a new bounded revision without mutating prior authority or steps.
func (s RevisionSet) Add(next Artifact) (RevisionSet, bool, error) {
	if err := ValidateArtifact(next); err != nil {
		return s, false, err
	}
	if s.seen[next.SemanticDigest] {
		return s, true, nil
	}
	if len(s.artifacts) >= s.max {
		return s, false, flowError(CategoryLimit, "revisions", nil)
	}
	prior := s.artifacts[len(s.artifacts)-1]
	if next.FlowID != prior.FlowID || next.Revision != prior.Revision+1 || next.Bindings != prior.Bindings || len(next.Steps) < len(prior.Steps) || s.confirmed[prior.Revision] == "" || next.PriorResultDigest != s.confirmed[prior.Revision] {
		return s, false, flowError(CategoryAuthority, "revision.authority", nil)
	}
	for i := range prior.Steps {
		if digestValue(next.Steps[i]) != digestValue(prior.Steps[i]) {
			return s, false, flowError(CategoryAuthority, "revision.steps", nil)
		}
	}
	out := s.clone()
	out.artifacts = append(out.artifacts, cloneArtifact(next))
	out.seen[next.SemanticDigest] = true
	return out, false, nil
}

// clone detaches all revision lineage maps and artifacts before an immutable update.
func (s RevisionSet) clone() RevisionSet {
	out := RevisionSet{artifacts: make([]Artifact, len(s.artifacts)), seen: map[string]bool{}, confirmed: map[uint64]string{}, max: s.max}
	for i, artifact := range s.artifacts {
		out.artifacts[i] = cloneArtifact(artifact)
	}
	for digest := range s.seen {
		out.seen[digest] = true
	}
	for revision, digest := range s.confirmed {
		out.confirmed[revision] = digest
	}
	return out
}

// Artifacts returns detached revision lineage in confirmed order.
func (s RevisionSet) Artifacts() []Artifact {
	out := make([]Artifact, len(s.artifacts))
	for i, artifact := range s.artifacts {
		out[i] = cloneArtifact(artifact)
	}
	return out
}
