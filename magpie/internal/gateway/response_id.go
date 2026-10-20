package gateway

import "github.com/yetone/magpie/internal/provider"

// usageEncoder reads the encoder's chosen ID after it starts writing. Upstream
// IDs can be absent or acquire a protocol prefix, so KStart.MsgID is not enough.
// In multi-round replies the encoder keeps the single client response ID.
type usageEncoder struct {
	streamEncoder
	usage *Usage
}

func (e *usageEncoder) capture() {
	var id string
	switch enc := e.streamEncoder.(type) {
	case *responsesEncoder:
		id = enc.Id
	case *anthropicEncoder:
		id = enc.Id
	case *chatEncoder:
		id = enc.Id
	case *geminiEncoder:
		id = enc.id
	}
	if id != "" {
		e.usage.ResponseID = id
	}
}

func (e *usageEncoder) Event(ev Event) {
	e.streamEncoder.Event(ev)
	e.capture()
}

func (e *usageEncoder) Finish() {
	e.streamEncoder.Finish()
	e.capture()
}

// Parse the already-rendered object: rendering again would generate a different
// ID for upstreams that don't supply one.
func renderUsage(proto provider.Protocol, res Result, req *Request, usage *Usage) []byte {
	out := render(proto, res, req)
	sniff := newSniffer(proto, "application/json")
	sniff.write(out)
	usage.ResponseID = sniff.usage().ResponseID
	return out
}
