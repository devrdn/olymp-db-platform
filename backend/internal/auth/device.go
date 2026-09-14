package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

// DeviceCookieName carries the proof that this browser has signed in to an
// account before.
const DeviceCookieName = "dbcontest_device"

// MinDeviceSecretLength is the shortest secret NewDeviceTrust accepts: the
// HMAC key is as strong as the secret, and 256 bits is SHA-256's own size.
const MinDeviceSecretLength = 32

const (
	// deviceTokenVersion is the first payload byte, so a later format can be
	// told apart from this one rather than misread as it.
	deviceTokenVersion byte = 1
	// devicePayloadLength is version, account id, device id, issue time,
	// session generation and status-change time.
	devicePayloadLength = 1 + 16 + 16 + 8 + 8 + 8
	// maxDeviceTokenLength bounds what Verify decodes: the cookie is caller
	// supplied, and a real token is well under this.
	maxDeviceTokenLength = 256
	// deviceClockSkew is how far in the future an issue time may be and still
	// be believed, for a secret shared by replicas whose clocks differ.
	deviceClockSkew = time.Minute
	// deviceMACContext separates this MAC from any other use of the secret.
	deviceMACContext = "dbcontest device cookie v1\x00"
)

// DeviceID names one browser that has signed in.
type DeviceID [16]byte

// String renders the id for a rate-limit key.
func (d DeviceID) String() string { return base64.RawURLEncoding.EncodeToString(d[:]) }

// Device is what a verified device cookie says: which browser, and the
// account as it was when the browser signed in to it.
type Device struct {
	ID         DeviceID
	accountID  uuid.UUID
	generation int64
	statusAt   int64
}

// Vouches reports whether the cookie still speaks for account as it is now.
//
// The account must be the one the cookie was issued for — a login deleted and
// taken by somebody else is a different account — and still active. Its
// session generation must be unchanged, which a password change advances,
// and so must the moment its status last changed, which a block, an unblock, a
// deletion or a restore moves. Any of those is the account saying that
// whatever vouched for it before does not now.
func (d Device) Vouches(account users.User) bool {
	return account.ID == d.accountID &&
		account.IsActive() &&
		account.SessionGeneration == d.generation &&
		statusMicros(account) == d.statusAt
}

// DeviceTrust issues and checks device cookies.
//
// The cookie is an HMAC over the account, a random device id, the issue time
// and the account's current state, keyed by a server secret, rather than a
// random token held in the cache. Nothing has to be stored for thirty days
// per browser that ever signed in: the in-process cache would lose every
// device on a restart and fill with them in between, and Redis would hold one
// record per lab machine per student for a month. Revocation needs no store
// either, because the account state is inside the MAC — see Device.Vouches.
//
// The login being tried is part of the MAC input but not of the cookie. A
// cookie offered for any other login simply fails verification, before
// anything is looked up, so a cookie for one account is no different from no
// cookie at all for another.
type DeviceTrust struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewDeviceTrust returns device trust keyed by secret, for cookies living ttl.
func NewDeviceTrust(secret []byte, ttl time.Duration) (*DeviceTrust, error) {
	if len(secret) < MinDeviceSecretLength {
		return nil, fmt.Errorf("device cookie secret must be at least %d bytes, got %d", MinDeviceSecretLength, len(secret))
	}
	if ttl <= 0 {
		return nil, errors.New("device cookie lifetime must be positive")
	}
	return &DeviceTrust{
		secret: append([]byte(nil), secret...),
		ttl:    ttl,
		now:    time.Now,
	}, nil
}

// TTL is how long a device cookie lives.
func (d *DeviceTrust) TTL() time.Duration { return d.ttl }

// Issue returns a cookie vouching that this browser signed in to account. A
// zero id mints a new device; a known one keeps the browser's identity, and
// with it its attempt counter, across sign-ins.
func (d *DeviceTrust) Issue(account users.User, id DeviceID) (string, error) {
	if id == (DeviceID{}) {
		if _, err := rand.Read(id[:]); err != nil {
			return "", fmt.Errorf("generate device id: %w", err)
		}
	}

	payload := make([]byte, 0, devicePayloadLength)
	payload = append(payload, deviceTokenVersion)
	payload = append(payload, account.ID[:]...)
	payload = append(payload, id[:]...)
	payload = binary.BigEndian.AppendUint64(payload, uint64(d.now().Unix()))            // #nosec G115 -- a timestamp's bits, read back the same way.
	payload = binary.BigEndian.AppendUint64(payload, uint64(account.SessionGeneration)) // #nosec G115 -- as above.
	payload = binary.BigEndian.AppendUint64(payload, uint64(statusMicros(account)))     // #nosec G115 -- as above.

	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(d.mac(payload, account.Login)), nil
}

// Verify checks a cookie offered with an attempt to sign in as login. It
// reports false for anything that is not a cookie this installation issued
// for that login within its lifetime; what it returns is then still to be
// checked against the account with Device.Vouches.
func (d *DeviceTrust) Verify(token, login string) (Device, bool) {
	if token == "" || len(token) > maxDeviceTokenLength {
		return Device{}, false
	}
	encodedPayload, encodedMAC, found := strings.Cut(token, ".")
	if !found {
		return Device{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil || len(payload) != devicePayloadLength || payload[0] != deviceTokenVersion {
		return Device{}, false
	}
	mac, err := base64.RawURLEncoding.DecodeString(encodedMAC)
	if err != nil || !hmac.Equal(mac, d.mac(payload, login)) {
		return Device{}, false
	}

	var device Device
	copy(device.accountID[:], payload[1:17])
	copy(device.ID[:], payload[17:33])
	issued := time.Unix(int64(binary.BigEndian.Uint64(payload[33:41])), 0) // #nosec G115 -- written by Issue from a timestamp.
	device.generation = int64(binary.BigEndian.Uint64(payload[41:49]))     // #nosec G115 -- as above.
	device.statusAt = int64(binary.BigEndian.Uint64(payload[49:57]))       // #nosec G115 -- as above.

	now := d.now()
	if issued.After(now.Add(deviceClockSkew)) || now.Sub(issued) > d.ttl {
		return Device{}, false
	}
	return device, true
}

// mac signs a payload for one login, normalised the way every lookup and
// throttle key is, so "Ivanov" and "ivanov" are the same account here too.
func (d *DeviceTrust) mac(payload []byte, login string) []byte {
	h := hmac.New(sha256.New, d.secret)
	h.Write([]byte(deviceMACContext))
	h.Write(payload)
	h.Write([]byte(normalizeLogin(login)))
	return h.Sum(nil)
}

// statusMicros is the moment the account's status last changed, in the
// precision the database keeps, or zero for an account whose status never
// changed.
func statusMicros(account users.User) int64 {
	if account.StatusChangedAt == nil {
		return 0
	}
	return account.StatusChangedAt.UnixMicro()
}
