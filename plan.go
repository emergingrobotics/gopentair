package gopentair

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// DebounceKey names a client-side rate limit bucket.
type DebounceKey string

// Debounce keys.
const (
	// DebounceProgramStart limits pump program starts per device.
	DebounceProgramStart DebounceKey = "program-start"
)

// DefaultDebounce is applied unless overridden with WithDebounce.
var DefaultDebounce = map[DebounceKey]time.Duration{
	DebounceProgramStart: 30 * time.Second,
}

// WriteStep is one PUT of one or more fields.
type WriteStep struct {
	Fields map[string]string
	Note   string        // human description
	After  time.Duration // delay before this step
}

// WritePlan is an ordered list of field writes for one device, plus an
// optional predicate that tells whether the device reflects the change.
type WritePlan struct {
	DeviceID       string
	Description    string
	Steps          []WriteStep
	Debounce       DebounceKey
	Confirm        func(DeviceState) bool `json:"-"`
	ConfirmTimeout time.Duration
}

// String renders the plan for --dry-run output.
func (p WritePlan) String() string {
	var b strings.Builder
	if p.Description != "" {
		b.WriteString(p.Description)
		b.WriteByte('\n')
	}
	for i, s := range p.Steps {
		keys := make([]string, 0, len(s.Fields))
		for k := range s.Fields {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		kv := make([]string, len(keys))
		for j, k := range keys {
			kv[j] = k + "=" + s.Fields[k]
		}
		fmt.Fprintf(&b, "  %d. PUT %s", i+1, strings.Join(kv, " "))
		if s.Note != "" {
			fmt.Fprintf(&b, "   # %s", s.Note)
		}
		if s.After > 0 {
			fmt.Fprintf(&b, " (after %s)", s.After)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// FieldPlan builds a one-step plan from raw fields.
func FieldPlan(deviceID string, fields map[string]string) WritePlan {
	return WritePlan{DeviceID: deviceID, Description: "set fields", Steps: []WriteStep{{Fields: fields}}}
}

// ApplyOption configures Apply.
type ApplyOption func(*applyConfig)

type applyConfig struct {
	dryRun     bool
	useDefault bool
	pred       func(DeviceState) bool
	timeout    time.Duration
	poll       time.Duration
}

// WithDryRun returns the plan without sending anything.
func WithDryRun() ApplyOption { return func(c *applyConfig) { c.dryRun = true } }

// WithConfirmDefault waits for the plan's own Confirm predicate, if any.
func WithConfirmDefault() ApplyOption { return func(c *applyConfig) { c.useDefault = true } }

// WithConfirm waits until pred holds (polling) or timeout elapses.
func WithConfirm(pred func(DeviceState) bool, timeout time.Duration) ApplyOption {
	return func(c *applyConfig) { c.pred, c.timeout = pred, timeout }
}

// WithConfirmPoll sets the confirm polling interval (default 5s).
func WithConfirmPoll(d time.Duration) ApplyOption { return func(c *applyConfig) { c.poll = d } }

// ApplyResult describes what Apply did.
type ApplyResult struct {
	Plan      WritePlan
	DryRun    bool
	Applied   int
	Confirmed bool
	State     *DeviceState // last observed state when confirming
}
