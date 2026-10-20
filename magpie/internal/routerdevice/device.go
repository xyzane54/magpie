// Package routerdevice observes device capabilities and recommends a model tier.
// It never installs, enables or starts a model, or changes Router configuration.
package routerdevice

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
)

// GiB is the unit used by the product's RAM and VRAM thresholds.
const (
	GiB     uint64 = 1 << 30
	HalfGiB uint64 = GiB / 2

	MinTotalMemoryBytes       uint64 = 8 * GiB
	MainstreamMemoryBytes     uint64 = 16 * GiB
	HighMemoryBytes           uint64 = 24 * GiB
	MinDiscreteVRAMBytes      uint64 = 6 * GiB
	MinAvailableMemory1_7B    uint64 = 5 * HalfGiB  // 2.5 GiB
	MinAvailableMemory4BAllow uint64 = 9 * HalfGiB  // 4.5 GiB
	MinAvailableMemory4BRec   uint64 = 13 * HalfGiB // 6.5 GiB

	MinLogicalCPUs1_7B int = 4
	MinLogicalCPUs4B   int = 8
)

// Fact distinguishes a measured zero (for example, a full disk) from unknown.
// An absent value always means unknown; Reason explains a failed observation.
type Fact[T any] struct {
	Value  *T     `json:"value"`
	Reason string `json:"reason,omitempty"`
}

func Known[T any](v T) Fact[T]             { return Fact[T]{Value: &v} }
func Unknown[T any](reason string) Fact[T] { return Fact[T]{Reason: reason} }

type Availability string

const (
	UnknownAvailability Availability = "unknown"
	Available           Availability = "available"
	Unavailable         Availability = "unavailable"
)

type System struct {
	OS           string       `json:"os"`
	Version      Fact[string] `json:"version"`
	Architecture Fact[string] `json:"architecture"` // Go names: amd64, arm64.
	CPU          Fact[string] `json:"cpu"`
	LogicalCPUs  Fact[int]    `json:"logicalCPUs"`
}

type Adapter struct {
	ID          string       `json:"id"` // Stable within a snapshot; used by GPU backends.
	Name        string       `json:"name"`
	Discrete    Availability `json:"discrete"`
	MemoryBytes Fact[uint64] `json:"memoryBytes"` // Dedicated video memory only.
}

type BackendKind string

const (
	CPU    BackendKind = "cpu"
	CUDA   BackendKind = "cuda"
	Metal  BackendKind = "metal"
	Vulkan BackendKind = "vulkan"
)

// Backend describes host capability, not an installed llama-server or a tested
// model. Unknown accelerator support must not be inferred from a driver file.
type Backend struct {
	Kind      BackendKind  `json:"kind"`
	State     Availability `json:"state"`
	AdapterID string       `json:"adapterId,omitempty"`
	Reason    string       `json:"reason,omitempty"`
}

type Snapshot struct {
	System               Fact[System]    `json:"system"`
	MemoryBytes          Fact[uint64]    `json:"memoryBytes"`          // Physical RAM, not free RAM.
	AvailableMemoryBytes Fact[uint64]    `json:"availableMemoryBytes"` // Reclaimable/free physical RAM at probe time.
	Adapters             Fact[[]Adapter] `json:"adapters"`             // Known empty means no adapters.
	DiskFreeBytes        Fact[uint64]    `json:"diskFreeBytes"`        // Available to this user.
	Backends             Fact[[]Backend] `json:"backends"`
}

// Provider must be read-only and safe for repeated calls. Probe receives the
// explicit model destination, never a path derived from a host application's
// config. A returned error invalidates the entire snapshot, including values
// returned alongside it. Partial failures belong in individual unknown Facts.
type Provider interface {
	Probe(context.Context, string) (Snapshot, error)
}

type ProviderFunc func(context.Context, string) (Snapshot, error)

func (f ProviderFunc) Probe(ctx context.Context, path string) (Snapshot, error) {
	return f(ctx, path)
}

type Tier string

const (
	TierUnknown Tier = "unknown"
	TierNone    Tier = "none"
	Tier1_7B    Tier = "1.7B"
	Tier4B      Tier = "4B"
)

type Recommendation struct {
	Tier    Tier   `json:"tier"`
	MaxTier Tier   `json:"maxTier,omitempty"`
	Reason  string `json:"reason"`
}

// EffectiveMaxTier returns MaxTier when present, falling back to Tier for
// callers that construct Recommendation literals without MaxTier.
func (r Recommendation) EffectiveMaxTier() Tier {
	if r.MaxTier != "" {
		return r.MaxTier
	}
	return r.Tier
}

// Allows reports whether target is within the hardware's maximum allowed tier.
func (r Recommendation) Allows(target Tier) bool {
	switch r.EffectiveMaxTier() {
	case Tier4B:
		return target == Tier1_7B || target == Tier4B
	case Tier1_7B:
		return target == Tier1_7B
	default:
		return false
	}
}

type CheckState string

const (
	CheckUnknown CheckState = "unknown"
	CheckPassed  CheckState = "passed"
	CheckFailed  CheckState = "failed"
)

type Check struct {
	State   CheckState `json:"state"`
	Reasons []string   `json:"reasons"`
}

// Requirements is supplied by T3.2's fixed artifact manifest. Disk space includes
// the model, runtime and installation overhead on this volume. Zero is unknown,
// never "no disk needed". Only the recommended tier's requirement is consulted
// for the primary check; MaxTier is capped when a higher tier lacks disk space.
type Requirements struct {
	Tier1_7BBytes uint64
	Tier4BBytes   uint64
}

// ModelState is owned by the future installer/runtime. Detection neither reads
// nor writes it. Zero values remain unknown, never installed or enabled.
type ModelState struct {
	Installed Fact[bool] `json:"installed"`
	Enabled   Fact[bool] `json:"enabled"`
}

type Report struct {
	Device         Snapshot       `json:"device"`
	Recommendation Recommendation `json:"recommendation"`
	Detection      Check          `json:"detection"`
}

// Detector has no cache or background work. Callers explicitly request a new
// observation, then pass its report to T3.2/T3.4 without changing ModelState.
type Detector struct{ provider Provider }

func New(provider Provider) *Detector { return &Detector{provider: provider} }

func (d *Detector) Detect(ctx context.Context, modelPath string, requirements Requirements) Report {
	var s Snapshot
	reason := ""
	failed := true
	switch {
	case ctx.Err() != nil:
		reason = ctx.Err().Error()
	case !filepath.IsAbs(modelPath):
		reason = "model destination must be absolute"
	case d == nil || d.provider == nil:
		reason = "provider unavailable"
	default:
		var err error
		s, err = d.provider.Probe(ctx, modelPath)
		failed = err != nil
		if err != nil {
			reason = err.Error()
		}
		if ctx.Err() != nil {
			failed = true
			reason = ctx.Err().Error()
		}
	}
	if failed {
		if reason == "" {
			reason = "device probe failed"
		}
		s = Snapshot{
			System: Unknown[System](reason), MemoryBytes: Unknown[uint64](reason),
			AvailableMemoryBytes: Unknown[uint64](reason),
			Adapters:             Unknown[[]Adapter](reason), DiskFreeBytes: Unknown[uint64](reason), Backends: Unknown[[]Backend](reason),
		}
	}
	return Evaluate(s, requirements)
}

// Recommend is a pure hardware classification. Detection is a separate check:
// a tier recommendation alone does not mean the device or model is ready.
func Recommend(s Snapshot) Recommendation {
	if s.MemoryBytes.Value == nil || *s.MemoryBytes.Value == 0 {
		return Recommendation{Tier: TierUnknown, MaxTier: TierUnknown, Reason: "memory_unknown"}
	}
	ram := *s.MemoryBytes.Value
	if ram < MinTotalMemoryBytes {
		return Recommendation{Tier: TierNone, MaxTier: TierNone, Reason: "memory_below_8_gib"}
	}
	hasAvail := s.AvailableMemoryBytes.Value != nil
	var avail uint64
	if hasAvail {
		avail = *s.AvailableMemoryBytes.Value
		if avail < MinAvailableMemory1_7B {
			return Recommendation{Tier: TierNone, MaxTier: TierNone, Reason: "available_memory_below_2_5_gib"}
		}
	}
	gpuAccel := hasConfirmedGPUAccelerator(s)
	hasCPUs := s.System.Value != nil && s.System.Value.LogicalCPUs.Value != nil
	var cpus int
	if hasCPUs {
		cpus = *s.System.Value.LogicalCPUs.Value
		if cpus < MinLogicalCPUs1_7B && !gpuAccel {
			return Recommendation{Tier: TierNone, MaxTier: TierNone, Reason: "cpu_threads_below_4"}
		}
	}
	cpuSupports4B := !hasCPUs || cpus >= MinLogicalCPUs4B || gpuAccel
	availSupports4BAllow := !hasAvail || avail >= MinAvailableMemory4BAllow
	availSupports4BRec := !hasAvail || avail >= MinAvailableMemory4BRec

	if ram >= HighMemoryBytes && availSupports4BRec && cpuSupports4B {
		return Recommendation{Tier: Tier4B, MaxTier: Tier4B, Reason: "memory_at_least_24_gib"}
	}
	if hasQualifyingDiscreteVRAM(s, ram < MainstreamMemoryBytes) && availSupports4BAllow {
		return Recommendation{Tier: Tier4B, MaxTier: Tier4B, Reason: "discrete_vram_at_least_6_gib"}
	}
	if ram >= MainstreamMemoryBytes {
		if availSupports4BAllow && cpuSupports4B {
			return Recommendation{Tier: Tier1_7B, MaxTier: Tier4B, Reason: "memory_16_gib_prefer_1_7b_allow_4b"}
		}
		return Recommendation{Tier: Tier1_7B, MaxTier: Tier1_7B, Reason: "memory_16_gib_limited_headroom"}
	}
	return Recommendation{Tier: Tier1_7B, MaxTier: Tier1_7B, Reason: "memory_at_least_8_gib"}
}

func hasConfirmedGPUAccelerator(s Snapshot) bool {
	if s.Backends.Value == nil {
		return false
	}
	isDarwin := s.System.Value != nil && s.System.Value.OS == "darwin"
	for _, b := range *s.Backends.Value {
		if b.State != Available {
			continue
		}
		if isDarwin && b.Kind == Metal {
			return true
		}
		if !isDarwin && (b.Kind == CUDA || b.Kind == Vulkan) {
			return true
		}
	}
	return false
}

func hasQualifyingDiscreteVRAM(s Snapshot, allowUnconfirmedBackend bool) bool {
	if s.Adapters.Value == nil {
		return false
	}
	for _, a := range *s.Adapters.Value {
		if a.Discrete != Available || a.MemoryBytes.Value == nil || *a.MemoryBytes.Value < MinDiscreteVRAMBytes {
			continue
		}
		if allowUnconfirmedBackend {
			return true
		}
		if s.Backends.Value != nil {
			for _, b := range *s.Backends.Value {
				if (b.Kind == CUDA || b.Kind == Vulkan) && b.State == Available && a.ID != "" && a.ID == b.AdapterID {
					return true
				}
			}
		}
	}
	return false
}

func Evaluate(s Snapshot, requirements Requirements) Report {
	s = cloneSnapshot(s)
	r := Report{Device: s, Recommendation: Recommend(s), Detection: Check{State: CheckPassed, Reasons: []string{}}}
	add := func(state CheckState, reason string) {
		r.Detection.Reasons = append(r.Detection.Reasons, reason)
		if state == CheckFailed || r.Detection.State == CheckPassed {
			r.Detection.State = state
		}
	}
	switch r.Recommendation.Tier {
	case TierUnknown:
		add(CheckUnknown, r.Recommendation.Reason)
	case TierNone:
		add(CheckFailed, r.Recommendation.Reason)
	}
	state, reason := checkSystem(s.System)
	if state != CheckPassed {
		add(state, reason)
	}
	state = checkBackends(s, r.Recommendation)
	if state != CheckPassed {
		add(state, "backend_"+string(state))
	}
	var needed uint64
	switch r.Recommendation.Tier {
	case Tier1_7B:
		needed = requirements.Tier1_7BBytes
	case Tier4B:
		needed = requirements.Tier4BBytes
	}
	if s.DiskFreeBytes.Value == nil {
		add(CheckUnknown, "disk_unknown")
	} else if needed == 0 {
		add(CheckUnknown, "disk_requirement_unknown")
	} else if *s.DiskFreeBytes.Value < needed {
		add(CheckFailed, "disk_insufficient")
	}
	if r.Recommendation.Tier == Tier1_7B && r.Recommendation.MaxTier == Tier4B {
		if s.DiskFreeBytes.Value == nil || requirements.Tier4BBytes == 0 || *s.DiskFreeBytes.Value < requirements.Tier4BBytes {
			r.Recommendation.MaxTier = Tier1_7B
		}
	}
	return r
}

func cloneFact[T any](f Fact[T]) Fact[T] {
	if f.Value != nil {
		v := *f.Value
		f.Value = &v
	}
	return f
}

func cloneSnapshot(s Snapshot) Snapshot {
	s.System = cloneFact(s.System)
	if s.System.Value != nil {
		p := s.System.Value
		p.Version, p.Architecture, p.CPU, p.LogicalCPUs = cloneFact(p.Version), cloneFact(p.Architecture), cloneFact(p.CPU), cloneFact(p.LogicalCPUs)
	}
	s.MemoryBytes, s.AvailableMemoryBytes, s.DiskFreeBytes = cloneFact(s.MemoryBytes), cloneFact(s.AvailableMemoryBytes), cloneFact(s.DiskFreeBytes)
	if s.Adapters.Value != nil {
		adapters := append([]Adapter{}, (*s.Adapters.Value)...)
		for i := range adapters {
			adapters[i].MemoryBytes = cloneFact(adapters[i].MemoryBytes)
		}
		s.Adapters.Value = &adapters
	}
	if s.Backends.Value != nil {
		backends := append([]Backend{}, (*s.Backends.Value)...)
		s.Backends.Value = &backends
	}
	return s
}

func checkSystem(f Fact[System]) (CheckState, string) {
	if f.Value == nil {
		return CheckUnknown, "system_unknown"
	}
	s := *f.Value
	if s.OS == "" || s.Architecture.Value == nil || *s.Architecture.Value == "" {
		return CheckUnknown, "architecture_unknown"
	}
	arch := *s.Architecture.Value
	if !((s.OS == "windows" || s.OS == "linux") && arch == "amd64" || s.OS == "darwin" && arch == "arm64") {
		return CheckFailed, "platform_unsupported"
	}
	// Linux version/distribution acceptance remains a native validation task;
	// fixtures support the provider contract without claiming Ubuntu acceptance.
	if s.OS == "linux" {
		return CheckPassed, ""
	}
	if s.Version.Value == nil {
		return CheckUnknown, "os_version_unknown"
	}
	parts := strings.Split(*s.Version.Value, ".")
	for _, part := range parts {
		if part == "" {
			return CheckUnknown, "os_version_unknown"
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return CheckUnknown, "os_version_unknown"
			}
		}
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil || major <= 0 {
		return CheckUnknown, "os_version_unknown"
	}
	minimum := 10
	if s.OS == "darwin" {
		minimum = 14
	}
	if major < minimum {
		return CheckFailed, "os_version_unsupported"
	}
	return CheckPassed, ""
}

func checkBackends(s Snapshot, recommendation Recommendation) CheckState {
	f := s.Backends
	if f.Value == nil {
		return CheckUnknown
	}
	state := CheckFailed
	matched := false
	for _, b := range *f.Value {
		// A VRAM-based upgrade requires a backend for that same adapter.
		// A CPU backend cannot make the discrete memory usable by the model.
		if recommendation.Reason == "discrete_vram_at_least_6_gib" {
			if b.Kind != CUDA && b.Kind != Vulkan {
				continue
			}
			found := false
			if s.Adapters.Value != nil {
				for _, a := range *s.Adapters.Value {
					if a.ID != "" && a.ID == b.AdapterID && a.Discrete == Available && a.MemoryBytes.Value != nil && *a.MemoryBytes.Value >= 6*GiB {
						found = true
					}
				}
			}
			if !found {
				continue
			}
		}
		if s.System.Value != nil && s.System.Value.OS == "darwin" && b.Kind != Metal {
			continue
		}
		if b.Kind == Metal && (s.System.Value == nil || s.System.Value.OS != "darwin") {
			continue
		}
		matched = true
		switch b.Kind {
		case CPU, CUDA, Metal, Vulkan:
			if b.State == Available {
				return CheckPassed
			}
			if b.State != Unavailable {
				state = CheckUnknown
			}
		default:
			state = CheckUnknown
		}
	}
	if !matched && len(*f.Value) > 0 {
		return CheckUnknown
	}
	return state
}
