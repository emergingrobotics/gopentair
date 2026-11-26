package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emergingrobotics/gopentair"
	"github.com/emergingrobotics/gopentair/internal/testutil"
)

const (
	user = "cli@example.com"
	pass = "pw-123456"
)

type harness struct {
	t         *testing.T
	f         *testutil.Fake
	tokenFile string
	password  string
}

func newHarness(t *testing.T) *harness {
	f := testutil.New(t)
	f.AddUser(user, pass)
	f.AddDevice(&testutil.Device{ID: "pump-1", Type: "IF31", Nickname: "Main Pump", Pname: "IntelliFlo 3", Online: true, Fields: testutil.PumpFields()})
	f.AddDevice(&testutil.Device{ID: "pool-1", Type: "PIF0", Nickname: "Backyard", Pname: "IntelliConnect", Online: true, Fields: testutil.PoolFields()})
	f.AddDevice(&testutil.Device{ID: "light-1", Type: "PLC1", Nickname: "Pool Lights", Pname: "Color Sync", Online: true, Fields: testutil.LightFields()})
	dir := t.TempDir()
	for _, k := range []string{"PENTAIR_REGION", "PENTAIR_DEFAULT_DEVICE", "PENTAIR_TOKEN_FILE", "PENTAIR_DEBUG", "PENTAIR_SIGN_PROFILE"} {
		t.Setenv(k, "")
	}
	t.Setenv("PENTAIR_CONFIG", filepath.Join(dir, "missing.yaml"))
	t.Setenv("PENTAIR_USERNAME", user)
	return &harness{t: t, f: f, tokenFile: filepath.Join(dir, "tokens.json"), password: pass}
}

// run executes the CLI and returns stdout, stderr and the exit code.
func (h *harness) run(args ...string) (string, string, int) {
	return h.runCtx(context.Background(), args...)
}

func (h *harness) runCtx(ctx context.Context, args ...string) (string, string, int) {
	h.t.Helper()
	h.t.Setenv("PENTAIR_PASSWORD", h.password)
	rest, ws, idp, ident := h.f.URLs()
	var out, errb bytes.Buffer
	a := newApp()
	a.stdout, a.stderr, a.stdin = &out, &errb, strings.NewReader("\n")
	a.isTTY = func() bool { return false }
	a.extraOpts = []gopentair.Option{
		gopentair.WithRegion(gopentair.Region{Name: "test", AWSRegion: h.f.Region, UserPoolID: h.f.PoolID, ClientID: h.f.ClientID, IdentityPoolID: h.f.IdentityPoolID}),
		gopentair.WithBaseURLs(gopentair.BaseURLs{REST: rest, WS: ws, IdP: idp, Identity: ident}),
		gopentair.WithClock(h.f.Clock.Now),
		gopentair.WithTimeout(5 * time.Second),
	}
	code := a.run(ctx, append([]string{"--token-file", h.tokenFile}, args...))
	return out.String(), errb.String(), code
}

func (h *harness) mustRun(args ...string) string {
	h.t.Helper()
	out, errs, code := h.run(args...)
	if code != 0 {
		h.t.Fatalf("pentair %v: exit %d\nstdout: %s\nstderr: %s", args, code, out, errs)
	}
	return out
}

func TestDevicesAndStatus(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("devices", "list")
	for _, want := range []string{"Main Pump", "IntelliFlo 3 pump", "Backyard", "Pool Lights", "yes"} {
		if !strings.Contains(out, want) {
			t.Errorf("devices list missing %q:\n%s", want, out)
		}
	}
	out = h.mustRun("status", "--device", "main pump")
	for _, want := range []string{"Active program", "2 (Cleaning)", "1392 W", "58.3 GPM", "28.5 psi", "Spa"} {
		if !strings.Contains(out, want) {
			t.Errorf("pump status missing %q:\n%s", want, out)
		}
	}
	out = h.mustRun("pool", "status")
	for _, want := range []string{"running 1392 W", "79.0°F (26.1°C)", "auto idle, setpoint 84.0°F", "salt 3200 ppm", "IC40"} {
		if !strings.Contains(out, want) {
			t.Errorf("pool status missing %q:\n%s", want, out)
		}
	}
	out = h.mustRun("light", "status", "--units", "f")
	if !strings.Contains(out, "caribbean") {
		t.Errorf("light status:\n%s", out)
	}
	// JSON status carries decoded and raw fields.
	out = h.mustRun("status", "-d", "light-1", "--json")
	var js struct {
		Decoded map[string]any            `json:"decoded"`
		Fields  map[string]map[string]any `json:"fields"`
	}
	if err := json.Unmarshal([]byte(out), &js); err != nil || js.Decoded["showName"] != "caribbean" || js.Fields["d1"]["value"] != "10" {
		t.Fatalf("json: %v %s", err, out)
	}
	// Session was cached with 0600.
	st, err := os.Stat(h.tokenFile)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v %v", err, st)
	}
}

func TestAmbiguousAndReauth(t *testing.T) {
	h := newHarness(t)
	if _, errs, code := h.run("status"); code != exitUsage || !strings.Contains(errs, "--device") {
		t.Fatalf("ambiguous: code %d %s", code, errs)
	}
	h.mustRun("logout")
	h.password = ""
	if _, errs, code := h.run("devices", "list"); code != exitAuth || !strings.Contains(errs, "pentair login") {
		t.Fatalf("reauth: code %d %s", code, errs)
	}
	if _, errs, code := h.run("login"); code != exitUsage || !strings.Contains(errs, "PENTAIR_PASSWORD") {
		t.Fatalf("login without tty: %d %s", code, errs)
	}
	h.password = "wrong"
	if _, _, code := h.run("login"); code != exitAuth {
		t.Fatalf("bad password code %d", code)
	}
	h.password = pass
	if out := h.mustRun("login"); !strings.Contains(out, "logged in as "+user) {
		t.Fatal(out)
	}
	// Session works without a password afterwards.
	h.password = ""
	h.mustRun("whoami")
}

func TestWrites(t *testing.T) {
	h := newHarness(t)
	// Dry run: plan printed, nothing sent.
	out := h.mustRun("pump", "start", "spa", "--dry-run")
	if !strings.Contains(out, "zp3e10=3") || len(h.f.Writes()) != 0 {
		t.Fatalf("dry run:\n%s", out)
	}
	h.mustRun("light", "show", "royal", "--no-confirm")
	h.mustRun("pool", "chlorine", "35%", "--no-confirm")
	h.mustRun("pool", "heater", "heat", "--setpoint", "86", "--no-confirm")
	h.mustRun("pool", "pump", "off", "--no-confirm")
	w := h.f.Writes()
	if len(w) != 4 ||
		w[0].Fields["d1"] != "13" ||
		w[1].Fields["icd1"] != "35" ||
		w[2].Fields["htd1"] != "1" || w[2].Fields["htd2"] != "860" ||
		w[3].Fields["ra0"] != "2" { // schedule bit preserved
		t.Fatalf("writes %+v", w)
	}
	// Confirmed write: the fake applies the field, so confirm succeeds.
	if out := h.mustRun("light", "off"); !strings.Contains(out, "(confirmed)") {
		t.Fatalf("confirm: %s", out)
	}
	// Raw write needs --yes when not interactive.
	if _, _, code := h.run("fields", "set", "-d", "light-1", "d13=1"); code != exitUsage {
		t.Fatalf("raw write without --yes: %d", code)
	}
	h.mustRun("fields", "set", "-d", "light-1", "d13=1", "--yes")
	// Validation errors exit 2.
	if _, _, code := h.run("pool", "chlorine", "150"); code != exitUsage {
		t.Fatalf("chlorine 150: %d", code)
	}
	if _, _, code := h.run("light", "show", "disco"); code != exitUsage {
		t.Fatalf("disco: %d", code)
	}
	// Wrong type for typed command.
	if _, errs, code := h.run("pump", "status", "-d", "light-1"); code == 0 || !strings.Contains(errs, "unsupported") {
		t.Fatalf("wrong type: %d %s", code, errs)
	}
	// Debounce: a second program start inside 30s exits 6.
	h.f.OnWrite = func(d *testutil.Device, fields map[string]string) {
		if fields["zp3e10"] == "3" {
			d.Fields["s14"] = "2"
		}
	}
	h.mustRun("pump", "start", "3", "--no-confirm")
	// Each CLI run is a new process with fresh debounce state, so the
	// window applies within one process; exercise the mapping instead.
	if got := exitCode(&gopentair.RateLimitError{Key: gopentair.DebounceProgramStart, Wait: time.Second}); got != exitRateLimited {
		t.Fatalf("rate limit exit %d", got)
	}
}

func TestFieldsAndDiff(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("fields", "-d", "pump-1", "--filter", "s1")
	if !strings.Contains(out, "Current power") || strings.Contains(out, "zp1e2") {
		t.Fatalf("fields:\n%s", out)
	}
	if i, j := strings.Index(out, "s13 "), strings.Index(out, "s17 "); i < 0 || j < 0 || i > j {
		t.Fatalf("natural sort:\n%s", out)
	}
	out = h.mustRun("fields", "-d", "light-1", "--csv")
	if !strings.HasPrefix(out, "Code,Name,Value,Min,Max\n") {
		t.Fatalf("csv:\n%s", out)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		h.f.SetFieldQuietly("light-1", "d1", "3")
	}()
	out = h.mustRun("diff", "-d", "light-1", "--wait", "1s")
	if !strings.Contains(out, "d1") || !strings.Contains(out, "10") || !strings.Contains(out, "3") {
		t.Fatalf("diff:\n%s", out)
	}
}

func TestWatch(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		for h.f.Subscribers() < 2 {
			time.Sleep(20 * time.Millisecond)
		}
		h.f.SetField("pump-1", "s18", "900")
	}()
	out, errs, code := h.runCtx(ctx, "watch", "-d", "pump-1", "--ndjson")
	if code != 0 {
		t.Fatalf("watch exit %d: %s", code, errs)
	}
	if !strings.Contains(out, `"source":"snapshot"`) || !strings.Contains(out, `"s18":"900"`) {
		t.Fatalf("watch output:\n%s", out)
	}
}

func TestExitCodes(t *testing.T) {
	cases := map[error]int{
		nil:                            exitOK,
		usageErr("x"):                  exitUsage,
		gopentair.ErrReauthRequired:    exitAuth,
		gopentair.ErrDeviceOffline:     exitOffline,
		&gopentair.NotConfirmedError{}: exitNotConfirmed,
		&gopentair.RateLimitError{}:    exitRateLimited,
		fmt.Errorf("w: %w", gopentair.ErrInvalidValue): exitUsage,
		errors.New("other"):                            exitError,
	}
	for err, want := range cases {
		if got := exitCode(err); got != want {
			t.Errorf("%v: got %d want %d", err, got, want)
		}
	}
}

func TestNaturalSort(t *testing.T) {
	if !naturalLess("s2", "s10") || naturalLess("zp10e1", "zp2e1") || !naturalLess("d1", "s1") {
		t.Fatal("natural ordering")
	}
}
