// Package redact scrubs secrets and identifiers from log output.
package redact

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
)

// Mask replaces redacted values.
const Mask = "**REDACTED**"

var (
	jwtRE   = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
	tokenRE = regexp.MustCompile(`(?i)(token=)[^&\s"']+`)
	akidRE  = regexp.MustCompile(`\b(ASIA|AKIA)[A-Z0-9]{12,}\b`)
	sigRE   = regexp.MustCompile(`(Signature=)[0-9a-f]{64}`)
	credRE  = regexp.MustCompile(`(Credential=)[A-Z0-9]+/`)
)

// sensitiveKeys are JSON keys / header names / attr keys whose values are
// always masked (compared case-insensitively).
var sensitiveKeys = map[string]bool{}

func init() {
	for _, k := range []string{
		// auth material
		"authorization", "x-amz-id-token", "x-amz-security-token", "token",
		"accesstoken", "idtoken", "refreshtoken", "access_token", "id_token", "refresh_token",
		"sessiontoken", "secretkey", "accesskeyid", "password", "srp_a", "srp_b",
		"password_claim_signature", "password_claim_secret_block", "secret_block", "salt",
		"x-api-key", "refresh_token_auth",
		// identifiers
		"deviceid", "device_ids", "deviceids", "arn", "email", "userid", "identityid",
		"user_id_for_srp", "username", "sub", "addressid",
		// IntelliFlo sensitive fields
		"s6", "s11", "s23", "d15",
	} {
		sensitiveKeys[k] = true
	}
}

// IsSensitive reports whether a key's value should be masked.
func IsSensitive(key string) bool { return sensitiveKeys[strings.ToLower(key)] }

// String scrubs JWTs, token query params, AWS key IDs and signatures.
func String(s string) string {
	s = jwtRE.ReplaceAllString(s, Mask)
	s = tokenRE.ReplaceAllString(s, "${1}"+Mask)
	s = akidRE.ReplaceAllString(s, Mask)
	s = sigRE.ReplaceAllString(s, "${1}"+Mask)
	s = credRE.ReplaceAllString(s, "${1}"+Mask+"/")
	return s
}

// JSON returns a scrubbed copy of a JSON document. Non-JSON input is
// returned as a size-only placeholder, because free text can't be
// redacted reliably.
func JSON(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return "<non-JSON body, " + itoa(len(b)) + " bytes>"
	}
	out, _ := json.Marshal(Value(v))
	return String(string(out))
}

// Value scrubs a decoded JSON value in place and returns it.
func Value(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if IsSensitive(k) {
				if val != nil && val != "" {
					t[k] = Mask
				}
				continue
			}
			t[k] = Value(val)
		}
		return t
	case []any:
		for i := range t {
			t[i] = Value(t[i])
		}
		return t
	case string:
		return String(t)
	}
	return v
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// Handler wraps an slog.Handler and scrubs every record.
type Handler struct{ inner slog.Handler }

// NewHandler returns a redacting handler.
func NewHandler(h slog.Handler) *Handler { return &Handler{inner: h} }

// Enabled implements slog.Handler.
func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool { return h.inner.Enabled(ctx, l) }

// Handle implements slog.Handler.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	nr := slog.NewRecord(r.Time, r.Level, String(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(scrubAttr(a))
		return true
	})
	return h.inner.Handle(ctx, nr)
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(as []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(as))
	for i, a := range as {
		out[i] = scrubAttr(a)
	}
	return &Handler{inner: h.inner.WithAttrs(out)}
}

// WithGroup implements slog.Handler.
func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{inner: h.inner.WithGroup(name)}
}

func scrubAttr(a slog.Attr) slog.Attr {
	if IsSensitive(a.Key) {
		return slog.String(a.Key, Mask)
	}
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, String(v.String()))
	case slog.KindGroup:
		gs := v.Group()
		out := make([]any, len(gs))
		for i, g := range gs {
			out[i] = scrubAttr(g)
		}
		return slog.Group(a.Key, out...)
	case slog.KindAny:
		return slog.String(a.Key, String(v.String()))
	}
	return a
}
