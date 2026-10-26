// Package clock is the only package in this module allowed to call time.Now.
// Business code takes a Clock so tests can freeze time.
package clock

import "time"

// Clock provides the current time.
type Clock interface {
	Now() time.Time
}

// Real is the production clock. It always returns UTC.
type Real struct{}

// Now returns the current time in UTC.
func (Real) Now() time.Time {
	return time.Now().UTC()
}

// Fixed is a clock frozen at a specific instant, for tests.
type Fixed struct {
	t time.Time
}

// NewFixed returns a clock frozen at t.
func NewFixed(t time.Time) Fixed {
	return Fixed{t: t}
}

// Now returns the frozen instant unchanged.
func (f Fixed) Now() time.Time {
	return f.t
}
