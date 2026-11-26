package gopentair

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"
)

// IntelliConnect (PIF0) field codes.
const (
	fieldRelayState    = "ra0"  // pump on/off + schedule (RelayState)
	fieldRelayStart    = "ra1"  // schedule start, UTC seconds-of-day
	fieldRelayStop     = "ra2"  // schedule stop, UTC seconds-of-day
	fieldRelayTimer    = "ra3"  // configured timer, seconds
	fieldRelayPower    = "ra4"  // watts (physical truth)
	fieldRelayTimerRem = "ras0" // remaining timer, seconds
	fieldHeaterMode    = "htd1"
	fieldHeaterSet     = "htd2" // tenths of °F
	fieldHeaterAux     = "htd13"
	fieldHeaterCool    = "htd14" // seconds
	fieldChlorSet      = "icd1"  // %
	fieldChlorBoostRPM = "icd2"
	fieldChlorBoostTmr = "icd3"
	fieldChlorActual   = "ics1"
	fieldChlorSalt     = "ics2"
	fieldChlorError    = "ics3"
	fieldChlorTemp     = "ics9"
	fieldChlorFW       = "ics11"
	fieldChlorModel    = "ics12"
	fieldChlorBoostRem = "ics13"
	fieldChlorHours    = "ics15"
	fieldWaterTemp     = "t0" // whole °F
	fieldAirTemp       = "t1" // whole °F
	fieldIFPower       = "ifs3"
	fieldIFSpeed       = "ifs4"
	fieldIFFlow        = "ifs6"
)

// RelayState is ra0: the pump's intent (override and schedule bits).
type RelayState int

// ra0 values.
const (
	RelayOffNoSchedule RelayState = 0
	RelayOnNoSchedule  RelayState = 1
	RelayOffScheduled  RelayState = 2
	RelayOnScheduled   RelayState = 3
	RelayTimerDone     RelayState = 4 // scheduled, timer expired
)

// ScheduleEnabled reports the schedule bit.
func (r RelayState) ScheduleEnabled() bool { return r == 2 || r == 3 || r == 4 }

// OverrideOn reports the manual-on bit.
func (r RelayState) OverrideOn() bool { return r == 1 || r == 3 }

func (r RelayState) String() string {
	switch r {
	case RelayOffNoSchedule:
		return "off"
	case RelayOnNoSchedule:
		return "on (manual)"
	case RelayOffScheduled:
		return "schedule"
	case RelayOnScheduled:
		return "on (override, schedule enabled)"
	case RelayTimerDone:
		return "schedule (timer done)"
	}
	return "state-" + strconv.Itoa(int(r))
}

func relayFor(on, schedule bool) RelayState {
	switch {
	case on && schedule:
		return RelayOnScheduled
	case on:
		return RelayOnNoSchedule
	case schedule:
		return RelayOffScheduled
	}
	return RelayOffNoSchedule
}

// HeaterMode is htd1.
type HeaterMode int

// htd1 values. Write HeaterAuto to turn the heater on; the firmware shows
// HeaterHeating while it fires.
const (
	HeaterOff       HeaterMode = 0
	HeaterAuto      HeaterMode = 1
	HeaterSchedRun  HeaterMode = 2 // tentative
	HeaterHeating   HeaterMode = 3
	HeaterSchedWill HeaterMode = 4 // tentative
)

func (h HeaterMode) String() string {
	switch h {
	case HeaterOff:
		return "off"
	case HeaterAuto:
		return "auto idle"
	case HeaterSchedRun:
		return "schedule running"
	case HeaterHeating:
		return "heating"
	case HeaterSchedWill:
		return "schedule will run"
	}
	return "mode-" + strconv.Itoa(int(h))
}

// ChlorStatus is the IntelliChlor salt cell state.
type ChlorStatus struct {
	SetPct         *int64         `json:"setPct,omitempty"`
	ActualPct      *int64         `json:"actualPct,omitempty"`
	SaltPPM        *int64         `json:"saltPPM,omitempty"`
	ErrorCode      string         `json:"errorCode,omitempty"`
	CellTempF      *int64         `json:"cellTempF,omitempty"`
	BoostRemaining *time.Duration `json:"boostRemaining,omitempty"`
	Hours          *int64         `json:"hours,omitempty"`
	Model          string         `json:"model,omitempty"`
	Firmware       string         `json:"firmware,omitempty"`
}

// PoolStatus is the decoded PIF0 state.
type PoolStatus struct {
	Online          bool           `json:"online"`
	Alarm           bool           `json:"alarm"`
	PumpRunning     bool           `json:"pumpRunning"` // ra4 > 0
	PumpPowerW      *int64         `json:"pumpPowerW,omitempty"`
	Relay           *RelayState    `json:"relay,omitempty"`
	RelayName       string         `json:"relayName,omitempty"`
	ScheduleOn      bool           `json:"scheduleOn"`
	ScheduleStart   *ClockUTC      `json:"scheduleStartUTC,omitempty"`
	ScheduleStop    *ClockUTC      `json:"scheduleStopUTC,omitempty"`
	TimerRemaining  *time.Duration `json:"timerRemaining,omitempty"`
	WaterTempF      *int64         `json:"waterTempF,omitempty"`
	AirTempF        *int64         `json:"airTempF,omitempty"`
	Heater          *HeaterMode    `json:"heater,omitempty"`
	HeaterName      string         `json:"heaterName,omitempty"`
	HeaterSetpointF *float64       `json:"heaterSetpointF,omitempty"`
	HeaterCooldown  *time.Duration `json:"heaterCooldown,omitempty"`
	VSPowerW        *int64         `json:"vsPowerW,omitempty"` // ifs3, if a VS pump is attached
	VSSpeed         *int64         `json:"vsSpeed,omitempty"`
	VSFlow          *int64         `json:"vsFlow,omitempty"`
	Chlor           *ChlorStatus   `json:"chlor,omitempty"`
}

func secs(s DeviceState, code string) *time.Duration {
	if v, ok := s.Int(code); ok {
		return ptr(time.Duration(v) * time.Second)
	}
	return nil
}

func intp(s DeviceState, code string) *int64 {
	if v, ok := s.Int(code); ok {
		return ptr(v)
	}
	return nil
}

// DecodePool decodes an IntelliConnect state.
func DecodePool(s DeviceState) PoolStatus {
	p := PoolStatus{Online: s.Online, Alarm: s.Alarm}
	p.PumpPowerW = intp(s, fieldRelayPower)
	p.PumpRunning = p.PumpPowerW != nil && *p.PumpPowerW > 0
	if v, ok := s.Int(fieldRelayState); ok {
		r := RelayState(v)
		p.Relay = &r
		p.RelayName = r.String()
		p.ScheduleOn = r.ScheduleEnabled()
	}
	if v, ok := s.Int(fieldRelayStart); ok {
		p.ScheduleStart = ptr(ClockUTC(v % 86400))
	}
	if v, ok := s.Int(fieldRelayStop); ok {
		p.ScheduleStop = ptr(ClockUTC(v % 86400))
	}
	p.TimerRemaining = secs(s, fieldRelayTimerRem)
	p.WaterTempF = intp(s, fieldWaterTemp)
	p.AirTempF = intp(s, fieldAirTemp)
	if v, ok := s.Int(fieldHeaterMode); ok {
		h := HeaterMode(v)
		p.Heater = &h
		p.HeaterName = h.String()
	}
	if v, ok := s.Scaled(fieldHeaterSet, 10); ok {
		p.HeaterSetpointF = ptr(round1(v))
	}
	p.HeaterCooldown = secs(s, fieldHeaterCool)
	p.VSPowerW = intp(s, fieldIFPower)
	p.VSSpeed = intp(s, fieldIFSpeed)
	p.VSFlow = intp(s, fieldIFFlow)
	if s.Has(fieldChlorSet) || s.Has(fieldChlorSalt) || s.Has(fieldChlorActual) {
		c := &ChlorStatus{
			SetPct:         intp(s, fieldChlorSet),
			ActualPct:      intp(s, fieldChlorActual),
			SaltPPM:        intp(s, fieldChlorSalt),
			CellTempF:      intp(s, fieldChlorTemp),
			BoostRemaining: secs(s, fieldChlorBoostRem),
			Hours:          intp(s, fieldChlorHours),
		}
		c.ErrorCode, _ = s.Str(fieldChlorError)
		c.Model, _ = s.Str(fieldChlorModel)
		c.Firmware, _ = s.Str(fieldChlorFW)
		p.Chlor = c
	}
	return p
}

func currentRelay(s DeviceState) (RelayState, error) {
	v, ok := s.Int(fieldRelayState)
	if !ok {
		return 0, fmt.Errorf("%w: device does not report %s", ErrUnsupportedDevice, fieldRelayState)
	}
	return RelayState(v), nil
}

// PoolPumpPlan turns the filter pump on/off, preserving the schedule bit.
func PoolPumpPlan(s DeviceState, on bool) (WritePlan, error) {
	r, err := currentRelay(s)
	if err != nil {
		return WritePlan{}, err
	}
	target := relayFor(on, r.ScheduleEnabled())
	word := map[bool]string{true: "on", false: "off"}[on]
	return WritePlan{
		DeviceID:    s.DeviceID,
		Description: "filter pump " + word,
		Steps:       []WriteStep{{Fields: map[string]string{fieldRelayState: strconv.Itoa(int(target))}, Note: target.String()}},
		Confirm: func(ds DeviceState) bool {
			p := DecodePool(ds)
			return p.PumpRunning == on
		},
		ConfirmTimeout: 90 * time.Second,
	}, nil
}

// PoolSchedulePlan enables/disables the daily schedule, preserving the
// manual override bit.
func PoolSchedulePlan(s DeviceState, enabled bool) (WritePlan, error) {
	r, err := currentRelay(s)
	if err != nil {
		return WritePlan{}, err
	}
	target := relayFor(r.OverrideOn(), enabled)
	word := map[bool]string{true: "enable", false: "disable"}[enabled]
	return WritePlan{
		DeviceID:    s.DeviceID,
		Description: word + " daily schedule",
		Steps:       []WriteStep{{Fields: map[string]string{fieldRelayState: strconv.Itoa(int(target))}, Note: target.String()}},
		Confirm: func(ds DeviceState) bool {
			v, ok := ds.Int(fieldRelayState)
			return ok && RelayState(v).ScheduleEnabled() == enabled
		},
		ConfirmTimeout: 60 * time.Second,
	}, nil
}

// PoolScheduleTimesPlan sets the daily schedule window (UTC seconds-of-day).
func PoolScheduleTimesPlan(deviceID string, start, stop ClockUTC) WritePlan {
	return WritePlan{
		DeviceID:    deviceID,
		Description: fmt.Sprintf("set daily schedule %s - %s", start, stop),
		Steps: []WriteStep{{Fields: map[string]string{
			fieldRelayStart: strconv.Itoa(int(start)),
			fieldRelayStop:  strconv.Itoa(int(stop)),
		}}},
		Confirm: func(ds DeviceState) bool {
			a, ok1 := ds.Int(fieldRelayStart)
			b, ok2 := ds.Int(fieldRelayStop)
			return ok1 && ok2 && ClockUTC(a) == start && ClockUTC(b) == stop
		},
		ConfirmTimeout: 60 * time.Second,
	}
}

// HeaterPlan sets the heater mode and/or setpoint (°F). on=nil leaves the
// mode unchanged.
func HeaterPlan(deviceID string, on *bool, setpointF *float64) (WritePlan, error) {
	if on == nil && setpointF == nil {
		return WritePlan{}, fmt.Errorf("%w: nothing to change", ErrInvalidValue)
	}
	fields := map[string]string{}
	desc := "heater"
	if setpointF != nil {
		f := *setpointF
		if f < 40 || f > 104 {
			return WritePlan{}, fmt.Errorf("%w: setpoint %.1f°F outside 40-104", ErrInvalidValue, f)
		}
		fields[fieldHeaterSet] = strconv.Itoa(int(math.Round(f * 10)))
		desc += fmt.Sprintf(" setpoint %.1f°F", f)
	}
	if on != nil {
		if *on {
			fields[fieldHeaterMode] = strconv.Itoa(int(HeaterAuto))
			desc += " on (auto)"
		} else {
			fields[fieldHeaterMode] = strconv.Itoa(int(HeaterOff))
			fields[fieldHeaterAux] = "0"
			desc += " off"
		}
	}
	return WritePlan{
		DeviceID:    deviceID,
		Description: desc,
		Steps:       []WriteStep{{Fields: fields}},
		Confirm: func(ds DeviceState) bool {
			p := DecodePool(ds)
			if on != nil {
				if p.Heater == nil || (*on != (*p.Heater != HeaterOff)) {
					return false
				}
			}
			if setpointF != nil {
				if p.HeaterSetpointF == nil || math.Abs(*p.HeaterSetpointF-*setpointF) > 0.05 {
					return false
				}
			}
			return true
		},
		ConfirmTimeout: 60 * time.Second,
	}, nil
}

// ChlorinePlan sets the IntelliChlor output percentage.
func ChlorinePlan(deviceID string, pct int) (WritePlan, error) {
	if pct < 0 || pct > 100 {
		return WritePlan{}, fmt.Errorf("%w: chlorine %d%% outside 0-100", ErrInvalidValue, pct)
	}
	return WritePlan{
		DeviceID:    deviceID,
		Description: fmt.Sprintf("chlorine output %d%%", pct),
		Steps:       []WriteStep{{Fields: map[string]string{fieldChlorSet: strconv.Itoa(pct)}}},
		Confirm: func(ds DeviceState) bool {
			v, ok := ds.Int(fieldChlorSet)
			return ok && v == int64(pct)
		},
		ConfirmTimeout: 60 * time.Second,
	}, nil
}

// IntelliConnect is a handle for an IntelliConnect controller.
type IntelliConnect struct {
	c  Client
	ID string
}

// NewIntelliConnect binds a handle to any Client implementation.
func NewIntelliConnect(c Client, id string) *IntelliConnect { return &IntelliConnect{c: c, ID: id} }

// Status fetches and decodes the controller state.
func (p *IntelliConnect) Status(ctx context.Context) (PoolStatus, *DeviceState, error) {
	st, err := requireType(ctx, p.c, p.ID, TypeIntelliConnect)
	if err != nil {
		return PoolStatus{}, st, err
	}
	return DecodePool(*st), st, nil
}

// PumpPlan returns the plan to switch the filter pump.
func (p *IntelliConnect) PumpPlan(ctx context.Context, on bool) (WritePlan, error) {
	_, st, err := p.Status(ctx)
	if err != nil {
		return WritePlan{}, err
	}
	return PoolPumpPlan(*st, on)
}

// SchedulePlan returns the plan to enable/disable the daily schedule.
func (p *IntelliConnect) SchedulePlan(ctx context.Context, enabled bool) (WritePlan, error) {
	_, st, err := p.Status(ctx)
	if err != nil {
		return WritePlan{}, err
	}
	return PoolSchedulePlan(*st, enabled)
}

// SetPump switches the filter pump.
func (p *IntelliConnect) SetPump(ctx context.Context, on bool, opts ...ApplyOption) (*ApplyResult, error) {
	plan, err := p.PumpPlan(ctx, on)
	if err != nil {
		return nil, err
	}
	return p.c.Apply(ctx, plan, opts...)
}

// SetSchedule enables/disables the daily schedule.
func (p *IntelliConnect) SetSchedule(ctx context.Context, enabled bool, opts ...ApplyOption) (*ApplyResult, error) {
	plan, err := p.SchedulePlan(ctx, enabled)
	if err != nil {
		return nil, err
	}
	return p.c.Apply(ctx, plan, opts...)
}

// SetHeater changes heater mode and/or setpoint.
func (p *IntelliConnect) SetHeater(ctx context.Context, on *bool, setpointF *float64, opts ...ApplyOption) (*ApplyResult, error) {
	plan, err := HeaterPlan(p.ID, on, setpointF)
	if err != nil {
		return nil, err
	}
	return p.c.Apply(ctx, plan, opts...)
}

// SetChlorine sets the salt cell output.
func (p *IntelliConnect) SetChlorine(ctx context.Context, pct int, opts ...ApplyOption) (*ApplyResult, error) {
	plan, err := ChlorinePlan(p.ID, pct)
	if err != nil {
		return nil, err
	}
	return p.c.Apply(ctx, plan, opts...)
}
