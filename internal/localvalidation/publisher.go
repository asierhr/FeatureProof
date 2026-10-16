package localvalidation

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/evidence"
	"github.com/asierhr/featureProof/internal/harness"
	"github.com/asierhr/featureProof/internal/planmanifest"
	"github.com/asierhr/featureProof/internal/planning"
	"github.com/asierhr/featureProof/internal/revisionbinding"
	"github.com/asierhr/featureProof/internal/specification"
)

// Publisher owns the staging directory and makes a complete run visible with one final rename.
type Publisher struct {
	parent, runID, staging, final string
	fs                            FileSystem
}

// NewPublisher reserves a unique restrictive staging bundle without replacing prior runs.
func NewPublisher(root, runID string) (*Publisher, error) {
	return NewPublisherWithFilesystem(root, runID, OSFileSystem{})
}

// NewPublisherWithFilesystem prepares staging through the injected durable filesystem boundary.
func NewPublisherWithFilesystem(root, runID string, fs FileSystem) (*Publisher, error) {
	if fs == nil {
		return nil, fmt.Errorf("filesystem is required")
	}
	parent := filepath.Join(root, ".featureproof", "runs")
	staging := filepath.Join(parent, runID+".incomplete")
	final := filepath.Join(parent, runID)
	if err := fs.MkdirAll(parent, 0700); err != nil {
		return nil, fmt.Errorf("create runs directory: %w", err)
	}
	for _, path := range []string{staging, final} {
		if _, err := fs.Lstat(path); err == nil {
			return nil, fmt.Errorf("run bundle collision")
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect run bundle: %w", err)
		}
	}
	if err := fs.Mkdir(staging, 0700); err != nil {
		return nil, fmt.Errorf("create staging bundle: %w", err)
	}
	return &Publisher{parent: parent, runID: runID, staging: staging, final: final, fs: fs}, nil
}

// EvidenceStore prepares the only directory in which the executor may confirm treated evidence.
func (p *Publisher) EvidenceStore() (*evidence.FileStore, error) {
	return evidence.NewFileStore(filepath.Join(p.staging, "evidence"))
}

// Publish writes every derived artifact in staging, verifies evidence links, and atomically commits the bundle.
func (p *Publisher) Publish(ctx context.Context, snapshot RunSnapshot, events []harness.Event, manifest planmanifest.Manifest, store evidence.Store) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if snapshot.Binding == nil || snapshot.ID != snapshot.Binding.RunID || snapshot.ID != p.runID {
		return &revisionbinding.Error{Category: revisionbinding.BindingIntegrity, Phase: "publication"}
	}
	if err := verifyBoundSequence(ctx, *snapshot.Binding, snapshot.PlanBindings, events, snapshot.Status); err != nil {
		return err
	}
	seenEvidence := map[string]bool{}
	var addAssertions func([]planning.AssertionResult)
	addAssertions = func(assertions []planning.AssertionResult) {
		for _, assertion := range assertions {
			for _, id := range assertion.EvidenceIDs {
				seenEvidence[id] = true
			}
			addAssertions(assertion.Children)
		}
	}
	for _, constraint := range snapshot.Constraints {
		for _, id := range constraint.EvidenceIDs {
			if seenEvidence[id] {
				return fmt.Errorf("duplicate evidence reference")
			}
			seenEvidence[id] = true
		}
		for _, step := range constraint.Steps {
			for _, id := range step.EvidenceIDs {
				seenEvidence[id] = true
			}
			if step.TerminalEvidenceID != "" {
				seenEvidence[step.TerminalEvidenceID] = true
			}
			addAssertions(step.AssertionResults)
		}
	}
	for _, event := range events {
		for _, id := range event.EvidenceIDs {
			seenEvidence[id] = true
		}
	}
	if len(seenEvidence) > 0 && store == nil {
		return fmt.Errorf("evidence store is required")
	}
	for id := range seenEvidence {
		if err := ctx.Err(); err != nil {
			return err
		}
		record, err := store.Get(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &revisionbinding.Error{Category: revisionbinding.BindingIntegrity, Phase: "evidence"}
		}
		if record.ID != id || record.RunID != snapshot.ID {
			return &revisionbinding.Error{Category: revisionbinding.BindingIntegrity, Phase: "evidence"}
		}
		if record.BindingSHA256 != snapshot.Binding.BindingSHA256 {
			return &revisionbinding.Error{Category: revisionbinding.BindingIntegrity, Phase: "evidence"}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.writeJSON("run.json", buildRunArtifact(snapshot)); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.writeEvents(events); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.writeJSON("plan.json", safeManifest(manifest)); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.writeJSON("report.json", buildReport(snapshot)); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.writeFile("report.md", []byte(renderMarkdown(snapshot))); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := syncDirectory(p.fs, p.staging); err != nil {
		return fmt.Errorf("sync staging bundle: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.fs.CommitNoReplace(p.staging, p.final); err != nil {
		return fmt.Errorf("publish run bundle: %w", err)
	}
	return nil
}

// writeJSON encodes one stable JSON artifact through a same-directory temporary file.
func (p *Publisher) writeJSON(name string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	raw = append(raw, '\n')
	return p.writeFile(name, raw)
}

// writeFile synchronizes one artifact before renaming it into its staging name.
func (p *Publisher) writeFile(name string, raw []byte) error {
	tmp, err := p.fs.CreateTemp(p.staging, ".artifact-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer p.fs.Remove(tmpName)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(raw)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err = p.fs.CommitNoReplace(tmpName, filepath.Join(p.staging, name)); err != nil {
		return fmt.Errorf("commit %s: %w", name, err)
	}
	return nil
}

// writeEvents serializes the confirmed journal sequence as newline-delimited JSON.
func (p *Publisher) writeEvents(events []harness.Event) error {
	var b strings.Builder
	writer := bufio.NewWriter(&b)
	encoder := json.NewEncoder(writer)
	for _, event := range events {
		encoded := eventArtifact{Sequence: event.Sequence, Type: string(event.Type), RunID: event.RunID, BindingSHA256: event.BindingSHA256, PlanSHA256: event.PlanSHA256, PlanID: event.PlanID, StepID: event.StepID, ConstraintID: event.ConstraintID, Step: event.Step, ToolCallID: event.ToolCallID, Reason: string(event.Reason), Detail: event.Detail, Status: event.Status, EvidenceIDs: event.EvidenceIDs, BlockingCauses: event.BlockingCause, Truncations: event.Truncations}
		if err := encoder.Encode(encoded); err != nil {
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	return p.writeFile("events.jsonl", []byte(b.String()))
}

// eventArtifact defines the stable snake-case journal representation without changing the core event contract.
type eventArtifact struct {
	Sequence       int                          `json:"sequence"`
	Type           string                       `json:"type"`
	RunID          string                       `json:"run_id"`
	BindingSHA256  string                       `json:"binding_sha256,omitempty"`
	PlanSHA256     string                       `json:"plan_sha256,omitempty"`
	PlanID         string                       `json:"plan_id,omitempty"`
	StepID         string                       `json:"step_id,omitempty"`
	ConstraintID   string                       `json:"constraint_id,omitempty"`
	Step           int                          `json:"step,omitempty"`
	ToolCallID     string                       `json:"tool_call_id,omitempty"`
	Reason         string                       `json:"reason,omitempty"`
	Detail         string                       `json:"detail,omitempty"`
	Status         string                       `json:"status,omitempty"`
	EvidenceIDs    []string                     `json:"evidence_ids,omitempty"`
	BlockingCauses []harness.EventBlockingCause `json:"blocking_causes,omitempty"`
	Truncations    []harness.EventTruncation    `json:"truncations,omitempty"`
}

// runArtifact is the compact terminal metadata document and excludes detailed assertion payloads.
type runArtifact struct {
	ID           string                        `json:"id"`
	Binding      *revisionbinding.Binding      `json:"binding,omitempty"`
	PlanBindings []revisionbinding.PlanBinding `json:"plan_bindings,omitempty"`
	FeatureID    string                        `json:"feature_id"`
	TargetID     string                        `json:"target_id"`
	Status       ResultStatus                  `json:"status"`
	StartedAt    time.Time                     `json:"started_at"`
	FinishedAt   time.Time                     `json:"finished_at"`
	DurationMS   int64                         `json:"duration_ms"`
	Limits       specification.Limits          `json:"limits"`
	Consumed     harness.Consumption           `json:"consumed"`
	Constraints  []runConstraintArtifact       `json:"constraints"`
}

// runConstraintArtifact keeps only terminal identity, status, and verified evidence links.
type runConstraintArtifact struct {
	ConstraintID string       `json:"constraint_id"`
	Status       ResultStatus `json:"status"`
	EvidenceIDs  []string     `json:"evidence_ids"`
}

// buildRunArtifact removes detailed step trees from operational run metadata.
func buildRunArtifact(snapshot RunSnapshot) runArtifact {
	out := runArtifact{ID: snapshot.ID, Binding: snapshot.Binding, PlanBindings: append([]revisionbinding.PlanBinding(nil), snapshot.PlanBindings...), FeatureID: snapshot.FeatureID, TargetID: snapshot.TargetID, Status: snapshot.Status, StartedAt: snapshot.StartedAt.UTC(), FinishedAt: snapshot.FinishedAt.UTC(), DurationMS: snapshot.DurationMS, Limits: snapshot.Limits, Consumed: snapshot.Consumed, Constraints: make([]runConstraintArtifact, len(snapshot.Constraints))}
	for n, constraint := range snapshot.Constraints {
		out.Constraints[n] = runConstraintArtifact{ConstraintID: constraint.ConstraintID, Status: constraint.Status, EvidenceIDs: append([]string(nil), constraint.EvidenceIDs...)}
	}
	return out
}

// reportArtifact is the payload-free machine report whose shape mirrors report.md.
type reportArtifact struct {
	RunID        string                        `json:"run_id"`
	Binding      *revisionbinding.Binding      `json:"binding,omitempty"`
	PlanBindings []revisionbinding.PlanBinding `json:"plan_bindings,omitempty"`
	Status       ResultStatus                  `json:"status"`
	Constraints  []reportConstraint            `json:"constraints"`
}

// reportConstraint records one ordered constraint with its complete safe step outcomes.
type reportConstraint struct {
	ConstraintID string       `json:"constraint_id"`
	Status       ResultStatus `json:"status"`
	EvidenceIDs  []string     `json:"evidence_ids"`
	Steps        []reportStep `json:"steps"`
}

// reportStep records execution and assertion status without request, response, expected, or observed values.
type reportStep struct {
	StepID             string                   `json:"step_id"`
	Status             planning.StepStatus      `json:"status"`
	EvidenceIDs        []string                 `json:"evidence_ids"`
	TerminalEvidenceID string                   `json:"terminal_evidence_id,omitempty"`
	BlockingCauses     []planning.BlockingCause `json:"blocking_causes,omitempty"`
	Cause              *planning.StepCause      `json:"cause,omitempty"`
	Assertions         []reportAssertion        `json:"assertions"`
}

// reportAssertion preserves the deterministic result tree while excluding assertion values.
type reportAssertion struct {
	DeclarationID string                   `json:"declaration_id"`
	NodePath      string                   `json:"node_path"`
	Operator      string                   `json:"operator"`
	Path          string                   `json:"path"`
	Status        planning.AssertionStatus `json:"status"`
	EvidenceIDs   []string                 `json:"evidence_ids"`
	Children      []reportAssertion        `json:"children,omitempty"`
	Cause         *planning.AssertionCause `json:"cause,omitempty"`
}

// buildReport translates domain results to a stable payload-free artifact schema.
func buildReport(snapshot RunSnapshot) reportArtifact {
	out := reportArtifact{RunID: snapshot.ID, Binding: snapshot.Binding, PlanBindings: append([]revisionbinding.PlanBinding(nil), snapshot.PlanBindings...), Status: snapshot.Status, Constraints: make([]reportConstraint, len(snapshot.Constraints))}
	for n, constraint := range snapshot.Constraints {
		current := reportConstraint{ConstraintID: constraint.ConstraintID, Status: constraint.Status, EvidenceIDs: append([]string(nil), constraint.EvidenceIDs...), Steps: make([]reportStep, len(constraint.Steps))}
		for i, step := range constraint.Steps {
			current.Steps[i] = reportStep{StepID: step.StepID, Status: step.Status, EvidenceIDs: append([]string(nil), step.EvidenceIDs...), TerminalEvidenceID: step.TerminalEvidenceID, BlockingCauses: append([]planning.BlockingCause(nil), step.BlockingCauses...), Cause: step.Cause, Assertions: reportAssertions(step.AssertionResults)}
		}
		out.Constraints[n] = current
	}
	return out
}

// reportAssertions recursively translates assertion outcomes without carrying values.
func reportAssertions(values []planning.AssertionResult) []reportAssertion {
	out := make([]reportAssertion, len(values))
	for n, value := range values {
		out[n] = reportAssertion{DeclarationID: value.DeclarationID, NodePath: value.NodePath, Operator: value.Operator, Path: value.Path, Status: value.Status, EvidenceIDs: append([]string(nil), value.EvidenceIDs...), Children: reportAssertions(value.Children), Cause: value.Cause}
	}
	return out
}

// safeManifest records explicit runtime and plan topology while excluding inputs and assertion values.
func safeManifest(manifest planmanifest.Manifest) any {
	type safeStep struct {
		ID         string         `json:"id"`
		Tool       string         `json:"tool"`
		Input      map[string]any `json:"input"`
		Assertions []any          `json:"assertions"`
		DependsOn  []string       `json:"depends_on"`
	}
	type safePlan struct {
		ID           string     `json:"id"`
		ConstraintID string     `json:"constraint_id"`
		Steps        []safeStep `json:"steps"`
	}
	plans := make([]safePlan, len(manifest.Plans))
	for n, plan := range manifest.Plans {
		plans[n] = safePlan{ID: plan.ID, ConstraintID: plan.ConstraintID, Steps: make([]safeStep, len(plan.Steps))}
		for i, step := range plan.Steps {
			input := make(map[string]any, len(step.Input))
			for key, value := range step.Input {
				input[key] = safeInputValue(value)
			}
			assertions := make([]any, len(step.Assertions))
			for index, declaration := range step.Assertions {
				assertions[index] = map[string]any{"id": declaration.ID, "definition": safeAssertionDefinition(declaration.Definition)}
			}
			plans[n].Steps[i] = safeStep{ID: step.ID, Tool: step.Tool, Input: input, Assertions: assertions, DependsOn: append([]string(nil), step.DependsOn...)}
		}
	}
	return struct {
		Version int `json:"version"`
		Runtime struct {
			HTTP      planmanifest.HTTPConfig `json:"http"`
			Redaction evidence.Config         `json:"redaction"`
			Policy    struct {
				AllowedTools []string `json:"allowed_tools"`
			} `json:"policy"`
		} `json:"runtime"`
		Plans []safePlan `json:"plans"`
	}{Version: manifest.Version, Runtime: struct {
		HTTP      planmanifest.HTTPConfig `json:"http"`
		Redaction evidence.Config         `json:"redaction"`
		Policy    struct {
			AllowedTools []string `json:"allowed_tools"`
		} `json:"policy"`
	}{HTTP: manifest.HTTP, Redaction: manifest.Redaction, Policy: struct {
		AllowedTools []string `json:"allowed_tools"`
	}{AllowedTools: []string{"http.request"}}}, Plans: plans}
}

// safeInputValue preserves plan structure and reference selectors while hiding every literal value.
func safeInputValue(value planning.InputValue) any {
	if value.Literal != nil {
		return map[string]any{"literal": evidence.Redacted}
	}
	if value.Reference != nil {
		path := make([]any, len(value.Reference.Path))
		for n, segment := range value.Reference.Path {
			if segment.Field != nil {
				path[n] = map[string]any{"field": *segment.Field}
			} else {
				path[n] = map[string]any{"index": *segment.Index}
			}
		}
		return map[string]any{"reference": map[string]any{"step_id": value.Reference.StepID, "path": path, "value_type": value.Reference.ValueType}}
	}
	if value.Object != nil {
		object := make(map[string]any, len(value.Object))
		for key, child := range value.Object {
			object[key] = safeInputValue(child)
		}
		return map[string]any{"object": object}
	}
	array := make([]any, len(value.Array))
	for n, child := range value.Array {
		array[n] = safeInputValue(child)
	}
	return map[string]any{"array": array}
}

// safeAssertionDefinition retains operators and selectors while hiding configured expected values recursively.
func safeAssertionDefinition(definition map[string]any) any {
	if children, ok := definition["all"].([]any); ok {
		safe := make([]any, len(children))
		for n, child := range children {
			safe[n] = safeAssertionDefinition(child.(map[string]any))
		}
		return map[string]any{"all": safe}
	}
	if children, ok := definition["any"].([]any); ok {
		safe := make([]any, len(children))
		for n, child := range children {
			safe[n] = safeAssertionDefinition(child.(map[string]any))
		}
		return map[string]any{"any": safe}
	}
	safe := map[string]any{"operator": definition["operator"], "path": definition["path"]}
	if _, exists := definition["expected"]; exists {
		safe["expected"] = evidence.Redacted
	}
	return safe
}

// syncDirectory flushes completed staging entries before the visibility-changing rename.
func syncDirectory(fs FileSystem, path string) error {
	directory, err := fs.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	err = directory.Sync()
	if runtime.GOOS == "windows" {
		switch fs.(type) {
		case OSFileSystem, *OSFileSystem:
			return nil
		}
	}
	return err
}

// renderMarkdown builds an inert payload-free report with escaped IDs and stable evidence order.
func renderMarkdown(snapshot RunSnapshot) string {
	var b strings.Builder
	b.WriteString("# FeatureProof validation\n\n")
	if snapshot.Binding != nil {
		b.WriteString("Revision binding: `" + escapeMarkdown(snapshot.Binding.BindingSHA256) + "`\n\n")
		b.WriteString("Repository: `" + escapeMarkdown(snapshot.Binding.RepositoryID) + "`\n\n")
		b.WriteString("Commit: `" + escapeMarkdown(snapshot.Binding.CommitOID) + "`\n\n")
		b.WriteString("Environment: `" + escapeMarkdown(snapshot.Binding.EnvironmentID) + "`\n\n")
		b.WriteString("Attestation: `" + escapeMarkdown(snapshot.Binding.AttestationRef) + "`\n\n")
		b.WriteString("Constraints SHA-256: `" + escapeMarkdown(snapshot.Binding.ConstraintsSHA256) + "`\n\n")
		b.WriteString("Contract SHA-256: `" + escapeMarkdown(snapshot.Binding.ContractSHA256) + "`\n\n")
		b.WriteString("Configuration SHA-256: `" + escapeMarkdown(snapshot.Binding.ConfigurationSHA256) + "`\n\n")
		for _, plan := range snapshot.PlanBindings {
			fmt.Fprintf(&b, "Plan batch %d: `%s` (%s)\n\n", plan.Batch, escapeMarkdown(plan.PlanSHA256), escapeMarkdown(plan.Mode))
		}
	}
	b.WriteString("Status: `" + escapeMarkdown(string(snapshot.Status)) + "`\n\n| Constraint | Status | Evidence |\n|---|---|---|\n")
	for _, result := range snapshot.Constraints {
		ids := append([]string(nil), result.EvidenceIDs...)
		for n := range ids {
			ids[n] = escapeMarkdown(ids[n])
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", escapeMarkdown(result.ConstraintID), escapeMarkdown(string(result.Status)), strings.Join(ids, ", "))
	}
	return b.String()
}

// escapeMarkdown neutralizes HTML and table/link syntax in identifiers.
func escapeMarkdown(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "|", "\\|", "[", "\\[", "]", "\\]", "`", "\\`")
	return replacer.Replace(value)
}
