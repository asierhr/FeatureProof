package githubpr

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitHubAppTokenConfig contains the trusted endpoint and private key used only by credential composition.
type GitHubAppTokenConfig struct {
	AppID               int64
	BaseURL, APIVersion string
	PrivateKeyPEM       []byte
	HTTP                HTTPDoer
	Clock               Clock
	MaxResponse         int64
}

// GitHubAppTokenSource exchanges short-lived signed app JWTs for exact-repository installation tokens.
type GitHubAppTokenSource struct {
	config GitHubAppTokenConfig
	base   *url.URL
	key    *rsa.PrivateKey
}

// NewGitHubAppTokenSource parses secret key material without retaining its PEM representation.
func NewGitHubAppTokenSource(config GitHubAppTokenConfig) (*GitHubAppTokenSource, error) {
	base, err := url.Parse(config.BaseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" || config.AppID <= 0 || config.HTTP == nil || config.Clock == nil || config.MaxResponse <= 0 {
		return nil, &Error{Code: CodeInvalid, Operation: "app_token_config"}
	}
	block, _ := pem.Decode(config.PrivateKeyPEM)
	if block == nil {
		return nil, &Error{Code: CodeUnauthorized, Operation: "app_private_key"}
	}
	var key *rsa.PrivateKey
	if parsed, parseErr := x509.ParsePKCS1PrivateKey(block.Bytes); parseErr == nil {
		key = parsed
	} else if generic, pkcs8Err := x509.ParsePKCS8PrivateKey(block.Bytes); pkcs8Err == nil {
		key, _ = generic.(*rsa.PrivateKey)
	}
	if key == nil {
		return nil, &Error{Code: CodeUnauthorized, Operation: "app_private_key"}
	}
	return &GitHubAppTokenSource{config: config, base: base, key: key}, nil
}

// Token requests a new token restricted to one repository and the exact minimum permission set.
func (s *GitHubAppTokenSource) Token(ctx context.Context, installationID, repositoryID int64, permissions PermissionSet) (InstallationToken, error) {
	if installationID <= 0 || repositoryID <= 0 || !equalPermissions(permissions, RequiredPermissions()) && !equalPermissions(permissions, ContextPermissions()) {
		return InstallationToken{}, &Error{Code: CodeUnauthorized, Operation: "token_scope"}
	}
	jwt, err := s.signJWT()
	if err != nil {
		return InstallationToken{}, err
	}
	body := map[string]any{"repository_ids": []int64{repositoryID}, "permissions": permissions}
	raw, _ := json.Marshal(body)
	target := *s.base
	target.Path = strings.TrimSuffix(s.base.Path, "/") + "/app/installations/" + strconv.FormatInt(installationID, 10) + "/access_tokens"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(raw))
	if err != nil {
		return InstallationToken{}, &Error{Code: CodeInternal, Operation: "token_request"}
	}
	request.Header.Set("Authorization", "Bearer "+jwt)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", s.config.APIVersion)
	response, err := s.config.HTTP.Do(request)
	if err != nil {
		return InstallationToken{}, &Error{Code: CodeTransient, Operation: "token_http"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return InstallationToken{}, classifyHTTP(response, s.config.Clock.Now())
	}
	responseRaw, err := io.ReadAll(io.LimitReader(response.Body, s.config.MaxResponse+1))
	if err != nil || int64(len(responseRaw)) > s.config.MaxResponse {
		return InstallationToken{}, &Error{Code: CodeIntegrity, Operation: "token_response"}
	}
	var dto struct {
		Token        string        `json:"token"`
		ExpiresAt    time.Time     `json:"expires_at"`
		Permissions  PermissionSet `json:"permissions"`
		Repositories []struct {
			ID int64 `json:"id"`
		} `json:"repositories"`
	}
	if json.Unmarshal(responseRaw, &dto) != nil || len(dto.Repositories) != 1 || dto.Repositories[0].ID != repositoryID || !equalPermissions(dto.Permissions, permissions) {
		return InstallationToken{}, &Error{Code: CodeIntegrity, Operation: "token_response"}
	}
	token := InstallationToken{Value: dto.Token, RepositoryID: repositoryID, Permissions: dto.Permissions, ExpiresAt: dto.ExpiresAt}
	if ValidateTokenFor(token, repositoryID, permissions, s.config.Clock.Now()) != nil {
		return InstallationToken{}, &Error{Code: CodeUnauthorized, Operation: "token_response"}
	}
	return token, nil
}

// signJWT creates the ten-minute GitHub App assertion without exposing its signing key.
func (s *GitHubAppTokenSource) signJWT() (string, error) {
	now := s.config.Clock.Now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claimsRaw, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": strconv.FormatInt(s.config.AppID, 10)})
	claims := base64.RawURLEncoding.EncodeToString(claimsRaw)
	unsigned := header + "." + claims
	sum := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", &Error{Code: CodeInternal, Operation: "app_jwt"}
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// GitHubAppTokenSource must remain the credential boundary consumed by REST composition.
var _ CredentialSource = (*GitHubAppTokenSource)(nil)
