// Package routermanifest owns the fixed, reviewable model/runtime catalog.
package routermanifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
)

// Artifact is immutable source metadata. Missing size/hash/URL means blocked.
type Artifact struct {
	ID            string `json:"id"`
	URL           string `json:"url"`
	FileName      string `json:"fileName"`
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
	Format        string `json:"format"`
	ExpandedBytes int64  `json:"expandedBytes"`
}

func (a Artifact) Validate() error {
	u, err := url.Parse(a.URL)
	h, hashErr := hex.DecodeString(a.SHA256)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || hashErr != nil || len(h) != 32 || a.Size <= 0 || a.ID == "" || a.FileName == "" || filepath.Base(a.FileName) != a.FileName || a.FileName == "." || a.FileName == ".." {
		return errors.New("incomplete artifact metadata")
	}
	if a.Format != "file" && a.Format != "zip" && a.Format != "tar.gz" {
		return errors.New("unsupported artifact format")
	}
	if a.Format != "file" && a.ExpandedBytes <= 0 {
		return errors.New("archive expansion budget unknown")
	}
	return nil
}

type Model struct {
	ID                  string   `json:"id"`
	Tier                string   `json:"tier"`
	Revision            string   `json:"revision"`
	Quantization        string   `json:"quantization"`
	License             string   `json:"license"`
	Purpose             string   `json:"purpose"`
	ContextTokens       int      `json:"contextTokens"` // Explicit serving cap, not native maximum.
	NativeContextTokens int      `json:"nativeContextTokens"`
	Artifact            Artifact `json:"artifact"`
	Template            Artifact `json:"template"`
	TemplateRevision    string   `json:"templateRevision"`
	InstallationBytes   int64    `json:"installationBytes"` // Conservative peak reservation.
}
type Runtime struct {
	ID           string        `json:"id"`
	Version      string        `json:"version"`
	Commit       string        `json:"commit"`
	Platform     string        `json:"platform"`
	Architecture string        `json:"architecture"`
	Backend      string        `json:"backend"`
	License      string        `json:"license"`
	Executable   string        `json:"executable"`
	Artifact     Artifact      `json:"artifact"`
	Validation   string        `json:"validation"`
	Dependencies *Dependencies `json:"dependencies,omitempty"`
}

func (m Model) Fingerprint(r Runtime) string {
	b, _ := json.Marshal(struct {
		Model   Model
		Runtime Runtime
	}{m, r})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type Catalog struct {
	SchemaVersion int       `json:"schemaVersion"`
	Models        []Model   `json:"models"`
	Runtimes      []Runtime `json:"runtimes"`
	VerifiedAt    string    `json:"verifiedAt"`
	Sources       []string  `json:"sources"`
}

func (c Catalog) Model(id string) (Model, bool) {
	for _, m := range c.Models {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}
func (c Catalog) Runtime(id string) (Runtime, bool) {
	for _, r := range c.Runtimes {
		if r.ID == id {
			return r, true
		}
	}
	return Runtime{}, false
}

// Fixed returns a detached catalog. Mirrors are trusted only after exact hash
// verification against the recorded official repository metadata.
func Fixed() Catalog {
	const smallRev = "1f7fa33b1245c14730da39e125714ad3a327901b"
	const largeRev = "9826e0be84e6e6e8b9668abc91421109a1df1e2d"
	c := Catalog{SchemaVersion: 1, VerifiedAt: "2026-10-17", Sources: []string{
		"https://api.github.com/repos/ggml-org/llama.cpp/releases/tags/b10828",
		"https://hf-mirror.com/api/models/XHToken/Spark-X2.5-1.7B-GGUF?blobs=true",
		"https://hf-mirror.com/api/models/XHToken/Spark-X2.5-4B-GGUF?blobs=true",
	}}
	for _, v := range []struct {
		tier, rev, base, hash string
		size                  int64
	}{
		{"1.7B", smallRev, "14d6e83c13c7add2b62a7c39b2131f4ed1cddcf8", "902bde2522394954ac17821b3e5fd0df02defbc6944f122253f2580acf0503f4", 1107457856},
		{"4B", largeRev, "0bcb35678590218655dff3765b9e61c83b35e9c4", "adfcfa19a4ed6a5985da8bf565fe15f8e1a7e131d79bae2d19d48d1c40109428", 2600224352},
	} {
		name := "Spark-X2.5-" + v.tier
		file := name + "-Q4_K_M.gguf"
		c.Models = append(c.Models, Model{ID: "spark-x2.5-" + v.tier, Tier: v.tier, Revision: v.rev, Quantization: "Q4_K_M", License: "Apache-2.0", Purpose: "public_execution_validation_only", ContextTokens: 8192, NativeContextTokens: 1048576,
			Artifact: Artifact{ID: name + "-" + v.rev, URL: "https://hf-mirror.com/XHToken/" + name + "-GGUF/resolve/" + v.rev + "/" + file, FileName: file, SHA256: v.hash, Size: v.size, Format: "file"},
			Template: Artifact{ID: name + "-template-" + v.base, URL: "https://hf-mirror.com/XHToken/" + name + "/resolve/" + v.base + "/chat_template.jinja", FileName: "chat_template.jinja", SHA256: "705345721970da40e6aa56db548a46ecd1d8d94a5418808fa180ac19ee77a9ba", Size: 4648, Format: "file"}, TemplateRevision: v.base,
			// Two model copies permit interrupted replacement; 512MiB reserves
			// archive/extraction and 64MiB filesystem/metadata margin.
			InstallationBytes: 2*v.size + (576 << 20),
		})
	}
	for _, v := range []struct {
		id, platform, arch, backend, asset, hash, exe string
		size                                          int64
	}{
		{"windows-amd64-cpu", "windows", "amd64", "cpu", "llama-b10828-bin-win-cpu-x64.zip", "221ef3c3d4f649e426708942fbb9dd369bc7ab7aa0153543323195459522b8cc", "llama-server.exe", 18414076},
		{"darwin-arm64-metal", "darwin", "arm64", "metal", "llama-b10828-bin-macos-arm64.tar.gz", "a9748e059515dfe853940f7cfc0ac7ac7af33901c773a64569b9435e27b0f6cb", "llama-server", 11133769},
		{"linux-amd64-cpu", "linux", "amd64", "cpu", "llama-b10828-bin-ubuntu-x64.tar.gz", "75085018390cb7a14d0048ad68fd2d7190a9f4a82cfdc531f1fbff156105929e", "llama-server", 16748330},
	} {
		format := "tar.gz"
		if v.platform == "windows" {
			format = "zip"
		}
		c.Runtimes = append(c.Runtimes, Runtime{ID: v.id, Version: "b10828", Commit: "3ad1ba7336986d98592d3e28cafd1a406715351f", Platform: v.platform, Architecture: v.arch, Backend: v.backend, License: "MIT", Executable: v.exe, Validation: "artifact_metadata_verified_native_model_pending", Artifact: Artifact{ID: v.id + "-b10828", URL: "https://github.com/ggml-org/llama.cpp/releases/download/b10828/" + v.asset, FileName: v.asset, SHA256: v.hash, Size: v.size, Format: format, ExpandedBytes: 256 << 20}})
		if v.platform == "windows" {
			c.Runtimes[len(c.Runtimes)-1].Dependencies = windowsDependencies()
		}
	}
	return c
}
