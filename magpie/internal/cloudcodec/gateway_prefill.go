package cloudcodec

// continueText is the user turn asked after a conversation the client
// ended with the assistant's message, for a model that takes no prefill.
const ContinueText = "Continue."

// endsWithAssistant says whether r's conversation ends with the
// assistant's message rather than a call it made: what a prefill is.
func (r *Request) EndsWithAssistant() bool {
	n := len(r.Messages)
	if n == 0 || r.Messages[n-1].Role != "assistant" {
		return false
	}
	for _, p := range r.Messages[n-1].Parts {
		if p.Kind == ToolCall {
			return false
		}
	}
	return true
}

// userLast is r with a user turn after its last, the assistant's,
// message.
func (r *Request) UserLast() *Request {
	q := *r
	q.Messages = append(q.Messages[:len(q.Messages):len(q.Messages)], Message{Role: "user", Parts: []Part{{Kind: Text, Text: ContinueText}}})
	return &q
}
