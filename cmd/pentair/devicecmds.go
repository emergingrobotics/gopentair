package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/emergingrobotics/gopentair"
)

func onOff(arg string) (bool, error) {
	switch strings.ToLower(arg) {
	case "on", "1", "true", "start":
		return true, nil
	case "off", "0", "false", "stop":
		return false, nil
	}
	return false, usageErr("expected on or off, got " + strconv.Quote(arg))
}

// --- IntelliFlo 3 ------------------------------------------------------------

func (a *app) pumpCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "pump", Short: "IntelliFlo 3 pump (IF31)"}
	status := &cobra.Command{
		Use: "status", Short: "Pump telemetry and programs", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, d, err := a.resolve(cmd.Context(), gopentair.TypeIntelliFlo3)
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
	programs := &cobra.Command{
		Use: "programs", Short: "List program slots", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, d, err := a.resolve(cmd.Context(), gopentair.TypeIntelliFlo3)
			if err != nil {
				return err
			}
			ps, _, err := gopentair.NewPump(c, d.ID).Status(cmd.Context())
			if err != nil {
				return err
			}
			if a.flags.json {
				return a.printJSON(ps.Programs)
			}
			return a.printPrograms(ps.Programs)
		},
	}
	startStop := func(start bool) *cobra.Command {
		verb := map[bool]string{true: "start", false: "stop"}[start]
		return &cobra.Command{
			Use: verb + " <program number|name>", Short: strings.ToUpper(verb[:1]) + verb[1:] + " a pump program",
			Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, d, err := a.resolve(cmd.Context(), gopentair.TypeIntelliFlo3)
				if err != nil {
					return err
				}
				p := gopentair.NewPump(c, d.ID)
				var plan gopentair.WritePlan
				if start {
					plan, err = p.StartPlan(cmd.Context(), args[0])
				} else {
					plan, err = p.StopPlan(cmd.Context(), args[0])
				}
				if err != nil {
					return err
				}
				res, err := c.Apply(cmd.Context(), plan, a.applyOpts()...)
				if err != nil {
					return err
				}
				return a.reportApply(res)
			},
		}
	}
	cmd.AddCommand(status, programs, startStop(true), startStop(false))
	return cmd
}

// --- IntelliConnect ------------------------------------------------------------

func (a *app) poolCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "pool", Short: "IntelliConnect controller (PIF0): pump, schedule, heater, chlorinator"}
	resolve := func(cmd *cobra.Command) (gopentair.Client, *gopentair.IntelliConnect, error) {
		c, d, err := a.resolve(cmd.Context(), gopentair.TypeIntelliConnect)
		if err != nil {
			return nil, nil, err
		}
		return c, gopentair.NewIntelliConnect(c, d.ID), nil
	}
	apply := func(cmd *cobra.Command, c gopentair.Client, plan gopentair.WritePlan, err error) error {
		if err != nil {
			return err
		}
		res, err := c.Apply(cmd.Context(), plan, a.applyOpts()...)
		if err != nil {
			return err
		}
		return a.reportApply(res)
	}
	status := &cobra.Command{
		Use: "status", Short: "Pump, temperatures, heater, salt cell", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, d, err := a.resolve(cmd.Context(), gopentair.TypeIntelliConnect)
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
	pump := &cobra.Command{
		Use: "pump on|off", Short: "Manual filter pump override (keeps the schedule setting)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			on, err := onOff(args[0])
			if err != nil {
				return err
			}
			c, ic, err := resolve(cmd)
			if err != nil {
				return err
			}
			plan, err := ic.PumpPlan(cmd.Context(), on)
			return apply(cmd, c, plan, err)
		},
	}
	var enable, disable bool
	var start, stop string
	schedule := &cobra.Command{
		Use:   "schedule",
		Short: "Show or change the daily schedule (times are local; stored as UTC)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, ic, err := resolve(cmd)
			if err != nil {
				return err
			}
			if enable && disable {
				return usageErr("--enable and --disable are mutually exclusive")
			}
			if (start == "") != (stop == "") {
				return usageErr("--start and --stop must be given together")
			}
			if !enable && !disable && start == "" {
				ps, _, err := ic.Status(cmd.Context())
				if err != nil {
					return err
				}
				if a.flags.json {
					return a.printJSON(map[string]any{"enabled": ps.ScheduleOn, "startUTC": ps.ScheduleStart, "stopUTC": ps.ScheduleStop})
				}
				pairs := [][2]string{{"Enabled", yesNo(ps.ScheduleOn)}}
				now := time.Now()
				if ps.ScheduleStart != nil {
					pairs = append(pairs, [2]string{"Start", ps.ScheduleStart.Local(time.Local, now).Format("15:04") + " local (" + ps.ScheduleStart.String() + ")"})
				}
				if ps.ScheduleStop != nil {
					pairs = append(pairs, [2]string{"Stop", ps.ScheduleStop.Local(time.Local, now).Format("15:04") + " local (" + ps.ScheduleStop.String() + ")"})
				}
				return a.kv(pairs)
			}
			if start != "" {
				sh, sm, err := gopentair.ParseHHMM(start)
				if err != nil {
					return err
				}
				eh, em, err := gopentair.ParseHHMM(stop)
				if err != nil {
					return err
				}
				now := time.Now()
				s, _ := gopentair.ClockFromLocal(sh, sm, time.Local, now)
				e, _ := gopentair.ClockFromLocal(eh, em, time.Local, now)
				if err := apply(cmd, c, gopentair.PoolScheduleTimesPlan(ic.ID, s, e), nil); err != nil {
					return err
				}
			}
			if enable || disable {
				plan, err := ic.SchedulePlan(cmd.Context(), enable)
				return apply(cmd, c, plan, err)
			}
			return nil
		},
	}
	schedule.Flags().BoolVar(&enable, "enable", false, "enable the daily schedule")
	schedule.Flags().BoolVar(&disable, "disable", false, "disable the daily schedule")
	schedule.Flags().StringVar(&start, "start", "", "schedule start HH:MM (local)")
	schedule.Flags().StringVar(&stop, "stop", "", "schedule stop HH:MM (local)")

	var setpoint float64
	heater := &cobra.Command{
		Use: "heater [off|heat]", Short: "Heater mode and/or setpoint (°F)", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ic, err := resolve(cmd)
			if err != nil {
				return err
			}
			var on *bool
			if len(args) == 1 {
				switch strings.ToLower(args[0]) {
				case "heat", "on", "auto":
					v := true
					on = &v
				case "off":
					v := false
					on = &v
				default:
					return usageErr("expected off or heat")
				}
			}
			var sp *float64
			if cmd.Flags().Changed("setpoint") {
				sp = &setpoint
			}
			plan, err := gopentair.HeaterPlan(ic.ID, on, sp)
			return apply(cmd, c, plan, err)
		},
	}
	heater.Flags().Float64Var(&setpoint, "setpoint", 0, "target water temperature in °F")

	chlorine := &cobra.Command{
		Use: "chlorine PERCENT", Short: "IntelliChlor output (0-100%)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pct, err := strconv.Atoi(strings.TrimSuffix(args[0], "%"))
			if err != nil {
				return usageErr("expected a percentage, got " + strconv.Quote(args[0]))
			}
			c, ic, err := resolve(cmd)
			if err != nil {
				return err
			}
			plan, err := gopentair.ChlorinePlan(ic.ID, pct)
			return apply(cmd, c, plan, err)
		},
	}
	cmd.AddCommand(status, pump, schedule, heater, chlorine)
	return cmd
}

// --- Color Sync -------------------------------------------------------------------

func (a *app) lightCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "light", Short: "Color Sync light controller (PLC1)"}
	run := func(cmd *cobra.Command, plan func(id string) (gopentair.WritePlan, error)) error {
		c, d, err := a.resolve(cmd.Context(), gopentair.TypeColorSync)
		if err != nil {
			return err
		}
		if _, _, err := gopentair.NewLight(c, d.ID).Status(cmd.Context()); err != nil {
			return err
		}
		p, err := plan(d.ID)
		if err != nil {
			return err
		}
		res, err := c.Apply(cmd.Context(), p, a.applyOpts()...)
		if err != nil {
			return err
		}
		return a.reportApply(res)
	}
	status := &cobra.Command{
		Use: "status", Short: "Power and current show", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, d, err := a.resolve(cmd.Context(), gopentair.TypeColorSync)
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
	power := func(on bool) *cobra.Command {
		w := map[bool]string{true: "on", false: "off"}[on]
		return &cobra.Command{
			Use: w, Short: "Turn the lights " + w, Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return run(cmd, func(id string) (gopentair.WritePlan, error) { return gopentair.LightPowerPlan(id, on), nil })
			},
		}
	}
	show := &cobra.Command{
		Use: "show NAME", Short: "Select a color or show (see: pentair light shows)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := gopentair.ParseLightShow(args[0])
			if err != nil {
				return err
			}
			return run(cmd, func(id string) (gopentair.WritePlan, error) { return gopentair.LightShowPlan(id, s) })
		},
	}
	command := func(name string, s gopentair.LightShow) *cobra.Command {
		return &cobra.Command{
			Use: name, Short: "Send the controller's " + name + " command", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return run(cmd, func(id string) (gopentair.WritePlan, error) { return gopentair.LightShowPlan(id, s) })
			},
		}
	}
	shows := &cobra.Command{
		Use: "shows", Short: "List show names", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var rows [][]string
			for _, s := range gopentair.LightShows() {
				rows = append(rows, []string{strconv.Itoa(int(s)), s.String()})
			}
			return a.table([]string{"Value", "Name"}, rows)
		},
	}
	cmd.AddCommand(status, power(true), power(false), show, command("hold", gopentair.CmdHold), command("recall", gopentair.CmdRecall), shows)
	return cmd
}

var _ = fmt.Sprint
