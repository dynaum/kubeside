package api

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/dynaum/kubeside/internal/logs"
)

// The live path. One websocket per browser tab; one poller per watched view,
// shared by every tab watching it.
//
// The source of truth is currently a periodic read, not an informer. That is
// deliberate: the wire protocol is deltas from the first commit, so swapping the
// source for informer-backed watches (issue #23) changes what fills a patch, not
// what a patch is, and no client code moves.
//
// Nothing is polled unless a tab is subscribed. Attention is what makes a
// cluster active, which is also what keeps a kubeconfig with thirty contexts
// from waking thirty apiservers because a window opened.

const (
	// DefaultPollInterval is how often a watched view is re-read.
	DefaultPollInterval = 5 * time.Second

	// sendBuffer is how many messages may queue for one tab before it is
	// disconnected. Deep enough to absorb a burst, shallow enough that a dead
	// tab cannot grow memory without bound.
	sendBuffer = 64

	// writeTimeout bounds a single frame write, so one wedged socket cannot
	// hold a feed goroutine forever.
	writeTimeout = 10 * time.Second

	// pingInterval keeps the connection alive through anything in between and
	// detects a tab that vanished without closing.
	pingInterval = 30 * time.Second

	// readLimit caps a client message. Subscriptions are a few dozen bytes.
	readLimit = 32 * 1024

	// maxLogFlush is the longest a merged log line waits for its batch. Short
	// enough that follow mode reads as live, long enough that a chatty
	// workload does not send one frame per line.
	maxLogFlush = 200 * time.Millisecond
)

// subscriber is one tab's outbound queue.
type subscriber struct {
	ch   chan ServerMessage
	done chan struct{}

	once sync.Once
	over atomic.Bool
}

func newSubscriber(buf int) *subscriber {
	return &subscriber{ch: make(chan ServerMessage, buf), done: make(chan struct{})}
}

// send never blocks. A tab that cannot keep up is disconnected rather than
// allowed to stall the feed everyone else shares; it reconnects and receives a
// fresh snapshot, which is cheaper than a queue nobody drains.
func (s *subscriber) send(m ServerMessage) {
	if s.over.Load() {
		return
	}
	select {
	case s.ch <- m:
	case <-s.done:
	default:
		s.over.Store(true)
		s.close()
	}
}

func (s *subscriber) close() { s.once.Do(func() { close(s.done) }) }

func (s *subscriber) overflowed() bool { return s.over.Load() }

// feed is one watched view of one context, with the subscribers watching it.
type feed struct {
	hub     *hub
	key     string
	view    string
	context string
	cancel  context.CancelFunc

	// subs is guarded by hub.mu, so membership and the feed registry move
	// together and a subscriber cannot attach to a feed that is being stopped.
	subs map[*subscriber]struct{}

	mu   sync.Mutex
	last AppsView
	has  bool
	seq  int64
}

// hub owns every feed. Lock order is hub.mu then feed.mu, never the reverse.
type hub struct {
	api      API
	interval time.Duration

	mu       sync.Mutex
	feeds    map[string]*feed
	logFeeds map[string]*logFeed
}

func newHub(a API, interval time.Duration) *hub {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	return &hub{api: a, interval: interval, feeds: map[string]*feed{}, logFeeds: map[string]*logFeed{}}
}

// subscribe attaches a tab to a view, starting the reader if this is the first
// watcher. A subscriber that arrives mid-stream gets the current state
// immediately, so a second tab does not stare at nothing until the next poll.
func (h *hub) subscribe(m ClientMessage, s *subscriber) error {
	if m.Context == "" {
		return fmt.Errorf("subscribe to %s requires a context", m.View)
	}
	switch m.View {
	case ViewApps:
		return h.subscribeApps(m, s)
	case ViewLogs:
		return h.subscribeLogs(m, s)
	default:
		return fmt.Errorf("unknown view %q", m.View)
	}
}

func (h *hub) subscribeApps(m ClientMessage, s *subscriber) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	key := m.subscriptionKey()
	f, ok := h.feeds[key]
	if !ok {
		ctx, cancel := context.WithCancel(context.Background())
		f = &feed{
			hub:     h,
			key:     key,
			view:    m.View,
			context: m.Context,
			cancel:  cancel,
			subs:    map[*subscriber]struct{}{},
		}
		h.feeds[key] = f
		go f.run(ctx)
	}
	f.subs[s] = struct{}{}

	f.mu.Lock()
	if f.has {
		snap := f.last
		s.send(ServerMessage{Type: msgSnapshot, View: m.View, Context: m.Context, Key: f.key, Seq: f.seq, Snapshot: &snap})
	}
	f.mu.Unlock()
	return nil
}

// unsubscribe detaches a tab, stopping the reader when the last one leaves.
func (h *hub) unsubscribe(m ClientMessage, s *subscriber) {
	key := m.subscriptionKey()

	h.mu.Lock()
	defer h.mu.Unlock()

	if f, ok := h.feeds[key]; ok {
		delete(f.subs, s)
		if len(f.subs) == 0 {
			delete(h.feeds, f.key)
			f.cancel()
		}
		return
	}
	if lf, ok := h.logFeeds[key]; ok {
		delete(lf.subs, s)
		if len(lf.subs) == 0 {
			delete(h.logFeeds, lf.key)
			lf.cancel()
		}
	}
}

// subscribers copies the current membership so a feed can broadcast without
// holding the hub lock across a send.
func (h *hub) subscribers(f *feed) []*subscriber {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*subscriber, 0, len(f.subs))
	for s := range f.subs {
		out = append(out, s)
	}
	return out
}

func (h *hub) logSubscribers(f *logFeed) []*subscriber {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*subscriber, 0, len(f.subs))
	for s := range f.subs {
		out = append(out, s)
	}
	return out
}

// flushInterval batches log lines. A workload emitting thousands of lines a
// second must not cost one websocket frame per line.
func (h *hub) flushInterval() time.Duration {
	if h.interval < maxLogFlush {
		return h.interval
	}
	return maxLogFlush
}

// logRefresh is how often a log feed re-reads which pods back the workload.
// Slower than the apps poll: a rollout takes longer than a few seconds, and
// this read is a full snapshot until informers land.
func (h *hub) logRefresh() time.Duration {
	if h.interval < logs.DefaultRefresh {
		return h.interval
	}
	return logs.DefaultRefresh
}

// stop tears a feed down and forgets it, so a later subscription starts clean.
func (h *hub) stop(f *feed) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.feeds[f.key] == f {
		delete(h.feeds, f.key)
	}
	f.subs = map[*subscriber]struct{}{}
	f.cancel()
}

// run reads the view until the last subscriber leaves.
func (f *feed) run(ctx context.Context) {
	for {
		v, err := f.hub.api.Apps(f.context)
		if err != nil {
			// A failed read here is a permanent condition, not a flaky one:
			// Apps reports an unreachable cluster inside the view, with its
			// state, and only returns an error for a context that does not
			// exist. Retrying that forever would be noise.
			f.broadcast(ServerMessage{Type: msgError, View: f.view, Context: f.context, Message: err.Error()})
			f.hub.stop(f)
			return
		}
		f.publish(v)

		select {
		case <-ctx.Done():
			return
		case <-time.After(f.hub.interval):
		}
	}
}

// publish turns a fresh read into either the first snapshot or a patch, and
// sends nothing at all when nothing moved.
func (f *feed) publish(v AppsView) {
	f.mu.Lock()
	var msg ServerMessage
	switch {
	case !f.has:
		f.has, f.last = true, v
		f.seq++
		snap := v
		msg = ServerMessage{Type: msgSnapshot, View: f.view, Context: f.context, Key: f.key, Seq: f.seq, Snapshot: &snap}
	default:
		patch, changed := diffApps(f.last, v)
		f.last = v
		if !changed {
			f.mu.Unlock()
			return
		}
		f.seq++
		msg = ServerMessage{Type: msgPatch, View: f.view, Context: f.context, Key: f.key, Seq: f.seq, Patch: &patch}
	}
	f.mu.Unlock()

	f.broadcast(msg)
}

func (f *feed) broadcast(m ServerMessage) {
	for _, s := range f.hub.subscribers(f) {
		s.send(m)
	}
}

// handleStream upgrades one tab.
//
// The upgrade passes through the same middleware as every other request, so
// Origin, Host, and the session token are already checked by the time this
// runs. That matters more here than anywhere else: the browser's same-origin
// policy does not cover websockets, and an unguarded upgrade would hand any
// page on the internet a live channel to the user's clusters.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	// No OriginPatterns: the library's default requires Origin to equal Host,
	// which is stricter than the loopback check the middleware already applied,
	// and both must pass.
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept has already written the failure.
	}
	c.SetReadLimit(readLimit)
	defer c.CloseNow()

	sub := newSubscriber(sendBuffer)
	sc := &streamConn{ws: c, hub: s.hub, sub: sub, held: map[string]ClientMessage{}}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go sc.write(ctx, cancel)
	sc.read(ctx)
	sc.detachAll()
	sub.close()
}

// streamConn is one tab: a read pump, a write pump, and the set of
// subscriptions it holds so closing the tab releases them.
type streamConn struct {
	ws  *websocket.Conn
	hub *hub
	sub *subscriber

	mu   sync.Mutex
	held map[string]ClientMessage
}

func (c *streamConn) read(ctx context.Context) {
	for {
		var m ClientMessage
		if err := wsjson.Read(ctx, c.ws, &m); err != nil {
			return
		}
		switch m.Type {
		case msgSubscribe:
			if err := c.hub.subscribe(m, c.sub); err != nil {
				c.sub.send(ServerMessage{Type: msgError, View: m.View, Context: m.Context, Key: m.subscriptionKey(), Message: err.Error()})
				continue
			}
			c.mu.Lock()
			c.held[m.subscriptionKey()] = m
			c.mu.Unlock()
		case msgUnsubscribe:
			c.hub.unsubscribe(m, c.sub)
			c.mu.Lock()
			delete(c.held, m.subscriptionKey())
			c.mu.Unlock()
		default:
			c.sub.send(ServerMessage{Type: msgError, Message: fmt.Sprintf("unknown message type %q", m.Type)})
		}
	}
}

func (c *streamConn) write(ctx context.Context, cancel context.CancelFunc) {
	defer cancel()
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.sub.done:
			// Only an overflow closes the queue from underneath the writer.
			// Naming the reason lets the client resubscribe knowingly instead
			// of guessing at a dropped connection.
			_ = c.ws.Close(websocket.StatusTryAgainLater, "delta backlog; resubscribe")
			return
		case m := <-c.sub.ch:
			wctx, wcancel := context.WithTimeout(ctx, writeTimeout)
			err := wsjson.Write(wctx, c.ws, m)
			wcancel()
			if err != nil {
				return
			}
		case <-ping.C:
			pctx, pcancel := context.WithTimeout(ctx, writeTimeout)
			err := c.ws.Ping(pctx)
			pcancel()
			if err != nil {
				return
			}
		}
	}
}

// detachAll releases every subscription this tab held, which is what stops the
// poller for a context nobody is watching any more.
func (c *streamConn) detachAll() {
	c.mu.Lock()
	held := make([]ClientMessage, 0, len(c.held))
	for _, m := range c.held {
		held = append(held, m)
	}
	c.held = map[string]ClientMessage{}
	c.mu.Unlock()

	for _, m := range held {
		c.hub.unsubscribe(m, c.sub)
	}
}
