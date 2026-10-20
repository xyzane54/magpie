package routerconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// RuntimeSettings is comparable and uses seconds for all time budgets. Its zero
// value preserves the original runtime contract, including the shared five
// second judgment queue/inference deadline. Explicit settings are complete.
type RuntimeSettings struct {
	ContextTokens            int `json:"contextTokens"`
	JudgmentQueueSeconds     int `json:"judgmentQueueSeconds"`
	JudgmentInferenceSeconds int `json:"judgmentInferenceSeconds"`
	ExecutionQueueSeconds    int `json:"executionQueueSeconds"`
	ExecutionSeconds         int `json:"executionSeconds"`
	LoadSeconds              int `json:"loadSeconds"`
	RequestSeconds           int `json:"requestSeconds"`
	CloudReserveSeconds      int `json:"cloudReserveSeconds"`
}

func (s RuntimeSettings) IsZero() bool { return s == (RuntimeSettings{}) }

func LongContextRuntimeSettings() RuntimeSettings {
	return RuntimeSettings{65536, 15, 30, 30, 300, 90, 600, 60}
}

func (s RuntimeSettings) Effective() RuntimeSettings {
	if s.IsZero() {
		return RuntimeSettings{8192, 5, 5, 8, 120, 60, 120, 0}
	}
	return s
}

func (s RuntimeSettings) Validate() error {
	if s.IsZero() {
		return nil
	}
	if err := ValidateContextTokens(s.ContextTokens); err != nil {
		return err
	}
	for _, field := range []struct {
		name           string
		value, maximum int
	}{
		{"judgmentQueueSeconds", s.JudgmentQueueSeconds, 300},
		{"judgmentInferenceSeconds", s.JudgmentInferenceSeconds, 300},
		{"executionQueueSeconds", s.ExecutionQueueSeconds, 300},
		{"executionSeconds", s.ExecutionSeconds, 1800},
		{"loadSeconds", s.LoadSeconds, 300},
		{"requestSeconds", s.RequestSeconds, 3600},
		{"cloudReserveSeconds", s.CloudReserveSeconds, 300},
	} {
		if field.value < 1 || field.value > field.maximum {
			return fmt.Errorf("%s must be between 1 and %d seconds", field.name, field.maximum)
		}
	}
	local := s.RequestSeconds - s.CloudReserveSeconds
	if local <= 0 || s.LoadSeconds > local || s.JudgmentQueueSeconds+s.JudgmentInferenceSeconds > local || s.ExecutionQueueSeconds+s.ExecutionSeconds > local {
		return errors.New("request budget minus cloud reserve must fit each local stage")
	}
	return nil
}

// ValidateContextTokens is shared by serving settings and trusted model binding.
func ValidateContextTokens(tokens int) error {
	switch tokens {
	case 8192, 16384, 32768, 65536:
		return nil
	default:
		return errors.New("contextTokens must be 8192, 16384, 32768 or 65536")
	}
}

func (s RuntimeSettings) Fingerprint() string {
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
