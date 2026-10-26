package stream

import "time"

// FirstTokenMS implements the T-26 / T08 hard constraint 10 clock: it starts at
// P01 and stops at the first frame carrying a content increment. A role-only
// frame or an empty delta does not count, because no user-visible token has
// been produced yet.
func FirstTokenMS(startedAt, now time.Time) *int64 {
	ms := now.Sub(startedAt).Milliseconds()
	if ms < 0 {
		ms = 0
	}
	return &ms
}
