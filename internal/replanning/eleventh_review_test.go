package replanning

import (
	"math/big"
	"testing"
	"testing/quick"
	"time"
)

// ownershipEnvelopeOracle evaluates the lease inequality with arbitrary precision for an independent test oracle.
func ownershipEnvelopeOracle(ttl, renewInterval, storeOperationTimeout time.Duration) bool {
	if ttl <= 0 || renewInterval <= 0 || storeOperationTimeout <= 0 {
		return false
	}
	required := new(big.Int).Mul(big.NewInt(int64(storeOperationTimeout)), big.NewInt(2))
	required.Add(required, big.NewInt(int64(renewInterval)))
	return required.IsInt64() && required.Cmp(big.NewInt(int64(ttl))) < 0
}

// ownershipConfigForBoundary creates a complete config so each case isolates temporal validation.
func ownershipConfigForBoundary(ttl, renewInterval, storeOperationTimeout time.Duration) OwnershipConfig {
	finalizeTimeout := time.Duration(0)
	if ttl > 0 {
		finalizeTimeout = ttl - 1
	}
	return OwnershipConfig{
		LeaseTTL:              ttl,
		RenewInterval:         renewInterval,
		StoreOperationTimeout: storeOperationTimeout,
		FinalizeTimeout:       finalizeTimeout,
		Clock:                 &controlledLeaseClock{now: time.Unix(1, 0)},
		Waiter:                &controlledLeaseWaiter{ticks: make(chan struct{}, 1)},
	}
}

// TestOwnershipConfigDurationBoundaries covers equality, representable successors and every int64 overflow edge.
func TestOwnershipConfigDurationBoundaries(t *testing.T) {
	const maxDuration = time.Duration(1<<63 - 1)
	const minDuration = time.Duration(-1 << 63)
	largestStoreWithSuccessor := (maxDuration - 2) / 2
	tests := []struct {
		name                        string
		ttl, renewInterval, storeTO time.Duration
		valid                       bool
	}{
		{name: "zero ttl", ttl: 0, renewInterval: 1, storeTO: 1},
		{name: "negative ttl", ttl: -1, renewInterval: 1, storeTO: 1},
		{name: "minimum ttl", ttl: minDuration, renewInterval: 1, storeTO: 1},
		{name: "zero renew", ttl: 4, renewInterval: 0, storeTO: 1},
		{name: "negative renew", ttl: 4, renewInterval: -1, storeTO: 1},
		{name: "zero store", ttl: 4, renewInterval: 1, storeTO: 0},
		{name: "negative store", ttl: 4, renewInterval: 1, storeTO: -1},
		{name: "ordinary equality", ttl: 9, renewInterval: 1, storeTO: 4},
		{name: "ordinary successor", ttl: 10, renewInterval: 1, storeTO: 4, valid: true},
		{name: "max store", ttl: maxDuration, renewInterval: 1, storeTO: maxDuration},
		{name: "half max reaches equality", ttl: maxDuration, renewInterval: 1, storeTO: maxDuration / 2},
		{name: "renew addition overflows", ttl: maxDuration, renewInterval: maxDuration - 1, storeTO: 1},
		{name: "largest representable equality with successor", ttl: 2*largestStoreWithSuccessor + 1, renewInterval: 1, storeTO: largestStoreWithSuccessor},
		{name: "largest representable successor", ttl: 2*largestStoreWithSuccessor + 2, renewInterval: 1, storeTO: largestStoreWithSuccessor, valid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := ownershipConfigForBoundary(tt.ttl, tt.renewInterval, tt.storeTO)
			if got := config.Validate() == nil; got != tt.valid {
				t.Fatalf("Validate validity = %t, want %t", got, tt.valid)
			}
			if got := ownershipEnvelopeOracle(tt.ttl, tt.renewInterval, tt.storeTO); got != tt.valid {
				t.Fatalf("oracle validity = %t, want %t", got, tt.valid)
			}
		})
	}
}

// TestOwnershipConfigValidationMatchesArbitraryPrecision checks the full int64 input domain without panics.
func TestOwnershipConfigValidationMatchesArbitraryPrecision(t *testing.T) {
	property := func(ttl, renewInterval, storeOperationTimeout int64) bool {
		config := ownershipConfigForBoundary(time.Duration(ttl), time.Duration(renewInterval), time.Duration(storeOperationTimeout))
		got := config.Validate() == nil
		want := ownershipEnvelopeOracle(config.LeaseTTL, config.RenewInterval, config.StoreOperationTimeout)
		return got == want
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 100_000}); err != nil {
		t.Fatal(err)
	}
}

// FuzzOwnershipConfigValidateNeverPanics preserves total validation across arbitrary duration bit patterns.
func FuzzOwnershipConfigValidateNeverPanics(f *testing.F) {
	const maxDuration = int64(1<<63 - 1)
	seeds := [][3]int64{{0, 0, 0}, {-1, 1, 1}, {maxDuration, 1, maxDuration}, {maxDuration, 1, maxDuration / 2}, {maxDuration, maxDuration - 1, 1}, {10, 1, 4}}
	for _, seed := range seeds {
		f.Add(seed[0], seed[1], seed[2])
	}
	f.Fuzz(func(t *testing.T, ttl, renewInterval, storeOperationTimeout int64) {
		config := ownershipConfigForBoundary(time.Duration(ttl), time.Duration(renewInterval), time.Duration(storeOperationTimeout))
		got := config.Validate() == nil
		want := ownershipEnvelopeOracle(config.LeaseTTL, config.RenewInterval, config.StoreOperationTimeout)
		if got != want {
			t.Fatalf("Validate validity = %t, want %t for ttl=%d renew=%d store=%d", got, want, ttl, renewInterval, storeOperationTimeout)
		}
	})
}

// TestNewCoordinatorRejectsOverflowingOwnershipConfig proves composition enforces the same safe boundary.
func TestNewCoordinatorRejectsOverflowingOwnershipConfig(t *testing.T) {
	coordinator, _, _, _, _, _ := coordinatorFixture(t, "pending", testLimits(), `{"version":1,"decision":"terminate"}`)
	compose := func(config OwnershipConfig) error {
		_, err := NewCoordinator(coordinator.run, coordinator.model, coordinator.summary, coordinator.budgets, coordinator.journal, coordinator.ledger, coordinator.fingerprints, coordinator.preparer, coordinator.approval, coordinator.executor, coordinator.limits, coordinator.base, coordinator.revision, config)
		return err
	}
	const maxDuration = time.Duration(1<<63 - 1)
	if err := compose(ownershipConfigForBoundary(maxDuration, 1, maxDuration)); err == nil {
		t.Fatal("NewCoordinator accepted an overflowing ownership envelope")
	}
	largestStoreWithSuccessor := (maxDuration - 2) / 2
	if err := compose(ownershipConfigForBoundary(2*largestStoreWithSuccessor+2, 1, largestStoreWithSuccessor)); err != nil {
		t.Fatalf("NewCoordinator rejected the largest representable valid envelope: %v", err)
	}
}
