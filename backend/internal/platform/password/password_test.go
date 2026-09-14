package password

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// newTestHasher returns a hasher roomy enough that no test below waits on it
// unless it means to.
func newTestHasher() *Hasher {
	return NewHasher(HasherConfig{Concurrency: 4, MaxWait: time.Second})
}

func mustHash(t *testing.T, h *Hasher, plaintext string) string {
	t.Helper()
	hash, err := h.Hash(context.Background(), plaintext)
	if err != nil {
		t.Fatalf("Hash() returned error: %v", err)
	}
	return hash
}

func TestHashProducesAVerifiableDigest(t *testing.T) {
	h := newTestHasher()
	hash := mustHash(t, h, "correct horse battery staple")

	ok, err := h.Verify(context.Background(), hash, "correct horse battery staple")
	if err != nil {
		t.Fatalf("Verify() returned error: %v", err)
	}
	if !ok {
		t.Error("Verify() rejected the correct password")
	}
}

func TestHashRejectsAWrongPassword(t *testing.T) {
	h := newTestHasher()
	hash := mustHash(t, h, "correct horse battery staple")

	ok, err := h.Verify(context.Background(), hash, "Correct horse battery staple")
	if err != nil {
		t.Fatalf("Verify() returned error: %v", err)
	}
	if ok {
		t.Error("Verify() accepted a wrong password")
	}
}

func TestHashIsSaltedPerCall(t *testing.T) {
	// Equal passwords must not produce equal digests, or a stolen dump would
	// reveal which accounts share a password.
	h := newTestHasher()
	first := mustHash(t, h, "same password")
	second := mustHash(t, h, "same password")

	if first == second {
		t.Error("two hashes of the same password are identical; the salt is not random")
	}
}

func TestHashUsesThePHCStringFormat(t *testing.T) {
	// The encoded parameters are what lets a future cost increase re-hash old
	// passwords instead of invalidating them.
	hash := mustHash(t, newTestHasher(), "password")

	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("hash %q does not start with the argon2id identifier", hash)
	}
	if parts := strings.Split(hash, "$"); len(parts) != 6 {
		t.Errorf("hash %q has %d segments, want the 6 of a PHC string", hash, len(parts))
	}
}

func TestHashNeverContainsThePassword(t *testing.T) {
	hash := mustHash(t, newTestHasher(), "hunter2")

	if strings.Contains(hash, "hunter2") {
		t.Errorf("hash %q contains the plaintext password", hash)
	}
}

func TestVerifyRejectsAMalformedHash(t *testing.T) {
	// A corrupt or truncated column value must fail closed, never authenticate.
	malformed := []string{
		"",
		"not-a-hash",
		"$argon2id$v=19$m=65536",
		"$argon2id$v=19$m=65536,t=3,p=2$not-base64$also-not-base64",
		"$bcrypt$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",
	}

	h := newTestHasher()
	for _, hash := range malformed {
		ok, err := h.Verify(context.Background(), hash, "password")
		if ok {
			t.Errorf("Verify(%q) authenticated against a malformed hash", hash)
		}
		if err == nil {
			t.Errorf("Verify(%q) reported no error for a malformed hash", hash)
		}
	}
}

func TestVerifyRejectsAnEmptyPasswordAgainstARealHash(t *testing.T) {
	h := newTestHasher()
	hash := mustHash(t, h, "actual password")

	ok, _ := h.Verify(context.Background(), hash, "")

	if ok {
		t.Error("Verify() accepted an empty password")
	}
}

func TestHashRejectsAnEmptyPassword(t *testing.T) {
	// Storing a hash of "" would create an account anyone can enter.
	_, err := newTestHasher().Hash(context.Background(), "")

	if err == nil {
		t.Error("Hash() accepted an empty password, want error")
	}
}

func TestHashRejectsAnOversizedPassword(t *testing.T) {
	// Argon2 cost grows with input; an unbounded password is a way to burn CPU
	// on an unauthenticated endpoint.
	_, err := newTestHasher().Hash(context.Background(), strings.Repeat("a", MaxLength+1))

	if err == nil {
		t.Error("Hash() accepted an oversized password, want error")
	}
}

func TestVerifyRefusesAnOversizedPasswordWithoutTakingASlot(t *testing.T) {
	// No stored digest can be of a password longer than Hash accepts, so the
	// answer is known without any work — and without waiting for a slot that
	// an honest sign-in could have used.
	h := NewHasher(HasherConfig{Concurrency: 1, MaxWait: 5 * time.Second})
	hash := mustHash(t, h, "actual password")
	release, err := h.Hold(context.Background())
	if err != nil {
		t.Fatalf("Hold() returned error: %v", err)
	}
	defer release()

	started := time.Now()
	ok, err := h.Verify(context.Background(), hash, strings.Repeat("a", MaxLength+1))

	if ok || err != nil {
		t.Errorf("Verify() = (%v, %v), want (false, nil) for an oversized password", ok, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("Verify() waited %v for a slot it did not need", elapsed)
	}
}

func TestHasherRefusesWithinTheWaitWhenEverySlotIsHeld(t *testing.T) {
	// Every argon2id computation holds 64 MiB. Admitting them without a bound
	// lets a burst of sign-in attempts exhaust the process's memory, so a call
	// that cannot get a slot within the wait is refused rather than queued.
	const wait = 50 * time.Millisecond
	h := NewHasher(HasherConfig{Concurrency: 1, MaxWait: wait})
	hash := mustHash(t, h, "actual password")

	release, err := h.Hold(context.Background())
	if err != nil {
		t.Fatalf("Hold() returned error: %v", err)
	}
	defer release()

	for name, call := range map[string]func() error{
		"Verify": func() error { _, err := h.Verify(context.Background(), hash, "actual password"); return err },
		"Hash":   func() error { _, err := h.Hash(context.Background(), "another password"); return err },
	} {
		started := time.Now()
		err := call()
		elapsed := time.Since(started)

		if !errors.Is(err, ErrBusy) {
			t.Errorf("%s() with every slot held = %v, want ErrBusy", name, err)
		}
		if elapsed < wait {
			t.Errorf("%s() gave up after %v, before the %v wait", name, elapsed, wait)
		}
		if elapsed > wait+time.Second {
			t.Errorf("%s() waited %v, far past the %v bound", name, elapsed, wait)
		}
	}
}

func TestHasherStopsWaitingWhenTheCallerDoes(t *testing.T) {
	// A request whose client has gone, or whose deadline is shorter than the
	// wait, must not keep a goroutine parked for the full wait.
	h := NewHasher(HasherConfig{Concurrency: 1, MaxWait: 30 * time.Second})
	release, err := h.Hold(context.Background())
	if err != nil {
		t.Fatalf("Hold() returned error: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err = h.Hash(ctx, "a password")

	if !errors.Is(err, ErrBusy) {
		t.Errorf("Hash() = %v, want ErrBusy", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Hash() = %v, want it to carry the context's own reason", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("Hash() waited %v after the caller's deadline", elapsed)
	}
}

func TestHasherAdmitsAWaiterOnceASlotIsReleased(t *testing.T) {
	h := NewHasher(HasherConfig{Concurrency: 1, MaxWait: 5 * time.Second})
	release, err := h.Hold(context.Background())
	if err != nil {
		t.Fatalf("Hold() returned error: %v", err)
	}
	time.AfterFunc(20*time.Millisecond, release)

	if _, err := h.Hash(context.Background(), "a password"); err != nil {
		t.Errorf("Hash() after the slot was released = %v, want nil", err)
	}
}

func TestReleasingASlotTwiceFreesItOnce(t *testing.T) {
	// A deferred release next to an explicit one must not hand out a slot the
	// hasher never had.
	h := NewHasher(HasherConfig{Concurrency: 1, MaxWait: 20 * time.Millisecond})
	release, err := h.Hold(context.Background())
	if err != nil {
		t.Fatalf("Hold() returned error: %v", err)
	}
	release()
	release()

	first, err := h.Hold(context.Background())
	if err != nil {
		t.Fatalf("Hold() returned error: %v", err)
	}
	defer first()
	if _, err := h.Hold(context.Background()); !errors.Is(err, ErrBusy) {
		t.Errorf("second Hold() = %v, want ErrBusy: a double release created a slot", err)
	}
}

func TestWithMaxWaitSharesTheSameSlots(t *testing.T) {
	// A caller that may wait longer is still one of the same bounded set of
	// hashes: a second pool would double the memory the bound exists to cap.
	h := NewHasher(HasherConfig{Concurrency: 1, MaxWait: time.Second})
	patient := h.WithMaxWait(30 * time.Millisecond)

	release, err := h.Hold(context.Background())
	if err != nil {
		t.Fatalf("Hold() returned error: %v", err)
	}
	defer release()

	if _, err := patient.Hash(context.Background(), "a password"); !errors.Is(err, ErrBusy) {
		t.Errorf("Hash() through WithMaxWait = %v, want ErrBusy: it did not share the slots", err)
	}
}

func TestNewHasherFillsInItsDefaults(t *testing.T) {
	h := NewHasher(HasherConfig{})

	if got := cap(h.slots); got != DefaultConcurrency() {
		t.Errorf("concurrency = %d, want DefaultConcurrency() = %d", got, DefaultConcurrency())
	}
	if DefaultConcurrency() < 2 {
		t.Errorf("DefaultConcurrency() = %d, want at least 2", DefaultConcurrency())
	}
	if h.maxWait != DefaultMaxWait {
		t.Errorf("max wait = %v, want %v", h.maxWait, DefaultMaxWait)
	}
}

func TestNeedsRehashDetectsOutdatedParameters(t *testing.T) {
	// Old digests stay valid but should be upgraded on the next successful
	// login, so raising the cost does not lock anyone out.
	weak := "$argon2id$v=19$m=1024,t=1,p=1$c29tZXNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA"

	if !NeedsRehash(weak) {
		t.Error("NeedsRehash() did not flag a hash below the current cost")
	}

	current := mustHash(t, newTestHasher(), "password")
	if NeedsRehash(current) {
		t.Error("NeedsRehash() flagged a freshly created hash")
	}
}

func TestNeedsRehashTreatsAnUnreadableHashAsOutdated(t *testing.T) {
	if !NeedsRehash("garbage") {
		t.Error("NeedsRehash() did not flag an unparseable hash")
	}
}
