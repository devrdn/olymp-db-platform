// Package password hashes and verifies passwords with argon2id and bounds how
// many of those computations the process runs at once, since each holds
// argonMemory. It is shared by auth and account management so neither depends
// on the other. It does not decide who may try a password or how often; that
// throttling belongs to package auth.
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

// Argon2id cost parameters (OWASP baseline). They are encoded into every
// digest, so raising them upgrades accounts on next login instead of
// invalidating them.
const (
	argonMemory  uint32 = 64 * 1024 // KiB
	argonTime    uint32 = 3
	argonThreads uint8  = 2
	argonKeyLen  uint32 = 32
	saltLength          = 16

	// MemoryPerHash is what one Hash or Verify holds while it runs, in bytes.
	MemoryPerHash = int64(argonMemory) << 10

	// MaxLength bounds the work an unauthenticated caller can ask for, since
	// Argon2 hashes the whole input. No stored digest is of a longer password,
	// so a caller may refuse one before doing anything else.
	MaxLength = 1024
)

// Defaults for a HasherConfig that leaves a field zero.
const (
	// DefaultMaxWait is how long a call waits for a slot before it is refused:
	// enough for a lecture hall signing in at once, short enough that a flood
	// is answered rather than queued.
	DefaultMaxWait = 2 * time.Second

	// MaxConcurrency bounds a configured concurrency (64 MiB per slot).
	MaxConcurrency = 64
)

// DefaultConcurrency is one hash per CPU and never fewer than two, so a single
// slow verification cannot stall every sign-in behind it.
func DefaultConcurrency() int {
	return max(2, runtime.NumCPU())
}

// ErrInvalidHash reports a stored digest that cannot be parsed. It never
// authenticates.
var ErrInvalidHash = errors.New("password hash is malformed")

// ErrBusy reports that no hashing slot became free within the wait. It says
// nothing about the password: the attempt was not evaluated.
var ErrBusy = errors.New("password hashing is at capacity")

// HasherConfig sizes a Hasher.
type HasherConfig struct {
	// Zero takes DefaultConcurrency.
	Concurrency int
	// Zero takes DefaultMaxWait.
	MaxWait time.Duration
}

// Hasher computes and checks password digests with a bound on how many run at
// once. Each holds 64 MiB, so without a bound an anonymous burst of sign-ins
// decides the process's memory. One Hasher is shared by every caller, so the
// bound is on the process, not on an endpoint.
type Hasher struct {
	slots   chan struct{}
	maxWait time.Duration
}

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
// for one. A batch may wait longer than a sign-in, but must not get slots of
// its own, or the memory bound would no longer hold.
func (h *Hasher) WithMaxWait(d time.Duration) *Hasher {
	if d <= 0 {
		d = DefaultMaxWait
	}
	return &Hasher{slots: h.slots, maxWait: d}
}

// ErrSlotReleased reports work asked of a slot after it was given back.
var ErrSlotReleased = errors.New("password hashing slot already released")

// Slot is one unit of the hasher's concurrency, held by the caller until
// Release. Release is safe to call more than once.
type Slot struct {
	hasher *Hasher
	once   sync.Once
	mu     sync.Mutex
	done   bool
}

// Hold takes one slot, waiting at most the hasher's wait and never past the
// context.
//
// A caller holds a slot itself when it must know a computation will be
// admitted before it spends anything else: sign-in takes its slot before it
// counts the attempt, so a refusal for load never costs the account's owner
// an attempt, then computes with Slot.Verify.
func (h *Hasher) Hold(ctx context.Context) (*Slot, error) {
	select {
	case h.slots <- struct{}{}:
		return &Slot{hasher: h}, nil
	default:
	}

	timer := time.NewTimer(h.maxWait)
	defer timer.Stop()

	select {
	case h.slots <- struct{}{}:
		return &Slot{hasher: h}, nil
	case <-timer.C:
		return nil, ErrBusy
	case <-ctx.Done():
		// Still ErrBusy, since nothing was evaluated; the cause goes to the log.
		return nil, fmt.Errorf("%w: %w", ErrBusy, context.Cause(ctx))
	}
}

// Release gives the slot back. Only the first call has an effect.
func (s *Slot) Release() {
	s.once.Do(func() {
		s.mu.Lock()
		s.done = true
		s.mu.Unlock()
		<-s.hasher.slots
	})
}

// Verify is Hasher.Verify computed inside this slot. It never waits and
// reports ErrSlotReleased once the slot has been given back.
func (s *Slot) Verify(encoded, password string) (bool, error) {
	params, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	// Every digest this package writes is argonKeyLen bytes; cost parameters
	// vary between versions, the key length does not. Any other length is a
	// corrupt or foreign value.
	if len(want) != int(argonKeyLen) {
		return false, ErrInvalidHash
	}
	// Hash never stores a digest of a longer password, so this cannot match.
	if len(password) > MaxLength {
		return false, nil
	}

	// Held across the computation so a concurrent Release cannot free the
	// slot while its memory is in use.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return false, ErrSlotReleased
	}
	got := argon2.IDKey([]byte(password), salt, params.time, params.memory, params.threads, argonKeyLen)

	// Constant time, so timing does not leak how much of a guess was right.
	return subtle.ConstantTimeCompare(got, want) == 1, nil
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

	slot, err := h.Hold(ctx)
	if err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	slot.Release()

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// Verify reports whether password matches the stored digest. A wrong
// password is (false, nil); an unusable digest is an error, and no free slot
// is ErrBusy. An unreadable digest or an over-long password is answered
// without a slot.
func (h *Hasher) Verify(ctx context.Context, encoded, password string) (bool, error) {
	if _, _, _, err := decodeHash(encoded); err != nil {
		return false, err
	}
	if len(password) > MaxLength {
		return false, nil
	}

	slot, err := h.Hold(ctx)
	if err != nil {
		return false, err
	}
	defer slot.Release()
	return slot.Verify(encoded, password)
}

// Concurrency reports how many computations this hasher admits at once.
func (h *Hasher) Concurrency() int { return cap(h.slots) }

// NeedsRehash reports whether a digest was produced with weaker parameters
// than the current ones, or cannot be read at all.
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
