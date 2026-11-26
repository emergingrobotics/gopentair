package gopentair

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/emergingrobotics/gopentair/internal/api"
	"github.com/emergingrobotics/gopentair/internal/ws"
)

// Source says where an Event came from.
type Source int

// Event sources.
const (
	SourceSnapshot Source = iota // initial or post-reconnect REST snapshot
	SourceWS                     // WebSocket push
	SourcePoll                   // fallback REST poll
)

func (s Source) String() string {
	switch s {
	case SourceSnapshot:
		return "snapshot"
	case SourceWS:
		return "ws"
	case SourcePoll:
		return "poll"
	}
	return "unknown"
}

// MarshalText implements encoding.TextMarshaler.
func (s Source) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// Event is a change to a device's merged state.
type Event struct {
	DeviceID string      `json:"deviceId"`
	Changed  []string    `json:"changed"`
	State    DeviceState `json:"state"`
	Source   Source      `json:"source"`
	At       time.Time   `json:"at"`
}

// WatchOption configures Watch.
type WatchOption func(*watchConfig)

type watchConfig struct {
	screens   []string
	poll      time.Duration
	pollSet   bool
	appUse    *bool
	appEvery  time.Duration
	filter    []string
	keepAlive time.Duration
	rotate    time.Duration
}

// WithScreens sets the app screens to subscribe to (default "poolScreen"
// and "product"; each opens one WebSocket).
func WithScreens(screens ...string) WatchOption {
	return func(c *watchConfig) { c.screens = screens }
}

// WithPollFallback sets the REST poll interval (default: client poll
// interval, 60s; minimum 30s). 0 disables polling.
func WithPollFallback(d time.Duration) WatchOption {
	if d > 0 && d < 30*time.Second {
		d = 30 * time.Second
	}
	return func(c *watchConfig) { c.poll, c.pollSet = d, true }
}

// WithAppUse enables the app's {"appuse":"1"} presence write every 60s
// (default: on when the "product" screen is watched).
func WithAppUse(on bool) WatchOption { return func(c *watchConfig) { c.appUse = &on } }

// WithFieldFilter only emits events when one of these codes changes.
func WithFieldFilter(codes ...string) WatchOption {
	return func(c *watchConfig) { c.filter = codes }
}

// WithKeepAlive sets the "keep-alive" text frame interval (default 10s).
func WithKeepAlive(d time.Duration) WatchOption { return func(c *watchConfig) { c.keepAlive = d } }

// withWatchTiming tunes internals (tests).
func withWatchTiming(appEvery, rotateBefore time.Duration, poll time.Duration) WatchOption {
	return func(c *watchConfig) {
		c.appEvery, c.rotate = appEvery, rotateBefore
		c.poll, c.pollSet = poll, true
	}
}

type watcher struct {
	c   *client
	cfg watchConfig
	ids []string

	mu      sync.Mutex
	states  map[string]*DeviceState
	pending map[string]*Event
	order   []string
	signal  chan struct{}
	out     chan Event
}

func (c *client) Watch(ctx context.Context, ids []string, opts ...WatchOption) (<-chan Event, error) {
	cfg := watchConfig{
		screens:   []string{"poolScreen", "product"},
		poll:      c.poll,
		keepAlive: 10 * time.Second,
		appEvery:  60 * time.Second,
		rotate:    2 * time.Minute,
	}
	for _, o := range opts {
		o(&cfg)
	}
	if !cfg.pollSet {
		cfg.poll = c.poll
	}
	if cfg.appUse == nil {
		cfg.appUse = ptr(slices.Contains(cfg.screens, "product"))
	}
	if len(ids) == 0 {
		return nil, ErrDeviceNotFound
	}
	initial, err := c.States(ctx, ids...)
	if err != nil {
		return nil, err
	}
	w := &watcher{
		c: c, cfg: cfg, ids: ids,
		states:  map[string]*DeviceState{},
		pending: map[string]*Event{},
		signal:  make(chan struct{}, 1),
		out:     make(chan Event, 64),
	}
	for i := range initial {
		st := initial[i].Clone()
		w.states[st.DeviceID] = &st
		w.queue(st.DeviceID, sortedKeys(st.Fields), SourceSnapshot, true)
	}
	go w.run(ctx)
	return w.out, nil
}

func sortedKeys(m map[string]Field) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

func (w *watcher) run(ctx context.Context) {
	var wg sync.WaitGroup
	start := func(f func(context.Context)) {
		wg.Add(1)
		go func() { defer wg.Done(); f(ctx) }()
	}
	for _, s := range w.cfg.screens {
		screen := s
		start(func(ctx context.Context) { w.connLoop(ctx, screen) })
	}
	if w.cfg.poll > 0 {
		start(w.pollLoop)
	}
	if *w.cfg.appUse {
		start(w.appUseLoop)
	}
	fan := make(chan struct{})
	go func() { w.fanOut(ctx); close(fan) }()
	wg.Wait()
	<-fan
	close(w.out)
}

// queue records an event, coalescing with any unsent event for the device.
func (w *watcher) queue(id string, changed []string, src Source, force bool) {
	w.mu.Lock()
	st := w.states[id]
	if st == nil {
		w.mu.Unlock()
		return
	}
	if !force && len(w.cfg.filter) > 0 && !slices.ContainsFunc(changed, func(c string) bool { return slices.Contains(w.cfg.filter, c) }) {
		w.mu.Unlock()
		return
	}
	now := w.c.now()
	if ev, ok := w.pending[id]; ok {
		for _, c := range changed {
			if !slices.Contains(ev.Changed, c) {
				ev.Changed = append(ev.Changed, c)
			}
		}
		ev.State, ev.Source, ev.At = st.Clone(), src, now
	} else {
		w.pending[id] = &Event{DeviceID: id, Changed: slices.Clone(changed), State: st.Clone(), Source: src, At: now}
		w.order = append(w.order, id)
	}
	w.mu.Unlock()
	select {
	case w.signal <- struct{}{}:
	default:
	}
}

func (w *watcher) fanOut(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.signal:
		}
		for {
			w.mu.Lock()
			if len(w.order) == 0 {
				w.mu.Unlock()
				break
			}
			id := w.order[0]
			w.order = w.order[1:]
			ev := w.pending[id]
			delete(w.pending, id)
			w.mu.Unlock()
			select {
			case w.out <- *ev:
			case <-ctx.Done():
				return
			}
		}
	}
}

// mergePush applies a WebSocket delta.
func (w *watcher) mergePush(ev *api.WSEvent) {
	w.mu.Lock()
	st := w.states[ev.DeviceID]
	if st == nil {
		w.mu.Unlock()
		return
	}
	changed := st.Merge(toFields(st.Type, ev.Data.Fields))
	if ev.Data.Online != nil && *ev.Data.Online != st.Online {
		st.Online = *ev.Data.Online
		changed = append(changed, "online")
	}
	if ev.Data.Alarm != nil && *ev.Data.Alarm != st.Alarm {
		st.Alarm = *ev.Data.Alarm
		changed = append(changed, "alarm")
	}
	if len(changed) > 0 {
		st.UpdatedAt = w.c.now()
	}
	w.mu.Unlock()
	if len(changed) > 0 {
		slices.Sort(changed)
		w.queue(ev.DeviceID, changed, SourceWS, false)
	}
}

// reconcile replaces state with a REST snapshot and emits drift.
func (w *watcher) reconcile(ctx context.Context, src Source) {
	states, err := w.c.States(ctx, w.ids...)
	if err != nil {
		if ctx.Err() == nil {
			w.c.log.Debug("watch: REST snapshot failed", "err", err)
		}
		return
	}
	for _, ns := range states {
		w.mu.Lock()
		st := w.states[ns.DeviceID]
		if st == nil {
			w.mu.Unlock()
			continue
		}
		var changed []string
		for code, f := range ns.Fields {
			if old, ok := st.Fields[code]; !ok || old.Value != f.Value {
				changed = append(changed, code)
			}
		}
		if ns.Online != st.Online {
			changed = append(changed, "online")
		}
		if ns.Alarm != st.Alarm {
			changed = append(changed, "alarm")
		}
		for code, f := range ns.Fields {
			st.Fields[code] = f
		}
		st.Online, st.Alarm, st.FWVersion = ns.Online, ns.Alarm, ns.FWVersion
		if len(changed) > 0 {
			st.UpdatedAt = w.c.now()
		}
		w.mu.Unlock()
		if len(changed) > 0 {
			slices.Sort(changed)
			w.queue(ns.DeviceID, changed, src, false)
		}
	}
}

func (w *watcher) pollLoop(ctx context.Context) {
	t := time.NewTicker(w.cfg.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.reconcile(ctx, SourcePoll)
		}
	}
}

func (w *watcher) appUseLoop(ctx context.Context) {
	first := min(5*time.Second, w.cfg.appEvery)
	t := time.NewTimer(first)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, id := range w.ids {
			if err := w.c.putFields(ctx, id, map[string]string{"appuse": "1"}); err != nil && ctx.Err() == nil {
				w.c.log.Debug("watch: appuse keepalive failed", "err", err)
			}
		}
		t.Reset(w.cfg.appEvery)
	}
}

type liveConn struct {
	conn   *ws.Conn
	cancel context.CancelFunc
	events chan *api.WSEvent
	errs   chan error
	expiry time.Time
}

func (l *liveConn) close() {
	l.cancel()
	_ = l.conn.Close()
}

func (w *watcher) connect(ctx context.Context, screen string) (*liveConn, error) {
	tok, exp, err := w.c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := ws.Dial(ctx, w.c.wsURL, tok, screen, w.c.hc)
	if err != nil {
		return nil, err
	}
	for _, id := range w.ids {
		if err := conn.Register(ctx, id, tok); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	cctx, cancel := context.WithCancel(ctx)
	l := &liveConn{conn: conn, cancel: cancel, events: make(chan *api.WSEvent, 16), errs: make(chan error, 1), expiry: exp}
	go func() {
		for {
			ev, err := conn.Read(cctx)
			if err != nil {
				l.errs <- err
				return
			}
			select {
			case l.events <- ev:
			case <-cctx.Done():
				return
			}
		}
	}()
	go func() {
		t := time.NewTicker(w.cfg.keepAlive)
		defer t.Stop()
		for {
			select {
			case <-cctx.Done():
				return
			case <-t.C:
				if err := conn.KeepAlive(cctx); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	w.c.log.Debug("watch: connected", "screen", screen)
	return l, nil
}

func (w *watcher) connLoop(ctx context.Context, screen string) {
	var cur *liveConn
	attempt := 0
	for cur == nil {
		l, err := w.connect(ctx, screen)
		if err == nil {
			cur = l
			break
		}
		if ctx.Err() != nil {
			return
		}
		w.c.log.Debug("watch: connect failed", "screen", screen, "err", err)
		if !sleepCtx(ctx, ws.Backoff(attempt)) {
			return
		}
		attempt++
	}
	rot := time.NewTimer(w.rotateDelay(cur))
	defer rot.Stop()
	for {
		select {
		case <-ctx.Done():
			cur.close()
			return
		case ev := <-cur.events:
			w.mergePush(ev)
		case err := <-cur.errs:
			cur.close()
			if ctx.Err() != nil {
				return
			}
			w.c.log.Debug("watch: connection lost", "screen", screen, "err", err)
			cur = nil
			attempt = 0
			for cur == nil {
				if !sleepCtx(ctx, ws.Backoff(attempt)) {
					return
				}
				attempt++
				l, err := w.connect(ctx, screen)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					w.c.log.Debug("watch: reconnect failed", "screen", screen, "err", err)
					continue
				}
				cur = l
			}
			rot.Reset(w.rotateDelay(cur))
			w.reconcile(ctx, SourceSnapshot)
		case <-rot.C:
			// Make-before-break: open the new connection with a fresh token
			// before closing the old one. The rotation point is inside the
			// auth refresh margin, so connect() obtains a fresh token.
			l, err := w.connect(ctx, screen)
			if err != nil {
				if ctx.Err() != nil {
					cur.close()
					return
				}
				w.c.log.Debug("watch: token rotation reconnect failed; retrying", "err", err)
				rot.Reset(30 * time.Second)
				continue
			}
			old := cur
			cur = l
			go w.drain(old)
			rot.Reset(w.rotateDelay(cur))
			w.c.log.Debug("watch: rotated connection", "screen", screen)
		}
	}
}

// rotateDelay is how long until a connection should be replaced: shortly
// before its access token expires (measured on the client's clock).
func (w *watcher) rotateDelay(l *liveConn) time.Duration {
	d := l.expiry.Add(-w.cfg.rotate).Sub(w.c.now())
	return max(d, time.Second)
}

// drain keeps merging events from a superseded connection for a short
// overlap (the new one may not be registered yet), then closes it.
func (w *watcher) drain(l *liveConn) {
	t := time.NewTimer(2 * time.Second)
	defer t.Stop()
	defer l.close()
	for {
		select {
		case ev := <-l.events:
			w.mergePush(ev)
		case <-l.errs:
			return
		case <-t.C:
			return
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
