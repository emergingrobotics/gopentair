package gopentair

import (
	"errors"
	"fmt"
	"time"

	"github.com/emergingrobotics/gopentair/internal/auth"
	"github.com/emergingrobotics/gopentair/internal/transport"
)

// Sentinel errors; test with errors.Is.
var (
	// ErrReauthRequired: no usable session and no password configured.
	ErrReauthRequired = auth.ErrReauthRequired
	// ErrInvalidCredentials: Cognito rejected the username or password.
	ErrInvalidCredentials = auth.ErrInvalidCredentials
	// ErrDeviceNotFound: no such device on the account.
	ErrDeviceNotFound = errors.New("device not found")
	// ErrDeviceOffline: the device reports online=false.
	ErrDeviceOffline = errors.New("device offline")
	// ErrUnsupportedDevice: a typed view was used with the wrong device type.
	ErrUnsupportedDevice = errors.New("unsupported device type")
	// ErrNotAcknowledged: a write's response code was not set_device_success.
	ErrNotAcknowledged = errors.New("write not acknowledged")
	// ErrNotConfirmed: a write was acknowledged but the state did not
	// reflect it before the confirm timeout.
	ErrNotConfirmed = errors.New("write not confirmed")
	// ErrRateLimited: blocked by the client-side debounce.
	ErrRateLimited = errors.New("rate limited")
	// ErrInvalidValue: an argument was out of range.
	ErrInvalidValue = errors.New("invalid value")
	// ErrAmbiguousDevice: several devices match and none was chosen.
	ErrAmbiguousDevice = errors.New("more than one matching device; specify one")
)

// AuthError is an authentication failure (op, Cognito error type, message).
type AuthError = auth.Error

// ChallengeError is returned for Cognito challenges this client does not
// support (MFA, NEW_PASSWORD_REQUIRED, ...).
type ChallengeError = auth.ChallengeError

// APIError is a failed REST call. Paths never contain device IDs.
type APIError struct {
	Status    int // 0 for network errors
	Method    string
	Path      string
	Message   string
	Ambiguous bool // a write may or may not have been applied
	Err       error
}

func (e *APIError) Error() string {
	if e.Status == 0 && e.Err != nil {
		s := fmt.Sprintf("%s %s: %v", e.Method, e.Path, e.Err)
		if e.Ambiguous {
			s += " (write outcome unknown)"
		}
		return s
	}
	s := fmt.Sprintf("%s %s: HTTP %d", e.Method, e.Path, e.Status)
	if e.Message != "" {
		s += ": " + e.Message
	}
	if e.Ambiguous {
		s += " (write outcome unknown)"
	}
	return s
}

func (e *APIError) Unwrap() error { return e.Err }

// DeviceError attaches a device operation to an error. DeviceID is kept
// for programmatic use but is not included in Error().
type DeviceError struct {
	DeviceID string
	Op       string
	Err      error
}

func (e *DeviceError) Error() string { return e.Op + ": " + e.Err.Error() }
func (e *DeviceError) Unwrap() error { return e.Err }

// PlanError reports a failed WritePlan step.
type PlanError struct {
	Applied int // steps applied before the failure
	Step    int // index of the failed step
	Err     error
}

func (e *PlanError) Error() string {
	return fmt.Sprintf("write step %d failed after %d applied: %v", e.Step+1, e.Applied, e.Err)
}
func (e *PlanError) Unwrap() error { return e.Err }

// RateLimitError is returned when a debounce window is active.
type RateLimitError struct {
	Key  DebounceKey
	Wait time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited (%s): retry in %s", e.Key, e.Wait.Round(time.Second))
}
func (e *RateLimitError) Is(t error) bool { return t == ErrRateLimited }

// NotConfirmedError carries the last observed state.
type NotConfirmedError struct {
	Timeout time.Duration
	Last    *DeviceState
}

func (e *NotConfirmedError) Error() string {
	return fmt.Sprintf("write not confirmed within %s", e.Timeout)
}
func (e *NotConfirmedError) Is(t error) bool { return t == ErrNotConfirmed }

func convertErr(err error) error {
	if err == nil {
		return nil
	}
	var se *transport.StatusError
	if errors.As(err, &se) {
		return &APIError{Status: se.Status, Method: se.Method, Path: se.Path, Message: se.Message, Ambiguous: se.Ambiguous}
	}
	var ne *transport.NetError
	if errors.As(err, &ne) {
		return &APIError{Method: ne.Method, Path: ne.Path, Ambiguous: ne.Ambiguous, Err: ne.Err}
	}
	return err
}
