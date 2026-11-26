// Command prometheus exposes Pentair telemetry in the Prometheus text
// format on :9817/metrics, kept current by a Watch stream (no polling
// per scrape).
//
//	go run ./examples/prometheus -listen :9817
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"

	"github.com/emergingrobotics/gopentair"
)

type store struct {
	mu     sync.Mutex
	names  map[string]string
	states map[string]gopentair.DeviceState
}

func (s *store) metrics() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	type sample struct {
		name, help string
		val        float64
		labels     string
	}
	var out []sample
	add := func(metric, help, id string, v any) {
		var f float64
		switch t := v.(type) {
		case *int64:
			if t == nil {
				return
			}
			f = float64(*t)
		case *float64:
			if t == nil {
				return
			}
			f = *t
		case bool:
			if t {
				f = 1
			}
		}
		out = append(out, sample{metric, help, f, fmt.Sprintf(`device=%q,name=%q`, id, s.names[id])})
	}
	for id, st := range s.states {
		add("pentair_online", "Device online (1/0).", id, st.Online)
		switch st.Type {
		case gopentair.TypeIntelliFlo3:
			p := gopentair.DecodePump(st)
			add("pentair_pump_power_watts", "Pump power.", id, p.PowerW)
			add("pentair_pump_speed_percent", "Pump motor speed.", id, p.SpeedPct)
			add("pentair_pump_flow_gpm", "Estimated flow.", id, p.FlowGPM)
			add("pentair_pump_pressure_psi", "Pressure.", id, p.PressurePSI)
		case gopentair.TypeIntelliConnect:
			p := gopentair.DecodePool(st)
			add("pentair_pool_pump_power_watts", "Filter pump relay power.", id, p.PumpPowerW)
			add("pentair_pool_water_temp_f", "Water temperature.", id, p.WaterTempF)
			add("pentair_pool_air_temp_f", "Air temperature.", id, p.AirTempF)
			add("pentair_pool_heater_setpoint_f", "Heater setpoint.", id, p.HeaterSetpointF)
			if p.Chlor != nil {
				add("pentair_chlor_salt_ppm", "Salt level.", id, p.Chlor.SaltPPM)
				add("pentair_chlor_output_percent", "Actual chlorinator output.", id, p.Chlor.ActualPct)
			}
		case gopentair.TypeColorSync:
			l := gopentair.DecodeLight(st)
			add("pentair_light_on", "Lights on (1/0).", id, l.On != nil && *l.On)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].name != out[j].name {
			return out[i].name < out[j].name
		}
		return out[i].labels < out[j].labels
	})
	last := ""
	for _, s := range out {
		if s.name != last {
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n", s.name, s.help, s.name)
			last = s.name
		}
		fmt.Fprintf(&b, "%s{%s} %g\n", s.name, s.labels, s.val)
	}
	return b.String()
}

func main() {
	listen := flag.String("listen", ":9817", "listen address")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	c, err := gopentair.NewClient(gopentair.WithEnvConfig(), gopentair.WithDefaultTokenFile())
	if err != nil {
		log.Fatal(err)
	}
	devs, err := c.Devices(ctx)
	if err != nil {
		log.Fatal(err)
	}
	s := &store{names: map[string]string{}, states: map[string]gopentair.DeviceState{}}
	var ids []string
	for _, d := range devs {
		ids = append(ids, d.ID)
		s.names[d.ID] = d.Name()
	}
	events, err := c.Watch(ctx, ids)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		for ev := range events {
			s.mu.Lock()
			s.states[ev.DeviceID] = ev.State
			s.mu.Unlock()
		}
	}()
	http.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(s.metrics()))
	})
	srv := &http.Server{Addr: *listen}
	go func() { <-ctx.Done(); _ = srv.Close() }()
	log.Printf("serving metrics on %s/metrics", *listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
