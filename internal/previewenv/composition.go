package previewenv

import "context"

// SecretSource resolves opaque references only at the trusted composition boundary.
type SecretSource interface {
	Resolve(context.Context, string) ([]byte, error)
}

// ProviderFactory constructs an authenticated provider transport from ephemeral secret bytes.
type ProviderFactory interface {
	Build([]byte) (Provider, error)
}

// ExecutorFactory constructs an authorized application transport from ephemeral secret bytes.
type ExecutorFactory interface {
	Build([]byte) (Executor, error)
}

// ProviderComposition configures one provider transport without exposing secret bytes downstream.
type ProviderComposition struct {
	ProviderID, CredentialReference string
	Secretless                      bool
	Factory                         ProviderFactory
}

// ProfileComposition configures one execution profile transport at composition time.
type ProfileComposition struct {
	ProfileID, CredentialReference string
	Secretless                     bool
	Factory                        ExecutorFactory
}

// TrustedComposition contains transports already constructed with their minimum credentials.
type TrustedComposition struct {
	Providers map[string]Provider
	Executor  Executor
}

// ComposeTrusted resolves secrets only while constructing specific authorized transports.
func ComposeTrusted(ctx context.Context, source SecretSource, selection Selection, providers []ProviderComposition, profiles []ProfileComposition) (TrustedComposition, error) {
	if source == nil {
		return TrustedComposition{}, &Error{Code: CodeInvalid, Operation: "secret_source"}
	}
	result := TrustedComposition{Providers: map[string]Provider{}}
	providerMatched := false
	for _, configured := range providers {
		if configured.ProviderID != selection.ProviderID {
			continue
		}
		if providerMatched {
			return TrustedComposition{}, &Error{Code: CodeConflict, Operation: "provider_composition"}
		}
		providerMatched = true
		if configured.Factory == nil || !safeID.MatchString(configured.ProviderID) || selection.Secretless && !configured.Secretless || (configured.Secretless && configured.CredentialReference != "") || (!configured.Secretless && configured.CredentialReference == "") {
			return TrustedComposition{}, &Error{Code: CodeInvalid, Operation: "provider_composition"}
		}
		secret, err := resolveSecret(ctx, source, configured.CredentialReference, configured.Secretless)
		if err != nil {
			return TrustedComposition{}, err
		}
		provider, err := configured.Factory.Build(secret)
		clear(secret)
		if err != nil || provider == nil {
			return TrustedComposition{}, &Error{Code: CodeUnauthorized, Operation: "provider_transport"}
		}
		result.Providers[configured.ProviderID] = provider
	}
	if !providerMatched {
		return TrustedComposition{}, &Error{Code: CodeUnauthorized, Operation: "provider_composition"}
	}
	router := &profileRouter{executors: map[string]Executor{}}
	profileMatched := false
	for _, configured := range profiles {
		if configured.ProfileID != selection.ProfileID {
			continue
		}
		if profileMatched {
			return TrustedComposition{}, &Error{Code: CodeConflict, Operation: "profile_composition"}
		}
		profileMatched = true
		if configured.Factory == nil || !safeID.MatchString(configured.ProfileID) || selection.Secretless && !configured.Secretless || (configured.Secretless && configured.CredentialReference != "") || (!configured.Secretless && configured.CredentialReference == "") {
			return TrustedComposition{}, &Error{Code: CodeInvalid, Operation: "profile_composition"}
		}
		secret, err := resolveSecret(ctx, source, configured.CredentialReference, configured.Secretless)
		if err != nil {
			return TrustedComposition{}, err
		}
		executor, err := configured.Factory.Build(secret)
		clear(secret)
		if err != nil || executor == nil {
			return TrustedComposition{}, &Error{Code: CodeUnauthorized, Operation: "execution_transport"}
		}
		router.executors[configured.ProfileID] = executor
	}
	if !profileMatched {
		return TrustedComposition{}, &Error{Code: CodeUnauthorized, Operation: "profile_composition"}
	}
	result.Executor = router
	return result, nil
}

// resolveSecret enforces that secretless profiles never invoke the resolver.
func resolveSecret(ctx context.Context, source SecretSource, reference string, secretless bool) ([]byte, error) {
	if secretless {
		return nil, nil
	}
	secret, err := source.Resolve(ctx, reference)
	if err != nil || len(secret) == 0 {
		return nil, &Error{Code: CodeUnauthorized, Operation: "credentials"}
	}
	return append([]byte(nil), secret...), nil
}

// profileRouter dispatches execution only by the trusted catalog profile ID.
type profileRouter struct{ executors map[string]Executor }

// Execute selects an already-composed authorized transport without resolving credentials.
func (r *profileRouter) Execute(ctx context.Context, execution AuthorizedExecution) (ExecutionResult, error) {
	executor := r.executors[execution.Selection.ProfileID]
	if executor == nil {
		return ExecutionResult{}, &Error{Code: CodeUnauthorized, Operation: "profile"}
	}
	return executor.Execute(ctx, execution)
}
