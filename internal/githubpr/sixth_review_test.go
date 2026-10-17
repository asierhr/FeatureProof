package githubpr

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestClaimPendingDrainsBacklogInReplayableBoundedBatches closes REV-21 above one mebibyte of pending payloads.
func TestClaimPendingDrainsBacklogInReplayableBoundedBatches(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenLocalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	const total = 20
	associationIDs := make([]string, 0, total)
	for index := 0; index < total; index++ {
		id := fmt.Sprintf("association-%02d", index)
		delivery := fmt.Sprintf("delivery-%02d", index)
		run := fmt.Sprintf("run-%02d", index)
		association, _ := NewAssociation(id, delivery, "default", 4, testSnapshot(t))
		association = bindTestAssociation(t, association, run)
		association.Lifecycle = "running"
		association, _ = SealAssociation(association)
		association, _, err = store.ReserveAssociation(context.Background(), association)
		if err != nil {
			t.Fatal(err)
		}
		payload := CheckPublication{Name: "FeatureProof validation", HeadOID: association.Snapshot.HeadOID, ExternalID: association.ExternalCheckID, Status: "in_progress", Title: "progress", Summary: strings.Repeat("x", 60_000)}
		payloadDigest, _ := CanonicalDigest(payload)
		if _, _, err = store.PrepareIntent(context.Background(), PublicationIntent{AssociationID: id, Kind: "check", PayloadSHA256: payloadDigest, ExpectedID: association.ExternalCheckID, Check: &payload}); err != nil {
			t.Fatal(err)
		}
		associationIDs = append(associationIDs, id)
	}

	now := time.Unix(100, 0)
	seenAssociations := map[string]bool{}
	seenIntents := map[string]bool{}
	batches := 0
	for {
		store, err = OpenLocalStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		associations, intents, claimErr := store.ClaimPending(context.Background(), "owner-a", now, 2*time.Minute)
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		if len(associations) == 0 && len(intents) == 0 {
			break
		}
		batches++
		for _, association := range associations {
			if seenAssociations[association.ID] {
				t.Fatalf("association %s claimed twice", association.ID)
			}
			seenAssociations[association.ID] = true
		}
		for _, intent := range intents {
			key := intent.AssociationID + ":" + intent.Kind
			if seenIntents[key] {
				t.Fatalf("intent %s claimed twice", key)
			}
			seenIntents[key] = true
		}
	}
	if batches < 2 || len(seenAssociations) != total || len(seenIntents) != total {
		t.Fatalf("batches=%d associations=%d intents=%d", batches, len(seenAssociations), len(seenIntents))
	}
	raw, err := os.ReadFile(store.journal)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(line) >= maxJournalRecordBytes {
			t.Fatalf("journal line exceeds replay bound: %d", len(line))
		}
	}

	store, err = OpenLocalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range associationIDs {
		if err = store.ConfirmIntent(context.Background(), id, "check", "confirmed"); err != nil {
			t.Fatal(err)
		}
		renewed, renewErr := store.RenewAssociation(context.Background(), id, "owner-a", now, 2*time.Minute)
		if renewErr != nil {
			t.Fatal(renewErr)
		}
		prior := renewed.VersionCAS
		renewed.Lifecycle = "terminal"
		renewed, err = store.UpdateAssociation(context.Background(), renewed, prior)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.ReleaseAssociation(context.Background(), renewed.ID, "owner-a", now); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := OpenLocalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	remainingAssociations, remainingIntents, err := reopened.ClaimPending(context.Background(), "owner-b", now, time.Minute)
	if err != nil || len(remainingAssociations) != 0 || len(remainingIntents) != 0 {
		t.Fatalf("remaining associations=%d intents=%d err=%v", len(remainingAssociations), len(remainingIntents), err)
	}
}
