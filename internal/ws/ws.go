// Package ws is the Pentair real-time WebSocket client: connect with an
// access token, registerEvent per device, keep-alive, and read pushes.
package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/coder/websocket"

	"github.com/emergingrobotics/gopentair/internal/api"
)

// Conn is one subscription connection (one active_screen).
type Conn struct {
	c      *websocket.Conn
	Screen string
}

// Dial connects to baseURL?token=accessToken.
func Dial(ctx context.Context, baseURL, accessToken, screen string, hc *http.Client) (*Conn, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("token", accessToken)
	u.RawQuery = q.Encode()
	c, resp, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: hc})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("websocket dial: HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("websocket dial: %w", scrub(err))
	}
	c.SetReadLimit(4 << 20)
	return &Conn{c: c, Screen: screen}, nil
}

// scrub removes the URL (which carries the token) from dial errors.
func scrub(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

// Register sends registerEvent for one device.
func (c *Conn) Register(ctx context.Context, deviceID, accessToken string) error {
	b, _ := json.Marshal(api.WSRegister{
		Action: "registerEvent",
		Body:   api.WSRegisterBody{DeviceIDs: deviceID, ActiveScreen: c.Screen, Token: accessToken},
	})
	return c.c.Write(ctx, websocket.MessageText, b)
}

// KeepAlive sends the app's literal "keep-alive" text frame.
func (c *Conn) KeepAlive(ctx context.Context) error {
	return c.c.Write(ctx, websocket.MessageText, []byte("keep-alive"))
}

// Ping sends a protocol-level ping and waits for the pong.
func (c *Conn) Ping(ctx context.Context) error { return c.c.Ping(ctx) }

// Read returns the next device_data event, skipping anything else
// (acks, non-JSON frames, other event types).
func (c *Conn) Read(ctx context.Context) (*api.WSEvent, error) {
	for {
		typ, b, err := c.c.Read(ctx)
		if err != nil {
			return nil, err
		}
		if typ != websocket.MessageText || len(b) == 0 || b[0] != '{' {
			continue
		}
		var ev api.WSEvent
		if json.Unmarshal(b, &ev) != nil || ev.EventType != "device_data" || ev.DeviceID == "" {
			continue
		}
		return &ev, nil
	}
}

// Close closes the connection.
func (c *Conn) Close() error { return c.c.Close(websocket.StatusNormalClosure, "bye") }

// Backoff returns the reconnect delay for attempt n (1s doubling to 30s).
func Backoff(n int) time.Duration {
	d := time.Second << min(n, 5)
	return min(d, 30*time.Second)
}
