package gopentair

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// IntelliFlo 3 (IF31) field codes.
const (
	fieldPumpRSSI          = "s13"
	fieldPumpActiveProgram = "s14" // 0-based
	fieldPumpPressure      = "s17" // hundredths of psi
	fieldPumpPower         = "s18" // watts
	fieldPumpSpeed         = "s19" // tenths of %
	fieldPumpAlarm         = "s20"
	fieldPumpEnabled       = "s25"
	fieldPumpFlow          = "s26" // tenths of GPM
	fieldPumpRemaining     = "s28"
	fieldPumpLastProgram   = "p2"
	fieldPumpNickname      = "d27"
)

// MaxPumpPrograms is the number of program slots on an IntelliFlo 3.
const MaxPumpPrograms = 14

// Program enable values for zpNe10.
const (
	programStartValue = "3"
	programStopValue  = "2"
)

// ProgramType is zpNe5.
type ProgramType int

// Program types.
const (
	ProgramSchedule ProgramType = 0
	ProgramInterval ProgramType = 1
	ProgramManual   ProgramType = 2
)

func (t ProgramType) String() string {
	switch t {
	case ProgramSchedule:
		return "schedule"
	case ProgramInterval:
		return "interval"
	case ProgramManual:
		return "manual"
	}
	return "type-" + strconv.Itoa(int(t))
}

// Program is one IntelliFlo program slot. Start, duration and days are
// exposed raw because their units are not yet confirmed.
type Program struct {
	Number      int         `json:"number"` // 1..14
	Name        string      `json:"name"`
	Type        ProgramType `json:"type"`
	TypeName    string      `json:"typeName"`
	EnableValue string      `json:"enableValue"` // raw zpNe10
	StartRaw    *int64      `json:"startRaw,omitempty"`
	DurationRaw *int64      `json:"durationRaw,omitempty"`
	DaysRaw     *int64      `json:"daysRaw,omitempty"`
	Value       *int64      `json:"value,omitempty"` // zpNe4 (speed/flow target)
	Running     bool        `json:"running"`
}

// PumpStatus is the decoded IF31 state. Nil pointers mean "not reported".
type PumpStatus struct {
	Online        bool      `json:"online"`
	PowerW        *int64    `json:"powerW,omitempty"`
	SpeedPct      *float64  `json:"speedPct,omitempty"`
	FlowGPM       *float64  `json:"flowGPM,omitempty"`
	PressurePSI   *float64  `json:"pressurePSI,omitempty"`
	AlarmBits     *int64    `json:"alarmBits,omitempty"`
	Enabled       *bool     `json:"enabled,omitempty"`
	ActiveProgram *int      `json:"activeProgram,omitempty"` // 1-based
	RemainingRaw  *int64    `json:"remainingRaw,omitempty"`
	RSSI          *int64    `json:"rssi,omitempty"`
	Nickname      string    `json:"nickname,omitempty"`
	Programs      []Program `json:"programs"`
}

// Running reports whether the pump is drawing power.
func (p PumpStatus) Running() bool { return p.PowerW != nil && *p.PowerW > 0 }

func progField(n, e int) string { return fmt.Sprintf("zp%de%d", n, e) }

// DecodePump decodes an IF31 state.
func DecodePump(s DeviceState) PumpStatus {
	p := PumpStatus{Online: s.Online}
	if v, ok := s.Int(fieldPumpPower); ok {
		p.PowerW = ptr(v)
	}
	if v, ok := s.Scaled(fieldPumpSpeed, 10); ok {
		p.SpeedPct = ptr(round1(v))
	}
	if v, ok := s.Scaled(fieldPumpFlow, 10); ok {
		p.FlowGPM = ptr(round1(v))
	}
	if v, ok := s.Scaled(fieldPumpPressure, 100); ok {
		p.PressurePSI = ptr(v)
	}
	if v, ok := s.Int(fieldPumpAlarm); ok {
		p.AlarmBits = ptr(v)
	}
	if v, ok := s.Bool(fieldPumpEnabled); ok {
		p.Enabled = ptr(v)
	}
	if v, ok := s.Int(fieldPumpRemaining); ok {
		p.RemainingRaw = ptr(v)
	}
	if v, ok := s.Int(fieldPumpRSSI); ok {
		p.RSSI = ptr(v)
	}
	p.Nickname, _ = s.Str(fieldPumpNickname)
	active := -1
	if v, ok := s.Int(fieldPumpActiveProgram); ok && v >= 0 && v < MaxPumpPrograms {
		active = int(v) + 1
	}
	for n := 1; n <= MaxPumpPrograms; n++ {
		exists, ok := s.Bool(progField(n, 13))
		if !ok || !exists {
			continue
		}
		pr := Program{Number: n}
		pr.Name, _ = s.Str(progField(n, 2))
		if t, ok := s.Int(progField(n, 5)); ok {
			pr.Type = ProgramType(t)
		}
		pr.TypeName = pr.Type.String()
		pr.EnableValue, _ = s.Str(progField(n, 10))
		if v, ok := s.Int(progField(n, 4)); ok {
			pr.Value = ptr(v)
		}
		if v, ok := s.Int(progField(n, 6)); ok {
			pr.StartRaw = ptr(v)
		}
		if v, ok := s.Int(progField(n, 7)); ok {
			pr.DurationRaw = ptr(v)
		}
		if v, ok := s.Int(progField(n, 8)); ok {
			pr.DaysRaw = ptr(v)
		}
		pr.Running = n == active && (p.PowerW == nil || *p.PowerW > 0)
		p.Programs = append(p.Programs, pr)
	}
	if active > 0 {
		for _, pr := range p.Programs {
			if pr.Number == active && pr.Running {
				p.ActiveProgram = ptr(active)
			}
		}
	}
	return p
}

// FindProgram resolves "3" or a program name (case-insensitive).
func (p PumpStatus) FindProgram(ref string) (Program, error) {
	if n, err := strconv.Atoi(strings.TrimSpace(ref)); err == nil {
		for _, pr := range p.Programs {
			if pr.Number == n {
				return pr, nil
			}
		}
		return Program{}, fmt.Errorf("%w: program %d does not exist", ErrInvalidValue, n)
	}
	for _, pr := range p.Programs {
		if strings.EqualFold(pr.Name, ref) {
			return pr, nil
		}
	}
	return Program{}, fmt.Errorf("%w: no program named %q", ErrInvalidValue, ref)
}

// PumpStartPlan builds the writes that start program n. When another
// program is running it is not stopped first: the firmware swaps profiles
// in place, avoiding a ~30 s restart.
func PumpStartPlan(s DeviceState, n int) (WritePlan, error) {
	if n < 1 || n > MaxPumpPrograms {
		return WritePlan{}, fmt.Errorf("%w: program %d out of range 1-%d", ErrInvalidValue, n, MaxPumpPrograms)
	}
	st := DecodePump(s)
	pr, err := st.FindProgram(strconv.Itoa(n))
	if err != nil {
		return WritePlan{}, err
	}
	return WritePlan{
		DeviceID:    s.DeviceID,
		Description: fmt.Sprintf("start pump program %d (%s)", n, pr.Name),
		Steps: []WriteStep{
			{Fields: map[string]string{progField(n, 10): programStartValue}, Note: "enable program"},
			{Fields: map[string]string{fieldPumpLastProgram: "99"}, Note: "last active program marker (as the app does)"},
		},
		Debounce: DebounceProgramStart,
		Confirm: func(ds DeviceState) bool {
			a := DecodePump(ds).ActiveProgram
			return a != nil && *a == n
		},
		ConfirmTimeout: 90 * time.Second,
	}, nil
}

// PumpStopPlan builds the writes that stop program n.
func PumpStopPlan(s DeviceState, n int) (WritePlan, error) {
	if n < 1 || n > MaxPumpPrograms {
		return WritePlan{}, fmt.Errorf("%w: program %d out of range 1-%d", ErrInvalidValue, n, MaxPumpPrograms)
	}
	st := DecodePump(s)
	pr, err := st.FindProgram(strconv.Itoa(n))
	if err != nil {
		return WritePlan{}, err
	}
	return WritePlan{
		DeviceID:    s.DeviceID,
		Description: fmt.Sprintf("stop pump program %d (%s)", n, pr.Name),
		Steps: []WriteStep{
			{Fields: map[string]string{progField(n, 10): programStopValue}, Note: "disable program"},
			{Fields: map[string]string{fieldPumpLastProgram: strconv.Itoa(n - 1)}, Note: "last active program"},
		},
		Confirm: func(ds DeviceState) bool {
			a := DecodePump(ds).ActiveProgram
			return a == nil || *a != n
		},
		ConfirmTimeout: 90 * time.Second,
	}, nil
}

// Pump is a handle for an IntelliFlo 3 pump.
type Pump struct {
	c  Client
	ID string
}

// NewPump binds a pump handle to any Client implementation.
func NewPump(c Client, id string) *Pump { return &Pump{c: c, ID: id} }

// Status fetches and decodes the pump state.
func (p *Pump) Status(ctx context.Context) (PumpStatus, *DeviceState, error) {
	st, err := requireType(ctx, p.c, p.ID, TypeIntelliFlo3)
	if err != nil {
		return PumpStatus{}, st, err
	}
	return DecodePump(*st), st, nil
}

// StartPlan resolves ref (number or name) and returns the start plan.
func (p *Pump) StartPlan(ctx context.Context, ref string) (WritePlan, error) {
	ps, st, err := p.Status(ctx)
	if err != nil {
		return WritePlan{}, err
	}
	pr, err := ps.FindProgram(ref)
	if err != nil {
		return WritePlan{}, err
	}
	return PumpStartPlan(*st, pr.Number)
}

// StopPlan resolves ref (number or name) and returns the stop plan.
func (p *Pump) StopPlan(ctx context.Context, ref string) (WritePlan, error) {
	ps, st, err := p.Status(ctx)
	if err != nil {
		return WritePlan{}, err
	}
	pr, err := ps.FindProgram(ref)
	if err != nil {
		return WritePlan{}, err
	}
	return PumpStopPlan(*st, pr.Number)
}

// Start starts a program (number or name).
func (p *Pump) Start(ctx context.Context, ref string, opts ...ApplyOption) (*ApplyResult, error) {
	plan, err := p.StartPlan(ctx, ref)
	if err != nil {
		return nil, err
	}
	return p.c.Apply(ctx, plan, opts...)
}

// Stop stops a program (number or name).
func (p *Pump) Stop(ctx context.Context, ref string, opts ...ApplyOption) (*ApplyResult, error) {
	plan, err := p.StopPlan(ctx, ref)
	if err != nil {
		return nil, err
	}
	return p.c.Apply(ctx, plan, opts...)
}
