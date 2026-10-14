// Package oauth owns OpenAI Sign in with ChatGPT profiles and secret token records.
package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/asierhr/featureProof/internal/model"
)

const (
	registrationClientID = "dynamic_agent_client"
	resource             = "https://api.openai.com/v1"
	requiredDirectScope  = "chatgpt.tokens.use.direct"
	sharedRefreshTimeout = 30 * time.Second
)

var requiredScopes = []string{"openid", "profile", "email", "offline_access", "resource.invoke", requiredDirectScope}

// ErrProfileNotFound distinguishes a new registration from credential-store failure.
var ErrProfileNotFound = errors.New("oauth profile not found")

// Config supplies stable public OAuth identity and callback configuration.
type Config struct {
	HostID, AppName, RedirectURI string
}

// Callback contains the bounded values delivered by one loopback authorization attempt.
type Callback struct{ State, Code, IssuedClientID string }

// RegistrationRequest contains public dynamic-client registration parameters.
type RegistrationRequest struct {
	ClientID, HostID, AppName, RedirectURI, State, Nonce, Challenge, IDTokenHint string
	Scopes                                                                       []string
	Resource                                                                     string
}

// ExchangeRequest contains public OAuth exchange parameters plus the one-time verifier.
type ExchangeRequest struct {
	Code, ClientID, RedirectURI, Verifier, Resource string
}

// TokenResponse is a transient response visible only inside the OAuth package boundary.
type TokenResponse struct {
	AccessToken, RefreshToken, IDToken, Scope string
	ExpiresIn                                 time.Duration
}

// Identity is the validated stable account subject stored without claims or raw ID token.
type Identity struct{ Subject string }

// Browser opens the authorization URL constructed for one sign-in attempt.
type Browser interface {
	Open(context.Context, string) error
}

// CallbackListener waits for one exact loopback redirect and supports cancellation.
type CallbackListener interface {
	Await(context.Context, string) (Callback, error)
}

// Gateway performs single-attempt OAuth protocol operations without retrying.
type Gateway interface {
	Register(context.Context, RegistrationRequest) (string, error)
	Exchange(context.Context, ExchangeRequest) (TokenResponse, error)
	Refresh(context.Context, string, string, string) (TokenResponse, error)
	Revoke(context.Context, string, string) error
}

// Random supplies fresh opaque state, nonce and PKCE verifier values.
type Random interface{ Value(int) (string, error) }

// Clock supplies expiry timestamps without binding tests to wall time.
type Clock interface{ Now() time.Time }

// IDTokenValidator validates issuer, audience, expiry and nonce outside service logic.
type IDTokenValidator interface {
	Validate(context.Context, string, string, string) (Identity, error)
}

// tokenRecord is the secret profile state that cannot cross this package boundary.
type tokenRecord struct {
	ProfileID, ClientID, Subject       string
	AccessToken, RefreshToken, IDToken string
	Scopes                             []string
	ExpiresAt                          time.Time
}

// credentialStore persists complete secret records atomically inside the auth boundary.
type credentialStore interface {
	Load(context.Context, string) (tokenRecord, error)
	Save(context.Context, tokenRecord) error
	Delete(context.Context, string) error
}

// Service coordinates sign-in, refresh and revocation for isolated local profiles.
type Service struct {
	cfg       Config
	browser   Browser
	listener  CallbackListener
	gateway   Gateway
	random    Random
	clock     Clock
	validator IDTokenValidator
	store     credentialStore
	mu        sync.Mutex
	refreshes map[string]*refreshCall
}

// refreshCall lets concurrent callers wait for one rotating-token exchange.
type refreshCall struct {
	done    chan struct{}
	cancel  context.CancelFunc
	err     error
	waiters int
}

// NewService validates composition without resolving or exposing credentials.
func NewService(cfg Config, browser Browser, listener CallbackListener, gateway Gateway, random Random, clock Clock, validator IDTokenValidator, store credentialStore) (*Service, error) {
	callback, err := url.Parse(cfg.RedirectURI)
	if err != nil || callback.Scheme != "http" || callback.Hostname() != "127.0.0.1" || cfg.HostID == "" || cfg.AppName == "" || browser == nil || listener == nil || gateway == nil || random == nil || clock == nil || validator == nil || store == nil {
		return nil, &model.Error{Category: model.CategoryInvalidConfig, Operation: "configure oauth"}
	}
	return &Service{cfg: cfg, browser: browser, listener: listener, gateway: gateway, random: random, clock: clock, validator: validator, store: store, refreshes: map[string]*refreshCall{}}, nil
}

// NewResolver composes non-interactive profile resolution for refresh and inference.
func NewResolver(gateway Gateway, clock Clock, validator IDTokenValidator, store credentialStore) (*Service, error) {
	if gateway == nil || clock == nil || validator == nil || store == nil {
		return nil, &model.Error{Category: model.CategoryInvalidConfig, Operation: "configure oauth resolver"}
	}
	return &Service{gateway: gateway, clock: clock, validator: validator, store: store, refreshes: map[string]*refreshCall{}}, nil
}

// SignIn provisions or replaces one profile only after complete protocol validation.
func (s *Service) SignIn(ctx context.Context, profileID string) error {
	if err := ctx.Err(); err != nil {
		return ctxModelError("start sign-in", err)
	}
	if profileID == "" {
		return authError("start sign-in")
	}
	state, nonce, verifier, err := s.attemptValues()
	if err != nil {
		return authError("create sign-in attempt")
	}
	challenge := pkceChallenge(verifier)
	clientID, appName, idTokenHint := registrationClientID, s.cfg.AppName, ""
	if current, loadErr := s.store.Load(ctx, profileID); loadErr == nil {
		clientID, appName, idTokenHint = current.ClientID, "", current.IDToken
	} else if !errors.Is(loadErr, ErrProfileNotFound) {
		return oauthBoundaryError(ctx, "load profile for sign-in", loadErr, model.CategoryInternal)
	}
	authorizationURL, err := s.gateway.Register(ctx, RegistrationRequest{ClientID: clientID, HostID: s.cfg.HostID, AppName: appName, RedirectURI: s.cfg.RedirectURI, State: state, Nonce: nonce, Challenge: challenge, IDTokenHint: idTokenHint, Scopes: append([]string(nil), requiredScopes...), Resource: resource})
	if err != nil || authorizationURL == "" {
		if err != nil {
			return oauthBoundaryError(ctx, "register oauth client", err, model.CategoryAuthentication)
		}
		return authError("register oauth client")
	}
	if err := s.browser.Open(ctx, authorizationURL); err != nil {
		return oauthBoundaryError(ctx, "open authorization", err, model.CategoryAuthentication)
	}
	callback, err := s.listener.Await(ctx, s.cfg.RedirectURI)
	issuedClientID := callback.IssuedClientID
	if clientID != registrationClientID && issuedClientID == "" {
		issuedClientID = clientID
	}
	if err != nil {
		return oauthBoundaryError(ctx, "validate callback", err, model.CategoryAuthentication)
	}
	if callback.State != state || callback.Code == "" || issuedClientID == "" || issuedClientID == registrationClientID || (clientID != registrationClientID && issuedClientID != clientID) {
		return authError("validate callback")
	}
	tokens, err := s.gateway.Exchange(ctx, ExchangeRequest{Code: callback.Code, ClientID: issuedClientID, RedirectURI: s.cfg.RedirectURI, Verifier: verifier, Resource: resource})
	if err != nil {
		return oauthBoundaryError(ctx, "exchange authorization", err, model.CategoryAuthentication)
	}
	return s.activate(ctx, profileID, issuedClientID, nonce, tokens)
}

// AccessToken resolves one profile at composition time and coalesces refresh when expired.
func (s *Service) AccessToken(ctx context.Context, profileID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", ctxModelError("load profile", err)
	}
	record, err := s.store.Load(ctx, profileID)
	if err != nil {
		if errors.Is(err, ErrProfileNotFound) {
			return "", authError("load profile")
		}
		return "", oauthBoundaryError(ctx, "load profile", err, model.CategoryInternal)
	}
	if !validRecord(record) {
		return "", authError("load profile")
	}
	if s.clock.Now().Before(record.ExpiresAt) {
		return record.AccessToken, nil
	}
	if err := s.refresh(ctx, profileID); err != nil {
		return "", err
	}
	record, err = s.store.Load(ctx, profileID)
	if err != nil {
		return "", oauthBoundaryError(ctx, "load refreshed profile", err, model.CategoryInternal)
	}
	if !validRecord(record) || !s.clock.Now().Before(record.ExpiresAt) {
		return "", authError("load refreshed profile")
	}
	return record.AccessToken, nil
}

// SignOut revokes the selected profile and deletes it only after explicit provider success.
func (s *Service) SignOut(ctx context.Context, profileID string) error {
	if err := ctx.Err(); err != nil {
		return ctxModelError("load profile for sign-out", err)
	}
	record, err := s.store.Load(ctx, profileID)
	if err != nil {
		if errors.Is(err, ErrProfileNotFound) {
			return authError("load profile for sign-out")
		}
		return oauthBoundaryError(ctx, "load profile for sign-out", err, model.CategoryInternal)
	}
	if !validRecord(record) {
		return authError("load profile for sign-out")
	}
	if err := s.gateway.Revoke(ctx, record.ClientID, record.RefreshToken); err != nil {
		if clearErr := s.clearTokens(context.WithoutCancel(ctx), record); clearErr != nil {
			return clearErr
		}
		return oauthBoundaryError(ctx, "revoke profile", err, model.CategoryAuthentication)
	}
	return s.clearTokens(ctx, record)
}

// activate validates identity, scope and token completeness before one atomic store commit.
func (s *Service) activate(ctx context.Context, profileID, clientID, nonce string, tokens TokenResponse) error {
	if tokens.AccessToken == "" || tokens.RefreshToken == "" || tokens.IDToken == "" || tokens.ExpiresIn <= 0 || !hasScope(tokens.Scope, requiredDirectScope) {
		return authError("validate tokens")
	}
	identity, err := s.validator.Validate(ctx, tokens.IDToken, clientID, nonce)
	if err != nil {
		return oauthBoundaryError(ctx, "validate identity", err, model.CategoryAuthentication)
	}
	if identity.Subject == "" {
		return authError("validate identity")
	}
	record := tokenRecord{ProfileID: profileID, ClientID: clientID, Subject: identity.Subject, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, IDToken: tokens.IDToken, Scopes: strings.Fields(tokens.Scope), ExpiresAt: s.clock.Now().Add(tokens.ExpiresIn)}
	if err := s.store.Save(ctx, record); err != nil {
		return oauthBoundaryError(ctx, "save oauth profile", err, model.CategoryInternal)
	}
	return nil
}

// refresh ensures a rotating refresh token is exchanged once for concurrent callers.
func (s *Service) refresh(ctx context.Context, profileID string) error {
	s.mu.Lock()
	running := s.refreshes[profileID]
	if running == nil {
		sharedCtx, cancel := sharedRefreshContext(ctx)
		running = &refreshCall{done: make(chan struct{}), cancel: cancel}
		s.refreshes[profileID] = running
		go s.runRefresh(sharedCtx, profileID, running)
	}
	running.waiters++
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		s.releaseRefresh(profileID, running)
		return ctxModelError("wait for refresh", ctx.Err())
	case <-running.done:
		if err := ctx.Err(); err != nil {
			s.releaseRefresh(profileID, running)
			return ctxModelError("wait for refresh", err)
		}
		s.releaseRefresh(profileID, running)
		return running.err
	}
}

// sharedRefreshContext gives coalesced work an independent finite lifetime while preserving caller values.
func sharedRefreshContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), sharedRefreshTimeout)
}

// runRefresh owns one coalesced refresh and publishes its result to every active waiter.
func (s *Service) runRefresh(ctx context.Context, profileID string, running *refreshCall) {
	err := s.performRefresh(ctx, profileID)
	s.mu.Lock()
	running.err = err
	if s.refreshes[profileID] == running {
		delete(s.refreshes, profileID)
	}
	close(running.done)
	if running.cancel != nil {
		running.cancel()
	}
	s.mu.Unlock()
}

// releaseRefresh removes one caller and cancels shared work only after its last active waiter leaves.
func (s *Service) releaseRefresh(profileID string, running *refreshCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if running.waiters > 0 {
		running.waiters--
	}
	if running.waiters == 0 && s.refreshes[profileID] == running {
		if running.cancel != nil {
			running.cancel()
		}
	}
}

// performRefresh performs network and store I/O without holding the coordinator lock.
func (s *Service) performRefresh(ctx context.Context, profileID string) error {
	if err := ctx.Err(); err != nil {
		return ctxModelError("load refresh profile", err)
	}
	record, err := s.store.Load(ctx, profileID)
	if err != nil {
		if errors.Is(err, ErrProfileNotFound) {
			return authError("load refresh profile")
		}
		return oauthBoundaryError(ctx, "load refresh profile", err, model.CategoryInternal)
	}
	if !validRecord(record) {
		return authError("load refresh profile")
	}
	if s.clock.Now().Before(record.ExpiresAt) {
		return nil
	}
	tokens, err := s.gateway.Refresh(ctx, record.ClientID, record.RefreshToken, resource)
	if err != nil {
		var typed *model.Error
		if errors.As(err, &typed) && typed.Category == model.CategoryAuthentication {
			if clearErr := s.clearTokens(context.WithoutCancel(ctx), record); clearErr != nil {
				return clearErr
			}
		}
		return oauthBoundaryError(ctx, "refresh profile", err, model.CategoryAuthentication)
	}
	if tokens.IDToken == "" {
		return authError("validate refreshed identity")
	}
	identity, err := s.validator.Validate(ctx, tokens.IDToken, record.ClientID, "")
	if err != nil {
		return oauthBoundaryError(ctx, "validate refreshed identity", err, model.CategoryAuthentication)
	}
	if identity.Subject != record.Subject || tokens.AccessToken == "" || tokens.RefreshToken == "" || tokens.ExpiresIn <= 0 || !hasScope(tokens.Scope, requiredDirectScope) {
		return authError("validate refreshed identity")
	}
	updated := tokenRecord{ProfileID: profileID, ClientID: record.ClientID, Subject: record.Subject, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, IDToken: tokens.IDToken, Scopes: strings.Fields(tokens.Scope), ExpiresAt: s.clock.Now().Add(tokens.ExpiresIn)}
	if err := s.store.Save(ctx, updated); err != nil {
		return oauthBoundaryError(ctx, "save refreshed profile", err, model.CategoryInternal)
	}
	return nil
}

// attemptValues obtains distinct fresh values for one OAuth attempt.
func (s *Service) attemptValues() (string, string, string, error) {
	state, err := s.random.Value(32)
	if err != nil {
		return "", "", "", err
	}
	nonce, err := s.random.Value(32)
	if err != nil {
		return "", "", "", err
	}
	verifier, err := s.random.Value(64)
	return state, nonce, verifier, err
}

// pkceChallenge derives the OAuth S256 challenge from a one-time verifier.
func pkceChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// hasScope matches an exact scope in the provider's space-delimited grant.
func hasScope(scopes, required string) bool {
	for _, scope := range strings.Fields(scopes) {
		if scope == required {
			return true
		}
	}
	return false
}

// validRecord rejects partial or mismatched credential records before use.
func validRecord(record tokenRecord) bool {
	return validRegistration(record) && record.AccessToken != "" && record.RefreshToken != "" && !record.ExpiresAt.IsZero() && hasScope(strings.Join(record.Scopes, " "), requiredDirectScope)
}

// validRegistration preserves the issued-client/account mapping independently of live tokens.
func validRegistration(record tokenRecord) bool {
	return record.ProfileID != "" && record.ClientID != "" && record.Subject != ""
}

// clearTokens retains registration identity while stopping local inference immediately.
func (s *Service) clearTokens(ctx context.Context, record tokenRecord) error {
	record.AccessToken, record.RefreshToken, record.IDToken = "", "", ""
	record.Scopes, record.ExpiresAt = nil, time.Time{}
	if err := s.store.Save(ctx, record); err != nil {
		return oauthBoundaryError(ctx, "clear oauth profile", err, model.CategoryInternal)
	}
	return nil
}

// authError returns constant authentication diagnostics without provider values.
func authError(operation string) error {
	return &model.Error{Category: model.CategoryAuthentication, Operation: operation}
}

// ctxModelError preserves only standard context causes from OAuth boundaries.
func ctxModelError(operation string, err error) error {
	category := model.CategoryCancelled
	if errors.Is(err, context.DeadlineExceeded) {
		category = model.CategoryTimeout
	}
	return &model.Error{Category: category, Operation: operation, Cause: err}
}

// oauthBoundaryError preserves safe typed categories and gives exact context causes precedence over fallbacks.
func oauthBoundaryError(ctx context.Context, operation string, err error, fallback model.Category) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxModelError(operation, ctxErr)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ctxModelError(operation, err)
	}
	var typed *model.Error
	if errors.As(err, &typed) {
		return typed
	}
	return &model.Error{Category: fallback, Operation: operation}
}

// RequiredProtocol exposes public non-secret constants for contract tests and gateways.
func RequiredProtocol() (string, string, []string) {
	return registrationClientID, resource, append([]string(nil), requiredScopes...)
}
