package gopentair

import (
	"fmt"
	"math"
	"time"
)

// FahrenheitToCelsius converts °F to °C.
func FahrenheitToCelsius(f float64) float64 { return (f - 32) * 5 / 9 }

// CelsiusToFahrenheit converts °C to °F.
func CelsiusToFahrenheit(c float64) float64 { return c*9/5 + 32 }

// ClockUTC is a time of day stored by the controller as seconds after
// midnight UTC (IntelliConnect schedules).
type ClockUTC int

// Local converts to a wall-clock time today in loc (DST-aware).
func (c ClockUTC) Local(loc *time.Location, today time.Time) time.Time {
	y, m, d := today.In(time.UTC).Date()
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Add(time.Duration(int(c)%86400) * time.Second)
	return t.In(loc)
}

// String renders HH:MM UTC.
func (c ClockUTC) String() string {
	s := int(c) % 86400
	return fmt.Sprintf("%02d:%02d UTC", s/3600, s%3600/60)
}

// ClockFromLocal converts a local wall-clock hour/minute (in loc, today)
// to UTC seconds-of-day.
func ClockFromLocal(hour, minute int, loc *time.Location, today time.Time) (ClockUTC, error) {
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, fmt.Errorf("%w: time %02d:%02d", ErrInvalidValue, hour, minute)
	}
	y, m, d := today.In(loc).Date()
	t := time.Date(y, m, d, hour, minute, 0, 0, loc).UTC()
	return ClockUTC(t.Hour()*3600 + t.Minute()*60 + t.Second()), nil
}

// ParseHHMM parses "HH:MM".
func ParseHHMM(s string) (int, int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %q is not HH:MM", ErrInvalidValue, s)
	}
	return t.Hour(), t.Minute(), nil
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

func ptr[T any](v T) *T { return &v }
