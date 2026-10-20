package routerdownload

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/steady"
)

// SelectedFile pins one regular, nonempty ZIP member and its flat output name.
// The caller owns the fixed allow-list, including redistribution eligibility.
type SelectedFile struct {
	Source string `json:"source"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// ExtractSelected verifies a pinned ZIP and publishes only its pinned members.
// All member headers are checked for unsafe paths, links, aliases and expansion
// bounds; unselected payloads are never decompressed or written. The resulting
// v1 receipt is compatible with VerifyDirectory. Existing trees are also checked
// against files, so editing a local receipt cannot change the accepted pins.
func ExtractSelected(ctx context.Context, a routermanifest.Artifact, archivePath, destinationDir string, files []SelectedFile) error {
	if err := validateSelected(a, files); err != nil {
		return err
	}
	if err := Verify(ctx, a, archivePath); err != nil {
		return err
	}
	unlock, err := lockDestination(destinationDir)
	if err != nil {
		return err
	}
	defer unlock()
	z, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer z.Close()
	members, err := selectedMembers(ctx, a, z.File, files)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(destinationDir); err == nil {
		return verifySelectedDirectory(ctx, a, destinationDir, files)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	available, err := freeBytes(filepath.Dir(destinationDir))
	if err != nil {
		return fmt.Errorf("check selected extraction space: %w", err)
	}
	if uint64(a.ExpandedBytes) > available {
		return ErrDiskSpace
	}
	stage, err := os.MkdirTemp(filepath.Dir(destinationDir), "."+filepath.Base(destinationDir)+".extract-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	e := extractor{ctx: ctx, root: stage, limit: a.ExpandedBytes, seen: make(map[string]bool),
		receipt: receipt{Version: 1, ArtifactSHA256: strings.ToLower(a.SHA256), ArtifactSize: a.Size}}
	for _, pin := range files {
		member := members[pin.Source]
		reader, err := member.Open()
		if err != nil {
			return err
		}
		err = e.entry(pin.Name, pin.Size, member.Mode(), reader)
		closeErr := reader.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
		if !strings.EqualFold(e.receipt.Files[len(e.receipt.Files)-1].SHA256, pin.SHA256) {
			return fmt.Errorf("%w: selected member hash differs: %s", ErrIntegrity, pin.Source)
		}
	}
	b, err := json.Marshal(e.receipt)
	if err != nil {
		return err
	}
	if len(b) > maxReceiptBytes {
		return errors.New("selected extraction receipt too large")
	}
	if err := steady.WriteFileContext(ctx, filepath.Join(stage, receiptName), b, 0o600); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return steady.Rename(stage, destinationDir)
}

func validateSelected(a routermanifest.Artifact, files []SelectedFile) error {
	if a.Format != "zip" || a.ExpandedBytes <= 0 {
		return errors.New("selected extraction requires a ZIP and confirmed expanded-byte bound")
	}
	if len(files) == 0 || len(files) > maxEntries {
		return errors.New("selected extraction requires a bounded nonempty file list")
	}
	sources, names := make(map[string]bool, len(files)), make(map[string]bool, len(files))
	var total int64
	for _, file := range files {
		source, sourceErr := safeEntry(file.Source)
		name, nameErr := safeEntry(file.Name)
		hash, hashErr := hex.DecodeString(file.SHA256)
		if sourceErr != nil || source != file.Source || nameErr != nil || name != file.Name || path.Base(name) != name || hashErr != nil || len(hash) != sha256.Size || file.Size <= 0 || file.Size > a.ExpandedBytes-total {
			return errors.New("selected file requires safe source, flat name, positive size and SHA256")
		}
		sourceKey, nameKey := strings.ToLower(source), strings.ToLower(name)
		if sources[sourceKey] || names[nameKey] {
			return errors.New("duplicate or case-colliding selected source/name")
		}
		sources[sourceKey], names[nameKey] = true, true
		total += file.Size
	}
	return nil
}

func selectedMembers(ctx context.Context, a routermanifest.Artifact, entries []*zip.File, files []SelectedFile) (map[string]*zip.File, error) {
	if len(entries) > maxEntries {
		return nil, errors.New("archive has too many entries")
	}
	all := make(map[string]*zip.File, len(entries))
	var total int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		clean, err := safeEntry(entry.Name)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(clean)
		if _, duplicate := all[key]; duplicate {
			return nil, errors.New("duplicate or case-colliding archive entries")
		}
		mode := entry.Mode()
		if !mode.IsRegular() && !mode.IsDir() {
			return nil, fmt.Errorf("archive links and special files refused: %s", entry.Name)
		}
		if mode.IsDir() && entry.UncompressedSize64 != 0 {
			return nil, errors.New("archive directory contains payload bytes")
		}
		if entry.UncompressedSize64 > uint64(a.ExpandedBytes-total) {
			return nil, errors.New("archive exceeds expanded-byte bound")
		}
		total += int64(entry.UncompressedSize64)
		all[key] = entry
	}
	for key := range all {
		for parent := path.Dir(key); parent != "."; parent = path.Dir(parent) {
			if entry, ok := all[parent]; ok && !entry.Mode().IsDir() {
				return nil, errors.New("archive file/directory path collision")
			}
		}
	}
	selected := make(map[string]*zip.File, len(files))
	for _, pin := range files {
		entry := all[strings.ToLower(pin.Source)]
		if entry == nil || entry.Name != pin.Source || !entry.Mode().IsRegular() || entry.UncompressedSize64 != uint64(pin.Size) {
			return nil, fmt.Errorf("%w: selected member missing or size differs: %s", ErrIntegrity, pin.Source)
		}
		selected[pin.Source] = entry
	}
	return selected, nil
}

// VerifySelectedDirectory also enforces the compiled selection independently
// of the local receipt, including an exact flat file set.
func VerifySelectedDirectory(ctx context.Context, a routermanifest.Artifact, destinationDir string, files []SelectedFile) error {
	if err := validateSelected(a, files); err != nil {
		return err
	}
	return verifySelectedDirectory(ctx, a, destinationDir, files)
}

func verifySelectedDirectory(ctx context.Context, a routermanifest.Artifact, destinationDir string, files []SelectedFile) error {
	if err := VerifyDirectory(ctx, a, destinationDir); err != nil {
		return err
	}
	want := make(map[string]bool, len(files))
	for _, pin := range files {
		want[pin.Name] = true
		if err := verifyExtracted(ctx, destinationDir, extractedFile{Path: pin.Name, Size: pin.Size, SHA256: pin.SHA256}); err != nil {
			return err
		}
	}
	return filepath.WalkDir(destinationDir, func(name string, entry fs.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			return err
		}
		if name == destinationDir {
			return nil
		}
		relative, err := filepath.Rel(destinationDir, name)
		if err != nil {
			return err
		}
		if relative == receiptName {
			return nil
		}
		if entry.IsDir() || !want[relative] {
			return fmt.Errorf("%w: entry outside selected flat file list", ErrIntegrity)
		}
		return nil
	})
}
