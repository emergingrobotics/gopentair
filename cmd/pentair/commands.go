package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/emergingrobotics/gopentair"
)

func (a *app) loginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Log in (SRP) and cache the session",
		Long:  "Performs a fresh Cognito SRP login and caches the session (not the password) so later\ncommands work without a password until the refresh token expires.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			if cfg.Username == "" {
				return usageErr("no username: set PENTAIR_USERNAME or username in the config file")
			}
			if cfg.Password == "" {
				if !a.isTTY() {
					return usageErr("no password: set PENTAIR_PASSWORD or run interactively")
				}
				pw, err := a.readPass()
				if err != nil {
					return err
				}
				cfg.Password = pw
			}
			c, err := a.getClient()
			if err != nil {
				return err
			}
			if err := c.ForceLogin(cmd.Context()); err != nil {
				return err
			}
			where := cfg.TokenFile
			switch {
			case strings.EqualFold(where, "none"):
				where = "not cached (token file disabled)"
			case where == "":
				where = "cached in " + gopentair.DefaultTokenFile()
			default:
				where = "cached in " + where
			}
			fmt.Fprintf(a.stdout, "logged in as %s; session %s\n", cfg.Username, where)
			return nil
		},
	}
}

func (a *app) logoutCmd() *cobra.Command {
	var everywhere bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Forget the cached session",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if everywhere {
				if err := a.confirm("Sign out ALL sessions for this account, including the Pentair Home app?"); err != nil {
					return err
				}
			}
			c, err := a.getClient()
			if err != nil {
				return err
			}
			if err := c.Logout(cmd.Context(), everywhere); err != nil {
				return err
			}
			if everywhere {
				fmt.Fprintln(a.stdout, "signed out everywhere; local session removed")
			} else {
				fmt.Fprintln(a.stdout, "local session removed")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&everywhere, "everywhere", false, "also revoke all sessions (Cognito GlobalSignOut), including the phone app")
	return cmd
}

func (a *app) whoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the account profile",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.getClient()
			if err != nil {
				return err
			}
			p, err := c.Profile(cmd.Context())
			if err != nil {
				return err
			}
			if a.flags.json {
				return a.printJSON(p.Raw)
			}
			return a.kv([][2]string{{"Email", p.Email}, {"Name", strings.TrimSpace(p.FirstName + " " + p.LastName)}})
		},
	}
}

func (a *app) devicesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "devices", Short: "List and inspect devices"}
	list := &cobra.Command{
		Use:   "list",
		Short: "List devices on the account",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.getClient()
			if err != nil {
				return err
			}
			ds, err := c.Devices(cmd.Context())
			if err != nil {
				return err
			}
			ids := make([]string, len(ds))
			for i, d := range ds {
				ids[i] = d.ID
			}
			online := map[string]string{}
			if states, err := c.States(cmd.Context(), ids...); err == nil {
				for _, s := range states {
					online[s.DeviceID] = yesNo(s.Online)
				}
			}
			rows := make([][]string, len(ds))
			for i, d := range ds {
				rows[i] = []string{d.ID, string(d.Type), d.Type.Description(), d.Nickname, online[d.ID], d.Status}
			}
			return a.table([]string{"ID", "Type", "Kind", "Nickname", "Online", "Status"}, rows)
		},
	}
	info := &cobra.Command{
		Use:   "info",
		Short: "Show the raw listing record of a device",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, d, err := a.resolve(cmd.Context())
			_ = c
			if err != nil {
				return err
			}
			var raw any
			if len(d.Raw) > 0 {
				_ = json.Unmarshal(d.Raw, &raw)
			}
			return a.printJSON(map[string]any{"device": d, "listing": raw})
		},
	}
	cmd.AddCommand(list, info)
	return cmd
}

// resolve returns the client and the target device.
func (a *app) resolve(ctx context.Context, types ...gopentair.DeviceType) (gopentair.Client, *gopentair.Device, error) {
	c, err := a.getClient()
	if err != nil {
		return nil, nil, err
	}
	d, err := c.ResolveDevice(ctx, a.flags.device, types...)
	if err != nil {
		if errors.Is(err, gopentair.ErrDeviceNotFound) && len(types) > 0 && a.flags.device == "" {
			names := make([]string, len(types))
			for i, t := range types {
				names[i] = t.Description()
			}
			return nil, nil, fmt.Errorf("%w: no %s on this account", err, strings.Join(names, "/"))
		}
		return nil, nil, err
	}
	if len(types) > 0 && !slices.Contains(types, d.Type) {
		return nil, nil, fmt.Errorf("%w: %s is a %s (%s), this command needs %s",
			gopentair.ErrUnsupportedDevice, d.Name(), d.Type.Description(), d.Type, types[0].Description())
	}
	return c, d, nil
}

func (a *app) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show decoded device status (raw fields for unknown types)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, d, err := a.resolve(cmd.Context())
			if err != nil {
				return err
			}
			st, err := c.State(cmd.Context(), d.ID)
			if err != nil {
				return err
			}
			return a.printStatus(d, st)
		},
	}
}

func (a *app) printStatus(d *gopentair.Device, st *gopentair.DeviceState) error {
	var decoded any
	switch st.Type {
	case gopentair.TypeIntelliFlo3:
		decoded = gopentair.DecodePump(*st)
	case gopentair.TypeIntelliConnect:
		decoded = gopentair.DecodePool(*st)
	case gopentair.TypeColorSync:
		decoded = gopentair.DecodeLight(*st)
	}
	if a.flags.json {
		return a.printJSON(map[string]any{"device": d, "online": st.Online, "alarm": st.Alarm,
			"fwVersion": st.FWVersion, "decoded": decoded, "fields": st.Fields})
	}
	fmt.Fprintf(a.stdout, "%s — %s (%s)\n", d.Name(), d.Type.Description(), d.Type)
	switch v := decoded.(type) {
	case gopentair.PumpStatus:
		return a.printPump(v)
	case gopentair.PoolStatus:
		return a.printPool(v)
	case gopentair.LightStatus:
		return a.printLight(v)
	}
	fmt.Fprintf(a.stdout, "online: %s  (no typed view for this device type; raw fields follow)\n", yesNo(st.Online))
	return a.printFields(st, nil)
}

func (a *app) printPump(p gopentair.PumpStatus) error {
	active := "none"
	if p.ActiveProgram != nil {
		active = strconv.Itoa(*p.ActiveProgram)
		for _, pr := range p.Programs {
			if pr.Number == *p.ActiveProgram && pr.Name != "" {
				active += " (" + pr.Name + ")"
			}
		}
	}
	alarm := ""
	if p.AlarmBits != nil {
		alarm = "none"
		if *p.AlarmBits != 0 {
			alarm = fmt.Sprintf("0x%x", *p.AlarmBits)
		}
	}
	if err := a.kv([][2]string{
		{"Online", yesNo(p.Online)}, {"Running", yesNo(p.Running())}, {"Active program", active},
		{"Power", fmtInt(p.PowerW, " W")}, {"Speed", fmtFloat(p.SpeedPct, " %")},
		{"Flow", fmtFloat(p.FlowGPM, " GPM")}, {"Pressure", fmtFloat(p.PressurePSI, " psi")},
		{"Alarm", alarm}, {"Wi-Fi RSSI", fmtInt(p.RSSI, " dBm")},
	}); err != nil {
		return err
	}
	if len(p.Programs) > 0 {
		fmt.Fprintln(a.stdout)
		return a.printPrograms(p.Programs)
	}
	return nil
}

func (a *app) printPrograms(ps []gopentair.Program) error {
	rows := make([][]string, len(ps))
	for i, pr := range ps {
		val := ""
		if pr.Value != nil {
			val = strconv.FormatInt(*pr.Value, 10)
		}
		rows[i] = []string{strconv.Itoa(pr.Number), pr.Name, pr.TypeName, val, pr.EnableValue, yesNo(pr.Running)}
	}
	return a.table([]string{"#", "Name", "Type", "Value", "Enable", "Running"}, rows)
}

func (a *app) printPool(p gopentair.PoolStatus) error {
	loc := time.Local
	sched := "disabled"
	if p.ScheduleOn {
		sched = "enabled"
	}
	if p.ScheduleStart != nil && p.ScheduleStop != nil {
		now := time.Now()
		sched += fmt.Sprintf(", %s–%s local", p.ScheduleStart.Local(loc, now).Format("15:04"), p.ScheduleStop.Local(loc, now).Format("15:04"))
	}
	heater := p.HeaterName
	if p.HeaterSetpointF != nil {
		heater += ", setpoint " + a.tempFloat(*p.HeaterSetpointF)
	}
	if p.HeaterCooldown != nil && *p.HeaterCooldown > 0 {
		heater += ", cooldown " + p.HeaterCooldown.String()
	}
	pairs := [][2]string{
		{"Online", yesNo(p.Online)}, {"Alarm", yesNo(p.Alarm)},
		{"Filter pump", map[bool]string{true: "running", false: "stopped"}[p.PumpRunning] + " " + fmtInt(p.PumpPowerW, " W")},
		{"Pump mode", p.RelayName}, {"Schedule", sched},
		{"Water", a.tempF(p.WaterTempF)}, {"Air", a.tempF(p.AirTempF)}, {"Heater", heater},
	}
	if p.VSPowerW != nil {
		pairs = append(pairs, [2]string{"VS pump", fmtInt(p.VSPowerW, " W") + " speed " + fmtInt(p.VSSpeed, "") + " flow " + fmtInt(p.VSFlow, "")})
	}
	if c := p.Chlor; c != nil {
		chl := fmtInt(c.ActualPct, "%") + " (set " + fmtInt(c.SetPct, "%") + ")"
		if c.SaltPPM != nil {
			chl += ", salt " + fmtInt(c.SaltPPM, " ppm")
		}
		if c.BoostRemaining != nil && *c.BoostRemaining > 0 {
			chl += ", boost " + c.BoostRemaining.String()
		}
		if c.ErrorCode != "" && c.ErrorCode != "0" {
			chl += ", error " + c.ErrorCode
		}
		pairs = append(pairs, [2]string{"Chlorinator", chl}, [2]string{"Salt cell", strings.TrimSpace(c.Model + " " + c.Firmware + " " + fmtInt(c.Hours, " h"))})
	}
	return a.kv(pairs)
}

func (a *app) printLight(l gopentair.LightStatus) error {
	on := ""
	if l.On != nil {
		on = map[bool]string{true: "on", false: "off"}[*l.On]
	}
	return a.kv([][2]string{{"Online", yesNo(l.Online)}, {"Power", on}, {"Show", l.ShowName}})
}

// naturalLess sorts "s2" before "s10" and "zp2e1" before "zp10e1".
func naturalLess(x, y string) bool {
	for x != "" && y != "" {
		if unicode.IsDigit(rune(x[0])) && unicode.IsDigit(rune(y[0])) {
			i, j := 0, 0
			for i < len(x) && unicode.IsDigit(rune(x[i])) {
				i++
			}
			for j < len(y) && unicode.IsDigit(rune(y[j])) {
				j++
			}
			nx, _ := strconv.Atoi(x[:i])
			ny, _ := strconv.Atoi(y[:j])
			if nx != ny {
				return nx < ny
			}
			x, y = x[i:], y[j:]
			continue
		}
		if x[0] != y[0] {
			return x[0] < y[0]
		}
		x, y = x[1:], y[1:]
	}
	return len(x) < len(y)
}

func sortedCodes(m map[string]gopentair.Field) []string {
	ks := slices.Collect(maps.Keys(m))
	slices.SortFunc(ks, func(a, b string) int {
		switch {
		case naturalLess(a, b):
			return -1
		case naturalLess(b, a):
			return 1
		}
		return 0
	})
	return ks
}

func (a *app) printFields(st *gopentair.DeviceState, prefixes []string) error {
	var rows [][]string
	for _, code := range sortedCodes(st.Fields) {
		if len(prefixes) > 0 && !slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(code, p) }) {
			continue
		}
		f := st.Fields[code]
		rows = append(rows, []string{code, f.Name, f.Value, f.Min, f.Max})
	}
	return a.table([]string{"Code", "Name", "Value", "Min", "Max"}, rows)
}

func (a *app) fieldsCmd() *cobra.Command {
	var filter []string
	cmd := &cobra.Command{
		Use:   "fields",
		Short: "Show every raw field of a device (the exploration toolkit)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, d, err := a.resolve(cmd.Context())
			if err != nil {
				return err
			}
			st, err := c.State(cmd.Context(), d.ID)
			if err != nil {
				return err
			}
			return a.printFields(st, filter)
		},
	}
	cmd.Flags().StringSliceVar(&filter, "filter", nil, "only codes with these prefixes (comma-separated)")
	set := &cobra.Command{
		Use:   "set CODE=VALUE [CODE=VALUE...]",
		Short: "Write raw fields (asks for confirmation)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fields := map[string]string{}
			for _, arg := range args {
				k, v, ok := strings.Cut(arg, "=")
				if !ok || k == "" {
					return usageErr("expected CODE=VALUE, got " + strconv.Quote(arg))
				}
				fields[k] = v
			}
			c, d, err := a.resolve(cmd.Context())
			if err != nil {
				return err
			}
			plan := gopentair.FieldPlan(d.ID, fields)
			plan.Description = "raw write to " + d.Name()
			plan.Confirm = func(s gopentair.DeviceState) bool {
				for k, v := range fields {
					if got, _ := s.Str(k); got != v {
						return false
					}
				}
				return true
			}
			plan.ConfirmTimeout = 30 * time.Second
			if !a.flags.dryRun {
				fmt.Fprintln(a.stderr, plan.String())
				if err := a.confirm("Send this raw write?"); err != nil {
					return err
				}
			}
			res, err := c.Apply(cmd.Context(), plan, a.applyOpts()...)
			if err != nil {
				return err
			}
			return a.reportApply(res)
		},
	}
	cmd.AddCommand(set)
	return cmd
}

func (a *app) diffCmd() *cobra.Command {
	var wait time.Duration
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Snapshot, wait (Enter or --wait), snapshot again, print changed fields",
		Long:  "Use this to map unknown fields: run it, change one setting in the Pentair Home app,\nthen press Enter. The changed field codes are printed.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, d, err := a.resolve(ctx)
			if err != nil {
				return err
			}
			before, err := c.State(ctx, d.ID)
			if err != nil {
				return err
			}
			if wait > 0 {
				fmt.Fprintf(a.stderr, "snapshot taken; waiting %s...\n", wait)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(wait):
				}
			} else {
				fmt.Fprint(a.stderr, "snapshot taken; make a change, then press Enter... ")
				_, _ = bufio.NewReader(a.stdin).ReadString('\n')
			}
			after, err := c.State(ctx, d.ID)
			if err != nil {
				return err
			}
			var rows [][]string
			all := maps.Clone(after.Fields)
			for k, v := range before.Fields {
				if _, ok := all[k]; !ok {
					all[k] = v
				}
			}
			for _, code := range sortedCodes(all) {
				b, bok := before.Fields[code]
				n, nok := after.Fields[code]
				if bok && nok && b.Value == n.Value {
					continue
				}
				name := n.Name
				if name == "" {
					name = b.Name
				}
				rows = append(rows, []string{code, name, missing(b.Value, bok), missing(n.Value, nok)})
			}
			if len(rows) == 0 && !a.flags.json && !a.flags.csv {
				fmt.Fprintln(a.stdout, "no fields changed")
				return nil
			}
			return a.table([]string{"Code", "Name", "Before", "After"}, rows)
		},
	}
	cmd.Flags().DurationVar(&wait, "wait", 0, "wait this long instead of for Enter")
	return cmd
}

func missing(v string, ok bool) string {
	if !ok {
		return "(absent)"
	}
	return v
}

func (a *app) watchCmd() *cobra.Command {
	var (
		all     bool
		screens []string
		fields  []string
		ndjson  bool
		poll    time.Duration
	)
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Stream live field changes (WebSocket, with REST poll fallback)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := a.getClient()
			if err != nil {
				return err
			}
			names := map[string]string{}
			var ids []string
			if all {
				ds, err := c.Devices(ctx)
				if err != nil {
					return err
				}
				for _, d := range ds {
					ids = append(ids, d.ID)
					names[d.ID] = d.Name()
				}
			} else {
				_, d, err := a.resolve(ctx)
				if err != nil {
					return err
				}
				ids = []string{d.ID}
				names[d.ID] = d.Name()
			}
			opts := []gopentair.WatchOption{}
			if len(screens) > 0 {
				opts = append(opts, gopentair.WithScreens(screens...))
			}
			if len(fields) > 0 {
				opts = append(opts, gopentair.WithFieldFilter(fields...))
			}
			if cmd.Flags().Changed("poll") {
				opts = append(opts, gopentair.WithPollFallback(poll))
			}
			events, err := c.Watch(ctx, ids, opts...)
			if err != nil {
				return err
			}
			last := map[string]gopentair.DeviceState{}
			enc := json.NewEncoder(a.stdout)
			for ev := range events {
				prev, seen := last[ev.DeviceID]
				last[ev.DeviceID] = ev.State
				if ndjson || a.flags.json {
					changed := map[string]string{}
					for _, code := range ev.Changed {
						if f, ok := ev.State.Fields[code]; ok {
							changed[code] = f.Value
						}
					}
					if err := enc.Encode(map[string]any{"at": ev.At, "deviceId": ev.DeviceID, "source": ev.Source, "changed": changed}); err != nil {
						return err
					}
					continue
				}
				ts := ev.At.Local().Format("15:04:05")
				if ev.Source == gopentair.SourceSnapshot && !seen {
					fmt.Fprintf(a.stdout, "%s [%s] %s: %d fields\n", ts, ev.Source, names[ev.DeviceID], len(ev.State.Fields))
					continue
				}
				for _, code := range ev.Changed {
					f, ok := ev.State.Fields[code]
					if !ok {
						continue
					}
					old := prev.Fields[code].Value
					label := code
					if f.Name != "" {
						label += " (" + f.Name + ")"
					}
					fmt.Fprintf(a.stdout, "%s [%s] %s %s: %s → %s\n", ts, ev.Source, names[ev.DeviceID], label, old, f.Value)
				}
			}
			if ctx.Err() != nil {
				return nil
			}
			return errors.New("watch ended")
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "watch every device on the account")
	cmd.Flags().StringSliceVar(&screens, "screen", nil, "app screens to subscribe to (default poolScreen,product)")
	cmd.Flags().StringSliceVar(&fields, "fields", nil, "only report changes to these codes")
	cmd.Flags().BoolVar(&ndjson, "ndjson", false, "one JSON object per event")
	cmd.Flags().DurationVar(&poll, "poll", time.Minute, "REST fallback poll interval (0 disables, min 30s)")
	return cmd
}
