// Package passwordtest builds password digests for tests: ordinary ones for
// fixtures, and ones with deliberately weak parameters so tests can exercise
// the upgrade-on-login path with a digest that still verifies correctly.
package passwordtest

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"golang.org/x/crypto/argon2"
)

// Cost below the current production parameters, but still a valid argon2id
// digest: verification succeeds and NeedsRehash reports true.
const (
	weakMemory  uint32 = 8 * 1024
	weakTime    uint32 = 1
	weakThreads uint8  = 1
	keyLength   uint32 = 32
)

// NewHasher returns a hasher for a test fixture: roomy enough that tests
// running in parallel inside one package do not refuse each other, with a
// wait long enough that a slow machine does not either.
func NewHasher() *password.Hasher {
	return password.NewHasher(password.HasherConfig{Concurrency: 4, MaxWait: time.Minute})
}

// shared serves Hash and Matches, so fixture digests across a test binary are
// bounded the same way production ones are.
var shared = NewHasher()

// Hash returns a production-strength digest of plaintext, failing the test on
// error.
func Hash(t testing.TB, plaintext string) string {
	t.Helper()
	hash, err := shared.Hash(context.Background(), plaintext)
	if err != nil {
		t.Fatalf("password hash: %v", err)
	}
	return hash
}

// Matches reports whether plaintext verifies against hash, failing the test
// when the digest cannot be read at all.
func Matches(t testing.TB, hash, plaintext string) bool {
	t.Helper()
	ok, err := shared.Verify(context.Background(), hash, plaintext)
	if err != nil {
		t.Fatalf("password verify: %v", err)
	}
	return ok
}

// WeakHash returns a digest of plaintext made with outdated parameters.
func WeakHash(t *testing.T, plaintext string) string {
	t.Helper()

	salt := []byte("sixteenbytesalt!")
	key := argon2.IDKey([]byte(plaintext), salt, weakTime, weakMemory, weakThreads, keyLength)

	hash := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, weakMemory, weakTime, weakThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)

	if !password.NeedsRehash(hash) {
		t.Fatalf("helper produced a hash that is not considered outdated: %s", hash)
	}
	if !Matches(t, hash, plaintext) {
		t.Fatalf("helper produced a hash that does not verify")
	}
	return hash
}
