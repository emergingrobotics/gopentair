package gopentair

import (
	"slices"
	"testing"
	"time"
)

func nextEvent(t *testing.T, ch <-chan Event, pred func(Event) bool) Event {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("channel closed")
			}
			if pred(ev) {
				return ev
			}
		case <-deadline:
			t.Fatal("timed out waiting for event")
		}
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestWatchPushAndReconnect(t *testing.T) {
	f := newFake(t)
	c := newTestClient(t, f)
	ctx := ctxT(t)
	ch, err := c.Watch(ctx, []string{pumpID, lightID}, WithPollFallback(0), WithAppUse(false), WithKeepAlive(20*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for len(seen) < 2 {
		ev := nextEvent(t, ch, func(e Event) bool { return e.Source == SourceSnapshot })
		seen[ev.DeviceID] = true
	}
	// Two screens x two devices registered.
	waitUntil(t, func() bool { return f.Subscribers() == 4 })

	f.SetField(pumpID, "s18", "800")
	ev := nextEvent(t, ch, func(e Event) bool { return e.Source == SourceWS && e.DeviceID == pumpID })
	if !slices.Contains(ev.Changed, "s18") || *DecodePump(ev.State).PowerW != 800 {
		t.Fatalf("event %+v", ev.Changed)
	}
	// Unchanged push (duplicate from the second screen) produces no event;
	// the next distinct change does.
	f.SetField(lightID, "d1", "13")
	ev = nextEvent(t, ch, func(e Event) bool { return e.DeviceID == lightID && e.Source == SourceWS })
	if *DecodeLight(ev.State).Show != ShowRoyal {
		t.Fatal("light show not merged")
	}
	waitUntil(t, func() bool { return f.Count("keepalive") > 0 })

	// Drop: client reconnects, re-registers and takes a snapshot that
	// picks up a change made while disconnected.
	connects := f.Count("ws-connect")
	f.DropConnections()
	f.SetFieldQuietly(pumpID, "s19", "333") // changed with no push
	ev = nextEvent(t, ch, func(e Event) bool { return e.Source == SourceSnapshot && e.DeviceID == pumpID })
	if !slices.Contains(ev.Changed, "s19") {
		t.Fatalf("resnapshot changed %v", ev.Changed)
	}
	if f.Count("ws-connect") <= connects {
		t.Fatal("no reconnect")
	}
}

func TestWatchFilterAndPoll(t *testing.T) {
	f := newFake(t)
	c := newTestClient(t, f)
	ctx := ctxT(t)
	ch, err := c.Watch(ctx, []string{pumpID}, WithScreens("poolScreen"), WithFieldFilter("s18"),
		withWatchTiming(time.Hour, 2*time.Minute, 100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	nextEvent(t, ch, func(e Event) bool { return e.Source == SourceSnapshot })
	waitUntil(t, func() bool { return f.Subscribers() == 1 })
	f.SetField(pumpID, "s19", "100")       // filtered out
	f.SetFieldQuietly(pumpID, "s18", "42") // no push: only the poll can see it
	ev := nextEvent(t, ch, func(e Event) bool { return true })
	if ev.Source != SourcePoll || !slices.Contains(ev.Changed, "s18") {
		t.Fatalf("got %s %v", ev.Source, ev.Changed)
	}
}

func TestWatchAppUseAndRotation(t *testing.T) {
	f := newFake(t)
	f.TokenTTL = 3 * time.Minute // rotation point (expiry-2m) is ~1 min away in fake time
	c := newTestClient(t, f)
	ctx := ctxT(t)
	if err := c.Login(ctx); err != nil {
		t.Fatal(err)
	}
	ch, err := c.Watch(ctx, []string{lightID}, WithScreens("product"), WithPollFallback(0),
		withWatchTiming(30*time.Millisecond, 2*time.Minute+59*time.Second, 0))
	if err != nil {
		t.Fatal(err)
	}
	nextEvent(t, ch, func(e Event) bool { return e.Source == SourceSnapshot })
	// appuse presence writes reach the device but are not recorded as writes.
	waitUntil(t, func() bool { return f.Field(lightID, "appuse") == "1" })
	if len(f.Writes()) != 0 {
		t.Fatalf("appuse recorded as user write: %+v", f.Writes())
	}
	// rotate=2m59s with a 3m token means rotation is due ~immediately:
	// the watcher opens a new connection with a refreshed token.
	waitUntil(t, func() bool { return f.Count("ws-connect") >= 2 && f.Count("refresh") >= 1 })
	f.SetField(lightID, "d13", "0")
	ev := nextEvent(t, ch, func(e Event) bool { return e.Source == SourceWS && slices.Contains(e.Changed, "d13") })
	if *DecodeLight(ev.State).On {
		t.Fatal("push after rotation not received")
	}
}
