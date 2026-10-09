package monitor_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/google/uuid"
)

// trailCache is the tracker's cache in memory, counting every call so a test
// can prove how many round trips a request cost.
type trailCache struct {
	mu      sync.Mutex
	values  map[string][]byte
	ttls    map[string]time.Duration
	gets    int
	sets    int
	failGet error
	failSet error
	block   bool
}

func newTrailCache() *trailCache {
	return &trailCache{values: map[string][]byte{}, ttls: map[string]time.Duration{}}
}

func (c *trailCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	c.gets++
	block, fail := c.block, c.failGet
	value, ok := c.values[key]
	c.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, false, ctx.Err()
	}
	if fail != nil {
		return nil, false, fail
	}
	return value, ok, nil
}

func (c *trailCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sets++
	if c.failSet != nil {
		return c.failSet
	}
	c.values[key] = value
	c.ttls[key] = ttl
	return nil
}

func (c *trailCache) counts() (gets, sets int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gets, c.sets
}

// eventLog records the events the tracker wrote.
type eventLog struct {
	mu      sync.Mutex
	events  []monitor.Event
	inserts int
	fail    error
}

func (l *eventLog) InsertEvents(_ context.Context, events []monitor.Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inserts++
	if l.fail != nil {
		return l.fail
	}
	l.events = append(l.events, events...)
	return nil
}

func (l *eventLog) all() []monitor.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]monitor.Event(nil), l.events...)
}

// sessionBook answers whether a tracked session is still live beside the
// current one: live unless a test ended it.
type sessionBook struct {
	mu    sync.Mutex
	ended map[string]bool
	asked int
	fail  error
}

func (b *sessionBook) SessionAlive(_ context.Context, tracked, _ string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.asked++
	if b.fail != nil {
		return false, b.fail
	}
	return !b.ended[tracked], nil
}

func (b *sessionBook) end(tag string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ended == nil {
		b.ended = map[string]bool{}
	}
	b.ended[tag] = true
}

type trackerRig struct {
	tracker  *monitor.Tracker
	cache    *trailCache
	events   *eventLog
	sessions *sessionBook
	now      time.Time
	visit    monitor.Visit
}

func newTrackerRig(t *testing.T) *trackerRig {
	t.Helper()
	rig := &trackerRig{
		cache:    newTrailCache(),
		events:   &eventLog{},
		sessions: &sessionBook{},
		now:      time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC),
		visit: monitor.Visit{
			Contest:      uuid.New(),
			Registration: uuid.New(),
			Address:      netip.MustParseAddr("10.0.0.1"),
			Session:      monitor.SessionTag("session-a"),
			UserAgent:    "Firefox",
		},
	}
	rig.tracker = monitor.NewTracker(rig.cache, rig.events, rig.sessions, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithClock(func() time.Time { return rig.now })
	return rig
}

// observe records one request at the rig's current time, from visit.
func (r *trackerRig) observe(visit monitor.Visit) {
	r.tracker.Observe(context.Background(), visit)
}

func (r *trackerRig) from(address, session string) monitor.Visit {
	visit := r.visit
	visit.Address = netip.MustParseAddr(address)
	visit.Session = monitor.SessionTag(session)
	return visit
}

func (r *trackerRig) kinds() []monitor.Kind {
	var kinds []monitor.Kind
	for _, event := range r.events.all() {
		kinds = append(kinds, event.Kind())
	}
	return kinds
}

func TestTheFirstRequestOfARegistrationIsNotAChange(t *testing.T) {
	rig := newTrackerRig(t)
	rig.observe(rig.visit)
	if got := rig.kinds(); len(got) != 0 {
		t.Fatalf("the first request wrote %v, want nothing", got)
	}
	if gets, sets := rig.cache.counts(); gets != 1 || sets != 1 {
		t.Fatalf("the first request cost %d reads and %d writes, want 1 and 1", gets, sets)
	}
}

func TestAnAddressChangeIsOneEventAndTheSameAddressAgainIsNone(t *testing.T) {
	rig := newTrackerRig(t)
	rig.observe(rig.from("10.0.0.1", "session-a"))
	rig.now = rig.now.Add(time.Second)
	rig.observe(rig.from("10.0.0.2", "session-a"))
	rig.now = rig.now.Add(time.Second)
	rig.observe(rig.from("10.0.0.2", "session-a"))
	rig.observe(rig.from("10.0.0.2", "session-a"))

	events := rig.events.all()
	if len(events) != 1 {
		t.Fatalf("events = %v, want one ip_changed", rig.kinds())
	}
	got, ok := events[0].Payload.(monitor.IPChanged)
	if !ok {
		t.Fatalf("payload = %#v, want IPChanged", events[0].Payload)
	}
	if got.From != netip.MustParseAddr("10.0.0.1") || got.To != netip.MustParseAddr("10.0.0.2") {
		t.Fatalf("ip_changed = %+v, want 10.0.0.1 → 10.0.0.2", got)
	}
	if events[0].Contest != rig.visit.Contest || events[0].Registration != rig.visit.Registration {
		t.Fatalf("the event is filed under %v/%v", events[0].Contest, events[0].Registration)
	}
}

// Rule 6: while nothing changes, a request reads the cache once and writes
// nothing anywhere.
func TestAnUnchangedRequestReadsOnceAndWritesNothing(t *testing.T) {
	rig := newTrackerRig(t)
	rig.observe(rig.visit)
	gets, sets := rig.cache.counts()

	rig.now = rig.now.Add(monitor.SeenRefresh - time.Millisecond)
	rig.observe(rig.visit)
	afterGets, afterSets := rig.cache.counts()
	if afterGets-gets != 1 || afterSets != sets {
		t.Fatalf("an unchanged request cost %d reads and %d writes, want 1 and 0", afterGets-gets, afterSets-sets)
	}
	if rig.events.inserts != 0 {
		t.Fatalf("an unchanged request inserted events %d times", rig.events.inserts)
	}

	// Once the session's last sighting is old enough to matter, it is moved
	// forward: one cache write, still no event.
	rig.now = rig.now.Add(time.Millisecond)
	rig.observe(rig.visit)
	if _, finalSets := rig.cache.counts(); finalSets != sets+1 {
		t.Fatalf("a request after SeenRefresh wrote the cache %d times, want 1", finalSets-sets)
	}
	if rig.events.inserts != 0 {
		t.Fatalf("refreshing the sighting inserted events")
	}
}

func TestASecondSessionInsideTheWindowIsReported(t *testing.T) {
	rig := newTrackerRig(t)
	rig.observe(rig.from("10.0.0.1", "session-a"))
	rig.now = rig.now.Add(monitor.ParallelWindow - time.Second)
	intruder := rig.from("10.0.0.9", "session-b")
	intruder.UserAgent = strings.Repeat("u", 300)
	rig.observe(intruder)

	events := rig.events.all()
	if len(events) != 1 {
		t.Fatalf("events = %v, want one parallel_session", rig.kinds())
	}
	// The bounds are the storage's to apply (monitor.Event.Normalize, which
	// internal/postgres calls on every event); what reaches it must pass.
	stored := normalized(t, events[0])
	got, ok := stored.Payload.(monitor.ParallelSession)
	if !ok {
		t.Fatalf("payload = %#v, want ParallelSession", stored.Payload)
	}
	if got.OtherIP != netip.MustParseAddr("10.0.0.9") {
		t.Fatalf("other_ip = %v, want the second session's address", got.OtherIP)
	}
	if len([]rune(got.UserAgent)) != monitor.MaxUserAgentRunes {
		t.Fatalf("user_agent is %d characters, want it cut to %d", len([]rune(got.UserAgent)), monitor.MaxUserAgentRunes)
	}
}

// The parallel session is reported as such, not also as the registration's
// address changing: the first session is still the one the address is
// tracked for.
func TestAParallelSessionDoesNotMoveTheAddress(t *testing.T) {
	rig := newTrackerRig(t)
	rig.observe(rig.from("10.0.0.1", "session-a"))
	rig.now = rig.now.Add(time.Second)
	rig.observe(rig.from("10.0.0.9", "session-b"))
	rig.now = rig.now.Add(time.Second)
	rig.observe(rig.from("10.0.0.1", "session-a"))

	if got := rig.kinds(); len(got) != 1 || got[0] != monitor.KindParallelSession {
		t.Fatalf("events = %v, want only parallel_session", got)
	}
}

func TestASecondSessionAfterTheWindowTakesOver(t *testing.T) {
	rig := newTrackerRig(t)
	rig.observe(rig.from("10.0.0.1", "session-a"))
	rig.now = rig.now.Add(monitor.ParallelWindow)
	rig.observe(rig.from("10.0.0.1", "session-b"))
	if got := rig.kinds(); len(got) != 0 {
		t.Fatalf("a new session after the first went quiet wrote %v, want nothing", got)
	}

	// The new session is now the one tracked: the old one coming back while
	// it is active is the parallel one.
	rig.now = rig.now.Add(time.Second)
	rig.observe(rig.from("10.0.0.1", "session-a"))
	if got := rig.kinds(); len(got) != 1 || got[0] != monitor.KindParallelSession {
		t.Fatalf("events = %v, want one parallel_session", got)
	}
}

// A new session from a new address after the old one went quiet is a
// participant who moved: the address change is what is reported.
func TestASessionTakingOverFromAnotherAddressIsAnAddressChange(t *testing.T) {
	rig := newTrackerRig(t)
	rig.observe(rig.from("10.0.0.1", "session-a"))
	rig.now = rig.now.Add(10 * time.Minute)
	rig.observe(rig.from("10.0.0.2", "session-b"))
	if got := rig.kinds(); len(got) != 1 || got[0] != monitor.KindIPChanged {
		t.Fatalf("events = %v, want one ip_changed", got)
	}
}

func TestTheSamePairIsReportedAtMostOnceInTenMinutes(t *testing.T) {
	rig := newTrackerRig(t)
	start := rig.now
	// Session A stays active throughout; session B keeps asking.
	for at := time.Duration(0); at <= monitor.ParallelReportEvery+time.Minute; at += 30 * time.Second {
		rig.now = start.Add(at)
		rig.observe(rig.from("10.0.0.1", "session-a"))
		rig.observe(rig.from("10.0.0.9", "session-b"))
	}
	events := rig.events.all()
	if len(events) != 2 {
		t.Fatalf("over %s the pair was reported %d times, want 2 (at the start and after %s)",
			monitor.ParallelReportEvery+time.Minute, len(events), monitor.ParallelReportEvery)
	}

	// A third session is a different pair and is reported at once.
	rig.observe(rig.from("10.0.0.7", "session-c"))
	if got := len(rig.events.all()); got != 3 {
		t.Fatalf("a third session made %d events in all, want 3", got)
	}
}

// The pair is the same whichever of its two sessions is currently tracked.
func TestThePairIsTheSameEitherWayRound(t *testing.T) {
	rig := newTrackerRig(t)
	rig.observe(rig.from("10.0.0.1", "session-a"))
	rig.now = rig.now.Add(time.Second)
	rig.observe(rig.from("10.0.0.1", "session-b")) // reported
	// A goes quiet, B takes over, then A comes back while B is active.
	rig.now = rig.now.Add(monitor.ParallelWindow + time.Second)
	rig.observe(rig.from("10.0.0.1", "session-b"))
	rig.now = rig.now.Add(time.Second)
	rig.observe(rig.from("10.0.0.1", "session-a"))
	if got := rig.kinds(); len(got) != 1 {
		t.Fatalf("events = %v, want the pair reported once", got)
	}
}

// The signal is best-effort: a cache that cannot be read, or that hangs,
// costs the request at most ObserveTimeout and never an error.
func TestAnUnreadableCacheIsSkipped(t *testing.T) {
	rig := newTrackerRig(t)
	rig.cache.failGet = errors.New("redis is down")
	rig.observe(rig.visit)
	if _, sets := rig.cache.counts(); sets != 0 || rig.events.inserts != 0 {
		t.Fatalf("an unreadable cache still wrote: %d sets, %d inserts", sets, rig.events.inserts)
	}
}

func TestAHangingCacheCostsAtMostTheTimeout(t *testing.T) {
	rig := newTrackerRig(t)
	rig.cache.block = true
	started := time.Now()
	rig.observe(rig.visit)
	if took := time.Since(started); took > 2*monitor.ObserveTimeout {
		t.Fatalf("Observe took %s against a hanging cache, want about %s", took, monitor.ObserveTimeout)
	}
}

// An event that cannot be stored does not keep the change unrecorded in the
// cache: otherwise every later request would try, and fail, again.
func TestAFailedInsertStillMovesTheTrailOn(t *testing.T) {
	rig := newTrackerRig(t)
	rig.observe(rig.from("10.0.0.1", "session-a"))
	rig.events.fail = errors.New("database is down")
	rig.now = rig.now.Add(time.Second)
	rig.observe(rig.from("10.0.0.2", "session-a"))
	rig.observe(rig.from("10.0.0.2", "session-a"))
	if rig.events.inserts != 1 {
		t.Fatalf("the failed change was attempted %d times, want 1", rig.events.inserts)
	}
}

// Without an address or a session there is nothing to compare, and nothing
// is read.
func TestAVisitWithoutAnAddressOrASessionIsNotTracked(t *testing.T) {
	rig := newTrackerRig(t)
	noAddress := rig.visit
	noAddress.Address = netip.Addr{}
	noSession := rig.visit
	noSession.Session = ""
	rig.observe(noAddress)
	rig.observe(noSession)
	if gets, _ := rig.cache.counts(); gets != 0 {
		t.Fatalf("an untrackable visit read the cache %d times", gets)
	}
}

// Rule 5: one key per registration, however many sessions and addresses
// touch it, and it expires.
func TestTheTrailIsOneKeyPerRegistration(t *testing.T) {
	rig := newTrackerRig(t)
	for i := range 20 {
		rig.now = rig.now.Add(time.Second)
		rig.observe(rig.from(netip.AddrFrom4([4]byte{10, 0, 1, byte(i)}).String(), "session-"+string(rune('a'+i))))
	}
	if len(rig.cache.values) != 1 {
		t.Fatalf("one registration occupies %d keys, want 1", len(rig.cache.values))
	}
	for key, ttl := range rig.cache.ttls {
		if ttl != monitor.TrailTTL {
			t.Fatalf("%s is kept for %s, want %s", key, ttl, monitor.TrailTTL)
		}
		if !strings.Contains(key, rig.visit.Registration.String()) {
			t.Fatalf("key %q does not name the registration", key)
		}
	}
	// And the pairs remembered for the ten-minute rule stay bounded too.
	for _, value := range rig.cache.values {
		if len(value) > 2048 {
			t.Fatalf("the trail grew to %d bytes over 20 sessions", len(value))
		}
	}
}

func TestTheSessionTagIsAHashNotTheToken(t *testing.T) {
	token := "super-secret-session-token"
	tag := monitor.SessionTag(token)
	if tag == "" || strings.Contains(tag, token) {
		t.Fatalf("SessionTag(%q) = %q", token, tag)
	}
	if monitor.SessionTag(token) != tag || monitor.SessionTag(token+"x") == tag {
		t.Fatal("SessionTag is not a stable function of the token")
	}
	if monitor.SessionTag("") != "" {
		t.Fatal("no token must give no tag")
	}
}

// BenchmarkObserveUnchanged is what tracking adds to every console query in
// the common case — same session, same address — over the in-process cache:
// one read and one decode, no write.
func BenchmarkObserveUnchanged(b *testing.B) {
	store := cache.NewMemory(0)
	b.Cleanup(func() { _ = store.Close() })
	events := &eventLog{}
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	tracker := monitor.NewTracker(store, events, &sessionBook{}, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithClock(func() time.Time { return now })
	visit := monitor.Visit{
		Contest: uuid.New(), Registration: uuid.New(),
		Address: netip.MustParseAddr("10.0.0.1"), Session: monitor.SessionTag("token"), UserAgent: "Firefox",
	}
	ctx := context.Background()
	tracker.Observe(ctx, visit)

	b.ReportAllocs()
	for b.Loop() {
		tracker.Observe(ctx, visit)
	}
	if events.inserts != 0 {
		b.Fatalf("an unchanged visit inserted events")
	}
}

// A session that ended — signed out, past its lifetime, retired — is not a
// second device: the next session takes over silently, even inside the
// window. A false "second device" on an honest participant is the costliest
// mistake this signal can make.
func TestASessionThatEndedIsNotAParallelOne(t *testing.T) {
	rig := newTrackerRig(t)
	rig.observe(rig.from("10.0.0.1", "session-a"))
	rig.sessions.end(monitor.SessionTag("session-a"))
	rig.now = rig.now.Add(30 * time.Second)
	rig.observe(rig.from("10.0.0.1", "session-b"))
	if got := rig.kinds(); len(got) != 0 {
		t.Fatalf("a new session after the old one ended wrote %v, want nothing", got)
	}
	// And the new one is tracked now: a third session while it is live is
	// the parallel one.
	rig.now = rig.now.Add(time.Second)
	rig.observe(rig.from("10.0.0.3", "session-c"))
	if got := rig.kinds(); len(got) != 1 || got[0] != monitor.KindParallelSession {
		t.Fatalf("events = %v, want one parallel_session", got)
	}
}

// Whether the tracked session is alive is asked only when the sessions
// differ: the common request costs one cache read and nothing more.
func TestLivenessIsAskedOnlyWhenTheSessionsDiffer(t *testing.T) {
	rig := newTrackerRig(t)
	for range 5 {
		rig.now = rig.now.Add(time.Second)
		rig.observe(rig.visit)
	}
	if rig.sessions.asked != 0 {
		t.Fatalf("an unchanged session asked about liveness %d times", rig.sessions.asked)
	}
	rig.observe(rig.from("10.0.0.1", "session-b"))
	if rig.sessions.asked != 1 {
		t.Fatalf("a different session asked %d times, want 1", rig.sessions.asked)
	}
}

// Past the window nobody asks: the old session went quiet either way.
func TestLivenessIsNotAskedPastTheWindow(t *testing.T) {
	rig := newTrackerRig(t)
	rig.observe(rig.from("10.0.0.1", "session-a"))
	rig.now = rig.now.Add(monitor.ParallelWindow)
	rig.observe(rig.from("10.0.0.1", "session-b"))
	if rig.sessions.asked != 0 {
		t.Fatalf("liveness asked %d times past the window", rig.sessions.asked)
	}
}

// When liveness cannot be answered, nothing is reported and nothing moves:
// a guess either way would be wrong half the time.
func TestAnUnanswerableLivenessReportsNothing(t *testing.T) {
	rig := newTrackerRig(t)
	rig.observe(rig.from("10.0.0.1", "session-a"))
	_, sets := rig.cache.counts()
	rig.sessions.fail = errors.New("redis is down")
	rig.now = rig.now.Add(time.Second)
	rig.observe(rig.from("10.0.0.9", "session-b"))
	if got := rig.kinds(); len(got) != 0 {
		t.Fatalf("events = %v, want nothing", got)
	}
	if _, after := rig.cache.counts(); after != sets {
		t.Fatalf("the trail was written %d times", after-sets)
	}
}

// A dual-stack browser or a NAT with several exits flips between addresses
// on every request. The same pair of addresses is reported at most once in
// ParallelReportEvery, and the tracked address still follows the requests.
func TestAnAddressFlippingBackAndForthIsReportedOncePerPair(t *testing.T) {
	rig := newTrackerRig(t)
	v4, v6 := "10.0.0.1", "2001:db8::1"
	start := rig.now
	rig.observe(rig.from(v4, "session-a"))
	for i := 1; i <= 40; i++ {
		rig.now = start.Add(time.Duration(i) * 10 * time.Second)
		address := v6
		if i%2 == 0 {
			address = v4
		}
		rig.observe(rig.from(address, "session-a"))
	}
	if got := rig.kinds(); len(got) != 1 {
		t.Fatalf("40 flips within %s wrote %d events, want 1", 400*time.Second, len(got))
	}

	// The tracked address followed: a third address is reported from the
	// last one seen (v4), and at once, being a new pair.
	rig.now = rig.now.Add(time.Second)
	rig.observe(rig.from("10.0.0.7", "session-a"))
	events := rig.events.all()
	if len(events) != 2 {
		t.Fatalf("a new address wrote %d events in all, want 2", len(events))
	}
	if got := events[1].Payload.(monitor.IPChanged); got.From != netip.MustParseAddr(v4) {
		t.Fatalf("ip_changed from %v, want the last address seen (%s)", got.From, v4)
	}

	// After ten minutes the first pair is due again; the pair just
	// reported (v4 and the third address) is not.
	rig.now = start.Add(monitor.ParallelReportEvery + time.Minute)
	rig.observe(rig.from(v4, "session-a"))
	rig.now = rig.now.Add(time.Second)
	rig.observe(rig.from(v6, "session-a"))
	if got := rig.kinds(); len(got) != 3 || got[2] != monitor.KindIPChanged {
		t.Fatalf("after ten minutes the events are %v, want a third ip_changed only", got)
	}
}

// slowCache stands in for Redis across a network: every call costs a round
// trip before the in-process store answers it.
type slowCache struct {
	monitor.TrailCache
	rtt   time.Duration
	calls int
}

func (c *slowCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	c.calls++
	time.Sleep(c.rtt)
	return c.TrailCache.Get(ctx, key)
}

func (c *slowCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	c.calls++
	time.Sleep(c.rtt)
	return c.TrailCache.Set(ctx, key, value, ttl)
}

// BenchmarkObserveUnchangedOverANetwork is the same request against a cache
// a round trip away (250 µs, a loaded local network): the added cost is one
// round trip, because the unchanged path makes exactly one call.
func BenchmarkObserveUnchangedOverANetwork(b *testing.B) {
	store := cache.NewMemory(0)
	b.Cleanup(func() { _ = store.Close() })
	slow := &slowCache{TrailCache: store, rtt: 250 * time.Microsecond}
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	tracker := monitor.NewTracker(slow, &eventLog{}, &sessionBook{}, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithClock(func() time.Time { return now })
	visit := monitor.Visit{
		Contest: uuid.New(), Registration: uuid.New(),
		Address: netip.MustParseAddr("10.0.0.1"), Session: monitor.SessionTag("token"),
	}
	ctx := context.Background()
	tracker.Observe(ctx, visit)
	slow.calls = 0

	for b.Loop() {
		tracker.Observe(ctx, visit)
	}
	b.ReportMetric(float64(slow.calls)/float64(b.N), "cache-calls/op")
}

// BenchmarkObserveUnchangedOnRedis is the same request against a real Redis,
// which is what the deployment's cache is when REDIS_ADDR is set: the
// simulated round trip above, measured. Skipped unless MONITOR_BENCH_REDIS is
// the address of a Redis this benchmark may write to (it writes only keys
// named after fresh identifiers, and leaves them for their own expiry).
func BenchmarkObserveUnchangedOnRedis(b *testing.B) {
	addr := os.Getenv("MONITOR_BENCH_REDIS")
	if addr == "" {
		b.Skip("set MONITOR_BENCH_REDIS to a Redis address to measure against it")
	}
	ctx := context.Background()
	store, err := cache.NewRedis(ctx, addr)
	if err != nil {
		b.Fatalf("NewRedis(%s) = %v", addr, err)
	}
	b.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	tracker := monitor.NewTracker(store, &eventLog{}, &sessionBook{}, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithClock(func() time.Time { return now })
	visit := monitor.Visit{
		Contest: uuid.New(), Registration: uuid.New(),
		Address: netip.MustParseAddr("10.0.0.1"), Session: monitor.SessionTag("token"), UserAgent: "Firefox",
	}
	tracker.Observe(ctx, visit)

	b.ReportAllocs()
	for b.Loop() {
		tracker.Observe(ctx, visit)
	}
}

// A genuine parallel session already reported does not pay for liveness on
// every request after: the pair is not due, so nobody asks.
func TestAReportedPairIsNotAskedAboutAgainUntilItIsDue(t *testing.T) {
	rig := newTrackerRig(t)
	rig.observe(rig.from("10.0.0.1", "session-a"))
	for range 5 {
		rig.now = rig.now.Add(5 * time.Second)
		rig.observe(rig.from("10.0.0.9", "session-b"))
	}
	if got := rig.kinds(); len(got) != 1 {
		t.Fatalf("events = %v, want one parallel_session", got)
	}
	if rig.sessions.asked != 1 {
		t.Fatalf("liveness asked %d times for one reported pair, want 1", rig.sessions.asked)
	}
}
