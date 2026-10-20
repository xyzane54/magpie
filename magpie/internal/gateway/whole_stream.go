package gateway

import (
	"net/http"

	"github.com/yetone/magpie/internal/provider"
)

// wholeAsStream writes a reply its vendor gave whole, in proto's own shape,
// as proto's stream: the agent asked for a stream, and has the stream's
// headers already from keepAlive, so a whole reply can no longer go to it
// as one. Every item, block and part goes out as the vendor wrote it, in
// the events that stream it. It reports false, writing nothing, for a body
// that isn't such a reply.
func wholeAsStream(w http.ResponseWriter, proto provider.Protocol, b []byte) bool {
	events := wholeEvents(proto, b)
	if events == nil {
		return false
	}
	sw := newSSEWriter(w)
	sw.Begun = true // the 200 and its headers went out with the keepalives
	for _, e := range events {
		sw.Event(e.Name, e.Data)
	}
	return true
}
