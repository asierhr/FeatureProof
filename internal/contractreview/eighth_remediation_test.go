package contractreview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// assertDurableCancellation requires a cancelled result while preserving the already committed target bytes.
func assertDurableCancellation(t *testing.T, target string, raw []byte, published string, err error) {
	t.Helper()
	if published != "" || errorCategoryOf(err) != CategoryCancelled || !errors.Is(err, context.Canceled) {
		t.Fatalf("published=%q category=%s err=%v", published, errorCategoryOf(err), err)
	}
	stored, readErr := os.ReadFile(target)
	if readErr != nil || string(stored) != string(raw) {
		t.Fatalf("stored=%q err=%v", stored, readErr)
	}
}

// retryDurableCancellation proves the same attempt reconciles its committed target without duplicating output.
func retryDurableCancellation(t *testing.T, publisher *LocalPublisher, request PublishRequest, raw []byte) {
	t.Helper()
	publisher.checkpoint = nil
	publisher.beforeCommit = nil
	published, err := publisher.Publish(context.Background(), request, raw, digestBytes(raw))
	if err != nil || published != request.Target {
		t.Fatalf("retry published=%q err=%v", published, err)
	}
	entries, err := os.ReadDir(publisher.root)
	if err != nil {
		t.Fatal(err)
	}
	targets := 0
	for _, entry := range entries {
		if entry.Name() == request.Target {
			targets++
		}
	}
	if targets != 1 {
		t.Fatalf("target copies=%d", targets)
	}
}

// TestPublishCancelsAfterDirectorySync makes the normal durable commit observable as cancelled and then retryable.
func TestPublishCancelsAfterDirectorySync(t *testing.T) {
	publisher, err := NewLocalPublisher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	raw := []byte("normal durable cancellation")
	request := PublishRequest{Target: "contract.json", AttemptID: strings.Repeat("8", 64)}
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	publisher.checkpoint = func(phase string) {
		if phase == "directory_synced" {
			once.Do(cancel)
		}
	}
	published, err := publisher.Publish(ctx, request, raw, digestBytes(raw))
	assertDurableCancellation(t, filepath.Join(publisher.root, request.Target), raw, published, err)
	retryDurableCancellation(t, publisher, request, raw)
}

// TestPreparedRecoveryCollisionCancelsAfterDirectorySync covers cancellation after collision cleanup has become durable.
func TestPreparedRecoveryCollisionCancelsAfterDirectorySync(t *testing.T) {
	publisher, request, raw, _ := preparedRecoveryFixture(t)
	defer publisher.Close()
	targetPath := filepath.Join(publisher.root, request.Target)
	publisher.beforeCommit = func() {
		if err := os.WriteFile(targetPath, raw, 0600); err != nil {
			t.Errorf("create colliding target: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	publisher.checkpoint = func(phase string) {
		if phase == "directory_synced" {
			once.Do(cancel)
		}
	}
	published, err := publisher.Publish(ctx, request, raw, digestBytes(raw))
	assertDurableCancellation(t, targetPath, raw, published, err)
	retryDurableCancellation(t, publisher, request, raw)
}
