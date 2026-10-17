package githubpr

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestGitHubAppTokenSourceUsesExactInstallationRepositoryAndPermissions verifies the production credential exchange boundary.
func TestGitHubAppTokenSourceUsesExactInstallationRepositoryAndPermissions(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	pkcs8, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Unix(100, 0)}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/app/installations/77/access_tokens" {
			t.Fatalf("request=%s %s", request.Method, request.URL.Path)
		}
		if !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer eyJ") || strings.Contains(request.Header.Get("Authorization"), string(privatePEM)) {
			t.Fatal("invalid or exposed app assertion")
		}
		var payload struct {
			RepositoryIDs []int64       `json:"repository_ids"`
			Permissions   PermissionSet `json:"permissions"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || len(payload.RepositoryIDs) != 1 || payload.RepositoryIDs[0] != 2 || ValidatePermissions(payload.Permissions) != nil {
			t.Fatalf("payload=%#v err=%v", payload, err)
		}
		body := `{"token":"short-lived","expires_at":"1970-01-01T00:11:40Z","permissions":{"checks":"write","issues":"write","metadata":"read","pull_requests":"read"},"repositories":[{"id":2}]}`
		return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	source, err := NewGitHubAppTokenSource(GitHubAppTokenConfig{AppID: 10, BaseURL: "https://api.github.test", APIVersion: "2022-11-28", PrivateKeyPEM: privatePEM, HTTP: transport, Clock: clock, MaxResponse: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewGitHubAppTokenSource(GitHubAppTokenConfig{AppID: 10, BaseURL: "https://api.github.test", APIVersion: "2022-11-28", PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), HTTP: transport, Clock: clock, MaxResponse: 4096}); err != nil {
		t.Fatal(err)
	}
	token, err := source.Token(context.Background(), 77, 2, RequiredPermissions())
	if err != nil || token.Value != "short-lived" || token.RepositoryID != 2 {
		t.Fatalf("token=%#v err=%v", token, err)
	}
}

// TestGitHubAppTokenSourceFailsClosed covers malformed composition, transport, scope, and response boundaries.
func TestGitHubAppTokenSourceFailsClosed(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	clock := &fakeClock{now: time.Unix(100, 0)}
	valid := GitHubAppTokenConfig{AppID: 10, BaseURL: "https://api.github.test", APIVersion: "2022-11-28", PrivateKeyPEM: privatePEM, HTTP: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"token":"value","expires_at":"1970-01-01T00:11:40Z","permissions":{"checks":"write","issues":"write","metadata":"read","pull_requests":"read"},"repositories":[{"id":2}]}`))}, nil
	}), Clock: clock, MaxResponse: 4096}
	invalidConfigs := []GitHubAppTokenConfig{{}, valid, valid}
	invalidConfigs[1].BaseURL = "http://api.github.test"
	invalidConfigs[2].PrivateKeyPEM = []byte("not a key")
	for _, config := range invalidConfigs {
		if _, err := NewGitHubAppTokenSource(config); err == nil {
			t.Fatal("invalid token composition accepted")
		}
	}
	source, err := NewGitHubAppTokenSource(valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.Token(context.Background(), 0, 2, RequiredPermissions()); !IsCode(err, CodeUnauthorized) {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		do   roundTripFunc
		code ErrorCode
	}{
		{"transport", func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") }, CodeTransient},
		{"status", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusForbidden, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("denied"))}, nil
		}, CodeUnauthorized},
		{"oversize", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 5000)))}, nil
		}, CodeIntegrity},
		{"wrong-repository", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"token":"value","expires_at":"1970-01-01T00:11:40Z","permissions":{"checks":"write","issues":"write","metadata":"read","pull_requests":"read"},"repositories":[{"id":9}]}`))}, nil
		}, CodeIntegrity},
		{"expired", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"token":"value","expires_at":"1970-01-01T00:01:40Z","permissions":{"checks":"write","issues":"write","metadata":"read","pull_requests":"read"},"repositories":[{"id":2}]}`))}, nil
		}, CodeUnauthorized},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			config.HTTP = test.do
			candidate, err := NewGitHubAppTokenSource(config)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = candidate.Token(context.Background(), 77, 2, RequiredPermissions()); !IsCode(err, test.code) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
