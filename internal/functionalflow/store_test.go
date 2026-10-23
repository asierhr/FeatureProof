package functionalflow

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/planning"
)

// mutableClock provides deterministic lease expiry without wall-clock sleeps.
type mutableClock struct {
	mu  sync.Mutex
	now time.Time
}

// Now returns the synchronized trusted store time.
func (c *mutableClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

// Advance moves trusted time across a lease boundary deterministically.
func (c *mutableClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

// TestLocalStoreLeasesIntentsAndTerminalsConverge proves process-safe ownership and durable idempotent recovery.
func TestLocalStoreLeasesIntentsAndTerminalsConverge(t *testing.T) {
	clock := &mutableClock{now: time.Unix(100, 0)}
	store, err := NewLocalStore(t.TempDir(), clock, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claim, acquired, err := store.Claim(context.Background(), "run", 1, "act", "owner-a")
	if err != nil || !acquired || claim.Token == "" {
		t.Fatalf("claim=%#v acquired=%v err=%v", claim, acquired, err)
	}
	if _, acquired, err := store.Claim(context.Background(), "run", 1, "act", "owner-b"); err != nil || acquired {
		t.Fatalf("second acquired=%v err=%v", acquired, err)
	}
	intent := Intent{RunID: "run", Revision: 1, StepID: "act", BindingDigest: digestValue("binding"), KeyDigest: digestValue("key"), ClaimToken: claim.Token}
	if err := store.ConfirmIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := store.Intent(context.Background(), "run", 1, "act")
	if err != nil || !ok || loaded.ClaimToken != "" || !sameIntent(loaded, intent) {
		t.Fatalf("intent=%#v ok=%v err=%v", loaded, ok, err)
	}
	record := StepRecord{RunID: "run", Revision: 1, StepID: "act", Status: planning.StepPassed, EvidenceID: "e", ClaimToken: claim.Token}
	if err := store.ConfirmTerminal(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	terminal, ok, err := store.Terminal(context.Background(), "run", 1, "act")
	if err != nil || !ok || terminal.ClaimToken != "" || terminal.Status != planning.StepPassed {
		t.Fatalf("terminal=%#v ok=%v err=%v", terminal, ok, err)
	}
	if _, acquired, err := store.Claim(context.Background(), "run", 1, "act", "owner-b"); err != nil || acquired {
		t.Fatalf("terminal reclaimed=%v err=%v", acquired, err)
	}
}

// TestLocalStoreExpiredLeaseHandoffRejectsStaleOwner proves a new owner can reclaim without accepting stale confirmation.
func TestLocalStoreExpiredLeaseHandoffRejectsStaleOwner(t *testing.T) {
	clock := &mutableClock{now: time.Unix(100, 0)}
	store, _ := NewLocalStore(t.TempDir(), clock, time.Minute)
	first, _, _ := store.Claim(context.Background(), "run", 1, "step", "first")
	clock.Advance(2 * time.Minute)
	second, acquired, err := store.Claim(context.Background(), "run", 1, "step", "second")
	if err != nil || !acquired || second.Token == first.Token {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	stale := StepRecord{RunID: "run", Revision: 1, StepID: "step", Status: planning.StepPassed, ClaimToken: first.Token}
	if err := store.ConfirmTerminal(context.Background(), stale); err == nil {
		t.Fatal("stale owner confirmed")
	}
	stale.ClaimToken = second.Token
	if err := store.ConfirmTerminal(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
}

// TestLocalStoreRenewClaimExtendsOnlyTheCurrentUnexpiredOwner proves durable post-tool renewal cannot revive stale authority.
func TestLocalStoreRenewClaimExtendsOnlyTheCurrentUnexpiredOwner(t *testing.T) {
	clock := &mutableClock{now: time.Unix(100, 0)}
	store, _ := NewLocalStore(t.TempDir(), clock, time.Minute)
	claim, _, _ := store.Claim(context.Background(), "run", 1, "step", "owner")
	clock.Advance(30 * time.Second)
	renewed, err := store.RenewClaim(context.Background(), "run", 1, "step", claim)
	if err != nil || renewed.Token != claim.Token || !renewed.ExpiresAt.Equal(time.Unix(190, 0)) {
		t.Fatalf("renewed=%#v err=%v", renewed, err)
	}
	foreign := claim
	foreign.Owner = "other"
	if _, err = store.RenewClaim(context.Background(), "run", 1, "step", foreign); err == nil {
		t.Fatal("foreign owner renewed claim")
	}
	clock.Advance(61 * time.Second)
	if _, err = store.RenewClaim(context.Background(), "run", 1, "step", renewed); err == nil {
		t.Fatal("expired owner renewed claim")
	}
	if _, err = store.RenewClaim(context.Background(), "bad/path", 1, "step", renewed); err == nil {
		t.Fatal("invalid coordinates renewed claim")
	}
}

// TestLocalStoreRejectsTraversalTamperingAndContradictoryReplay proves filesystem and integrity failures close the run.
func TestLocalStoreRejectsTraversalTamperingAndContradictoryReplay(t *testing.T) {
	clock := &mutableClock{now: time.Unix(100, 0)}
	root := t.TempDir()
	store, _ := NewLocalStore(root, clock, time.Minute)
	if _, _, err := store.Claim(context.Background(), "../escape", 1, "step", "owner"); err == nil {
		t.Fatal("traversal accepted")
	}
	claim, _, _ := store.Claim(context.Background(), "run", 1, "step", "owner")
	record := StepRecord{RunID: "run", Revision: 1, StepID: "step", Status: planning.StepPassed, ClaimToken: claim.Token}
	if err := store.ConfirmTerminal(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	record.Status = planning.StepFailed
	record.ClaimToken = ""
	if err := store.ConfirmTerminal(context.Background(), record); err == nil {
		t.Fatal("contradictory terminal accepted")
	}
	raw, err := os.ReadFile(store.path("run"))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 1
	if err := os.WriteFile(store.path("run"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Terminal(context.Background(), "run", 1, "step"); err == nil {
		t.Fatal("tampered state accepted")
	}
}

// TestLocalStoreConcurrentClaimsSelectOneOwner proves independent deliveries cannot both own a step.
func TestLocalStoreConcurrentClaimsSelectOneOwner(t *testing.T) {
	clock := &mutableClock{now: time.Unix(100, 0)}
	store, _ := NewLocalStore(t.TempDir(), clock, time.Minute)
	start := make(chan struct{})
	results := make(chan bool, 2)
	var wait sync.WaitGroup
	for _, owner := range []string{"a", "b"} {
		wait.Add(1)
		go func(owner string) {
			defer wait.Done()
			<-start
			_, acquired, _ := store.Claim(context.Background(), "run", 1, "step", owner)
			results <- acquired
		}(owner)
	}
	close(start)
	wait.Wait()
	close(results)
	acquired := 0
	for result := range results {
		if result {
			acquired++
		}
	}
	if acquired != 1 {
		t.Fatalf("owners acquired=%d", acquired)
	}
}

// TestLocalStoreRejectsInvalidAndCancelledTransitions proves every public durable boundary fails before creating state for malformed or cancelled work.
func TestLocalStoreRejectsInvalidAndCancelledTransitions(t *testing.T) {
	clock := &mutableClock{now: time.Unix(100, 0)}
	for _, config := range []struct {
		root  string
		clock Clock
		ttl   time.Duration
	}{{root: "", clock: clock, ttl: time.Minute}, {root: t.TempDir(), clock: nil, ttl: time.Minute}, {root: t.TempDir(), clock: clock, ttl: 0}} {
		if _, err := NewLocalStore(config.root, config.clock, config.ttl); err == nil {
			t.Fatal("invalid store configuration was accepted")
		}
	}
	store, err := NewLocalStore(t.TempDir(), clock, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Claim(context.Background(), "run", 0, "step", "owner"); err == nil {
		t.Fatal("zero revision claim was accepted")
	}
	if err = store.ConfirmIntent(context.Background(), Intent{}); err == nil {
		t.Fatal("empty intent was accepted")
	}
	if err = store.ConfirmTerminal(context.Background(), StepRecord{}); err == nil {
		t.Fatal("empty terminal was accepted")
	}
	if _, ok, err := store.Intent(context.Background(), "run", 1, "missing"); err != nil || ok {
		t.Fatalf("missing intent ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.Terminal(context.Background(), "run", 1, "missing"); err != nil || ok {
		t.Fatalf("missing terminal ok=%v err=%v", ok, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = store.Claim(cancelled, "cancelled", 1, "step", "owner"); err == nil {
		t.Fatal("cancelled claim was accepted")
	}
	claim, acquired, err := store.Claim(context.Background(), "run", 1, "act", "owner")
	if err != nil || !acquired {
		t.Fatal(err)
	}
	intent := Intent{RunID: "run", Revision: 1, StepID: "act", BindingDigest: digestValue("binding"), ClaimToken: claim.Token, EvidenceID: "evidence"}
	if err = store.ConfirmIntent(context.Background(), intent); err == nil {
		t.Fatal("evidence without effect checkpoint was accepted")
	}
	intent.EvidenceID = ""
	intent.ClaimToken = "stale"
	if err = store.ConfirmIntent(context.Background(), intent); err == nil {
		t.Fatal("stale intent owner was accepted")
	}
	intent.ClaimToken = claim.Token
	if err = store.ConfirmIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if err = store.ConfirmIntent(context.Background(), intent); err != nil {
		t.Fatalf("identical intent redelivery failed: %v", err)
	}
	intent.EffectStarted = true
	if err = store.ConfirmIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	intent.EffectStarted = false
	if err = store.ConfirmIntent(context.Background(), intent); err == nil {
		t.Fatal("effect checkpoint regression was accepted")
	}
	terminal := StepRecord{RunID: "run", Revision: 1, StepID: "act", Status: planning.StepInconclusive, ClaimToken: "stale"}
	if err = store.ConfirmTerminal(context.Background(), terminal); err == nil {
		t.Fatal("stale terminal owner was accepted")
	}
	terminal.ClaimToken = claim.Token
	if err = store.ConfirmTerminal(context.Background(), terminal); err != nil {
		t.Fatal(err)
	}
	terminal.ClaimToken = ""
	if err = store.ConfirmTerminal(context.Background(), terminal); err != nil {
		t.Fatalf("identical terminal redelivery failed: %v", err)
	}
}
