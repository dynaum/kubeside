package api

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/dynaum/kubeside/internal/logs"
)

// One log feed is one workload's merged output, shared by every window watching
// it with the same filters. Two windows on the same deployment cost the kubelet
// one set of streams, not two; a window that changes a filter is a different
// subscription, so revealing sidecars in one place cannot change another.
//
// Log streams are the most expensive thing kubeside opens, so nothing streams
// unless a tab is subscribed and the last unsubscribe closes everything.

// logFeed is one merged log stream and its subscribers.
type logFeed struct {
	hub    *hub
	key    string
	req    ClientMessage
	cancel context.CancelFunc

	// subs is guarded by hub.mu, like every other feed's membership.
	subs map[*subscriber]struct{}

	mu       sync.Mutex
	streamer *logs.Streamer
	// sent is the buffer sequence already flushed, and edgesSent the number of
	// edges already delivered. Both are cursors into what the streamer holds.
	sent      int64
	edgesSent int
	seq       int64
}

func (h *hub) subscribeLogs(m ClientMessage, s *subscriber) error {
	if m.Namespace == "" || m.Workload == "" {
		return fmt.Errorf("subscribe to logs requires a namespace and a workload")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	key := m.subscriptionKey()
	f, ok := h.logFeeds[key]
	if !ok {
		ctx, cancel := context.WithCancel(context.Background())
		f = &logFeed{hub: h, key: key, req: m, cancel: cancel, subs: map[*subscriber]struct{}{}}
		h.logFeeds[key] = f
		go f.run(ctx)
	}
	f.subs[s] = struct{}{}
	f.attach(s)
	return nil
}

// attach hands a joining window everything already buffered, so a second tab
// sees the same scrollback as the first instead of starting from the next line.
//
// Lines carry their sequence, so the one batch a joiner may receive twice
// around a flush costs the client a lookup, not a duplicate line.
func (f *logFeed) attach(s *subscriber) {
	f.mu.Lock()
	st := f.streamer
	seq := f.seq
	f.mu.Unlock()
	if st == nil {
		return
	}

	lines, dropped := st.Buffer().Snapshot()
	batch := &LogsBatch{Dropped: dropped, Reset: true}
	for _, l := range lines {
		batch.Lines = append(batch.Lines, logLine(l))
	}
	for _, e := range st.Edges() {
		batch.Edges = append(batch.Edges, logEdge(e))
	}
	s.send(f.message(seq, batch))
}

func (f *logFeed) message(seq int64, batch *LogsBatch) ServerMessage {
	return ServerMessage{
		Type: msgLogs, View: ViewLogs, Context: f.req.Context, Key: f.key, Seq: seq, Logs: batch,
	}
}

func (f *logFeed) run(ctx context.Context) {
	src, err := f.hub.api.LogSource(f.req.Context, f.req.Namespace, f.req.Workload)
	if err != nil {
		// A workload that cannot be resolved will not resolve on a retry
		// either: it is the wrong name, or the cluster is unreachable and says
		// so elsewhere. The window is told why rather than left waiting.
		f.broadcast(ServerMessage{
			Type: msgError, View: ViewLogs, Context: f.req.Context, Key: f.key, Message: err.Error(),
		})
		f.hub.stopLogs(f)
		return
	}

	st := logs.NewStreamer(src, logs.Options{
		Containers:      f.req.Containers,
		Pods:            f.req.Pods,
		IncludeSidecars: f.req.IncludeSidecars,
		IncludeInit:     f.req.IncludeInit,
		Previous:        f.req.Previous,
		Tail:            f.req.Tail,
		Refresh:         f.hub.logRefresh(),
	})

	f.mu.Lock()
	f.streamer = st
	f.mu.Unlock()

	go st.Run(ctx)

	tick := time.NewTicker(f.hub.flushInterval())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			f.flush()
		}
	}
}

// flush sends whatever arrived since the last one, and nothing at all when
// nothing did.
func (f *logFeed) flush() {
	f.mu.Lock()
	st := f.streamer
	if st == nil {
		f.mu.Unlock()
		return
	}
	lines, dropped := st.Buffer().Since(f.sent)
	edges := st.Edges()
	fresh := edges[min(f.edgesSent, len(edges)):]

	if len(lines) == 0 && len(fresh) == 0 {
		f.mu.Unlock()
		return
	}

	batch := &LogsBatch{Dropped: dropped}
	for _, l := range lines {
		batch.Lines = append(batch.Lines, logLine(l))
		if l.Seq > f.sent {
			f.sent = l.Seq
		}
	}
	for _, e := range fresh {
		batch.Edges = append(batch.Edges, logEdge(e))
	}
	f.edgesSent = len(edges)
	f.seq++
	msg := f.message(f.seq, batch)
	f.mu.Unlock()

	f.broadcast(msg)
}

func (f *logFeed) broadcast(m ServerMessage) {
	for _, s := range f.hub.logSubscribers(f) {
		s.send(m)
	}
}

// stopLogs tears a log feed down and forgets it, closing every stream it held.
func (h *hub) stopLogs(f *logFeed) {
	h.mu.Lock()
	if h.logFeeds[f.key] == f {
		delete(h.logFeeds, f.key)
	}
	f.subs = map[*subscriber]struct{}{}
	h.mu.Unlock()
	f.cancel()
}
