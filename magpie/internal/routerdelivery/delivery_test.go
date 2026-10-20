package routerdelivery

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"debug/pe"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func executable(machine uint16) []byte {
	b := make([]byte, 128+24)
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[60:], 128)
	copy(b[128:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(b[132:], machine)
	binary.LittleEndian.PutUint16(b[150:], pe.IMAGE_FILE_EXECUTABLE_IMAGE)
	return b
}

type zipItem struct {
	name string
	data []byte
	link bool
}

func fixture(t *testing.T, change func(*Manifest, *[]zipItem)) InstallOptions {
	t.Helper()
	dir := t.TempDir()
	items := []zipItem{{"api-router.exe", executable(pe.IMAGE_FILE_MACHINE_AMD64), false}, {"README-windows.md", []byte("user guide"), false}, {"install-windows.ps1", []byte("# explicit installer"), false}, {"uninstall-windows.ps1", []byte("# explicit uninstaller"), false}}
	m := Manifest{SchemaVersion: 1, Product: "api-router", Version: "0.1.0-round4", OS: "windows", Architecture: "amd64"}
	for _, item := range items {
		m.Files = append(m.Files, pin(item.name, item.data))
	}
	if change != nil {
		change(&m, &items)
	}
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for _, item := range items {
		h := &zip.FileHeader{Name: item.name, Method: zip.Deflate}
		if item.link {
			h.SetMode(os.ModeSymlink | 0o700)
		} else {
			h.SetMode(0o700)
		}
		w, err := z.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(item.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	m.Package = pin("api-router-0.1.0-round4-windows-amd64.zip", buf.Bytes())
	opts := InstallOptions{ManifestPath: filepath.Join(dir, "fixed.manifest.json"), PackagePath: filepath.Join(dir, m.Package.Name), InstallDir: filepath.Join(dir, "用户 program files", "API-router")}
	write(t, opts.PackagePath, buf.Bytes())
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	write(t, opts.ManifestPath, b)
	return opts
}

func pin(name string, b []byte) File {
	h := sha256.Sum256(b)
	return File{Name: name, Size: int64(len(b)), SHA256: hex.EncodeToString(h[:])}
}
func write(t *testing.T, name string, b []byte) {
	t.Helper()
	if err := os.WriteFile(name, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInstallLocalFixedPackageAndRepeat(t *testing.T) {
	opts := fixture(t, nil)
	first, err := Install(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.Reused || first.Version != "0.1.0-round4" {
		t.Fatalf("unexpected first result: %+v", first)
	}
	before, err := os.ReadFile(filepath.Join(opts.InstallDir, ReceiptName))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Install(context.Background(), opts)
	if err != nil || !second.Reused {
		t.Fatalf("repeat=%+v, %v", second, err)
	}
	after, _ := os.ReadFile(filepath.Join(opts.InstallDir, ReceiptName))
	if !bytes.Equal(before, after) {
		t.Fatal("repeat rewrote install identity")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(opts.InstallDir), "models")); !os.IsNotExist(err) {
		t.Fatal("installation touched models")
	}
}

func TestDamagedSameVersionAndInvalidPackagePreserveInstallation(t *testing.T) {
	opts := fixture(t, nil)
	if _, err := Install(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(opts.InstallDir, "api-router.exe")
	original, _ := os.ReadFile(exe)
	packageBytes, _ := os.ReadFile(opts.PackagePath)
	packageBytes[0] ^= 1
	write(t, opts.PackagePath, packageBytes)
	if _, err := Install(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "SHA256") {
		t.Fatalf("corrupt package=%v", err)
	}
	still, _ := os.ReadFile(exe)
	if !bytes.Equal(original, still) {
		t.Fatal("bad package changed installed executable")
	}
	packageBytes[0] ^= 1
	write(t, opts.PackagePath, packageBytes)
	write(t, exe, []byte("user diagnostic damage"))
	if _, err := Install(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("damaged repeat=%v", err)
	}
	still, _ = os.ReadFile(exe)
	if string(still) != "user diagnostic damage" {
		t.Fatal("damaged install was silently overwritten")
	}
}

func TestRejectUnsafeAndIncompletePackagesBeforePublication(t *testing.T) {
	cases := map[string]func(*Manifest, *[]zipItem){
		"traversal":          func(_ *Manifest, v *[]zipItem) { (*v)[1].name = "../escaped.txt" },
		"windows alias":      func(_ *Manifest, v *[]zipItem) { (*v)[1].name = "README-windows.md:evil" },
		"backslash":          func(_ *Manifest, v *[]zipItem) { (*v)[1].name = `..\escaped.txt` },
		"duplicate":          func(_ *Manifest, v *[]zipItem) { (*v)[1].name = "api-router.exe" },
		"link":               func(_ *Manifest, v *[]zipItem) { (*v)[1].link = true },
		"missing executable": func(_ *Manifest, v *[]zipItem) { *v = (*v)[1:] },
		"wrong file hash":    func(_ *Manifest, v *[]zipItem) { (*v)[1].data = []byte("USER guide") },
		"wrong platform":     func(m *Manifest, _ *[]zipItem) { m.OS = "linux" },
		"wrong architecture": func(m *Manifest, _ *[]zipItem) { m.Architecture = "arm64" },
		"wrong PE architecture": func(m *Manifest, v *[]zipItem) {
			(*v)[0].data = executable(pe.IMAGE_FILE_MACHINE_I386)
			m.Files[0] = pin("api-router.exe", (*v)[0].data)
		},
		"not PE": func(m *Manifest, v *[]zipItem) {
			(*v)[0].data = []byte("not a Windows executable")
			m.Files[0] = pin("api-router.exe", (*v)[0].data)
		},
		"wrong PE type": func(m *Manifest, v *[]zipItem) {
			binary.LittleEndian.PutUint16((*v)[0].data[150:], pe.IMAGE_FILE_EXECUTABLE_IMAGE|pe.IMAGE_FILE_DLL)
			m.Files[0] = pin("api-router.exe", (*v)[0].data)
		},
		"unpinned version": func(m *Manifest, _ *[]zipItem) { m.Version = "latest" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			opts := fixture(t, change)
			if _, err := Install(context.Background(), opts); err == nil {
				t.Fatal("unsafe package installed successfully")
			}
			if _, err := os.Lstat(opts.InstallDir); !os.IsNotExist(err) {
				t.Fatalf("invalid package published a directory: %v", err)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(opts.InstallDir), "escaped.txt")); !os.IsNotExist(err) {
				t.Fatal("archive escaped installation")
			}
			entries, _ := os.ReadDir(filepath.Dir(opts.InstallDir))
			for _, entry := range entries {
				if strings.Contains(entry.Name(), "stage") || strings.Contains(entry.Name(), "lock") {
					t.Fatalf("failed install left %s", entry.Name())
				}
			}
		})
	}
}

func TestInstallRefusesDataOverlapUnknownDirectoryAndOtherVersion(t *testing.T) {
	t.Run("data overlap", func(t *testing.T) {
		opts := fixture(t, nil)
		opts.ProtectedDirs = []string{filepath.Dir(opts.InstallDir)}
		if _, err := Install(context.Background(), opts); err == nil {
			t.Fatal("installed into data")
		}
	})
	t.Run("unknown directory", func(t *testing.T) {
		opts := fixture(t, nil)
		if err := os.MkdirAll(opts.InstallDir, 0o700); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(opts.InstallDir, "user.txt"), []byte("keep"))
		if _, err := Install(context.Background(), opts); err == nil {
			t.Fatal("adopted unknown directory")
		}
		b, _ := os.ReadFile(filepath.Join(opts.InstallDir, "user.txt"))
		if string(b) != "keep" {
			t.Fatal("lost unknown file")
		}
	})
	t.Run("another version", func(t *testing.T) {
		opts := fixture(t, nil)
		if _, err := Install(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
		m, err := ReadManifest(opts.ManifestPath)
		if err != nil {
			t.Fatal(err)
		}
		m.Version = "0.2.0"
		b, _ := json.Marshal(m)
		write(t, opts.ManifestPath, b)
		if _, err := Install(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "another version") {
			t.Fatalf("version overwrite=%v", err)
		}
	})
}

func TestUninstallPreviewAndManagedRemovalRetainRecoveryAndUserFiles(t *testing.T) {
	opts := fixture(t, nil)
	if _, err := Install(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(opts.InstallDir, "personal-note.txt")
	write(t, unknown, []byte("keep"))
	data := filepath.Join(filepath.Dir(opts.InstallDir), "user-data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(data, "config.json"), []byte("private-config"))
	p, err := PreviewUninstall(opts.InstallDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Remove) != 3 || len(p.Retain) != 2 || !p.UserDataPreserved {
		t.Fatalf("bad preview: %+v", p)
	}
	if _, err := os.Stat(p.Remove[0]); err != nil {
		t.Fatal("preview changed files")
	}
	r, err := Uninstall(context.Background(), opts.InstallDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecoveryTool == "" {
		t.Fatal("lost recovery tool")
	}
	for _, name := range append(p.Retain, unknown, filepath.Join(data, "config.json")) {
		if _, err := os.Stat(name); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range p.Remove {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("managed file remains: %s", name)
		}
	}
	if _, err := Uninstall(context.Background(), opts.InstallDir, true); err != nil {
		t.Fatalf("repeat uninstall: %v", err)
	}
	if _, err := Uninstall(context.Background(), opts.InstallDir, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatal("uninstall deleted unmanaged file")
	}
}

func TestInstallAndUninstallRejectLinks(t *testing.T) {
	opts := fixture(t, nil)
	real := filepath.Join(t.TempDir(), "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(real), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("native symlink privilege unavailable: %v", err)
	}
	opts.InstallDir = filepath.Join(link, "product")
	if _, err := Install(context.Background(), opts); err == nil {
		t.Fatal("followed install ancestor link")
	}
	if _, err := PreviewUninstall(opts.InstallDir, true); err == nil {
		t.Fatal("followed uninstall ancestor link")
	}
}

func TestUninstallRefusesDamagedRecoveryToolBeforeDeletingFiles(t *testing.T) {
	opts := fixture(t, nil)
	if _, err := Install(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(opts.InstallDir, "api-router.exe"), []byte("damaged recovery executable"))
	if _, err := Uninstall(context.Background(), opts.InstallDir, true); err == nil || !strings.Contains(err.Error(), "recovery executable is damaged") {
		t.Fatalf("damaged recovery tool=%v", err)
	}
	if _, err := os.Stat(filepath.Join(opts.InstallDir, "README-windows.md")); err != nil {
		t.Fatal("uninstall removed files before checking recovery tool")
	}
}

func TestCancelledInstallDoesNotPublish(t *testing.T) {
	opts := fixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Install(ctx, opts); err == nil {
		t.Fatal("canceled install succeeded")
	}
	if _, err := os.Stat(opts.InstallDir); !os.IsNotExist(err) {
		t.Fatal("canceled install published directory")
	}
}

func TestMetadataPathCannotHideLinkWithParentComponent(t *testing.T) {
	opts := fixture(t, nil)
	other := t.TempDir()
	nested := filepath.Join(other, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(nested, filepath.Join(filepath.Dir(opts.ManifestPath), "linked")); err != nil {
		t.Skipf("native symlink privilege unavailable: %v", err)
	}
	manifest, err := os.ReadFile(opts.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(other, filepath.Base(opts.ManifestPath)), manifest)
	// Do not use filepath.Join: its lexical cleaning would erase the case.
	opts.ManifestPath = filepath.Dir(opts.ManifestPath) + string(os.PathSeparator) + "linked" + string(os.PathSeparator) + ".." + string(os.PathSeparator) + filepath.Base(opts.ManifestPath)
	if _, err := Install(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "parent components") {
		t.Fatalf("raw parent-component path=%v", err)
	}
}
