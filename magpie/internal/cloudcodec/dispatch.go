// Package cloudcodec implements wire-only message translation. It performs no
// provider discovery, configuration reads, network calls or process lifecycle.
package cloudcodec

import "fmt"

// These dispatchers intentionally accept only the three cloud wire APIs. The
// legacy gateway retains its other protocol dispatch and delegates these same
// parsers, builders, encoders and renderers through compatibility aliases.
func cloudParse(protocol Protocol, body []byte) (*Request, error) {
	var request *Request
	var err error
	switch protocol {
	case Chat:
		request, err = ParseChat(body)
	case Responses:
		request, err = ParseResponses(body)
	case Anthropic:
		request, err = ParseAnthropic(body)
	default:
		return nil, fmt.Errorf("unsupported cloud protocol")
	}
	if err != nil {
		return nil, err
	}
	UnsignCalls(request)
	request.LastIsTurn = protocol == Chat || protocol == Responses
	if request.ToolChoice == "required" && len(request.Tools) == 0 && !request.WebSearch && RequiredAllowlist(protocol, body) {
		return nil, fmt.Errorf("required tool choice has no callable tools after filtering")
	}
	return request, nil
}

func cloudBuild(protocol Protocol, request *Request, model, host string, rejectTemperature bool) ([]byte, error) {
	switch protocol {
	case Chat:
		return BuildChat(request, model, host, rejectTemperature), nil
	case Responses:
		return BuildResponses(request, model, host, rejectTemperature), nil
	case Anthropic:
		replay := AnthropicReplaysThinking(host, model)
		body := AnthropicBody(request, model, replay)
		if replay {
			body = WithThinkingOnToolTurns(body)
		}
		return body, nil
	}
	return nil, fmt.Errorf("unsupported cloud protocol")
}

func cloudDecoder(protocol Protocol) func(string, func(Event)) error {
	switch protocol {
	case Chat:
		return (&ChatDecoder{}).Decode
	case Responses:
		return (&ResponsesDecoder{}).Decode
	default:
		return (&AnthropicDecoder{Server: map[int]bool{}, Whole: map[int]bool{}}).Decode
	}
}

func cloudEncoder(protocol Protocol, writer *SseWriter, request *Request) StreamEncoder {
	var encoder StreamEncoder
	switch protocol {
	case Chat:
		encoder = &ChatEncoder{W: writer, Model: request.Model}
	case Responses:
		encoder = &ResponsesEncoder{W: writer, Model: request.Model, Named: request.Namespaced}
	default:
		encoder = &AnthropicEncoder{W: writer, Model: request.Model}
	}
	if request.WantsJSON() {
		encoder = &FenceEncoder{StreamEncoder: encoder}
	}
	return encoder
}

func cloudRender(protocol Protocol, result Result, request *Request) []byte {
	result = UnfencedResult(result, request)
	switch protocol {
	case Chat:
		return RenderChat(result, request.Model)
	case Responses:
		return RenderResponses(result, request.Model, request.Namespaced)
	default:
		return RenderAnthropic(result, request.Model)
	}
}

func cloudReplyEnds(protocol Protocol, data, stop string) bool {
	switch protocol {
	case Anthropic:
		kind := EventType(data)
		return kind == "message_stop" || kind == "error" || kind == "message_delta" && stop != ""
	case Responses:
		kind := EventType(data)
		return kind == "error" || kind == "response.completed" || kind == "response.incomplete" || kind == "response.failed" || stop != ""
	}
	return stop != ""
}
