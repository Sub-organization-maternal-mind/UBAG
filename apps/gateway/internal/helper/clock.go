package helper

import "time"

// Clock is the time source of the service: lease expiry, the attempt deadline
// and the drain grace are all timers, and tests drive them with a fake clock
// instead of sleeping.
type Clock interface {
	Now() time.Time
	// AfterFunc runs f once, in its own goroutine, after d.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is the part of *time.Timer the service uses.
type Timer interface {
	Stop() bool
	Reset(d time.Duration) bool
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }
