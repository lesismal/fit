package channels

import "time"

// Clock is the time channels and push targets go by: the wall clock, or a
// test's, so that a schedule is tested without waiting for it.
type Clock interface {
	Now() time.Time
	// AfterFunc calls f on a goroutine of its own once d has passed, as
	// time.AfterFunc does; never on the caller's.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a call AfterFunc arranged.
type Timer interface {
	// Stop stops the call, and reports false when it has run or runs now.
	Stop() bool
}

// wallClock is the Clock of time.Now and time.AfterFunc.
type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

func (wallClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }
