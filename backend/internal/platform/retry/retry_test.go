package retry_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/platform/retry"
)

var errRefused = errors.New("refused")

func TestARefusalCarriesItsWaitThroughWrapping(t *testing.T) {
	err := fmt.Errorf("while signing up: %w", retry.After(errRefused, 90*time.Second))

	if !errors.Is(err, errRefused) {
		t.Errorf("errors.Is(%v, errRefused) = false, want the refusal kept", err)
	}
	if wait, ok := retry.Delay(err); !ok || wait != 90*time.Second {
		t.Errorf("Delay() = %s, %v, want 1m30s", wait, ok)
	}
}

func TestACarriedRefusalKeepsItsWaitUnderAnotherSentinel(t *testing.T) {
	errOther := errors.New("other")
	carried := retry.Carry(retry.After(errRefused, time.Minute), errOther)
	if wait, ok := retry.Delay(carried); !errors.Is(carried, errOther) || errors.Is(carried, errRefused) || !ok || wait != time.Minute {
		t.Errorf("Carry() = %v waiting %s (%v), want errOther waiting a minute", carried, wait, ok)
	}
	if bare := retry.Carry(errRefused, errOther); bare != errOther {
		t.Errorf("Carry() of an error with no wait = %v, want errOther itself", bare)
	}
}

func TestAnErrorThatSaysNothingHasNoDelay(t *testing.T) {
	if wait, ok := retry.Delay(errRefused); ok {
		t.Errorf("Delay() = %s, true, want nothing", wait)
	}
}

// Any error that knows its own wait is read the same way, such as
// httpx.Refusal.
type ownWait struct{}

func (ownWait) Error() string             { return "own" }
func (ownWait) RetryAfter() time.Duration { return time.Minute }

func TestAnErrorThatSaysItsOwnWaitIsRead(t *testing.T) {
	if wait, ok := retry.Delay(fmt.Errorf("x: %w", ownWait{})); !ok || wait != time.Minute {
		t.Errorf("Delay() = %s, %v, want 1m0s", wait, ok)
	}
}

func TestAWaitIsSaidInWholeSecondsRoundedUp(t *testing.T) {
	for d, want := range map[time.Duration]int{
		0: 1, 300 * time.Millisecond: 1, time.Second: 1, 1500 * time.Millisecond: 2, 15 * time.Minute: 900,
	} {
		if got := retry.Seconds(d); got != want {
			t.Errorf("Seconds(%s) = %d, want %d", d, got, want)
		}
	}
}
