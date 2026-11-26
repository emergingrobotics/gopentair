// Package testutil provides a fake Pentair cloud: Cognito User Pool (with
// real server-side SRP), Cognito Identity, the SigV4-verified REST API and
// the WebSocket push API, all on one httptest.Server.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/emergingrobotics/gopentair/internal/sign"
	"github.com/emergingrobotics/gopentair/internal/srp"
)

// Clock is a controllable time source shared by the fake and the client.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

// NewClock starts at t.
func NewClock(t time.Time) *Clock { return &Clock{t: t} }

// Now returns the current fake time.
func (c *Clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

// Advance moves the clock forward.
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// Device is a fake device.
type Device struct {
	ID       string
	Type     string
	Nickname string
	Pname    string
	Online   bool
	Fields   map[string]string
}

// Write records one accepted field write.
type Write struct {
	DeviceID string
	Fields   map[string]string
}

// Failure is an injected REST failure.
type Failure struct {
	Status int
	Body   string
	// Match restricts the failure to a method ("" = any).
	Method string
}

// Fake is the fake cloud.
type Fake struct {
	T      testing.TB
	Server *httptest.Server
	Clock  *Clock

	PoolID, ClientID, IdentityPoolID, Region string
	TokenTTL, CredsTTL                       time.Duration

	// OnWrite lets tests emulate firmware reactions to writes. It runs
	// with the fake's lock held and may mutate dev.Fields.
	OnWrite func(dev *Device, fields map[string]string)

	mu          sync.Mutex
	users       map[string]*user
	pending     map[string]*srpPending
	idTokens    map[string]tokenInfo
	accTokens   map[string]tokenInfo
	refresh     map[string]string // refresh -> username
	creds       map[string]credInfo
	devices     []*Device
	writes      []Write
	failures    []Failure
	subs        map[*websocket.Conn]*sub
	counts      map[string]int
	lastBodies  map[string][]byte
	expireCreds bool
	ackCode     string
}

type user struct {
	username, password, sub, salt string
	verifier                      *big.Int
}

type srpPending struct {
	user  *user
	b, B  *big.Int
	A     string
	block []byte
}

type tokenInfo struct {
	user string
	exp  time.Time
}

type credInfo struct {
	secret, session string
	exp             time.Time
}

type sub struct {
	mu      sync.Mutex
	devices map[string]string // deviceID -> screen
}

// New starts a fake cloud. It is closed via t.Cleanup.
func New(t testing.TB) *Fake {
	f := &Fake{
		T:              t,
		Clock:          NewClock(time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)),
		PoolID:         "us-west-2_FakePool",
		ClientID:       "fakeclientid",
		IdentityPoolID: "us-west-2:00000000-0000-0000-0000-000000000000",
		Region:         "us-west-2",
		TokenTTL:       time.Hour,
		CredsTTL:       time.Hour,
		users:          map[string]*user{},
		pending:        map[string]*srpPending{},
		idTokens:       map[string]tokenInfo{},
		accTokens:      map[string]tokenInfo{},
		refresh:        map[string]string{},
		creds:          map[string]credInfo{},
		subs:           map[*websocket.Conn]*sub{},
		counts:         map[string]int{},
		lastBodies:     map[string][]byte{},
		ackCode:        "set_device_success",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/idp/", f.handleCognito)
	mux.HandleFunc("/identity/", f.handleCognito)
	mux.HandleFunc("/rest/", f.handleREST)
	mux.HandleFunc("/ws", f.handleWS)
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// Close shuts the server down.
func (f *Fake) Close() {
	f.mu.Lock()
	for c := range f.subs {
		_ = c.CloseNow()
	}
	f.mu.Unlock()
	f.Server.Close()
}

// URLs returns the endpoint base URLs.
func (f *Fake) URLs() (rest, ws, idp, identity string) {
	u := f.Server.URL
	return u + "/rest", "ws" + strings.TrimPrefix(u, "http") + "/ws", u + "/idp/", u + "/identity/"
}

// AddUser registers an account.
func (f *Fake) AddUser(username, password string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sub := "sub-" + randHex(4)
	salt := randHex(16)
	f.users[strings.ToLower(username)] = &user{
		username: username, password: password, sub: sub, salt: salt,
		verifier: srp.Verifier(f.PoolID, sub, password, salt),
	}
}

// AddDevice adds a device.
func (f *Fake) AddDevice(d *Device) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d.Fields == nil {
		d.Fields = map[string]string{}
	}
	f.devices = append(f.devices, d)
}

// Device returns a device by ID (for assertions; do not mutate concurrently).
func (f *Fake) Device(id string) *Device {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.devices {
		if d.ID == id {
			return d
		}
	}
	return nil
}

// SetField changes a field and pushes it to WebSocket subscribers.
func (f *Fake) SetField(id, code, value string) {
	f.mu.Lock()
	for _, d := range f.devices {
		if d.ID == id {
			d.Fields[code] = value
		}
	}
	f.mu.Unlock()
	f.Push(id, map[string]string{code: value})
}

// SetFieldQuietly changes a field without a WebSocket push (simulates a
// change the client can only see by polling).
func (f *Fake) SetFieldQuietly(id, code, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.devices {
		if d.ID == id {
			d.Fields[code] = value
		}
	}
}

// Field returns a device field value.
func (f *Fake) Field(id, code string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.devices {
		if d.ID == id {
			return d.Fields[code]
		}
	}
	return ""
}

// Writes returns accepted writes.
func (f *Fake) Writes() []Write {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Write(nil), f.writes...)
}

// Count returns how many times an operation ran ("srp", "refresh",
// "identity", "rest", "ws-connect", "register", "keepalive").
func (f *Fake) Count(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[op]
}

// LastBody returns the last request body for a REST path suffix.
func (f *Fake) LastBody(path string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastBodies[path]
}

// FailNext queues REST failures.
func (f *Fake) FailNext(fs ...Failure) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = append(f.failures, fs...)
}

// ExpireCredentials makes every issued AWS credential look expired to the
// REST API (until new credentials are minted).
func (f *Fake) ExpireCredentials() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, c := range f.creds {
		c.exp = time.Time{}
		f.creds[k] = c
	}
}

// RevokeRefreshTokens invalidates all refresh tokens.
func (f *Fake) RevokeRefreshTokens() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refresh = map[string]string{}
}

// SetAckCode changes the write ack code.
func (f *Fake) SetAckCode(code string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ackCode = code
}

// DropConnections closes all WebSocket connections.
func (f *Fake) DropConnections() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for c := range f.subs {
		_ = c.Close(websocket.StatusGoingAway, "drop")
		delete(f.subs, c)
	}
}

// Subscribers returns the number of (connection, device) registrations.
func (f *Fake) Subscribers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.subs {
		s.mu.Lock()
		n += len(s.devices)
		s.mu.Unlock()
	}
	return n
}

// Push sends a device_data delta to subscribers of the device.
func (f *Fake) Push(id string, fields map[string]string) {
	wf := map[string]any{}
	for k, v := range fields {
		wf[k] = map[string]string{"value": v}
	}
	f.mu.Lock()
	type target struct {
		c      *websocket.Conn
		screen string
	}
	var ts []target
	for c, s := range f.subs {
		s.mu.Lock()
		if scr, ok := s.devices[id]; ok {
			ts = append(ts, target{c, scr})
		}
		s.mu.Unlock()
	}
	f.mu.Unlock()
	for _, t := range ts {
		b, _ := json.Marshal(map[string]any{
			"event_type": "device_data", "deviceId": id, "screen_type": t.screen,
			"data": map[string]any{"fields": wf},
		})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = t.c.Write(ctx, websocket.MessageText, b)
		cancel()
	}
}

// --- Cognito ---------------------------------------------------------------

func cognitoErr(w http.ResponseWriter, typ, msg string) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(400)
	_ = json.NewEncoder(w).Encode(map[string]string{"__type": typ, "message": msg})
}

func (f *Fake) jwt(sub string, exp time.Time, kind string) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	p, _ := json.Marshal(map[string]any{"sub": sub, "exp": exp.Unix(), "token_use": kind, "jti": randHex(8)})
	return h + "." + base64.RawURLEncoding.EncodeToString(p) + "." + randHex(8)
}

func (f *Fake) issueLocked(u *user, withRefresh bool) map[string]any {
	exp := f.Clock.Now().Add(f.TokenTTL)
	id := f.jwt(u.sub, exp, "id")
	acc := f.jwt(u.sub, exp, "access")
	f.idTokens[id] = tokenInfo{u.username, exp}
	f.accTokens[acc] = tokenInfo{u.username, exp}
	ar := map[string]any{"IdToken": id, "AccessToken": acc, "ExpiresIn": int(f.TokenTTL.Seconds()), "TokenType": "Bearer"}
	if withRefresh {
		r := "refresh-" + randHex(16)
		f.refresh[r] = strings.ToLower(u.username)
		ar["RefreshToken"] = r
	}
	return map[string]any{"AuthenticationResult": ar, "ChallengeParameters": map[string]string{}}
}

func (f *Fake) handleCognito(w http.ResponseWriter, r *http.Request) {
	var in map[string]any
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &in)
	target := r.Header.Get("X-Amz-Target")
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	f.mu.Lock()
	defer f.mu.Unlock()
	str := func(m any, k string) string {
		if mm, ok := m.(map[string]any); ok {
			s, _ := mm[k].(string)
			return s
		}
		return ""
	}
	switch target {
	case "AWSCognitoIdentityProviderService.InitiateAuth":
		if in["ClientId"] != f.ClientID {
			cognitoErr(w, "ResourceNotFoundException", "User pool client does not exist.")
			return
		}
		params := in["AuthParameters"]
		switch in["AuthFlow"] {
		case "USER_SRP_AUTH":
			u := f.users[strings.ToLower(str(params, "USERNAME"))]
			if u == nil {
				cognitoErr(w, "UserNotFoundException", "User does not exist.")
				return
			}
			bb := make([]byte, 64)
			_, _ = rand.Read(bb)
			b := new(big.Int).SetBytes(bb)
			B := srp.ServerB(u.verifier, b)
			block := []byte("secret-block-" + randHex(8))
			sess := "sess-" + randHex(8)
			f.pending[sess] = &srpPending{user: u, b: b, B: B, A: str(params, "SRP_A"), block: block}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ChallengeName": "PASSWORD_VERIFIER",
				"Session":       sess,
				"ChallengeParameters": map[string]string{
					"USER_ID_FOR_SRP": u.sub, "USERNAME": u.sub, "SALT": u.salt,
					"SRP_B": B.Text(16), "SECRET_BLOCK": base64.StdEncoding.EncodeToString(block),
				},
			})
		case "REFRESH_TOKEN_AUTH":
			f.counts["refresh"]++
			name, ok := f.refresh[str(params, "REFRESH_TOKEN")]
			if !ok {
				cognitoErr(w, "NotAuthorizedException", "Invalid Refresh Token")
				return
			}
			_ = json.NewEncoder(w).Encode(f.issueLocked(f.users[name], false))
		default:
			cognitoErr(w, "InvalidParameterException", "unsupported flow")
		}
	case "AWSCognitoIdentityProviderService.RespondToAuthChallenge":
		sess, _ := in["Session"].(string)
		p := f.pending[sess]
		delete(f.pending, sess)
		if p == nil {
			cognitoErr(w, "NotAuthorizedException", "Invalid session")
			return
		}
		resp := in["ChallengeResponses"]
		ts := str(resp, "TIMESTAMP")
		if _, err := time.Parse(srp.TimestampLayout, ts); err != nil {
			cognitoErr(w, "InvalidParameterException", "bad timestamp")
			return
		}
		key, err := srp.ServerKey(p.A, p.user.verifier, p.b, p.B)
		if err != nil || srp.Signature(key, f.PoolID, p.user.sub, p.block, ts) != str(resp, "PASSWORD_CLAIM_SIGNATURE") {
			cognitoErr(w, "NotAuthorizedException", "Incorrect username or password.")
			return
		}
		f.counts["srp"]++
		_ = json.NewEncoder(w).Encode(f.issueLocked(p.user, true))
	case "AWSCognitoIdentityProviderService.GlobalSignOut":
		ti, ok := f.accTokens[str(in, "AccessToken")]
		if !ok {
			cognitoErr(w, "NotAuthorizedException", "Invalid Access Token")
			return
		}
		for r, u := range f.refresh {
			if u == strings.ToLower(ti.user) {
				delete(f.refresh, r)
			}
		}
		f.counts["signout"]++
		_, _ = w.Write([]byte("{}"))
	case "AWSCognitoIdentityService.GetId", "AWSCognitoIdentityService.GetCredentialsForIdentity":
		logins, _ := in["Logins"].(map[string]any)
		tok, _ := logins["cognito-idp."+f.Region+".amazonaws.com/"+f.PoolID].(string)
		ti, ok := f.idTokens[tok]
		if !ok || !f.Clock.Now().Before(ti.exp) {
			cognitoErr(w, "NotAuthorizedException", "Invalid login token.")
			return
		}
		if target == "AWSCognitoIdentityService.GetId" {
			if in["IdentityPoolId"] != f.IdentityPoolID {
				cognitoErr(w, "ResourceNotFoundException", "IdentityPool not found")
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"IdentityId": f.Region + ":id-" + strings.ToLower(ti.user)})
			return
		}
		f.counts["identity"]++
		ak := "ASIAFAKE" + strings.ToUpper(randHex(6))
		c := credInfo{secret: randHex(20), session: "st-" + randHex(16), exp: f.Clock.Now().Add(f.CredsTTL)}
		f.creds[ak] = c
		_ = json.NewEncoder(w).Encode(map[string]any{
			"IdentityId": in["IdentityId"],
			"Credentials": map[string]any{
				"AccessKeyId": ak, "SecretKey": c.secret, "SessionToken": c.session,
				"Expiration": float64(c.exp.Unix()),
			},
		})
	default:
		cognitoErr(w, "UnknownOperationException", target)
	}
}

// --- REST --------------------------------------------------------------------

func restErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

func (f *Fake) handleREST(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	path := strings.TrimPrefix(r.URL.Path, "/rest")
	f.mu.Lock()
	f.counts["rest"]++
	f.lastBodies[path] = body
	for i, fl := range f.failures {
		if fl.Method == "" || fl.Method == r.Method {
			f.failures = append(f.failures[:i], f.failures[i+1:]...)
			f.mu.Unlock()
			w.WriteHeader(fl.Status)
			_, _ = io.WriteString(w, fl.Body)
			return
		}
	}
	// Authenticate: SigV4 with an issued credential + valid id token.
	auth := r.Header.Get("Authorization")
	ak := ""
	if i := strings.Index(auth, "Credential="); i >= 0 {
		ak = strings.SplitN(auth[i+len("Credential="):], "/", 2)[0]
	}
	cred, ok := f.creds[ak]
	idt, idOK := f.idTokens[r.Header.Get("x-amz-id-token")]
	now := f.Clock.Now()
	f.mu.Unlock()
	if !ok {
		restErr(w, 403, "The security token included in the request is invalid.")
		return
	}
	if err := sign.Verify(r, body, cred.secret, f.Region, "execute-api"); err != nil {
		restErr(w, 403, "The request signature we calculated does not match the signature you provided. "+err.Error())
		return
	}
	if r.Header.Get("X-Amz-Security-Token") != cred.session {
		restErr(w, 403, "The security token included in the request is invalid.")
		return
	}
	if !now.Before(cred.exp) {
		restErr(w, 403, "The security token included in the request is expired")
		return
	}
	if !idOK || !now.Before(idt.exp) {
		restErr(w, 401, "Token expired")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && path == "/device2/device2-service/user/listdevices":
		var out []map[string]any
		for _, d := range f.devices {
			out = append(out, f.listingLocked(d))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"response": out, "allDevicesCount": len(out), "msgs": []any{}})
	case r.Method == http.MethodGet && path == "/device/device-service/user/devices":
		var out []map[string]any
		for _, d := range f.devices {
			out = append(out, f.listingLocked(d))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": out})
	case r.Method == http.MethodPost && path == "/device2/device2-service/user/device":
		var req struct {
			DeviceIDs []string `json:"deviceIds"`
		}
		_ = json.Unmarshal(body, &req)
		out := []map[string]any{}
		for _, id := range req.DeviceIDs { // singular "deviceId" -> empty list, like the real API
			for _, d := range f.devices {
				if d.ID == id {
					out = append(out, f.stateLocked(d))
				}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"code": "get_device_success", "data": out}})
	case strings.HasPrefix(path, "/device/device-service/user/device/"):
		id := strings.TrimPrefix(path, "/device/device-service/user/device/")
		var dev *Device
		for _, d := range f.devices {
			if d.ID == id {
				dev = d
			}
		}
		if dev == nil {
			restErr(w, 404, "device not found")
			return
		}
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": f.stateLocked(dev)})
			return
		}
		if r.Method != http.MethodPut {
			restErr(w, 405, "method not allowed")
			return
		}
		var req struct {
			Payload map[string]string `json:"payload"`
		}
		if err := json.Unmarshal(body, &req); err != nil || len(req.Payload) == 0 {
			restErr(w, 400, "invalid payload")
			return
		}
		if f.ackCode == "set_device_success" {
			for k, v := range req.Payload {
				dev.Fields[k] = v
			}
			if k := "appuse"; len(req.Payload) != 1 || req.Payload[k] == "" {
				f.writes = append(f.writes, Write{DeviceID: id, Fields: req.Payload})
			}
			if f.OnWrite != nil {
				f.OnWrite(dev, req.Payload)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"code": f.ackCode}})
		changed := map[string]string{}
		for k := range req.Payload {
			changed[k] = dev.Fields[k]
		}
		go f.Push(id, changed)
	case r.Method == http.MethodGet && (path == "/user/user-service/common/profilev2" || path == "/user/user-service/common/profile"):
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"email": idt.user, "firstName": "Test", "lastName": "User"}})
	default:
		restErr(w, 404, "no route "+r.Method+" "+path)
	}
}

func (f *Fake) listingLocked(d *Device) map[string]any {
	return map[string]any{
		"deviceId": d.ID, "deviceType": d.Type, "status": "ACTIVE", "pname": d.Pname,
		"arn":         "arn:aws:iot:us-west-2:000000000000:thing/" + d.ID,
		"productInfo": map[string]any{"nickName": d.Nickname, "model": d.Pname},
	}
}

func (f *Fake) stateLocked(d *Device) map[string]any {
	fields := map[string]any{}
	for k, v := range d.Fields {
		fields[k] = map[string]string{"value": v, "name": "", "min": "", "max": ""}
	}
	return map[string]any{
		"deviceId": d.ID, "deviceType": d.Type, "online": d.Online, "alarm": false,
		"fwVersion": "1.0", "fields": fields,
	}
}

// --- WebSocket ------------------------------------------------------------------

func (f *Fake) handleWS(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	f.mu.Lock()
	ti, ok := f.accTokens[tok]
	valid := ok && f.Clock.Now().Before(ti.exp)
	f.mu.Unlock()
	if !valid {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	s := &sub{devices: map[string]string{}}
	f.mu.Lock()
	f.subs[c] = s
	f.counts["ws-connect"]++
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.subs, c)
		f.mu.Unlock()
		_ = c.CloseNow()
	}()
	ctx := r.Context()
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			return
		}
		if string(b) == "keep-alive" {
			f.mu.Lock()
			f.counts["keepalive"]++
			f.mu.Unlock()
			continue
		}
		var msg struct {
			Action string `json:"action"`
			Body   struct {
				DeviceIDs    string `json:"device_ids"`
				ActiveScreen string `json:"active_screen"`
				Token        string `json:"token"`
			} `json:"body"`
		}
		if json.Unmarshal(b, &msg) != nil || msg.Action != "registerEvent" || msg.Body.Token != tok {
			continue
		}
		s.mu.Lock()
		s.devices[msg.Body.DeviceIDs] = msg.Body.ActiveScreen
		s.mu.Unlock()
		f.mu.Lock()
		f.counts["register"]++
		f.mu.Unlock()
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"message":"registered"}`))
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Itoa is a convenience for tests.
func Itoa(n int) string { return strconv.Itoa(n) }

// Must panics on error (test helper).
func Must(err error) {
	if err != nil {
		panic(fmt.Sprint(err))
	}
}
