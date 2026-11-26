package gopentair

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/emergingrobotics/gopentair/internal/api"
	"github.com/emergingrobotics/gopentair/internal/auth"
	"github.com/emergingrobotics/gopentair/internal/cognito"
	"github.com/emergingrobotics/gopentair/internal/redact"
	"github.com/emergingrobotics/gopentair/internal/sign"
	"github.com/emergingrobotics/gopentair/internal/tokenstore"
	"github.com/emergingrobotics/gopentair/internal/transport"
)

// Version is the library version, used in the default User-Agent.
const Version = "0.1.0"

// Client is the Pentair Home cloud client. Implementations are safe for
// concurrent use.
type Client interface {
	// Login resumes a stored session or performs an SRP login.
	Login(ctx context.Context) error
	// ForceLogin always performs a fresh SRP login.
	ForceLogin(ctx context.Context) error
	// Logout forgets the session locally. With everywhere=true it also
	// calls Cognito GlobalSignOut, which signs out the phone app too.
	Logout(ctx context.Context, everywhere bool) error

	Profile(ctx context.Context) (*Profile, error)
	Devices(ctx context.Context) ([]Device, error)
	Device(ctx context.Context, id string) (*Device, error)
	// ResolveDevice finds a device by ID, configured alias or nickname. An
	// empty ref uses the default device, or the only device of the given
	// types.
	ResolveDevice(ctx context.Context, ref string, types ...DeviceType) (*Device, error)
	State(ctx context.Context, id string) (*DeviceState, error)
	States(ctx context.Context, ids ...string) ([]DeviceState, error)

	// SetFields writes raw fields (a one-step plan).
	SetFields(ctx context.Context, id string, fields map[string]string, opts ...ApplyOption) (*ApplyResult, error)
	// Apply executes a WritePlan.
	Apply(ctx context.Context, p WritePlan, opts ...ApplyOption) (*ApplyResult, error)

	// Watch streams merged device state from the WebSocket (with REST
	// polling as a fallback) until ctx is cancelled.
	Watch(ctx context.Context, ids []string, opts ...WatchOption) (<-chan Event, error)

	Pump(id string) *Pump
	IntelliConnect(id string) *IntelliConnect
	Light(id string) *Light
}

type client struct {
	s       settings
	region  Region
	auth    *auth.Manager
	tr      *transport.Transport
	log     *slog.Logger
	now     func() time.Time
	hc      *http.Client
	wsURL   string
	poll    time.Duration
	aliases map[string]string

	mu    sync.Mutex
	types map[string]DeviceType

	lockMu sync.Mutex
	locks  map[string]chan struct{}

	debMu     sync.Mutex
	lastWrite map[string]time.Time
}

// NewClient builds a client. It does not contact the network.
func NewClient(opts ...Option) (Client, error) {
	s := settings{debounce: map[DebounceKey]time.Duration{}}
	for k, v := range DefaultDebounce {
		s.debounce[k] = v
	}
	for _, o := range opts {
		o(&s)
	}
	if len(s.errs) > 0 {
		return nil, errors.Join(s.errs...)
	}
	region := RegionUS
	if s.region != nil {
		region = *s.region
	} else {
		r, err := RegionByName(s.cfg.Region)
		if err != nil {
			return nil, err
		}
		region = r
	}
	if region.ClientID == "" || region.IdentityPoolID == "" {
		return nil, fmt.Errorf("%w: region %q has no client/identity pool IDs; pass a complete Region with WithRegion", ErrInvalidValue, region.Name)
	}
	timeout := time.Duration(s.cfg.Timeout)
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	retries := s.cfg.RetryCount
	if retries <= 0 {
		retries = 3
	}
	poll := time.Duration(s.cfg.PollInterval)
	if poll <= 0 {
		poll = 60 * time.Second
	}
	if poll < 30*time.Second {
		poll = 30 * time.Second
	}
	profile, err := sign.ProfileByName(s.cfg.SignProfile)
	if err != nil {
		return nil, err
	}
	hc := s.httpClient
	if hc == nil {
		hc = &http.Client{Timeout: timeout}
	}
	base := s.logger
	if base == nil {
		if s.cfg.Debug {
			base = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
		} else {
			base = slog.New(slog.DiscardHandler)
		}
	}
	logger := slog.New(redact.NewHandler(base.Handler()))
	now := s.now
	if now == nil {
		now = time.Now
	}
	var store auth.Store
	switch {
	case s.storeSet:
		store = s.store
	case strings.EqualFold(s.cfg.TokenFile, "none"):
	case s.cfg.TokenFile != "":
		store = tokenstore.NewFile(s.cfg.TokenFile)
	case s.defaultFile:
		store = tokenstore.NewFile("")
	default:
		store = auth.NewMemoryStore()
	}
	ua := s.cfg.UserAgent
	if ua == "" {
		ua = "gopentair/" + Version
	}
	cog := cognito.New(region.AWSRegion, hc)
	cog.UserAgent = ua
	if s.urls.IdP != "" {
		cog.IdPURL = s.urls.IdP
	}
	if s.urls.Identity != "" {
		cog.IdentityURL = s.urls.Identity
	}
	am := auth.NewManager(auth.Config{
		Region:   region,
		Username: s.cfg.Username,
		Password: s.cfg.Password,
		Cognito:  cog,
		Store:    store,
		Now:      now,
		Logger:   logger,
	})
	rest := api.RESTBase
	if s.urls.REST != "" {
		rest = s.urls.REST
	}
	wsURL := api.WSURL
	if s.urls.WS != "" {
		wsURL = s.urls.WS
	}
	tr := transport.New(transport.Config{
		BaseURL:    rest,
		HTTP:       hc,
		Auth:       am,
		AWSRegion:  region.AWSRegion,
		Profile:    profile,
		UserAgent:  ua,
		RetryCount: retries,
		Now:        now,
		Logger:     logger,
	})
	return &client{
		s: s, region: region, auth: am, tr: tr, log: logger, now: now, hc: hc,
		wsURL: wsURL, poll: poll, aliases: s.cfg.Devices,
		types: map[string]DeviceType{}, locks: map[string]chan struct{}{}, lastWrite: map[string]time.Time{},
	}, nil
}

func (c *client) Login(ctx context.Context) error      { return c.auth.Login(ctx) }
func (c *client) ForceLogin(ctx context.Context) error { return c.auth.ForceLogin(ctx) }
func (c *client) Logout(ctx context.Context, everywhere bool) error {
	return c.auth.Logout(ctx, everywhere)
}

func (c *client) get(ctx context.Context, path string) ([]byte, error) {
	b, err := c.tr.Do(ctx, transport.Request{Method: http.MethodGet, Path: path, Kind: transport.Read})
	return b, convertErr(err)
}

func (c *client) Profile(ctx context.Context) (*Profile, error) {
	b, err := c.get(ctx, api.PathProfileV2)
	var ae *APIError
	if errors.As(err, &ae) && (ae.Status == 404 || ae.Status == 400) {
		b, err = c.get(ctx, api.PathProfile)
	}
	if err != nil {
		return nil, err
	}
	m, err := api.DecodeProfile(b)
	if err != nil {
		return nil, err
	}
	p := &Profile{Raw: m}
	if v, ok := m["email"].(string); ok {
		p.Email = v
	}
	if v, ok := m["firstName"].(string); ok {
		p.FirstName = v
	}
	if v, ok := m["lastName"].(string); ok {
		p.LastName = v
	}
	return p, nil
}

func (c *client) Devices(ctx context.Context) ([]Device, error) {
	b, err := c.get(ctx, api.PathListDevicesV2)
	var wds []api.WireDevice
	var raws []json.RawMessage
	if err == nil {
		wds, raws, err = api.DecodeDevices(b)
	}
	if err != nil && shouldFallback(err) {
		c.log.Debug("listdevices v2 failed; falling back to v1", "err", err)
		b, err2 := c.get(ctx, api.PathDevicesV1)
		if err2 != nil {
			return nil, err2
		}
		wds, raws, err = api.DecodeDevices(b)
	}
	if err != nil {
		return nil, err
	}
	out := make([]Device, 0, len(wds))
	c.mu.Lock()
	for i, w := range wds {
		d := toDevice(w, raws[i])
		if d.ID == "" {
			continue
		}
		c.types[d.ID] = d.Type
		out = append(out, d)
	}
	c.mu.Unlock()
	return out, nil
}

// shouldFallback reports whether a v2 listing failure warrants trying v1:
// the endpoint is missing/rejected, or its body has an unexpected shape.
// Auth and network failures are returned as-is.
func shouldFallback(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status == 400 || ae.Status == 404 || ae.Status == 405
	}
	var aue *AuthError
	var ce *ChallengeError
	if errors.As(err, &aue) || errors.As(err, &ce) || errors.Is(err, ErrReauthRequired) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true // decode error
}

func (c *client) Device(ctx context.Context, id string) (*Device, error) {
	ds, err := c.Devices(ctx)
	if err != nil {
		return nil, err
	}
	for i := range ds {
		if ds[i].ID == id {
			return &ds[i], nil
		}
	}
	return nil, ErrDeviceNotFound
}

func (c *client) ResolveDevice(ctx context.Context, ref string, types ...DeviceType) (*Device, error) {
	if ref == "" {
		ref = c.s.cfg.DefaultDevice
	}
	if id, ok := c.aliases[ref]; ok {
		ref = id
	}
	ds, err := c.Devices(ctx)
	if err != nil {
		return nil, err
	}
	match := func(d Device) bool { return len(types) == 0 || slices.Contains(types, d.Type) }
	if ref != "" {
		for i := range ds {
			if ds[i].ID == ref {
				return &ds[i], nil
			}
		}
		var hits []Device
		for _, d := range ds {
			if strings.EqualFold(d.Nickname, ref) && match(d) {
				hits = append(hits, d)
			}
		}
		if len(hits) == 1 {
			return &hits[0], nil
		}
		if len(hits) > 1 {
			return nil, ErrAmbiguousDevice
		}
		return nil, ErrDeviceNotFound
	}
	var hits []Device
	for _, d := range ds {
		if match(d) {
			hits = append(hits, d)
		}
	}
	switch len(hits) {
	case 0:
		return nil, ErrDeviceNotFound
	case 1:
		return &hits[0], nil
	}
	return nil, ErrAmbiguousDevice
}

func (c *client) States(ctx context.Context, ids ...string) ([]DeviceState, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	b, err := c.tr.Do(ctx, transport.Request{
		Method: http.MethodPost, Path: api.PathDeviceStates,
		Body: api.StatesRequest{DeviceIDs: ids}, Kind: transport.Read,
	})
	if err != nil {
		return nil, convertErr(err)
	}
	wds, raws, err := api.DecodeDevices(b)
	if err != nil {
		return nil, err
	}
	now := c.now()
	out := make([]DeviceState, 0, len(wds))
	for i, w := range wds {
		out = append(out, toState(w, raws[i], c.typeOf(ctx, w.DeviceID), now))
	}
	return out, nil
}

// typeOf returns the cached listing type, loading the listing once if needed.
func (c *client) typeOf(ctx context.Context, id string) DeviceType {
	c.mu.Lock()
	t, ok := c.types[id]
	empty := len(c.types) == 0
	c.mu.Unlock()
	if ok || !empty {
		return t
	}
	if _, err := c.Devices(ctx); err != nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.types[id]
}

func (c *client) State(ctx context.Context, id string) (*DeviceState, error) {
	ss, err := c.States(ctx, id)
	if err != nil {
		return nil, err
	}
	for i := range ss {
		if ss[i].DeviceID == id {
			return &ss[i], nil
		}
	}
	return nil, ErrDeviceNotFound
}

func (c *client) SetFields(ctx context.Context, id string, fields map[string]string, opts ...ApplyOption) (*ApplyResult, error) {
	if len(fields) == 0 {
		return nil, fmt.Errorf("%w: no fields", ErrInvalidValue)
	}
	return c.Apply(ctx, FieldPlan(id, fields), opts...)
}

// putFields sends one field write and checks the ack.
func (c *client) putFields(ctx context.Context, id string, fields map[string]string) error {
	b, err := c.tr.Do(ctx, transport.Request{
		Method: http.MethodPut, Path: api.PathDeviceV1 + id, LogPath: api.PathDeviceV1 + "{id}",
		Body: api.SetFieldsRequest{Payload: fields}, Kind: transport.Write,
	})
	if err != nil {
		return convertErr(err)
	}
	code, err := api.DecodeAck(b)
	if err != nil {
		return err
	}
	if code != api.SetDeviceSuccess {
		return fmt.Errorf("%w (code %q)", ErrNotAcknowledged, code)
	}
	return nil
}

func (c *client) lock(ctx context.Context, id string) (func(), error) {
	c.lockMu.Lock()
	ch, ok := c.locks[id]
	if !ok {
		ch = make(chan struct{}, 1)
		c.locks[id] = ch
	}
	c.lockMu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *client) Apply(ctx context.Context, p WritePlan, opts ...ApplyOption) (*ApplyResult, error) {
	cfg := applyConfig{poll: 5 * time.Second}
	for _, o := range opts {
		o(&cfg)
	}
	if p.DeviceID == "" {
		return nil, fmt.Errorf("%w: plan has no device", ErrInvalidValue)
	}
	if len(p.Steps) == 0 {
		return nil, fmt.Errorf("%w: plan has no steps", ErrInvalidValue)
	}
	res := &ApplyResult{Plan: p}
	if cfg.dryRun {
		res.DryRun = true
		return res, nil
	}
	unlock, err := c.lock(ctx, p.DeviceID)
	if err != nil {
		return nil, err
	}
	if p.Debounce != "" {
		if w := c.debounceWait(p.DeviceID, p.Debounce); w > 0 {
			unlock()
			return res, &RateLimitError{Key: p.Debounce, Wait: w}
		}
	}
	for i, st := range p.Steps {
		if st.After > 0 {
			select {
			case <-ctx.Done():
				unlock()
				return res, &PlanError{Applied: res.Applied, Step: i, Err: ctx.Err()}
			case <-time.After(st.After):
			}
		}
		if err := c.putFields(ctx, p.DeviceID, st.Fields); err != nil {
			unlock()
			return res, &PlanError{Applied: res.Applied, Step: i, Err: err}
		}
		res.Applied++
		if i == 0 && p.Debounce != "" {
			c.debMu.Lock()
			c.lastWrite[p.DeviceID+"|"+string(p.Debounce)] = c.now()
			c.debMu.Unlock()
		}
	}
	unlock()

	pred, timeout := cfg.pred, cfg.timeout
	if pred == nil && cfg.useDefault {
		pred, timeout = p.Confirm, p.ConfirmTimeout
	}
	if pred == nil {
		return res, nil
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	st, ok, err := c.waitFor(ctx, p.DeviceID, pred, timeout, cfg.poll)
	res.State = st
	if err != nil {
		return res, err
	}
	if !ok {
		return res, &NotConfirmedError{Timeout: timeout, Last: st}
	}
	res.Confirmed = true
	return res, nil
}

func (c *client) debounceWait(id string, key DebounceKey) time.Duration {
	win := c.s.debounce[key]
	if win <= 0 {
		return 0
	}
	c.debMu.Lock()
	defer c.debMu.Unlock()
	last, ok := c.lastWrite[id+"|"+string(key)]
	if !ok {
		return 0
	}
	if w := win - c.now().Sub(last); w > 0 {
		return w
	}
	return 0
}

func (c *client) waitFor(ctx context.Context, id string, pred func(DeviceState) bool, timeout, poll time.Duration) (*DeviceState, bool, error) {
	if poll <= 0 {
		poll = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var last *DeviceState
	for {
		st, err := c.State(ctx, id)
		if err == nil {
			last = st
			if pred(*st) {
				return st, true, nil
			}
		} else if ctx.Err() != nil {
			return last, false, ctx.Err()
		}
		if time.Now().Add(poll).After(deadline) {
			return last, false, nil
		}
		select {
		case <-ctx.Done():
			return last, false, ctx.Err()
		case <-time.After(poll):
		}
	}
}

func (c *client) accessToken(ctx context.Context) (string, time.Time, error) {
	s, err := c.auth.Session(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	return s.AccessToken, s.AccessExpiry, nil
}

func (c *client) Pump(id string) *Pump                     { return &Pump{c: c, ID: id} }
func (c *client) IntelliConnect(id string) *IntelliConnect { return &IntelliConnect{c: c, ID: id} }
func (c *client) Light(id string) *Light                   { return &Light{c: c, ID: id} }

// requireType fetches state and checks the device type.
func requireType(ctx context.Context, c Client, id string, want DeviceType) (*DeviceState, error) {
	st, err := c.State(ctx, id)
	if err != nil {
		return nil, err
	}
	if st.Type != "" && st.Type != want {
		return st, &DeviceError{DeviceID: id, Op: "type check",
			Err: fmt.Errorf("%w: device is %s (%s), not %s", ErrUnsupportedDevice, st.Type, st.Type.Description(), want.Description())}
	}
	return st, nil
}
