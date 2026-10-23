package localvalidation

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/asierhr/featureProof/internal/fixture"
)

// TestFixtureAuditIsSealedAndOfflineVerified proves allowlisted lifecycle audit is durable and tampering fails closed.
func TestFixtureAuditIsSealedAndOfflineVerified(t *testing.T) {
	dir := fixturePackage(t)
	summary := validFixturePackageSummary()
	signer, trust := fixtureAuditAuthority(t)
	if err := AttachFixtureAudit(context.Background(), dir, summary, signer); err != nil {
		t.Fatal(err)
	}
	index, err := ReadPackageWithFixtureTrust(context.Background(), dir, trust)
	if err != nil || index.Fixtures == nil || len(index.Fixtures.Cleanup.Audit) != 5 {
		t.Fatalf("index=%#v err=%v", index, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixtures.json"), []byte(`{"preparation_status":"prepared","cleanup":{"status":"succeeded"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPackageWithFixtureTrust(context.Background(), dir, trust); err == nil {
		t.Fatal("tampered fixture audit accepted")
	}
	resigned := fixturePackage(t)
	if err := AttachFixtureAudit(context.Background(), resigned, summary, signer); err != nil {
		t.Fatal(err)
	}
	forged := summary
	forged.Cleanup.Audit = nil
	if err := writeFixturePackageJSON(filepath.Join(resigned, "fixtures.json"), forged); err != nil {
		t.Fatal(err)
	}
	packageRaw, _ := os.ReadFile(filepath.Join(resigned, "package.json"))
	forgedIndex, _ := decodePackageIndex(packageRaw)
	forgedIndex.Fixtures = &forged
	_ = writeFixturePackageJSON(filepath.Join(resigned, "package.json"), forgedIndex)
	reportRaw, _ := os.ReadFile(filepath.Join(resigned, "report.json"))
	var report reportArtifact
	_ = decodePackageJSON(reportRaw, "report.json", &report)
	report.Fixtures = &forged
	_ = writeFixturePackageJSON(filepath.Join(resigned, "report.json"), report)
	artifacts, _ := inventoryPackage(context.Background(), resigned)
	_ = writeFixturePackageJSON(filepath.Join(resigned, "manifest.json"), PackageManifest{Version: PackageVersion, RunID: forgedIndex.RunID, BindingSHA256: forgedIndex.Binding.BindingSHA256, Artifacts: artifacts})
	if _, err := ReadPackageWithFixtureTrust(context.Background(), resigned, trust); err == nil {
		t.Fatal("semantically forged and rehashed fixture audit accepted")
	}
	mutations := []func(*fixture.FixtureSummary){
		func(value *fixture.FixtureSummary) { value.Cleanup.Status = fixture.CleanupFailed },
		func(value *fixture.FixtureSummary) { value.Fixtures[0].FixtureID = "other" },
		func(value *fixture.FixtureSummary) { value.Fixtures[0].CreateStatus = fixture.CreateFailed },
		func(value *fixture.FixtureSummary) { value.Fixtures[0].ConfirmedCaptureNames[0] = "other" },
		func(value *fixture.FixtureSummary) { value.Cleanup.Audit[4].BindingDigest = "other-cleanup" },
		func(value *fixture.FixtureSummary) {
			value.Cleanup.Audit[1].Attempt = 99
			value.Cleanup.Audit[1].Category = fixture.CategoryUnavailable
			value.Cleanup.Audit[1].EvidenceID = "EXTERNAL-CANARY"
		},
	}
	for index, mutate := range mutations {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			dir := fixturePackage(t)
			if err := AttachFixtureAudit(context.Background(), dir, summary, signer); err != nil {
				t.Fatal(err)
			}
			candidate := cloneFixturePackageSummary(t, summary)
			mutate(&candidate)
			writerDir := fixturePackage(t)
			if err := AttachFixtureAudit(context.Background(), writerDir, candidate, signer); err == nil {
				t.Fatalf("writer accepted semantic mutation %d", index)
			}
			forceResealFixturePackage(t, dir, candidate)
			if _, err := ReadPackageWithFixtureTrust(context.Background(), dir, trust); err == nil {
				t.Fatalf("rehashed semantic mutation %d accepted", index)
			}
		})
	}
	authorityMutations := []func(*fixture.FixtureSummary){
		func(value *fixture.FixtureSummary) { value.FixtureSetDigest = strings.Repeat("1", 64) },
		func(value *fixture.FixtureSummary) { value.Cleanup.Audit[1].EvidenceID = strings.Repeat("2", 64) },
		func(value *fixture.FixtureSummary) { value.Cleanup.Audit[2].Capture.Digest = strings.Repeat("3", 64) },
		func(value *fixture.FixtureSummary) {
			value.Cleanup.Audit[3].BindingDigest = strings.Repeat("4", 64)
			value.Cleanup.Audit[4].BindingDigest = strings.Repeat("4", 64)
		},
	}
	for index, mutate := range authorityMutations {
		t.Run("authority_"+strconv.Itoa(index), func(t *testing.T) {
			dir := fixturePackage(t)
			if err := AttachFixtureAudit(context.Background(), dir, summary, signer); err != nil {
				t.Fatal(err)
			}
			candidate := cloneFixturePackageSummary(t, summary)
			mutate(&candidate)
			forceResealFixturePackage(t, dir, candidate)
			if _, err := ReadPackageWithFixtureTrust(context.Background(), dir, trust); err == nil {
				t.Fatalf("authority accepted re-signed valid SHA-256 mutation %d", index)
			}
		})
	}
	markdownDir := fixturePackage(t)
	if err := AttachFixtureAudit(context.Background(), markdownDir, summary, signer); err != nil {
		t.Fatal(err)
	}
	markdownPath := filepath.Join(markdownDir, "report.md")
	markdownRaw, _ := os.ReadFile(markdownPath)
	contradiction := "\n## Fixture lifecycle\n\n- Run: `run`\n- Binding: `EXTERNAL-CANARY`\n- Preparation: `failed`\n- Cleanup: `failed`\n"
	if err := writeFixturePackageBytes(markdownPath, append(markdownRaw, contradiction...)); err != nil {
		t.Fatal(err)
	}
	markdownPackageRaw, _ := os.ReadFile(filepath.Join(markdownDir, "package.json"))
	indexWithMarkdown, _ := decodePackageIndex(markdownPackageRaw)
	markdownArtifacts, _ := inventoryPackage(context.Background(), markdownDir)
	if err := writeFixturePackageJSON(filepath.Join(markdownDir, "manifest.json"), PackageManifest{Version: PackageVersion, RunID: indexWithMarkdown.RunID, BindingSHA256: indexWithMarkdown.Binding.BindingSHA256, Artifacts: markdownArtifacts}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPackageWithFixtureTrust(context.Background(), markdownDir, trust); err == nil {
		t.Fatal("duplicate contradictory fixture markdown accepted")
	}
}

// TestFixtureAuditAuthorityStoreFailsClosed exercises restart pinning, corruption, links, permissions, short writes, and concurrent bootstrap.
func TestFixtureAuditAuthorityStoreFailsClosed(t *testing.T) {
	t.Run("configuration_and_orphans", func(t *testing.T) {
		root := t.TempDir()
		if _, _, err := OpenFixtureAuditAuthority(filepath.Join(root, "key"), filepath.Join(root, "a"), filepath.Join(root, "b")); err == nil {
			t.Fatal("multiple trusted pins accepted")
		}
		privateOnly := filepath.Join(root, "private-only")
		if err := os.WriteFile(privateOnly, make([]byte, ed25519.PrivateKeySize), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := OpenFixtureAuditAuthority(privateOnly); err == nil {
			t.Fatal("private key without pin accepted")
		}
		pinOnly := filepath.Join(root, "pin-only")
		if err := os.WriteFile(pinOnly+".public-pin", []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := OpenFixtureAuditAuthority(pinOnly); err == nil {
			t.Fatal("pin without private key accepted")
		}
		blockedParent := filepath.Join(root, "blocked")
		if err := os.WriteFile(blockedParent, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := OpenFixtureAuditAuthority(filepath.Join(blockedParent, "key")); err == nil {
			t.Fatal("invalid private parent accepted")
		}
		privatePath := filepath.Join(root, "valid", "key")
		if _, _, err := OpenFixtureAuditAuthority(privatePath, filepath.Join(blockedParent, "pin")); err == nil {
			t.Fatal("invalid pin parent accepted")
		}
	})
	t.Run("write_boundaries", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "existing")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeFixtureAuthorityFile(path, []byte("secret"), writeAll); err == nil {
			t.Fatal("exclusive write replaced authority")
		}
		zeroWriter := writerFunc(func([]byte) (int, error) { return 0, nil })
		if _, err := writeAll(zeroWriter, []byte("x")); !errors.Is(err, io.ErrShortWrite) {
			t.Fatal("zero write accepted")
		}
		errorWriter := writerFunc(func([]byte) (int, error) { return 0, errors.New("injected") })
		if _, err := writeAll(errorWriter, []byte("x")); err == nil {
			t.Fatal("writer error ignored")
		}
		if _, err := readFixtureAuthorityFile(filepath.Join(t.TempDir(), "missing"), 64); err == nil {
			t.Fatal("missing authority read accepted")
		}
		if _, err := acquireFixtureAuthorityLock(filepath.Join(t.TempDir(), "missing", "lock")); err == nil {
			t.Fatal("lock in missing trusted directory accepted")
		}
	})
	t.Run("valid_keypair_replacement", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "authority", "key")
		_, _, err := OpenFixtureAuditAuthority(path)
		if err != nil {
			t.Fatal(err)
		}
		_, replacement, _ := ed25519.GenerateKey(rand.Reader)
		if err := os.WriteFile(path, replacement, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := OpenFixtureAuditAuthority(path); err == nil {
			t.Fatal("valid replacement bypassed independent pin")
		}
	})
	t.Run("same_length_corruption", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "authority", "key")
		_, _, _ = OpenFixtureAuditAuthority(path)
		raw, _ := os.ReadFile(path)
		raw[0] ^= 0xff
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := OpenFixtureAuditAuthority(path); err == nil {
			t.Fatal("same-length corruption accepted")
		}
	})
	t.Run("corrupt_independent_pin", func(t *testing.T) {
		root := t.TempDir()
		path, pin := filepath.Join(root, "private", "key"), filepath.Join(root, "trust", "pin")
		_, _, err := OpenFixtureAuditAuthority(path, pin)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pin, []byte("{invalid"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := OpenFixtureAuditAuthority(path, pin); err == nil {
			t.Fatal("corrupt trusted pin accepted")
		}
	})
	t.Run("valid_but_mismatched_pin", func(t *testing.T) {
		root := t.TempDir()
		path, pin := filepath.Join(root, "private", "key"), filepath.Join(root, "trust", "pin")
		_, _, err := OpenFixtureAuditAuthority(path, pin)
		if err != nil {
			t.Fatal(err)
		}
		foreignPublic, _, _ := ed25519.GenerateKey(rand.Reader)
		digest := sha256.Sum256(foreignPublic)
		raw, _ := json.Marshal(fixtureAuditPin{KeyID: hex.EncodeToString(digest[:]), PublicKey: hex.EncodeToString(foreignPublic)})
		if err := os.WriteFile(pin, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := OpenFixtureAuditAuthority(path, pin); err == nil {
			t.Fatal("mismatched valid pin accepted")
		}
	})
	t.Run("malformed_public_key_pin", func(t *testing.T) {
		root := t.TempDir()
		path, pin := filepath.Join(root, "private", "key"), filepath.Join(root, "trust", "pin")
		_, _, err := OpenFixtureAuditAuthority(path, pin)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(fixtureAuditPin{KeyID: strings.Repeat("0", 64), PublicKey: "zz"})
		if err := os.WriteFile(pin, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := OpenFixtureAuditAuthority(path, pin); err == nil {
			t.Fatal("malformed pinned public key accepted")
		}
	})
	t.Run("unsafe_files", func(t *testing.T) {
		permissionRoot := t.TempDir()
		permissionKey, permissionPin := filepath.Join(permissionRoot, "private", "key"), filepath.Join(permissionRoot, "trust", "pin")
		_, _, _ = OpenFixtureAuditAuthority(permissionKey, permissionPin)
		if err := os.Chmod(permissionKey, 0o644); err == nil && runtime.GOOS != "windows" {
			if _, _, err := OpenFixtureAuditAuthority(permissionKey, permissionPin); err == nil {
				t.Fatal("permissive key accepted")
			}
		}
		pinPermissionRoot := t.TempDir()
		pinPermissionKey, pinPermissionPin := filepath.Join(pinPermissionRoot, "private", "key"), filepath.Join(pinPermissionRoot, "trust", "pin")
		_, _, _ = OpenFixtureAuditAuthority(pinPermissionKey, pinPermissionPin)
		if err := os.Chmod(pinPermissionPin, 0o644); err == nil && runtime.GOOS != "windows" {
			if _, _, err := OpenFixtureAuditAuthority(pinPermissionKey, pinPermissionPin); err == nil {
				t.Fatal("permissive pin accepted")
			}
		}
		symlinkRoot := t.TempDir()
		symlinkKey, symlinkPin := filepath.Join(symlinkRoot, "private", "key"), filepath.Join(symlinkRoot, "trust", "pin")
		_, _, _ = OpenFixtureAuditAuthority(symlinkKey, symlinkPin)
		keyLink := filepath.Join(symlinkRoot, "key-link")
		if err := os.Symlink(symlinkKey, keyLink); err == nil {
			if _, _, err := OpenFixtureAuditAuthority(keyLink, symlinkPin); err == nil {
				t.Fatal("symlink key accepted")
			}
		}
		pinLink := filepath.Join(symlinkRoot, "pin-link")
		if err := os.Symlink(symlinkPin, pinLink); err == nil {
			if _, _, err := OpenFixtureAuditAuthority(symlinkKey, pinLink); err == nil {
				t.Fatal("symlink pin accepted")
			}
		}
		hardRoot := t.TempDir()
		hardKey, hardPin := filepath.Join(hardRoot, "private", "key"), filepath.Join(hardRoot, "trust", "pin")
		_, _, _ = OpenFixtureAuditAuthority(hardKey, hardPin)
		if err := os.Link(hardKey, filepath.Join(hardRoot, "key-hardlink")); err == nil {
			if _, _, err := OpenFixtureAuditAuthority(hardKey, hardPin); err == nil {
				t.Fatal("hardlinked key accepted")
			}
		}
		pinHardRoot := t.TempDir()
		pinHardKey, pinHardPin := filepath.Join(pinHardRoot, "private", "key"), filepath.Join(pinHardRoot, "trust", "pin")
		_, _, _ = OpenFixtureAuditAuthority(pinHardKey, pinHardPin)
		if err := os.Link(pinHardPin, filepath.Join(pinHardRoot, "pin-hardlink")); err == nil {
			if _, _, err := OpenFixtureAuditAuthority(pinHardKey, pinHardPin); err == nil {
				t.Fatal("hardlinked pin accepted")
			}
		}
	})
	t.Run("partial_write_cleanup", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "partial")
		partial := func(writer io.Writer, raw []byte) (int, error) {
			n, _ := writer.Write(raw[:1])
			return n, errors.New("injected")
		}
		if err := writeFixtureAuthorityFile(path, []byte("secret"), partial); err == nil {
			t.Fatal("partial write accepted")
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("partial authority remained")
		}
	})
	t.Run("bootstrap_race", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "authority", "key")
		var wait sync.WaitGroup
		errorsFound := make(chan error, 8)
		ids := make(chan string, 8)
		for index := 0; index < 8; index++ {
			wait.Add(1)
			go func() {
				defer wait.Done()
				_, trust, err := OpenFixtureAuditAuthority(path)
				errorsFound <- err
				ids <- trust.KeyID
			}()
		}
		wait.Wait()
		close(errorsFound)
		close(ids)
		for err := range errorsFound {
			if err != nil {
				t.Fatal(err)
			}
		}
		first := ""
		for id := range ids {
			if first == "" {
				first = id
			}
			if id != first {
				t.Fatal("race rotated authority")
			}
		}
	})
}

// writerFunc adapts an injected write behavior for short-write boundary tests.
type writerFunc func([]byte) (int, error)

// Write invokes the injected authority write behavior.
func (write writerFunc) Write(raw []byte) (int, error) { return write(raw) }

// TestAttachFixtureAuditFailsClosedAtArtifactBoundaries verifies cancellation, missing/malformed inputs, replacement, and serialization errors.
func TestAttachFixtureAuditFailsClosedAtArtifactBoundaries(t *testing.T) {
	summary := validFixturePackageSummary()
	signer, _ := fixtureAuditAuthority(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := AttachFixtureAudit(cancelled, fixturePackage(t), summary, signer); err == nil {
		t.Fatal("cancelled audit attachment accepted")
	}
	if err := AttachFixtureAudit(context.Background(), t.TempDir(), summary, signer); err == nil {
		t.Fatal("missing package index accepted")
	}
	invalidIndex := fixturePackage(t)
	if err := os.WriteFile(filepath.Join(invalidIndex, "package.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AttachFixtureAudit(context.Background(), invalidIndex, summary, signer); err == nil {
		t.Fatal("malformed package index accepted")
	}
	missingReport := fixturePackage(t)
	if err := os.Remove(filepath.Join(missingReport, "report.json")); err != nil {
		t.Fatal(err)
	}
	if err := AttachFixtureAudit(context.Background(), missingReport, summary, signer); err == nil {
		t.Fatal("missing report accepted")
	}
	missingMarkdown := fixturePackage(t)
	if err := os.Remove(filepath.Join(missingMarkdown, "report.md")); err != nil {
		t.Fatal(err)
	}
	if err := AttachFixtureAudit(context.Background(), missingMarkdown, summary, signer); err == nil {
		t.Fatal("missing markdown accepted")
	}
	replaced := fixturePackage(t)
	if err := AttachFixtureAudit(context.Background(), replaced, summary, signer); err != nil {
		t.Fatal(err)
	}
	if err := AttachFixtureAudit(context.Background(), replaced, summary, signer); err != nil {
		t.Fatalf("canonical fixture section replacement failed: %v", err)
	}
	if err := writeFixturePackageJSON(filepath.Join(t.TempDir(), "invalid.json"), make(chan int)); err == nil {
		t.Fatal("unserializable package JSON accepted")
	}
	if err := writeFixturePackageBytes(filepath.Join(t.TempDir(), "missing", "report.md"), []byte("report")); err == nil {
		t.Fatal("missing artifact directory accepted")
	}
	invalidAuthority := fixturePackage(t)
	if err := AttachFixtureAudit(context.Background(), invalidAuthority, summary, FixtureAuditSigner{KeyID: signer.KeyID}); err == nil {
		t.Fatal("invalid consumer-owned audit authority accepted")
	}
	mismatchedSigner := signer
	mismatchedSigner.KeyID = strings.Repeat("0", 64)
	if err := AttachFixtureAudit(context.Background(), fixturePackage(t), summary, mismatchedSigner); err == nil {
		t.Fatal("signer with mismatched key fingerprint accepted")
	}
}

// TestFixtureAuditAuthoritySeparatesWriterAndReaderCapabilities rejects absent, substituted, rotated, and forged trust material.
func TestFixtureAuditAuthoritySeparatesWriterAndReaderCapabilities(t *testing.T) {
	authorityPath := filepath.Join(t.TempDir(), "persistent", "fixture.ed25519")
	_, firstTrust, err := OpenFixtureAuditAuthority(authorityPath)
	if err != nil {
		t.Fatal(err)
	}
	firstDigest := sha256.Sum256(firstTrust.PublicKey)
	if firstTrust.KeyID != hex.EncodeToString(firstDigest[:]) {
		t.Fatal("public trust key ID is not its SHA-256 fingerprint")
	}
	_, secondTrust, err := OpenFixtureAuditAuthority(authorityPath)
	if err != nil || firstTrust.KeyID != secondTrust.KeyID {
		t.Fatalf("persistent authority rotated unexpectedly: %v", err)
	}
	invalidPath := filepath.Join(t.TempDir(), "invalid.ed25519")
	if err := os.WriteFile(invalidPath, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenFixtureAuditAuthority(invalidPath); err == nil {
		t.Fatal("malformed persisted private authority accepted")
	}
	dir := fixturePackage(t)
	summary := validFixturePackageSummary()
	signer, trust := fixtureAuditAuthority(t)
	if err := AttachFixtureAudit(context.Background(), dir, summary, signer); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPackage(context.Background(), dir); err == nil {
		t.Fatal("fixture package accepted without external trust anchor")
	}
	_, foreignTrust := fixtureAuditAuthority(t)
	if _, err := ReadPackageWithFixtureTrust(context.Background(), dir, foreignTrust); err == nil {
		t.Fatal("unauthorized trust rotation accepted")
	}
	keyIDDir := fixturePackage(t)
	if err := AttachFixtureAudit(context.Background(), keyIDDir, summary, signer); err != nil {
		t.Fatal(err)
	}
	keyIDSealPath := filepath.Join(keyIDDir, "fixture-authority.json")
	keyIDSealRaw, _ := os.ReadFile(keyIDSealPath)
	var keyIDSeal fixtureAuditSeal
	_ = decodePackageJSON(keyIDSealRaw, "fixture-authority.json", &keyIDSeal)
	keyIDSeal.KeyID = foreignTrust.KeyID
	if err := writeFixturePackageJSON(keyIDSealPath, keyIDSeal); err != nil {
		t.Fatal(err)
	}
	keyIDArtifacts, _ := inventoryPackage(context.Background(), keyIDDir)
	keyIDPackageRaw, _ := os.ReadFile(filepath.Join(keyIDDir, "package.json"))
	keyIDIndex, _ := decodePackageIndex(keyIDPackageRaw)
	_ = writeFixturePackageJSON(filepath.Join(keyIDDir, "manifest.json"), PackageManifest{Version: PackageVersion, RunID: keyIDIndex.RunID, BindingSHA256: keyIDIndex.Binding.BindingSHA256, Artifacts: keyIDArtifacts})
	if _, err := ReadPackageWithFixtureTrust(context.Background(), keyIDDir, foreignTrust); err == nil {
		t.Fatal("concordant key ID and public trust substitution accepted without private signature")
	}
	if err := os.WriteFile(dir+".fixture-audit.key", []byte("attacker-sidecar"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPackageWithFixtureTrust(context.Background(), dir, trust); err != nil {
		t.Fatalf("reader consulted attacker private sidecar: %v", err)
	}
	sealPath := filepath.Join(dir, "fixture-authority.json")
	sealRaw, _ := os.ReadFile(sealPath)
	var seal fixtureAuditSeal
	if err := decodePackageJSON(sealRaw, "fixture-authority.json", &seal); err != nil {
		t.Fatal(err)
	}
	seal.Signature = strings.Repeat("0", 128)
	if err := writeFixturePackageJSON(sealPath, seal); err != nil {
		t.Fatal(err)
	}
	artifacts, _ := inventoryPackage(context.Background(), dir)
	packageRaw, _ := os.ReadFile(filepath.Join(dir, "package.json"))
	index, _ := decodePackageIndex(packageRaw)
	if err := writeFixturePackageJSON(filepath.Join(dir, "manifest.json"), PackageManifest{Version: PackageVersion, RunID: index.RunID, BindingSHA256: index.Binding.BindingSHA256, Artifacts: artifacts}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPackageWithFixtureTrust(context.Background(), dir, trust); err == nil {
		t.Fatal("forged signature accepted")
	}
}

// fixtureAuditAuthority creates one protected writer capability and detached reader trust anchor for a test.
func fixtureAuditAuthority(t *testing.T) (FixtureAuditSigner, FixtureAuditTrust) {
	t.Helper()
	signer, trust, err := OpenFixtureAuditAuthority(filepath.Join(t.TempDir(), "authority", "fixture.ed25519"))
	if err != nil {
		t.Fatal(err)
	}
	return signer, trust
}

// validFixturePackageSummary returns one fully related result, capture, retry, terminal, and aggregate fixture audit.
func validFixturePackageSummary() fixture.FixtureSummary {
	digest := strings.Repeat("a", 64)
	createDigest, cleanupDigest := strings.Repeat("b", 64), strings.Repeat("c", 64)
	return fixture.FixtureSummary{RunID: "run", FixtureSetDigest: strings.Repeat("d", 64), PreparationStatus: fixture.PreparationPrepared, Fixtures: []fixture.FixtureResult{{FixtureID: "account", CreateStatus: fixture.CreatePrepared, ConfirmedCaptureNames: []string{"id"}}}, Cleanup: fixture.CleanupReport{Status: fixture.CleanupSucceeded, Audit: []fixture.AuditRecord{{FixtureID: "account", Phase: "intent", BindingDigest: createDigest}, {FixtureID: "account", Phase: "create", Attempt: 1, BindingDigest: createDigest, Terminal: true}, {FixtureID: "account", Phase: "capture", BindingDigest: createDigest, Capture: &fixture.CaptureMetadata{Name: "id", ValueType: "string", Bytes: 3, Digest: digest}}, {FixtureID: "account", Phase: "cleanup", Attempt: 1, Category: fixture.CategoryUnavailable, BindingDigest: cleanupDigest}, {FixtureID: "account", Phase: "cleanup", Attempt: 2, Category: fixture.CategorySuccess, BindingDigest: cleanupDigest, Terminal: true}}}}
}

// cloneFixturePackageSummary deep-copies the public audit so table mutations remain isolated.
func cloneFixturePackageSummary(t *testing.T, value fixture.FixtureSummary) fixture.FixtureSummary {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var clone fixture.FixtureSummary
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

// forceResealFixturePackage rewrites every public copy and inventory hash to exercise semantic verification beyond byte integrity.
func forceResealFixturePackage(t *testing.T, dir string, summary fixture.FixtureSummary) {
	t.Helper()
	packageRaw, _ := os.ReadFile(filepath.Join(dir, "package.json"))
	index, _ := decodePackageIndex(packageRaw)
	index.Fixtures = &summary
	if err := writeFixturePackageJSON(filepath.Join(dir, "package.json"), index); err != nil {
		t.Fatal(err)
	}
	if err := writeFixturePackageJSON(filepath.Join(dir, "fixtures.json"), summary); err != nil {
		t.Fatal(err)
	}
	reportRaw, _ := os.ReadFile(filepath.Join(dir, "report.json"))
	var report reportArtifact
	_ = decodePackageJSON(reportRaw, "report.json", &report)
	report.Fixtures = &summary
	if err := writeFixturePackageJSON(filepath.Join(dir, "report.json"), report); err != nil {
		t.Fatal(err)
	}
	markdownRaw, _ := os.ReadFile(filepath.Join(dir, "report.md"))
	if offset := strings.Index(string(markdownRaw), "\n## Fixture lifecycle\n"); offset >= 0 {
		markdownRaw = markdownRaw[:offset]
	}
	marker := "\n## Fixture lifecycle\n\n- Run: `" + summary.RunID + "`\n- Binding: `" + summary.FixtureSetDigest + "`\n- Preparation: `" + string(summary.PreparationStatus) + "`\n- Cleanup: `" + string(summary.Cleanup.Status) + "`\n"
	if err := writeFixturePackageBytes(filepath.Join(dir, "report.md"), append(markdownRaw, marker...)); err != nil {
		t.Fatal(err)
	}
	artifacts, err := inventoryPackage(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFixturePackageJSON(filepath.Join(dir, "manifest.json"), PackageManifest{Version: PackageVersion, RunID: index.RunID, BindingSHA256: index.Binding.BindingSHA256, Artifacts: artifacts}); err != nil {
		t.Fatal(err)
	}
}
