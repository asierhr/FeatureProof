package oauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/asierhr/featureProof/internal/model"
)

const (
	authorizationEndpoint = "https://auth.openai.com/api/accounts/authorize"
	tokenEndpoint         = "https://auth.openai.com/api/accounts/oauth/token"
	discoveryEndpoint     = "https://auth.openai.com/.well-known/openid-configuration"
)

// HTTPDoer is the single-attempt HTTP boundary used by the OAuth gateway.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// HTTPGateway implements the documented public-client OAuth requests without retries.
type HTTPGateway struct{ client HTTPDoer }

// NewHTTPGateway wires an explicit HTTP client for OAuth protocol operations.
func NewHTTPGateway(client HTTPDoer) (*HTTPGateway, error) {
	if client == nil {
		return nil, &model.Error{Category: model.CategoryInvalidConfig, Operation: "configure oauth http"}
	}
	return &HTTPGateway{client: client}, nil
}

// Register constructs the first dynamic-client authorization URL from public values.
func (g *HTTPGateway) Register(ctx context.Context, request RegistrationRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", ctxModelError("build registration authorization", err)
	}
	if request.ClientID == "" || request.HostID == "" || request.State == "" || request.Nonce == "" || request.Challenge == "" || request.Resource != resource || (request.ClientID == registrationClientID && request.AppName == "") || (request.ClientID != registrationClientID && request.AppName != "") {
		return "", authError("build registration authorization")
	}
	query := url.Values{
		"client_id":             {request.ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {request.RedirectURI},
		"scope":                 {strings.Join(request.Scopes, " ")},
		"resource":              {request.Resource},
		"state":                 {request.State},
		"nonce":                 {request.Nonce},
		"code_challenge":        {request.Challenge},
		"code_challenge_method": {"S256"},
		"ext_agent_host_id":     {request.HostID},
	}
	if request.AppName != "" {
		query.Set("agent_name_hint", request.AppName)
	}
	if request.IDTokenHint != "" {
		query.Set("id_token_hint", request.IDTokenHint)
	}
	return authorizationEndpoint + "?" + query.Encode(), nil
}

// Exchange trades one authorization code using the callback-issued client ID.
func (g *HTTPGateway) Exchange(ctx context.Context, request ExchangeRequest) (TokenResponse, error) {
	return g.token(ctx, url.Values{"grant_type": {"authorization_code"}, "client_id": {request.ClientID}, "code": {request.Code}, "code_verifier": {request.Verifier}, "redirect_uri": {request.RedirectURI}, "resource": {request.Resource}})
}

// Refresh exchanges one rotating refresh token and omits scope to retain the grant.
func (g *HTTPGateway) Refresh(ctx context.Context, clientID, refreshToken, requestedResource string) (TokenResponse, error) {
	return g.token(ctx, url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {refreshToken}, "resource": {requestedResource}})
}

// Revoke discovers the official revocation endpoint and ends one renewable session.
func (g *HTTPGateway) Revoke(ctx context.Context, clientID, refreshToken string) error {
	discovery, err := g.getDiscovery(ctx)
	if err != nil {
		return err
	}
	if discovery.RevocationEndpoint == "" {
		return authError("discover revocation endpoint")
	}
	response, err := g.postForm(ctx, discovery.RevocationEndpoint, url.Values{"token": {refreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {clientID}})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return authError("revoke oauth token")
	}
	return nil
}

// token performs one bounded form exchange and validates the complete token envelope.
func (g *HTTPGateway) token(ctx context.Context, values url.Values) (TokenResponse, error) {
	response, err := g.postForm(ctx, tokenEndpoint, values)
	if err != nil {
		return TokenResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return TokenResponse{}, authError("exchange oauth token")
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		Scope        string `json:"scope"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&payload); err != nil || payload.ExpiresIn <= 0 {
		return TokenResponse{}, authError("decode oauth token")
	}
	return TokenResponse{AccessToken: payload.AccessToken, RefreshToken: payload.RefreshToken, IDToken: payload.IDToken, Scope: payload.Scope, ExpiresIn: time.Duration(payload.ExpiresIn) * time.Second}, nil
}

// postForm sends one OAuth request with no retry and a context-owned lifetime.
func (g *HTTPGateway) postForm(ctx context.Context, endpoint string, values url.Values) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, ctxModelError("send oauth request", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, authError("build oauth request")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := g.client.Do(request)
	if err != nil {
		return nil, oauthBoundaryError(ctx, "send oauth request", err, model.CategoryProviderTransient)
	}
	return response, nil
}

// discoveryDocument contains only the allowlisted endpoints needed by OAuth composition.
type discoveryDocument struct {
	Issuer             string `json:"issuer"`
	JWKSURI            string `json:"jwks_uri"`
	RevocationEndpoint string `json:"revocation_endpoint"`
}

// getDiscovery loads bounded official OpenID metadata and rejects endpoint substitution.
func (g *HTTPGateway) getDiscovery(ctx context.Context) (discoveryDocument, error) {
	if err := ctx.Err(); err != nil {
		return discoveryDocument{}, ctxModelError("load oauth discovery", err)
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, discoveryEndpoint, nil)
	response, err := g.client.Do(request)
	if err != nil {
		return discoveryDocument{}, oauthBoundaryError(ctx, "load oauth discovery", err, model.CategoryProviderTransient)
	}
	defer response.Body.Close()
	var document discoveryDocument
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&document) != nil || document.Issuer != "https://auth.openai.com" {
		return discoveryDocument{}, authError("validate oauth discovery")
	}
	parsed, err := url.Parse(document.RevocationEndpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "auth.openai.com" {
		return discoveryDocument{}, authError("validate revocation endpoint")
	}
	return document, nil
}

// PublicEndpoints returns immutable official endpoints for contract tests.
func PublicEndpoints() (string, string, string) {
	return authorizationEndpoint, tokenEndpoint, discoveryEndpoint
}
