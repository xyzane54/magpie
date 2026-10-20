package routerrequest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/routerconfig"
)

// Error codes are stable; protocol adapters own wire-envelope mapping.
// Messages are constant safe text, never configuration, headers or body values.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"`
}

func (e *Error) Error() string { return e.Message }
func ClassifyError(err error) *Error {
	var known *Error
	if errors.As(err, &known) {
		return known
	}
	var normalized *NormalizeError
	if errors.As(err, &normalized) {
		status := 400
		if normalized.Code == "remote_history_unsupported" {
			status = 501
		}
		return &Error{Code: normalized.Code, Message: normalized.Message, Status: status}
	}
	switch {
	case errors.Is(err, routerconfig.ErrAppNotFound):
		return &Error{"app_not_registered", "application is not registered", 404}
	case errors.Is(err, routerconfig.ErrProviderConflict):
		return &Error{"provider_conflict", "provider changed; reload before checking or saving", 409}
	case errors.Is(err, routerconfig.ErrAppConflict):
		return &Error{"profile_conflict", "application changed; reload before saving", 409}
	case errors.Is(err, routerconfig.ErrAppUnconfigured):
		return &Error{"profile_unconfigured", "select an application cloud provider and model", 409}
	case errors.Is(err, routerconfig.ErrMissingCredential):
		return &Error{"credential_missing", "required provider or business credential is missing", 401}
	case errors.Is(err, routerconfig.ErrCredentialMisuse):
		return &Error{"credential_misuse", "management credentials cannot be used for model requests", 403}
	case errors.Is(err, context.Canceled):
		return &Error{"request_canceled", "request canceled", 499}
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{"budget_exhausted", "request budget exhausted", 504}
	}
	var v *routerconfig.ProviderValidation
	if errors.As(err, &v) {
		return &Error{"profile_invalid", v.Message, 400}
	}
	if errors.Is(err, routerconfig.ErrProviderNotFound) {
		return &Error{"target_missing", "configured provider does not exist", 409}
	}
	return &Error{"configuration_unavailable", "application configuration could not be read or saved", 503}
}

// State is owned by one protocol executor, not shared mutable global state.
// Target and Profile are immutable snapshots; do not log/encode Target.
// Context carries cancellation through every subsequent operation.
type State struct {
	ID              string                 `json:"requestId"`
	AppID           string                 `json:"appId"`
	Path            string                 `json:"path"`
	Stage           string                 `json:"stage"`
	Destination     string                 `json:"destination"`
	Attempts        int                    `json:"attempts"`
	OutputCommitted bool                   `json:"outputCommitted"`
	StartedAt       time.Time              `json:"startedAt"`
	Deadline        time.Time              `json:"deadline"`
	Context         context.Context        `json:"-"`
	Target          routerconfig.AppTarget `json:"-"`
	memberAttempts  int
	memberTracked   bool
}

func (s *State) Canceled() bool { return errors.Is(s.Context.Err(), context.Canceled) }
func (s *State) Remaining() time.Duration {
	d := time.Until(s.Deadline)
	if d < 0 {
		return 0
	}
	return d
}
func (s *State) BeginMemberDispatch() {
	if !s.OutputCommitted {
		s.memberTracked = true
		s.memberAttempts = 0
	}
}
func (s *State) BeginCloudAttempt() error {
	if err := s.Context.Err(); err != nil {
		return err
	}
	if s.Remaining() == 0 {
		return context.DeadlineExceeded
	}
	limitReached := s.Attempts >= 2
	if s.memberTracked {
		limitReached = s.memberAttempts >= 2
	}
	if s.OutputCommitted || limitReached {
		return &Error{"attempt_forbidden", "output committed or attempt limit reached", 409}
	}
	s.Attempts++
	s.memberAttempts++
	s.Stage = "cloud"
	s.Destination = "cloud"
	return nil
}
func (s *State) CommitOutput() { s.OutputCommitted = true }

// ValidateModel prevents a host's model field from overriding its fixed Profile.
func (s *State) ValidateModel(model string) error {
	if model == "" {
		return nil
	}
	if s.Target.Profile.Cloud.Model != "" && model == s.Target.Profile.Cloud.Model {
		return nil
	}
	for _, bound := range s.Target.Profile.Models {
		if matchBoundModel(model, bound) {
			return nil
		}
	}
	return &Error{"target_mismatch", "request model must match the application Profile", 400}
}

func matchBoundModel(model, bound string) bool {
	if model == "" || bound == "" {
		return false
	}
	if model == bound {
		return true
	}
	if idx := strings.IndexByte(bound, '/'); idx > 0 {
		suffix := bound[idx+1:]
		if model == suffix {
			return true
		}
		if bound[:idx] == "group" {
			if strings.HasPrefix(model, "group/") && routerconfig.GroupSlug(strings.TrimPrefix(model, "group/")) == suffix {
				return true
			}
			if routerconfig.GroupSlug(model) == suffix {
				return true
			}
		}
	}
	return false
}

type requestIDKey struct{}

func RequestID(ctx context.Context) string { id, _ := ctx.Value(requestIDKey{}).(string); return id }

// WithRequestID ignores user-provided correlation headers. It runs before
// validation so failures and later protocol/statistics modules share an ID.
func WithRequestID(r *http.Request) (*http.Request, error) {
	if RequestID(r.Context()) != "" {
		return r, nil
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	return r.WithContext(context.WithValue(r.Context(), requestIDKey{}, hex.EncodeToString(id[:]))), nil
}

type requestStateKey struct{}

func FromContext(ctx context.Context) (*State, bool) {
	s, ok := ctx.Value(requestStateKey{}).(*State)
	return s, ok
}

// ParseAppPath accepts only canonical, literal paths. No unescaping, cleaning,
// case folding or duplicated-v1 correction is permitted at the identity boundary.
func ParseAppPath(r *http.Request) (string, string, error) {
	path := r.URL.EscapedPath()
	parts := strings.Split(path, "/")
	if path != r.URL.Path || strings.ContainsAny(path, "%\\") || len(path) > 1024 || len(parts) < 4 || parts[0] != "" || parts[1] != "app" || !routerconfig.ValidAppID(parts[2]) || parts[3] != "v1" {
		return "", "", &Error{"invalid_app_path", "invalid application path", 400}
	}
	for _, part := range parts[4:] {
		if part == "" || part == "." || part == ".." {
			return "", "", &Error{"invalid_app_path", "invalid application path", 400}
		}
	}
	sub := strings.Join(parts[4:], "/")
	switch sub {
	case "", "models", "chat/completions", "responses", "messages", "messages/count_tokens":
	default:
		return "", "", &Error{"unsupported_endpoint", "application endpoint is not supported", 404}
	}
	return parts[2], sub, nil
}

// Prepare is the integration point for protocols. It performs no I/O
// to an upstream, pins the target and attaches state. Caller must defer cancel.
func Prepare(r *http.Request, store *routerconfig.Store, managementToken string, budget time.Duration) (*http.Request, context.CancelFunc, error) {
	id, _, err := ParseAppPath(r)
	if err != nil {
		return nil, nil, err
	}
	r, err = WithRequestID(r)
	if err != nil {
		return nil, nil, err
	}
	if err := r.Context().Err(); err != nil {
		return nil, nil, err
	}
	if store == nil {
		return nil, nil, errors.New("application store unavailable")
	}
	if len(r.Header.Values("Authorization")) > 1 || len(r.Header.Values("x-api-key")) > 1 {
		return nil, nil, &Error{"invalid_auth_headers", "authentication headers must not be repeated", 400}
	}
	r, budgetCancel := WithBudget(r, budget)
	target, err := store.ResolveAppContext(r.Context(), id, r.Header.Get("Authorization"), r.Header.Get("x-api-key"), managementToken)
	if err != nil {
		budgetCancel()
		return nil, nil, err
	}
	prepared, cancel, err := PrepareResolved(r, target, budget)
	if err != nil {
		budgetCancel()
		return nil, nil, err
	}
	return prepared, func() { cancel(); budgetCancel() }, nil
}

// WithBudget starts the shared deadline before any configuration wait, including
// optional admission metadata reads. Nested preparation never extends it.
func WithBudget(r *http.Request, budget time.Duration) (*http.Request, context.CancelFunc) {
	if budget <= 0 {
		budget = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), budget)
	return r.WithContext(ctx), cancel
}

// PrepareResolved attaches the same request state and shared operation budget to
// a resolved target. Callers must enforce the appropriate access boundary and
// use Store.ResolveApp or Store.ResolveAppDraft to resolve the exact target first.
func PrepareResolved(r *http.Request, target routerconfig.AppTarget, budget time.Duration) (*http.Request, context.CancelFunc, error) {
	r, err := WithRequestID(r)
	if err != nil {
		return nil, nil, err
	}
	if err := r.Context().Err(); err != nil {
		return nil, nil, err
	}
	r = r.Clone(r.Context())
	// Inbound placeholders and management credentials never reach adapters.
	// Loomy's dynamic business authorization is carried only by target.
	r.Header.Del("Authorization")
	r.Header.Del("x-api-key")
	if budget <= 0 {
		budget = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), budget)
	deadline, _ := ctx.Deadline()
	state := &State{ID: RequestID(r.Context()), AppID: target.Profile.ID, Path: r.URL.Path, Stage: "configured", Destination: "cloud", StartedAt: time.Now(), Deadline: deadline, Context: ctx, Target: target}
	return r.WithContext(context.WithValue(ctx, requestStateKey{}, state)), cancel, nil
}
