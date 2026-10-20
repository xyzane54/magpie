package routerdevice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

var fixtureRequirements = Requirements{4 * GiB, 8 * GiB}

func fixture(osName string, ram uint64, gpu ...Adapter) Snapshot {
	arch, version := "amd64", "10.0.22631"
	if osName == "darwin" {
		arch, version = "arm64", "14.0"
	}
	if osName == "linux" {
		version = "6.8.0"
	}
	backends := []Backend{{Kind: CPU, State: Available}}
	if osName == "darwin" {
		backends = append(backends, Backend{Kind: Metal, State: Available})
	}
	return Snapshot{
		System:      Known(System{OS: osName, Version: Known(version), Architecture: Known(arch), CPU: Known("fixture CPU"), LogicalCPUs: Known(8)}),
		MemoryBytes: Known(ram), Adapters: Known(append([]Adapter{}, gpu...)),
		DiskFreeBytes: Known(20 * GiB), Backends: Known(backends),
	}
}

func TestRecommendationBoundaries(t *testing.T) {
	for _, platform := range []string{"windows", "darwin", "linux"} {
		for _, tc := range []struct {
			name          string
			ram, vram     uint64
			tier, maxTier Tier
		}{
			{"7.9GB", 79 * GiB / 10, 0, TierNone, TierNone},
			{"8GB", 8 * GiB, 0, Tier1_7B, Tier1_7B},
			{"below16GB", 16*GiB - 1, 0, Tier1_7B, Tier1_7B},
			{"16GB", 16 * GiB, 0, Tier1_7B, Tier4B},
			{"below24GB", 24*GiB - 1, 0, Tier1_7B, Tier4B},
			{"24GB", 24 * GiB, 0, Tier4B, Tier4B},
			{"5.9GBVRAM", 8 * GiB, 59 * GiB / 10, Tier1_7B, Tier1_7B},
			{"6GBVRAM", 8 * GiB, 6 * GiB, Tier4B, Tier4B},
			{"below8WithGPU", 8*GiB - 1, 24 * GiB, TierNone, TierNone},
		} {
			t.Run(platform+"/"+tc.name, func(t *testing.T) {
				s := fixture(platform, tc.ram, Adapter{Name: "fixture GPU", Discrete: Available, MemoryBytes: Known(tc.vram)})
				r := Evaluate(s, fixtureRequirements)
				if r.Recommendation.Tier != tc.tier || r.Recommendation.EffectiveMaxTier() != tc.maxTier {
					t.Fatalf("recommended %+v, want tier=%s maxTier=%s", r.Recommendation, tc.tier, tc.maxTier)
				}
				wantCheck := CheckPassed
				if tc.vram >= 6*GiB {
					wantCheck = CheckUnknown
				}
				if tc.tier == TierNone {
					wantCheck = CheckFailed
				}
				if r.Detection.State != wantCheck {
					t.Fatalf("detection = %+v, want %s", r.Detection, wantCheck)
				}
			})
		}
	}
}

func TestAvailableMemoryAndCPUThreadThresholds(t *testing.T) {
	for _, tc := range []struct {
		name          string
		edit          func(*Snapshot)
		tier, maxTier Tier
		reason        string
		state         CheckState
	}{
		{
			name: "availBelow2.5GiBBlocksEvenOn32GiB",
			edit: func(s *Snapshot) {
				s.MemoryBytes = Known(32 * GiB)
				s.AvailableMemoryBytes = Known(5*HalfGiB - 1)
			},
			tier: TierNone, maxTier: TierNone, reason: "available_memory_below_2_5_gib", state: CheckFailed,
		},
		{
			name: "availAt2.5GiBTo4.5GiBLimits16GiBTo1.7BOnly",
			edit: func(s *Snapshot) {
				s.MemoryBytes = Known(16 * GiB)
				s.AvailableMemoryBytes = Known(5 * HalfGiB)
			},
			tier: Tier1_7B, maxTier: Tier1_7B, reason: "memory_16_gib_limited_headroom", state: CheckPassed,
		},
		{
			name: "availAt4.5GiBOn16GiBPrefers1.7BAllows4B",
			edit: func(s *Snapshot) {
				s.MemoryBytes = Known(16 * GiB)
				s.AvailableMemoryBytes = Known(9 * HalfGiB)
			},
			tier: Tier1_7B, maxTier: Tier4B, reason: "memory_16_gib_prefer_1_7b_allow_4b", state: CheckPassed,
		},
		{
			name: "availBetween4.5And6.5GiBOn24GiBPrefers1.7BAllows4B",
			edit: func(s *Snapshot) {
				s.MemoryBytes = Known(24 * GiB)
				s.AvailableMemoryBytes = Known(13*HalfGiB - 1)
			},
			tier: Tier1_7B, maxTier: Tier4B, reason: "memory_16_gib_prefer_1_7b_allow_4b", state: CheckPassed,
		},
		{
			name: "availAt6.5GiBOn24GiBRecommends4B",
			edit: func(s *Snapshot) {
				s.MemoryBytes = Known(24 * GiB)
				s.AvailableMemoryBytes = Known(13 * HalfGiB)
			},
			tier: Tier4B, maxTier: Tier4B, reason: "memory_at_least_24_gib", state: CheckPassed,
		},
		{
			name: "cpuThreadsBelow4WithoutGPUAcceleratorFails",
			edit: func(s *Snapshot) {
				s.MemoryBytes = Known(16 * GiB)
				s.System.Value.LogicalCPUs = Known(2)
			},
			tier: TierNone, maxTier: TierNone, reason: "cpu_threads_below_4", state: CheckFailed,
		},
		{
			name: "cpuThreads4To7On16GiBLimitsTo1.7BOnly",
			edit: func(s *Snapshot) {
				s.MemoryBytes = Known(16 * GiB)
				s.System.Value.LogicalCPUs = Known(4)
			},
			tier: Tier1_7B, maxTier: Tier1_7B, reason: "memory_16_gib_limited_headroom", state: CheckPassed,
		},
		{
			name: "diskBelow4BRequirementCapsMaxTierTo1.7B",
			edit: func(s *Snapshot) {
				s.MemoryBytes = Known(16 * GiB)
				s.AvailableMemoryBytes = Known(8 * GiB)
				s.DiskFreeBytes = Known(6 * GiB)
			},
			tier: Tier1_7B, maxTier: Tier1_7B, reason: "memory_16_gib_prefer_1_7b_allow_4b", state: CheckPassed,
		},
		{
			name: "16GiBWithConfirmedDiscreteGPUBackendRecommends4B",
			edit: func(s *Snapshot) {
				s.MemoryBytes = Known(16 * GiB)
				s.AvailableMemoryBytes = Known(6 * GiB)
				s.Adapters = Known([]Adapter{{ID: "gpu0", Discrete: Available, MemoryBytes: Known(8 * GiB)}})
				s.Backends = Known([]Backend{{Kind: CUDA, State: Available, AdapterID: "gpu0"}})
			},
			tier: Tier4B, maxTier: Tier4B, reason: "discrete_vram_at_least_6_gib", state: CheckPassed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture("windows", 16*GiB)
			tc.edit(&s)
			r := Evaluate(s, fixtureRequirements)
			if r.Recommendation.Tier != tc.tier || r.Recommendation.EffectiveMaxTier() != tc.maxTier || r.Recommendation.Reason != tc.reason || r.Detection.State != tc.state {
				t.Fatalf("got recommendation=%+v detection=%+v, want tier=%s maxTier=%s reason=%s state=%s", r.Recommendation, r.Detection, tc.tier, tc.maxTier, tc.reason, tc.state)
			}
			if r.Recommendation.Allows(Tier4B) != (tc.maxTier == Tier4B) {
				t.Fatalf("Allows(4B) = %v, want %v", r.Recommendation.Allows(Tier4B), tc.maxTier == Tier4B)
			}
		})
	}
}

func TestVRAMRecommendationNeedsBackendForSameAdapter(t *testing.T) {
	s := fixture("windows", 8*GiB, Adapter{ID: "large", Discrete: Available, MemoryBytes: Known(6 * GiB)}, Adapter{ID: "small", Discrete: Available, MemoryBytes: Known(3 * GiB)})
	for _, tc := range []struct {
		backend Backend
		state   CheckState
	}{
		{Backend{Kind: CPU, State: Available}, CheckUnknown},
		{Backend{Kind: CUDA, State: UnknownAvailability, AdapterID: "large"}, CheckUnknown},
		{Backend{Kind: CUDA, State: Available, AdapterID: "small"}, CheckUnknown},
		{Backend{Kind: CUDA, State: Available, AdapterID: "large"}, CheckPassed},
		{Backend{Kind: Vulkan, State: Available, AdapterID: "large"}, CheckPassed},
	} {
		s.Backends = Known([]Backend{tc.backend})
		r := Evaluate(s, fixtureRequirements)
		if r.Recommendation.Tier != Tier4B || r.Detection.State != tc.state {
			t.Fatalf("VRAM/backend mismatch: %+v %+v", tc.backend, r)
		}
	}
	s = fixture("darwin", 16*GiB)
	s.Backends = Known([]Backend{{Kind: CPU, State: Available}, {Kind: Metal, State: UnknownAvailability}})
	if r := Evaluate(s, fixtureRequirements); r.Detection.State != CheckUnknown {
		t.Fatalf("CPU masked unknown Metal: %+v", r)
	}
}

func TestDiscreteMemoryNeverUsesUMAUnknownOrSum(t *testing.T) {
	for _, adapters := range [][]Adapter{
		{{Discrete: Unavailable, MemoryBytes: Known(8 * GiB)}},
		{{Discrete: UnknownAvailability, MemoryBytes: Known(8 * GiB)}},
		{{Discrete: Available, MemoryBytes: Unknown[uint64]("read failed")}},
		{{Discrete: Available, MemoryBytes: Known(3 * GiB)}, {Discrete: Available, MemoryBytes: Known(3 * GiB)}},
	} {
		s := fixture("windows", 8*GiB, adapters...)
		if r := Recommend(s); r.Tier != Tier1_7B || r.EffectiveMaxTier() != Tier1_7B {
			t.Fatalf("invalid VRAM upgrade: %+v: %+v", adapters, r)
		}
	}
	s := fixture("windows", 16*GiB)
	s.Adapters = Unknown[[]Adapter]("enumeration failed")
	if r := Recommend(s); r.Tier != Tier1_7B || r.EffectiveMaxTier() != Tier4B {
		t.Fatalf("known 16 GiB RAM lost independent recommendation: %+v", r)
	}
	s24 := fixture("windows", 24*GiB)
	s24.Adapters = Unknown[[]Adapter]("enumeration failed")
	if r := Recommend(s24); r.Tier != Tier4B || r.EffectiveMaxTier() != Tier4B {
		t.Fatalf("known 24 GiB RAM lost independent recommendation: %+v", r)
	}
	s.MemoryBytes = Unknown[uint64]("read failed")
	if r := Recommend(s); r.Tier != TierUnknown {
		t.Fatalf("unknown RAM = %+v", r)
	}
	s.Adapters = Known([]Adapter{{Discrete: Available, MemoryBytes: Known(24 * GiB)}})
	if r := Recommend(s); r.Tier != TierUnknown {
		t.Fatalf("unknown RAM upgraded using GPU: %+v", r)
	}
}

func TestDetectionAndRecommendationAreSeparate(t *testing.T) {
	for _, tc := range []struct {
		name         string
		edit         func(*Snapshot)
		requirements Requirements
		state        CheckState
	}{
		{"architectureUnknown", func(s *Snapshot) { s.System.Value.Architecture = Unknown[string]("unreadable") }, fixtureRequirements, CheckUnknown},
		{"architectureUnsupported", func(s *Snapshot) { s.System.Value.Architecture = Known("mips64") }, fixtureRequirements, CheckFailed},
		{"versionUnknown", func(s *Snapshot) { s.System.Value.Version = Unknown[string]("unreadable") }, fixtureRequirements, CheckUnknown},
		{"versionMalformed", func(s *Snapshot) { s.System.Value.Version = Known("10.not-a-version") }, fixtureRequirements, CheckUnknown},
		{"oldMacOS", func(s *Snapshot) { s.System = fixture("darwin", 24*GiB).System; s.System.Value.Version = Known("13.6") }, fixtureRequirements, CheckFailed},
		{"backendUnknown", func(s *Snapshot) { s.Backends = Unknown[[]Backend]("probe failed") }, fixtureRequirements, CheckUnknown},
		{"backendEmpty", func(s *Snapshot) { s.Backends = Known([]Backend{}) }, fixtureRequirements, CheckFailed},
		{"unrecognizedBackend", func(s *Snapshot) { s.Backends = Known([]Backend{{Kind: "invented", State: Available}}) }, fixtureRequirements, CheckUnknown},
		{"backendUnavailable", func(s *Snapshot) { s.Backends = Known([]Backend{{Kind: CPU, State: Unavailable}}) }, fixtureRequirements, CheckFailed},
		{"diskUnknown", func(s *Snapshot) { s.DiskFreeBytes = Unknown[uint64]("permission denied") }, fixtureRequirements, CheckUnknown},
		{"diskInsufficient", func(s *Snapshot) { s.DiskFreeBytes = Known(8*GiB - 1) }, fixtureRequirements, CheckFailed},
		{"diskFull", func(s *Snapshot) { s.DiskFreeBytes = Known(uint64(0)) }, fixtureRequirements, CheckFailed},
		{"diskExact", func(s *Snapshot) { s.DiskFreeBytes = Known(8 * GiB) }, fixtureRequirements, CheckPassed},
		{"manifestUnknown", func(s *Snapshot) {}, Requirements{}, CheckUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture("windows", 24*GiB)
			tc.edit(&s)
			r := Evaluate(s, tc.requirements)
			if r.Recommendation.Tier != Tier4B || r.Detection.State != tc.state {
				t.Fatalf("recommendation and detection conflated: %+v", r)
			}
		})
	}
}

func TestDetectorErrorDiscardsValuesAndRepeatDoesNotCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models")
	calls := 0
	d := New(ProviderFunc(func(ctx context.Context, got string) (Snapshot, error) {
		if got != path {
			t.Fatalf("destination = %q", got)
		}
		calls++
		s := fixture("windows", 16*GiB)
		if calls == 2 {
			return s, errors.New("failed with stale values")
		}
		return s, nil
	}))
	first := d.Detect(context.Background(), path, fixtureRequirements)
	if first.Detection.State != CheckPassed {
		t.Fatalf("first = %+v", first)
	}
	(*first.Device.Backends.Value)[0].State = Unavailable
	second := d.Detect(context.Background(), path, fixtureRequirements)
	if second.Recommendation.Tier != TierUnknown || second.Detection.State != CheckUnknown || second.Device.MemoryBytes.Value != nil {
		t.Fatalf("failed detection reused success: %+v", second)
	}
	third := d.Detect(context.Background(), path, fixtureRequirements)
	if third.Detection.State != CheckPassed || calls != 3 {
		t.Fatalf("repeat = %+v, calls %d", third, calls)
	}
}

func TestReportsDoNotShareMutableFixtureData(t *testing.T) {
	s := fixture("windows", 24*GiB, Adapter{Discrete: Available, MemoryBytes: Known(8 * GiB)})
	s.AvailableMemoryBytes = Known(12 * GiB)
	d := New(ProviderFunc(func(context.Context, string) (Snapshot, error) { return s, nil }))
	path := t.TempDir()
	first := d.Detect(context.Background(), path, fixtureRequirements)
	*first.Device.MemoryBytes.Value = 1
	*first.Device.AvailableMemoryBytes.Value = 1
	*first.Device.System.Value.Architecture.Value = "unknown"
	*(*first.Device.Adapters.Value)[0].MemoryBytes.Value = 1
	(*first.Device.Backends.Value)[0].State = Unavailable
	second := d.Detect(context.Background(), path, fixtureRequirements)
	if second.Detection.State != CheckPassed || second.Recommendation.Tier != Tier4B || *s.MemoryBytes.Value != 24*GiB || *s.AvailableMemoryBytes.Value != 12*GiB || *(*s.Adapters.Value)[0].MemoryBytes.Value != 8*GiB {
		t.Fatalf("mutating a report polluted repeated detection: %+v", second)
	}
}

func TestDetectorCancellationAndBadPathDoNotProbe(t *testing.T) {
	d := New(ProviderFunc(func(context.Context, string) (Snapshot, error) { t.Fatal("provider called"); return Snapshot{}, nil }))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		ctx  context.Context
		path string
	}{{ctx, t.TempDir()}, {context.Background(), "relative"}, {context.Background(), ""}} {
		r := d.Detect(tc.ctx, tc.path, fixtureRequirements)
		if r.Detection.State != CheckUnknown || r.Recommendation.Tier != TierUnknown {
			t.Fatalf("invalid request passed: %+v", r)
		}
	}
	if r := New(nil).Detect(context.Background(), t.TempDir(), fixtureRequirements); r.Detection.State != CheckUnknown {
		t.Fatal(r)
	}
}

func TestProviderErrorWithEmptyMessageStillInvalidatesSnapshot(t *testing.T) {
	d := New(ProviderFunc(func(context.Context, string) (Snapshot, error) { return fixture("windows", 32*GiB), errors.New("") }))
	r := d.Detect(context.Background(), t.TempDir(), fixtureRequirements)
	if r.Recommendation.Tier != TierUnknown || r.Detection.State != CheckUnknown || r.Device.MemoryBytes.Value != nil {
		t.Fatalf("failed probe recommended hardware: %+v", r)
	}
}

func TestDetectionHasNoDownloadProcessOrConfigurationEffects(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config.json")
	before := []byte(`{"runtime":{},"redaction":{"enabled":true}}`)
	if err := os.WriteFile(config, before, 0600); err != nil {
		t.Fatal(err)
	}
	model := ModelState{}
	d := New(ProviderFunc(func(context.Context, string) (Snapshot, error) { return fixture("windows", 16*GiB), nil }))
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			r := d.Detect(context.Background(), filepath.Join(root, "models"), fixtureRequirements)
			if r.Detection.State != CheckPassed {
				t.Errorf("detect = %+v", r)
			}
		})
	}
	wg.Wait()
	after, err := os.ReadFile(config)
	entries, readErr := os.ReadDir(root)
	if err != nil || readErr != nil || string(after) != string(before) || len(entries) != 1 {
		t.Fatalf("side effects: %s, %v, %v, %v", after, entries, err, readErr)
	}
	if !reflect.DeepEqual(model, ModelState{}) {
		t.Fatal("detection marked a model installed/enabled")
	}
	encoded, err := json.Marshal(Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip Snapshot
	if err := json.Unmarshal(encoded, &roundtrip); err != nil || roundtrip.MemoryBytes.Value != nil {
		t.Fatalf("unknown became zero: %s (%v)", encoded, err)
	}
}
