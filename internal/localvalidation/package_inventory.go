package localvalidation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
)

var safePathSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func canonicalArtifactPath(name string) bool {
	if name == "" || len(name) > 512 || strings.ContainsAny(name, `\:`) || strings.HasPrefix(name, "/") || path.Clean(name) != name {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if !safePathSegment.MatchString(part) || strings.HasSuffix(part, ".") {
			return false
		}
	}
	return true
}

// inventoryPackage walks through an os.Root capability and hashes exact bytes.
// It rejects links and special files before reading, and never follows a path
// outside the root even if a concurrent writer replaces an entry.
func inventoryPackage(ctx context.Context, dir string) ([]PackageArtifact, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, &PackageError{Category: "invalid_path", Path: ""}
	}
	defer root.Close()
	entries := []PackageArtifact{}
	var total int64
	var walk func(string, int) error
	walk = func(prefix string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 8 {
			return &PackageError{Category: "limit_exceeded", Path: prefix}
		}
		where := "."
		if prefix != "" {
			where = prefix
		}
		folder, err := root.Open(where)
		if err != nil {
			return &PackageError{Category: "invalid_path", Path: prefix}
		}
		names, err := folder.Readdirnames(-1)
		folder.Close()
		if err != nil {
			return &PackageError{Category: "invalid_path", Path: prefix}
		}
		sort.Strings(names)
		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return err
			}
			rel := name
			if prefix != "" {
				rel = prefix + "/" + name
			}
			if !canonicalArtifactPath(rel) {
				return &PackageError{Category: "invalid_path", Path: ""}
			}
			info, err := root.Lstat(rel)
			if err != nil {
				return &PackageError{Category: "invalid_path", Path: rel}
			}
			if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeIrregular != 0 {
				return &PackageError{Category: "invalid_path", Path: rel}
			}
			if info.IsDir() {
				if rel != "evidence" {
					return &PackageError{Category: "extra_artifact", Path: rel}
				}
				if err := walk(rel, depth+1); err != nil {
					return err
				}
				continue
			}
			if !info.Mode().IsRegular() {
				return &PackageError{Category: "invalid_path", Path: rel}
			}
			if rel == "manifest.json" {
				continue
			}
			if info.Size() > maxArtifactBytes || len(entries) >= maxPackageEntries || total+info.Size() > maxPackageBytes {
				return &PackageError{Category: "limit_exceeded", Path: rel}
			}
			artifact, err := hashPackageArtifact(ctx, root, rel, info)
			if err != nil {
				return err
			}
			entries = append(entries, artifact)
			total += artifact.SizeBytes
		}
		return nil
	}
	if err := walk("", 0); err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func hashPackageArtifact(ctx context.Context, root *os.Root, rel string, before os.FileInfo) (PackageArtifact, error) {
	file, err := root.Open(rel)
	if err != nil {
		return PackageArtifact{}, &PackageError{Category: "invalid_path", Path: rel}
	}
	defer file.Close()
	if !singleLink(file) {
		return PackageArtifact{}, &PackageError{Category: "invalid_path", Path: rel}
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return PackageArtifact{}, &PackageError{Category: "invalid_path", Path: rel}
	}
	h := sha256.New()
	buffer := make([]byte, 32<<10)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return PackageArtifact{}, err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			size += int64(n)
			if size > maxArtifactBytes {
				return PackageArtifact{}, &PackageError{Category: "limit_exceeded", Path: rel}
			}
			_, _ = h.Write(buffer[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return PackageArtifact{}, &PackageError{Category: "invalid_path", Path: rel}
		}
	}
	after, err := root.Lstat(rel)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(opened, after) || before.Size() != size || after.Size() != size {
		return PackageArtifact{}, &PackageError{Category: "invalid_path", Path: rel}
	}
	return PackageArtifact{Path: rel, SizeBytes: size, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}
