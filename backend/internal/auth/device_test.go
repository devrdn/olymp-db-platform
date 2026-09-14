package auth

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

// testDeviceSecret is long enough for NewDeviceTrust and used by every test
// in the package that needs trusted devices.
var testDeviceSecret = bytes.Repeat([]byte("k"), MinDeviceSecretLength)

func testDevices(t *testing.T) *DeviceTrust {
	t.Helper()
	devices, err := NewDeviceTrust(testDeviceSecret, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("NewDeviceTrust() returned error: %v", err)
	}
	return devices
}

func deviceAccount() users.User {
	changed := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	return users.User{
		ID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), Login: "ivanov",
		Status: users.StatusActive, SessionGeneration: 3, StatusChangedAt: &changed,
	}
}

func TestADeviceCookieVouchesForTheAccountItWasIssuedFor(t *testing.T) {
	devices := testDevices(t)
	account := deviceAccount()
	token, err := devices.Issue(account, DeviceID{})
	if err != nil {
		t.Fatalf("Issue() returned error: %v", err)
	}

	device, ok := devices.Verify(token, "IVANOV ")
	if !ok {
		t.Fatal("Verify() refused the cookie for the login it was issued to")
	}
	if !device.Vouches(account) {
		t.Error("the cookie does not vouch for the account as it was when issued")
	}
	if device.ID == (DeviceID{}) {
		t.Error("Issue() with no device id did not mint one")
	}
}

func TestADeviceCookieKeepsItsDeviceIDWhenReissued(t *testing.T) {
	devices := testDevices(t)
	first, _ := devices.Issue(deviceAccount(), DeviceID{})
	device, _ := devices.Verify(first, "ivanov")

	again, _ := devices.Issue(deviceAccount(), device.ID)
	reissued, ok := devices.Verify(again, "ivanov")

	if !ok || reissued.ID != device.ID {
		t.Errorf("reissued device = %v (ok %v), want the same id %v", reissued.ID, ok, device.ID)
	}
}

func TestADeviceCookieForAnotherLoginIsIgnored(t *testing.T) {
	devices := testDevices(t)
	token, _ := devices.Issue(deviceAccount(), DeviceID{})

	if _, ok := devices.Verify(token, "petrov"); ok {
		t.Error("a cookie issued for ivanov was accepted for petrov")
	}
}

func TestAForgedDeviceCookieIsIgnored(t *testing.T) {
	devices := testDevices(t)
	token, _ := devices.Issue(deviceAccount(), DeviceID{})
	other, _ := NewDeviceTrust(bytes.Repeat([]byte("x"), MinDeviceSecretLength), 30*24*time.Hour)
	foreign, _ := other.Issue(deviceAccount(), DeviceID{})

	payload, mac, _ := strings.Cut(token, ".")
	flipped := []byte(payload)
	flipped[5] ^= 0x01

	for name, candidate := range map[string]string{
		"empty":                "",
		"garbage":              "not-a-cookie",
		"no signature":         payload,
		"payload altered":      string(flipped) + "." + mac,
		"signature truncated":  payload + "." + mac[:len(mac)-2],
		"another secret":       foreign,
		"overlong":             strings.Repeat("a", maxDeviceTokenLength+1),
		"signature of nothing": "." + mac,
	} {
		if _, ok := devices.Verify(candidate, "ivanov"); ok {
			t.Errorf("%s: Verify() accepted it", name)
		}
	}
}

func TestAnExpiredDeviceCookieIsIgnored(t *testing.T) {
	devices := testDevices(t)
	token, _ := devices.Issue(deviceAccount(), DeviceID{})

	devices.now = func() time.Time { return time.Now().Add(30*24*time.Hour + time.Minute) }

	if _, ok := devices.Verify(token, "ivanov"); ok {
		t.Error("a cookie past its lifetime was accepted")
	}
}

func TestADeviceCookieStopsVouchingOnceTheAccountChanges(t *testing.T) {
	// A changed password advances the session generation; a block, unblock,
	// deletion or restore moves the status timestamp. Either is the account
	// saying "whatever vouched for me before, does not now".
	devices := testDevices(t)
	token, _ := devices.Issue(deviceAccount(), DeviceID{})
	device, _ := devices.Verify(token, "ivanov")

	passwordChanged := deviceAccount()
	passwordChanged.SessionGeneration++
	statusChanged := deviceAccount()
	later := statusChanged.StatusChangedAt.Add(time.Second)
	statusChanged.StatusChangedAt = &later
	blocked := deviceAccount()
	blocked.Status = users.StatusBlocked
	replaced := deviceAccount()
	replaced.ID = uuid.New()

	for name, account := range map[string]users.User{
		"password changed":    passwordChanged,
		"status changed":      statusChanged,
		"blocked":             blocked,
		"login now another's": replaced,
	} {
		if device.Vouches(account) {
			t.Errorf("%s: the cookie still vouches", name)
		}
	}
}

func TestADeviceSecretShorterThanTheMinimumIsRefused(t *testing.T) {
	if _, err := NewDeviceTrust(bytes.Repeat([]byte("k"), MinDeviceSecretLength-1), time.Hour); err == nil {
		t.Error("NewDeviceTrust() accepted a short secret")
	}
}
