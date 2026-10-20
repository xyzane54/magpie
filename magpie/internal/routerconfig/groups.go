package routerconfig

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode"
)

// GroupPrefix marks a member reference that points to another routing group.
const GroupPrefix = "group/"

// RegexpPrefix marks a Match pattern as a regular expression instead of a glob.
const RegexpPrefix = "re:"

// EffortAuto lets the router pick reasoning effort per request.
const EffortAuto = "auto"

// Routing strategies aligned with internal/provider/group.go and plugins/routing.
const (
	Manual       = "manual"
	Ordered      = "order"
	Weighted     = "weight"
	Rotate       = "rotate"
	LeastUsed    = "usage"
	Pace         = "pace"
	SmartSplit   = "smart_split"
	RulesRouting = "rules"
)

// Affinity modes aligned with internal/provider/group.go.
const (
	AffinitySession = "session"
	AffinityTurn    = "turn"
	AffinityOff     = "off"
	AffinityStrict  = "strict"
	AffinityPin     = "pin"
)

// Patience and context sentinel values aligned with internal/provider/group.go.
const (
	PatienceOff     = -1
	PatienceDefault = 60
	PatienceLongest = 240
	ContextSmallest = -1
	MaxGroupNest    = 8
)

const maxGroupIDLen = 128

var (
	ErrGroupCycle    = errors.New("routing group cycle detected")
	ErrGroupNotFound = errors.New("routing group not found")
)

type GroupValidation struct{ Message string }

func (e *GroupValidation) Error() string { return e.Message }
func invalidGroup(s string) error        { return &GroupValidation{s} }

// GroupError maps group validation and storage errors to safe HTTP status codes.
func GroupError(err error) (string, int) {
	var validation *GroupValidation
	switch {
	case errors.Is(err, context.Canceled):
		return "request canceled", 499
	case errors.Is(err, context.DeadlineExceeded):
		return "configuration operation budget exhausted", http.StatusGatewayTimeout
	case errors.Is(err, ErrGroupCycle):
		return err.Error(), http.StatusBadRequest
	case errors.As(err, &validation):
		return validation.Message, http.StatusBadRequest
	case errors.Is(err, ErrGroupNotFound):
		return err.Error(), http.StatusNotFound
	default:
		return "routing group configuration could not be read or saved", http.StatusServiceUnavailable
	}
}

// Group combines multiple provider/model entries under one model name.
// JSON tags are 100% compatible with internal/provider/group.go and plugins/routing/client.js.
type Group struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Members       []string          `json:"members"`            // "providerID/model[:effort]", "local/model", or "group/<id>"
	Match         []string          `json:"match,omitempty"`    // glob/re patterns against "provider/model" or "model"
	Matched       []string          `json:"matched,omitempty"`  // view-only: members pulled in by Match (cleared on save)
	Routing       string            `json:"routing,omitempty"`  // ""|"order"|"ordered"|"weight"|"weighted"|"manual"|"smart_split"|"rules"|"rotate"|"usage"|"least"|"pace"
	Affinity      string            `json:"affinity,omitempty"` // ""="auto", "session", "turn", "off", "strict", "pin"
	Stickiness    int               `json:"stickiness,omitempty"`
	Sink          bool              `json:"sink,omitempty"`
	FirstToken    int               `json:"firstToken,omitempty"`
	Patience      int               `json:"patience,omitempty"`
	Off           []string          `json:"off,omitempty"`
	Pick          string            `json:"pick,omitempty"`
	Rules         []Rule            `json:"rules,omitempty"`
	Classifier    string            `json:"classifier,omitempty"`
	Effort        string            `json:"effort,omitempty"`
	MemberEfforts map[string]string `json:"memberEfforts,omitempty"`
	Context       int               `json:"context,omitempty"`
	Levels        []string          `json:"levels,omitempty"`
	Fast          []string          `json:"fast,omitempty"`
	Weights       map[string]int    `json:"weights,omitempty"`
	Family        string            `json:"family,omitempty"`
	Unlists       bool              `json:"unlists,omitempty"`
	Auto          bool              `json:"auto,omitempty"`   // runtime-derived marker (cleared on save)
	Hidden        bool              `json:"hidden,omitempty"` // tombstone for user-dismissed auto group
	Disabled      bool              `json:"disabled,omitempty"`
}

// Rule is a single condition -> member mapping within a Group.
// Supports both full routing rule conditions and pattern/target/reason shorthand.
type Rule struct {
	When          string      `json:"when,omitempty"`
	Use           string      `json:"use,omitempty"`
	Pattern       string      `json:"pattern,omitempty"`
	Target        string      `json:"target,omitempty"`
	Reason        string      `json:"reason,omitempty"`
	Fast          bool        `json:"fast,omitempty"`
	Tokens        string      `json:"tokens,omitempty"`
	Images        *bool       `json:"images,omitempty"`
	Effort        string      `json:"effort,omitempty"`
	Agents        string      `json:"agents,omitempty"`
	Intent        string      `json:"intent,omitempty"`
	Compact       *bool       `json:"compact,omitempty"`
	Time          *TimeWindow `json:"time,omitempty"`
	Folders       []string    `json:"folders,omitempty"`
	ExceptFolders []string    `json:"exceptFolders,omitempty"`
	Pause         string      `json:"pause,omitempty"`
}

// GroupRule is an alias for Rule for caller convenience.
type GroupRule = Rule

// TimeWindow matches requests by local time of day and optional weekday list.
type TimeWindow struct {
	Start string   `json:"start"`
	End   string   `json:"end"`
	Days  []string `json:"days,omitempty"`
}

// GroupIDOf returns the referenced group ID if ref is a "group/<id>" member.
func GroupIDOf(ref string) (string, bool) {
	id, ok := strings.CutPrefix(strings.TrimSpace(ref), GroupPrefix)
	return id, ok && id != ""
}

// Slug turns a display name into a lowercase URL-safe identifier.
func Slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		case r > 127 && (unicode.IsLetter(r) || unicode.IsNumber(r)):
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// GroupSlug is an alias for Slug aligned with group naming helpers.
func GroupSlug(name string) string {
	return Slug(name)
}

// AutoGroupID returns the deterministic group ID for an auto-merged model.
func AutoGroupID(model string) string {
	canon := sameModel(model)
	if canon == "" {
		canon = model
	}
	return "auto-" + GroupSlug(canon)
}

// MergeName returns the user-facing display name for an auto-merged model group.
func MergeName(model string) string {
	s := strings.TrimSpace(model)
	for _, prefix := range []string{"global.", "us.", "eu.", "ap.", "apac."} {
		s = strings.TrimPrefix(s, prefix)
	}
	for _, vendor := range []string{
		"anthropic.", "anthropic/", "openai/", "google/", "deepseek/", "deepseek-ai/",
		"meta-llama/", "mistralai/", "qwen/", "x-ai/", "z-ai/", "moonshotai/", "minimax/",
	} {
		if strings.HasPrefix(strings.ToLower(s), vendor) {
			s = s[len(vendor):]
			break
		}
	}
	if idx := strings.IndexByte(s, ':'); idx > 0 {
		s = s[:idx]
	}
	s = stripDate(s)
	return s
}

// sameModel returns a canonical key for cross-provider model matching.
// For example:
//   - "anthropic/claude-sonnet-4-20250514" -> "claude-sonnet-4"
//   - "global.anthropic.claude-sonnet-4-20250514-v1:0" -> "claude-sonnet-4"
//   - "claude-sonnet-4-0" -> "claude-sonnet-4"
//   - "deepseek-ai/DeepSeek-V3" -> "deepseek-v3"
func sameModel(id string) string {
	s := strings.ToLower(strings.TrimSpace(id))
	if s == "" {
		return ""
	}
	for _, prefix := range []string{"global.", "us.", "eu.", "ap.", "apac."} {
		s = strings.TrimPrefix(s, prefix)
	}
	for _, vendor := range []string{
		"anthropic.", "anthropic/", "openai/", "google/", "deepseek/", "deepseek-ai/",
		"meta-llama/", "mistralai/", "qwen/", "x-ai/", "z-ai/", "moonshotai/", "minimax/",
	} {
		s = strings.TrimPrefix(s, vendor)
	}
	if idx := strings.IndexByte(s, ':'); idx > 0 {
		s = s[:idx]
	}
	for _, suffix := range []string{"-v1", "-v2"} {
		s = strings.TrimSuffix(s, suffix)
	}
	s = stripDate(s)
	if strings.HasPrefix(s, "claude-") {
		s = strings.TrimSuffix(s, "-latest")
		s = normalizeClaudeVersion(s)
	}
	return s
}
