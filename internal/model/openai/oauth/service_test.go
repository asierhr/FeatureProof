package oauth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/model"
)

// fakeClock fixes OAuth expiry decisions.
type fakeClock struct{ now time.Time }

// Now returns deterministic OAuth time.
func (c fakeClock) Now() time.Time { return c.now }

// fakeRandom returns queued fresh protocol values.
type fakeRandom struct{ values []string }

// Value consumes one deterministic state, nonce or verifier.
func (r *fakeRandom) Value(int) (string, error) {
	if len(r.values) == 0 {
		return "", errors.New("empty random")
	}
	value := r.values[0]
	r.values = r.values[1:]
	return value, nil
}

// fakeBrowser captures the public authorization URL.
type fakeBrowser struct {
	url string
	err error
}

// Open records the authorization URL without launching a real browser.
func (b *fakeBrowser) Open(_ context.Context, value string) error { b.url = value; return b.err }

// fakeListener returns one controlled loopback callback.
type fakeListener struct {
	callback Callback
	err      error
}

// Await returns a fake callback only for the exact configured loopback URI.
func (l fakeListener) Await(_ context.Context, redirect string) (Callback, error) {
	if l.err != nil {
		return Callback{}, l.err
	}
	if redirect != "http://127.0.0.1:8765/callback" {
		return Callback{}, errors.New("redirect")
	}
	return l.callback, nil
}

// fakeValidator maps a controlled ID token to one stable identity.
type fakeValidator struct {
	identity Identity
	err      error
}

// Validate supplies the verified identity without parsing a real JWT.
func (v fakeValidator) Validate(context.Context, string, string, string) (Identity, error) {
	return v.identity, v.err
}

// fakeGateway captures protocol requests and supports refresh barriers.
type fakeGateway struct {
	registration RegistrationRequest
	exchange     ExchangeRequest
	tokens       TokenResponse
	refresh      TokenResponse
	refreshErr   error
	registerErr  error
	exchangeErr  error
	revokeErr    error
	refreshCalls atomic.Int32
	refreshStart chan struct{}
	refreshGo    chan struct{}
	revoked      string
}

// Register returns a controlled public authorization URL.
func (g *fakeGateway) Register(_ context.Context, request RegistrationRequest) (string, error) {
	g.registration = request
	if g.registerErr != nil {
		return "", g.registerErr
	}
	return "https://auth.openai.example/authorize?safe=1", nil
}

// Exchange captures use of the issued client and one-time verifier.
func (g *fakeGateway) Exchange(_ context.Context, request ExchangeRequest) (TokenResponse, error) {
	g.exchange = request
	if g.exchangeErr != nil {
		return TokenResponse{}, g.exchangeErr
	}
	return g.tokens, nil
}

// Refresh coordinates concurrent callers while counting a single rotating exchange.
func (g *fakeGateway) Refresh(ctx context.Context, _, _, _ string) (TokenResponse, error) {
	if g.refreshCalls.Add(1) == 1 && g.refreshStart != nil {
		close(g.refreshStart)
	}
	if g.refreshGo != nil {
		select {
		case <-ctx.Done():
			return TokenResponse{}, ctx.Err()
		case <-g.refreshGo:
		}
	}
	if g.refreshErr != nil {
		return TokenResponse{}, g.refreshErr
	}
	return g.refresh, nil
}

// Revoke captures only the selected refresh token in the fake boundary.
func (g *fakeGateway) Revoke(_ context.Context, _ string, token string) error {
	g.revoked = token
	return g.revokeErr
}

// memoryStore keeps private token records isolated for OAuth tests.
type memoryStore struct {
	mu                          sync.Mutex
	records                     map[string]tokenRecord
	loadErr, saveErr, deleteErr error
}

// Load returns an isolated private record.
func (s *memoryStore) Load(_ context.Context, profile string) (tokenRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return tokenRecord{}, s.loadErr
	}
	record, ok := s.records[profile]
	if !ok {
		return tokenRecord{}, ErrProfileNotFound
	}
	return cloneRecord(record), nil
}

// Save atomically replaces one record in memory.
func (s *memoryStore) Save(_ context.Context, record tokenRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.records[record.ProfileID] = cloneRecord(record)
	return nil
}

// Delete removes only the selected profile.
func (s *memoryStore) Delete(_ context.Context, profile string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.records, profile)
	return nil
}

// newService builds a hermetic OAuth composition and returns observable fakes.
func newService(t *testing.T) (*Service, *fakeGateway, *memoryStore, *fakeBrowser) {
	t.Helper()
	gateway := &fakeGateway{tokens: TokenResponse{AccessToken: "access-one", RefreshToken: "refresh-one", IDToken: "id-one", Scope: requiredDirectScope, ExpiresIn: time.Hour}, refresh: TokenResponse{AccessToken: "access-two", RefreshToken: "refresh-two", IDToken: "id-two", Scope: requiredDirectScope, ExpiresIn: time.Hour}}
	store := &memoryStore{records: map[string]tokenRecord{}}
	browser := &fakeBrowser{}
	service, err := NewService(Config{HostID: "stable-host", AppName: "FeatureProof", RedirectURI: "http://127.0.0.1:8765/callback"}, browser, fakeListener{callback: Callback{State: "state", Code: "code", IssuedClientID: "issued-client"}}, gateway, &fakeRandom{values: []string{"state", "nonce", "verifier"}}, fakeClock{now: time.Unix(100, 0)}, fakeValidator{identity: Identity{Subject: "subject-a"}}, store)
	if err != nil {
		t.Fatal(err)
	}
	return service, gateway, store, browser
}

// TestOAuthRegistrationUsesHostPKCEStateNonceScopesAndIssuedClient verifies the full registration contract.
func TestOAuthRegistrationUsesHostPKCEStateNonceScopesAndIssuedClient(t *testing.T) {
	service, gateway, store, browser := newService(t)
	if err := service.SignIn(context.Background(), "profile-a"); err != nil {
		t.Fatal(err)
	}
	req := gateway.registration
	if req.ClientID != registrationClientID || req.HostID != "stable-host" || req.State != "state" || req.Nonce != "nonce" || req.Challenge != pkceChallenge("verifier") || req.Resource != resource || !hasScope(strings.Join(req.Scopes, " "), requiredDirectScope) {
		t.Fatalf("registration=%+v", req)
	}
	if gateway.exchange.ClientID != "issued-client" || gateway.exchange.Verifier != "verifier" || browser.url == "" {
		t.Fatalf("exchange=%+v browser=%q", gateway.exchange, browser.url)
	}
	record, _ := store.Load(context.Background(), "profile-a")
	if record.ClientID != "issued-client" || record.AccessToken != "access-one" {
		t.Fatalf("record=%+v", record)
	}
}

// TestOAuthRejectsInvalidCallbackIdentityTokenAndScope verifies activation stays fail-closed.
func TestOAuthRejectsInvalidCallbackIdentityTokenAndScope(t *testing.T) {
	service, gateway, store, _ := newService(t)
	service.listener = fakeListener{callback: Callback{State: "wrong", Code: "code", IssuedClientID: "issued-client"}}
	if err := service.SignIn(context.Background(), "profile-a"); err == nil {
		t.Fatal("mismatched state accepted")
	}
	if len(store.records) != 0 {
		t.Fatal("credential activated")
	}
	service, gateway, store, _ = newService(t)
	gateway.tokens.Scope = "openid"
	if err := service.SignIn(context.Background(), "profile-a"); err == nil {
		t.Fatal("missing direct scope accepted")
	}
	if len(store.records) != 0 {
		t.Fatal("credential activated")
	}
	service, _, store, _ = newService(t)
	service.validator = fakeValidator{err: errors.New("invalid token")}
	if err := service.SignIn(context.Background(), "profile-a"); err == nil {
		t.Fatal("invalid identity accepted")
	}
}

// TestOAuthRefreshIsSerializedAndRotatesAtomically verifies one complete rotating replacement.
func TestOAuthRefreshIsSerializedAndRotatesAtomically(t *testing.T) {
	service, gateway, store, _ := newService(t)
	store.records["profile-a"] = tokenRecord{ProfileID: "profile-a", ClientID: "issued-client", Subject: "subject-a", AccessToken: "expired", RefreshToken: "refresh-one", Scopes: []string{requiredDirectScope}, ExpiresAt: time.Unix(99, 0)}
	gateway.refreshStart, gateway.refreshGo = make(chan struct{}), make(chan struct{})
	results := make(chan string, 2)
	for range 2 {
		go func() { token, _ := service.AccessToken(context.Background(), "profile-a"); results <- token }()
	}
	<-gateway.refreshStart
	close(gateway.refreshGo)
	if first, second := <-results, <-results; first != "access-two" || second != "access-two" {
		t.Fatalf("tokens=%q,%q", first, second)
	}
	if gateway.refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls=%d", gateway.refreshCalls.Load())
	}
	record, _ := store.Load(context.Background(), "profile-a")
	if record.RefreshToken != "refresh-two" || record.AccessToken != "access-two" {
		t.Fatalf("record=%+v", record)
	}
}

// TestProfilesNeverMixCredentialsAndSignOutRevokes verifies profile isolation and confirmed cleanup.
func TestProfilesNeverMixCredentialsAndSignOutRevokes(t *testing.T) {
	service, gateway, store, _ := newService(t)
	expires := time.Unix(200, 0)
	store.records["a"] = tokenRecord{ProfileID: "a", ClientID: "client-a", Subject: "subject-a", AccessToken: "access-a", RefreshToken: "refresh-a", Scopes: []string{requiredDirectScope}, ExpiresAt: expires}
	store.records["b"] = tokenRecord{ProfileID: "b", ClientID: "client-b", Subject: "subject-b", AccessToken: "access-b", RefreshToken: "refresh-b", Scopes: []string{requiredDirectScope}, ExpiresAt: expires}
	token, err := service.AccessToken(context.Background(), "b")
	if err != nil || token != "access-b" {
		t.Fatalf("token=%q error=%v", token, err)
	}
	if err := service.SignOut(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if gateway.revoked != "refresh-a" {
		t.Fatalf("revoked=%q", gateway.revoked)
	}
	if record := store.records["a"]; record.AccessToken != "" || record.RefreshToken != "" || record.ClientID != "client-a" {
		t.Fatalf("signed-out registration=%+v", record)
	}
	if _, ok := store.records["b"]; !ok {
		t.Fatal("profile b deleted")
	}
}

// TestConcurrentRefreshUsesOneRotatingTokenExchange repeats the named concurrent refresh contract.
func TestConcurrentRefreshUsesOneRotatingTokenExchange(t *testing.T) {
	TestOAuthRefreshIsSerializedAndRotatesAtomically(t)
}

// TestRefreshSurvivesInitiatorCancellationForActiveWaiter verifies shared ownership and one rotating exchange.
func TestRefreshSurvivesInitiatorCancellationForActiveWaiter(t *testing.T) {
	service, gateway, store, _ := newService(t)
	store.records["profile"] = tokenRecord{ProfileID: "profile", ClientID: "client", Subject: "subject-a", AccessToken: "expired", RefreshToken: "refresh-one", IDToken: "id", Scopes: []string{requiredDirectScope}, ExpiresAt: time.Unix(1, 0)}
	gateway.refreshStart, gateway.refreshGo = make(chan struct{}), make(chan struct{})
	initiatorCtx, cancelInitiator := context.WithCancel(context.Background())
	initiator := make(chan error, 1)
	waiter := make(chan struct {
		token string
		err   error
	}, 1)
	go func() {
		_, err := service.AccessToken(initiatorCtx, "profile")
		initiator <- err
	}()
	<-gateway.refreshStart
	go func() {
		token, err := service.AccessToken(context.Background(), "profile")
		waiter <- struct {
			token string
			err   error
		}{token: token, err: err}
	}()
	joined := make(chan struct{})
	go func() {
		for {
			service.mu.Lock()
			count := 0
			if running := service.refreshes["profile"]; running != nil {
				count = running.waiters
			}
			service.mu.Unlock()
			if count == 2 {
				close(joined)
				return
			}
			runtime.Gosched()
		}
	}()
	<-joined
	cancelInitiator()
	if err := <-initiator; errorCategoryForOAuth(err) != model.CategoryCancelled {
		t.Fatalf("initiator error=%v", err)
	}
	close(gateway.refreshGo)
	result := <-waiter
	if result.err != nil || result.token != "access-two" {
		t.Fatalf("waiter token=%q error=%v", result.token, result.err)
	}
	if gateway.refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls=%d", gateway.refreshCalls.Load())
	}
}

// TestOAuthBoundariesPreserveExactSafeCategories verifies browser, listener, exchange, refresh and profile resolution.
func TestOAuthBoundariesPreserveExactSafeCategories(t *testing.T) {
	for _, test := range []struct {
		name     string
		prepare  func(*Service, *fakeGateway, *fakeBrowser, *memoryStore)
		action   func(*Service) error
		expected model.Category
	}{
		{name: "missing profile", action: func(service *Service) error {
			_, err := service.AccessToken(context.Background(), "missing")
			return err
		}, expected: model.CategoryAuthentication},
		{name: "browser cancelled", prepare: func(_ *Service, _ *fakeGateway, browser *fakeBrowser, _ *memoryStore) { browser.err = context.Canceled }, action: func(service *Service) error { return service.SignIn(context.Background(), "profile") }, expected: model.CategoryCancelled},
		{name: "listener timeout", prepare: func(service *Service, _ *fakeGateway, _ *fakeBrowser, _ *memoryStore) {
			service.listener = fakeListener{err: context.DeadlineExceeded}
		}, action: func(service *Service) error { return service.SignIn(context.Background(), "profile") }, expected: model.CategoryTimeout},
		{name: "exchange transient", prepare: func(_ *Service, gateway *fakeGateway, _ *fakeBrowser, _ *memoryStore) {
			gateway.exchangeErr = &model.Error{Category: model.CategoryProviderTransient, Operation: "exchange"}
		}, action: func(service *Service) error { return service.SignIn(context.Background(), "profile") }, expected: model.CategoryProviderTransient},
		{name: "refresh transient", prepare: func(_ *Service, gateway *fakeGateway, _ *fakeBrowser, store *memoryStore) {
			store.records["profile"] = tokenRecord{ProfileID: "profile", ClientID: "client", Subject: "subject-a", AccessToken: "expired", RefreshToken: "refresh", Scopes: []string{requiredDirectScope}, ExpiresAt: time.Unix(1, 0)}
			gateway.refreshErr = &model.Error{Category: model.CategoryProviderTransient, Operation: "refresh"}
		}, action: func(service *Service) error {
			_, err := service.AccessToken(context.Background(), "profile")
			return err
		}, expected: model.CategoryProviderTransient},
		{name: "refresh authentication", prepare: func(_ *Service, gateway *fakeGateway, _ *fakeBrowser, store *memoryStore) {
			store.records["profile"] = tokenRecord{ProfileID: "profile", ClientID: "client", Subject: "subject-a", AccessToken: "expired", RefreshToken: "refresh", Scopes: []string{requiredDirectScope}, ExpiresAt: time.Unix(1, 0)}
			gateway.refreshErr = &model.Error{Category: model.CategoryAuthentication, Operation: "refresh"}
		}, action: func(service *Service) error {
			_, err := service.AccessToken(context.Background(), "profile")
			return err
		}, expected: model.CategoryAuthentication},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, gateway, store, browser := newService(t)
			if test.prepare != nil {
				test.prepare(service, gateway, browser, store)
			}
			if err := test.action(service); errorCategoryForOAuth(err) != test.expected {
				t.Fatalf("category=%s error=%v", errorCategoryForOAuth(err), err)
			}
		})
	}
}

// TestOAuthPreflightCancellationAndSharedOwnerBounds verifies exact early exits and shared refresh ownership.
func TestOAuthPreflightCancellationAndSharedOwnerBounds(t *testing.T) {
	service, _, _, _ := newService(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name   string
		action func() error
	}{
		{name: "sign in", action: func() error { return service.SignIn(cancelled, "profile") }},
		{name: "access", action: func() error { _, err := service.AccessToken(cancelled, "profile"); return err }},
		{name: "sign out", action: func() error { return service.SignOut(cancelled, "profile") }},
		{name: "refresh", action: func() error { return service.performRefresh(cancelled, "profile") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.action(); errorCategoryForOAuth(err) != model.CategoryCancelled {
				t.Fatalf("category=%s error=%v", errorCategoryForOAuth(err), err)
			}
		})
	}

	deadline := time.Unix(4_000_000_000, 0)
	parent, cancelParent := context.WithDeadline(context.Background(), deadline)
	shared, cancelShared := sharedRefreshContext(parent)
	cancelParent()
	if sharedDeadline, ok := shared.Deadline(); !ok || !sharedDeadline.Before(deadline) || shared.Err() != nil {
		t.Fatalf("shared deadline=%v ok=%t error=%v", sharedDeadline, ok, shared.Err())
	}
	cancelShared()

	ownerCancelled := make(chan struct{})
	running := &refreshCall{done: make(chan struct{}), waiters: 1, cancel: func() { close(ownerCancelled) }}
	service.refreshes["owned"] = running
	service.releaseRefresh("owned", running)
	select {
	case <-ownerCancelled:
	default:
		t.Fatal("orphaned shared refresh was not cancelled")
	}
}

// TestLocalStoreAtomicallyPersistsCompleteOwnerOnlyRecords verifies protected credential commits.
func TestLocalStoreAtomicallyPersistsCompleteOwnerOnlyRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	store, err := NewLocalStore(path)
	if err != nil {
		t.Fatal(err)
	}
	record := tokenRecord{ProfileID: "profile", ClientID: "client", Subject: "subject", AccessToken: "access", RefreshToken: "refresh", Scopes: []string{requiredDirectScope}, ExpiresAt: time.Unix(500, 0)}
	if err := store.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), "profile")
	if err != nil || loaded.AccessToken != "access" {
		t.Fatalf("loaded=%+v error=%v", loaded, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("permissions=%o", info.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "ProfileID\":\"other") {
		t.Fatal("unexpected profile")
	}
}

// TestLocalStoreCoordinationWaitIsCancelableWithoutHoldingIOLock verifies queue cancellation and turn handoff.
func TestLocalStoreCoordinationWaitIsCancelableWithoutHoldingIOLock(t *testing.T) {
	store, err := NewLocalStore(filepath.Join(t.TempDir(), "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	finishFirst, err := store.begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancelWait := context.WithCancel(context.Background())
	waitResult := make(chan error, 1)
	go func() {
		_, err := store.begin(waitCtx)
		waitResult <- err
	}()
	for {
		store.mu.Lock()
		queued := store.next == 2
		store.mu.Unlock()
		if queued {
			break
		}
		runtime.Gosched()
	}
	cancelWait()
	if err := <-waitResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error=%v", err)
	}
	third := make(chan func(), 1)
	go func() {
		finish, err := store.begin(context.Background())
		if err != nil {
			third <- nil
			return
		}
		third <- finish
	}()
	finishFirst()
	finishThird := <-third
	if finishThird == nil {
		t.Fatal("third transaction did not acquire turn")
	}
	finishThird()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.begin(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("immediate turn error=%v", err)
	}
	if _, err := store.Load(cancelled, "profile"); !errors.Is(err, context.Canceled) {
		t.Fatalf("load error=%v", err)
	}
	if err := store.writeFile(cancelled, map[string]tokenRecord{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("write error=%v", err)
	}
}

// TestOAuthSignInStopsAtEveryFailedBoundary verifies no partial profile survives external failures.
func TestOAuthSignInStopsAtEveryFailedBoundary(t *testing.T) {
	cases := []struct {
		name string
		fail func(*Service, *fakeGateway, *fakeBrowser)
	}{
		{"random", func(service *Service, _ *fakeGateway, _ *fakeBrowser) { service.random = &fakeRandom{} }},
		{"registration", func(_ *Service, gateway *fakeGateway, _ *fakeBrowser) { gateway.registerErr = errors.New("register") }},
		{"browser", func(_ *Service, _ *fakeGateway, browser *fakeBrowser) { browser.err = errors.New("browser") }},
		{"listener", func(service *Service, _ *fakeGateway, _ *fakeBrowser) {
			service.listener = fakeListener{err: errors.New("listener")}
		}},
		{"exchange", func(_ *Service, gateway *fakeGateway, _ *fakeBrowser) { gateway.exchangeErr = errors.New("exchange") }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service, gateway, store, browser := newService(t)
			test.fail(service, gateway, browser)
			if err := service.SignIn(context.Background(), "profile"); err == nil {
				t.Fatal("failed boundary accepted")
			}
			if len(store.records) != 0 {
				t.Fatal("partial profile persisted")
			}
		})
	}
}

// TestOAuthFailedRevocationClearsLocalAccessAndReportsFailure verifies sign-out stops inference.
func TestOAuthFailedRevocationClearsLocalAccessAndReportsFailure(t *testing.T) {
	service, gateway, store, _ := newService(t)
	store.records["profile"] = tokenRecord{ProfileID: "profile", ClientID: "client", Subject: "subject", AccessToken: "access", RefreshToken: "refresh", IDToken: "id", Scopes: []string{requiredDirectScope}, ExpiresAt: time.Now().Add(time.Hour)}
	gateway.revokeErr = errors.New("offline")
	if err := service.SignOut(context.Background(), "profile"); err == nil {
		t.Fatal("unconfirmed revocation reported as success")
	}
	if record := store.records["profile"]; record.AccessToken != "" || record.RefreshToken != "" || record.ClientID != "client" {
		t.Fatalf("local inference credential retained: %+v", record)
	}
}

// TestOAuthReauthorizationReusesIssuedClientAndRetainedIdentityHint verifies profile updates never re-register.
func TestOAuthReauthorizationReusesIssuedClientAndRetainedIdentityHint(t *testing.T) {
	service, gateway, store, _ := newService(t)
	store.records["profile"] = tokenRecord{ProfileID: "profile", ClientID: "issued-client", Subject: "subject-a", AccessToken: "old-access", RefreshToken: "old-refresh", IDToken: "retained-id", Scopes: []string{requiredDirectScope}, ExpiresAt: time.Now().Add(time.Hour)}
	service.listener = fakeListener{callback: Callback{State: "state", Code: "code"}}
	if err := service.SignIn(context.Background(), "profile"); err != nil {
		t.Fatal(err)
	}
	if gateway.registration.ClientID != "issued-client" || gateway.registration.AppName != "" || gateway.registration.IDTokenHint != "retained-id" || gateway.exchange.ClientID != "issued-client" {
		t.Fatalf("registration=%+v exchange=%+v", gateway.registration, gateway.exchange)
	}
	record := store.records["profile"]
	if record.AccessToken != "access-one" || record.Subject != "subject-a" {
		t.Fatalf("record=%+v", record)
	}
}
