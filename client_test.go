package gopentair

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emergingrobotics/gopentair/internal/testutil"
)

const (
	testUser = "pool@example.com"
	testPass = "Correct-Horse-1"
	pumpID   = "dev-pump-1"
	poolID   = "dev-pool-1"
	lightID  = "dev-light-1"
)

func newFake(t *testing.T) *testutil.Fake {
	t.Helper()
	f := testutil.New(t)
	f.AddUser(testUser, testPass)
	f.AddDevice(&testutil.Device{ID: pumpID, Type: "IF31", Nickname: "Main Pump", Pname: "IntelliFlo 3", Online: true, Fields: testutil.PumpFields()})
	f.AddDevice(&testutil.Device{ID: poolID, Type: "PIF0", Nickname: "Backyard", Pname: "IntelliConnect", Online: true, Fields: testutil.PoolFields()})
	f.AddDevice(&testutil.Device{ID: lightID, Type: "PLC1", Nickname: "Pool Lights", Pname: "Color Sync", Online: true, Fields: testutil.LightFields()})
	return f
}

func newTestClient(t *testing.T, f *testutil.Fake, extra ...Option) *client {
	t.Helper()
	rest, ws, idp, ident := f.URLs()
	opts := append([]Option{
		WithUsername(testUser),
		WithPassword(testPass),
		WithRegion(Region{Name: "test", AWSRegion: f.Region, UserPoolID: f.PoolID, ClientID: f.ClientID, IdentityPoolID: f.IdentityPoolID}),
		WithBaseURLs(BaseURLs{REST: rest, WS: ws, IdP: idp, Identity: ident}),
		WithClock(f.Clock.Now),
		WithTimeout(5 * time.Second),
		WithRetryCount(2),
	}, extra...)
	c, err := NewClient(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c.(*client)
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestLoginAndRead(t *testing.T) {
	f := newFake(t)
	c := newTestClient(t, f)
	ctx := ctxT(t)
	if err := c.Login(ctx); err != nil {
		t.Fatal(err)
	}
	if f.Count("srp") != 1 || f.Count("identity") != 1 {
		t.Fatalf("srp=%d identity=%d", f.Count("srp"), f.Count("identity"))
	}
	devs, err := c.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 3 || devs[0].Type != TypeIntelliFlo3 || devs[0].Nickname != "Main Pump" {
		t.Fatalf("devices: %+v", devs)
	}
	st, err := c.State(ctx, pumpID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Type != TypeIntelliFlo3 || st.Fields["s18"].Name != "Current power" {
		t.Fatalf("state type/name: %v %q", st.Type, st.Fields["s18"].Name)
	}
	// Body must use the plural deviceIds list.
	if !strings.Contains(string(f.LastBody("/device2/device2-service/user/device")), `"deviceIds":["dev-pump-1"]`) {
		t.Fatalf("body: %s", f.LastBody("/device2/device2-service/user/device"))
	}
	p, err := c.Profile(ctx)
	if err != nil || p.Email != testUser {
		t.Fatalf("profile %+v %v", p, err)
	}
	if _, err := c.State(ctx, "nope"); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestWrongPassword(t *testing.T) {
	f := newFake(t)
	c := newTestClient(t, f, WithPassword("wrong"))
	err := c.Login(ctxT(t))
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("got %v", err)
	}
	c2 := newTestClient(t, f, WithUsername("nobody@example.com"))
	if err := c2.Login(ctxT(t)); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user: %v", err)
	}
}

func TestResumeRefreshAndReauth(t *testing.T) {
	f := newFake(t)
	store := MemoryTokenStore()
	ctx := ctxT(t)
	c1 := newTestClient(t, f, WithTokenStore(store))
	if err := c1.Login(ctx); err != nil {
		t.Fatal(err)
	}
	// Second client, no password: resumes from the store, no SRP.
	c2 := newTestClient(t, f, WithTokenStore(store), WithPassword(""))
	if err := c2.Login(ctx); err != nil {
		t.Fatal(err)
	}
	if f.Count("srp") != 1 {
		t.Fatalf("expected resume without SRP, srp=%d", f.Count("srp"))
	}
	// Past JWT expiry: refresh token is used, and new AWS creds minted.
	f.Clock.Advance(58 * time.Minute)
	if _, err := c2.Devices(ctx); err != nil {
		t.Fatal(err)
	}
	if f.Count("refresh") != 1 || f.Count("identity") != 3 {
		t.Fatalf("refresh=%d identity=%d", f.Count("refresh"), f.Count("identity"))
	}
	// Revoked refresh token, no password: ErrReauthRequired.
	f.RevokeRefreshTokens()
	f.Clock.Advance(58 * time.Minute)
	if _, err := c2.Devices(ctx); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("want reauth, got %v", err)
	}
	// With a password the client silently logs in again.
	if _, err := c1.Devices(ctx); err != nil {
		t.Fatalf("c1 should re-SRP: %v", err)
	}
	if f.Count("srp") != 2 {
		t.Fatalf("srp=%d", f.Count("srp"))
	}
}

func TestExpiredCredentialsRetriedOnce(t *testing.T) {
	f := newFake(t)
	c := newTestClient(t, f)
	ctx := ctxT(t)
	if err := c.Login(ctx); err != nil {
		t.Fatal(err)
	}
	f.ExpireCredentials()
	if _, err := c.Devices(ctx); err != nil {
		t.Fatalf("expected transparent recovery: %v", err)
	}
	if f.Count("identity") != 2 {
		t.Fatalf("identity=%d", f.Count("identity"))
	}
}

func TestRetryPolicy(t *testing.T) {
	f := newFake(t)
	c := newTestClient(t, f)
	ctx := ctxT(t)
	_ = c.Login(ctx)
	// Reads retry through transient 503s.
	f.FailNext(testutil.Failure{Status: 503, Body: "busy"}, testutil.Failure{Status: 503, Body: "busy"})
	if _, err := c.State(ctx, pumpID); err != nil {
		t.Fatalf("read should retry: %v", err)
	}
	// Writes do not retry and are flagged ambiguous.
	f.FailNext(testutil.Failure{Status: 503, Body: "busy", Method: "PUT"})
	_, err := c.SetFields(ctx, lightID, map[string]string{"d13": "0"})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 503 || !ae.Ambiguous {
		t.Fatalf("want ambiguous 503, got %v", err)
	}
	if strings.Contains(err.Error(), lightID) {
		t.Fatalf("error leaks device id: %v", err)
	}
	if len(f.Writes()) != 0 {
		t.Fatal("write should not have been retried")
	}
	// Non-transient errors are returned immediately.
	f.FailNext(testutil.Failure{Status: 400, Body: `{"message":"bad"}`})
	if _, err := c.State(ctx, pumpID); !errors.As(err, &ae) || ae.Status != 400 || ae.Message != "bad" {
		t.Fatalf("got %v", err)
	}
}

func TestStandardSignProfile(t *testing.T) {
	f := newFake(t)
	c := newTestClient(t, f, WithSignProfile("standard"))
	ctx := ctxT(t)
	if _, err := c.SetFields(ctx, lightID, map[string]string{"d13": "0"}); err != nil {
		t.Fatal(err)
	}
}

func TestNotAcknowledged(t *testing.T) {
	f := newFake(t)
	c := newTestClient(t, f)
	f.SetAckCode("set_device_failed")
	_, err := c.SetFields(ctxT(t), lightID, map[string]string{"d13": "0"})
	var pe *PlanError
	if !errors.Is(err, ErrNotAcknowledged) || !errors.As(err, &pe) || pe.Applied != 0 {
		t.Fatalf("got %v", err)
	}
}

func TestPumpStartDebounceAndConfirm(t *testing.T) {
	f := newFake(t)
	// Emulate firmware: enabling program N makes it active.
	f.OnWrite = func(d *testutil.Device, fields map[string]string) {
		for k, v := range fields {
			var n int
			if _, err := fmtSscanf(k, "zp%de10", &n); err == nil && v == "3" {
				d.Fields["s14"] = testutil.Itoa(n - 1)
			}
		}
	}
	c := newTestClient(t, f)
	ctx := ctxT(t)
	pump := c.Pump(pumpID)
	plan, err := pump.StartPlan(ctx, "spa")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "zp3e10=3") || !strings.Contains(plan.String(), "p2=99") {
		t.Fatalf("plan:\n%s", plan)
	}
	// Dry run sends nothing.
	if res, err := c.Apply(ctx, plan, WithDryRun()); err != nil || !res.DryRun || len(f.Writes()) != 0 {
		t.Fatalf("dry run: %+v %v", res, err)
	}
	res, err := pump.Start(ctx, "3", WithConfirmDefault(), WithConfirmPoll(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Confirmed || res.Applied != 2 {
		t.Fatalf("res %+v", res)
	}
	w := f.Writes()
	if len(w) != 2 || w[0].Fields["zp3e10"] != "3" || w[1].Fields["p2"] != "99" {
		t.Fatalf("writes %+v", w)
	}
	st, _, _ := pump.Status(ctx)
	if st.ActiveProgram == nil || *st.ActiveProgram != 3 {
		t.Fatalf("active %v", st.ActiveProgram)
	}
	// Second start inside the debounce window is refused.
	if _, err := pump.Start(ctx, "Cleaning"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("want rate limited, got %v", err)
	}
	f.Clock.Advance(31 * time.Second)
	if _, err := pump.Start(ctx, "Cleaning"); err != nil {
		t.Fatalf("after window: %v", err)
	}
	// Stop: zp2e10=2 then p2=1.
	if _, err := pump.Stop(ctx, "2"); err != nil {
		t.Fatal(err)
	}
	w = f.Writes()
	if w[len(w)-2].Fields["zp2e10"] != "2" || w[len(w)-1].Fields["p2"] != "1" {
		t.Fatalf("stop writes %+v", w[len(w)-2:])
	}
}

func TestNotConfirmed(t *testing.T) {
	f := newFake(t)
	f.OnWrite = func(d *testutil.Device, fields map[string]string) {
		d.Fields["d13"] = "1" // firmware ignores the off command
	}
	c := newTestClient(t, f)
	_, err := c.Light(lightID).SetPower(ctxT(t), false, WithConfirmDefault(), WithConfirmPoll(10*time.Millisecond),
		WithConfirm(func(ds DeviceState) bool { v, _ := ds.Bool("d13"); return !v }, 50*time.Millisecond))
	var nc *NotConfirmedError
	if !errors.Is(err, ErrNotConfirmed) || !errors.As(err, &nc) || nc.Last == nil {
		t.Fatalf("got %v", err)
	}
}

func TestTypedViewWrongType(t *testing.T) {
	f := newFake(t)
	c := newTestClient(t, f)
	_, _, err := c.Pump(lightID).Status(ctxT(t))
	if !errors.Is(err, ErrUnsupportedDevice) {
		t.Fatalf("got %v", err)
	}
}

func TestResolveDevice(t *testing.T) {
	f := newFake(t)
	c := newTestClient(t, f, WithConfig(&Config{Devices: map[string]string{"lights": lightID}}))
	ctx := ctxT(t)
	cases := []struct {
		ref   string
		types []DeviceType
		want  string
		err   error
	}{
		{"lights", nil, lightID, nil},
		{pumpID, nil, pumpID, nil},
		{"backyard", nil, poolID, nil},
		{"", []DeviceType{TypeColorSync}, lightID, nil},
		{"", nil, "", ErrAmbiguousDevice},
		{"missing", nil, "", ErrDeviceNotFound},
	}
	for _, tc := range cases {
		d, err := c.ResolveDevice(ctx, tc.ref, tc.types...)
		if tc.err != nil {
			if !errors.Is(err, tc.err) {
				t.Errorf("%q: want %v got %v", tc.ref, tc.err, err)
			}
			continue
		}
		if err != nil || d.ID != tc.want {
			t.Errorf("%q: got %v %v", tc.ref, d, err)
		}
	}
}

func TestLogout(t *testing.T) {
	f := newFake(t)
	store := MemoryTokenStore()
	c := newTestClient(t, f, WithTokenStore(store))
	ctx := ctxT(t)
	_ = c.Login(ctx)
	if err := c.Logout(ctx, true); err != nil {
		t.Fatal(err)
	}
	if f.Count("signout") != 1 {
		t.Fatal("GlobalSignOut not called")
	}
	if _, err := store.Load(ctx, "test:"+testUser); err == nil {
		t.Fatal("session not deleted")
	}
}

func TestJSONOfState(t *testing.T) {
	f := newFake(t)
	c := newTestClient(t, f)
	st, err := c.State(ctxT(t), poolID)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(DecodePool(*st))
	if !strings.Contains(string(b), `"pumpRunning":true`) {
		t.Fatal(string(b))
	}
}
