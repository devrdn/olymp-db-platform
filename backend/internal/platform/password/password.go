// Package password hashes and verifies passwords with argon2id, and bounds how
// many of those computations the process runs at once.
//
// It is a primitive rather than part of a domain: both authentication (which
// checks a password) and account management (which sets one) need it, and
// keeping it separate is what stops those two packages depending on each other.
//
// It does not decide who may try a password or how often — throttling belongs
// to package auth. What it does own is the one resource every caller shares:
// memory. Each computation allocates argonMemory, so the only place a bound
// holds for every caller is here, where the hashing happens.
package password

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// Argon2id cost parameters. These follow the OWASP baseline (64 MiB, 3
// iterations); the values are encoded into every digest, so raising them later
// upgrades accounts on their next login instead of invalidating them.
const (
	argonMemory  uint32 = 64 * 1024 // KiB
	argonTime    uint32 = 3
	argonThreads uint8  = 2
	argonKeyLen  uint32 = 32
	saltLength          = 16

	// MemoryPerHash is what one Hash or Verify holds while it runs. A
	// deployment sizes its memory limit as Concurrency × MemoryPerHash plus
	// everything else the process does.
	MemoryPerHash = int64(argonMemory) << 10

	// MaxLength bounds the work an unauthenticated caller can ask for:
	// Argon2 hashes the whole input, so an unbounded password is a CPU sink on
	// the login endpoint. No stored digest can be of a longer password, so a
	// caller may refuse one before doing anything else with it.
	MaxLength = 1024
)

// Defaults for a HasherConfig that leaves a field zero.
const (
	// DefaultMaxWait is how long a call waits for a slot before it is refused.
	// Long enough to ride out an ordinary burst — a lecture hall signing in at
	// once — and short enough that a flood is answered rather than queued
	// behind itself.
	DefaultMaxWait = 2 * time.Second

	// MaxConcurrency bounds a configured concurrency: at 64 MiB per slot it
	// is already more memory than an installation of this kind is given.
	MaxConcurrency = 64
)

// DefaultConcurrency is the number of simultaneous hashes when the deployment
// does not state one: one per CPU, and never fewer than two, so a single slow
// verification cannot stall every sign-in behind it.
func DefaultConcurrency() int {
	return max(2, runtime.NumCPU())
}

// ErrInvalidHash reports a stored digest that cannot be parsed. It is a data
// problem, never a reason to authenticate.
var ErrInvalidHash = errors.New("password hash is malformed")

// ErrBusy reports that no hashing slot became free within the wait. It is a
// statement about load, not about the password: the caller answers that the
// service is busy and the attempt was not evaluated.
var ErrBusy = errors.New("password hashing is at capacity")

// HasherConfig sizes a Hasher.
type HasherConfig struct {
	// Concurrency is how many hashes may run at once. Zero takes
	// DefaultConcurrency.
	Concurrency int
	// MaxWait is how long a call waits for a slot. Zero takes DefaultMaxWait.
	MaxWait time.Duration
}

// Hasher computes and checks password digests with a process-wide bound on
// how many run at once.
//
// argon2id is memory-hard by design: every computation holds 64 MiB. Without a
// bound, the memory a process needs is 64 MiB times however many sign-in
// requests arrive together, and an anonymous burst decides that number. One
// Hasher is built at start-up and shared by every caller, so the bound is on
// the process rather than on any one endpoint.
type Hasher struct {
	slots   chan struct{}
	maxWait time.Duration
}

// NewHasher returns a hasher with its own set of slots.
func NewHasher(cfg HasherConfig) *Hasher {
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = DefaultConcurrency()
	}
	concurrency = min(concurrency, MaxConcurrency)
	wait := cfg.MaxWait
	if wait <= 0 {
		wait = DefaultMaxWait
	}
	return &Hasher{slots: make(chan struct{}, concurrency), maxWait: wait}
}

// WithMaxWait returns a hasher that shares this one's slots but waits up to d
// for one. An administrator's batch may reasonably wait longer than an
// anonymous sign-in; it must not get slots of its own, or the bound would be
// on each caller rather than on the process.
func (h *Hasher) WithMaxWait(d time.Duration) *Hasher {
	if d <= 0 {
		d = DefaultMaxWait
	}
	return &Hasher{slots: h.slots, maxWait: d}
}

// Hold takes one slot, waiting at most the hasher's wait and never past the
// context, and returns the function that gives it back. Calling the release
// function more than once frees the slot once.
//
// Hash and Verify take their slot this way. It is exported so a caller can
// fill the gate on purpose — a test that proves a refusal, or work that must
// not overlap hashing — on exactly the terms a hash would.
func (h *Hasher) Hold(ctx context.Context) (func(), error) {
	select {
	case h.slots <- struct{}{}:
		return h.releaser(), nil
	default:
	}

	timer := time.NewTimer(h.maxWait)
	defer timer.Stop()

	select {
	case h.slots <- struct{}{}:
		return h.releaser(), nil
	case <-timer.C:
		return nil, ErrBusy
	case <-ctx.Done():
		// Still ErrBusy to the caller's error mapping: nothing was evaluated.
		// The context's own reason travels with it for the log.
		return nil, fmt.Errorf("%w: %w", ErrBusy, context.Cause(ctx))
	}
}

func (h *Hasher) releaser() func() {
	var once sync.Once
	return func() { once.Do(func() { <-h.slots }) }
}

// Hash returns a PHC-encoded argon2id digest of the password.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	if password == "" {
		return "", errors.New("password must not be empty")
	}
	if len(password) > MaxLength {
		return "", fmt.Errorf("password must be at most %d bytes", MaxLength)
	}

	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	release, err := h.Hold(ctx)
	if err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	release()

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// Verify reports whether password matches the stored digest.
//
// It returns an error when the digest itself is unusable, or ErrBusy when no
// slot came free; a wrong password is (false, nil).
func (h *Hasher) Verify(ctx context.Context, encoded, password string) (bool, error) {
	params, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}

	// Every digest this package writes is argonKeyLen bytes. Any other length
	// is a corrupt or foreign column value, not an older parameter set — the
	// cost parameters vary between versions, the key length does not.
	// Deriving with the constant also keeps the call free of a length
	// conversion that would have to be range-checked.
	if len(want) != int(argonKeyLen) {
		return false, ErrInvalidHash
	}

	// Hash never stores a digest of a longer password, so this one cannot
	// match — and deciding that costs neither a slot nor a computation.
	if len(password) > MaxLength {
		return false, nil
	}

	release, err := h.Hold(ctx)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, params.time, params.memory, params.threads, argonKeyLen)
	release()

	// Constant-time comparison: a byte-by-byte match would leak how much of a
	// guess was right through timing.
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// NeedsRehash reports whether a digest was produced with weaker parameters
// than the current ones, or cannot be read at all. Callers upgrade the stored
// hash after a successful login.
func NeedsRehash(encoded string) bool {
	params, _, _, err := decodeHash(encoded)
	if err != nil {
		return true
	}
	return params.memory < argonMemory || params.time < argonTime || params.threads < argonThreads
}

type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
}

// decodeHash parses "$argon2id$v=19$m=...,t=...,p=...$salt$key".
func decodeHash(encoded string) (argonParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" {
		return argonParams{}, nil, nil, ErrInvalidHash
	}
	if parts[1] != "argon2id" {
		return argonParams{}, nil, nil, fmt.Errorf("%w: unsupported algorithm %q", ErrInvalidHash, parts[1])
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return argonParams{}, nil, nil, ErrInvalidHash
	}
	if version != argon2.Version {
		return argonParams{}, nil, nil, fmt.Errorf("%w: unsupported version %d", ErrInvalidHash, version)
	}

	var params argonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.memory, &params.time, &params.threads); err != nil {
		return argonParams{}, nil, nil, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return argonParams{}, nil, nil, ErrInvalidHash
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return argonParams{}, nil, nil, ErrInvalidHash
	}
	if len(salt) == 0 || len(key) == 0 {
		return argonParams{}, nil, nil, ErrInvalidHash
	}

	return params, salt, key, nil
}
