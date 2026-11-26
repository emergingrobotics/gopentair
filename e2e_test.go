package gopentair

import (
	"context"
	"os"
	"testing"
	"time"
)

// End-to-end tests against the real Pentair cloud. They are skipped unless
// PENTAIR_E2E=1 and PENTAIR_USERNAME/PENTAIR_PASSWORD are set. Never run in CI.
//
// Write tests additionally need PENTAIR_E2E_WRITE=1 and
// PENTAIR_E2E_LIGHT=<Color Sync device ID>; they toggle the lights and
// restore the previous power state.

func e2eClient(t *testing.T) (Client, context.Context) {
	t.Helper()
	if os.Getenv("PENTAIR_E2E") != "1" {
		t.Skip("set PENTAIR_E2E=1 to run against the real cloud")
	}
	c, err := NewClient(WithEnvConfig(), WithDefaultTokenFile())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	if err := c.Login(ctx); err != nil {
		t.Fatal(err)
	}
	return c, ctx
}

func TestE2ERead(t *testing.T) {
	c, ctx := e2eClient(t)
	devs, err := c.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) == 0 {
		t.Skip("account has no devices")
	}
	for _, d := range devs {
		st, err := c.State(ctx, d.ID)
		if err != nil {
			t.Fatalf("%s: %v", d.Type, err)
		}
		t.Logf("%s (%s): online=%v fields=%d", d.Type, d.Type.Description(), st.Online, len(st.Fields))
	}
	if _, err := c.Profile(ctx); err != nil {
		t.Errorf("profile: %v", err)
	}
}

func TestE2ELightToggle(t *testing.T) {
	c, ctx := e2eClient(t)
	id := os.Getenv("PENTAIR_E2E_LIGHT")
	if os.Getenv("PENTAIR_E2E_WRITE") != "1" || id == "" {
		t.Skip("set PENTAIR_E2E_WRITE=1 and PENTAIR_E2E_LIGHT to run write tests")
	}
	l := c.Light(id)
	st, _, err := l.Status(ctx)
	if err != nil || st.On == nil {
		t.Fatalf("status: %v", err)
	}
	orig := *st.On
	if _, err := l.SetPower(ctx, !orig, WithConfirmDefault()); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if _, err := l.SetPower(ctx, orig, WithConfirmDefault()); err != nil {
		t.Fatalf("restore: %v", err)
	}
}
