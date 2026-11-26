package gopentair

import (
	"encoding/json"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/emergingrobotics/gopentair/internal/api"
)

// DeviceType is Pentair's product code.
type DeviceType string

// Known device types.
const (
	TypeIntelliFlo3    DeviceType = "IF31" // IntelliFlo 3 VSF Wi-Fi pump
	TypeIntelliConnect DeviceType = "PIF0" // IntelliConnect controller
	TypeColorSync      DeviceType = "PLC1" // Color Sync light controller
	TypeIntelliCenter  DeviceType = "ICT1" // listed, fields not mapped
	TypeChemCheck      DeviceType = "WQM1" // listed, fields not mapped
)

// Description returns a human name for known types.
func (t DeviceType) Description() string {
	switch t {
	case TypeIntelliFlo3:
		return "IntelliFlo 3 pump"
	case TypeIntelliConnect:
		return "IntelliConnect"
	case TypeColorSync:
		return "Color Sync lights"
	case TypeIntelliCenter:
		return "IntelliCenter"
	case TypeChemCheck:
		return "ChemCheck"
	}
	return string(t)
}

// Device is an entry of the account's device list.
type Device struct {
	ID         string          `json:"deviceId"`
	Type       DeviceType      `json:"deviceType"`
	Product    string          `json:"product,omitempty"`
	Nickname   string          `json:"nickname,omitempty"`
	Model      string          `json:"model,omitempty"`
	Status     string          `json:"status,omitempty"`
	ARN        string          `json:"arn,omitempty"`
	Components map[string]any  `json:"components,omitempty"`
	Raw        json.RawMessage `json:"-"`
}

// Name returns the nickname, product name or type, whichever is set.
func (d Device) Name() string {
	switch {
	case d.Nickname != "":
		return d.Nickname
	case d.Product != "":
		return d.Product
	}
	return string(d.Type)
}

// Field is one device field. Values are always strings on the wire.
type Field struct {
	Code  string `json:"code"`
	Name  string `json:"name,omitempty"`
	Value string `json:"value"`
	Min   string `json:"min,omitempty"`
	Max   string `json:"max,omitempty"`
}

// DeviceState is a snapshot of a device's fields.
type DeviceState struct {
	DeviceID  string           `json:"deviceId"`
	Type      DeviceType       `json:"deviceType"`
	Online    bool             `json:"online"`
	Alarm     bool             `json:"alarm"`
	FWVersion string           `json:"fwVersion,omitempty"`
	Fields    map[string]Field `json:"fields"`
	UpdatedAt time.Time        `json:"updatedAt"`
	Raw       json.RawMessage  `json:"-"`
}

// Has reports whether a field is present.
func (s DeviceState) Has(code string) bool { _, ok := s.Fields[code]; return ok }

// Str returns a field's raw value.
func (s DeviceState) Str(code string) (string, bool) {
	f, ok := s.Fields[code]
	if !ok {
		return "", false
	}
	return f.Value, true
}

// Int parses a field as an integer (decimal values are truncated).
func (s DeviceState) Int(code string) (int64, bool) {
	v, ok := s.Str(code)
	if !ok {
		return 0, false
	}
	v = strings.TrimSpace(v)
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n, true
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return int64(f), true
	}
	return 0, false
}

// Float parses a field as a float.
func (s DeviceState) Float(code string) (float64, bool) {
	v, ok := s.Str(code)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	return f, err == nil
}

// Scaled returns the field divided by div.
func (s DeviceState) Scaled(code string, div float64) (float64, bool) {
	f, ok := s.Float(code)
	if !ok || div == 0 {
		return 0, false
	}
	return f / div, true
}

// Bool parses "1"/"0"/"true"/"false".
func (s DeviceState) Bool(code string) (bool, bool) {
	v, ok := s.Str(code)
	if !ok {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on":
		return true, true
	case "0", "false", "off":
		return false, true
	}
	return false, false
}

// Clone returns a deep copy.
func (s DeviceState) Clone() DeviceState {
	c := s
	c.Fields = maps.Clone(s.Fields)
	if c.Fields == nil {
		c.Fields = map[string]Field{}
	}
	c.Raw = nil
	return c
}

// Merge applies a delta and returns the codes whose value changed.
func (s *DeviceState) Merge(delta map[string]Field) []string {
	if s.Fields == nil {
		s.Fields = map[string]Field{}
	}
	var changed []string
	for code, f := range delta {
		old, ok := s.Fields[code]
		if f.Name == "" {
			f.Name = old.Name
		}
		if f.Min == "" {
			f.Min = old.Min
		}
		if f.Max == "" {
			f.Max = old.Max
		}
		if !ok || old.Value != f.Value {
			changed = append(changed, code)
		}
		s.Fields[code] = f
	}
	return changed
}

// Profile is the user profile, kept as a generic map plus common keys.
type Profile struct {
	Email     string         `json:"email,omitempty"`
	FirstName string         `json:"firstName,omitempty"`
	LastName  string         `json:"lastName,omitempty"`
	Raw       map[string]any `json:"raw"`
}

func toFields(t DeviceType, wf map[string]api.WireField) map[string]Field {
	out := make(map[string]Field, len(wf))
	for code, w := range wf {
		name := w.Name
		if name == "" {
			name = api.FieldName(string(t), code)
		}
		out[code] = Field{Code: code, Name: name, Value: api.RawString(w.Value), Min: api.RawString(w.Min), Max: api.RawString(w.Max)}
	}
	return out
}

func toDevice(w api.WireDevice, raw json.RawMessage) Device {
	return Device{
		ID:         w.DeviceID,
		Type:       DeviceType(w.DeviceType),
		Product:    w.Pname,
		Nickname:   w.ProductInfo.NickName,
		Model:      w.ProductInfo.Model,
		Status:     w.Status,
		ARN:        w.ARN,
		Components: w.Devices,
		Raw:        raw,
	}
}

func toState(w api.WireDevice, raw json.RawMessage, fallbackType DeviceType, now time.Time) DeviceState {
	t := DeviceType(w.DeviceType)
	if t == "" {
		t = fallbackType
	}
	s := DeviceState{
		DeviceID:  w.DeviceID,
		Type:      t,
		FWVersion: api.RawString(w.FwVersion),
		Fields:    toFields(t, w.Fields),
		UpdatedAt: now,
		Raw:       raw,
	}
	if w.Online != nil {
		s.Online = *w.Online
	}
	if w.Alarm != nil {
		s.Alarm = *w.Alarm
	}
	return s
}
