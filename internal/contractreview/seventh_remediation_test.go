package contractreview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// preparedRecoveryFixture persists the exact marker and synced temp that a post-crash caller must recover under ownership.
func preparedRecoveryFixture(t *testing.T) (*LocalPublisher, PublishRequest, []byte, string) {
	t.Helper()
	root := t.TempDir()
	publisher, err := NewLocalPublisher(root)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("prepared recovery")
	attemptID := strings.Repeat("7", 64)
	tempName := ".featureproof-contract-" + attemptID[:12] + "-prepared"
	if err = os.WriteFile(filepath.Join(root, tempName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	marker := publicationMarker{AttemptID: attemptID, Target: "contract.json", OutputDigest: digestBytes(raw), Temp: tempName}
	if err = publisher.writeCommitMarker(marker); err != nil {
		t.Fatal(err)
	}
	return publisher, PublishRequest{Target: marker.Target, AttemptID: attemptID}, raw, filepath.Join(root, tempName)
}

// assertCancelledRecoveryUntouched requires cancellation identity and proves recovery did not promote or remove prepared state.
func assertCancelledRecoveryUntouched(t *testing.T, publisher *LocalPublisher, request PublishRequest, tempPath string, err error) {
	t.Helper()
	if errorCategoryOf(err) != CategoryCancelled || !errors.Is(err, context.Canceled) {
		t.Fatalf("category=%s err=%v", errorCategoryOf(err), err)
	}
	if _, statErr := os.Stat(filepath.Join(publisher.root, request.Target)); !os.IsNotExist(statErr) {
		t.Fatalf("cancelled recovery created target: %v", statErr)
	}
	if _, statErr := os.Stat(tempPath); statErr != nil {
		t.Fatalf("cancelled recovery removed prepared temp: %v", statErr)
	}
}

// retryPreparedRecovery proves the same non-cancelled attempt consumes its prepared marker and temp after a cancelled owner.
func retryPreparedRecovery(t *testing.T, publisher *LocalPublisher, request PublishRequest, raw []byte) {
	t.Helper()
	publisher.checkpoint = nil
	target, err := publisher.Publish(context.Background(), request, raw, digestBytes(raw))
	if err != nil || target != request.Target {
		t.Fatalf("retry target=%q err=%v", target, err)
	}
	stored, err := os.ReadFile(filepath.Join(publisher.root, request.Target))
	if err != nil || string(stored) != string(raw) {
		t.Fatalf("stored=%q err=%v", stored, err)
	}
}

// TestPreparedRecoveryCleansLinkedTempAfterCommittedTarget covers the idempotent cleanup left by a crash after rename.
func TestPreparedRecoveryCleansLinkedTempAfterCommittedTarget(t *testing.T) {
	publisher, request, raw, tempPath := preparedRecoveryFixture(t)
	defer publisher.Close()
	targetPath := filepath.Join(publisher.root, request.Target)
	if err := os.Link(tempPath, targetPath); err != nil {
		t.Fatal(err)
	}
	marker, err := publisher.readCommitMarker(request.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := publisher.recoverPrepared(context.Background(), publisher.rootHandle, publisher.root, targetPath, request.Target, digestBytes(raw), marker)
	if err != nil || committed != request.Target {
		t.Fatalf("committed=%q err=%v", committed, err)
	}
	if _, err = os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("linked temp was not cleaned: %v", err)
	}
	stored, err := os.ReadFile(targetPath)
	if err != nil || string(stored) != string(raw) {
		t.Fatalf("stored=%q err=%v", stored, err)
	}
	if err = os.Link(targetPath, tempPath); err != nil {
		t.Fatal(err)
	}
	inspected, digest, err := publisher.Inspect(context.Background(), request.Target, request.AttemptID)
	if err != nil || inspected != request.Target || digest != digestBytes(raw) {
		t.Fatalf("inspected=%q digest=%q err=%v", inspected, digest, err)
	}
	if _, err = os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("inspect did not clean linked temp: %v", err)
	}
}

// TestPreparedRecoveryCancelsAfterOwnershipAcquired closes the exact REV-18 window exposed by the ownership checkpoint.
func TestPreparedRecoveryCancelsAfterOwnershipAcquired(t *testing.T) {
	publisher, request, raw, tempPath := preparedRecoveryFixture(t)
	defer publisher.Close()
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	publisher.checkpoint = func(phase string) {
		if phase == "ownership_acquired" {
			once.Do(cancel)
		}
	}
	_, err := publisher.Publish(ctx, request, raw, digestBytes(raw))
	assertCancelledRecoveryUntouched(t, publisher, request, tempPath, err)
	retryPreparedRecovery(t, publisher, request, raw)
}

// TestPreparedRecoveryOwnerReleaseRacesFollowerCancellation synchronizes unlock and cancel without assuming which event wins first.
func TestPreparedRecoveryOwnerReleaseRacesFollowerCancellation(t *testing.T) {
	publisher, request, raw, tempPath := preparedRecoveryFixture(t)
	defer publisher.Close()
	directory, err := openRelativeDirectory(publisher.rootHandle, ".featureproof-publication-markers", false)
	if err != nil {
		t.Fatal(err)
	}
	owner, acquired, err := tryAttemptLock(directory, request.AttemptID+".owner.lock")
	_ = directory.Close()
	if err != nil || !acquired {
		t.Fatalf("acquired=%v err=%v", acquired, err)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { _ = releaseAttemptLock(owner) }) }
	defer release()
	waiting := make(chan struct{})
	cancelled := make(chan struct{})
	var waitingOnce sync.Once
	publisher.checkpoint = func(phase string) {
		if phase == "ownership_wait" {
			waitingOnce.Do(func() { close(waiting) })
		}
		if phase == "ownership_acquired" {
			<-cancelled
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, publishErr := publisher.Publish(ctx, request, raw, digestBytes(raw))
		result <- publishErr
	}()
	select {
	case <-waiting:
	case <-time.After(3 * time.Second):
		t.Fatal("follower did not wait on live owner")
	}
	start := make(chan struct{})
	var actions sync.WaitGroup
	actions.Add(2)
	go func() {
		defer actions.Done()
		<-start
		release()
	}()
	go func() {
		defer actions.Done()
		<-start
		cancel()
		close(cancelled)
	}()
	close(start)
	actions.Wait()
	select {
	case publishErr := <-result:
		assertCancelledRecoveryUntouched(t, publisher, request, tempPath, publishErr)
	case <-time.After(3 * time.Second):
		t.Fatal("release/cancel follower did not terminate")
	}
	retryPreparedRecovery(t, publisher, request, raw)
}
