package cleanup

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"
)

// Clock supplies deterministic controller time.
type Clock interface{ Now() time.Time }

// Waiter owns cancelable periodic and retry waits.
type Waiter interface {
	Wait(context.Context, time.Duration) error
}

// IntentSource projects durable cleanup obligations without workflow mutation authority.
type IntentSource interface {
	ScanCleanupIntents(context.Context, string, int) (Page, error)
}

// DiscoverySource scans one durable lifecycle or capacity index into complete cleanup candidates.
type DiscoverySource interface {
	ScanCleanupCandidates(context.Context, string, int) (Page, error)
}

// LifecycleStatus reports whether exact sealed authority remains present.
type LifecycleStatus string

// Lifecycle statuses form the closed provider-neutral cleanup observation.
const (
	LifecyclePresent   LifecycleStatus = "present"
	LifecycleAbsent    LifecycleStatus = "absent"
	LifecycleUncertain LifecycleStatus = "uncertain"
)

// LifecycleCleanup performs known lifecycle reconciliation using full sealed authority.
type LifecycleCleanup interface {
	ReconcileUncertain(context.Context, UncertainAcquisitionBinding) (LifecycleStatus, LifecycleCleanupBinding, error)
	Release(context.Context, LifecycleCleanupBinding) error
	Status(context.Context, LifecycleCleanupBinding) (LifecycleStatus, error)
}

// InventoryCandidate is a safe provider-owned orphan candidate.
type InventoryCandidate struct {
	Item                         Item
	OwnerDigest, SnapshotVersion string
}

// InventoryPage is a bounded managed provider inventory page.
type InventoryPage struct {
	Candidates            []InventoryCandidate
	Quarantines           []InventoryQuarantine
	Next, SnapshotVersion string
}

// InventoryQuarantine carries only safe project scope and opaque identity for malformed owned resources.
type InventoryQuarantine struct {
	ProjectID, IdentityDigest string
}

// ManagedInventory lists only configured owner scope and reconciles exact orphan cleanup.
type ManagedInventory interface {
	List(context.Context, string, int, string) (InventoryPage, error)
	ReconcileCleanup(context.Context, Item) (LifecycleStatus, error)
}

// OrphanAuthority proves both lifecycle and workflow absence without interpreting transport errors.
type OrphanAuthority interface {
	LifecycleAbsent(context.Context, LifecycleCleanupBinding) (bool, error)
	RunAbsent(context.Context, string, string) (bool, error)
}

// Observer receives safe operational projections and never participates in authoritative commits.
type Observer interface {
	ObserveCleanup(context.Context, Status) error
}

// Config explicitly bounds every controller cycle, lease and retry.
type Config struct {
	PageSize, MaxPages, MaxItems, MaxClaims, MaxAttemptsPerItem                          int
	Interval, LeaseTTL, ProviderTimeout, MaxBackoff, MaxCumulativeWait, MaxCycleDuration time.Duration
	Owner                                                                                string
	OrphanGrace                                                                          time.Duration
}

// Dependencies supplies all replaceable local controller boundaries.
type Dependencies struct {
	Store       Store
	Intents     IntentSource
	Lifecycle   LifecycleCleanup
	Inventories map[string]ManagedInventory
	Orphans     OrphanAuthority
	Sources     map[string]DiscoverySource
	Observer    Observer
	Ledger      CapacityLedgerPort
	Clock       Clock
	Waiter      Waiter
}

// CapacityLedgerPort is the cleanup-owned quota boundary required for orphan adoption and confirmed release.
type CapacityLedgerPort interface {
	AdoptUnknown(context.Context, string, string, string, string, ResourceDemand, time.Duration) (CapacityReceipt, bool, error)
	BlockUnknown(context.Context, string, string) (CapacityReceipt, bool, error)
	ConfirmEnvironment(context.Context, CapacityReceipt, string, string) (CapacityReceipt, error)
	ReleaseConfirmed(context.Context, string, string, string, string, string) (CapacityReceipt, bool, error)
	Snapshot(string) (CapacitySnapshot, error)
}

// localCapacityLedgerContract prevents the local implementation from drifting from controller semantics.
var localCapacityLedgerContract CapacityLedgerPort = (*CapacityLedger)(nil)

// Status reads the local operational projection without resolving lifecycle authority from a run ID.
func (c *Controller) Status(ctx context.Context, cleanupID string) (Status, error) {
	return c.deps.Store.Status(ctx, cleanupID)
}

// observe publishes best-effort safe status after authoritative local transitions.
func (c *Controller) observe(ctx context.Context, cleanupID string) {
	if c.deps.Observer == nil {
		return
	}
	status, err := c.deps.Store.Status(ctx, cleanupID)
	if err == nil {
		_ = c.deps.Observer.ObserveCleanup(ctx, status)
	}
}

// Controller independently discovers, claims and reconciles durable cleanup obligations.
type Controller struct {
	config Config
	deps   Dependencies
}

// NewController validates fail-closed composition without starting background work.
func NewController(config Config, deps Dependencies) (*Controller, error) {
	if config.PageSize <= 0 || config.MaxPages <= 0 || config.MaxItems <= 0 || config.MaxClaims <= 0 || config.MaxAttemptsPerItem <= 0 || config.Interval <= 0 || config.LeaseTTL <= 0 || config.ProviderTimeout <= 0 || config.LeaseTTL <= config.ProviderTimeout || config.MaxBackoff <= 0 || config.MaxCumulativeWait <= 0 || config.MaxCycleDuration <= 0 || config.OrphanGrace < 0 || !safeID.MatchString(config.Owner) || deps.Store == nil || deps.Lifecycle == nil || deps.Ledger == nil || deps.Clock == nil || deps.Waiter == nil {
		return nil, &Error{Category: CategoryInvalid, Operation: "controller_compose"}
	}
	if _, ok := deps.Store.(CursorStore); !ok {
		return nil, &Error{Category: CategoryInvalid, Operation: "controller_cursor_store"}
	}
	if deps.Intents != nil {
		if _, ok := deps.Store.(PassCursorStore); !ok {
			return nil, &Error{Category: CategoryInvalid, Operation: "controller_pass_cursor_store"}
		}
	}
	for name, source := range deps.Sources {
		if !safeID.MatchString(name) || source == nil {
			return nil, &Error{Category: CategoryInvalid, Operation: "controller_discovery_source"}
		}
	}
	return &Controller{config: config, deps: deps}, nil
}

// Run executes bounded cycles until cancellation and owns no hidden goroutine.
func (c *Controller) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		if err := c.deps.Waiter.Wait(ctx, c.config.Interval); err != nil {
			return err
		}
	}
}

// RunOnce discovers and processes a bounded amount of durable work.
func (c *Controller) RunOnce(ctx context.Context) error {
	cycleCtx, cancel := context.WithTimeout(ctx, c.config.MaxCycleDuration)
	defer cancel()
	if c.deps.Intents != nil {
		if err := c.discoverIntents(cycleCtx); err != nil {
			return err
		}
	}
	if err := c.discoverSources(cycleCtx); err != nil {
		return err
	}
	if len(c.deps.Inventories) > 0 {
		if err := c.discoverInventories(cycleCtx); err != nil {
			return err
		}
	}
	page, cursor, claims := 0, "", 0
	for page < c.config.MaxPages && claims < c.config.MaxClaims {
		if err := cycleCtx.Err(); err != nil {
			return err
		}
		result, err := c.deps.Store.Scan(cycleCtx, cursor, c.config.PageSize)
		if err != nil {
			return err
		}
		if len(result.Items) == 0 && result.Next != "" {
			return &Error{Category: CategoryIntegrity, Operation: "scan_page"}
		}
		for _, item := range result.Items {
			if claims >= c.config.MaxClaims {
				break
			}
			now := c.deps.Clock.Now().UTC()
			if !eligible(item, now) {
				continue
			}
			claimed, ok, claimErr := c.deps.Store.Claim(cycleCtx, item.CleanupID, c.config.Owner, now, c.config.LeaseTTL)
			if claimErr != nil {
				return claimErr
			}
			if !ok {
				continue
			}
			claims++
			if err = c.reconcile(cycleCtx, claimed); err != nil && errors.Is(err, context.Canceled) {
				return err
			}
		}
		page++
		if result.Next == "" {
			break
		}
		if result.Next == cursor {
			return &Error{Category: CategoryIntegrity, Operation: "scan_cycle"}
		}
		cursor = result.Next
	}
	return nil
}

// discoverSources deterministically merges lifecycle and capacity candidates by cleanup identity.
func (c *Controller) discoverSources(ctx context.Context) error {
	names := make([]string, 0, len(c.deps.Sources))
	for name := range c.deps.Sources {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := c.discoverSource(ctx, name, c.deps.Sources[name]); err != nil {
			return err
		}
	}
	return nil
}

// discoverSource advances its durable cursor only after every candidate on the page is persisted.
func (c *Controller) discoverSource(ctx context.Context, name string, source DiscoverySource) error {
	cursors := c.deps.Store.(CursorStore)
	checkpointName := "source-" + name
	cursor, err := cursors.LoadCursor(ctx, checkpointName)
	if err != nil {
		return err
	}
	items := 0
	for page := 0; page < c.config.MaxPages && items < c.config.MaxItems; page++ {
		result, scanErr := source.ScanCleanupCandidates(ctx, cursor, minInt(c.config.PageSize, c.config.MaxItems-items))
		if scanErr != nil {
			return scanErr
		}
		if len(result.Items) == 0 && result.Next != "" && !result.Scanned || result.Next == cursor && result.Next != "" {
			return &Error{Category: CategoryIntegrity, Operation: "discovery_page"}
		}
		for _, item := range result.Items {
			if VerifyItem(item) != nil {
				return &Error{Category: CategoryIntegrity, Operation: "discovery_candidate"}
			}
			event := Event{ID: item.CleanupID + ":" + name + ":discovered", Kind: "cleanup_discovered", At: c.deps.Clock.Now()}
			if _, _, err = c.deps.Store.Reserve(ctx, item, event); err != nil && !IsCategory(err, CategoryConflict) {
				return err
			}
			if err != nil {
				return err
			}
			items++
		}
		if err = cursors.SaveCursor(ctx, checkpointName, cursor, result.Next); err != nil {
			return err
		}
		if result.Next == "" {
			break
		}
		cursor = result.Next
	}
	return nil
}

// discoverIntents persists every projected workflow intent before cleanup I/O.
func (c *Controller) discoverIntents(ctx context.Context) error {
	cursors, ok := c.deps.Store.(PassCursorStore)
	if !ok {
		return &Error{Category: CategoryInvalid, Operation: "intent_cursor_store"}
	}
	pass, err := cursors.LoadCursorPass(ctx, "workflow-intents")
	if err != nil {
		return err
	}
	seen := map[string]struct{}{}
	if pass.Value != "" {
		seen[pass.Value] = struct{}{}
	}
	for page := 0; page < c.config.MaxPages; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := c.deps.Intents.ScanCleanupIntents(ctx, pass.Value, c.config.PageSize)
		if err != nil {
			return err
		}
		if !result.Complete && result.Next == "" || result.Complete && result.Next != "" || len(result.Items) == 0 && result.Next != "" && !result.Scanned {
			return &Error{Category: CategoryIntegrity, Operation: "intent_page"}
		}
		_, repeated := seen[result.Next]
		if result.Next != "" && (repeated || pass.Value != "" && result.Next <= pass.Value) {
			return &Error{Category: CategoryIntegrity, Operation: "intent_cursor"}
		}
		for _, item := range result.Items {
			event := Event{ID: item.CleanupID + ":intent", Kind: "cleanup_intent_committed", At: c.deps.Clock.Now()}
			if _, _, err = c.deps.Store.Reserve(ctx, item, event); err != nil && !IsCategory(err, CategoryConflict) {
				return err
			}
		}
		if result.Complete {
			nextPass := CursorPass{Epoch: pass.Epoch + 1}
			if pass.Epoch == ^uint64(0) {
				return &Error{Category: CategoryIntegrity, Operation: "intent_epoch_overflow"}
			}
			if err = cursors.SaveCursorPass(ctx, "workflow-intents", pass, nextPass); err != nil {
				return err
			}
			return nil
		}
		nextPass := CursorPass{Epoch: pass.Epoch, Value: result.Next}
		if err = cursors.SaveCursorPass(ctx, "workflow-intents", pass, nextPass); err != nil {
			return err
		}
		seen[result.Next] = struct{}{}
		pass = nextPass
	}
	return nil
}

// inventoryCheckpoint persists an opaque provider token together with its immutable list snapshot.
type inventoryCheckpoint struct {
	Token    string `json:"token"`
	Snapshot string `json:"snapshot"`
}

// discoverInventories adopts only expired, grace-qualified resources whose lifecycle and run are authoritatively absent.
func (c *Controller) discoverInventories(ctx context.Context) error {
	if c.deps.Orphans == nil {
		return &Error{Category: CategoryInvalid, Operation: "orphan_authority"}
	}
	keys := make([]string, 0, len(c.deps.Inventories))
	for key := range c.deps.Inventories {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := c.discoverInventory(ctx, key, c.deps.Inventories[key]); err != nil {
			return err
		}
	}
	return nil
}

// discoverInventory checkpoints each successfully persisted page and rejects snapshot/token ambiguity.
func (c *Controller) discoverInventory(ctx context.Context, key string, inventory ManagedInventory) error {
	if inventory == nil || key == "" {
		return &Error{Category: CategoryInvalid, Operation: "inventory_source"}
	}
	cursors := c.deps.Store.(CursorStore)
	source := "inventory-" + digestOf(key)[:24]
	raw, err := cursors.LoadCursor(ctx, source)
	if err != nil {
		return err
	}
	checkpoint := inventoryCheckpoint{}
	if raw != "" && json.Unmarshal([]byte(raw), &checkpoint) != nil {
		return &Error{Category: CategoryIntegrity, Operation: "inventory_checkpoint"}
	}
	items := 0
	for page := 0; page < c.config.MaxPages && items < c.config.MaxItems; page++ {
		result, listErr := inventory.List(ctx, checkpoint.Token, minInt(c.config.PageSize, c.config.MaxItems-items), checkpoint.Snapshot)
		if listErr != nil {
			return listErr
		}
		if len(result.Candidates) == 0 && result.Next != "" || checkpoint.Snapshot != "" && result.SnapshotVersion != checkpoint.Snapshot || result.Next == checkpoint.Token && result.Next != "" {
			return &Error{Category: CategoryIntegrity, Operation: "inventory_page"}
		}
		if result.Next != "" && result.SnapshotVersion == "" {
			return &Error{Category: CategoryIntegrity, Operation: "inventory_snapshot"}
		}
		for _, candidate := range result.Candidates {
			items++
			if err = c.adoptOrphan(ctx, key, candidate); err != nil {
				return err
			}
		}
		for _, quarantine := range result.Quarantines {
			if _, _, err = c.deps.Ledger.BlockUnknown(ctx, quarantine.ProjectID, quarantine.IdentityDigest); err != nil {
				return err
			}
		}
		next := ""
		if result.Next != "" {
			encoded, encodeErr := json.Marshal(inventoryCheckpoint{Token: result.Next, Snapshot: result.SnapshotVersion})
			if encodeErr != nil {
				return &Error{Category: CategoryIntegrity, Operation: "inventory_checkpoint", Cause: encodeErr}
			}
			next = string(encoded)
		}
		if err = cursors.SaveCursor(ctx, source, raw, next); err != nil {
			return err
		}
		raw = next
		checkpoint = inventoryCheckpoint{Token: result.Next, Snapshot: result.SnapshotVersion}
		if result.Next == "" {
			break
		}
	}
	return nil
}

// adoptOrphan reserves fail-closed capacity and cleanup intent only after authoritative absence and grace.
func (c *Controller) adoptOrphan(ctx context.Context, source string, candidate InventoryCandidate) error {
	item := candidate.Item
	if VerifyItem(item) != nil || item.Source != "orphan" || candidate.OwnerDigest != item.OwnerDigest || item.Binding.ProviderID+"@"+item.Binding.ProviderVersion != source || candidate.SnapshotVersion == "" {
		return &Error{Category: CategoryIntegrity, Operation: "orphan_candidate"}
	}
	now := c.deps.Clock.Now().UTC()
	graceDeadline := item.ExpiresAt.Add(c.config.OrphanGrace)
	if graceDeadline.Before(item.ExpiresAt) || now.Before(graceDeadline) {
		return nil
	}
	lifecycleAbsent, err := c.deps.Orphans.LifecycleAbsent(ctx, item.Binding)
	if err != nil {
		return err
	}
	runAbsent, err := c.deps.Orphans.RunAbsent(ctx, item.ProjectID, item.RunID)
	if err != nil {
		return err
	}
	if !lifecycleAbsent || !runAbsent {
		return nil
	}
	receipt, _, err := c.deps.Ledger.AdoptUnknown(ctx, item.ProjectID, item.RunID, item.Binding.AcquisitionKey, item.Binding.CapacityReceiptDigest, item.Demand, item.TTL)
	if err != nil {
		return err
	}
	if receipt.EnvironmentDigest == "" {
		if _, err = c.deps.Ledger.ConfirmEnvironment(ctx, receipt, item.Binding.EnvironmentDigest, item.Binding.ResourceIdentityDigest); err != nil {
			return err
		}
	}
	event := Event{ID: item.CleanupID + ":orphan-intent", Kind: "orphan_cleanup_intent_committed", At: now}
	_, _, err = c.deps.Store.Reserve(ctx, item, event)
	if IsCategory(err, CategoryConflict) {
		return nil
	}
	return err
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

// reconcile transitions one claimed item through exact provider confirmation and quota release.
func (c *Controller) reconcile(ctx context.Context, item Item) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if item.State == StateConfirmed {
		return c.releaseConfirmedCapacity(ctx, item)
	}
	item.State = StateReconciling
	event := Event{ID: item.CleanupID + ":reconciling:" + itoa(item.FencingVersion), Kind: "cleanup_reconciling", At: c.deps.Clock.Now()}
	saved, err := c.deps.Store.SaveOwned(ctx, item, c.config.Owner, item.FencingVersion, event)
	if err != nil {
		return err
	}
	item = saved
	providerCtx, cancel := context.WithTimeout(ctx, c.config.ProviderTimeout)
	defer cancel()
	if item.Uncertain != nil {
		status, binding, reconcileErr := c.deps.Lifecycle.ReconcileUncertain(providerCtx, *item.Uncertain)
		if reconcileErr != nil {
			return c.handleFailure(ctx, item, reconcileErr)
		}
		if status == LifecycleUncertain || VerifyLifecycleCleanupBinding(binding) != nil || !bindingMatchesUncertain(binding, *item.Uncertain) {
			return c.handleFailure(ctx, item, &Error{Category: CategoryIntegrity, Operation: "uncertain_reconcile"})
		}
		item.Binding, item.Uncertain, item.State = binding, nil, StatePending
		event := Event{ID: item.CleanupID + ":binding-known", Kind: "cleanup_binding_resolved", At: c.deps.Clock.Now()}
		_, err = c.deps.Store.ReleaseOwner(ctx, item, c.config.Owner, item.FencingVersion, event)
		return err
	}
	status, operationErr := c.cleanup(providerCtx, item)
	if operationErr != nil {
		return c.handleFailure(ctx, item, operationErr)
	}
	if status != LifecycleAbsent {
		return c.handleFailure(ctx, item, &Error{Category: CategoryUncertain, Operation: "absence"})
	}
	item.State, item.LastCategory = StateConfirmed, ""
	confirmedEvent := Event{ID: item.CleanupID + ":confirmed", Kind: "cleanup_confirmed", At: c.deps.Clock.Now()}
	item, err = c.deps.Store.SaveOwned(ctx, item, c.config.Owner, item.FencingVersion, confirmedEvent)
	if err != nil {
		return err
	}
	return c.releaseConfirmedCapacity(ctx, item)
}

// releaseConfirmedCapacity binds the exact absence proof to receipt authority before yielding controller ownership.
func (c *Controller) releaseConfirmedCapacity(ctx context.Context, item Item) error {
	confirmation := ReleaseConfirmationDigest(item.CleanupID, item.Binding.EnvironmentDigest, item.Binding.ResourceIdentityDigest, digestOf(struct{ Project, Acquisition string }{item.ProjectID, item.Binding.AcquisitionKey}))
	if _, _, err := c.deps.Ledger.ReleaseConfirmed(ctx, item.ProjectID, item.Binding.AcquisitionKey, item.Binding.CapacityReceiptDigest, item.CleanupID, confirmation); err != nil {
		return err
	}
	item.State = StateCapacityReleased
	releaseEvent := Event{ID: item.CleanupID + ":capacity_released", Kind: "capacity_released", At: c.deps.Clock.Now()}
	_, err := c.deps.Store.ReleaseOwner(ctx, item, c.config.Owner, item.FencingVersion, releaseEvent)
	if err == nil {
		c.observe(ctx, item.CleanupID)
	}
	return err
}

// bindingMatchesUncertain ensures reconciliation cannot switch project, run, provider, selection, acquisition or capacity authority.
func bindingMatchesUncertain(known LifecycleCleanupBinding, uncertain UncertainAcquisitionBinding) bool {
	return known.ProjectID == uncertain.ProjectID && known.RunID == uncertain.RunID && known.ProviderID == uncertain.ProviderID && known.ProviderVersion == uncertain.ProviderVersion && known.SelectionDigest == uncertain.SelectionDigest && known.AcquisitionKey == uncertain.AcquisitionKey && known.CapacityReceiptDigest == uncertain.CapacityReceiptDigest
}

// cleanup chooses only the provider-specific path authorized by the durable source.
func (c *Controller) cleanup(ctx context.Context, item Item) (LifecycleStatus, error) {
	if item.Source == "orphan" {
		inventory := c.deps.Inventories[item.Binding.ProviderID+"@"+item.Binding.ProviderVersion]
		if inventory == nil {
			return LifecycleUncertain, &Error{Category: CategoryUnauthorized, Operation: "inventory"}
		}
		return inventory.ReconcileCleanup(ctx, item)
	}
	if VerifyLifecycleCleanupBinding(item.Binding) != nil {
		return LifecycleUncertain, &Error{Category: CategoryIntegrity, Operation: "binding"}
	}
	status, err := c.deps.Lifecycle.Status(ctx, item.Binding)
	if err != nil {
		return LifecycleUncertain, err
	}
	if status == LifecycleAbsent {
		return status, nil
	}
	if err = c.deps.Lifecycle.Release(ctx, item.Binding); err != nil {
		return LifecycleUncertain, err
	}
	return c.deps.Lifecycle.Status(ctx, item.Binding)
}

// handleFailure persists retry or permanent quarantine before yielding ownership.
func (c *Controller) handleFailure(ctx context.Context, item Item, cause error) error {
	category := classify(ctx, cause)
	item.LastCategory = category
	if item.Attempts == math.MaxUint64 {
		category, item.LastCategory = CategoryIntegrity, CategoryIntegrity
	} else {
		item.Attempts++
	}
	retryable := category == CategoryTransient || category == CategoryRateLimited || category == CategoryTimeout || category == CategoryUncertain
	delay := backoff(item.Attempts, c.config.MaxBackoff)
	waitFits := delay <= c.config.MaxCumulativeWait && item.CumulativeWait <= c.config.MaxCumulativeWait-delay
	if retryable && item.Attempts <= uint64(c.config.MaxAttemptsPerItem) && waitFits {
		item.CumulativeWait += delay
		item.State, item.NextAttemptAt = StateRetryWait, c.deps.Clock.Now().UTC().Add(delay)
	} else {
		item.State = StateQuarantine
	}
	event := Event{ID: item.CleanupID + ":" + string(item.State) + ":" + itoa(item.Attempts), Kind: map[bool]string{true: "cleanup_retry_scheduled", false: "cleanup_quarantined"}[retryable && item.State == StateRetryWait], Category: category, At: c.deps.Clock.Now()}
	_, err := c.deps.Store.ReleaseOwner(ctx, item, c.config.Owner, item.FencingVersion, event)
	if err == nil {
		c.observe(ctx, item.CleanupID)
	}
	return err
}

// Requeue applies an explicit sealed operator decision without weakening cleanup authority.
func (c *Controller) Requeue(ctx context.Context, authorization RequeueAuthorization) (Item, error) {
	if VerifyRequeueAuthorization(authorization) != nil {
		return Item{}, &Error{Category: CategoryUnauthorized, Operation: "controller_requeue"}
	}
	return c.deps.Store.Requeue(ctx, authorization, c.deps.Clock.Now().UTC())
}

// eligible determines whether durable state may be claimed at the inclusive time boundary.
func eligible(item Item, now time.Time) bool {
	return EligibleForClaim(item, now)
}

// classify maps cancellation and typed errors into the closed scheduler taxonomy.
func classify(ctx context.Context, err error) Category {
	if errors.Is(ctx.Err(), context.Canceled) {
		return CategoryCancelled
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return CategoryTimeout
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return CategoryTimeout
	}
	if errors.Is(err, context.Canceled) {
		return CategoryCancelled
	}
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Category
	}
	return CategoryIntegrity
}

// backoff returns overflow-safe bounded exponential retry delay.
func backoff(attempt uint64, maximum time.Duration) time.Duration {
	delay := time.Second
	for count := uint64(1); count < attempt && delay < maximum/2; count++ {
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

// itoa formats audit counters without importing provider-controlled content.
func itoa(value uint64) string {
	if value == 0 {
		return "0"
	}
	buffer := [20]byte{}
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[index:])
}
