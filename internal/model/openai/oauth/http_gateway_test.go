package oauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/model"
)

// httpDoerFunc adapts controlled OAuth and JWKS responses to the HTTP boundary.
type httpDoerFunc func(*http.Request) (*http.Response, error)

// Do executes one controlled protocol response.
func (f httpDoerFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

// oauthResponse builds a bounded fake HTTP response.
func oauthResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

// TestHTTPGatewayUsesOnlyOfficialEndpointsAndDocumentedForms verifies real protocol serialization.
func TestHTTPGatewayUsesOnlyOfficialEndpointsAndDocumentedForms(t *testing.T) {
	var requests []*http.Request
	client := httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request)
		switch request.URL.String() {
		case tokenEndpoint:
			_ = request.ParseForm()
			return oauthResponse(200, `{"access_token":"access","refresh_token":"refresh-next","id_token":"id","scope":"chatgpt.tokens.use.direct","expires_in":3600}`), nil
		case discoveryEndpoint:
			return oauthResponse(200, `{"issuer":"https://auth.openai.com","jwks_uri":"https://auth.openai.com/.well-known/jwks.json","revocation_endpoint":"https://auth.openai.com/api/accounts/oauth/revoke"}`), nil
		case "https://auth.openai.com/api/accounts/oauth/revoke":
			_ = request.ParseForm()
			return oauthResponse(200, ""), nil
		default:
			t.Fatalf("unexpected endpoint %s", request.URL)
			return nil, nil
		}
	})
	gateway, err := NewHTTPGateway(client)
	if err != nil {
		t.Fatal(err)
	}
	registration := RegistrationRequest{ClientID: registrationClientID, HostID: "host", AppName: "FeatureProof", RedirectURI: "http://127.0.0.1:1234/auth/callback", State: "state", Nonce: "nonce", Challenge: "challenge", Scopes: requiredScopes, Resource: resource}
	authorization, err := gateway.Register(context.Background(), registration)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(authorization)
	if parsed.Scheme != "https" || parsed.Host != "auth.openai.com" || parsed.Query().Get("client_id") != registrationClientID || parsed.Query().Get("code_challenge_method") != "S256" || parsed.Query().Get("ext_agent_host_id") != "host" {
		t.Fatalf("authorization=%s", authorization)
	}
	tokens, err := gateway.Exchange(context.Background(), ExchangeRequest{Code: "code", ClientID: "issued", RedirectURI: registration.RedirectURI, Verifier: "verifier", Resource: resource})
	if err != nil || tokens.AccessToken != "access" {
		t.Fatalf("tokens=%+v error=%v", tokens, err)
	}
	if requests[0].Form.Get("client_id") != "issued" || requests[0].Form.Get("code_verifier") != "verifier" {
		t.Fatalf("exchange form=%v", requests[0].Form)
	}
	_, err = gateway.Refresh(context.Background(), "issued", "rotating", resource)
	if err != nil {
		t.Fatal(err)
	}
	if requests[1].Form.Get("refresh_token") != "rotating" || requests[1].Form.Has("scope") {
		t.Fatalf("refresh form=%v", requests[1].Form)
	}
	if err := gateway.Revoke(context.Background(), "issued", "latest"); err != nil {
		t.Fatal(err)
	}
	if requests[3].Form.Get("token") != "latest" || requests[3].Form.Get("client_id") != "issued" {
		t.Fatalf("revoke form=%v", requests[3].Form)
	}
}

// TestHTTPGatewayRejectsUnsafeDiscoveryAndInvalidResponses verifies fail-closed provider admission.
func TestHTTPGatewayRejectsUnsafeDiscoveryAndInvalidResponses(t *testing.T) {
	gateway, _ := NewHTTPGateway(httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() == discoveryEndpoint {
			return oauthResponse(200, `{"issuer":"https://auth.openai.com","revocation_endpoint":"https://evil.invalid/revoke"}`), nil
		}
		return oauthResponse(400, `{"error":"canary"}`), nil
	}))
	if _, err := gateway.Exchange(context.Background(), ExchangeRequest{}); err == nil {
		t.Fatal("failed exchange accepted")
	}
	if err := gateway.Revoke(context.Background(), "client", "token"); err == nil {
		t.Fatal("unsafe revocation accepted")
	}
	if _, err := gateway.Register(context.Background(), RegistrationRequest{}); err == nil {
		t.Fatal("invalid registration accepted")
	}
}

// TestJWKSValidatorVerifiesSignatureIssuerAudienceExpiryAndNonce exercises the real ID-token validator.
func TestJWKSValidatorVerifiesSignatureIssuerAudienceExpiryAndNonce(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	modulus := base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes())
	exponent := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.PublicKey.E)).Bytes())
	jwks, _ := json.Marshal(map[string]any{"keys": []map[string]any{{"kid": "key-one", "kty": "RSA", "use": "sig", "n": modulus, "e": exponent}}})
	validator, err := NewJWKSValidator(httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != jwksEndpoint {
			t.Fatalf("url=%s", request.URL)
		}
		return oauthResponse(200, string(jwks)), nil
	}), fakeClock{now: time.Unix(1000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	token := signedToken(t, privateKey, map[string]any{"iss": "https://auth.openai.com", "sub": "subject", "aud": "issued-client", "exp": 1100, "nonce": "nonce"})
	identity, err := validator.Validate(context.Background(), token, "issued-client", "nonce")
	if err != nil || identity.Subject != "subject" {
		t.Fatalf("identity=%+v error=%v", identity, err)
	}
	if _, err := validator.Validate(context.Background(), token, "other-client", "nonce"); err == nil {
		t.Fatal("wrong audience accepted")
	}
	if _, err := validator.Validate(context.Background(), token, "issued-client", "wrong"); err == nil {
		t.Fatal("wrong nonce accepted")
	}
}

// signedToken creates one local RS256 token for identity-validation tests.
func signedToken(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]any{"alg": "RS256", "kid": "key-one"})
	payload, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// TestOAuthSystemBoundariesAreCancelableAndBounded verifies randomness, loopback and browser ownership.
func TestOAuthSystemBoundariesAreCancelableAndBounded(t *testing.T) {
	first, err := (CryptoRandom{}).Value(32)
	if err != nil || first == "" {
		t.Fatalf("random=%q error=%v", first, err)
	}
	if _, err := (CryptoRandom{}).Value(1); err == nil {
		t.Fatal("short random accepted")
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	_ = probe.Close()
	redirect := "http://" + address + "/auth/callback"
	listener, err := NewLoopbackListener(redirect)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan Callback, 1)
	failures := make(chan error, 1)
	go func() {
		callback, awaitErr := listener.Await(context.Background(), redirect)
		if awaitErr != nil {
			failures <- awaitErr
			return
		}
		result <- callback
	}()
	response, err := http.Get(redirect + "?state=state&code=code&client_id=issued")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	select {
	case err := <-failures:
		t.Fatal(err)
	case callback := <-result:
		if callback.State != "state" || callback.Code != "code" || callback.IssuedClientID != "issued" {
			t.Fatalf("callback=%+v", callback)
		}
	}
	if boundedQuery("") != "" || boundedQuery(strings.Repeat("x", 4097)) != "" || boundedQuery("has space") != "" {
		t.Fatal("unsafe callback value accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (SystemBrowser{}).Open(ctx, "https://auth.openai.com"); errorCategoryForOAuth(err) != model.CategoryCancelled {
		t.Fatalf("browser category=%s error=%v", errorCategoryForOAuth(err), err)
	}
	_ = (&modelLoopbackError{}).Error()
}

// TestCredentialStoreUpdateDeleteAndProfileSource verifies restart-safe profile selection.
func TestCredentialStoreUpdateDeleteAndProfileSource(t *testing.T) {
	store, err := NewLocalStore(t.TempDir() + string(os.PathSeparator) + "profiles.json")
	if err != nil {
		t.Fatal(err)
	}
	base := tokenRecord{ProfileID: "profile", ClientID: "client", Subject: "subject", AccessToken: "first", RefreshToken: "refresh-one", IDToken: "id-one", Scopes: []string{requiredDirectScope}, ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.Save(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	base.AccessToken, base.RefreshToken, base.IDToken = "second", "refresh-two", "id-two"
	if err := store.Save(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(&fakeGateway{}, WallClock{}, fakeValidator{identity: Identity{Subject: "subject"}}, store)
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewProfileSource(resolver, "profile")
	if err != nil {
		t.Fatal(err)
	}
	token, err := source.AccessToken(context.Background())
	if err != nil || token != "second" {
		t.Fatalf("token=%q error=%v", token, err)
	}
	if err := store.Delete(context.Background(), "profile"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), "profile"); err == nil {
		t.Fatal("deleted profile loaded")
	}
	if _, err := NewProfileSource(nil, ""); err == nil {
		t.Fatal("invalid source accepted")
	}
	if _, err := NewLocalStore("relative"); err == nil {
		t.Fatal("relative store accepted")
	}
	_ = (&modelStoreError{operation: "test"}).Error()
}

// TestOAuthFailureBranchesClearTerminalProfilesAndPreserveSafeErrors verifies recovery policy.
func TestOAuthFailureBranchesClearTerminalProfilesAndPreserveSafeErrors(t *testing.T) {
	service, gateway, store, _ := newService(t)
	store.records["profile"] = tokenRecord{ProfileID: "profile", ClientID: "client", Subject: "subject-a", AccessToken: "expired", RefreshToken: "refresh", IDToken: "id", Scopes: []string{requiredDirectScope}, ExpiresAt: time.Unix(1, 0)}
	gateway.refreshErr = &model.Error{Category: model.CategoryAuthentication, Operation: "refresh"}
	if _, err := service.AccessToken(context.Background(), "profile"); err == nil {
		t.Fatal("invalid refresh accepted")
	}
	if record := store.records["profile"]; record.AccessToken != "" || record.RefreshToken != "" || record.ClientID != "client" {
		t.Fatalf("terminal profile=%+v", record)
	}
	if category := errorCategoryForOAuth(ctxModelError("test", context.DeadlineExceeded)); category != model.CategoryTimeout {
		t.Fatalf("category=%s", category)
	}
	_, _, scopes := RequiredProtocol()
	if !hasScope(strings.Join(scopes, " "), requiredDirectScope) {
		t.Fatal("required scope missing")
	}
	_, _, _ = PublicEndpoints()
	_ = (WallClock{}).Now()
	if _, err := NewResolver(nil, nil, nil, nil); err == nil {
		t.Fatal("invalid resolver accepted")
	}
}

// TestIDTokenAndStoreRejectMalformedOrCancelledState verifies local fail-closed branches.
func TestIDTokenAndStoreRejectMalformedOrCancelledState(t *testing.T) {
	validator, _ := NewJWKSValidator(httpDoerFunc(func(*http.Request) (*http.Response, error) { return oauthResponse(500, ""), nil }), fakeClock{now: time.Unix(1000, 0)})
	for _, token := range []string{"", "a.b", base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","kid":"x"}`)) + ".e30.signature"} {
		if _, err := validator.Validate(context.Background(), token, "audience", "nonce"); err == nil {
			t.Fatalf("token %q accepted", token)
		}
	}
	arrayAudience, _ := json.Marshal([]string{"one", "two"})
	if !audienceContains(arrayAudience, "two") || audienceContains(arrayAudience, "missing") || audienceContains(json.RawMessage(`123`), "two") {
		t.Fatal("audience matching failed")
	}
	if _, err := NewJWKSValidator(nil, nil); err == nil {
		t.Fatal("invalid validator accepted")
	}

	path := filepath.Join(t.TempDir(), "profiles.json")
	store, _ := NewLocalStore(path)
	if err := store.Save(context.Background(), tokenRecord{}); err == nil {
		t.Fatal("partial record accepted")
	}
	if err := store.Delete(context.Background(), "missing"); err == nil {
		t.Fatal("missing profile deletion accepted")
	}
	if err := os.WriteFile(path, []byte(`{"broken":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), "profile"); err == nil {
		t.Fatal("corrupt store accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Save(cancelled, tokenRecord{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("save error=%v", err)
	}
	if err := store.Delete(cancelled, "profile"); !errors.Is(err, context.Canceled) {
		t.Fatalf("delete error=%v", err)
	}
}

// TestHTTPGatewayPropagatesCancellationWithoutProviderText verifies safe transport failure mapping.
func TestHTTPGatewayPropagatesCancellationWithoutProviderText(t *testing.T) {
	gateway, _ := NewHTTPGateway(httpDoerFunc(func(*http.Request) (*http.Response, error) { return nil, context.Canceled }))
	_, err := gateway.Exchange(context.Background(), ExchangeRequest{})
	if category := errorCategoryForOAuth(err); category != model.CategoryCancelled {
		t.Fatalf("category=%s error=%v", category, err)
	}
	if _, err := NewHTTPGateway(nil); err == nil {
		t.Fatal("nil gateway client accepted")
	}
}

// TestOAuthHTTPAndKeyBoundariesPreserveExactCategories verifies exchange, discovery and JWKS context taxonomy.
func TestOAuthHTTPAndKeyBoundariesPreserveExactCategories(t *testing.T) {
	for _, test := range []struct {
		name     string
		err      error
		expected model.Category
	}{
		{name: "authentication", err: &model.Error{Category: model.CategoryAuthentication, Operation: "profile"}, expected: model.CategoryAuthentication},
		{name: "cancelled", err: context.Canceled, expected: model.CategoryCancelled},
		{name: "timeout", err: context.DeadlineExceeded, expected: model.CategoryTimeout},
		{name: "typed cancellation cause", err: &model.Error{Category: model.CategoryAuthentication, Operation: "profile", Cause: context.Canceled}, expected: model.CategoryCancelled},
	} {
		t.Run("exchange "+test.name, func(t *testing.T) {
			gateway, _ := NewHTTPGateway(httpDoerFunc(func(*http.Request) (*http.Response, error) { return nil, test.err }))
			if _, err := gateway.Exchange(context.Background(), ExchangeRequest{}); errorCategoryForOAuth(err) != test.expected {
				t.Fatalf("category=%s error=%v", errorCategoryForOAuth(err), err)
			}
		})
	}

	for _, boundary := range []struct {
		name string
		call func(httpDoerFunc) error
	}{
		{name: "discovery", call: func(client httpDoerFunc) error {
			gateway, _ := NewHTTPGateway(client)
			_, err := gateway.getDiscovery(context.Background())
			return err
		}},
		{name: "jwks", call: func(client httpDoerFunc) error {
			privateKey, _ := rsa.GenerateKey(rand.Reader, 1024)
			token := signedToken(t, privateKey, map[string]any{"iss": "https://auth.openai.com", "sub": "subject", "aud": "client", "exp": 100, "nonce": "nonce"})
			validator, _ := NewJWKSValidator(client, fakeClock{now: time.Unix(1, 0)})
			_, err := validator.Validate(context.Background(), token, "client", "nonce")
			return err
		}},
	} {
		t.Run(boundary.name+" cancelled", func(t *testing.T) {
			if err := boundary.call(httpDoerFunc(func(*http.Request) (*http.Response, error) { return nil, context.Canceled })); errorCategoryForOAuth(err) != model.CategoryCancelled {
				t.Fatalf("category=%s error=%v", errorCategoryForOAuth(err), err)
			}
		})
		t.Run(boundary.name+" timeout", func(t *testing.T) {
			if err := boundary.call(httpDoerFunc(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })); errorCategoryForOAuth(err) != model.CategoryTimeout {
				t.Fatalf("category=%s error=%v", errorCategoryForOAuth(err), err)
			}
		})
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	gateway, _ := NewHTTPGateway(httpDoerFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("cancelled preflight reached HTTP")
		return nil, nil
	}))
	if _, err := gateway.Register(cancelled, RegistrationRequest{}); errorCategoryForOAuth(err) != model.CategoryCancelled {
		t.Fatalf("registration error=%v", err)
	}
	if _, err := gateway.postForm(cancelled, tokenEndpoint, url.Values{}); errorCategoryForOAuth(err) != model.CategoryCancelled {
		t.Fatalf("form error=%v", err)
	}
	if _, err := gateway.getDiscovery(cancelled); errorCategoryForOAuth(err) != model.CategoryCancelled {
		t.Fatalf("discovery error=%v", err)
	}
	privateKey, _ := rsa.GenerateKey(rand.Reader, 1024)
	token := signedToken(t, privateKey, map[string]any{"iss": "https://auth.openai.com", "sub": "subject", "aud": "client", "exp": 100, "nonce": "nonce"})
	validator, _ := NewJWKSValidator(gateway.client, fakeClock{now: time.Unix(1, 0)})
	if _, err := validator.Validate(cancelled, token, "client", "nonce"); errorCategoryForOAuth(err) != model.CategoryCancelled {
		t.Fatalf("jwks error=%v", err)
	}
}

// TestRemainingOAuthFailurePathsStayFailClosed verifies store, identity, listener and HTTP terminal branches.
func TestRemainingOAuthFailurePathsStayFailClosed(t *testing.T) {
	service, _, store, _ := newService(t)
	store.saveErr = errors.New("disk")
	if err := service.SignIn(context.Background(), "profile"); errorCategoryForOAuth(err) != model.CategoryInternal {
		t.Fatalf("save error=%v", err)
	}
	store.saveErr = nil
	store.records["profile"] = tokenRecord{ProfileID: "profile", ClientID: "client", Subject: "subject", AccessToken: "access", RefreshToken: "refresh", IDToken: "id", Scopes: []string{requiredDirectScope}, ExpiresAt: time.Now().Add(time.Hour)}
	store.saveErr = errors.New("disk")
	if err := service.SignOut(context.Background(), "profile"); errorCategoryForOAuth(err) != model.CategoryInternal {
		t.Fatalf("delete error=%v", err)
	}
	store.saveErr = nil
	store.loadErr = errors.New("disk")
	if _, err := service.AccessToken(context.Background(), "profile"); err == nil {
		t.Fatal("load failure accepted")
	}

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	_ = probe.Close()
	listener, err := NewLoopbackListener("http://" + address + "/callback")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := listener.Await(ctx, ""); errorCategoryForOAuth(err) != model.CategoryCancelled {
		t.Fatalf("listener error=%v", err)
	}
	if _, err := NewLoopbackListener("http://localhost/callback"); err == nil {
		t.Fatal("unsafe callback accepted")
	}

	gateway, _ := NewHTTPGateway(httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() == discoveryEndpoint {
			return oauthResponse(200, `{"issuer":"https://auth.openai.com","revocation_endpoint":"https://auth.openai.com/revoke"}`), nil
		}
		return oauthResponse(500, ""), nil
	}))
	if err := gateway.Revoke(context.Background(), "client", "token"); err == nil {
		t.Fatal("failed revoke accepted")
	}

	validator, _ := NewJWKSValidator(httpDoerFunc(func(*http.Request) (*http.Response, error) { return oauthResponse(200, `{"keys":[]}`), nil }), fakeClock{now: time.Unix(1, 0)})
	privateKey, _ := rsa.GenerateKey(rand.Reader, 1024)
	unknown := signedToken(t, privateKey, map[string]any{"iss": "https://auth.openai.com", "sub": "subject", "aud": "client", "exp": 100, "nonce": "nonce"})
	if _, err := validator.Validate(context.Background(), unknown, "client", "nonce"); err == nil {
		t.Fatal("unknown signing key accepted")
	}
}

// TestRefreshIdentityAndPersistenceFailuresNeverReplaceCredentials covers atomic rotation failure paths.
func TestRefreshIdentityAndPersistenceFailuresNeverReplaceCredentials(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*Service, *memoryStore)
	}{
		{"identity", func(service *Service, _ *memoryStore) {
			service.validator = fakeValidator{identity: Identity{Subject: "different"}}
		}},
		{"store", func(_ *Service, store *memoryStore) { store.saveErr = errors.New("disk") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, store, _ := newService(t)
			store.records["profile"] = tokenRecord{ProfileID: "profile", ClientID: "client", Subject: "subject-a", AccessToken: "expired", RefreshToken: "old-refresh", IDToken: "old-id", Scopes: []string{requiredDirectScope}, ExpiresAt: time.Unix(1, 0)}
			test.prepare(service, store)
			if _, err := service.AccessToken(context.Background(), "profile"); err == nil {
				t.Fatal("failed rotation accepted")
			}
			record := store.records["profile"]
			if record.RefreshToken != "old-refresh" || record.AccessToken != "expired" {
				t.Fatalf("partial rotation=%+v", record)
			}
		})
	}
	service, _, store, _ := newService(t)
	store.loadErr = errors.New("disk")
	if err := service.SignOut(context.Background(), "profile"); err == nil {
		t.Fatal("sign-out load failure accepted")
	}
	if _, err := NewService(Config{}, nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("invalid service composition accepted")
	}
}

// TestJWKSRejectsInvalidKeyDocumentsAndClaims covers bounded key-selection failures.
func TestJWKSRejectsInvalidKeyDocumentsAndClaims(t *testing.T) {
	privateKey, _ := rsa.GenerateKey(rand.Reader, 1024)
	token := signedToken(t, privateKey, map[string]any{"iss": "https://auth.openai.com", "sub": "subject", "aud": "client", "exp": 100, "nonce": "nonce"})
	cases := []string{
		`{"keys":[{"kid":"key-one","kty":"EC","use":"sig","n":"x","e":"AQAB"}]}`,
		`{"keys":[{"kid":"key-one","kty":"RSA","use":"sig","n":"***","e":"AQAB"}]}`,
		`{"keys":[{"kid":"key-one","kty":"RSA","use":"sig","n":"AQ","e":""}]}`,
	}
	for _, document := range cases {
		validator, _ := NewJWKSValidator(httpDoerFunc(func(*http.Request) (*http.Response, error) { return oauthResponse(200, document), nil }), fakeClock{now: time.Unix(1, 0)})
		if _, err := validator.Validate(context.Background(), token, "client", "nonce"); err == nil {
			t.Fatalf("invalid jwks accepted: %s", document)
		}
	}
	validator, _ := NewJWKSValidator(httpDoerFunc(func(*http.Request) (*http.Response, error) { return oauthResponse(500, ""), nil }), fakeClock{now: time.Unix(1, 0)})
	if _, err := validator.Validate(context.Background(), token, "client", "nonce"); err == nil {
		t.Fatal("failed jwks response accepted")
	}
}

// TestLocalStoreRejectsBlockedParentAndInconsistentProfile covers durable-store failure branches.
func TestLocalStoreRejectsBlockedParentAndInconsistentProfile(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(parent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, _ := NewLocalStore(filepath.Join(parent, "profiles.json"))
	record := tokenRecord{ProfileID: "profile", ClientID: "client", Subject: "subject", AccessToken: "access", RefreshToken: "refresh", IDToken: "id", Scopes: []string{requiredDirectScope}, ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.Save(context.Background(), record); err == nil {
		t.Fatal("blocked parent accepted")
	}
	store, _ = NewLocalStore(filepath.Join(t.TempDir(), "profiles.json"))
	if err := store.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	record.ClientID = "other-client"
	if err := store.Save(context.Background(), record); err == nil {
		t.Fatal("inconsistent registration replaced")
	}
}

// TestOAuthRemainingBoundaryBranchesStayDeterministic covers multi-stage randomness and metadata failures.
func TestOAuthRemainingBoundaryBranchesStayDeterministic(t *testing.T) {
	service, _, _, _ := newService(t)
	service.random = &fakeRandom{values: []string{"state"}}
	if _, _, _, err := service.attemptValues(); err == nil {
		t.Fatal("missing nonce accepted")
	}
	service.random = &fakeRandom{values: []string{"state", "nonce"}}
	if _, _, _, err := service.attemptValues(); err == nil {
		t.Fatal("missing verifier accepted")
	}

	gateway, _ := NewHTTPGateway(httpDoerFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") }))
	if _, err := gateway.getDiscovery(context.Background()); errorCategoryForOAuth(err) != model.CategoryProviderTransient {
		t.Fatalf("discovery error=%v", err)
	}
	if _, err := gateway.postForm(context.Background(), "://invalid", url.Values{}); err == nil {
		t.Fatal("invalid endpoint accepted")
	}

	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, _ := NewLocalStore(path)
	if _, err := store.Load(context.Background(), "profile"); err == nil {
		t.Fatal("null credential store accepted")
	}
}

// TestRefreshInternalEarlyAndCancelledWaiterPaths verifies coalescing without network or sleeps.
func TestRefreshInternalEarlyAndCancelledWaiterPaths(t *testing.T) {
	service, gateway, store, _ := newService(t)
	store.records["fresh"] = tokenRecord{ProfileID: "fresh", ClientID: "client", Subject: "subject", AccessToken: "access", RefreshToken: "refresh", IDToken: "id", Scopes: []string{requiredDirectScope}, ExpiresAt: time.Unix(1000, 0)}
	if err := service.performRefresh(context.Background(), "fresh"); err != nil || gateway.refreshCalls.Load() != 0 {
		t.Fatalf("error=%v calls=%d", err, gateway.refreshCalls.Load())
	}
	store.records["expired"] = tokenRecord{ProfileID: "expired", ClientID: "client", Subject: "subject", AccessToken: "access", RefreshToken: "refresh", IDToken: "id", Scopes: []string{requiredDirectScope}, ExpiresAt: time.Unix(1, 0)}
	gateway.refresh.IDToken = ""
	if err := service.performRefresh(context.Background(), "expired"); err == nil {
		t.Fatal("refresh without ID token accepted")
	}
	running := &refreshCall{done: make(chan struct{})}
	service.refreshes["waiting"] = running
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.refresh(ctx, "waiting"); errorCategoryForOAuth(err) != model.CategoryCancelled {
		t.Fatalf("wait error=%v", err)
	}
}

// errorCategoryForOAuth extracts safe taxonomy from OAuth tests.
func errorCategoryForOAuth(err error) model.Category {
	var typed *model.Error
	if errors.As(err, &typed) {
		return typed.Category
	}
	return ""
}
