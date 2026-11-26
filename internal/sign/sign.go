// Package sign implements AWS Signature Version 4 for API Gateway
// ("execute-api") requests, with explicit control over which headers are
// signed.
package sign

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	algorithm = "AWS4-HMAC-SHA256"
	// AmzDateLayout is the X-Amz-Date format.
	AmzDateLayout = "20060102T150405Z"
	dateLayout    = "20060102"
)

// Credentials are temporary AWS credentials.
type Credentials struct {
	AccessKeyID  string
	SecretKey    string
	SessionToken string
}

// Profile controls which headers are included in the signature.
type Profile struct {
	Name string
	// Headers, if non-empty, is the exact list of headers to sign. Headers
	// in the list that are absent from the request are skipped.
	Headers []string
	// When Headers is empty, all request headers are signed except these
	// (botocore behaviour).
	Exclude []string
}

// Standard signs every header except a few transport-level ones,
// matching botocore's SigV4Auth.
var Standard = Profile{
	Name:    "standard",
	Exclude: []string{"user-agent", "content-length", "expect", "x-amzn-trace-id", "authorization", "connection", "accept-encoding"},
}

// App signs exactly the header set captured from the Pentair Home app.
var App = Profile{
	Name:    "app",
	Headers: []string{"host", "iseuropeanuser", "x-amz-date", "x-amz-id-token", "x-amz-security-token", "x-pha-apptype"},
}

// ProfileByName returns a profile by name ("app" or "standard").
func ProfileByName(name string) (Profile, error) {
	switch strings.ToLower(name) {
	case "", "app":
		return App, nil
	case "standard":
		return Standard, nil
	}
	return Profile{}, fmt.Errorf("unknown sign profile %q", name)
}

// PayloadHash returns the hex SHA-256 of body.
func PayloadHash(body []byte) string {
	s := sha256.Sum256(body)
	return hex.EncodeToString(s[:])
}

// Sign adds X-Amz-Date, X-Amz-Security-Token (if any) and Authorization
// to r. body must be the exact bytes that will be sent.
func Sign(r *http.Request, body []byte, creds Credentials, region, service string, p Profile, now time.Time) error {
	if creds.AccessKeyID == "" || creds.SecretKey == "" {
		return errors.New("sign: missing credentials")
	}
	now = now.UTC()
	amzDate := now.Format(AmzDateLayout)
	date := now.Format(dateLayout)
	r.Header.Set("X-Amz-Date", amzDate)
	if creds.SessionToken != "" {
		r.Header.Set("X-Amz-Security-Token", creds.SessionToken)
	}
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}

	names, values := headersToSign(r, host, p)
	var ch strings.Builder
	for _, n := range names {
		ch.WriteString(n)
		ch.WriteByte(':')
		ch.WriteString(values[n])
		ch.WriteByte('\n')
	}
	signed := strings.Join(names, ";")

	canonical := strings.Join([]string{
		r.Method,
		canonicalURI(r.URL),
		canonicalQuery(r.URL),
		ch.String(),
		signed,
		PayloadHash(body),
	}, "\n")

	scope := date + "/" + region + "/" + service + "/aws4_request"
	sts := strings.Join([]string{algorithm, amzDate, scope, PayloadHash([]byte(canonical))}, "\n")

	k := hmacSHA256([]byte("AWS4"+creds.SecretKey), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, sts))

	r.Header.Set("Authorization", fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, creds.AccessKeyID, scope, signed, sig))
	return nil
}

// Verify recomputes the signature of a received request with the given
// secret and reports whether the Authorization header matches. Used by the
// fake server in tests.
func Verify(r *http.Request, body []byte, secret, region, service string) error {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, algorithm+" ") {
		return errors.New("missing or malformed Authorization")
	}
	parts := map[string]string{}
	for _, kv := range strings.Split(strings.TrimPrefix(auth, algorithm+" "), ", ") {
		if i := strings.IndexByte(kv, '='); i > 0 {
			parts[kv[:i]] = kv[i+1:]
		}
	}
	cred := strings.SplitN(parts["Credential"], "/", 2)
	if len(cred) != 2 {
		return errors.New("bad Credential")
	}
	amz := r.Header.Get("X-Amz-Date")
	t, err := time.Parse(AmzDateLayout, amz)
	if err != nil {
		return fmt.Errorf("bad X-Amz-Date: %w", err)
	}
	clone := r.Clone(r.Context())
	clone.Header = r.Header.Clone()
	if clone.URL.Host == "" {
		clone.URL.Host = r.Host
	}
	p := Profile{Headers: strings.Split(parts["SignedHeaders"], ";")}
	if err := Sign(clone, body, Credentials{AccessKeyID: cred[0], SecretKey: secret, SessionToken: r.Header.Get("X-Amz-Security-Token")}, region, service, p, t); err != nil {
		return err
	}
	if !hmac.Equal([]byte(clone.Header.Get("Authorization")), []byte(auth)) {
		return errors.New("signature mismatch")
	}
	return nil
}

func headersToSign(r *http.Request, host string, p Profile) ([]string, map[string]string) {
	values := map[string]string{}
	all := map[string][]string{"host": {host}}
	for k, v := range r.Header {
		all[strings.ToLower(k)] = v
	}
	var names []string
	if len(p.Headers) > 0 {
		for _, h := range p.Headers {
			h = strings.ToLower(h)
			if _, ok := all[h]; ok {
				names = append(names, h)
			}
		}
	} else {
		ex := map[string]bool{}
		for _, e := range p.Exclude {
			ex[strings.ToLower(e)] = true
		}
		for k := range all {
			if !ex[k] {
				names = append(names, k)
			}
		}
	}
	sort.Strings(names)
	for _, n := range names {
		vs := make([]string, len(all[n]))
		for i, v := range all[n] {
			vs[i] = strings.Join(strings.Fields(v), " ")
		}
		values[n] = strings.Join(vs, ",")
	}
	return names, values
}

func canonicalURI(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		return "/"
	}
	// Non-S3 services: URI-encode each segment of the (already escaped) path.
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = uriEncode(s)
	}
	return strings.Join(segs, "/")
}

func canonicalQuery(u *url.URL) string {
	q := u.Query()
	if len(q) == 0 {
		return ""
	}
	var pairs []string
	for k, vs := range q {
		for _, v := range vs {
			pairs = append(pairs, uriEncode(k)+"="+uriEncode(v))
		}
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "&")
}

// uriEncode percent-encodes everything except RFC 3986 unreserved chars.
func uriEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}
