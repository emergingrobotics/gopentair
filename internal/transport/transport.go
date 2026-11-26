// Package transport sends SigV4-signed requests to the Pentair REST API,
// handling credential refresh and a conservative retry policy.
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/emergingrobotics/gopentair/internal/auth"
	"github.com/emergingrobotics/gopentair/internal/redact"
	"github.com/emergingrobotics/gopentair/internal/sign"
)

// Kind classifies a request for retry and header purposes.
type Kind int

const (
	// Read requests are safe to retry.
	Read Kind = iota
	// Write requests change device state and are never retried when the
	// outcome is ambiguous.
	Write
)

// Request describes one API call.
type Request struct {
	Method  string
	Path    string // e.g. /device2/device2-service/user/device
	LogPath string // Path with identifiers replaced, for logs/errors
	Body    any    // JSON-encoded if non-nil
	Kind    Kind
}

// StatusError is a non-2xx response.
type StatusError struct {
	Status    int
	Method    string
	Path      string // redacted
	Message   string
	Ambiguous bool // write may or may not have been applied
}

func (e *StatusError) Error() string {
	s := fmt.Sprintf("%s %s: HTTP %d", e.Method, e.Path, e.Status)
	if e.Message != "" {
		s += ": " + e.Message
	}
	if e.Ambiguous {
		s += " (write outcome unknown)"
	}
	return s
}

// NetError wraps a network failure.
type NetError struct {
	Method, Path string
	Ambiguous    bool
	Err          error
}

func (e *NetError) Error() string {
	s := fmt.Sprintf("%s %s: %v", e.Method, e.Path, e.Err)
	if e.Ambiguous {
		s += " (write outcome unknown)"
	}
	return s
}

func (e *NetError) Unwrap() error { return e.Err }

// Credentialer provides signing material.
type Credentialer interface {
	Credentials(ctx context.Context) (auth.AWSCredentials, auth.Session, error)
	Invalidate(session bool)
}

// Config configures a Transport.
type Config struct {
	BaseURL     string
	HTTP        *http.Client
	Auth        Credentialer
	AWSRegion   string
	Profile     sign.Profile
	UserAgent   string
	RetryCount  int
	BackoffBase time.Duration
	Now         func() time.Time
	Logger      *slog.Logger
}

// Transport is safe for concurrent use.
type Transport struct{ cfg Config }

// New returns a Transport.
func New(cfg Config) *Transport {
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.BackoffBase == 0 {
		cfg.BackoffBase = 500 * time.Millisecond
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Transport{cfg: cfg}
}

// Do performs the request and returns the raw response body.
func (t *Transport) Do(ctx context.Context, req Request) ([]byte, error) {
	var body []byte
	if req.Body != nil {
		var err error
		if body, err = json.Marshal(req.Body); err != nil {
			return nil, err
		}
	}
	logPath := req.LogPath
	if logPath == "" {
		logPath = req.Path
	}
	expiredRetried := false
	for attempt := 0; ; attempt++ {
		out, retry, err := t.once(ctx, req, body, logPath)
		if err == nil {
			return out, nil
		}
		var se *StatusError
		if errors.As(err, &se) && isExpired(se) && !expiredRetried {
			expiredRetried = true
			t.cfg.Auth.Invalidate(false)
			t.cfg.Logger.Debug("credentials rejected as expired; retrying once", "path", logPath)
			attempt--
			continue
		}
		if !retry || attempt >= t.cfg.RetryCount {
			return nil, err
		}
		d := t.cfg.BackoffBase << attempt
		d += time.Duration(rand.Int64N(int64(d/2) + 1))
		t.cfg.Logger.Debug("retrying", "path", logPath, "attempt", attempt+1, "delay", d, "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(d):
		}
	}
}

func isExpired(se *StatusError) bool {
	if se.Status != http.StatusUnauthorized && se.Status != http.StatusForbidden {
		return false
	}
	return strings.Contains(strings.ToLower(se.Message), "expired")
}

// once returns (body, retryable, error).
func (t *Transport) once(ctx context.Context, req Request, body []byte, logPath string) ([]byte, bool, error) {
	creds, sess, err := t.cfg.Auth.Credentials(ctx)
	if err != nil {
		return nil, false, err
	}
	hr, err := http.NewRequestWithContext(ctx, req.Method, t.cfg.BaseURL+req.Path, bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	if body == nil {
		hr.Body = http.NoBody
		hr.ContentLength = 0
	}
	hr.Header.Set("x-amz-id-token", sess.IDToken)
	hr.Header.Set("iseuropeanuser", "false")
	if req.Kind == Write {
		hr.Header["X-Pha-Apptype"] = []string{""}
	} else {
		hr.Header.Set("x-pha-apptype", "pool")
	}
	if body != nil {
		hr.Header.Set("Content-Type", "application/json; charset=UTF-8")
	}
	if t.cfg.UserAgent != "" {
		hr.Header.Set("User-Agent", t.cfg.UserAgent)
	}
	if err := sign.Sign(hr, body, sign.Credentials{
		AccessKeyID: creds.AccessKeyID, SecretKey: creds.SecretKey, SessionToken: creds.SessionToken,
	}, t.cfg.AWSRegion, "execute-api", t.cfg.Profile, t.cfg.Now()); err != nil {
		return nil, false, err
	}
	t.cfg.Logger.Debug("request", "method", req.Method, "path", logPath, "body", redact.JSON(body))
	start := time.Now()
	resp, err := t.cfg.HTTP.Do(hr)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		pre := isPreSend(err)
		ambiguous := req.Kind == Write && !pre
		return nil, req.Kind == Read || pre, &NetError{Method: req.Method, Path: logPath, Ambiguous: ambiguous, Err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, req.Kind == Read, &NetError{Method: req.Method, Path: logPath, Ambiguous: req.Kind == Write, Err: err}
	}
	t.cfg.Logger.Debug("response", "method", req.Method, "path", logPath, "status", resp.StatusCode,
		"elapsed", time.Since(start).Round(time.Millisecond), "body", redact.JSON(raw))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, false, nil
	}
	se := &StatusError{Status: resp.StatusCode, Method: req.Method, Path: logPath, Message: errorMessage(raw)}
	transient := resp.StatusCode == 429 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504
	if transient && req.Kind == Write {
		se.Ambiguous = resp.StatusCode != 429
		return nil, false, se
	}
	return nil, transient, se
}

func errorMessage(raw []byte) string {
	var m struct {
		Message  string `json:"message"`
		Message2 string `json:"Message"`
		Error    string `json:"error"`
	}
	if json.Unmarshal(raw, &m) == nil {
		for _, s := range []string{m.Message, m.Message2, m.Error} {
			if s != "" {
				return redact.String(s)
			}
		}
	}
	if len(raw) > 0 && len(raw) < 200 && !bytes.ContainsAny(raw, "<{") {
		return redact.String(strings.TrimSpace(string(raw)))
	}
	return ""
}

// isPreSend reports whether err certainly happened before the request was
// written (dial / DNS failures).
func isPreSend(err error) bool {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	return false
}
