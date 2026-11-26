package redact

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	in := `wss://x/prod?token=eyJabc.def.ghi&x=1 Authorization: AWS4-HMAC-SHA256 Credential=ASIAABCDEFGHIJKLMNOP/2026 Signature=` + strings.Repeat("a", 64)
	out := String(in)
	for _, bad := range []string{"eyJabc", "ASIAABCDEFGHIJKLMNOP", strings.Repeat("a", 64)} {
		if strings.Contains(out, bad) {
			t.Errorf("leaked %q in %q", bad, out)
		}
	}
}

func TestJSON(t *testing.T) {
	in := []byte(`{"data":[{"deviceId":"DEV123","pname":"IntelliFlo","fields":{"s6":{"value":"aa:bb"},"s18":{"value":"1200"}}}]}`)
	out := JSON(in)
	if strings.Contains(out, "DEV123") || strings.Contains(out, "aa:bb") {
		t.Fatalf("leak: %s", out)
	}
	if !strings.Contains(out, "1200") || !strings.Contains(out, "IntelliFlo") {
		t.Fatalf("over-redacted: %s", out)
	}
	if got := JSON([]byte("<html>")); !strings.Contains(got, "non-JSON") {
		t.Fatal(got)
	}
}

func TestHandler(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(NewHandler(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	l.Debug("req", "url", "https://h/?token=secret", "password", "pw", "n", 3)
	s := buf.String()
	if strings.Contains(s, "secret") || strings.Contains(s, "pw ") || strings.Contains(s, "password=pw") {
		t.Fatalf("leak: %s", s)
	}
	if !strings.Contains(s, "n=3") {
		t.Fatalf("lost attr: %s", s)
	}
}
