// Package password hashes and verifies passwords with argon2id.
//
// It is a primitive rather than part of a domain: both authentication (which
// checks a password) and account management (which sets one) need it, and
// keeping it separate is what stops those two packages depending on each other.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

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

	// maxPasswordLength bounds the work an unauthenticated caller can ask for:
	// Argon2 hashes the whole input, so an unbounded password is a CPU sink on
	// the login endpoint.
	maxPasswordLength = 1024
)

// ErrInvalidHash reports a stored digest that cannot be parsed. It is a data
// problem, never a reason to authenticate.
var ErrInvalidHash = errors.New("password hash is malformed")

// Hash returns a PHC-encoded argon2id digest of the password.
func Hash(password string) (string, error) {
	if password == "" {
		return "", errors.New("password must not be empty")
	}
	if len(password) > maxPasswordLength {
		return "", fmt.Errorf("password must be at most %d bytes", maxPasswordLength)
	}

	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// Verify reports whether password matches the stored digest.
//
// It returns an error only when the digest itself is unusable; a wrong
// password is (false, nil).
func Verify(encoded, password string) (bool, error) {
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
	got := argon2.IDKey([]byte(password), salt, params.time, params.memory, params.threads, argonKeyLen)

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
