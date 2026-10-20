package routerdownload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/routermanifest"
	"github.com/yetone/magpie/internal/steady"
)

// Assembly preserves the two original extraction trees and publishes a new
// executable tree. Overlay files replace only files beside the executable.
type Assembly struct {
	Base, Overlay                   routermanifest.Artifact
	BaseDirectory, OverlayDirectory string
	OverlayPrefix                   string
}

type assemblyReceipt struct {
	Version       int             `json:"version"`
	Identity      string          `json:"identity"`
	BaseSHA256    string          `json:"baseSHA256"`
	OverlaySHA256 string          `json:"overlaySHA256"`
	Files         []extractedFile `json:"files"`
}

func (a Assembly) identity() string {
	b, _ := json.Marshal(struct {
		Base, Overlay routermanifest.Artifact
		Prefix        string
	}{a.Base, a.Overlay, a.OverlayPrefix})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func readExtractionReceipt(root string) (receipt, error) {
	var r receipt
	f, err := os.Open(filepath.Join(root, receiptName))
	if err != nil {
		return r, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxReceiptBytes+1))
	if err != nil {
		return r, err
	}
	if len(b) > maxReceiptBytes {
		return r, ErrIntegrity
	}
	err = json.Unmarshal(b, &r)
	return r, err
}

// inputs verifies sources before deriving the complete output file plan. No
// receipt from the output can redefine the expected files or source identities.
func (a Assembly) inputs(ctx context.Context) (assemblyReceipt, map[string]string, error) {
	r := assemblyReceipt{Version: 2, Identity: a.identity(), BaseSHA256: a.Base.SHA256, OverlaySHA256: a.Overlay.SHA256}
	if a.Base.ExpandedBytes <= 0 || a.Overlay.ExpandedBytes <= 0 || a.Base.ExpandedBytes > math.MaxInt64-a.Overlay.ExpandedBytes {
		return r, nil, errors.New("invalid assembly expanded-byte budget")
	}
	if a.OverlayPrefix != "" {
		if _, err := safeEntry(a.OverlayPrefix); err != nil {
			return r, nil, err
		}
	}
	for _, source := range []struct {
		artifact routermanifest.Artifact
		root     string
	}{{a.Base, a.BaseDirectory}, {a.Overlay, a.OverlayDirectory}} {
		if err := VerifyDirectory(ctx, source.artifact, source.root); err != nil {
			return r, nil, err
		}
	}
	base, err := readExtractionReceipt(a.BaseDirectory)
	if err != nil {
		return r, nil, err
	}
	overlay, err := readExtractionReceipt(a.OverlayDirectory)
	if err != nil {
		return r, nil, err
	}
	files, sources := map[string]extractedFile{}, map[string]string{}
	for _, f := range base.Files {
		key := strings.ToLower(f.Path)
		files[key], sources[key] = f, filepath.Join(a.BaseDirectory, filepath.FromSlash(f.Path))
	}
	for _, f := range overlay.Files {
		if path.Base(f.Path) != f.Path {
			return r, nil, errors.New("dependency extraction must be flat")
		}
		target := path.Join(a.OverlayPrefix, f.Path)
		if _, err := safeEntry(target); err != nil {
			return r, nil, err
		}
		key := strings.ToLower(target)
		if previous, ok := files[key]; ok && previous.Path != target {
			return r, nil, errors.New("assembly case collision")
		}
		f.Path = target
		files[key], sources[key] = f, filepath.Join(a.OverlayDirectory, filepath.Base(target))
	}
	if len(files) == 0 || len(files) > maxEntries {
		return r, nil, ErrIntegrity
	}
	for _, f := range files {
		r.Files = append(r.Files, f)
	}
	sort.Slice(r.Files, func(i, j int) bool { return r.Files[i].Path < r.Files[j].Path })
	return r, sources, nil
}

func (a Assembly) Verify(ctx context.Context, destination string) error {
	if err := a.disjoint(destination); err != nil {
		return err
	}
	expected, _, err := a.inputs(ctx)
	if err != nil {
		return err
	}
	if err := checkParents(destination); err != nil {
		return err
	}
	if info, err := os.Lstat(destination); err != nil {
		return err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrIntegrity
	}
	name := filepath.Join(destination, receiptName)
	if err := regularOrMissing(name); err != nil {
		return err
	}
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxReceiptBytes+1))
	if err != nil {
		return err
	}
	var got assemblyReceipt
	if len(b) > maxReceiptBytes || json.Unmarshal(b, &got) != nil || !reflect.DeepEqual(got, expected) {
		return ErrIntegrity
	}
	return verifyTree(ctx, destination, expected.Files, a.Base.ExpandedBytes+a.Overlay.ExpandedBytes)
}

func (a Assembly) Install(ctx context.Context, destination string) error {
	if err := a.disjoint(destination); err != nil {
		return err
	}
	r, sources, err := a.inputs(ctx)
	if err != nil {
		return err
	}
	unlock, err := lockDestination(destination)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := os.Lstat(destination); err == nil {
		return a.Verify(ctx, destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	available, err := freeBytes(filepath.Dir(destination))
	if err != nil {
		return err
	}
	if uint64(a.Base.ExpandedBytes+a.Overlay.ExpandedBytes) > available {
		return ErrDiskSpace
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), "."+filepath.Base(destination)+".assemble-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for _, f := range r.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		source := sources[strings.ToLower(f.Path)]
		if err := regularOrMissing(source); err != nil {
			return err
		}
		if err := checkParents(filepath.Dir(source)); err != nil {
			return err
		}
		in, err := os.Open(source)
		if err != nil {
			return err
		}
		info, err := in.Stat()
		if err != nil {
			in.Close()
			return err
		}
		target := filepath.Join(stage, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			in.Close()
			return err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			in.Close()
			return err
		}
		_, copyErr := io.Copy(out, io.LimitReader(&contextReader{ctx: ctx, reader: in}, f.Size+1))
		err = errors.Join(copyErr, out.Sync(), out.Close(), in.Close())
		if err != nil {
			return err
		}
		if err := verifyExtracted(ctx, stage, f); err != nil {
			return err
		}
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(b) > maxReceiptBytes {
		return ErrIntegrity
	}
	if err := steady.WriteFileContext(ctx, filepath.Join(stage, receiptName), b, 0600); err != nil {
		return err
	}
	if err := a.Verify(ctx, stage); err != nil {
		return err
	}
	return steady.Rename(stage, destination)
}

func (a Assembly) disjoint(destination string) error {
	for _, input := range []string{a.BaseDirectory, a.OverlayDirectory} {
		if !filepath.IsAbs(input) || !filepath.IsAbs(destination) {
			return errors.New("assembly directories must be absolute")
		}
		for _, pair := range [][2]string{{input, destination}, {destination, input}} {
			rel, err := filepath.Rel(pair[0], pair[1])
			if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
				return errors.New("assembly destination overlaps an input")
			}
		}
	}
	return nil
}
