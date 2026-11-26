// Package auth manages the Pentair Home credential chain:
//
//	username/password --SRP--> Cognito JWTs (Session)
//	IdToken --Cognito Identity--> temporary AWS credentials
//
// The JWTs and the AWS credentials expire independently; Manager refreshes
// each on its own clock, single-flighted behind one mutex.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/emergingrobotics/gopentair/internal/cognito"
	"github.com/emergingrobotics/gopentair/internal/srp"
)

// Sentinel errors.
var (
	// ErrReauthRequired means there is no usable session and no password
	// to log in again with.
	ErrReauthRequired = errors.New("re-authentication required")
	// ErrInvalidCredentials means Cognito rejected the username/password.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrNotFound is returned by a Store with no session for a key.
	ErrNotFound = errors.New("session not found")
)

// Error is an authentication failure.
type Error struct {
	Op      string // "srp", "refresh", "identity", "signout"
	Code    string // Cognito error type, e.g. NotAuthorizedException
	Message string
	Err     error
}

func (e *Error) Error() string {
	s := "auth " + e.Op
	if e.Code != "" {
		s += ": " + e.Code
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	if e.Err != nil && e.Code == "" {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// Is reports ErrInvalidCredentials for rejected logins.
func (e *Error) Is(target error) bool {
	return target == ErrInvalidCredentials && e.Op == "srp" &&
		(e.Code == "NotAuthorizedException" || e.Code == "UserNotFoundException")
}

// ChallengeError is returned when Cognito asks for a challenge this client
// does not handle (MFA, NEW_PASSWORD_REQUIRED, device auth, ...).
type ChallengeError struct {
	Challenge string
}

func (e *ChallengeError) Error() string {
	return "auth: unsupported Cognito challenge " + e.Challenge
}

func wrap(op string, err error) error {
	var ce *cognito.Error
	if errors.As(err, &ce) {
		return &Error{Op: op, Code: ce.Type, Message: ce.Message, Err: err}
	}
	return &Error{Op: op, Err: err}
}

// Session holds Cognito User Pool tokens.
type Session struct {
	IDToken      string    `json:"id_token"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IDExpiry     time.Time `json:"id_expiry"`
	AccessExpiry time.Time `json:"access_expiry"`
	Subject      string    `json:"sub,omitempty"`
}

// AWSCredentials are temporary credentials from the identity pool.
type AWSCredentials struct {
	AccessKeyID  string
	SecretKey    string
	SessionToken string
	Expiry       time.Time
	IdentityID   string
	forIDToken   string // hash of the IdToken used to mint these
}

// Store persists sessions between runs.
type Store interface {
	Load(ctx context.Context, key string) (*Session, error)
	Save(ctx context.Context, key string, s *Session) error
	Delete(ctx context.Context, key string) error
}

// Region identifies the Cognito pools to use.
type Region struct {
	Name           string
	AWSRegion      string
	UserPoolID     string
	ClientID       string
	IdentityPoolID string
}

// LoginsKey is the provider name used in Cognito Identity Logins maps.
func (r Region) LoginsKey() string {
	return "cognito-idp." + r.AWSRegion + ".amazonaws.com/" + r.UserPoolID
}

// Config configures a Manager.
type Config struct {
	Region   Region
	Username string
	Password string
	Cognito  *cognito.Client
	Store    Store // nil: memory only
	Now      func() time.Time
	Margin   time.Duration // refresh this long before expiry (default 5m)
	Logger   *slog.Logger
}

// Manager owns and refreshes the credential chain. Safe for concurrent use.
type Manager struct {
	cfg   Config
	mu    sync.Mutex
	sess  *Session
	creds *AWSCredentials
}

// NewManager returns a Manager.
func NewManager(cfg Config) *Manager {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Margin == 0 {
		cfg.Margin = 5 * time.Minute
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return &Manager{cfg: cfg}
}

// StoreKey is the key a session is stored under.
func (m *Manager) StoreKey() string {
	return m.cfg.Region.Name + ":" + strings.ToLower(m.cfg.Username)
}

// Login establishes a session: resume from the store if possible,
// otherwise SRP with the configured password. It also mints AWS
// credentials so configuration errors surface immediately.
func (m *Manager) Login(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureSessionLocked(ctx); err != nil {
		return err
	}
	return m.ensureCredsLocked(ctx)
}

// ForceLogin always performs a fresh SRP login, ignoring any stored session.
func (m *Manager) ForceLogin(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sess, m.creds = nil, nil
	if err := m.srpLoginLocked(ctx); err != nil {
		return err
	}
	return m.ensureCredsLocked(ctx)
}

// Session returns a fresh copy of the current session.
func (m *Manager) Session(ctx context.Context) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureSessionLocked(ctx); err != nil {
		return Session{}, err
	}
	return *m.sess, nil
}

// Credentials returns fresh AWS credentials and the session they belong to.
func (m *Manager) Credentials(ctx context.Context) (AWSCredentials, Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureSessionLocked(ctx); err != nil {
		return AWSCredentials{}, Session{}, err
	}
	if err := m.ensureCredsLocked(ctx); err != nil {
		return AWSCredentials{}, Session{}, err
	}
	return *m.creds, *m.sess, nil
}

// Invalidate forces the AWS credentials (and, if session is true, the
// JWTs) to be renewed on next use.
func (m *Manager) Invalidate(session bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.creds = nil
	if session && m.sess != nil {
		m.sess.IDExpiry = time.Time{}
		m.sess.AccessExpiry = time.Time{}
	}
}

// Logout forgets the session locally (memory and store). If everywhere is
// true it first calls Cognito GlobalSignOut, which also signs out every
// other client of the account, including the phone app.
func (m *Manager) Logout(ctx context.Context, everywhere bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var err error
	if everywhere {
		if e := m.ensureSessionLocked(ctx); e != nil {
			err = e
		} else if e := m.cfg.Cognito.GlobalSignOut(ctx, m.sess.AccessToken); e != nil {
			err = wrap("signout", e)
		}
	}
	m.sess, m.creds = nil, nil
	if m.cfg.Store != nil {
		if e := m.cfg.Store.Delete(ctx, m.StoreKey()); e != nil && !errors.Is(e, ErrNotFound) && err == nil {
			err = e
		}
	}
	return err
}

func (m *Manager) ensureSessionLocked(ctx context.Context) error {
	if m.sess == nil && m.cfg.Store != nil && m.cfg.Username != "" {
		s, err := m.cfg.Store.Load(ctx, m.StoreKey())
		switch {
		case err == nil && s != nil && s.RefreshToken != "":
			m.cfg.Logger.Debug("resumed session from token store")
			m.sess = s
		case err != nil && !errors.Is(err, ErrNotFound):
			m.cfg.Logger.Warn("token store load failed", "err", err)
		}
	}
	if m.sess == nil {
		return m.srpLoginLocked(ctx)
	}
	now := m.cfg.Now()
	if now.Add(m.cfg.Margin).Before(m.sess.IDExpiry) && now.Add(m.cfg.Margin).Before(m.sess.AccessExpiry) {
		return nil
	}
	err := m.refreshLocked(ctx)
	if err == nil {
		return nil
	}
	var ae *Error
	if errors.As(err, &ae) && ae.Code == "NotAuthorizedException" {
		m.cfg.Logger.Debug("refresh token rejected")
		m.sess = nil
		if m.cfg.Store != nil {
			_ = m.cfg.Store.Delete(ctx, m.StoreKey())
		}
		if m.cfg.Password != "" {
			return m.srpLoginLocked(ctx)
		}
		return fmt.Errorf("%w: refresh token expired or revoked", ErrReauthRequired)
	}
	return err
}

func (m *Manager) srpLoginLocked(ctx context.Context) error {
	if m.cfg.Username == "" || m.cfg.Password == "" {
		return ErrReauthRequired
	}
	r := m.cfg.Region
	cl, err := srp.NewClient(r.UserPoolID, m.cfg.Password)
	if err != nil {
		return wrap("srp", err)
	}
	m.cfg.Logger.Debug("starting SRP login")
	out, err := m.cfg.Cognito.InitiateAuth(ctx, r.ClientID, "USER_SRP_AUTH", map[string]string{
		"USERNAME": m.cfg.Username,
		"SRP_A":    cl.AHex(),
	})
	if err != nil {
		return wrap("srp", err)
	}
	if out.ChallengeName != "PASSWORD_VERIFIER" {
		if out.AuthenticationResult != nil {
			return m.acceptLocked(ctx, out.AuthenticationResult, "")
		}
		return &ChallengeError{Challenge: out.ChallengeName}
	}
	p := out.ChallengeParameters
	resp, err := cl.Response(srp.Challenge{
		UserIDForSRP: p["USER_ID_FOR_SRP"],
		Username:     p["USERNAME"],
		SaltHex:      p["SALT"],
		BHex:         p["SRP_B"],
		SecretBlock:  p["SECRET_BLOCK"],
	}, m.cfg.Now())
	if err != nil {
		return wrap("srp", err)
	}
	out, err = m.cfg.Cognito.RespondToAuthChallenge(ctx, r.ClientID, "PASSWORD_VERIFIER", out.Session, resp)
	if err != nil {
		return wrap("srp", err)
	}
	if out.AuthenticationResult == nil {
		return &ChallengeError{Challenge: out.ChallengeName}
	}
	return m.acceptLocked(ctx, out.AuthenticationResult, "")
}

func (m *Manager) refreshLocked(ctx context.Context) error {
	m.cfg.Logger.Debug("refreshing Cognito tokens")
	out, err := m.cfg.Cognito.InitiateAuth(ctx, m.cfg.Region.ClientID, "REFRESH_TOKEN_AUTH",
		map[string]string{"REFRESH_TOKEN": m.sess.RefreshToken})
	if err != nil {
		return wrap("refresh", err)
	}
	if out.AuthenticationResult == nil {
		return &ChallengeError{Challenge: out.ChallengeName}
	}
	return m.acceptLocked(ctx, out.AuthenticationResult, m.sess.RefreshToken)
}

func (m *Manager) acceptLocked(ctx context.Context, ar *cognito.AuthResult, prevRefresh string) error {
	now := m.cfg.Now()
	fallback := now.Add(time.Duration(ar.ExpiresIn) * time.Second)
	s := &Session{
		IDToken:      ar.IdToken,
		AccessToken:  ar.AccessToken,
		RefreshToken: ar.RefreshToken,
	}
	if s.RefreshToken == "" {
		s.RefreshToken = prevRefresh
	}
	c := parseClaims(ar.IdToken)
	s.IDExpiry = c.exp(fallback)
	s.Subject = c.Sub
	s.AccessExpiry = parseClaims(ar.AccessToken).exp(fallback)
	m.sess = s
	m.creds = nil
	if m.cfg.Store != nil && m.cfg.Username != "" {
		if err := m.cfg.Store.Save(ctx, m.StoreKey(), s); err != nil {
			m.cfg.Logger.Warn("token store save failed", "err", err)
		}
	}
	return nil
}

func (m *Manager) ensureCredsLocked(ctx context.Context) error {
	now := m.cfg.Now()
	h := tokenHash(m.sess.IDToken)
	if m.creds != nil && m.creds.forIDToken == h && now.Add(m.cfg.Margin).Before(m.creds.Expiry) {
		return nil
	}
	m.cfg.Logger.Debug("minting AWS credentials")
	logins := map[string]string{m.cfg.Region.LoginsKey(): m.sess.IDToken}
	id, err := m.cfg.Cognito.GetID(ctx, m.cfg.Region.IdentityPoolID, logins)
	if err != nil {
		return wrap("identity", err)
	}
	cr, err := m.cfg.Cognito.GetCredentialsForIdentity(ctx, id, logins)
	if err != nil {
		return wrap("identity", err)
	}
	if !cr.Expiration.After(now) {
		return &Error{Op: "identity", Message: "identity pool returned already-expired credentials"}
	}
	m.creds = &AWSCredentials{
		AccessKeyID:  cr.AccessKeyID,
		SecretKey:    cr.SecretKey,
		SessionToken: cr.SessionToken,
		Expiry:       cr.Expiration,
		IdentityID:   id,
		forIDToken:   h,
	}
	return nil
}

func tokenHash(t string) string {
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:8])
}

type claims struct {
	Exp float64 `json:"exp"`
	Sub string  `json:"sub"`
}

func (c claims) exp(fallback time.Time) time.Time {
	if c.Exp <= 0 {
		return fallback
	}
	return time.Unix(int64(c.Exp), 0)
}

// parseClaims decodes a JWT payload without verifying it.
func parseClaims(tok string) claims {
	var c claims
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return c
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return c
	}
	_ = json.Unmarshal(b, &c)
	return c
}

// MemoryStore is an in-process Store.
type MemoryStore struct {
	mu sync.Mutex
	m  map[string]Session
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore { return &MemoryStore{m: map[string]Session{}} }

// Load implements Store.
func (s *MemoryStore) Load(_ context.Context, key string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	if !ok {
		return nil, ErrNotFound
	}
	return &v, nil
}

// Save implements Store.
func (s *MemoryStore) Save(_ context.Context, key string, v *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = *v
	return nil
}

// Delete implements Store.
func (s *MemoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}
