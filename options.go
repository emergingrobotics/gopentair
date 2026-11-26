package gopentair

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/emergingrobotics/gopentair/internal/auth"
	"github.com/emergingrobotics/gopentair/internal/tokenstore"
)

// Option configures a Client. Options apply in order; later non-empty
// values win.
type Option func(*settings)

type settings struct {
	cfg         Config
	region      *Region
	store       auth.Store
	storeSet    bool
	defaultFile bool
	httpClient  *http.Client
	logger      *slog.Logger
	now         func() time.Time
	urls        BaseURLs
	debounce    map[DebounceKey]time.Duration
	errs        []error
}

// BaseURLs overrides service endpoints (tests, fake servers).
type BaseURLs struct {
	REST     string
	WS       string
	IdP      string
	Identity string
}

// WithUsername sets the account email.
func WithUsername(u string) Option { return func(s *settings) { s.cfg.Username = u } }

// WithPassword sets the account password.
func WithPassword(p string) Option { return func(s *settings) { s.cfg.Password = p } }

// WithRegion selects a region profile (RegionUS, RegionEU or custom).
func WithRegion(r Region) Option { return func(s *settings) { s.region = &r } }

// WithTokenStore sets where sessions are persisted. nil disables caching.
func WithTokenStore(st TokenStore) Option {
	return func(s *settings) { s.store = st; s.storeSet = true }
}

// WithDefaultTokenFile persists sessions to DefaultTokenFile() unless a
// token store or token_file is configured explicitly (CLI behaviour).
func WithDefaultTokenFile() Option { return func(s *settings) { s.defaultFile = true } }

// WithHTTPClient sets the HTTP client for all calls.
func WithHTTPClient(hc *http.Client) Option { return func(s *settings) { s.httpClient = hc } }

// WithTimeout sets the per-request timeout.
func WithTimeout(d time.Duration) Option { return func(s *settings) { s.cfg.Timeout = Duration(d) } }

// WithRetryCount sets retries for idempotent requests.
func WithRetryCount(n int) Option { return func(s *settings) { s.cfg.RetryCount = n } }

// WithSignProfile selects "app" (default) or "standard" SigV4 header sets.
func WithSignProfile(name string) Option { return func(s *settings) { s.cfg.SignProfile = name } }

// WithUserAgent overrides the User-Agent.
func WithUserAgent(ua string) Option { return func(s *settings) { s.cfg.UserAgent = ua } }

// WithLogger sets a logger. All output is passed through a redacting handler.
func WithLogger(l *slog.Logger) Option { return func(s *settings) { s.logger = l } }

// WithDefaultDevice sets the device used when none is given.
func WithDefaultDevice(id string) Option { return func(s *settings) { s.cfg.DefaultDevice = id } }

// WithClock overrides time.Now (tests).
func WithClock(now func() time.Time) Option { return func(s *settings) { s.now = now } }

// WithBaseURLs overrides endpoints (tests / fake servers). Empty fields
// keep their defaults.
func WithBaseURLs(u BaseURLs) Option { return func(s *settings) { s.urls = u } }

// WithDebounce overrides debounce windows (0 disables a key).
func WithDebounce(m map[DebounceKey]time.Duration) Option {
	return func(s *settings) {
		for k, v := range m {
			s.debounce[k] = v
		}
	}
}

// WithConfig merges a Config (non-empty fields win).
func WithConfig(c *Config) Option { return func(s *settings) { s.cfg.Merge(c) } }

// WithEnvConfig merges PENTAIR_* environment variables.
func WithEnvConfig() Option { return func(s *settings) { s.cfg.Merge(LoadConfigFromEnv()) } }

// WithConfigFile merges a YAML config file ("" for the default path). Load
// errors are returned by NewClient.
func WithConfigFile(path string) Option {
	return func(s *settings) {
		c, err := LoadConfigFile(path)
		if err != nil {
			s.errs = append(s.errs, err)
			return
		}
		s.cfg.Merge(c)
	}
}

// TokenStore persists sessions between runs.
type TokenStore = auth.Store

// Session is a stored Cognito session.
type Session = auth.Session

// FileTokenStore stores sessions in a 0600 JSON file ("" for
// ~/.config/pentair/tokens.json).
func FileTokenStore(path string) TokenStore { return tokenstore.NewFile(path) }

// MemoryTokenStore keeps sessions in memory only.
func MemoryTokenStore() TokenStore { return auth.NewMemoryStore() }

// DefaultTokenFile is the CLI's default token cache path.
func DefaultTokenFile() string { return tokenstore.DefaultPath() }
