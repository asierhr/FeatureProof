package contractreview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// PublishRequest contains a trusted root-relative target and its guarded replacement expectation.
type PublishRequest struct {
	Target         string
	Replace        bool
	ExpectedDigest string
	AttemptID      string
}

// Publisher is the application-owned boundary that commits canonical output bytes atomically.
type Publisher interface {
	Publish(context.Context, PublishRequest, []byte, string) (string, error)
	Inspect(context.Context, string, string) (string, string, error)
}

// LocalPublisher publishes only beneath an already trusted canonical root and exposes an internal checkpoint for deterministic race tests.
type LocalPublisher struct {
	root           string
	rootHandle     *os.File
	rootIdentity   os.FileInfo
	closeOnce      sync.Once
	closeErr       error
	beforeCommit   func()
	beforeInspect  func()
	checkpoint     func(string)
	syncFile       func(*os.File) error
	wait           func(context.Context, time.Duration) error
	visibilityWait time.Duration
}

// Close releases the stable publication-root descriptor exactly once.
func (p *LocalPublisher) Close() error {
	p.closeOnce.Do(func() {
		if p.rootHandle != nil {
			p.closeErr = p.rootHandle.Close()
			p.rootHandle = nil
		}
	})
	return p.closeErr
}

// Inspect safely identifies an existing target so a prepared export can reconcile after a crash.
func (p *LocalPublisher) Inspect(ctx context.Context, relative, attemptID string) (string, string, error) {
	if err := cancellation(ctx, "publish.inspect"); err != nil {
		return "", "", err
	}
	if err := p.verifyRoot(); err != nil {
		return "", "", err
	}
	target, err := p.resolve(relative)
	if err != nil {
		return "", "", err
	}
	parentRelative, _ := filepath.Rel(p.root, filepath.Dir(target))
	parentHandle, err := openRelativeDirectory(p.rootHandle, parentRelative, false)
	if err != nil {
		return "", "", typedError(CategoryInvalidInput, "publish.parent", err)
	}
	defer parentHandle.Close()
	parentIdentity, err := parentHandle.Stat()
	if err != nil {
		return "", "", typedError(CategoryUnavailable, "publish.parent", err)
	}
	if p.beforeInspect != nil {
		p.beforeInspect()
	}
	currentParent, statErr := os.Stat(filepath.Dir(target))
	if statErr != nil || !os.SameFile(parentIdentity, currentParent) {
		return "", "", typedError(CategoryConflict, "publish.toctou", statErr)
	}
	targetFile, err := openRegularInDirectory(parentHandle, filepath.Base(target))
	if err != nil {
		return "", "", typedError(CategoryInvalidInput, "publish.target", nil)
	}
	defer targetFile.Close()
	info, err := targetFile.Stat()
	if err != nil {
		return "", "", typedError(CategoryUnavailable, "publish.target", err)
	}
	var marker publicationMarker
	if attemptID != "" {
		marker, err = p.readCommitMarker(attemptID)
		if err != nil {
			return "", "", typedError(CategoryConflict, "publish.marker", err)
		}
	}
	links, linkErr := fileLinkCountHandle(targetFile)
	if linkErr == nil && links > 1 && marker.Temp != "" {
		if temp, resolveErr := p.resolve(filepath.FromSlash(marker.Temp)); resolveErr == nil {
			if filepath.Dir(temp) == filepath.Dir(target) {
				tempFile, openErr := openRegularInDirectory(parentHandle, filepath.Base(temp))
				if openErr == nil {
					tempInfo, statErr := tempFile.Stat()
					_ = tempFile.Close()
					if statErr == nil && os.SameFile(info, tempInfo) {
						if cancelErr := cancellation(ctx, "publish.inspect"); cancelErr != nil {
							return "", "", cancelErr
						}
						_ = removeInDirectory(parentHandle, filepath.Base(temp))
						_ = syncOpenDirectory(parentHandle)
					}
				}
				links, linkErr = fileLinkCountHandle(targetFile)
			}
		}
	}
	if linkErr != nil || links != 1 {
		return "", "", typedError(CategoryInvalidInput, "publish.hardlink", linkErr)
	}
	digest, err := digestOpenFile(targetFile)
	if err != nil {
		return "", "", err
	}
	relativePath, err := filepath.Rel(p.root, target)
	if err != nil {
		return "", "", typedError(CategoryInternal, "publish.target", err)
	}
	if attemptID != "" {
		if marker.Target != filepath.ToSlash(relativePath) || marker.OutputDigest != digest {
			return "", "", typedError(CategoryConflict, "publish.marker", nil)
		}
	}
	if err = cancellation(ctx, "publish.inspect"); err != nil {
		return "", "", err
	}
	return filepath.ToSlash(relativePath), digest, nil
}

// digestOpenFile hashes an already-open descriptor and restores its offset for repeatable verification.
func digestOpenFile(file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", typedError(CategoryUnavailable, "publish.target", err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", typedError(CategoryUnavailable, "publish.target", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// NewLocalPublisher validates the root without following a symbolic-link root.
func NewLocalPublisher(root string) (*LocalPublisher, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, typedError(CategoryInvalidInput, "publisher.root", nil)
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, typedError(CategoryInvalidInput, "publisher.root", nil)
	}
	handle, err := openRootDescriptor(abs)
	if err != nil {
		return nil, typedError(CategoryUnavailable, "publisher.root", err)
	}
	identity, err := handle.Stat()
	if err != nil {
		_ = handle.Close()
		return nil, typedError(CategoryUnavailable, "publisher.root", err)
	}
	markerHandle, err := openRelativeDirectory(handle, ".featureproof-publication-markers", true)
	if err != nil {
		_ = handle.Close()
		return nil, typedError(CategoryInvalidInput, "publisher.marker", err)
	}
	_ = markerHandle.Close()
	return &LocalPublisher{root: abs, rootHandle: handle, rootIdentity: identity, visibilityWait: 250 * time.Millisecond}, nil
}

// Publish performs guarded create or replacement through a same-directory synced temporary file.
func (p *LocalPublisher) Publish(ctx context.Context, request PublishRequest, raw []byte, expectedOutput string) (string, error) {
	return p.publish(ctx, request, raw, expectedOutput)
}

// publish coordinates one attempt under a durable process lock before it may reuse crash-owned state.
func (p *LocalPublisher) publish(ctx context.Context, request PublishRequest, raw []byte, expectedOutput string) (string, error) {
	if err := cancellation(ctx, "publish"); err != nil {
		return "", err
	}
	if err := p.verifyRoot(); err != nil {
		return "", err
	}
	if digestBytes(raw) != expectedOutput {
		return "", typedError(CategoryInvalidInput, "publish.digest", nil)
	}
	target, err := p.resolve(request.Target)
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(target)
	parentRelative, _ := filepath.Rel(p.root, parent)
	parentHandle, err := openRelativeDirectory(p.rootHandle, parentRelative, true)
	if err != nil {
		return "", typedError(CategoryInvalidInput, "publish.parent", err)
	}
	defer parentHandle.Close()
	parentIdentity, err := parentHandle.Stat()
	if err != nil {
		return "", typedError(CategoryUnavailable, "publish.parent", err)
	}
	committedTarget := filepath.ToSlash(request.Target)
	resumeTemp := ""
	intentExists := false
	var ownership *os.File
	if request.AttemptID != "" {
		if !validDigest(request.AttemptID) {
			return "", typedError(CategoryInvalidInput, "publish.attempt", nil)
		}
		ownership, err = p.acquireAttemptOwnership(ctx, request.AttemptID)
		if err != nil {
			return "", err
		}
		defer func() { _ = releaseAttemptLock(ownership) }()
		if err = cancellation(ctx, "publish.owner"); err != nil {
			return "", err
		}
		marker, markerErr := p.readCommitMarker(request.AttemptID)
		if markerErr == nil {
			committed, recoverErr := p.recoverPrepared(ctx, parentHandle, parent, target, committedTarget, expectedOutput, marker)
			if recoverErr != nil {
				return "", recoverErr
			}
			if cancelErr := cancellation(ctx, "publish.cleanup"); cancelErr != nil {
				return "", cancelErr
			}
			_ = p.removePublicationIntent(request.AttemptID)
			return p.confirmPublication(ctx, committed)
		}
		if !descriptorIsNotExist(markerErr) {
			return "", markerErr
		}
		intentTemp, recoveredIntent, recoverErr := p.recoverAbandonedAttempt(parentRelative, request, committedTarget, expectedOutput)
		if recoverErr != nil {
			return "", recoverErr
		}
		if recoveredIntent {
			resumeTemp = intentTemp
			intentExists = true
		} else {
			if p.checkpoint != nil {
				p.checkpoint("intent_absent")
			}
			entries, readErr := parentHandle.ReadDir(-1)
			if readErr != nil {
				return "", typedError(CategoryUnavailable, "publish.temp", readErr)
			}
			orphans := []string{}
			prefix := ".featureproof-contract-" + request.AttemptID[:12] + "-"
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
					orphans = append(orphans, entry.Name())
				}
			}
			if len(orphans) > 0 {
				sort.Strings(orphans)
				resumeTemp = filepath.Join(parent, orphans[0])
			}
		}
	}
	priorFile, priorErr := openRegularInDirectory(parentHandle, filepath.Base(target))
	exists := priorErr == nil
	if priorErr != nil && !descriptorIsNotExist(priorErr) {
		return "", typedError(CategoryInvalidInput, "publish.target", priorErr)
	}
	var priorInfo os.FileInfo
	if exists {
		priorInfo, err = priorFile.Stat()
		if err != nil {
			_ = priorFile.Close()
			return "", typedError(CategoryUnavailable, "publish.target", err)
		}
		links, linkErr := fileLinkCountHandle(priorFile)
		if linkErr != nil || links != 1 {
			_ = priorFile.Close()
			return "", typedError(CategoryInvalidInput, "publish.hardlink", linkErr)
		}
		if !request.Replace {
			actual, hashErr := digestOpenFile(priorFile)
			_ = priorFile.Close()
			if hashErr == nil && actual == expectedOutput && request.AttemptID != "" {
				if marker, markerErr := p.readCommitMarker(request.AttemptID); markerErr == nil {
					return p.recoverPrepared(ctx, parentHandle, parent, target, committedTarget, expectedOutput, marker)
				}
			}
			return "", typedError(CategoryTargetExists, "publish.target", nil)
		}
		if !validDigest(request.ExpectedDigest) {
			_ = priorFile.Close()
			return "", typedError(CategoryConflict, "publish.expected_digest", nil)
		}
		actual, hashErr := digestOpenFile(priorFile)
		_ = priorFile.Close()
		if hashErr != nil {
			return "", hashErr
		}
		if actual != request.ExpectedDigest {
			return "", typedError(CategoryConflict, "publish.expected_digest", nil)
		}
	} else if request.Replace {
		return "", typedError(CategoryConflict, "publish.target", nil)
	}
	if err := cancellation(ctx, "publish.stage"); err != nil {
		return "", err
	}
	pattern := ".featureproof-contract-"
	if request.AttemptID != "" {
		pattern = ".featureproof-contract-" + request.AttemptID[:12] + "-"
	}
	var temp *os.File
	if resumeTemp != "" {
		if err = cancellation(ctx, "publish.recover"); err != nil {
			return "", err
		}
		temp, err = resetFileInDirectory(parentHandle, parent, filepath.Base(resumeTemp))
		if descriptorIsNotExist(err) && intentExists {
			if cancelErr := cancellation(ctx, "publish.recover"); cancelErr != nil {
				return "", cancelErr
			}
			_ = p.removePublicationIntent(request.AttemptID)
			intentExists = false
			resumeTemp = ""
			temp, err = createTempInDirectory(parentHandle, parent, pattern)
		}
	} else {
		temp, err = createTempInDirectory(parentHandle, parent, pattern)
	}
	if err != nil {
		return "", typedError(CategoryUnavailable, "publish.temp", err)
	}
	tempName := temp.Name()
	if p.checkpoint != nil {
		p.checkpoint("temp_created")
	}
	if request.AttemptID == "" {
		defer func() { _ = removeInDirectory(parentHandle, filepath.Base(tempName)) }()
	}
	if request.AttemptID != "" && !intentExists {
		relativeTemp, relErr := filepath.Rel(p.root, tempName)
		if relErr != nil {
			_ = temp.Close()
			return "", typedError(CategoryInternal, "publish.temp", relErr)
		}
		previous := ""
		if exists {
			previous = request.ExpectedDigest
		}
		intent := publicationIntent{AttemptID: request.AttemptID, Target: committedTarget, OutputDigest: expectedOutput, Temp: filepath.ToSlash(relativeTemp), Replace: exists, PreviousDigest: previous}
		if err = p.writePublicationIntent(intent); err != nil {
			closeErr := temp.Close()
			if cancelErr := cancellation(ctx, "publish.cleanup"); cancelErr != nil {
				return "", cancelErr
			}
			cleanupErr := p.cleanupAttemptTemp(parentHandle, filepath.Base(tempName))
			if closeErr != nil || cleanupErr != nil {
				return "", typedError(CategoryUnavailable, "publish.temp", errors.Join(closeErr, cleanupErr))
			}
			return "", err
		}
		if p.checkpoint != nil {
			p.checkpoint("intent_synced")
		}
	}
	if err = temp.Chmod(0600); err == nil {
		cut := len(raw) / 2
		if cut == 0 {
			cut = len(raw)
		}
		_, err = temp.Write(raw[:cut])
		if err == nil && p.checkpoint != nil {
			p.checkpoint("temp_partial")
		}
		if err == nil {
			err = cancellation(ctx, "publish.write")
		}
		if err == nil && cut < len(raw) {
			_, err = temp.Write(raw[cut:])
		}
	}
	if err == nil {
		if p.syncFile != nil {
			err = p.syncFile(temp)
		} else {
			err = temp.Sync()
		}
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		if cancelErr := cancellation(ctx, "publish.write"); cancelErr != nil {
			return "", cancelErr
		}
		return "", typedError(CategoryUnavailable, "publish.write", err)
	}
	if p.checkpoint != nil {
		p.checkpoint("temp_synced")
	}
	if err = cancellation(ctx, "publish.write"); err != nil {
		return "", err
	}
	if request.AttemptID != "" {
		previous := ""
		if exists {
			previous = request.ExpectedDigest
		}
		relativeTemp, relErr := filepath.Rel(p.root, tempName)
		if relErr != nil {
			return "", typedError(CategoryInternal, "publish.temp", relErr)
		}
		if err = p.writeCommitMarker(publicationMarker{AttemptID: request.AttemptID, Target: committedTarget, OutputDigest: expectedOutput, Temp: filepath.ToSlash(relativeTemp), Replace: exists, PreviousDigest: previous}); err != nil {
			return "", err
		}
		if p.checkpoint != nil {
			p.checkpoint("marker_synced")
		}
		if err = cancellation(ctx, "publish.cleanup"); err != nil {
			return "", err
		}
		_ = p.removePublicationIntent(request.AttemptID)
	}
	if err := cancellation(ctx, "publish.commit"); err != nil {
		return "", err
	}
	if p.beforeCommit != nil {
		p.beforeCommit()
	}
	if err = p.verifyRoot(); err != nil {
		return "", err
	}
	currentParent, statErr := os.Stat(parent)
	if statErr != nil || !os.SameFile(parentIdentity, currentParent) {
		return "", typedError(CategoryConflict, "publish.toctou", statErr)
	}
	currentFile, currentErr := openRegularInDirectory(parentHandle, filepath.Base(target))
	if exists {
		if currentErr != nil {
			return "", typedError(CategoryConflict, "publish.toctou", currentErr)
		}
		currentInfo, infoErr := currentFile.Stat()
		if infoErr != nil || !os.SameFile(priorInfo, currentInfo) {
			_ = currentFile.Close()
			return "", typedError(CategoryConflict, "publish.toctou", nil)
		}
		actual, hashErr := digestOpenFile(currentFile)
		_ = currentFile.Close()
		if hashErr != nil || actual != request.ExpectedDigest {
			return "", typedError(CategoryConflict, "publish.toctou", nil)
		}
	} else if currentErr == nil {
		actual, hashErr := digestOpenFile(currentFile)
		_ = currentFile.Close()
		if hashErr == nil && actual == expectedOutput && request.AttemptID != "" {
			if marker, markerErr := p.readCommitMarker(request.AttemptID); markerErr == nil {
				return p.recoverPrepared(ctx, parentHandle, parent, target, committedTarget, expectedOutput, marker)
			}
		}
		return "", typedError(CategoryTargetExists, "publish.target", nil)
	} else if !descriptorIsNotExist(currentErr) {
		return "", typedError(CategoryUnavailable, "publish.target", currentErr)
	}
	if err = cancellation(ctx, "publish.commit"); err != nil {
		return "", err
	}
	if err = atomicPublisherCommit(parentHandle, parent, filepath.Base(tempName), filepath.Base(target), exists, p.checkpoint); err != nil {
		return "", typedError(CategoryUnavailable, "publish.rename", err)
	}
	if p.checkpoint != nil {
		p.checkpoint("target_committed")
	}
	if err = syncOpenDirectory(parentHandle); err != nil {
		return "", err
	}
	if p.checkpoint != nil {
		p.checkpoint("directory_synced")
	}
	committed, openErr := openRegularInDirectory(parentHandle, filepath.Base(target))
	if openErr != nil {
		return "", typedError(CategoryUnavailable, "publish.verify", openErr)
	}
	actual, err := digestOpenFile(committed)
	_ = committed.Close()
	if err != nil || actual != expectedOutput {
		return "", typedError(CategoryUnavailable, "publish.verify", err)
	}
	return p.confirmPublication(ctx, committedTarget)
}

// confirmPublication prevents a durable target from being reported as successful after its request was cancelled.
func (p *LocalPublisher) confirmPublication(ctx context.Context, target string) (string, error) {
	if err := cancellation(ctx, "publish.confirm"); err != nil {
		return "", err
	}
	return target, nil
}

// cleanupAttemptTemp removes and syncs only the losing caller's descriptor-relative private temp.
func (p *LocalPublisher) cleanupAttemptTemp(parentHandle *os.File, name string) error {
	if err := removeInDirectory(parentHandle, name); err != nil && !descriptorIsNotExist(err) {
		return err
	}
	return syncOpenDirectory(parentHandle)
}

// acquireAttemptOwnership waits cancelably for the OS to prove that no process still owns this durable attempt.
func (p *LocalPublisher) acquireAttemptOwnership(ctx context.Context, attemptID string) (*os.File, error) {
	directory, err := openRelativeDirectory(p.rootHandle, ".featureproof-publication-markers", false)
	if err != nil {
		return nil, typedError(CategoryUnavailable, "publish.owner", err)
	}
	defer directory.Close()
	for {
		owner, acquired, lockErr := tryAttemptLock(directory, attemptID+".owner.lock")
		if lockErr != nil {
			return nil, typedError(CategoryUnavailable, "publish.owner", lockErr)
		}
		if acquired {
			if p.checkpoint != nil {
				p.checkpoint("ownership_acquired")
			}
			if err = cancellation(ctx, "publish.owner"); err != nil {
				_ = releaseAttemptLock(owner)
				return nil, err
			}
			return owner, nil
		}
		if p.checkpoint != nil {
			p.checkpoint("ownership_wait")
		}
		if err = p.waitFor(ctx, 2*time.Millisecond); err != nil {
			return nil, err
		}
	}
}

// waitFor uses an injected deterministic waiter or a context-bound timer while a concurrent publisher owns the intent.
func (p *LocalPublisher) waitFor(ctx context.Context, duration time.Duration) error {
	if p.wait != nil {
		return p.wait(ctx, duration)
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return typedError(CategoryCancelled, "publish.wait", ctx.Err())
	case <-timer.C:
		return nil
	}
}

// verifyRoot ensures every path operation still resolves to the descriptor retained at composition.
func (p *LocalPublisher) verifyRoot() error {
	current, err := os.Stat(p.root)
	if err != nil || p.rootHandle == nil || p.rootIdentity == nil || !os.SameFile(p.rootIdentity, current) {
		return typedError(CategoryConflict, "publisher.root_drift", err)
	}
	return nil
}

// publicationMarker is the publisher-owned durable proof that this exact attempt completed its rename.
type publicationMarker struct {
	AttemptID, Target, OutputDigest, Temp, PreviousDigest string
	Replace                                               bool
}

// publicationIntent durably binds an attempt to its private temp before any output byte is written.
type publicationIntent publicationMarker

// recoverPrepared completes or confirms an attempt whose durable marker preceded the filesystem commit.
func (p *LocalPublisher) recoverPrepared(ctx context.Context, parentHandle *os.File, parent, target, relativeTarget, expectedOutput string, marker publicationMarker) (string, error) {
	if err := cancellation(ctx, "publish.recover"); err != nil {
		return "", err
	}
	if marker.Target != relativeTarget || marker.OutputDigest != expectedOutput || marker.Temp == "" {
		return "", typedError(CategoryConflict, "publish.marker", nil)
	}
	parentIdentity, err := parentHandle.Stat()
	currentParent, statErr := os.Stat(parent)
	if err != nil || statErr != nil || !os.SameFile(parentIdentity, currentParent) {
		return "", typedError(CategoryConflict, "publish.toctou", statErr)
	}
	temp, err := p.resolve(filepath.FromSlash(marker.Temp))
	if err != nil || filepath.Dir(temp) != parent {
		return "", typedError(CategoryConflict, "publish.marker", err)
	}
	targetFile, targetErr := openRegularInDirectory(parentHandle, filepath.Base(target))
	if targetErr == nil {
		targetInfo, infoErr := targetFile.Stat()
		digest, digestErr := digestOpenFile(targetFile)
		_ = targetFile.Close()
		if infoErr != nil {
			return "", typedError(CategoryUnavailable, "publish.target", infoErr)
		}
		if digestErr == nil && digest == expectedOutput {
			if tempFile, tempErr := openRegularInDirectory(parentHandle, filepath.Base(temp)); tempErr == nil {
				tempInfo, statErr := tempFile.Stat()
				_ = tempFile.Close()
				if statErr == nil && os.SameFile(targetInfo, tempInfo) {
					if cancelErr := cancellation(ctx, "publish.cleanup"); cancelErr != nil {
						return "", cancelErr
					}
					_ = removeInDirectory(parentHandle, filepath.Base(temp))
					_ = syncOpenDirectory(parentHandle)
				}
			}
			if cancelErr := cancellation(ctx, "publish.recover"); cancelErr != nil {
				return "", cancelErr
			}
			return p.confirmPublication(ctx, relativeTarget)
		}
		if !marker.Replace || marker.PreviousDigest == "" {
			return "", typedError(CategoryTargetExists, "publish.target", nil)
		}
		priorFile, openErr := openRegularInDirectory(parentHandle, filepath.Base(target))
		if openErr != nil {
			return "", typedError(CategoryConflict, "publish.expected_digest", openErr)
		}
		prior, priorErr := digestOpenFile(priorFile)
		_ = priorFile.Close()
		if priorErr != nil || prior != marker.PreviousDigest {
			return "", typedError(CategoryConflict, "publish.expected_digest", priorErr)
		}
	} else if !descriptorIsNotExist(targetErr) {
		return "", typedError(CategoryUnavailable, "publish.target", targetErr)
	}
	tempFile, openErr := openRegularInDirectory(parentHandle, filepath.Base(temp))
	if openErr != nil {
		if committed, awaitErr := p.awaitCommittedTarget(ctx, parentHandle, filepath.Base(target), expectedOutput); awaitErr != nil || committed {
			if committed {
				return p.confirmPublication(ctx, relativeTarget)
			}
			return "", awaitErr
		}
		return "", typedError(CategoryConflict, "publish.temp", openErr)
	}
	digest, digestErr := digestOpenFile(tempFile)
	_ = tempFile.Close()
	if digestErr != nil || digest != expectedOutput {
		return "", typedError(CategoryConflict, "publish.temp", digestErr)
	}
	if err = cancellation(ctx, "publish.commit"); err != nil {
		return "", err
	}
	if p.beforeCommit != nil {
		p.beforeCommit()
	}
	if err = cancellation(ctx, "publish.commit"); err != nil {
		return "", err
	}
	if err = atomicPublisherCommit(parentHandle, parent, filepath.Base(temp), filepath.Base(target), marker.Replace, p.checkpoint); err != nil {
		committed, awaitErr := p.awaitCommittedTarget(ctx, parentHandle, filepath.Base(target), expectedOutput)
		if committed {
			if cancelErr := cancellation(ctx, "publish.cleanup"); cancelErr != nil {
				return "", cancelErr
			}
			_ = removeInDirectory(parentHandle, filepath.Base(temp))
			if syncErr := syncOpenDirectory(parentHandle); syncErr != nil {
				return "", syncErr
			}
			if p.checkpoint != nil {
				p.checkpoint("directory_synced")
			}
			return p.confirmPublication(ctx, relativeTarget)
		}
		if awaitErr != nil {
			return "", awaitErr
		}
		return "", typedError(CategoryUnavailable, "publish.rename", err)
	}
	if err = syncOpenDirectory(parentHandle); err != nil {
		return "", err
	}
	if p.checkpoint != nil {
		p.checkpoint("directory_synced")
	}
	committed, openErr := openRegularInDirectory(parentHandle, filepath.Base(target))
	if openErr != nil {
		return "", typedError(CategoryUnavailable, "publish.verify", openErr)
	}
	digest, digestErr = digestOpenFile(committed)
	_ = committed.Close()
	if digestErr != nil || digest != expectedOutput {
		return "", typedError(CategoryUnavailable, "publish.verify", digestErr)
	}
	return p.confirmPublication(ctx, relativeTarget)
}

// awaitCommittedTarget closes the rename visibility window when another process helps the same durable attempt.
func (p *LocalPublisher) awaitCommittedTarget(ctx context.Context, parentHandle *os.File, target, expectedOutput string) (bool, error) {
	const interval = 2 * time.Millisecond
	attempts := int(p.visibilityWait / interval)
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		if err := cancellation(ctx, "publish.recover"); err != nil {
			return false, err
		}
		committed, openErr := openRegularInDirectory(parentHandle, target)
		if openErr == nil {
			digest, digestErr := digestOpenFile(committed)
			_ = committed.Close()
			if digestErr == nil && digest == expectedOutput {
				if err := cancellation(ctx, "publish.recover"); err != nil {
					return false, err
				}
				return true, nil
			}
		}
		if err := p.waitFor(ctx, interval); err != nil {
			return false, err
		}
	}
	return false, nil
}

// recoverAbandonedAttempt removes only the temp durably bound to this attempt so a pre-sync crash can retry from clean bytes.
func (p *LocalPublisher) recoverAbandonedAttempt(parentRelative string, request PublishRequest, relativeTarget, expectedOutput string) (string, bool, error) {
	intent, found, err := p.readPublicationIntent(request.AttemptID)
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, nil
	}
	previous := ""
	if request.Replace {
		previous = request.ExpectedDigest
	}
	if intent.Target != relativeTarget || intent.OutputDigest != expectedOutput || intent.Replace != request.Replace || intent.PreviousDigest != previous {
		return "", false, typedError(CategoryConflict, "publish.intent", nil)
	}
	temp, resolveErr := p.resolve(filepath.FromSlash(intent.Temp))
	if resolveErr != nil || filepath.Clean(filepath.Dir(temp)) != filepath.Clean(filepath.Join(p.root, parentRelative)) {
		return "", false, typedError(CategoryConflict, "publish.intent", resolveErr)
	}
	return temp, true, nil
}

// writePublicationIntent commits the attempt-to-temp binding before callers write any possibly partial output.
func (p *LocalPublisher) writePublicationIntent(intent publicationIntent) error {
	marker := publicationMarker(intent)
	raw, _ := json.Marshal(intent)
	directory := filepath.Join(p.root, ".featureproof-publication-markers")
	directoryHandle, err := openRelativeDirectory(p.rootHandle, ".featureproof-publication-markers", false)
	if err != nil {
		return typedError(CategoryUnavailable, "publish.intent", err)
	}
	defer directoryHandle.Close()
	temp, err := createTempInDirectory(directoryHandle, directory, ".intent-"+intent.AttemptID[:12]+"-")
	if err != nil {
		return typedError(CategoryUnavailable, "publish.intent", err)
	}
	tempName := temp.Name()
	defer func() { _ = removeInDirectory(directoryHandle, filepath.Base(tempName)) }()
	if _, err = temp.Write(raw); err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return typedError(CategoryUnavailable, "publish.intent", err)
	}
	err = atomicCommitInDirectory(directoryHandle, directory, filepath.Base(tempName), intent.AttemptID+".intent.json", false)
	if err != nil {
		existing, found, readErr := p.readPublicationIntent(intent.AttemptID)
		if readErr == nil && found && publicationMarker(existing) == marker {
			return nil
		}
		return typedError(CategoryConflict, "publish.intent", err)
	}
	return syncOpenDirectory(directoryHandle)
}

// readPublicationIntent reads an immutable intent and distinguishes absence from corrupt durable state.
func (p *LocalPublisher) readPublicationIntent(attemptID string) (publicationIntent, bool, error) {
	directory, err := openRelativeDirectory(p.rootHandle, ".featureproof-publication-markers", false)
	if err != nil {
		return publicationIntent{}, false, typedError(CategoryUnavailable, "publish.intent", err)
	}
	defer directory.Close()
	file, err := openRegularInDirectory(directory, attemptID+".intent.json")
	if descriptorIsNotExist(err) {
		return publicationIntent{}, false, nil
	}
	if err != nil {
		return publicationIntent{}, false, typedError(CategoryUnavailable, "publish.intent", err)
	}
	defer file.Close()
	raw, readErr := io.ReadAll(file)
	var intent publicationIntent
	if readErr != nil || json.Unmarshal(raw, &intent) != nil || intent.AttemptID != attemptID || !validDigest(intent.OutputDigest) || intent.Target == "" || intent.Temp == "" || intent.Replace && !validDigest(intent.PreviousDigest) {
		return publicationIntent{}, false, typedError(CategoryInternal, "publish.intent", readErr)
	}
	return intent, true, nil
}

// removePublicationIntent durably removes an intent after either cleanup or promotion to a commit marker.
func (p *LocalPublisher) removePublicationIntent(attemptID string) error {
	directory, err := openRelativeDirectory(p.rootHandle, ".featureproof-publication-markers", false)
	if err != nil {
		return typedError(CategoryUnavailable, "publish.intent", err)
	}
	defer directory.Close()
	if err = removeInDirectory(directory, attemptID+".intent.json"); err != nil && !descriptorIsNotExist(err) {
		return typedError(CategoryUnavailable, "publish.intent", err)
	}
	return syncOpenDirectory(directory)
}

// mustReadDirectory returns a best-effort snapshot used only to clean private temp names before an atomic intent retry.
func mustReadDirectory(directory *os.File) []os.DirEntry {
	entries, _ := directory.ReadDir(-1)
	return entries
}

// writeCommitMarker durably records a confirmed commit without replacing a marker from another outcome.
func (p *LocalPublisher) writeCommitMarker(marker publicationMarker) error {
	directory := filepath.Join(p.root, ".featureproof-publication-markers")
	raw, _ := json.Marshal(marker)
	directoryHandle, openErr := openRelativeDirectory(p.rootHandle, ".featureproof-publication-markers", false)
	if openErr != nil {
		return typedError(CategoryUnavailable, "publish.marker", openErr)
	}
	defer directoryHandle.Close()
	prefix := ".marker-" + marker.AttemptID[:12] + "-"
	markerTemps := []string{}
	for _, entry := range mustReadDirectory(directoryHandle) {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			markerTemps = append(markerTemps, entry.Name())
		}
	}
	var temp *os.File
	var err error
	if len(markerTemps) > 0 {
		sort.Strings(markerTemps)
		temp, err = resetFileInDirectory(directoryHandle, directory, markerTemps[0])
	} else {
		temp, err = createTempInDirectory(directoryHandle, directory, prefix)
	}
	if err != nil {
		return typedError(CategoryUnavailable, "publish.marker", err)
	}
	tempName := temp.Name()
	defer func() { _ = removeInDirectory(directoryHandle, filepath.Base(tempName)) }()
	if _, err = temp.Write(raw); err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return typedError(CategoryUnavailable, "publish.marker", err)
	}
	if p.checkpoint != nil {
		p.checkpoint("marker_temp_synced")
	}
	err = atomicCommitInDirectory(directoryHandle, directory, filepath.Base(tempName), marker.AttemptID+".json", false)
	if err != nil {
		existing, readErr := p.readCommitMarker(marker.AttemptID)
		if readErr == nil && existing == marker {
			return nil
		}
		return typedError(CategoryConflict, "publish.marker", err)
	}
	if p.checkpoint != nil {
		p.checkpoint("marker_committed")
	}
	return syncOpenDirectory(directoryHandle)
}

// readCommitMarker verifies the closed attempt receipt used for crash reconciliation.
func (p *LocalPublisher) readCommitMarker(attemptID string) (publicationMarker, error) {
	if !validDigest(attemptID) {
		return publicationMarker{}, typedError(CategoryInvalidInput, "publish.attempt", nil)
	}
	directory, err := openRelativeDirectory(p.rootHandle, ".featureproof-publication-markers", false)
	if err != nil {
		return publicationMarker{}, typedError(CategoryInvalidInput, "publish.marker", err)
	}
	defer directory.Close()
	file, err := openRegularInDirectory(directory, attemptID+".json")
	if err != nil {
		return publicationMarker{}, typedError(CategoryInvalidInput, "publish.marker", err)
	}
	defer file.Close()
	links, err := fileLinkCountHandle(file)
	if err != nil || links != 1 {
		return publicationMarker{}, typedError(CategoryInvalidInput, "publish.marker", err)
	}
	raw, err := io.ReadAll(file)
	var marker publicationMarker
	if err != nil || json.Unmarshal(raw, &marker) != nil || marker.AttemptID != attemptID || !validDigest(marker.OutputDigest) || marker.Temp == "" || marker.Target == "" || marker.Replace && !validDigest(marker.PreviousDigest) {
		return publicationMarker{}, typedError(CategoryInternal, "publish.marker", err)
	}
	return marker, nil
}

// resolve accepts only canonical relative paths that remain beneath the publisher root.
func (p *LocalPublisher) resolve(value string) (string, error) {
	if value == "" || filepath.IsAbs(value) || filepath.Clean(value) != value || value == "." || strings.HasPrefix(value, ".."+string(os.PathSeparator)) || strings.Contains(value, "\x00") {
		return "", typedError(CategoryInvalidInput, "publish.path", nil)
	}
	target := filepath.Join(p.root, value)
	relative, err := filepath.Rel(p.root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", typedError(CategoryInvalidInput, "publish.path", nil)
	}
	return target, nil
}
