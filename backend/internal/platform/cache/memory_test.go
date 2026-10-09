package cache

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestMemoryReturnsStoredValue(t *testing.T) {
	c := NewMemory(10)
	ctx := context.Background()

	if err := c.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("Set() returned error: %v", err)
	}

	got, found, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get() returned error: %v", err)
	}
	if !found {
		t.Fatal("Get() reported the key missing right after Set")
	}
	if string(got) != "v" {
		t.Errorf("value = %q, want v", got)
	}
}

func TestMemoryReportsMissForUnknownKey(t *testing.T) {
	c := NewMemory(10)

	_, found, err := c.Get(context.Background(), "absent")

	if err != nil {
		t.Fatalf("Get() returned error: %v", err)
	}
	if found {
		t.Error("Get() found a key that was never set")
	}
}

func TestMemoryExpiresValueAfterTTL(t *testing.T) {
	c := NewMemory(10)
	ctx := context.Background()
	_ = c.Set(ctx, "k", []byte("v"), 20*time.Millisecond)

	time.Sleep(50 * time.Millisecond)

	if _, found, _ := c.Get(ctx, "k"); found {
		t.Error("value survived its TTL; a session would outlive its expiry")
	}
}

func TestMemoryDeleteRemovesValue(t *testing.T) {
	c := NewMemory(10)
	ctx := context.Background()
	_ = c.Set(ctx, "k", []byte("v"), time.Minute)

	if err := c.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete() returned error: %v", err)
	}

	if _, found, _ := c.Get(ctx, "k"); found {
		t.Error("value survived Delete; logout would not end the session")
	}
}

func TestMemoryIncrCountsWithinWindow(t *testing.T) {
	c := NewMemory(10)
	ctx := context.Background()

	for want := int64(1); want <= 3; want++ {
		got, err := c.Incr(ctx, "rate:user-1", time.Minute)
		if err != nil {
			t.Fatalf("Incr() returned error: %v", err)
		}
		if got != want {
			t.Errorf("Incr() = %d, want %d", got, want)
		}
	}
}

func TestMemoryIncrRestartsAfterWindowExpires(t *testing.T) {
	c := NewMemory(10)
	ctx := context.Background()
	_, _ = c.Incr(ctx, "rate:user-1", 20*time.Millisecond)

	time.Sleep(50 * time.Millisecond)

	got, _ := c.Incr(ctx, "rate:user-1", 20*time.Millisecond)
	if got != 1 {
		t.Errorf("Incr() after the window = %d, want the counter to restart at 1", got)
	}
}

func TestMemoryEvictsOldestWhenOverCapacity(t *testing.T) {
	c := NewMemory(3)
	ctx := context.Background()

	for i := range 5 {
		_ = c.Set(ctx, "k"+strconv.Itoa(i), []byte("v"), time.Minute)
	}

	if got := c.Len(); got > 3 {
		t.Errorf("cache holds %d entries, want at most the 3 it was built with", got)
	}
	if _, found, _ := c.Get(ctx, "k4"); !found {
		t.Error("the most recent entry was evicted")
	}
}

func TestMemoryIsSafeForConcurrentUse(t *testing.T) {
	c := NewMemory(100)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "k" + strconv.Itoa(i%10)
			_ = c.Set(ctx, key, []byte("v"), time.Minute)
			_, _, _ = c.Get(ctx, key)
			_, _ = c.Incr(ctx, "counter", time.Minute)
		}(i)
	}
	wg.Wait()
}

func TestMemoryCloseIsSafe(t *testing.T) {
	c := NewMemory(10)

	if err := c.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

func TestFloodingTheCacheCannotSilentlyResetALoginThrottle(t *testing.T) {
	// Under plain LRU an attacker could evict a victim's brute-force counter
	// by trying many other names (see ErrFull).
	c := NewMemory(4)
	ctx := context.Background()

	if _, err := c.Incr(ctx, "rl:login:victim", time.Minute); err != nil {
		t.Fatalf("Incr() = %v", err)
	}

	var lastErr error
	for i := range 20 {
		if _, err := c.Incr(ctx, fmt.Sprintf("rl:login:flood-%d", i), time.Minute); err != nil {
			lastErr = err
		}
	}

	if lastErr == nil {
		t.Fatal("the store absorbed every counter; something was evicted silently")
	}

	n, err := c.Incr(ctx, "rl:login:victim", time.Minute)
	if err != nil {
		t.Fatalf("Incr() on the victim = %v", err)
	}
	if n != 2 {
		t.Errorf("victim counter = %d, want 2: it was evicted and started over", n)
	}
}

func TestAFullStoreReclaimsWhatHasExpiredBeforeRefusing(t *testing.T) {
	c := NewMemory(2)
	ctx := context.Background()

	if _, err := c.Incr(ctx, "rl:old", time.Millisecond); err != nil {
		t.Fatalf("Incr() = %v", err)
	}
	if _, err := c.Incr(ctx, "rl:also-old", time.Millisecond); err != nil {
		t.Fatalf("Incr() = %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	if _, err := c.Incr(ctx, "rl:fresh", time.Minute); err != nil {
		t.Errorf("Incr() = %v, want the expired windows to make room", err)
	}
}

func TestSessionsStillMakeRoomForEachOther(t *testing.T) {
	// A store refusing new sessions once full would lock everyone out.
	c := NewMemory(2)
	ctx := context.Background()

	for _, key := range []string{"sess:a", "sess:b", "sess:c"} {
		if err := c.Set(ctx, key, []byte("x"), time.Minute); err != nil {
			t.Fatalf("Set(%s) = %v", key, err)
		}
	}

	if _, found, _ := c.Get(ctx, "sess:c"); !found {
		t.Error("the newest session was not stored")
	}
	if c.Len() > 2 {
		t.Errorf("Len() = %d, want the capacity to hold", c.Len())
	}
}

func TestReclaimingWalksABoundedPartOfTheStore(t *testing.T) {
	const capacity = reclaimScan * 4
	c := NewMemory(capacity)
	ctx := context.Background()

	for i := range capacity {
		if _, err := c.Incr(ctx, "rl:old-"+strconv.Itoa(i), time.Millisecond); err != nil {
			t.Fatalf("Incr() = %v", err)
		}
	}
	time.Sleep(5 * time.Millisecond)

	if _, err := c.Incr(ctx, "rl:fresh", time.Minute); err != nil {
		t.Fatalf("Incr() = %v, want the expired windows to make room", err)
	}

	// What is left is everything the walk did not reach.
	if left := c.Len(); left < capacity-reclaimScan {
		t.Errorf("Len() = %d after reclaiming, want at least %d: the walk swept the whole store",
			left, capacity-reclaimScan)
	}
}
