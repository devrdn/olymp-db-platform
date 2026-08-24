// Package passwordtest builds password digests with deliberately weak
// parameters, so tests can exercise the upgrade-on-login path with a digest
// that still verifies correctly.
package passwordtest

import (
	"encoding/base64"
	"fmt"
	"testing"

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
	if ok, err := password.Verify(hash, plaintext); err != nil || !ok {
		t.Fatalf("helper produced a hash that does not verify: ok=%v err=%v", ok, err)
	}
	return hash
}
