package githubpr

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLeaseProcessChild is the isolated executable boundary used to contend for a durable lease.
func TestLeaseProcessChild(t *testing.T) {
	directory := os.Getenv("FEATUREPROOF_LEASE_CHILD")
	if directory == "" {
		return
	}
	store, err := OpenLocalStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	_, won, err := store.ClaimAssociation(context.Background(), "a", os.Getenv("FEATUREPROOF_LEASE_OWNER"), time.Unix(30, 0), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if won {
		fmt.Print("won")
		return
	}
	fmt.Print("lost")
}

// TestDurableLeaseElectsExactlyOneOperatingSystemProcess proves the journal lock and CAS work across processes.
func TestDurableLeaseElectsExactlyOneOperatingSystemProcess(t *testing.T) {
	directory := t.TempDir()
	store, err := OpenLocalStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	association, _ := NewAssociation("a", "d", "default", 4, testSnapshot(t))
	association.Owner, association.LeaseUntil = "expired", time.Unix(20, 0)
	association, _ = SealAssociation(association)
	if _, _, err = store.ReserveAssociation(context.Background(), association); err != nil {
		t.Fatal(err)
	}
	results := make(chan string, 2)
	var wait sync.WaitGroup
	for _, owner := range []string{"process-one", "process-two"} {
		wait.Add(1)
		go func(owner string) {
			defer wait.Done()
			command := exec.Command(os.Args[0], "-test.run=^TestLeaseProcessChild$", "-test.v=false")
			command.Env = append(os.Environ(), "FEATUREPROOF_LEASE_CHILD="+directory, "FEATUREPROOF_LEASE_OWNER="+owner)
			output, runErr := command.CombinedOutput()
			if runErr != nil {
				results <- "error:" + runErr.Error() + ":" + string(output)
				return
			}
			results <- strings.TrimSpace(string(output))
		}(owner)
	}
	wait.Wait()
	close(results)
	winners := 0
	for result := range results {
		if strings.Contains(result, "error:") {
			t.Fatal(result)
		}
		if strings.Contains(result, "won") {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d", winners)
	}
}
