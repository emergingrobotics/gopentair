package gopentair

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestDebugLogRedaction runs a full session with debug logging and checks
// that no secret or identifier reaches the log.
func TestDebugLogRedaction(t *testing.T) {
	f := newFake(t)
	var buf bytes.Buffer
	c := newTestClient(t, f, WithLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	ctx := ctxT(t)
	if err := c.Login(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Devices(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.State(ctx, pumpID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetFields(ctx, lightID, map[string]string{"d13": "0"}); err != nil {
		t.Fatal(err)
	}
	sess, _ := c.auth.Session(ctx)
	creds, _, _ := c.auth.Credentials(ctx)
	log := buf.String()
	if !strings.Contains(log, "request") || !strings.Contains(log, "{id}") {
		t.Fatalf("expected debug output with redacted paths:\n%s", log)
	}
	for name, secret := range map[string]string{
		"pump id": pumpID, "light id": lightID, "id token": sess.IDToken[:20],
		"access token": sess.AccessToken[:20], "refresh token": sess.RefreshToken,
		"access key": creds.AccessKeyID, "session token": creds.SessionToken,
		"password": testPass, "wifi ssid": "HomeWifi", "mac": "aa:bb:cc:dd:ee:ff",
	} {
		if strings.Contains(log, secret) {
			t.Errorf("%s leaked into debug log", name)
		}
	}
}
