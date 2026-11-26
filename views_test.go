package gopentair

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/emergingrobotics/gopentair/internal/testutil"
)

var fmtSscanf = fmt.Sscanf

func stateOf(id string, t DeviceType, fields map[string]string) DeviceState {
	s := DeviceState{DeviceID: id, Type: t, Online: true, Fields: map[string]Field{}}
	for k, v := range fields {
		s.Fields[k] = Field{Code: k, Value: v}
	}
	return s
}

func TestDecodePump(t *testing.T) {
	p := DecodePump(stateOf("p", TypeIntelliFlo3, testutil.PumpFields()))
	if *p.PowerW != 1392 || *p.SpeedPct != 72.5 || *p.FlowGPM != 58.3 || *p.PressurePSI != 28.51 {
		t.Fatalf("telemetry %+v", p)
	}
	if p.ActiveProgram == nil || *p.ActiveProgram != 2 || !p.Running() {
		t.Fatalf("active %v", p.ActiveProgram)
	}
	if len(p.Programs) != 3 || p.Programs[1].Name != "Cleaning" || !p.Programs[1].Running || p.Programs[0].Running {
		t.Fatalf("programs %+v", p.Programs)
	}
	if p.Programs[1].Type != ProgramManual || p.Programs[0].TypeName != "schedule" {
		t.Fatalf("types %+v", p.Programs)
	}
	if _, err := p.FindProgram("4"); !errors.Is(err, ErrInvalidValue) {
		t.Fatal("program 4 does not exist")
	}
	// Missing fields are nil, not zero.
	empty := DecodePump(stateOf("p", TypeIntelliFlo3, map[string]string{}))
	if empty.PowerW != nil || empty.ActiveProgram != nil {
		t.Fatal("expected nils")
	}
	// Stopped pump: s14 set but no power => no active program.
	f := testutil.PumpFields()
	f["s18"] = "0"
	if a := DecodePump(stateOf("p", TypeIntelliFlo3, f)).ActiveProgram; a != nil {
		t.Fatalf("stopped pump has active %d", *a)
	}
}

func TestPumpPlansValidate(t *testing.T) {
	s := stateOf("p", TypeIntelliFlo3, testutil.PumpFields())
	if _, err := PumpStartPlan(s, 0); !errors.Is(err, ErrInvalidValue) {
		t.Fatal("0 accepted")
	}
	if _, err := PumpStartPlan(s, 9); !errors.Is(err, ErrInvalidValue) {
		t.Fatal("nonexistent accepted")
	}
	p, err := PumpStartPlan(s, 1)
	if err != nil || p.Debounce != DebounceProgramStart || len(p.Steps) != 2 {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestDecodePool(t *testing.T) {
	p := DecodePool(stateOf("x", TypeIntelliConnect, testutil.PoolFields()))
	if !p.PumpRunning || !p.ScheduleOn || *p.Relay != RelayOffScheduled {
		t.Fatalf("relay %+v", p)
	}
	if *p.HeaterSetpointF != 84 || *p.Heater != HeaterAuto || *p.HeaterCooldown != 5*time.Minute {
		t.Fatalf("heater %+v", p)
	}
	if *p.WaterTempF != 79 || *p.AirTempF != -3 {
		t.Fatalf("temps")
	}
	if p.Chlor == nil || *p.Chlor.SaltPPM != 3200 || p.Chlor.Model != "IC40" {
		t.Fatalf("chlor %+v", p.Chlor)
	}
	if p.ScheduleStart.String() != "14:00 UTC" {
		t.Fatalf("start %s", p.ScheduleStart)
	}
	noChlor := testutil.PoolFields()
	for _, k := range []string{"icd1", "ics1", "ics2"} {
		delete(noChlor, k)
	}
	if DecodePool(stateOf("x", TypeIntelliConnect, noChlor)).Chlor != nil {
		t.Fatal("chlor should be nil without salt-cell fields")
	}
}

func TestPoolPlansPreserveBits(t *testing.T) {
	cases := []struct {
		ra0       string
		pumpOn    bool
		wantPump  string
		schedOn   bool
		wantSched string
	}{
		{"0", true, "1", true, "2"},
		{"1", false, "0", true, "3"},
		{"2", true, "3", false, "0"},
		{"3", false, "2", false, "1"},
		{"4", true, "3", false, "0"},
	}
	for _, tc := range cases {
		s := stateOf("x", TypeIntelliConnect, map[string]string{"ra0": tc.ra0})
		p, _ := PoolPumpPlan(s, tc.pumpOn)
		if got := p.Steps[0].Fields["ra0"]; got != tc.wantPump {
			t.Errorf("ra0=%s pump(%v): got %s want %s", tc.ra0, tc.pumpOn, got, tc.wantPump)
		}
		q, _ := PoolSchedulePlan(s, tc.schedOn)
		if got := q.Steps[0].Fields["ra0"]; got != tc.wantSched {
			t.Errorf("ra0=%s sched(%v): got %s want %s", tc.ra0, tc.schedOn, got, tc.wantSched)
		}
	}
	if _, err := PoolPumpPlan(stateOf("x", TypeIntelliConnect, nil), true); !errors.Is(err, ErrUnsupportedDevice) {
		t.Fatal("missing ra0 should fail")
	}
}

func TestHeaterAndChlorinePlans(t *testing.T) {
	off := false
	p, err := HeaterPlan("x", &off, nil)
	if err != nil || p.Steps[0].Fields["htd1"] != "0" || p.Steps[0].Fields["htd13"] != "0" {
		t.Fatalf("%+v %v", p, err)
	}
	on := true
	p, _ = HeaterPlan("x", &on, ptr(86.5))
	if p.Steps[0].Fields["htd1"] != "1" || p.Steps[0].Fields["htd2"] != "865" {
		t.Fatalf("%+v", p.Steps)
	}
	if _, err := HeaterPlan("x", nil, ptr(120.0)); !errors.Is(err, ErrInvalidValue) {
		t.Fatal("120F accepted")
	}
	if _, err := HeaterPlan("x", nil, nil); !errors.Is(err, ErrInvalidValue) {
		t.Fatal("empty accepted")
	}
	if _, err := ChlorinePlan("x", 101); !errors.Is(err, ErrInvalidValue) {
		t.Fatal("101% accepted")
	}
	c, _ := ChlorinePlan("x", 35)
	if c.Steps[0].Fields["icd1"] != "35" {
		t.Fatal(c.Steps)
	}
}

func TestLight(t *testing.T) {
	l := DecodeLight(stateOf("l", TypeColorSync, testutil.LightFields()))
	if !*l.On || *l.Show != ShowCaribbean || l.ShowName != "caribbean" {
		t.Fatalf("%+v", l)
	}
	for in, want := range map[string]LightShow{"Caribbean": ShowCaribbean, "S.Am": ShowSAm, "royal": ShowRoyal, "4": ShowBlue, "hold": CmdHold} {
		got, err := ParseLightShow(in)
		if err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := ParseLightShow("disco"); !errors.Is(err, ErrInvalidValue) {
		t.Fatal("disco accepted")
	}
	if len(LightShows()) != 12 {
		t.Fatalf("shows %v", LightShows())
	}
	p, _ := LightShowPlan("l", CmdRecall)
	if p.Confirm != nil {
		t.Fatal("commands have no confirm predicate")
	}
}

func TestClock(t *testing.T) {
	la, _ := time.LoadLocation("America/Los_Angeles")
	summer := time.Date(2026, 7, 1, 12, 0, 0, 0, la)
	c, err := ClockFromLocal(7, 0, la, summer)
	if err != nil || c != ClockUTC(14*3600) {
		t.Fatalf("got %v %v", c, err)
	}
	if lt := c.Local(la, summer); lt.Hour() != 7 {
		t.Fatalf("round trip %v", lt)
	}
	winter := time.Date(2026, 1, 15, 12, 0, 0, 0, la)
	if c, _ := ClockFromLocal(7, 0, la, winter); c != ClockUTC(15*3600) {
		t.Fatalf("winter %v", c)
	}
	if _, err := ClockFromLocal(25, 0, la, winter); err == nil {
		t.Fatal("25:00 accepted")
	}
}

func TestMerge(t *testing.T) {
	s := stateOf("x", TypeColorSync, map[string]string{"d1": "1"})
	s.Fields["d1"] = Field{Code: "d1", Name: "Show", Value: "1"}
	ch := s.Merge(map[string]Field{"d1": {Code: "d1", Value: "1"}, "d13": {Code: "d13", Value: "0"}})
	if len(ch) != 1 || ch[0] != "d13" || s.Fields["d1"].Name != "Show" {
		t.Fatalf("changed %v fields %+v", ch, s.Fields)
	}
}
