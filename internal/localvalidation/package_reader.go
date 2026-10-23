package localvalidation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/fixture"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/revisionbinding"
)

// packagePlanDocument decodes the public redacted plan projection without granting execution authority.
type packagePlanDocument struct {
	Version            int             `json:"version"`
	PlanArtifactSHA256 string          `json:"plan_artifact_sha256,omitempty"`
	Runtime            json.RawMessage `json:"runtime"`
	Plans              []struct {
		ID           string `json:"id"`
		ConstraintID string `json:"constraint_id"`
		Steps        []struct {
			ID                     string          `json:"id"`
			Tool                   string          `json:"tool"`
			Input                  json.RawMessage `json:"input"`
			Assertions             json.RawMessage `json:"assertions"`
			DependsOn              []string        `json:"depends_on"`
			IdentityID             string          `json:"identity_id,omitempty"`
			OperationBindingSHA256 string          `json:"operation_binding_sha256,omitempty"`
		} `json:"steps"`
	} `json:"plans"`
}

// ReadPackage returns a trusted offline view only after byte, identity and
// reference validation of the entire local directory; it has no tool or network
// dependency and never interprets model text as a result.
func ReadPackage(ctx context.Context, dir string) (PackageIndex, error) {
	return readPackageWithInventory(ctx, dir, inventoryPackage, FixtureAuditTrust{})
}

// ReadPackageWithFixtureTrust verifies fixture audit authority with an external public trust anchor.
func ReadPackageWithFixtureTrust(ctx context.Context, dir string, trust FixtureAuditTrust) (PackageIndex, error) {
	return readPackageWithInventory(ctx, dir, inventoryPackage, trust)
}

// readPackageWithInventory verifies one stable inventory and optionally one external fixture trust anchor.
func readPackageWithInventory(ctx context.Context, dir string, scan func(context.Context, string) ([]PackageArtifact, error), trusts ...FixtureAuditTrust) (PackageIndex, error) {
	trust := FixtureAuditTrust{}
	if len(trusts) == 1 {
		trust = trusts[0]
	} else if len(trusts) > 1 {
		return PackageIndex{}, &PackageError{Category: "binding_integrity", Path: "fixture-authority.json"}
	}
	if err := ctx.Err(); err != nil {
		return PackageIndex{}, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return PackageIndex{}, &PackageError{Category: "invalid_path"}
	}
	defer root.Close()
	manifestRaw, err := readPackageFile(ctx, root, "manifest.json", maxManifestBytes)
	if err != nil {
		return PackageIndex{}, err
	}
	var manifest PackageManifest
	if err := decodePackageJSON(manifestRaw, "manifest.json", &manifest); err != nil {
		return PackageIndex{}, err
	}
	if manifest.Version != PackageVersion {
		return PackageIndex{}, &PackageError{Category: "unsupported_version", Path: "manifest.json"}
	}
	if !revisionbinding.ValidID(manifest.RunID) || len(manifest.BindingSHA256) != 64 || manifest.Artifacts == nil || len(manifest.Artifacts) > maxPackageEntries {
		return PackageIndex{}, &PackageError{Category: "invalid_schema", Path: "manifest.json"}
	}
	actual, err := scan(ctx, dir)
	if err != nil {
		return PackageIndex{}, err
	}
	if len(actual) != len(manifest.Artifacts) {
		category := "missing_artifact"
		if len(actual) > len(manifest.Artifacts) {
			category = "extra_artifact"
		}
		return PackageIndex{}, &PackageError{Category: category, Path: "manifest.json"}
	}
	for i, entry := range manifest.Artifacts {
		if !canonicalArtifactPath(entry.Path) || entry.Path == "manifest.json" || (i > 0 && manifest.Artifacts[i-1].Path >= entry.Path) {
			return PackageIndex{}, &PackageError{Category: "invalid_path", Path: "manifest.json"}
		}
		if actual[i].Path != entry.Path {
			return PackageIndex{}, &PackageError{Category: "missing_artifact", Path: entry.Path}
		}
		if actual[i].SizeBytes != entry.SizeBytes || actual[i].SHA256 != entry.SHA256 {
			return PackageIndex{}, &PackageError{Category: "digest_mismatch", Path: entry.Path}
		}
	}
	for _, required := range []string{"run.json", "events.jsonl", "plan.json", "report.json", "report.md", "package.json"} {
		found := false
		for _, entry := range actual {
			if entry.Path == required {
				found = true
				break
			}
		}
		if !found {
			return PackageIndex{}, &PackageError{Category: "missing_artifact", Path: required}
		}
	}
	verified := make(map[string]PackageArtifact, len(actual))
	for _, artifact := range actual {
		verified[artifact.Path] = artifact
	}
	read := func(name string) ([]byte, error) {
		artifact, ok := verified[name]
		if !ok {
			return nil, &PackageError{Category: "missing_artifact", Path: name}
		}
		raw, err := readPackageFile(ctx, root, name, maxArtifactBytes)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(raw)
		if int64(len(raw)) != artifact.SizeBytes || hex.EncodeToString(sum[:]) != artifact.SHA256 {
			return nil, &PackageError{Category: "digest_mismatch", Path: name}
		}
		return raw, nil
	}
	packageRaw, err := read("package.json")
	if err != nil {
		return PackageIndex{}, err
	}
	index, err := decodePackageIndex(packageRaw)
	if err != nil {
		return PackageIndex{}, err
	}
	if fixtureArtifact, ok := verified["fixtures.json"]; ok {
		fixtureRaw, readErr := read("fixtures.json")
		if readErr != nil {
			return PackageIndex{}, readErr
		}
		var summary fixture.FixtureSummary
		if decodePackageJSON(fixtureRaw, fixtureArtifact.Path, &summary) != nil || index.Fixtures == nil || !reflect.DeepEqual(*index.Fixtures, summary) || fixture.ValidateSummary(summary, index.RunID) != nil {
			return PackageIndex{}, &PackageError{Category: "binding_integrity", Path: "fixtures.json"}
		}
		if err := verifyFixtureAuditAuthority(index, read, summary, trust); err != nil {
			return PackageIndex{}, err
		}
	} else if index.Fixtures != nil {
		return PackageIndex{}, &PackageError{Category: "missing_artifact", Path: "fixtures.json"}
	}
	if index.RunID != manifest.RunID || index.Binding.BindingSHA256 != manifest.BindingSHA256 {
		return PackageIndex{}, &PackageError{Category: "binding_integrity", Path: "package.json"}
	}
	if err := index.Binding.Verify(ctx); err != nil {
		return PackageIndex{}, &PackageError{Category: "binding_integrity", Path: "package.json"}
	}
	if err := revisionbinding.VerifyPlanChain(ctx, index.Binding, index.PlanBindings); err != nil {
		return PackageIndex{}, &PackageError{Category: "binding_integrity", Path: "package.json"}
	}
	if err := validatePackageDocuments(ctx, read, index, actual); err != nil {
		return PackageIndex{}, err
	}
	second, err := scan(ctx, dir)
	if err != nil {
		return PackageIndex{}, err
	}
	if len(second) != len(actual) {
		category := "missing_artifact"
		if len(second) > len(actual) {
			category = "extra_artifact"
		}
		return PackageIndex{}, &PackageError{Category: category, Path: "manifest.json"}
	}
	for i := range actual {
		if second[i] != actual[i] {
			return PackageIndex{}, &PackageError{Category: "digest_mismatch", Path: actual[i].Path}
		}
	}
	manifestAgain, err := readPackageFile(ctx, root, "manifest.json", maxManifestBytes)
	if err != nil {
		return PackageIndex{}, err
	}
	if !bytes.Equal(manifestRaw, manifestAgain) {
		return PackageIndex{}, &PackageError{Category: "digest_mismatch", Path: "manifest.json"}
	}
	return index, nil
}

// verifyFixtureAuditAuthority verifies the full audit against consumer-owned authority outside the re-sealable package.
func verifyFixtureAuditAuthority(index PackageIndex, read func(string) ([]byte, error), summary fixture.FixtureSummary, trust FixtureAuditTrust) error {
	sealRaw, err := read("fixture-authority.json")
	if err != nil {
		return err
	}
	var seal fixtureAuditSeal
	if decodePackageJSON(sealRaw, "fixture-authority.json", &seal) != nil {
		return &PackageError{Category: "binding_integrity", Path: "fixture-authority.json"}
	}
	signature, signatureErr := hex.DecodeString(seal.Signature)
	raw, marshalErr := json.Marshal(summary)
	digest := sha256.Sum256(raw)
	publicDigest := sha256.Sum256(trust.PublicKey)
	if len(trust.PublicKey) != ed25519.PublicKeySize || !validFixtureAuthorityID(trust.KeyID) || trust.KeyID != hex.EncodeToString(publicDigest[:]) || seal.KeyID != trust.KeyID || signatureErr != nil || marshalErr != nil || seal.Digest != hex.EncodeToString(digest[:]) || !ed25519.Verify(trust.PublicKey, fixtureAuditMessage(index, seal.KeyID, digest), signature) {
		return &PackageError{Category: "binding_integrity", Path: "fixture-authority.json"}
	}
	return nil
}

func readPackageFile(ctx context.Context, root *os.Root, rel string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !canonicalArtifactPath(rel) {
		return nil, &PackageError{Category: "invalid_path", Path: ""}
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return nil, &PackageError{Category: "missing_artifact", Path: rel}
	}
	if !info.Mode().IsRegular() {
		return nil, &PackageError{Category: "invalid_path", Path: rel}
	}
	if info.Size() > limit {
		return nil, &PackageError{Category: "limit_exceeded", Path: rel}
	}
	file, err := root.Open(rel)
	if err != nil {
		return nil, &PackageError{Category: "invalid_path", Path: rel}
	}
	defer file.Close()
	if !singleLink(file) {
		return nil, &PackageError{Category: "invalid_path", Path: rel}
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, &PackageError{Category: "invalid_path", Path: rel}
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, &PackageError{Category: "invalid_path", Path: rel}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, &PackageError{Category: "limit_exceeded", Path: rel}
	}
	return raw, nil
}

func validatePackageDocuments(ctx context.Context, read func(string) ([]byte, error), index PackageIndex, artifacts []PackageArtifact) error {
	raw, err := read("run.json")
	if err != nil {
		return err
	}
	var run runArtifact
	if err := decodePackageJSON(raw, "run.json", &run); err != nil {
		return err
	}
	raw, err = read("report.json")
	if err != nil {
		return err
	}
	var report reportArtifact
	if err := decodePackageJSON(raw, "report.json", &report); err != nil {
		return err
	}
	if run.Binding == nil || report.Binding == nil || *run.Binding != index.Binding || *report.Binding != index.Binding || run.ID != index.RunID || report.RunID != index.RunID || run.Status != index.Status || report.Status != index.Status || !equalPlanBindings(run.PlanBindings, index.PlanBindings) || !equalPlanBindings(report.PlanBindings, index.PlanBindings) {
		return &PackageError{Category: "binding_integrity", Path: "run.json"}
	}
	if !reflect.DeepEqual(report.Fixtures, index.Fixtures) {
		return &PackageError{Category: "binding_integrity", Path: "report.json"}
	}
	if index.Fixtures != nil {
		markdown, markdownErr := read("report.md")
		expected := fixtureMarkdownSection(*index.Fixtures)
		if markdownErr != nil || bytes.Count(markdown, []byte("\n## Fixture lifecycle\n")) != 1 || !bytes.HasSuffix(markdown, expected) {
			return &PackageError{Category: "binding_integrity", Path: "report.md"}
		}
	}
	if len(index.Constraints) != len(run.Constraints) || len(index.Constraints) != len(report.Constraints) {
		return &PackageError{Category: "broken_reference", Path: "package.json"}
	}
	planRaw, err := read("plan.json")
	if err != nil {
		return err
	}
	var plan packagePlanDocument
	if err := decodePackageJSON(planRaw, "plan.json", &plan); err != nil {
		return err
	}
	topology, err := validatePlanTopology(plan, report)
	if err != nil {
		return err
	}
	if !validPackageStatus(index.Status) {
		return &PackageError{Category: "invalid_schema", Path: "package.json"}
	}
	if index.Status == ResultPassed || index.Status == ResultFailed {
		outcomes := make([]ConstraintResult, len(index.Constraints))
		for i, c := range index.Constraints {
			outcomes[i] = ConstraintResult{Status: c.Status}
		}
		if aggregateStatus(outcomes) != index.Status {
			return &PackageError{Category: "broken_reference", Path: "package.json"}
		}
	}
	evidenceIDs := map[string]string{}
	for _, entry := range index.Evidence {
		if !safePackageID.MatchString(entry.ID) || entry.Path != "evidence/"+entry.ID+".json" || evidenceIDs[entry.ID] != "" {
			return &PackageError{Category: "broken_reference", Path: "package.json"}
		}
		evidenceIDs[entry.ID] = entry.Path
	}
	count := 0
	for _, artifact := range artifacts {
		if strings.HasPrefix(artifact.Path, "evidence/") {
			count++
			if !containsEvidencePath(evidenceIDs, artifact.Path) {
				return &PackageError{Category: "extra_artifact", Path: artifact.Path}
			}
		}
	}
	if count != len(evidenceIDs) {
		return &PackageError{Category: "missing_artifact", Path: "evidence"}
	}
	records := map[string]evidence.Record{}
	for id, rel := range evidenceIDs {
		raw, err := read(rel)
		if err != nil {
			return err
		}
		var record evidence.Record
		if err := decodePackageJSON(raw, rel, &record); err != nil {
			return err
		}
		if record.ID != id || record.RunID != index.RunID || record.BindingSHA256 != index.Binding.BindingSHA256 {
			return &PackageError{Category: "binding_integrity", Path: rel}
		}
		want := record.IntegrityHash
		record.IntegrityHash = ""
		canonical, err := json.Marshal(record)
		if err != nil {
			return &PackageError{Category: "invalid_schema", Path: rel}
		}
		sum := sha256.Sum256(canonical)
		if hex.EncodeToString(sum[:]) != want {
			return &PackageError{Category: "digest_mismatch", Path: rel}
		}
		records[id] = record
	}
	for i, constraint := range index.Constraints {
		if !revisionbinding.ValidID(constraint.ConstraintID) || !validConstraintStatus(constraint.Status) {
			return &PackageError{Category: "invalid_schema", Path: "package.json"}
		}
		if constraint.Status == ResultPassed && len(constraint.EvidenceIDs) == 0 {
			return &PackageError{Category: "broken_reference", Path: "package.json"}
		}
		if constraint.ConstraintID != run.Constraints[i].ConstraintID || constraint.ConstraintID != report.Constraints[i].ConstraintID || constraint.Status != run.Constraints[i].Status || constraint.Status != report.Constraints[i].Status || !equalStrings(constraint.EvidenceIDs, run.Constraints[i].EvidenceIDs) || !equalStrings(constraint.EvidenceIDs, report.Constraints[i].EvidenceIDs) {
			return &PackageError{Category: "broken_reference", Path: "package.json"}
		}
		if err := requireEvidence(constraint.EvidenceIDs, evidenceIDs); err != nil {
			return err
		}
		failedAssertion := false
		nonPassedStep := false
		for _, step := range report.Constraints[i].Steps {
			if constraint.Status == ResultPassed && step.Status != planning.StepPassed {
				return &PackageError{Category: "broken_reference", Path: "report.json"}
			}
			rootFailed := false
			rootInconclusive := false
			for _, assertion := range step.Assertions {
				if assertion.Status == planning.AssertionFailed {
					rootFailed = true
				} else if assertion.Status == planning.AssertionInconclusive {
					rootInconclusive = true
				} else if assertion.Status != planning.AssertionPassed {
					return &PackageError{Category: "invalid_schema", Path: "report.json"}
				}
			}
			if (step.Status == planning.StepFailed) != rootFailed || (step.Status == planning.StepPassed && rootInconclusive) || (step.Status == planning.StepBlocked && len(step.Assertions) != 0) {
				return &PackageError{Category: "broken_reference", Path: "report.json"}
			}
			failedAssertion = failedAssertion || rootFailed
			nonPassedStep = nonPassedStep || step.Status != planning.StepPassed
			if err := requireEvidence(step.EvidenceIDs, evidenceIDs); err != nil {
				return err
			}
			if step.TerminalEvidenceID != "" {
				if err := requireEvidence([]string{step.TerminalEvidenceID}, evidenceIDs); err != nil {
					return err
				}
			}
			if err := checkReportAssertions(step.Assertions, evidenceIDs); err != nil {
				return err
			}
		}
		if (constraint.Status == ResultFailed) != failedAssertion {
			return &PackageError{Category: "broken_reference", Path: "report.json"}
		}
		if constraint.Status == ResultInconclusive && !nonPassedStep && len(report.Constraints[i].Steps) > 0 {
			return &PackageError{Category: "broken_reference", Path: "report.json"}
		}
	}
	if err := validatePackageReferences(index, report, topology, evidenceIDs, records); err != nil {
		return err
	}
	raw, err = read("events.jsonl")
	if err != nil {
		return err
	}
	if err := validatePackageEvents(ctx, raw, index, topology, evidenceIDs, records); err != nil {
		return err
	}
	return nil
}

func containsEvidencePath(ids map[string]string, path string) bool {
	for _, current := range ids {
		if current == path {
			return true
		}
	}
	return false
}

type packageStepKey struct{ constraint, step string }

// packageIdentityExpectation is the safe identity selection sealed into one public plan step.
type packageIdentityExpectation struct {
	identityID, operationBindingSHA256 string
}

// packagePlanTopology indexes plan ownership and sealed identity expectations for cross-document verification.
type packagePlanTopology struct {
	stepBatches        map[packageStepKey]int
	planSteps          map[packageStepKey]bool
	planConstraints    map[string]string
	identityByPlanStep map[packageStepKey]packageIdentityExpectation
}

// validatePlanTopology validates the public plan/report graph and preserves each step identity binding for journal verification.
func validatePlanTopology(plan packagePlanDocument, report reportArtifact) (packagePlanTopology, error) {
	if plan.Version != 0 && plan.Version != 1 && plan.Version != 2 && plan.Version != 3 {
		return packagePlanTopology{}, &PackageError{Category: "invalid_schema", Path: "plan.json"}
	}
	known := map[string]map[string]bool{}
	topology := packagePlanTopology{stepBatches: map[packageStepKey]int{}, planSteps: map[packageStepKey]bool{}, planConstraints: map[string]string{}, identityByPlanStep: map[packageStepKey]packageIdentityExpectation{}}
	planIDs := map[string]bool{}
	for i, batch := range plan.Plans {
		if !revisionbinding.ValidID(batch.ID) || !revisionbinding.ValidID(batch.ConstraintID) || planIDs[batch.ID] {
			return packagePlanTopology{}, &PackageError{Category: "invalid_schema", Path: "plan.json"}
		}
		planIDs[batch.ID] = true
		topology.planConstraints[batch.ID] = batch.ConstraintID
		if known[batch.ConstraintID] == nil {
			known[batch.ConstraintID] = map[string]bool{}
		}
		for _, step := range batch.Steps {
			if !revisionbinding.ValidID(step.ID) || known[batch.ConstraintID][step.ID] {
				return packagePlanTopology{}, &PackageError{Category: "invalid_schema", Path: "plan.json"}
			}
			if (step.IdentityID == "") != (step.OperationBindingSHA256 == "") || step.IdentityID != "" && (!revisionbinding.ValidID(step.IdentityID) || len(step.OperationBindingSHA256) != 64) {
				return packagePlanTopology{}, &PackageError{Category: "invalid_schema", Path: "plan.json"}
			}
			known[batch.ConstraintID][step.ID] = true
			topology.stepBatches[packageStepKey{batch.ConstraintID, step.ID}] = i + 1
			key := packageStepKey{batch.ID, step.ID}
			topology.planSteps[key] = true
			if step.IdentityID != "" {
				topology.identityByPlanStep[key] = packageIdentityExpectation{identityID: step.IdentityID, operationBindingSHA256: step.OperationBindingSHA256}
			}
		}
	}
	for _, constraint := range report.Constraints {
		for _, step := range constraint.Steps {
			if !known[constraint.ConstraintID][step.StepID] {
				return packagePlanTopology{}, &PackageError{Category: "broken_reference", Path: "report.json"}
			}
		}
	}
	return topology, nil
}
func equalPlanBindings(a, b []revisionbinding.PlanBinding) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func requireEvidence(ids []string, known map[string]string) error {
	for _, id := range ids {
		if _, ok := known[id]; !ok {
			return &PackageError{Category: "broken_reference", Path: "package.json"}
		}
	}
	return nil
}
func checkReportAssertions(nodes []reportAssertion, known map[string]string) error {
	for _, node := range nodes {
		if node.Status != planning.AssertionPassed && node.Status != planning.AssertionFailed && node.Status != planning.AssertionInconclusive {
			return &PackageError{Category: "invalid_schema", Path: "report.json"}
		}
		if err := requireEvidence(node.EvidenceIDs, known); err != nil {
			return err
		}
		if err := checkReportAssertions(node.Children, known); err != nil {
			return err
		}
		if len(node.Children) > 0 {
			if node.Operator != "all" && node.Operator != "any" {
				return &PackageError{Category: "broken_reference", Path: "report.json"}
			}
			passed, failed, allPassed, allFailed := false, false, true, true
			for _, child := range node.Children {
				passed = passed || child.Status == planning.AssertionPassed
				failed = failed || child.Status == planning.AssertionFailed
				allPassed = allPassed && child.Status == planning.AssertionPassed
				allFailed = allFailed && child.Status == planning.AssertionFailed
			}
			want := planning.AssertionInconclusive
			if node.Operator == "all" {
				if failed {
					want = planning.AssertionFailed
				} else if allPassed {
					want = planning.AssertionPassed
				}
			} else if passed {
				want = planning.AssertionPassed
			} else if allFailed {
				want = planning.AssertionFailed
			}
			if node.Status != want {
				return &PackageError{Category: "broken_reference", Path: "report.json"}
			}
		}
	}
	return nil
}

func validatePackageReferences(index PackageIndex, report reportArtifact, topology packagePlanTopology, known map[string]string, records map[string]evidence.Record) error {
	type nodeKey struct{ constraint, step, declaration, path string }
	nodes := map[nodeKey]reportAssertion{}
	steps := map[string]map[string]reportStep{}
	stepCount, assertionCount, failedCount := 0, 0, 0
	for _, constraint := range report.Constraints {
		steps[constraint.ConstraintID] = map[string]reportStep{}
		var add func(string, []reportAssertion)
		add = func(step string, assertions []reportAssertion) {
			for _, node := range assertions {
				assertionCount++
				if node.Status == planning.AssertionFailed {
					failedCount++
				}
				nodes[nodeKey{constraint.ConstraintID, step, node.DeclarationID, node.NodePath}] = node
				add(step, node.Children)
			}
		}
		for _, step := range constraint.Steps {
			stepCount++
			steps[constraint.ConstraintID][step.StepID] = step
			add(step.StepID, step.Assertions)
		}
	}
	if len(index.Failures) != failedCount {
		return &PackageError{Category: "broken_reference", Path: "package.json"}
	}
	seenFacts := map[string]bool{}
	seenFailures := map[nodeKey]bool{}
	seenStepFacts, seenAssertionFacts, seenToolFacts := 0, 0, 0
	if len(index.Reproduction) != len(index.Constraints) {
		return &PackageError{Category: "broken_reference", Path: "package.json"}
	}
	for _, fact := range index.Facts {
		if err := requireEvidence(fact.EvidenceIDs, known); err != nil {
			return err
		}
		switch fact.Kind {
		case "tool":
			seenToolFacts++
			if len(fact.EvidenceIDs) != 1 {
				return &PackageError{Category: "broken_reference", Path: "package.json"}
			}
			record := records[fact.EvidenceIDs[0]]
			want := "observed"
			if record.ErrorClass != "" {
				want = "error"
			}
			if record.ID == "" || record.StepID != fact.StepID || record.PlanID != fact.PlanID || !topology.planSteps[packageStepKey{fact.PlanID, fact.StepID}] || fact.Status != want {
				return &PackageError{Category: "broken_reference", Path: "package.json"}
			}
		case "step":
			seenStepFacts++
			step, ok := steps[fact.ConstraintID][fact.StepID]
			if !ok || string(step.Status) != fact.Status || fact.Status == "" || !equalStrings(step.EvidenceIDs, fact.EvidenceIDs) {
				return &PackageError{Category: "broken_reference", Path: "package.json"}
			}
		case "assertion":
			seenAssertionFacts++
			node, ok := nodes[nodeKey{fact.ConstraintID, fact.StepID, fact.DeclarationID, fact.NodePath}]
			if !ok || string(node.Status) != fact.Status || !equalStrings(node.EvidenceIDs, fact.EvidenceIDs) {
				return &PackageError{Category: "broken_reference", Path: "package.json"}
			}
		default:
			return &PackageError{Category: "invalid_schema", Path: "package.json"}
		}
		key := fact.Kind + "/" + fact.ConstraintID + "/" + fact.StepID + "/" + fact.DeclarationID + "/" + fact.NodePath
		if fact.Kind == "tool" {
			key = "tool/" + fact.EvidenceIDs[0]
		}
		if seenFacts[key] {
			return &PackageError{Category: "broken_reference", Path: "package.json"}
		}
		seenFacts[key] = true
	}
	if seenStepFacts != stepCount || seenAssertionFacts != assertionCount || seenToolFacts != len(known) {
		return &PackageError{Category: "broken_reference", Path: "package.json"}
	}
	for _, failure := range index.Failures {
		if failure.Scenario != "unspecified" || failure.CheckID != "" {
			return &PackageError{Category: "broken_reference", Path: "package.json"}
		}
		batch := topology.stepBatches[packageStepKey{failure.ConstraintID, failure.StepID}]
		if batch == 0 || batch > len(index.PlanBindings) || failure.BatchID != strconv.Itoa(batch) {
			return &PackageError{Category: "broken_reference", Path: "package.json"}
		}
		identity := nodeKey{failure.ConstraintID, failure.StepID, failure.DeclarationID, failure.NodePath}
		node, ok := nodes[identity]
		if !ok || node.Status != planning.AssertionFailed || failure.Status != "failed" || node.Operator != failure.Operator || !equalStrings(node.EvidenceIDs, failure.EvidenceIDs) {
			return &PackageError{Category: "broken_reference", Path: "package.json"}
		}
		if seenFailures[identity] {
			return &PackageError{Category: "broken_reference", Path: "package.json"}
		}
		seenFailures[identity] = true
		if err := requireEvidence(failure.EvidenceIDs, known); err != nil {
			return err
		}
		if !validSafeValue(failure.Expected) || !validSafeValue(failure.Observed) {
			return &PackageError{Category: "invalid_schema", Path: "package.json"}
		}
	}
	for i, reproduction := range index.Reproduction {
		if reproduction.Scenario != "unspecified" {
			return &PackageError{Category: "broken_reference", Path: "package.json"}
		}
		if reproduction.ConstraintID != index.Constraints[i].ConstraintID || !equalStrings(reproduction.EvidenceIDs, index.Constraints[i].EvidenceIDs) {
			return &PackageError{Category: "broken_reference", Path: "package.json"}
		}
		if len(reproduction.PlanSHA256) != len(index.PlanBindings) {
			return &PackageError{Category: "broken_reference", Path: "package.json"}
		}
		for j, digest := range reproduction.PlanSHA256 {
			if digest != index.PlanBindings[j].PlanSHA256 {
				return &PackageError{Category: "broken_reference", Path: "package.json"}
			}
		}
		if _, ok := steps[reproduction.ConstraintID]; !ok || reproduction.CommitOID != index.Binding.CommitOID || reproduction.RepositoryID != index.Binding.RepositoryID || reproduction.EnvironmentID != index.Binding.EnvironmentID || reproduction.AttestationRef != index.Binding.AttestationRef || reproduction.ConstraintsSHA256 != index.Binding.ConstraintsSHA256 || reproduction.ContractSHA256 != index.Binding.ContractSHA256 || reproduction.ConfigurationSHA256 != index.Binding.ConfigurationSHA256 || reproduction.Prerequisite != "environment_attests_bound_commit" || reproduction.Replayability != "manual_prerequisite" {
			return &PackageError{Category: "broken_reference", Path: "package.json"}
		}
		for _, step := range reproduction.StepIDs {
			if _, ok := steps[reproduction.ConstraintID][step]; !ok {
				return &PackageError{Category: "broken_reference", Path: "package.json"}
			}
		}
		if err := requireEvidence(reproduction.EvidenceIDs, known); err != nil {
			return err
		}
	}
	return nil
}

func validSafeValue(value SafeValue) bool {
	switch value.State {
	case "absent", "redacted", "truncated":
		return value.Value == nil
	case "present":
		switch scalar := value.Value.(type) {
		case nil, bool:
			return true
		case json.Number:
			return len(scalar.String()) <= maxValueBytes && json.Valid([]byte(scalar.String()))
		}
	}
	return false
}

// validatePackageEvents verifies cross-document references and fail-closed identity facts before replaying the bound sequence.
func validatePackageEvents(ctx context.Context, raw []byte, index PackageIndex, topology packagePlanTopology, known map[string]string, records map[string]evidence.Record) error {
	lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
	if len(lines) < 3 {
		return &PackageError{Category: "broken_reference", Path: "events.jsonl"}
	}
	events := make([]harness.Event, 0, len(lines))
	nextIdentityAttempt := map[packageStepKey]int{}
	identityAttempts := map[packageIdentityAttemptKey]*packageIdentityAttemptState{}
	lastIdentityCategory := map[packageStepKey]string{}
	for i, line := range lines {
		var event eventArtifact
		if err := decodePackageJSON(line, "events.jsonl", &event); err != nil {
			return err
		}
		if event.Sequence != i+1 {
			return &PackageError{Category: "broken_reference", Path: "events.jsonl"}
		}
		if err := requireEvidence(event.EvidenceIDs, known); err != nil {
			return err
		}
		if event.PlanID != "" {
			constraint, ok := topology.planConstraints[event.PlanID]
			if !ok || event.ConstraintID != constraint {
				return &PackageError{Category: "broken_reference", Path: "events.jsonl"}
			}
		} else if event.ConstraintID != "" {
			return &PackageError{Category: "broken_reference", Path: "events.jsonl"}
		}
		if event.StepID != "" && (event.PlanID == "" || !topology.planSteps[packageStepKey{event.PlanID, event.StepID}]) {
			return &PackageError{Category: "broken_reference", Path: "events.jsonl"}
		}
		if event.ToolCallID != "" && (event.StepID == "" || event.ToolCallID != event.PlanID+":"+event.StepID) {
			return &PackageError{Category: "broken_reference", Path: "events.jsonl"}
		}
		stepKey := packageStepKey{event.PlanID, event.StepID}
		expectedIdentity, identityStep := topology.identityByPlanStep[stepKey]
		hasIdentityID := event.IdentityID != ""
		hasIdentityDigest := event.OperationBindingSHA256 != ""
		hasAttempt := event.Attempt != 0
		hasOutcome := event.OutcomeCategory != ""
		attemptKey := packageIdentityAttemptKey{step: stepKey, attempt: event.Attempt}
		switch harness.EventType(event.Type) {
		case harness.EventIdentityAttemptTerminal:
			if !identityStep || !hasIdentityID || !hasIdentityDigest || !hasAttempt || !hasOutcome || event.Attempt <= 0 || !validIdentityTerminalCategory(event.OutcomeCategory) || event.IdentityID != expectedIdentity.identityID || event.OperationBindingSHA256 != expectedIdentity.operationBindingSHA256 {
				return &PackageError{Category: "binding_integrity", Path: "events.jsonl"}
			}
			next := nextIdentityAttempt[stepKey] + 1
			state := identityAttemptState(identityAttempts, attemptKey)
			if !identityAttemptIsCurrent(nextIdentityAttempt, lastIdentityCategory, stepKey, event.Attempt) || !state.observeTerminal(event.OutcomeCategory) {
				return &PackageError{Category: "binding_integrity", Path: "events.jsonl"}
			}
			nextIdentityAttempt[stepKey] = next
			lastIdentityCategory[stepKey] = event.OutcomeCategory
		case harness.EventExecutionStarted, harness.EventRetryScheduled:
			if !hasAttempt || hasOutcome || identityStep && (!hasIdentityID || !hasIdentityDigest || event.Attempt <= 0 || event.IdentityID != expectedIdentity.identityID || event.OperationBindingSHA256 != expectedIdentity.operationBindingSHA256) || !identityStep && (hasIdentityID || hasIdentityDigest) {
				return &PackageError{Category: "binding_integrity", Path: "events.jsonl"}
			}
			if identityStep {
				if !identityAttemptIsCurrent(nextIdentityAttempt, lastIdentityCategory, stepKey, event.Attempt) {
					return &PackageError{Category: "binding_integrity", Path: "events.jsonl"}
				}
				state := identityAttemptState(identityAttempts, attemptKey)
				valid := false
				if harness.EventType(event.Type) == harness.EventExecutionStarted {
					valid = state.observeStarted(event.Attempt)
				} else {
					valid = state.observeRetry(event.Attempt)
				}
				if !valid {
					return &PackageError{Category: "binding_integrity", Path: "events.jsonl"}
				}
			}
		case harness.EventExecutionFinished:
			if !hasAttempt || !validIdentityExecutionCategory(event.OutcomeCategory) || identityStep && (!hasIdentityID || !hasIdentityDigest || event.Attempt <= 0 || event.IdentityID != expectedIdentity.identityID || event.OperationBindingSHA256 != expectedIdentity.operationBindingSHA256) || !identityStep && (hasIdentityID || hasIdentityDigest) {
				return &PackageError{Category: "binding_integrity", Path: "events.jsonl"}
			}
			if identityStep {
				if !identityAttemptIsCurrent(nextIdentityAttempt, lastIdentityCategory, stepKey, event.Attempt) {
					return &PackageError{Category: "binding_integrity", Path: "events.jsonl"}
				}
				if !identityAttemptState(identityAttempts, attemptKey).observeFinished(event.OutcomeCategory) {
					return &PackageError{Category: "binding_integrity", Path: "events.jsonl"}
				}
			}
		case harness.EventPolicyEvaluated, harness.EventBudgetReserved:
			if !hasAttempt || event.Attempt <= 0 || hasIdentityID || hasIdentityDigest || hasOutcome {
				return &PackageError{Category: "binding_integrity", Path: "events.jsonl"}
			}
			if identityStep {
				if !identityAttemptIsCurrent(nextIdentityAttempt, lastIdentityCategory, stepKey, event.Attempt) {
					return &PackageError{Category: "binding_integrity", Path: "events.jsonl"}
				}
				state := identityAttemptState(identityAttempts, attemptKey)
				valid := false
				if harness.EventType(event.Type) == harness.EventPolicyEvaluated {
					valid = state.observePolicy()
				} else {
					valid = state.observeBudget()
				}
				if !valid {
					return &PackageError{Category: "binding_integrity", Path: "events.jsonl"}
				}
			}
		default:
			if hasIdentityID || hasIdentityDigest || hasAttempt || hasOutcome {
				return &PackageError{Category: "binding_integrity", Path: "events.jsonl"}
			}
		}
		for _, id := range event.EvidenceIDs {
			record := records[id]
			if record.PlanID != event.PlanID || record.StepID != event.StepID || (event.ToolCallID != "" && record.ToolCallID != event.ToolCallID) {
				return &PackageError{Category: "broken_reference", Path: "events.jsonl"}
			}
		}
		events = append(events, harness.Event{Sequence: event.Sequence, Type: harness.EventType(event.Type), RunID: event.RunID, BindingSHA256: event.BindingSHA256, PlanSHA256: event.PlanSHA256, Status: event.Status, IdentityID: event.IdentityID, OperationBindingSHA256: event.OperationBindingSHA256, Attempt: event.Attempt, OutcomeCategory: event.OutcomeCategory})
	}
	for _, state := range identityAttempts {
		if !state.terminal {
			return &PackageError{Category: "binding_integrity", Path: "events.jsonl"}
		}
	}
	if err := verifyBoundSequence(ctx, index.Binding, index.PlanBindings, events, index.Status); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &PackageError{Category: "binding_integrity", Path: "events.jsonl"}
	}
	return nil
}

// packageIdentityAttemptKey correlates one authenticated step attempt across compatible durable event types.
type packageIdentityAttemptKey struct {
	step    packageStepKey
	attempt int
}

// packageIdentityAttemptState models the only accepted durable phase path for one authenticated attempt.
type packageIdentityAttemptState struct {
	policy           bool
	budget           bool
	retryScheduled   bool
	started          bool
	finished         bool
	terminal         bool
	finishedCategory string
}

// identityAttemptState returns the stable phase state for an authenticated step attempt.
func identityAttemptState(states map[packageIdentityAttemptKey]*packageIdentityAttemptState, key packageIdentityAttemptKey) *packageIdentityAttemptState {
	state := states[key]
	if state == nil {
		state = &packageIdentityAttemptState{}
		states[key] = state
	}
	return state
}

// identityAttemptIsCurrent prevents a later attempt from opening before the prior transient attempt has closed.
func identityAttemptIsCurrent(next map[packageStepKey]int, last map[packageStepKey]string, step packageStepKey, attempt int) bool {
	return attempt == next[step]+1 && (attempt == 1 || last[step] == "transient")
}

// observePolicy accepts a unique policy observation before all later attempt phases.
func (s *packageIdentityAttemptState) observePolicy() bool {
	if s.terminal || s.policy || s.budget || s.retryScheduled || s.started || s.finished {
		return false
	}
	s.policy = true
	return true
}

// observeBudget accepts a unique budget reservation after policy and before execution phases.
func (s *packageIdentityAttemptState) observeBudget() bool {
	if s.terminal || !s.policy || s.budget || s.retryScheduled || s.started || s.finished {
		return false
	}
	s.budget = true
	return true
}

// observeRetry accepts the retry marker only for a later attempt after its policy and budget checks.
func (s *packageIdentityAttemptState) observeRetry(attempt int) bool {
	if s.terminal || attempt <= 1 || !s.policy || !s.budget || s.retryScheduled || s.started || s.finished {
		return false
	}
	s.retryScheduled = true
	return true
}

// observeStarted accepts a unique execution start after policy and budget, and after the marker for a retry attempt.
func (s *packageIdentityAttemptState) observeStarted(attempt int) bool {
	if s.terminal || !s.policy || !s.budget || attempt > 1 && !s.retryScheduled || s.started || s.finished {
		return false
	}
	s.started = true
	return true
}

// observeFinished accepts a unique execution finish after start and preserves its outcome for terminal concordance.
func (s *packageIdentityAttemptState) observeFinished(category string) bool {
	if s.terminal || !s.started || s.finished || !validIdentityExecutionCategory(category) {
		return false
	}
	s.finished = true
	s.finishedCategory = category
	return true
}

// observeTerminal closes an attempt only when its durable phases support the declared terminal category.
func (s *packageIdentityAttemptState) observeTerminal(category string) bool {
	if s.terminal {
		return false
	}
	valid := false
	switch category {
	case "success":
		valid = s.finished && s.finishedCategory == ""
	case "transient":
		valid = s.finished && s.finishedCategory == string(harness.ErrorTransient)
	case "permanent", "credential_rejected":
		valid = s.finished && s.finishedCategory == string(harness.ErrorPermanent)
	case "cancelled":
		valid = s.finished && s.finishedCategory == "cancelled" || !s.started && !s.finished && (!s.policy && !s.budget && !s.retryScheduled || s.policy && !s.budget && !s.retryScheduled || s.policy && s.budget)
	case "composition_unavailable", "identity_drift", "policy_error":
		valid = !s.policy && !s.budget && !s.retryScheduled && !s.started && !s.finished
	case "policy_denied", "budget_denied":
		valid = s.policy && !s.budget && !s.retryScheduled && !s.started && !s.finished
	}
	if !valid {
		return false
	}
	s.terminal = true
	return true
}

// identityTerminalMayStandAlone identifies pre-I/O rejections whose terminal fact is itself the only durable attempt observation.
func identityTerminalMayStandAlone(category string) bool {
	switch category {
	case "cancelled", "composition_unavailable", "identity_drift", "policy_error":
		return true
	default:
		return false
	}
}

// validIdentityTerminalCategory limits durable terminal facts to categories emitted by the authenticated executor.
func validIdentityTerminalCategory(category string) bool {
	switch category {
	case "success", "transient", "permanent", "credential_rejected", "cancelled", "composition_unavailable", "identity_drift", "policy_error", "policy_denied", "budget_denied":
		return true
	default:
		return false
	}
}

// validIdentityExecutionCategory limits execution-finished metadata to the tool error vocabulary used before terminal reduction.
func validIdentityExecutionCategory(category string) bool {
	return category == "" || category == string(harness.ErrorTransient) || category == string(harness.ErrorPermanent) || category == "cancelled"
}
