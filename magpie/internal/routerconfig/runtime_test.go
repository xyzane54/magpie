package routerconfig

import "testing"

func TestRuntimeSettingsValidateCompleteProfilesAndLegacy(t *testing.T) {
	if err := (RuntimeSettings{}).Validate(); err != nil {
		t.Fatal(err)
	}
	profile := LongContextRuntimeSettings()
	if err := profile.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, window := range []int{8192, 16384, 32768, 65536} {
		s := profile
		s.ContextTokens = window
		if err := s.Validate(); err != nil {
			t.Errorf("supported window %d: %v", window, err)
		}
	}
	for name, mutate := range map[string]func(*RuntimeSettings){
		"unsupported window":    func(s *RuntimeSettings) { s.ContextTokens = 1048576 },
		"missing budget":        func(s *RuntimeSettings) { s.LoadSeconds = 0 },
		"negative budget":       func(s *RuntimeSettings) { s.ExecutionSeconds = -1 },
		"oversized budget":      func(s *RuntimeSettings) { s.RequestSeconds = 3601 },
		"reserve outside total": func(s *RuntimeSettings) { s.CloudReserveSeconds = s.RequestSeconds },
		"stage cannot fit":      func(s *RuntimeSettings) { s.RequestSeconds = 100; s.CloudReserveSeconds = 60 },
	} {
		t.Run(name, func(t *testing.T) {
			s := profile
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("unsafe or incomplete profile accepted")
			}
		})
	}
}

func TestRuntimeSettingsKeepLegacyDistinctAndFingerprintBudgets(t *testing.T) {
	legacy := RuntimeSettings{}
	if !legacy.IsZero() || legacy.Effective().ContextTokens != 8192 || legacy.Effective().RequestSeconds != 120 {
		t.Fatal("legacy profile changed")
	}
	s := LongContextRuntimeSettings()
	next := s
	next.ContextTokens = 32768
	if s.Fingerprint() == next.Fingerprint() {
		t.Fatal("window omitted from settings identity")
	}
	next = s
	next.ExecutionSeconds++
	if s.Fingerprint() == next.Fingerprint() {
		t.Fatal("budget omitted from settings identity")
	}
}
