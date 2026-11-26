package gopentair

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Color Sync (PLC1) field codes.
const (
	fieldLightShow  = "d1"
	fieldLightPower = "d13"
)

// LightStatus is the decoded PLC1 state.
type LightStatus struct {
	Online   bool       `json:"online"`
	On       *bool      `json:"on,omitempty"`
	Show     *LightShow `json:"show,omitempty"`
	ShowName string     `json:"showName,omitempty"`
}

// DecodeLight decodes a Color Sync state.
func DecodeLight(s DeviceState) LightStatus {
	l := LightStatus{Online: s.Online}
	if v, ok := s.Bool(fieldLightPower); ok {
		l.On = ptr(v)
	}
	if v, ok := s.Int(fieldLightShow); ok {
		sh := LightShow(v)
		l.Show = &sh
		l.ShowName = sh.String()
	}
	return l
}

// ParseLightShow parses a show name or number (case-insensitive).
func ParseLightShow(name string) (LightShow, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.NewReplacer(" ", "", "-", "", "_", "", ".", "").Replace(n)
	for k, v := range showNames {
		if v == n {
			return k, nil
		}
	}
	if i, err := strconv.Atoi(n); err == nil {
		if _, ok := showNames[LightShow(i)]; ok {
			return LightShow(i), nil
		}
	}
	return 0, fmt.Errorf("%w: unknown light show %q", ErrInvalidValue, name)
}

// LightPowerPlan switches the lights.
func LightPowerPlan(deviceID string, on bool) WritePlan {
	v := map[bool]string{true: "1", false: "0"}[on]
	return WritePlan{
		DeviceID:    deviceID,
		Description: "lights " + map[bool]string{true: "on", false: "off"}[on],
		Steps:       []WriteStep{{Fields: map[string]string{fieldLightPower: v}}},
		Confirm: func(ds DeviceState) bool {
			b, ok := ds.Bool(fieldLightPower)
			return ok && b == on
		},
		ConfirmTimeout: 30 * time.Second,
	}
}

// LightShowPlan selects a show/color, or sends hold/recall.
func LightShowPlan(deviceID string, show LightShow) (WritePlan, error) {
	if _, ok := showNames[show]; !ok {
		return WritePlan{}, fmt.Errorf("%w: light show %d", ErrInvalidValue, int(show))
	}
	p := WritePlan{
		DeviceID:    deviceID,
		Description: "lights: " + show.String(),
		Steps:       []WriteStep{{Fields: map[string]string{fieldLightShow: strconv.Itoa(int(show))}}},
	}
	if !show.IsCommand() {
		p.Confirm = func(ds DeviceState) bool {
			v, ok := ds.Int(fieldLightShow)
			return ok && LightShow(v) == show
		}
		p.ConfirmTimeout = 30 * time.Second
	}
	return p, nil
}

// Light is a handle for a Color Sync controller.
type Light struct {
	c  Client
	ID string
}

// NewLight binds a handle to any Client implementation.
func NewLight(c Client, id string) *Light { return &Light{c: c, ID: id} }

// Status fetches and decodes the light state.
func (l *Light) Status(ctx context.Context) (LightStatus, *DeviceState, error) {
	st, err := requireType(ctx, l.c, l.ID, TypeColorSync)
	if err != nil {
		return LightStatus{}, st, err
	}
	return DecodeLight(*st), st, nil
}

// SetPower switches the lights.
func (l *Light) SetPower(ctx context.Context, on bool, opts ...ApplyOption) (*ApplyResult, error) {
	if _, _, err := l.Status(ctx); err != nil {
		return nil, err
	}
	return l.c.Apply(ctx, LightPowerPlan(l.ID, on), opts...)
}

// SetShow selects a show or sends hold/recall.
func (l *Light) SetShow(ctx context.Context, show LightShow, opts ...ApplyOption) (*ApplyResult, error) {
	if _, _, err := l.Status(ctx); err != nil {
		return nil, err
	}
	p, err := LightShowPlan(l.ID, show)
	if err != nil {
		return nil, err
	}
	return l.c.Apply(ctx, p, opts...)
}

// Light show definitions for Color Sync (PLC1).

// LightShow is the d1 value of a Color Sync controller.
type LightShow int

// Shows and colors. 5 and 6 are commands (hold, recall), not shows.
const (
	ShowRed       LightShow = 0
	ShowWhite     LightShow = 1
	ShowMagenta   LightShow = 2
	ShowGreen     LightShow = 3
	ShowBlue      LightShow = 4
	CmdHold       LightShow = 5
	CmdRecall     LightShow = 6
	ShowSAm       LightShow = 7
	ShowParty     LightShow = 8
	ShowRomance   LightShow = 9
	ShowCaribbean LightShow = 10
	ShowAmerican  LightShow = 11
	ShowSunset    LightShow = 12
	ShowRoyal     LightShow = 13
)

var showNames = map[LightShow]string{
	ShowRed: "red", ShowWhite: "white", ShowMagenta: "magenta", ShowGreen: "green", ShowBlue: "blue",
	CmdHold: "hold", CmdRecall: "recall", ShowSAm: "sam", ShowParty: "party", ShowRomance: "romance",
	ShowCaribbean: "caribbean", ShowAmerican: "american", ShowSunset: "sunset", ShowRoyal: "royal",
}

func (l LightShow) String() string {
	if n, ok := showNames[l]; ok {
		return n
	}
	return "show-" + strconv.Itoa(int(l))
}

// IsCommand reports hold/recall.
func (l LightShow) IsCommand() bool { return l == CmdHold || l == CmdRecall }

// LightShows lists selectable shows in d1 order (commands excluded).
func LightShows() []LightShow {
	var out []LightShow
	for k := range showNames {
		if !k.IsCommand() {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}
