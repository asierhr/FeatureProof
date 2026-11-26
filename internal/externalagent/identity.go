package externalagent

import (
	"context"
	"sync"
)

// authenticatedPrincipalContextKey is private so request payload packages
// cannot forge a transport-authenticated principal context slot.
type authenticatedPrincipalContextKey struct{}

// WithAuthenticatedPrincipal seals a transport-authenticated principal into
// the request context; application payload fields cannot construct this key.
func WithAuthenticatedPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, authenticatedPrincipalContextKey{}, clonePrincipal(principal))
}

// RequestIdentity resolves the principal authenticated at the current trusted
// transport boundary, with an optional host fallback for non-remote surfaces.
type RequestIdentity struct{ Fallback IdentitySource }

// Resolve returns only a sealed request principal when present.
func (identity RequestIdentity) Resolve(ctx context.Context) (Principal, error) {
	if principal, ok := ctx.Value(authenticatedPrincipalContextKey{}).(Principal); ok && principal.ID != "" && principal.Scopes != nil {
		return clonePrincipal(principal), nil
	}
	if identity.Fallback != nil {
		return identity.Fallback.Resolve(ctx)
	}
	return Principal{}, &AppError{Code: CodeUnauthorized}
}

// LocalIdentity keeps a host-resolved principal and supports atomic scope revocation.
type LocalIdentity struct {
	mu        sync.RWMutex
	principal Principal
}

// NewLocalIdentity accepts identity only from trusted host composition.
func NewLocalIdentity(principal Principal) (*LocalIdentity, error) {
	if principal.ID == "" || principal.Scopes == nil {
		return nil, &AppError{Code: CodeUnauthorized}
	}
	return &LocalIdentity{principal: clonePrincipal(principal)}, nil
}

// Resolve returns a defensive snapshot for per-operation authorization.
func (i *LocalIdentity) Resolve(context.Context) (Principal, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return clonePrincipal(i.principal), nil
}

// ReplaceScopes applies protected configuration revocation without trusting a request.
func (i *LocalIdentity) ReplaceScopes(scopes map[string]bool) {
	i.mu.Lock()
	i.principal.Scopes = map[string]bool{}
	for scope, allowed := range scopes {
		i.principal.Scopes[scope] = allowed
	}
	i.mu.Unlock()
}

// clonePrincipal prevents callers from mutating the authorization source.
func clonePrincipal(principal Principal) Principal {
	copy := Principal{ID: principal.ID, Scopes: map[string]bool{}}
	for scope, allowed := range principal.Scopes {
		copy.Scopes[scope] = allowed
	}
	return copy
}

// AliasCatalog stores protected principal-to-selection assignments.
type AliasCatalog struct {
	entries map[string]map[string]Selection
}

// NewAliasCatalog validates aliases and makes defensive catalog copies.
func NewAliasCatalog(entries map[string]map[string]Selection) (*AliasCatalog, error) {
	copy := map[string]map[string]Selection{}
	for principal, aliases := range entries {
		if principal == "" {
			return nil, &AppError{Code: CodeInternal}
		}
		copy[principal] = map[string]Selection{}
		for alias, selection := range aliases {
			if validateAlias(alias) != nil || selection.Alias != alias {
				return nil, &AppError{Code: CodeInternal}
			}
			copy[principal][alias] = selection
		}
	}
	return &AliasCatalog{entries: copy}, nil
}

// Resolve authorizes an alias without accepting its underlying paths or target from the caller.
func (c *AliasCatalog) Resolve(_ context.Context, principal Principal, alias string) (Selection, error) {
	selection, ok := c.entries[principal.ID][alias]
	if !ok {
		return Selection{}, &AppError{Code: CodeUnauthorized}
	}
	return selection, nil
}
