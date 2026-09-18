package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
)

// feedFixture is a contest whose two participants did one of everything,
// several of it in the very same instant across sources and within one, and
// a participant of another contest whose history must never show.
type feedFixture struct {
	*watchFixture
	alice, bob uuid.UUID
}

func newFeedFixture(t *testing.T, ctx context.Context) feedFixture {
	t.Helper()
	f := newWatchFixture(t, ctx)
	alice, aliceUser := f.participant("alice")
	bob, bobUser := f.participant("bob")
	// Registered an hour before the base time, so their sign-ins count.
	f.exec(`UPDATE registrations SET created_at = $2 WHERE contest_id = $1`, f.contest, f.at(-time.Hour))

	same := f.at(10 * time.Minute)
	f.exec(`UPDATE registrations SET started_at = $2 WHERE id = $1`, alice, f.at(time.Minute))
	f.exec(`UPDATE registrations SET started_at = $2, finished_at = $3, status = 'finished' WHERE id = $1`,
		bob, f.at(time.Minute), same)
	f.exec(`INSERT INTO audit_log (actor_id, action, entity, entity_id, ip, user_agent, created_at)
	        VALUES ($1::uuid, 'auth.login', 'user', $1::uuid::text, '192.0.2.1', 'Firefox', $2),
	               ($3::uuid, 'auth.login', 'user', $3::uuid::text, '192.0.2.2', 'Chrome', $4),
	               ($1::uuid, 'auth.logout', 'user', $1::uuid::text, NULL, NULL, $5),
	               ($1::uuid, 'auth.login', 'user', $1::uuid::text, NULL, NULL, $6)`,
		aliceUser, f.at(0), bobUser, same, f.at(20*time.Minute), f.at(-2*time.Hour))
	var login string
	if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx, `SELECT login FROM users WHERE id = $1`, aliceUser).Scan(&login); err != nil {
		t.Fatal(err)
	}
	f.exec(`INSERT INTO audit_log (action, entity, payload, ip, created_at)
	        VALUES ('auth.login_failed', 'user', jsonb_build_object('login', upper($1::text), 'reason', 'wrong_password'), '192.0.2.9', $2)`,
		login, f.at(30*time.Second))
	f.exec(`INSERT INTO audit_log (actor_id, action, entity, entity_id, payload, created_at)
	        VALUES ($1, 'participant.disqualify', 'contest', $2::uuid::text, jsonb_build_object('user_id', $3::uuid::text), $4)`,
		aliceUser, f.contest, bobUser, f.at(25*time.Minute))

	f.query(alice, "select 1", "ok", "192.0.2.1", f.at(2*time.Minute))
	f.query(alice, "select 2", "ok", "192.0.2.1", same)
	f.query(bob, "select 3", "error", "192.0.2.2", same)
	f.query(bob, "select 4", "ok", "192.0.2.2", same)
	f.answer(alice, f.question, 1, false, same)
	f.answer(bob, f.question, 1, true, same)
	f.event(alice, monitor.PageLeft{AwayMs: 5000}, same)
	f.event(bob, monitor.Paste{Target: monitor.PasteEditor, Chars: 3, Text: "abc"}, same)
	f.event(alice, monitor.TabCreated{TabID: uuid.New(), Title: "Query 2"}, f.at(15*time.Minute))

	// Another contest, the same instant: never in this contest's feed.
	other := newWatchFixture(t, ctx)
	stranger, _ := other.participant("stranger")
	other.query(stranger, "select 5", "ok", "", same)
	other.event(stranger, monitor.PageLeft{AwayMs: 5000}, same)
	return feedFixture{watchFixture: f, alice: alice, bob: bob}
}

func readFeed(t *testing.T, ctx context.Context, q monitor.FeedQuery) monitor.FeedPage {
	t.Helper()
	page, err := NewWatch(testPool).Feed(ctx, q)
	if err != nil {
		t.Fatalf("feed %+v: %v", q, err)
	}
	return page
}

func kindsOf(items []monitor.FeedItem) []string {
	kinds := make([]string, len(items))
	for i, item := range items {
		kinds[i] = item.Kind
	}
	return kinds
}

func TestTheFeedMergesEverySourceInTimeOrder(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newFeedFixture(t, ctx)
		page := readFeed(t, ctx, monitor.FeedQuery{Contest: f.contest, Limit: monitor.MaxFeedPage})
		if page.More {
			t.Error("the whole feed fits a page, but More is set")
		}
		want := []string{
			monitor.FeedSignIn, monitor.FeedSignInFailed, monitor.FeedStarted, monitor.FeedStarted, monitor.FeedKindQuery,
			// The same instant: audit, then events, queries, answers, finish.
			monitor.FeedSignIn, string(monitor.KindPageLeft), string(monitor.KindPaste),
			monitor.FeedKindQuery, monitor.FeedKindQuery, monitor.FeedKindQuery,
			monitor.FeedKindAnswer, monitor.FeedKindAnswer, monitor.FeedFinished,
			string(monitor.KindTabCreated), monitor.FeedSignOut, monitor.FeedDisqualified,
		}
		got := kindsOf(page.Items)
		if len(got) != len(want) {
			t.Fatalf("kinds = %v,\nwant %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("kinds = %v,\nwant %v", got, want)
			}
		}
		for i := 1; i < len(page.Items); i++ {
			if page.Items[i-1].Cursor().Compare(page.Items[i].Cursor()) >= 0 {
				t.Errorf("items %d and %d are out of order", i-1, i)
			}
		}
		for _, item := range page.Items {
			if item.Login == "" {
				t.Errorf("item %s of %s has no login", item.Kind, item.Registration)
			}
		}
		failed := page.Items[1].Data.(monitor.AuditData)
		if failed.Reason != "wrong_password" || failed.IP != "192.0.2.9" || page.Items[1].Registration != f.alice {
			t.Errorf("failed sign-in = %+v for %s", failed, page.Items[1].Registration)
		}
		if page.Items[len(page.Items)-1].Registration != f.bob {
			t.Error("the disqualification is not attributed to the disqualified participant")
		}
	})
}

// Paging through the feed a few items at a time, forwards and backwards,
// meets every item exactly once, whatever falls on a page boundary.
func TestTheFeedPagesWithoutGapsOrRepeats(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newFeedFixture(t, ctx)
		whole := readFeed(t, ctx, monitor.FeedQuery{Contest: f.contest, Limit: monitor.MaxFeedPage}).Items

		for _, size := range []int{1, 2, 3, 5} {
			// Forwards from before everything.
			var forwards []monitor.FeedItem
			cursor := monitor.Cursor{At: f.at(-24 * time.Hour), Source: monitor.SourceAudit, ID: "0"}
			for range 100 {
				page := readFeed(t, ctx, monitor.FeedQuery{Contest: f.contest, After: &cursor, Limit: size})
				forwards = append(forwards, page.Items...)
				if len(page.Items) == 0 {
					break
				}
				cursor = page.Items[len(page.Items)-1].Cursor()
				if !page.More {
					break
				}
			}
			// Backwards from the newest page.
			var backwards []monitor.FeedItem
			page := readFeed(t, ctx, monitor.FeedQuery{Contest: f.contest, Limit: size})
			for range 100 {
				backwards = append(append([]monitor.FeedItem{}, page.Items...), backwards...)
				if !page.More {
					break
				}
				before := page.Items[0].Cursor()
				page = readFeed(t, ctx, monitor.FeedQuery{Contest: f.contest, Before: &before, Limit: size})
			}
			for name, got := range map[string][]monitor.FeedItem{"forwards": forwards, "backwards": backwards} {
				if len(got) != len(whole) {
					t.Fatalf("page size %d %s: %d items, want %d", size, name, len(got), len(whole))
				}
				for i := range whole {
					if got[i].Cursor().Compare(whole[i].Cursor()) != 0 {
						t.Fatalf("page size %d %s: item %d is %s, want %s", size, name, i, got[i].Kind, whole[i].Kind)
					}
				}
			}
		}
	})
}

func TestTheFeedFilters(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newFeedFixture(t, ctx)
		all := func(q monitor.FeedQuery) []monitor.FeedItem {
			q.Contest, q.Limit = f.contest, monitor.MaxFeedPage
			return readFeed(t, ctx, q).Items
		}

		queries := all(monitor.FeedQuery{Kinds: []string{monitor.FeedKindQuery}})
		if len(queries) != 4 {
			t.Errorf("queries only: %v", kindsOf(queries))
		}
		mixed := all(monitor.FeedQuery{Kinds: []string{monitor.FeedSignOut, string(monitor.KindPaste)}})
		if len(mixed) != 2 || mixed[0].Kind != string(monitor.KindPaste) || mixed[1].Kind != monitor.FeedSignOut {
			t.Errorf("paste and sign-out: %v", kindsOf(mixed))
		}

		bobs := all(monitor.FeedQuery{Registration: f.bob})
		for _, item := range bobs {
			if item.Registration != f.bob {
				t.Errorf("bob's feed carries %s of %s", item.Kind, item.Registration)
			}
		}
		if len(bobs) != 8 {
			t.Errorf("bob's feed: %v", kindsOf(bobs))
		}

		ranged := all(monitor.FeedQuery{From: f.at(10 * time.Minute), Until: f.at(20 * time.Minute)})
		for _, item := range ranged {
			if item.At.Before(f.at(10*time.Minute)) || !item.At.Before(f.at(20*time.Minute)) {
				t.Errorf("%s at %v is outside the range", item.Kind, item.At)
			}
		}
		if len(ranged) != 10 {
			t.Errorf("ranged: %v", kindsOf(ranged))
		}

		// Another contest's registration is nobody here.
		strangers := all(monitor.FeedQuery{Registration: uuid.New()})
		if len(strangers) != 0 {
			t.Errorf("an unknown registration's feed: %v", kindsOf(strangers))
		}
	})
}

// An item stamped before one already delivered, but written after it — two
// transactions committing out of the order of their clocks — still reaches a
// feed polled forwards: the newest FeedSettle of the journals is held back
// until it can no longer change.
func TestPollingForwardsLosesNoItemCommittedLate(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newWatchFixture(t, ctx)
		reg, _ := f.participant("late")
		origin := monitor.Cursor{At: monitor.EarliestCursorTime, Source: monitor.SourceAudit, ID: "0"}
		insert := func(ago time.Duration) {
			f.exec(`INSERT INTO participant_events (contest_id, registration_id, kind, payload, created_at)
			        VALUES ($1, $2, 'page_left', '{"away_ms": 2000}', clock_timestamp() - $3::interval)`,
				f.contest, reg, ago.String())
		}
		poll := func() []monitor.FeedItem {
			return readFeed(t, ctx, monitor.FeedQuery{Contest: f.contest, After: &origin, Limit: 10}).Items
		}

		insert(monitor.FeedSettle / 4)
		if got := poll(); len(got) != 0 {
			t.Fatalf("an item younger than the settle window was delivered: %v", kindsOf(got))
		}
		insert(monitor.FeedSettle / 2) // stamped earlier, written later
		time.Sleep(monitor.FeedSettle + 100*time.Millisecond)
		if got := poll(); len(got) != 2 {
			t.Fatalf("after the settle window: %d items, want both", len(got))
		}
	})
}

func TestAStreamedFeedIsTheWholeFeedOnce(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newFeedFixture(t, ctx)
		whole := readFeed(t, ctx, monitor.FeedQuery{Contest: f.contest, Limit: monitor.MaxFeedPage}).Items
		for _, reg := range []uuid.UUID{uuid.Nil, f.bob} {
			var got []monitor.FeedItem
			if err := monitor.StreamFeed(ctx, NewWatch(testPool), monitor.FeedQuery{Contest: f.contest, Registration: reg}, time.Now(),
				func(item monitor.FeedItem) error { got = append(got, item); return nil }); err != nil {
				t.Fatal(err)
			}
			want := whole
			if reg != uuid.Nil {
				want = nil
				for _, item := range whole {
					if item.Registration == reg {
						want = append(want, item)
					}
				}
			}
			if len(got) != len(want) {
				t.Fatalf("registration %s: streamed %v, want %v", reg, kindsOf(got), kindsOf(want))
			}
			for i := range want {
				if got[i].Cursor().Compare(want[i].Cursor()) != 0 || got[i].Login == "" {
					t.Fatalf("item %d: %+v, want %+v", i, got[i], want[i])
				}
			}
		}
	})
}

// A participant's sign-ins belong to the contest only while it could
// concern them: from their registration to an hour past the contest's end,
// or past their own finish.
func TestSignInsOutsideTheContestAreNotInItsFeed(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newWatchFixture(t, ctx)
		early, earlyUser := f.participant("early")
		_, lateUser := f.participant("late")
		f.exec(`UPDATE registrations SET created_at = $2 WHERE contest_id = $1`, f.contest, f.at(-time.Hour))
		f.exec(`UPDATE contests SET starts_at = $2, ends_at = $3 WHERE id = $1`, f.contest, f.at(0), f.at(3*time.Hour))
		f.exec(`UPDATE registrations SET started_at = $2, finished_at = $3, status = 'finished' WHERE id = $1`,
			early, f.at(0), f.at(time.Hour))
		var login string
		if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx, `SELECT login FROM users WHERE id = $1`, lateUser).Scan(&login); err != nil {
			t.Fatal(err)
		}
		signIn := func(user uuid.UUID, at time.Time) {
			f.exec(`INSERT INTO audit_log (actor_id, action, entity, entity_id, created_at)
			        VALUES ($1::uuid, 'auth.login', 'user', $1::uuid::text, $2)`, user, at)
		}
		signIn(earlyUser, f.at(90*time.Minute))            // within an hour of their own finish
		signIn(earlyUser, f.at(2*time.Hour+time.Minute))   // past it, though the contest runs
		signIn(lateUser, f.at(3*time.Hour+30*time.Minute)) // within an hour of the end
		signIn(lateUser, f.at(5*time.Hour))                // hours after
		f.exec(`INSERT INTO audit_log (action, entity, payload, created_at)
		        VALUES ('auth.login_failed', 'user', jsonb_build_object('login', $1::text), $2)`, login, f.at(6*time.Hour))

		items := readFeed(t, ctx, monitor.FeedQuery{Contest: f.contest, Limit: monitor.MaxFeedPage,
			Kinds: []string{monitor.FeedSignIn, monitor.FeedSignInFailed}}).Items
		var at []time.Time
		for _, item := range items {
			at = append(at, item.At)
		}
		if len(items) != 2 || !at[0].Equal(f.at(90*time.Minute)) || !at[1].Equal(f.at(3*time.Hour+30*time.Minute)) {
			t.Errorf("sign-ins shown at %v, want only the two inside the window", at)
		}
	})
}

// One participant's disqualification is found however many of the others'
// come after it: the limit applies to theirs alone.
func TestATimelineFindsItsDisqualificationAmongOthers(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newWatchFixture(t, ctx)
		mine, myUser := f.participant("mine")
		disqualify := func(user uuid.UUID, at time.Time) {
			f.exec(`INSERT INTO audit_log (action, entity, entity_id, payload, created_at)
			        VALUES ('participant.disqualify', 'contest', $1::uuid::text, jsonb_build_object('user_id', $2::uuid::text), $3)`,
				f.contest, user, at)
		}
		disqualify(myUser, f.at(time.Minute))
		for i := range 5 {
			_, other := f.participant("other")
			disqualify(other, f.at(time.Duration(2+i)*time.Minute))
		}
		items := readFeed(t, ctx, monitor.FeedQuery{Contest: f.contest, Registration: mine,
			Kinds: []string{monitor.FeedDisqualified}, Limit: 1}).Items
		if len(items) != 1 || items[0].Registration != mine {
			t.Errorf("the timeline's disqualification: %+v", items)
		}
	})
}

// journalRowsRead is how many rows this transaction has read from a table so
// far, by index or by scan.
func journalRowsRead(t *testing.T, ctx context.Context, table string) int64 {
	t.Helper()
	var n int64
	if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx, `
		SELECT COALESCE(idx_tup_fetch, 0) + COALESCE(seq_tup_read, 0)
		FROM pg_stat_xact_user_tables WHERE relname = $1`, table).Scan(&n); err != nil {
		t.Fatalf("read the statistics of %s: %v", table, err)
	}
	return n
}

// A contest-wide export reads each journal row about once, however many
// pages the file runs to: the cost grows with the contest, not its square.
func TestAContestExportReadsEachJournalRowOnce(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newWatchFixture(t, ctx)
		loadOlympiad(t, f, 7, 40) // 12,000 queries, 1,200 answers, 2,400 sign-ins
		for _, table := range []string{"users", "registrations", "query_log", "submissions", "audit_log", "participant_events"} {
			f.exec("ANALYZE " + table)
		}
		tables := []string{"query_log", "submissions", "audit_log"}
		before := map[string]int64{}
		for _, table := range tables {
			before[table] = journalRowsRead(t, ctx, table)
		}
		streamed := 0
		if err := monitor.StreamFeed(ctx, NewWatch(testPool), monitor.FeedQuery{Contest: f.contest}, time.Now(),
			func(monitor.FeedItem) error { streamed++; return nil }); err != nil {
			t.Fatal(err)
		}
		if streamed < 12_000 {
			t.Fatalf("streamed %d items, want the whole contest", streamed)
		}
		for table, rows := range map[string]int64{"query_log": 12_000, "submissions": 1_200, "audit_log": 2_400} {
			read := journalRowsRead(t, ctx, table) - before[table]
			// Linear with room for the planner's choices (a bitmap scan may
			// fetch a page's rows again); the quadratic read this replaced
			// was twenty-seven times the rows here, and grows with them.
			if read > 4*rows {
				t.Errorf("%s: %d rows read for %d rows exported, want at most four times as many", table, read, rows)
			}
		}
	})
}

// The newest page holds back the settle window too: the cursor a live screen
// starts polling from must not already be past an item still committing.
func TestTheNewestPageHoldsBackTheSettleWindow(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newWatchFixture(t, ctx)
		reg, _ := f.participant("fresh")
		f.exec(`INSERT INTO participant_events (contest_id, registration_id, kind, payload, created_at)
		        VALUES ($1, $2, 'page_left', '{"away_ms": 2000}', clock_timestamp() - $3::interval)`,
			f.contest, reg, (monitor.FeedSettle / 4).String())
		if got := readFeed(t, ctx, monitor.FeedQuery{Contest: f.contest}).Items; len(got) != 0 {
			t.Errorf("the newest page carries an item younger than the settle window: %v", kindsOf(got))
		}
	})
}

// Under individual timing a participant who started but never finished is
// bounded by their own deadline (contests.Deadline): the start plus the
// duration, or the contest's end when that comes first.
func TestAnIndividualParticipantsSignInsEndWithTheirDeadline(t *testing.T) {
	for name, endsAt := range map[string]*time.Duration{"no end": nil, "a distant end": ptrDuration(10 * time.Hour)} {
		t.Run(name, func(t *testing.T) {
			withTx(t, func(ctx context.Context) {
				f := newWatchFixture(t, ctx)
				reg, user := f.participant("solo")
				f.exec(`UPDATE contests SET timing = 'individual', duration_min = 60, starts_at = $2 WHERE id = $1`, f.contest, f.at(-time.Hour))
				if endsAt != nil {
					f.exec(`UPDATE contests SET ends_at = $2 WHERE id = $1`, f.contest, f.at(*endsAt))
				}
				f.exec(`UPDATE registrations SET created_at = $2, started_at = $3, status = 'active' WHERE id = $1`,
					reg, f.at(-time.Hour), f.at(0))
				for _, at := range []time.Duration{90 * time.Minute, 2*time.Hour + time.Minute} {
					f.exec(`INSERT INTO audit_log (actor_id, action, entity, entity_id, created_at)
					        VALUES ($1::uuid, 'auth.login', 'user', $1::uuid::text, $2)`, user, f.at(at))
				}
				items := readFeed(t, ctx, monitor.FeedQuery{Contest: f.contest, Kinds: []string{monitor.FeedSignIn}}).Items
				if len(items) != 1 || !items[0].At.Equal(f.at(90*time.Minute)) {
					var at []time.Time
					for _, item := range items {
						at = append(at, item.At)
					}
					t.Errorf("sign-ins shown at %v, want only the one within an hour of the deadline", at)
				}
			})
		})
	}
}

func ptrDuration(d time.Duration) *time.Duration { return &d }
