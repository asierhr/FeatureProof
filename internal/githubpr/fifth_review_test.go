package githubpr

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestClaimPendingValidatesAndCommitsOneAtomicBatch closes REV-19 for orphan and append-failure boundaries.
func TestClaimPendingValidatesAndCommitsOneAtomicBatch(t *testing.T) {
	t.Run("orphan", func(t *testing.T) {
		store := testStore(t)
		association, _ := NewAssociation("valid", "delivery", "default", 4, testSnapshot(t))
		association, _, _ = store.ReserveAssociation(context.Background(), association)
		payload := CheckPublication{ExternalID: "orphan-external"}
		payloadDigest, _ := CanonicalDigest(payload)
		if _, _, err := store.PrepareIntent(context.Background(), PublicationIntent{AssociationID: "missing", Kind: "check", PayloadSHA256: payloadDigest, ExpectedID: payload.ExternalID, Check: &payload}); err != nil {
			t.Fatal(err)
		}
		now := time.Unix(100, 0)
		if _, _, err := store.ClaimPending(context.Background(), "owner-a", now, time.Minute); !IsCode(err, CodeIntegrity) {
			t.Fatalf("orphan claim err=%v", err)
		}
		got, err := store.GetAssociation(context.Background(), association.ID)
		if err != nil || got.Owner != "" || !got.LeaseUntil.IsZero() {
			t.Fatalf("valid work partially claimed: %#v err=%v", got, err)
		}
		claimed, intents, err := store.ClaimPending(context.Background(), "owner-b", now, time.Minute)
		if err != nil || len(claimed) != 1 || claimed[0].Owner != "owner-b" || len(intents) != 0 {
			t.Fatalf("owner B could not reclaim immediately: %#v %#v err=%v", claimed, intents, err)
		}
	})

	t.Run("commit-failure", func(t *testing.T) {
		dir := t.TempDir()
		storeA, err := OpenLocalStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		association, _ := NewAssociation("valid", "delivery", "default", 4, testSnapshot(t))
		if _, _, err = storeA.ReserveAssociation(context.Background(), association); err != nil {
			t.Fatal(err)
		}
		storeA.beforeCommit = func(event StoreEvent) error {
			if event.Kind == "pending_claimed" {
				return errors.New("injected append failure")
			}
			return nil
		}
		now := time.Unix(100, 0)
		if _, _, err = storeA.ClaimPending(context.Background(), "owner-a", now, time.Minute); err == nil {
			t.Fatal("claim unexpectedly survived append failure")
		}
		storeB, err := OpenLocalStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		claimed, _, err := storeB.ClaimPending(context.Background(), "owner-b", now, time.Minute)
		if err != nil || len(claimed) != 1 || claimed[0].Owner != "owner-b" {
			t.Fatalf("owner B could not claim after failed append: %#v err=%v", claimed, err)
		}
	})
}

// TestCommentReconciliationComparesExactBody closes REV-20 in normal and crash-recovery publication paths.
func TestCommentReconciliationComparesExactBody(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		for _, stale := range []bool{false, true} {
			for _, persisted := range []bool{false, true} {
				name := map[bool]string{false: "normal", true: "recovery"}[recovery] + "-" + map[bool]string{false: "equal", true: "stale"}[stale] + "-" + map[bool]string{false: "discovered", true: "persisted"}[persisted]
				t.Run(name, func(t *testing.T) {
					store := testStore(t)
					association, _ := NewAssociation("association", "delivery", "default", 4, testSnapshot(t))
					association = bindTestAssociation(t, association, "run")
					if persisted {
						association.CommentID = 8
						association, _ = SealAssociation(association)
					}
					association, _, _ = store.ReserveAssociation(context.Background(), association)
					desired := CommentPublication{Body: SummaryMarker + "\nnew summary"}
					remoteBody := desired.Body
					if stale {
						remoteBody = SummaryMarker + "\nold summary"
					}
					gateway := &fakeGitHub{snapshot: association.Snapshot, comments: []CommentRecord{{ID: 8, AuthorID: 20, Body: remoteBody}}}
					publisher := testPublisher(gateway, store, 20)
					var err error
					if recovery {
						digest, _ := CanonicalDigest(desired)
						intent, _, prepareErr := store.PrepareIntent(context.Background(), PublicationIntent{AssociationID: association.ID, Kind: "comment", PayloadSHA256: digest, ExpectedID: "20", Comment: &desired, Owner: publisher.Owner, LeaseStartedAt: time.Unix(100, 0), LeaseUntil: time.Unix(100, 0).Add(2 * time.Minute)})
						if prepareErr != nil {
							t.Fatal(prepareErr)
						}
						association, err = publisher.RecoverIntent(context.Background(), association, intent)
					} else {
						association, err = publisher.PublishComment(context.Background(), association, Projection{Summary: "new summary"})
					}
					if err != nil || association.CommentID != 8 {
						t.Fatalf("association=%#v err=%v", association, err)
					}
					updates := 0
					for _, call := range gateway.calls {
						if call == "update_comment" {
							updates++
						}
					}
					wantUpdates := 0
					if stale {
						wantUpdates = 1
					}
					if updates != wantUpdates || len(gateway.commentPayloads) != wantUpdates {
						t.Fatalf("updates=%d payloads=%#v", updates, gateway.commentPayloads)
					}
					if stale && gateway.commentPayloads[0] != desired {
						t.Fatalf("updated payload=%#v", gateway.commentPayloads[0])
					}
				})
			}
		}
	}
}
