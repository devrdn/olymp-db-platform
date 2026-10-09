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

const DeviceCookieName = "dbcontest_device"

// MinDeviceSecretLength is the shortest secret accepted: 256 bits, the
// HMAC-SHA256 key size.
const MinDeviceSecretLength = 32

const (
	// deviceTokenVersion is the first payload byte, so a later format is not
	// misread as this one.
	deviceTokenVersion byte = 1
	// devicePayloadLength is version, account id, device id, issue time,
	// session generation and status-change time.
	devicePayloadLength = 1 + 16 + 16 + 8 + 8 + 8
	// maxDeviceTokenLength bounds what Verify decodes from the caller.
	maxDeviceTokenLength = 256
	// deviceClockSkew tolerates replicas' clock differences.
	deviceClockSkew = time.Minute
	// deviceMACContext separates this MAC from any other use of the secret.
	deviceMACContext = "dbcontest device cookie v1\x00"
)

type DeviceID [16]byte

func (d DeviceID) String() string { return base64.RawURLEncoding.EncodeToString(d[:]) }

// Device is what a verified device cookie says: which browser, and the
// account's state when it signed in.
type Device struct {
	ID         DeviceID
	accountID  uuid.UUID
	generation int64
	statusAt   int64
	issued     time.Time
}

// Vouches reports whether the cookie still speaks for account: the same
// account id, still active, with an unchanged session generation (a password
// change advances it) and status-change time (a block, unblock, delete or
// restore moves it).
func (d Device) Vouches(account users.User) bool {
	return account.ID == d.accountID &&
		account.IsActive() &&
		account.SessionGeneration == d.generation &&
		statusMicros(account) == d.statusAt
}

// DeviceTrust issues and checks device cookies: an HMAC over the account, a
// random device id, the issue time and the account's state, so nothing is
// stored per browser and revocation follows from the state (Device.Vouches).
// The login is in the MAC input but not the cookie, so a cookie offered for
// another login fails verification before any lookup.
type DeviceTrust struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

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

func (d *DeviceTrust) TTL() time.Duration { return d.ttl }

// Issue returns a cookie vouching that this browser signed in to account. A
// zero id mints a new device; a known one keeps its attempt counter.
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

// Verify checks a cookie offered to sign in as login, reporting false for
// anything this installation did not issue for that login within its
// lifetime. The result must still pass Device.Vouches.
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
	device.issued = time.Unix(int64(binary.BigEndian.Uint64(payload[33:41])), 0) // #nosec G115 -- written by Issue from a timestamp.
	device.generation = int64(binary.BigEndian.Uint64(payload[41:49]))           // #nosec G115 -- as above.
	device.statusAt = int64(binary.BigEndian.Uint64(payload[49:57]))             // #nosec G115 -- as above.

	now := d.now()
	if device.issued.After(now.Add(deviceClockSkew)) || now.Sub(device.issued) > d.ttl {
		return Device{}, false
	}
	return device, true
}

// DueForRenewal reports whether a cookie has lived past half its lifetime.
func (d *DeviceTrust) DueForRenewal(device Device) bool {
	return d.now().Sub(device.issued) > d.ttl/2
}

// mac signs a payload for one normalised login.
func (d *DeviceTrust) mac(payload []byte, login string) []byte {
	h := hmac.New(sha256.New, d.secret)
	h.Write([]byte(deviceMACContext))
	h.Write(payload)
	h.Write([]byte(normalizeLogin(login)))
	return h.Sum(nil)
}

// statusMicros is when the status last changed, in the database's
// precision, or zero.
func statusMicros(account users.User) int64 {
	if account.StatusChangedAt == nil {
		return 0
	}
	return account.StatusChangedAt.UnixMicro()
}
