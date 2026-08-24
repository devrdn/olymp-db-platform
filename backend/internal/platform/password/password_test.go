package password

import (
	"strings"
	"testing"
)

func TestHashProducesAVerifiableDigest(t *testing.T) {
	hash, err := Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("Hash() returned error: %v", err)
	}

	ok, err := Verify(hash, "correct horse battery staple")
	if err != nil {
		t.Fatalf("Verify() returned error: %v", err)
	}
	if !ok {
		t.Error("Verify() rejected the correct password")
	}
}

func TestHashRejectsAWrongPassword(t *testing.T) {
	hash, _ := Hash("correct horse battery staple")

	ok, err := Verify(hash, "Correct horse battery staple")
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
	first, _ := Hash("same password")
	second, _ := Hash("same password")

	if first == second {
		t.Error("two hashes of the same password are identical; the salt is not random")
	}
}

func TestHashUsesThePHCStringFormat(t *testing.T) {
	// The encoded parameters are what lets a future cost increase re-hash old
	// passwords instead of invalidating them.
	hash, _ := Hash("password")

	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("hash %q does not start with the argon2id identifier", hash)
	}
	if parts := strings.Split(hash, "$"); len(parts) != 6 {
		t.Errorf("hash %q has %d segments, want the 6 of a PHC string", hash, len(parts))
	}
}

func TestHashNeverContainsThePassword(t *testing.T) {
	hash, _ := Hash("hunter2")

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

	for _, hash := range malformed {
		ok, err := Verify(hash, "password")
		if ok {
			t.Errorf("Verify(%q) authenticated against a malformed hash", hash)
		}
		if err == nil {
			t.Errorf("Verify(%q) reported no error for a malformed hash", hash)
		}
	}
}

func TestVerifyRejectsAnEmptyPasswordAgainstARealHash(t *testing.T) {
	hash, _ := Hash("actual password")

	ok, _ := Verify(hash, "")

	if ok {
		t.Error("Verify() accepted an empty password")
	}
}

func TestHashRejectsAnEmptyPassword(t *testing.T) {
	// Storing a hash of "" would create an account anyone can enter.
	_, err := Hash("")

	if err == nil {
		t.Error("Hash() accepted an empty password, want error")
	}
}

func TestHashRejectsAnOversizedPassword(t *testing.T) {
	// Argon2 cost grows with input; an unbounded password is a way to burn CPU
	// on an unauthenticated endpoint.
	_, err := Hash(strings.Repeat("a", maxPasswordLength+1))

	if err == nil {
		t.Error("Hash() accepted an oversized password, want error")
	}
}

func TestNeedsRehashDetectsOutdatedParameters(t *testing.T) {
	// Old digests stay valid but should be upgraded on the next successful
	// login, so raising the cost does not lock anyone out.
	weak := "$argon2id$v=19$m=1024,t=1,p=1$c29tZXNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA"

	if !NeedsRehash(weak) {
		t.Error("NeedsRehash() did not flag a hash below the current cost")
	}

	current, _ := Hash("password")
	if NeedsRehash(current) {
		t.Error("NeedsRehash() flagged a freshly created hash")
	}
}

func TestNeedsRehashTreatsAnUnreadableHashAsOutdated(t *testing.T) {
	if !NeedsRehash("garbage") {
		t.Error("NeedsRehash() did not flag an unparseable hash")
	}
}
