package contractreview

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestLocalPublisherCrashMatrix proves every durable boundary converges after an abrupt process exit for create and replace.
func TestLocalPublisherCrashMatrix(t *testing.T) {
	if root := os.Getenv("FEATUREPROOF_PUBLISH_CRASH_ROOT"); root != "" {
		publisher, err := NewLocalPublisher(root)
		if err != nil {
			panic(err)
		}
		publisher.checkpoint = func(phase string) {
			if phase == os.Getenv("FEATUREPROOF_PUBLISH_CRASH_PHASE") {
				os.Exit(91)
			}
		}
		raw := []byte(os.Getenv("FEATUREPROOF_PUBLISH_CRASH_RAW"))
		request := PublishRequest{Target: "contract.json", AttemptID: strings.Repeat(os.Getenv("FEATUREPROOF_PUBLISH_CRASH_ATTEMPT"), 64)}
		if os.Getenv("FEATUREPROOF_PUBLISH_CRASH_REPLACE") == "1" {
			request.Replace = true
			request.ExpectedDigest = digestBytes([]byte("old"))
		}
		if _, err = publisher.Publish(context.Background(), request, raw, digestBytes(raw)); err != nil {
			panic(err)
		}
		panic("publisher did not reach requested crash phase")
	}
	phases := []string{"intent_synced", "temp_partial", "temp_synced", "marker_temp_synced", "marker_committed", "marker_synced", "target_committed", "directory_synced"}
	if runtime.GOOS == "darwin" {
		phases = append(phases, "target_linked")
	}
	for _, replace := range []bool{false, true} {
		for index, phase := range phases {
			if replace && phase == "target_linked" {
				continue
			}
			root := t.TempDir()
			if replace {
				if err := os.WriteFile(filepath.Join(root, "contract.json"), []byte("old"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			attemptRune := string("abcdef0123456789"[index])
			command := exec.Command(os.Args[0], "-test.run=^TestLocalPublisherCrashMatrix$")
			command.Env = append(os.Environ(),
				"FEATUREPROOF_PUBLISH_CRASH_ROOT="+root,
				"FEATUREPROOF_PUBLISH_CRASH_PHASE="+phase,
				"FEATUREPROOF_PUBLISH_CRASH_RAW=new",
				"FEATUREPROOF_PUBLISH_CRASH_ATTEMPT="+attemptRune,
				"FEATUREPROOF_PUBLISH_CRASH_REPLACE="+map[bool]string{false: "0", true: "1"}[replace],
			)
			output, runErr := command.CombinedOutput()
			if runErr == nil || command.ProcessState.ExitCode() != 91 {
				t.Fatalf("phase=%s replace=%v did not crash at boundary: %v output=%s", phase, replace, runErr, output)
			}
			publisher, err := NewLocalPublisher(root)
			if err != nil {
				t.Fatal(err)
			}
			request := PublishRequest{Target: "contract.json", AttemptID: strings.Repeat(attemptRune, 64), Replace: replace}
			if replace {
				request.ExpectedDigest = digestBytes([]byte("old"))
			}
			if _, err = publisher.Publish(context.Background(), request, []byte("new"), digestBytes([]byte("new"))); err != nil {
				t.Fatalf("phase=%s replace=%v did not reconcile: %v", phase, replace, err)
			}
			stored, err := os.ReadFile(filepath.Join(root, "contract.json"))
			if err != nil || !bytes.Equal(stored, []byte("new")) {
				t.Fatalf("phase=%s replace=%v wrong target: %q %v", phase, replace, stored, err)
			}
			matches, _ := filepath.Glob(filepath.Join(root, ".featureproof-contract-"+request.AttemptID[:12]+"-*"))
			if len(matches) != 0 {
				t.Fatalf("phase=%s replace=%v leaked temporary files: %v", phase, replace, matches)
			}
			intents, _ := filepath.Glob(filepath.Join(root, ".featureproof-publication-markers", request.AttemptID+".intent.json"))
			if len(intents) != 0 {
				t.Fatalf("phase=%s replace=%v leaked durable intent: %v", phase, replace, intents)
			}
			if _, _, err = publisher.Inspect(context.Background(), "contract.json", request.AttemptID); err != nil {
				t.Fatalf("phase=%s replace=%v receipt inspection failed: %v", phase, replace, err)
			}
			_ = publisher.Close()
		}
	}
}

// TestLocalPublisherRecoversAfterTempSyncFailure proves a durable intent preserves retry authority when sync fails before a prepared marker.
func TestLocalPublisherRecoversAfterTempSyncFailure(t *testing.T) {
	root := t.TempDir()
	publisher, err := NewLocalPublisher(root)
	if err != nil {
		t.Fatal(err)
	}
	request := PublishRequest{Target: "contract.json", AttemptID: strings.Repeat("f", 64)}
	publisher.syncFile = func(*os.File) error { return errors.New("injected sync failure") }
	if _, err = publisher.Publish(context.Background(), request, []byte("approved"), digestBytes([]byte("approved"))); errorCategoryOf(err) != CategoryUnavailable {
		t.Fatalf("sync failure returned %v", err)
	}
	publisher.syncFile = nil
	if _, err = publisher.Publish(context.Background(), request, []byte("approved"), digestBytes([]byte("approved"))); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(root, "contract.json"))
	if err != nil || !bytes.Equal(stored, []byte("approved")) {
		t.Fatalf("retry bytes=%q err=%v", stored, err)
	}
	_ = publisher.Close()
}
