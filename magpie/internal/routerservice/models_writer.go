package routerservice

import (
	"context"
	"encoding/json"
	"github.com/yetone/magpie/internal/routercloud"
	"github.com/yetone/magpie/internal/routerobserve"
	"github.com/yetone/magpie/internal/routerrequest"
	"net/http"
	"strings"
	"time"
)

func isAllowedAnthropicBetaToken(appID, token string) bool {
	lower := strings.ToLower(token)
	if strings.HasPrefix(lower, "claude-code-") || strings.HasPrefix(lower, "token-counting-") {
		return true
	}
	if appID == "claude-code" {
		for _, prefix := range []string{
			"interleaved-thinking-",
			"prompt-caching-",
			"effort-",
			"output-128k-",
			"max-tokens-",
		} {
			if strings.HasPrefix(lower, prefix) {
				return true
			}
		}
	}
	return false
}

// Semantic protocol headers cannot be silently discarded or copied onto a
// different Profile protocol. Only the version understood by this entry is
// accepted; beta features need their own reviewed forwarding contract.
func validateModelHeaders(header http.Header, protocol string) error {
	for name, values := range header {
		switch {
		case strings.EqualFold(name, "anthropic-beta"), strings.EqualFold(name, "openai-beta"):
			return &routercloud.Error{Code: "unsupported_input", Message: "unsupported model protocol headers", Status: 400}
		case strings.EqualFold(name, "anthropic-version"):
			if protocol != "anthropic" || len(values) != 1 || values[0] != "2023-06-01" {
				return &routercloud.Error{Code: "unsupported_input", Message: "unsupported model protocol headers", Status: 400}
			}
		}
	}
	return nil
}

// executePreparedModel is shared by actual application requests and explicit
// editor connection checks. B alone decides retry eligibility and waiting.
func executePreparedModel(w http.ResponseWriter, prepared *http.Request, sub string, body []byte, options ModelOptions) (routercloud.Result, error) {
	ctx, options := snapshotRuntimeSettings(prepared.Context(), options)
	prepared = prepared.WithContext(ctx)
	state, _ := routerrequest.FromContext(prepared.Context())
	// C binds the request's observer after Prepare. Promote that trusted
	// context value into A's producer options so judgment, execution and
	// redaction events carry the current request watch. The service-level
	// observer remains the fallback for management and non-HTTP callers.
	if observer := routerobserve.LocalObserver(prepared.Context()); observer != nil {
		options.LocalObserver = observer
	}
	target := routercloud.Target{Provider: state.Target.Provider, Model: state.Target.Profile.Cloud.Model,
		Authorization: state.Target.Authorization, RedactionRequired: state.Target.Profile.RedactionEnabled,
		ResponseCapabilities: state.Target.Profile.Cloud.Capabilities}
	controller := http.NewResponseController(w)
	// B validates the full input (including duplicate keys). Read only the model
	// selector here; no rewrite or removal of host fields takes place.
	var selector struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &selector) == nil {
		if err := state.ValidateModel(selector.Model); err != nil {
			if _, ok := resolveBoundTarget(selector.Model, state.Target, options); !ok {
				return routercloud.Result{}, err
			}
		}
	}
	emit := func(event routercloud.Event) {
		if options.Observe != nil {
			options.Observe(ModelEvent{Purpose: modelPurpose(options), RequestID: state.ID, AppID: state.AppID, Event: event})
		}
	}
	writer := &modelWriter{ResponseWriter: w, commit: func() {
		// Bound a blocked downstream write/flush as well as upstream work.
		// net/http clears this deadline after finishing the response.
		_ = controller.SetWriteDeadline(state.Deadline)
		state.CommitOutput()
		emit(routercloud.Event{Kind: "output_committed", Operation: operationFor(sub), At: time.Now(), Attempt: state.Attempts, OutputCommitted: true})
	}}
	if targetRef, ok := resolveBoundTarget(selector.Model, state.Target, options); ok {
		return executeBoundTarget(w, prepared, protocolForSub(sub), sub, body, targetRef, options, emit, writer)
	}
	// W4 routing is enabled only when explicitly injected or when the user has
	// enabled the shared local runtime. The legacy cloud executor remains the
	// direct path for disabled/unconfigured local models.
	if options.shouldRoute() {
		return executeRoutedModelSub(w, prepared, protocolForSub(sub), sub, body, options, emit, writer)
	}
	executor := routercloud.Executor{Timeout: options.Budget, Observe: emit, BeforeAttempt: func(_ context.Context) error {
		if err := state.BeginCloudAttempt(); err != nil {
			safe := routerrequest.ClassifyError(err)
			return &routercloud.Error{Code: safe.Code, Message: safe.Message, Status: safe.Status}
		}
		return nil
	}}
	result, err := executor.ExecutePath(prepared.Context(), writer, "/v1/"+sub, routercloud.Request{Body: body, Target: target})
	result.Decision = string(DecisionCloud)
	if result.Attempts > 0 {
		result.Destination = string(DecisionCloud)
	}
	return result, err
}

func protocolForSub(sub string) string {
	switch sub {
	case "responses":
		return "responses"
	case "messages", "messages/count_tokens":
		return "anthropic"
	default:
		return "chat"
	}
}

func operationFor(sub string) string {
	if sub == "messages/count_tokens" {
		return "count_tokens"
	}
	return "generate"
}

// Mark commitment before the first attempted client write, including a failed
// write. After this point no retry or second JSON error envelope is safe.
type modelWriter struct {
	http.ResponseWriter
	committed bool
	commit    func()
}

func (w *modelWriter) mark() {
	if !w.committed {
		w.committed = true
		w.commit()
	}
}
func (w *modelWriter) WriteHeader(status int)      { w.mark(); w.ResponseWriter.WriteHeader(status) }
func (w *modelWriter) Write(b []byte) (int, error) { w.mark(); return w.ResponseWriter.Write(b) }
func (w *modelWriter) Flush() {
	w.mark()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
