package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/emergingrobotics/gopentair"
)

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// table prints rows as an aligned table, CSV or JSON objects keyed by header.
func (a *app) table(header []string, rows [][]string) error {
	switch {
	case a.flags.json:
		out := make([]map[string]string, len(rows))
		for i, r := range rows {
			m := map[string]string{}
			for j, h := range header {
				if j < len(r) {
					m[strings.ToLower(strings.ReplaceAll(h, " ", "_"))] = r[j]
				}
			}
			out[i] = m
		}
		return a.printJSON(out)
	case a.flags.csv:
		w := csv.NewWriter(a.stdout)
		_ = w.Write(header)
		_ = w.WriteAll(rows)
		w.Flush()
		return w.Error()
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	return tw.Flush()
}

// kv prints key/value pairs (human mode).
func (a *app) kv(pairs [][2]string) error {
	tw := tabwriter.NewWriter(a.stdout, 0, 2, 2, ' ', 0)
	for _, p := range pairs {
		if p[1] == "" {
			continue
		}
		fmt.Fprintf(tw, "%s:\t%s\n", p[0], p[1])
	}
	return tw.Flush()
}

func (a *app) tempF(f *int64) string {
	if f == nil {
		return ""
	}
	return a.tempFloat(float64(*f))
}

func (a *app) tempFloat(f float64) string {
	c := gopentair.FahrenheitToCelsius(f)
	switch strings.ToLower(a.flags.units) {
	case "f":
		return fmt.Sprintf("%.1f°F", f)
	case "c":
		return fmt.Sprintf("%.1f°C", c)
	}
	return fmt.Sprintf("%.1f°F (%.1f°C)", f, c)
}

func fmtInt(p *int64, unit string) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf("%d%s", *p, unit)
}

func fmtFloat(p *float64, unit string) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf("%.1f%s", *p, unit)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// confirm asks a yes/no question unless --yes; non-interactive without
// --yes is refused.
func (a *app) confirm(question string) error {
	if a.flags.yes {
		return nil
	}
	if !a.isTTY() {
		return usageErr(question + " (refusing without a terminal; pass --yes)")
	}
	fmt.Fprintf(a.stderr, "%s [y/N] ", question)
	line, _ := bufio.NewReader(a.stdin).ReadString('\n')
	if s := strings.ToLower(strings.TrimSpace(line)); s == "y" || s == "yes" {
		return nil
	}
	return usageErr("aborted")
}

func stdinIsTerminal() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

func readPassword() (string, error) {
	fmt.Fprint(os.Stderr, "Password: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(b), err
}

// reportApply prints the result of a write.
func (a *app) reportApply(res *gopentair.ApplyResult) error {
	if res == nil {
		return nil
	}
	if a.flags.json {
		out := map[string]any{
			"description": res.Plan.Description, "dryRun": res.DryRun,
			"applied": res.Applied, "confirmed": res.Confirmed, "steps": res.Plan.Steps,
		}
		return a.printJSON(out)
	}
	if res.DryRun {
		fmt.Fprintln(a.stdout, "dry run, nothing sent:")
		fmt.Fprintln(a.stdout, res.Plan.String())
		return nil
	}
	msg := "done: " + res.Plan.Description
	if res.Confirmed {
		msg += " (confirmed)"
	} else if !a.flags.noConfirm && res.Plan.Confirm != nil {
		msg += " (not confirmed)"
	} else {
		msg += " (acknowledged)"
	}
	fmt.Fprintln(a.stdout, msg)
	return nil
}

var _ io.Writer = os.Stdout
