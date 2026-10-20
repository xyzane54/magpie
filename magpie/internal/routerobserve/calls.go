package routerobserve

import (
	"context"
	"sync"

	"github.com/yetone/magpie/internal/routerledger"
)

type callSink interface {
	RecordCall(routerledger.Call) error
	ReportObservationFailure()
}

type callMessage struct {
	call    *routerledger.Call
	barrier chan struct{}
	stop    bool
}

// Calls consumes safe completed calls on a bounded queue. Emit never performs
// persistence or waits for its worker, including while a model lock is held.
// Flush is an explicit diagnostic boundary, never part of model invocation.
type Calls struct {
	mu     sync.RWMutex
	closed bool
	sink   callSink
	queue  chan callMessage
	done   chan struct{}
}

func NewCalls(sink callSink, capacity int) *Calls {
	if capacity < 1 || capacity > 4096 {
		capacity = 256
	}
	c := &Calls{sink: sink, queue: make(chan callMessage, capacity), done: make(chan struct{})}
	go c.run()
	return c
}

func cloneUsage(u *routerledger.Usage) *routerledger.Usage {
	if u == nil {
		return nil
	}
	return &routerledger.Usage{InputTokens: copyCount(u.InputTokens), OutputTokens: copyCount(u.OutputTokens), TotalTokens: copyCount(u.TotalTokens)}
}

func cloneCall(v routerledger.Call) routerledger.Call {
	v.Usage = cloneUsage(v.Usage)
	if v.ContextUsage != nil {
		snapshot := *v.ContextUsage
		v.ContextUsage = &snapshot
	}
	v.Timings = routerledger.CallTimings{
		QueueLoadMS: copyCount(v.Timings.QueueLoadMS), QueueMS: copyCount(v.Timings.QueueMS),
		LoadMS: copyCount(v.Timings.LoadMS), InferenceMS: copyCount(v.Timings.InferenceMS),
		RequestHeadersMS: copyCount(v.Timings.RequestHeadersMS), ConsumeMS: copyCount(v.Timings.ConsumeMS), TotalMS: copyCount(v.Timings.TotalMS),
	}
	return v
}

func (c *Calls) Emit(v routerledger.Call) {
	if c == nil || c.sink == nil {
		return
	}
	v = cloneCall(v)
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		c.sink.ReportObservationFailure()
		return
	}
	select {
	case c.queue <- callMessage{call: &v}:
	default:
		c.sink.ReportObservationFailure()
	}
}

func (c *Calls) run() {
	defer close(c.done)
	for {
		m := <-c.queue
		if m.stop {
			return
		}
		if m.call != nil && c.sink != nil {
			if err := c.sink.RecordCall(*m.call); err != nil {
				c.sink.ReportObservationFailure()
			}
		}
		if m.barrier != nil {
			close(m.barrier)
		}
	}
}

func (c *Calls) Flush(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	closed := c.closed
	c.mu.RUnlock()
	if closed {
		select {
		case <-c.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	barrier := make(chan struct{})
	select {
	case c.queue <- callMessage{barrier: barrier}:
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-barrier:
		return nil
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close prevents further callbacks from enqueueing, then drains accepted events.
// The service calls it after handlers and its model manager have stopped.
func (c *Calls) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		<-c.done
		return
	}
	c.closed = true
	c.mu.Unlock()
	c.queue <- callMessage{stop: true}
	<-c.done
}
