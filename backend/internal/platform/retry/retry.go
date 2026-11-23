// Package retry carries how long a refused caller must wait, from the code
// that refused to the HTTP layer that says so in Retry-After, so neither
// needs to know the other. It does not decide a wait: the refusing code
// knows its window, and this only keeps the number beside the error.
package retry

import (
	"errors"
	"time"
)

type refusal struct {
	err   error
	after time.Duration
}

func (r *refusal) Error() string { return r.err.Error() }

func (r *refusal) Unwrap() error { return r.err }

func (r *refusal) RetryAfter() time.Duration { return r.after }

// After is err, still matched by errors.Is, saying that whoever it refused
// may try again after d.
func After(err error, d time.Duration) error {
	return &refusal{err: err, after: d}
}

// Carry is onto, saying whatever wait err says: a refusal of one package
// answered with another's sentinel keeps its Retry-After.
func Carry(err, onto error) error {
	if wait, ok := Delay(err); ok {
		return After(onto, wait)
	}
	return onto
}

// Delay is how long err says to wait, from After or from any error in its
// chain with a RetryAfter method; ok is false when it says nothing.
func Delay(err error) (wait time.Duration, ok bool) {
	var waits interface{ RetryAfter() time.Duration }
	if errors.As(err, &waits) {
		return waits.RetryAfter(), true
	}
	return 0, false
}

// Seconds is d as Retry-After carries it: whole seconds, rounded up so a
// client that waits exactly that long finds the window over, and never zero.
func Seconds(d time.Duration) int {
	seconds := int((d + time.Second - 1) / time.Second)
	return max(seconds, 1)
}
