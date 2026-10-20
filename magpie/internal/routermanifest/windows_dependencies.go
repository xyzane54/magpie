package routermanifest

import (
	"encoding/hex"
	"errors"
	"path"
	"strconv"
	"strings"
)

// Dependencies pins release-only app-local files from an official Microsoft
// Visual Studio payload. The payload is downloaded only by explicit install.
type Dependencies struct {
	ID           string           `json:"id"`
	Version      string           `json:"version"`
	Architecture string           `json:"architecture"`
	Artifact     Artifact         `json:"artifact"`
	Files        []DependencyFile `json:"files"`
	LicenseURL   string           `json:"licenseURL"`
}

type DependencyFile struct {
	Source  string `json:"source"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Version string `json:"version"`
}

func (d Dependencies) Validate() error {
	if err := d.Artifact.Validate(); err != nil {
		return err
	}
	if !safeDependencyComponent(d.ID) || !safeDependencyComponent(d.Artifact.ID) || !validDependencyVersion(d.Version) || d.Architecture != "amd64" || d.Artifact.Format != "zip" || len(d.Files) == 0 || len(d.Files) > 32 {
		return errors.New("invalid Windows dependency metadata")
	}
	seen := make(map[string]bool)
	sources := make(map[string]bool)
	var total int64
	for _, f := range d.Files {
		h, err := hex.DecodeString(f.SHA256)
		key := strings.ToLower(f.Name)
		if err != nil || len(h) != 32 || f.Size <= 0 || f.Size > 20<<20 || f.Size > d.Artifact.ExpandedBytes-total || f.Version != d.Version || !strings.HasSuffix(key, ".dll") || !safeDependencyComponent(f.Name) || f.Source == "" || path.Clean(f.Source) != f.Source || strings.HasPrefix(f.Source, "/") || strings.Contains(strings.ToLower(f.Source), "debug_nonredist") || seen[key] || sources[strings.ToLower(f.Source)] {
			return errors.New("invalid Windows dependency file metadata")
		}
		for _, part := range strings.Split(f.Source, "/") {
			if !safeDependencyComponent(part) {
				return errors.New("unsafe dependency source path")
			}
		}
		seen[key] = true
		sources[strings.ToLower(f.Source)] = true
		total += f.Size
	}
	return nil
}

func validDependencyVersion(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 4 {
		return false
	}
	for _, part := range parts {
		if _, err := strconv.ParseUint(part, 10, 16); err != nil {
			return false
		}
	}
	return true
}

func safeDependencyComponent(name string) bool {
	if name == "" || name == "." || name == ".." || path.Base(name) != name || strings.TrimRight(name, ". ") != name || strings.ContainsAny(name, "\\/:*?\"<>|\x00") {
		return false
	}
	for _, r := range name {
		if r < 32 {
			return false
		}
	}
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" {
		return false
	}
	return !(len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '0' && stem[3] <= '9')
}

func windowsDependencies() *Dependencies {
	const version = "14.50.35719.0"
	const prefix = "Contents/VC/Redist/MSVC/14.50.35710/x64/Microsoft.VC145.CRT/"
	d := &Dependencies{ID: "msvc-x64-14.50.35719.0", Version: version, Architecture: "amd64",
		LicenseURL: "https://learn.microsoft.com/en-us/visualstudio/releases/2026/redistribution",
		Artifact: Artifact{ID: "microsoft-vc145-crt-x64-14.50.35719", FileName: "Microsoft.VC.14.50.18.0.CRT.Redist.X64.base.vsix",
			URL:    "https://download.visualstudio.microsoft.com/download/pr/a424e95b-20f6-4af6-844f-9d9a806080a7/a4ab48822362df6e478eb4ed33bf825a07a6faacd6879c1e3d59929cd38b984b/Microsoft.VC.14.50.18.0.CRT.Redist.X64.base.vsix",
			SHA256: "a4ab48822362df6e478eb4ed33bf825a07a6faacd6879c1e3d59929cd38b984b", Size: 3313422, Format: "zip", ExpandedBytes: 16 << 20}}
	for _, f := range []struct {
		name, hash string
		size       int64
	}{
		{"msvcp140.dll", "def46aa6a8f72f27bafac0c43334419486a4d1dcdb6c479a8ef7034b3e1fa4cb", 553552},
		{"vcruntime140.dll", "184146852727a9db4eea06178716bec3cdbb1015c911f6b0f915b184ad7775b2", 123472},
		{"vcruntime140_1.dll", "e6bfb3662ab4b1969a73441dbe35c96d51441b6bff8cf1fe7430bd5b246ca605", 47264},
		{"msvcp140_1.dll", "2dd670f874562fbdca5b022df1943d70a57ba91fde559280e3a1daebe4db2380", 35488},
		{"msvcp140_atomic_wait.dll", "e7963645e0d1db08e300614d4c5fa7194bd8173e9ab7a5558859e6b232ed3241", 48800},
		{"concrt140.dll", "b2faf3b85b23c840b654e57d5497a0ad31acd02fb01856cad4725a1715d5f78e", 321696},
	} {
		d.Files = append(d.Files, DependencyFile{Source: prefix + f.name, Name: f.name, Size: f.size, SHA256: f.hash, Version: version})
	}
	return d
}
