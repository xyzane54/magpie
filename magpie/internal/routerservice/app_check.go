package routerservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/cloudcodec"
	"github.com/yetone/magpie/internal/routercloud"
	"github.com/yetone/magpie/internal/routerconfig"
	"github.com/yetone/magpie/internal/routerobserve"
	"github.com/yetone/magpie/internal/routerrequest"
)

const appCheckBudget = 20 * time.Second

// appCheckAPI checks an unsaved, exact editor target. The response contains no
// generated content or private provider data, and nothing is persisted.
func appCheckAPI(w http.ResponseWriter, r *http.Request, store *routerconfig.Store, token string, options ModelOptions) {
	acceptedAt := time.Now()
	budget := appCheckBudget
	if options.Budget > 0 && options.Budget < budget {
		budget = options.Budget
	}
	ctx, budgetCancel := context.WithTimeout(r.Context(), budget)
	defer budgetCancel()
	r = r.WithContext(ctx)
	var err error
	r, err = routerrequest.WithRequestID(r)
	if err != nil {
		appFailure(w, err)
		return
	}
	w.Header().Set("X-Request-ID", routerrequest.RequestID(r.Context()))
	fail := func(err error) {
		var cloud *routercloud.Error
		if errors.As(err, &cloud) {
			providerJSON(w, cloud.Status, map[string]any{"error": map[string]string{"code": cloud.Code, "message": cloud.Message}, "requestId": routerrequest.RequestID(r.Context())})
		} else {
			appFailure(w, err)
		}
	}
	if store == nil {
		fail(errors.New("store unavailable"))
		return
	}
	contentType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || contentType != "application/json" {
		fail(&routercloud.Error{Code: "unsupported_media_type", Message: "application/json required", Status: 415})
		return
	}
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	_ = controller.SetReadDeadline(deadline)
	var in routerconfig.AppEdit
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&in)
	if decodeErr == nil && decoder.Decode(new(any)) != io.EOF {
		decodeErr = errors.New("invalid JSON")
	}
	if decodeErr != nil {
		// Keep the read deadline and prevent net/http from draining a stalled
		// unread body before it writes this error response.
		w.Header().Set("Connection", "close")
		if !time.Now().Before(deadline) {
			// A socket timeout can cancel the parent before its timer fires.
			// Reaching the shared deadline is budget expiry, not client cancel.
			fail(context.DeadlineExceeded)
		} else if ctx.Err() != nil {
			fail(ctx.Err())
		} else {
			fail(&routercloud.Error{Code: "invalid_request", Message: "invalid application check JSON", Status: 400})
		}
		return
	}
	_ = controller.SetReadDeadline(time.Time{})
	if in.CredentialMode == routerconfig.ForwardAuthorization {
		fail(&routercloud.Error{Code: "connection_check_unsupported", Message: "dynamic business authorization requires a real application request", Status: 409})
		return
	}
	// Redaction capability context is server-owned and must match the same
	// current runtime projection used by the save endpoint. In particular, a
	// formally accepted suite permits the exact same draft during read-only
	// checking; clients cannot claim acceptance and checks never save it.
	capability := routerconfig.RedactionUnavailable
	if options.Routing != nil {
		if production, ok := options.Routing.Redactor.(*runtimeRedactor); ok {
			projection := production.state()
			switch {
			case projection.Experimental && projection.TrialReady:
				capability = routerconfig.RedactionExperimental
			case !projection.Experimental && projection.RecognitionReady:
				capability = routerconfig.RedactionAvailable
			}
		}
	}
	target, err := store.ResolveAppDraftWithRedactionCapabilityContext(r.Context(), in, token, capability)
	if err != nil {
		fail(err)
		return
	}
	protocol, sub := "chat", "chat/completions"
	switch in.ID {
	case "codex":
		protocol, sub = "responses", "responses"
	case "claude-code":
		protocol, sub = "anthropic", "messages"
	}
	// Deliberately ask for a tiny text answer through the application's own wire
	// protocol. This does not verify a host's version, tools or extended inputs.
	prompt := "Reply with OK."
	checkModel := target.Profile.Cloud.Model
	if checkModel == "" && len(target.Profile.Models) > 0 {
		checkModel = target.Profile.Models[0]
	}
	body := map[string]any{"model": checkModel, "stream": true}
	switch protocol {
	case "responses":
		body["input"], body["max_output_tokens"] = prompt, 256
	case "anthropic":
		body["messages"], body["max_tokens"] = []map[string]string{{"role": "user", "content": prompt}}, 256
	default:
		msg := map[string]string{"role": "user", "content": prompt}
		if in.ID == "workbuddy" {
			msg["agent"] = "connection-check"
		}
		body["messages"], body["max_tokens"] = []map[string]string{msg}, 256
		if in.ID == "loomy" {
			body["$comment"] = "connection-check"
		}
	}
	payload, _ := json.Marshal(body)
	prepared, cancel, err := routerrequest.PrepareResolved(r, target, budget)
	if err != nil {
		fail(err)
		return
	}
	defer cancel()
	options.Budget = budget
	options.purpose = "connection_check"
	options.store = store
	options.managementToken = token
	observation := beginRequestObservation(w, r, target.Profile, sub, acceptedAt, options)
	prepared = prepared.WithContext(routerobserve.WithLocalObserver(prepared.Context(), options.LocalCalls, "", true, nil))
	if observation != nil {
		defer observation.End("interrupted", "request_interrupted", "interrupted")
	}
	captured := newCheckOutput()
	result, err := executePreparedModel(captured, prepared, sub, payload, observingModelOptions(options, observation))
	state, _ := routerrequest.FromContext(prepared.Context())
	if err == nil {
		err = checkTextReply(protocol, captured)
	}
	final := ""
	stage := "completed"
	if err != nil {
		stage = "failed"
		var cloud *routercloud.Error
		if errors.As(err, &cloud) {
			final = cloud.Code
		} else {
			final = routerrequest.ClassifyError(err).Code
		}
		if final == "request_canceled" {
			stage = "canceled"
		}
	}
	state.Stage = stage
	if observation != nil {
		if state.Target.Profile.Cloud.ProviderID != "" || state.Target.Profile.Cloud.Model != "" {
			observation.SetCloudTarget(state.Target.Profile.Cloud.ProviderID, state.Target.Profile.Cloud.Model, nil)
		}
		_ = observation.Finish(result, err)
	}
	if options.Complete != nil {
		options.Complete(ModelCompletion{Purpose: modelPurpose(options), RequestID: state.ID, AppID: state.AppID, Stage: stage, ErrorCode: final, Result: result})
	}
	if err != nil {
		fail(err)
		return
	}
	verifiedModel := state.Target.Profile.Cloud.Model
	if verifiedModel == "" {
		verifiedModel = checkModel
	}
	providerJSON(w, 200, map[string]any{"verified": true, "providerId": state.Target.Provider.Config.ID, "providerRevision": state.Target.Provider.Config.Revision, "protocol": protocol, "appId": in.ID, "model": verifiedModel})
}

// checkOutput keeps a strict memory cap while discarding all provider text when
// the operation ends. It is never used as an actual client response writer.
type checkOutput struct {
	header http.Header
	body   bytes.Buffer
}

func newCheckOutput() *checkOutput         { return &checkOutput{header: make(http.Header)} }
func (w *checkOutput) Header() http.Header { return w.header }
func (w *checkOutput) WriteHeader(int)     {}
func (w *checkOutput) Flush()              {}
func (w *checkOutput) Write(b []byte) (int, error) {
	if w.body.Len()+len(b) > 1<<20 {
		return 0, io.ErrShortBuffer
	}
	return w.body.Write(b)
}

func checkTextReply(protocol string, captured *checkOutput) error {
	invalid := &routercloud.Error{Code: "connection_check_no_text", Message: "model check did not complete with nonempty text; select a text model or review provider settings", Status: 502}
	// Reuse the same codec to require a complete successful SSE response and
	// normalize the three protocols into one Responses text container.
	if !strings.HasPrefix(captured.header.Get("Content-Type"), "text/event-stream") {
		return invalid
	}
	codec, err := cloudcodec.NewCloudCodec("responses", []byte(`{"input":"Reply with OK.","stream":false}`))
	if err != nil {
		return invalid
	}
	normalized := newCheckOutput()
	if _, err := codec.Reply(normalized, protocol, captured.header.Get("Content-Type"), bytes.NewReader(captured.body.Bytes())); err != nil {
		return invalid
	}
	var reply struct {
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if json.Unmarshal(normalized.body.Bytes(), &reply) != nil {
		return invalid
	}
	for _, item := range reply.Output {
		for _, part := range item.Content {
			if item.Type == "message" && part.Type == "output_text" && strings.TrimSpace(part.Text) != "" {
				return nil
			}
		}
	}
	return invalid
}
