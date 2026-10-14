package openai

import (
	"context"
	"net/http"
)

// CredentialSource resolves a bearer only at the authenticated transport boundary.
type CredentialSource interface {
	AccessToken(context.Context) (string, error)
}

// HTTPClient is the minimal non-retrying transport used for one request.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// AuthenticatedTransport adds the current bearer after the adapter builds its request.
type AuthenticatedTransport struct {
	credentials CredentialSource
	client      HTTPClient
}

// NewAuthenticatedTransport composes private credentials with a redirect-safe HTTP client.
func NewAuthenticatedTransport(credentials CredentialSource, client HTTPClient) (*AuthenticatedTransport, error) {
	if credentials == nil || client == nil {
		return nil, &transportConfigError{}
	}
	return &AuthenticatedTransport{credentials: credentials, client: client}, nil
}

// Do resolves credentials just in time and sends exactly one endpoint-bound request.
func (t *AuthenticatedTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || request.Method != http.MethodPost || request.URL.String() != responsesEndpoint {
		return nil, &transportConfigError{}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	token, err := t.credentials.AccessToken(ctx)
	if err != nil {
		return nil, err
	}
	clone := request.Clone(ctx)
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+token)
	return t.client.Do(clone)
}

// transportConfigError is a constant-text error that cannot contain request secrets.
type transportConfigError struct{}

// Error reports invalid authenticated transport composition safely.
func (*transportConfigError) Error() string { return "invalid authenticated transport configuration" }

// NewHTTPClient returns a client that refuses all redirects and has no retry layer.
func NewHTTPClient(roundTripper http.RoundTripper) *http.Client {
	if transport, ok := roundTripper.(*http.Transport); ok {
		clone := transport.Clone()
		clone.MaxResponseHeaderBytes = 32 * 1024
		roundTripper = clone
	}
	client := &http.Client{Transport: roundTripper}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return client
}
