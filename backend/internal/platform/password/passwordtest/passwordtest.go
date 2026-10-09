// Package passwordtest builds password digests for tests: ordinary ones for
// fixtures, and valid ones with weak parameters for the upgrade-on-login path.
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

// Weaker than production: verification succeeds and NeedsRehash reports true.
const (
	weakMemory  uint32 = 8 * 1024
	weakTime    uint32 = 1
	weakThreads uint8  = 1
	keyLength   uint32 = 32
)

// NewHasher returns a hasher roomy enough that parallel tests on a slow
// machine do not refuse each other.
func NewHasher() *password.Hasher {
	return password.NewHasher(password.HasherConfig{Concurrency: 4, MaxWait: time.Minute})
}

var shared = NewHasher()

// Hash returns a production-strength digest of plaintext.
func Hash(t testing.TB, plaintext string) string {
	t.Helper()
	hash, err := shared.Hash(context.Background(), plaintext)
	if err != nil {
		t.Fatalf("password hash: %v", err)
	}
	return hash
}

// Matches reports whether plaintext verifies against hash.
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
