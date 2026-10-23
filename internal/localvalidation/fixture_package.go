package localvalidation

import (
	"bytes"
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
	"strconv"
	"time"

	"github.com/asierhr/featureProof/internal/fixture"
)

// fixtureAuditSeal is the public, secret-free proof over the complete canonical fixture audit.
type fixtureAuditSeal struct {
	KeyID     string `json:"key_id"`
	Digest    string `json:"digest"`
	Signature string `json:"signature"`
}

// FixtureAuditSigner is the writer-only private capability loaded from protected consumer-owned storage.
type FixtureAuditSigner struct {
	KeyID      string
	privateKey ed25519.PrivateKey
}

// FixtureAuditTrust is the reader-only public trust anchor supplied outside the package.
type FixtureAuditTrust struct {
	KeyID     string
	PublicKey ed25519.PublicKey
}

// fixtureAuditPin is the independently persisted trusted public identity checked on every restart.
type fixtureAuditPin struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
}

// OpenFixtureAuditAuthority loads or creates one persistent signer and returns a detached public trust anchor.
func OpenFixtureAuditAuthority(path string, trustedPinPaths ...string) (FixtureAuditSigner, FixtureAuditTrust, error) {
	if len(trustedPinPaths) > 1 {
		return FixtureAuditSigner{}, FixtureAuditTrust{}, &PackageError{Category: "binding_integrity", Path: "fixture-authority"}
	}
	pinPath := path + ".public-pin"
	if len(trustedPinPaths) == 1 {
		pinPath = trustedPinPaths[0]
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return FixtureAuditSigner{}, FixtureAuditTrust{}, &PackageError{Category: "binding_integrity", Path: "fixture-authority"}
	}
	if err := os.MkdirAll(filepath.Dir(pinPath), 0o700); err != nil {
		return FixtureAuditSigner{}, FixtureAuditTrust{}, &PackageError{Category: "binding_integrity", Path: "fixture-authority"}
	}
	release, err := acquireFixtureAuthorityLock(path + ".lock")
	if err != nil {
		return FixtureAuditSigner{}, FixtureAuditTrust{}, &PackageError{Category: "binding_integrity", Path: "fixture-authority-lock"}
	}
	defer release()
	privateExists, pinExists := regularAuthorityExists(path), regularAuthorityExists(pinPath)
	if !privateExists && !pinExists {
		_, privateKey, generateErr := ed25519.GenerateKey(rand.Reader)
		if generateErr != nil {
			return FixtureAuditSigner{}, FixtureAuditTrust{}, &PackageError{Category: "binding_integrity", Path: "fixture-authority"}
		}
		publicKey := privateKey.Public().(ed25519.PublicKey)
		digest := sha256.Sum256(publicKey)
		pinRaw, _ := json.Marshal(fixtureAuditPin{KeyID: hex.EncodeToString(digest[:]), PublicKey: hex.EncodeToString(publicKey)})
		if err := writeFixtureAuthorityFile(pinPath, append(pinRaw, '\n'), writeAll); err != nil {
			return FixtureAuditSigner{}, FixtureAuditTrust{}, &PackageError{Category: "binding_integrity", Path: "fixture-authority-pin-write"}
		}
		if err := writeFixtureAuthorityFile(path, privateKey, writeAll); err != nil {
			_ = os.Remove(pinPath)
			return FixtureAuditSigner{}, FixtureAuditTrust{}, &PackageError{Category: "binding_integrity", Path: "fixture-authority-key-write"}
		}
	} else if !privateExists || !pinExists {
		return FixtureAuditSigner{}, FixtureAuditTrust{}, &PackageError{Category: "binding_integrity", Path: "fixture-authority"}
	}
	privateKey, err := readFixtureAuthorityFile(path, ed25519.PrivateKeySize)
	pinRaw, pinErr := readFixtureAuthorityFile(pinPath, 1024)
	var pin fixtureAuditPin
	if pinErr != nil || decodePackageJSON(pinRaw, "fixture-authority-pin", &pin) != nil || err != nil || len(privateKey) != ed25519.PrivateKeySize {
		return FixtureAuditSigner{}, FixtureAuditTrust{}, &PackageError{Category: "binding_integrity", Path: "fixture-authority-read"}
	}
	derived := ed25519.NewKeyFromSeed(privateKey[:ed25519.SeedSize])
	if !bytes.Equal(derived, privateKey) {
		return FixtureAuditSigner{}, FixtureAuditTrust{}, &PackageError{Category: "binding_integrity", Path: "fixture-authority-coherence"}
	}
	publicKey := append(ed25519.PublicKey(nil), derived[ed25519.PrivateKeySize-ed25519.PublicKeySize:]...)
	keyDigest := sha256.Sum256(publicKey)
	keyID := hex.EncodeToString(keyDigest[:])
	pinnedPublic, pinDecodeErr := hex.DecodeString(pin.PublicKey)
	if pinDecodeErr != nil || pin.KeyID != keyID || !bytes.Equal(pinnedPublic, publicKey) {
		return FixtureAuditSigner{}, FixtureAuditTrust{}, &PackageError{Category: "binding_integrity", Path: "fixture-authority-pin"}
	}
	return FixtureAuditSigner{KeyID: keyID, privateKey: ed25519.PrivateKey(append([]byte(nil), privateKey...))}, FixtureAuditTrust{KeyID: keyID, PublicKey: publicKey}, nil
}

// acquireFixtureAuthorityLock serializes bootstrap/reload so the independent pin and key cannot race.
func acquireFixtureAuthorityLock(path string) (func(), error) {
	for attempt := 0; attempt < 1000; attempt++ {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return func() { _ = file.Close(); _ = os.Remove(path) }, nil
		}
		time.Sleep(time.Millisecond)
	}
	return nil, errors.New("authority lock unavailable")
}

// regularAuthorityExists reports existence without following links; malformed entries remain present and fail later.
func regularAuthorityExists(path string) bool { _, err := os.Lstat(path); return err == nil }

// readFixtureAuthorityFile rejects symlinks, hardlinks, permissive modes, replacement races and oversized material.
func readFixtureAuthorityFile(path string, limit int) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || !fixtureAuthorityOwnerOK(path, before) {
		return nil, errors.New("unsafe authority file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if !singleLink(file) {
		return nil, errors.New("linked authority file")
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, errors.New("replaced authority file")
	}
	return io.ReadAll(io.LimitReader(file, int64(limit+1)))
}

// writeFixtureAuthorityFile performs an exclusive complete durable write and removes partial material on every error.
func writeFixtureAuthorityFile(path string, raw []byte, writeFull func(io.Writer, []byte) (int, error)) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if err = protectFixtureAuthorityFile(path); err != nil {
		return err
	}
	n, err := writeFull(file, raw)
	if err != nil || n != len(raw) {
		return io.ErrShortWrite
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	err = syncFixtureAuthorityDirectory(filepath.Dir(path))
	if err == nil {
		ok = true
	}
	return err
}

// writeAll repeats short writes until every byte is persisted or the writer returns an error.
func writeAll(writer io.Writer, raw []byte) (int, error) {
	written := 0
	for written < len(raw) {
		n, err := writer.Write(raw[written:])
		written += n
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

// AttachFixtureAudit adds the allowlisted lifecycle summary to an existing package and reseals its closed inventory.
func AttachFixtureAudit(ctx context.Context, dir string, summary fixture.FixtureSummary, signer FixtureAuditSigner) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	packagePath := filepath.Join(dir, "package.json")
	raw, err := os.ReadFile(packagePath)
	if err != nil {
		return err
	}
	index, err := decodePackageIndex(raw)
	if err != nil {
		return err
	}
	if err := fixture.ValidateSummary(summary, index.RunID); err != nil {
		return err
	}
	index.Fixtures = &summary
	seal, err := signFixtureAudit(index, summary, signer)
	if err != nil {
		return err
	}
	if err := writeFixturePackageJSON(packagePath, index); err != nil {
		return err
	}
	if err := writeFixturePackageJSON(filepath.Join(dir, "fixtures.json"), summary); err != nil {
		return err
	}
	if err := writeFixturePackageJSON(filepath.Join(dir, "fixture-authority.json"), seal); err != nil {
		return err
	}
	reportPath := filepath.Join(dir, "report.json")
	reportRaw, err := os.ReadFile(reportPath)
	if err != nil {
		return err
	}
	var report reportArtifact
	if err := decodePackageJSON(reportRaw, "report.json", &report); err != nil {
		return err
	}
	report.Fixtures = &summary
	if err := writeFixturePackageJSON(reportPath, report); err != nil {
		return err
	}
	markdownPath := filepath.Join(dir, "report.md")
	markdown, err := os.ReadFile(markdownPath)
	if err != nil {
		return err
	}
	if offset := bytes.Index(markdown, []byte("\n## Fixture lifecycle\n")); offset >= 0 {
		markdown = markdown[:offset]
	}
	marker := fixtureMarkdownSection(summary)
	if err := writeFixturePackageBytes(markdownPath, append(markdown, marker...)); err != nil {
		return err
	}
	artifacts, err := inventoryPackage(ctx, dir)
	if err != nil {
		return err
	}
	manifest := PackageManifest{Version: PackageVersion, RunID: index.RunID, BindingSHA256: index.Binding.BindingSHA256, Artifacts: artifacts}
	if err := writeFixturePackageJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		return err
	}
	trust := FixtureAuditTrust{KeyID: signer.KeyID, PublicKey: append(ed25519.PublicKey(nil), signer.privateKey[ed25519.PrivateKeySize-ed25519.PublicKeySize:]...)}
	_, err = ReadPackageWithFixtureTrust(ctx, dir, trust)
	return err
}

// signFixtureAudit signs the canonical complete audit with consumer-owned authority stored outside the writable package.
func signFixtureAudit(index PackageIndex, summary fixture.FixtureSummary, signer FixtureAuditSigner) (fixtureAuditSeal, error) {
	raw, err := json.Marshal(summary)
	if err != nil {
		return fixtureAuditSeal{}, err
	}
	digest := sha256.Sum256(raw)
	if len(signer.privateKey) != ed25519.PrivateKeySize || !validFixtureAuthorityID(signer.KeyID) {
		return fixtureAuditSeal{}, &PackageError{Category: "binding_integrity", Path: "fixture-authority.json"}
	}
	publicDigest := sha256.Sum256(signer.privateKey[ed25519.PrivateKeySize-ed25519.PublicKeySize:])
	if signer.KeyID != hex.EncodeToString(publicDigest[:]) {
		return fixtureAuditSeal{}, &PackageError{Category: "binding_integrity", Path: "fixture-authority.json"}
	}
	message := fixtureAuditMessage(index, signer.KeyID, digest)
	return fixtureAuditSeal{KeyID: signer.KeyID, Digest: hex.EncodeToString(digest[:]), Signature: hex.EncodeToString(ed25519.Sign(signer.privateKey, message))}, nil
}

// fixtureAuditMessage domain-separates the audit signature and binds package version, run, base binding and audit digest.
func fixtureAuditMessage(index PackageIndex, keyID string, digest [32]byte) []byte {
	return []byte("featureproof.fixture-audit/v1\x00" + strconv.Itoa(index.Version) + "\x00" + index.RunID + "\x00" + index.Binding.BindingSHA256 + "\x00" + keyID + "\x00" + hex.EncodeToString(digest[:]))
}

// validFixtureAuthorityID accepts only a canonical SHA-256 key fingerprint.
func validFixtureAuthorityID(value string) bool {
	_, err := hex.DecodeString(value)
	return len(value) == 64 && err == nil
}

// fixtureMarkdownSection renders the single canonical human-readable fixture suffix verified by the offline reader.
func fixtureMarkdownSection(summary fixture.FixtureSummary) []byte {
	return []byte("\n## Fixture lifecycle\n\n- Run: `" + summary.RunID + "`\n- Binding: `" + summary.FixtureSetDigest + "`\n- Preparation: `" + string(summary.PreparationStatus) + "`\n- Cleanup: `" + string(summary.Cleanup.Status) + "`\n")
}

// writeFixturePackageBytes atomically replaces one report artifact before inventory sealing.
func writeFixturePackageBytes(path string, raw []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".fixture-package-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(raw)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

// writeFixturePackageJSON atomically replaces one bounded package JSON artifact in its existing directory.
func writeFixturePackageJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".fixture-package-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(raw)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
