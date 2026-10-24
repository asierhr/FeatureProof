package validationworkflow

import (
	"context"
	"encoding/json"
	"time"

	"github.com/asierhr/featureProof/internal/agentplanning"
	"github.com/asierhr/featureProof/internal/contextassembly"
)

// ContextObserver receives payload-free assembly records from product composition.
type ContextObserver struct{}

// Observe confirms synchronous terminal telemetry without persisting source content.
func (ContextObserver) Observe(ctx context.Context, _ contextassembly.Record) error { return ctx.Err() }

// ContextAssemblyAdapter builds planner producers from a trusted admission snapshot and retains no model capabilities.
type ContextAssemblyAdapter struct {
	Profiles *contextassembly.StaticProfiles
	Registry contextassembly.ProducerRegistry
	Secrets  contextassembly.SecretGate
	Ranker   contextassembly.SemanticRanker
	Tokens   contextassembly.TokenCounter
	Store    contextassembly.Store
	Observer contextassembly.Observer
	Clock    contextassembly.Clock
}

// CurrentContextProducerRegistry resolves sealed producer IDs from the workflow's current durable command and admission authorities.
type CurrentContextProducerRegistry struct {
	Workflows WorkflowStore
	Admission AdmissionBuilder
}

// Resolve rebuilds current source projections from product authorities and never from historical snapshot bindings.
func (r CurrentContextProducerRegistry) Resolve(ctx context.Context, runID string, descriptor contextassembly.RegistryDescriptor, historical contextassembly.AuthorityBinding) ([]contextassembly.Producer, error) {
	if r.Workflows == nil || r.Admission == nil {
		return nil, &Error{Code: CodeInvalid, Operation: "context_registry_composition"}
	}
	record, err := r.Workflows.Load(ctx, runID)
	if err != nil || record.Handle != runID {
		return nil, &Error{Code: CodeConflict, Operation: "context_registry_workflow"}
	}
	current, err := r.Admission.Build(ctx, record.Command)
	if err != nil {
		return nil, mapBoundaryError(ctx, "context_registry_admission", err)
	}
	authority := contextassembly.AuthorityBinding{ProjectID: record.Command.RepositoryID, RepositoryID: record.Command.RepositoryID, Revision: record.Command.Revision, ConfigurationDigest: current.RuntimeDigest, ProfileVersion: descriptor.ProfileVersion, InstructionVersion: descriptor.InstructionVersion, Provider: current.Provider, Model: current.Model}
	if historical.ProjectID != authority.ProjectID || historical.RepositoryID != authority.RepositoryID {
		return nil, &Error{Code: CodeConflict, Operation: "context_registry_authority"}
	}
	return buildContextProducers(authority, current, record.Command)
}

// buildContextProducers constructs the complete product registry from current admitted values under one authority binding.
func buildContextProducers(authority contextassembly.AuthorityBinding, snapshot AdmissionSnapshot, command StartCommand) ([]contextassembly.Producer, error) {
	constraints, err := contextassembly.ConstraintProducer(authority, snapshot.Generation.Constraints)
	if err != nil {
		return nil, &Error{Code: CodeInvalid, Operation: "context_constraints"}
	}
	contract, err := contextassembly.ContractProducer(authority, snapshot.Generation.Contract)
	if err != nil {
		return nil, &Error{Code: CodeInvalid, Operation: "context_contract"}
	}
	capabilities, err := contextassembly.CapabilityProducer(authority, snapshot.Generation.Capabilities)
	if err != nil {
		return nil, &Error{Code: CodeInvalid, Operation: "context_capabilities"}
	}
	configurationPayload, _ := json.Marshal(map[string]string{"runtime_digest": snapshot.RuntimeDigest, "catalog_digest": snapshot.CatalogDigest, "contract_policy_digest": snapshot.ContractPolicyDigest})
	configurationItem := contextassembly.Item{ID: "approved-configuration", Kind: contextassembly.SourceConfig, Trust: contextassembly.TrustConfiguration, Redaction: contextassembly.RedactionVerified, Authority: contextassembly.AuthorityApproved, SafeReference: "configuration:runtime", ProjectID: authority.ProjectID, RepositoryID: authority.RepositoryID, Revision: authority.Revision, Scope: "revision", State: "approved", Payload: configurationPayload}
	configurationProjection, err := contextassembly.NewProjection(contextassembly.SourceConfig, "validationworkflow.configuration", "v1", contextassembly.CompletenessComplete, []contextassembly.Item{configurationItem})
	if err != nil {
		return nil, &Error{Code: CodeInvalid, Operation: "context_configuration"}
	}
	producers := []contextassembly.Producer{constraints, contract, capabilities, &contextassembly.StaticProducer{Source: contextassembly.SourceConfig, Projection: configurationProjection, Current: configurationProjection.Binding}}
	if command.MemoryBinding != nil {
		memory, memoryErr := contextassembly.MemoryProducer(authority, command.MemoryDescriptors, *command.MemoryBinding)
		if memoryErr != nil {
			return nil, &Error{Code: CodeInvalid, Operation: "context_memory"}
		}
		producers = append(producers, memory)
	}
	return producers, nil
}

// RoleContextRequest contains only composition-owned identities and neutral producers for a non-planner projection.
type RoleContextRequest struct {
	Key, RunID, PlanID, ContextDigest string
	Role                              contextassembly.Role
}

// productRoleConsumer enforces that composition never delivers one role's projection to another consumer.
type productRoleConsumer struct{ role contextassembly.Role }

// Consume accepts only the exact role sealed into this composition-owned consumer.
func (c productRoleConsumer) Consume(ctx context.Context, snapshot contextassembly.Snapshot) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if snapshot.Role != c.role {
		return &Error{Code: CodeUnauthorized, Operation: "context_role_isolation"}
	}
	return nil
}

// ConsumeRole assembles analyzer or diagnostic context through the same controlled product boundary as planning.
func (a *ContextAssemblyAdapter) ConsumeRole(ctx context.Context, request RoleContextRequest, consumer contextassembly.Consumer) (string, error) {
	if a == nil || a.Profiles == nil || a.Secrets == nil || a.Tokens == nil || a.Store == nil || a.Observer == nil || a.Clock == nil || (request.Role != contextassembly.RoleAnalyzer && request.Role != contextassembly.RoleDiagnostic) {
		return "", &Error{Code: CodeInvalid, Operation: "context_role_composition"}
	}
	if consumer == nil {
		return "", &Error{Code: CodeInvalid, Operation: "context_role_consumer"}
	}
	snapshot, err := a.Store.LoadSnapshot(ctx, request.ContextDigest)
	if err != nil || snapshot.Digest != request.ContextDigest || snapshot.RunID != request.RunID || snapshot.PlanID != request.PlanID || snapshot.Role != request.Role {
		return "", &Error{Code: CodeInvalid, Operation: "context_role_profile"}
	}
	profile, ok := a.Profiles.Profile(request.Role)
	if !ok || snapshot.Registry.ProfileVersion != profile.Version || snapshot.Registry.InstructionVersion != profile.InstructionVersion {
		return "", &Error{Code: CodeInvalid, Operation: "context_role_profile"}
	}
	if err = consumer.Consume(ctx, snapshot); err != nil {
		return "", mapBoundaryError(ctx, "context_role_consume", err)
	}
	return snapshot.Digest, nil
}

// assembleRole persists one composition-owned non-planner projection before delivering it to its fixed consumer.
func (a *ContextAssemblyAdapter) assembleRole(ctx context.Context, handle, planID string, role contextassembly.Role, base contextassembly.AuthorityBinding, producers []contextassembly.Producer) (string, error) {
	profile, ok := a.Profiles.Profile(role)
	if !ok {
		return "", &Error{Code: CodeInvalid, Operation: "context_role_profile"}
	}
	authority := base
	authority.ProfileVersion, authority.InstructionVersion = profile.Version, profile.InstructionVersion
	assembler, err := contextassembly.NewAssembler(contextassembly.Config{ObserverTimeout: time.Second}, contextassembly.Dependencies{Profiles: a.Profiles, Producers: producers, Secrets: a.Secrets, Ranker: a.Ranker, Tokens: a.Tokens, Store: a.Store, Observer: a.Observer, Clock: a.Clock})
	if err != nil {
		return "", &Error{Code: CodeInvalid, Operation: "context_role_composition"}
	}
	request := contextassembly.Request{Version: contextassembly.SnapshotVersion, IdempotencyKey: handle + ":" + string(role), RunID: handle, PlanID: planID, Role: role, Authority: authority, Limits: profile.Budgets, MaxSensitivity: profile.MaxSensitivity}
	snapshot, err := assembler.Assemble(ctx, request)
	if err != nil {
		return "", mapBoundaryError(ctx, "context_role_assemble", err)
	}
	consumer := productRoleConsumer{role: role}
	if err = consumer.Consume(ctx, snapshot); err != nil {
		return "", mapBoundaryError(ctx, "context_role_consume", err)
	}
	return snapshot.Digest, nil
}

// Prepare constructs the exact planner snapshot from admission-owned values and ignores caller context claims.
func (a *ContextAssemblyAdapter) Prepare(ctx context.Context, handle string, snapshot AdmissionSnapshot, command StartCommand) (ContextAssemblySelection, error) {
	if a == nil || a.Profiles == nil || a.Secrets == nil || a.Tokens == nil || a.Store == nil || a.Observer == nil || a.Clock == nil {
		return ContextAssemblySelection{}, &Error{Code: CodeInvalid, Operation: "context_composition"}
	}
	profile, ok := a.Profiles.Profile(contextassembly.RolePlanner)
	if !ok {
		return ContextAssemblySelection{}, &Error{Code: CodeInvalid, Operation: "context_profile"}
	}
	authority := contextassembly.AuthorityBinding{ProjectID: command.RepositoryID, RepositoryID: command.RepositoryID, Revision: command.Revision, ConfigurationDigest: snapshot.RuntimeDigest, ProfileVersion: profile.Version, InstructionVersion: profile.InstructionVersion, Provider: snapshot.Provider, Model: snapshot.Model}
	producers, err := buildContextProducers(authority, snapshot, command)
	if err != nil {
		return ContextAssemblySelection{}, err
	}
	assembler, err := contextassembly.NewAssembler(contextassembly.Config{ObserverTimeout: time.Second}, contextassembly.Dependencies{Profiles: a.Profiles, Producers: producers, Secrets: a.Secrets, Ranker: a.Ranker, Tokens: a.Tokens, Store: a.Store, Observer: a.Observer, Clock: a.Clock})
	if err != nil {
		return ContextAssemblySelection{}, &Error{Code: CodeInvalid, Operation: "context_composition"}
	}
	request := contextassembly.Request{Version: contextassembly.SnapshotVersion, IdempotencyKey: handle + ":planner", RunID: handle, PlanID: snapshot.Generation.PlanID, Role: contextassembly.RolePlanner, Authority: authority, Limits: profile.Budgets, MaxSensitivity: profile.MaxSensitivity}
	assembled, err := assembler.Assemble(ctx, request)
	if err != nil || assembled.Completeness == contextassembly.CompletenessInsufficient {
		if err != nil {
			return ContextAssemblySelection{}, err
		}
		return ContextAssemblySelection{}, &Error{Code: CodeInsufficientContext, Operation: "context_assembly"}
	}
	envelope, err := contextassembly.Envelope(assembled)
	if err != nil {
		return ContextAssemblySelection{}, &Error{Code: CodeInvalid, Operation: "context_envelope"}
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return ContextAssemblySelection{}, &Error{Code: CodeInternal, Operation: "context_envelope"}
	}
	for _, role := range []contextassembly.Role{contextassembly.RoleAnalyzer, contextassembly.RoleDiagnostic} {
		if _, roleErr := a.assembleRole(ctx, handle, snapshot.Generation.PlanID, role, authority, producers); roleErr != nil {
			return ContextAssemblySelection{}, roleErr
		}
	}
	return ContextAssemblySelection{Instructions: envelope.Instructions, Envelope: raw, Digest: assembled.Digest, Seal: envelope.Seal}, nil
}

// Bind seals the planner snapshot to the private prepared-plan digest before approval is exposed.
func (a *ContextAssemblyAdapter) Bind(ctx context.Context, key, planID string, prepared agentplanning.PreparedSnapshot, contextDigest string) (string, error) {
	planDigest, err := contextassembly.PreparedPlanDigest(prepared)
	if err != nil {
		return "", &Error{Code: CodeInternal, Operation: "context_plan_digest"}
	}
	assembler, snapshot, rebuildErr := a.revalidationAssembler(ctx, contextDigest)
	if rebuildErr != nil || snapshot.PlanID != planID {
		return "", &Error{Code: CodeConflict, Operation: "context_snapshot"}
	}
	binding, err := assembler.Bind(ctx, key, planID, planDigest, contextDigest)
	if err != nil {
		return "", &Error{Code: CodeConflict, Operation: "context_binding"}
	}
	return binding.Digest, nil
}

// Revalidate loads one immutable binding and asks its current source producers to confirm authority.
func (a *ContextAssemblyAdapter) Revalidate(ctx context.Context, bindingDigest string) (contextassembly.RevalidationResult, error) {
	if a == nil || a.Store == nil {
		return contextassembly.RevalidationResult{}, &Error{Code: CodeInvalid, Operation: "context_composition"}
	}
	binding, err := a.Store.LoadBinding(ctx, bindingDigest)
	if err != nil {
		return contextassembly.RevalidationResult{}, &Error{Code: CodeConflict, Operation: "context_binding"}
	}
	assembler, snapshot, rebuildErr := a.revalidationAssembler(ctx, binding.ContextDigest)
	if rebuildErr != nil || snapshot.Registry.Digest != binding.Registry.Digest {
		return contextassembly.RevalidationResult{}, &Error{Code: CodeConflict, Operation: "context_snapshot"}
	}
	return assembler.Revalidate(ctx, binding)
}

// revalidationAssembler rebuilds the producer registry from the sealed durable snapshot and current composition profiles.
func (a *ContextAssemblyAdapter) revalidationAssembler(ctx context.Context, contextDigest string) (*contextassembly.Assembler, contextassembly.Snapshot, error) {
	if a == nil || a.Profiles == nil || a.Registry == nil || a.Secrets == nil || a.Tokens == nil || a.Store == nil || a.Observer == nil || a.Clock == nil {
		return nil, contextassembly.Snapshot{}, &Error{Code: CodeInvalid, Operation: "context_composition"}
	}
	snapshot, err := a.Store.LoadSnapshot(ctx, contextDigest)
	if err != nil {
		return nil, contextassembly.Snapshot{}, err
	}
	profile, ok := a.Profiles.Profile(snapshot.Role)
	if !ok || profile.Version != snapshot.Registry.ProfileVersion || profile.InstructionVersion != snapshot.Registry.InstructionVersion {
		return nil, contextassembly.Snapshot{}, &Error{Code: CodeConflict, Operation: "context_registry_profile"}
	}
	producers, err := contextassembly.RevalidationProducers(ctx, snapshot, a.Registry)
	if err != nil {
		return nil, contextassembly.Snapshot{}, err
	}
	assembler, err := contextassembly.NewAssembler(contextassembly.Config{ObserverTimeout: time.Second}, contextassembly.Dependencies{Profiles: a.Profiles, Producers: producers, Secrets: a.Secrets, Ranker: a.Ranker, Tokens: a.Tokens, Store: a.Store, Observer: a.Observer, Clock: a.Clock})
	return assembler, snapshot, err
}
