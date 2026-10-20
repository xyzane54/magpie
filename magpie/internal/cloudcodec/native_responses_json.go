package cloudcodec

import "encoding/json"

func (c *CloudCodec) nativeResponsesJSON(out *CloudWriter, body []byte) (CloudReply, error) {
	var reply CloudReply
	var response Obj
	var output []Obj
	if json.Unmarshal(body, &response) != nil || Str(response, "id") == "" || json.Unmarshal(response["output"], &output) != nil || validateNativeOutput(output) != nil {
		return reply, ErrCloudReply
	}
	var usage RUsage
	json.Unmarshal(response["usage"], &usage)
	reply.Usage, reply.UsageKnown = usage.Usage(), CloudUsageFields(Responses, body) == 3
	if !c.request.Stream {
		out.Header().Set("Content-Type", "application/json")
		_, err := out.Write(body)
		return reply, err
	}
	// A JSON response may expand several times in SSE: deltas, part/item
	// snapshots and the final response each carry data. Check the exact wire
	// size before committing headers, then regenerate one event at a time.
	wireBytes := 0
	if err := ResponsesWholeEventsEach(body, func(event WholeEvent) error {
		encoded, err := json.Marshal(event.Data)
		if err != nil {
			return ErrCloudReply
		}
		wireBytes += len(encoded) + len(event.Name) + 16
		if wireBytes > CloudReplyLimit {
			return ErrCloudReply
		}
		return nil
	}); err != nil {
		return reply, ErrCloudReply
	}
	sw := NewSSEWriter(out)
	if err := ResponsesWholeEventsEach(body, func(event WholeEvent) error {
		encoded, err := json.Marshal(event.Data)
		if err != nil {
			return ErrCloudReply
		}
		sw.Event(event.Name, encoded)
		return out.err
	}); err != nil {
		return reply, ErrCloudReply
	}
	return reply, nil
}
