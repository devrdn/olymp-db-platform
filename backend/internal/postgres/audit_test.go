package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

// writeTrail writes one actor's two entries and one system entry, and returns
// the contest identifier the system entry names. The identifier is fresh each
// call because the database is shared and the trail is append-only.
func writeTrail(t *testing.T, ctx context.Context, actor uuid.UUID) string {
	t.Helper()

	contestID := "c-" + uuid.NewString()
	sink := NewAuditSink(testPool)
	for _, e := range []audit.Entry{
		{ActorID: &actor, Action: "user.create", Entity: "user", EntityID: "u-1", IP: "10.0.0.1"},
		{ActorID: &actor, Action: "user.block", Entity: "user", EntityID: "u-1"},
		{Action: "contest.status_change", Entity: "contest", EntityID: contestID,
			Payload: map[string]any{"to": "published"}},
	} {
		if err := sink.Append(ctx, e); err != nil {
			t.Fatalf("Append() = %v", err)
		}
	}
	return contestID
}

func TestTheTrailComesBackNewestFirst(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		actor := makeUser(t, ctx, "auditor-order")
		writeTrail(t, ctx, actor.ID)

		found, _, err := NewAuditTrail(testPool).List(ctx, audit.Filter{Limit: 3}.Normalize())
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if len(found) != 3 {
			t.Fatalf("listed %d entries, want 3", len(found))
		}
		for i := 1; i < len(found); i++ {
			if found[i-1].CreatedAt.Before(found[i].CreatedAt) {
				t.Errorf("entry %d is older than the one after it", i-1)
			}
		}
	})
}

func TestTheTrailNamesTheActorRatherThanItsIdentifier(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		actor := makeUser(t, ctx, "auditor-login")
		writeTrail(t, ctx, actor.ID)

		found, _, err := NewAuditTrail(testPool).List(ctx,
			audit.Filter{Actor: actor.ID, Limit: 10}.Normalize())
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if len(found) != 2 {
			t.Fatalf("listed %d entries for the actor, want 2", len(found))
		}
		for _, r := range found {
			if r.ActorLogin != "auditor-login" {
				t.Errorf("actor login = %q, want auditor-login", r.ActorLogin)
			}
		}
	})
}

func TestASystemEventHasNoActorAndSaysSo(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		actor := makeUser(t, ctx, "auditor-system")
		contestID := writeTrail(t, ctx, actor.ID)

		found, _, err := NewAuditTrail(testPool).List(ctx,
			audit.Filter{Action: "contest.status_change", Entity: "contest",
				EntityID: contestID, Limit: 10}.Normalize())
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if len(found) != 1 {
			t.Fatalf("listed %d entries, want 1", len(found))
		}
		if found[0].ActorID != nil || found[0].ActorLogin != "" {
			t.Errorf("a system event reported actor %v/%q, want neither",
				found[0].ActorID, found[0].ActorLogin)
		}
		if found[0].Payload["to"] != "published" {
			t.Errorf("payload = %v, want it read back", found[0].Payload)
		}
	})
}

func TestTheTrailFiltersToOneThing(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		actor := makeUser(t, ctx, "auditor-entity")
		writeTrail(t, ctx, actor.ID)

		found, total, err := NewAuditTrail(testPool).List(ctx,
			audit.Filter{Entity: "user", EntityID: "u-1", Limit: 10}.Normalize())
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if total != 2 || len(found) != 2 {
			t.Errorf("found %d of %d, want both entries about that user", len(found), total)
		}
	})
}

func TestTheTrailFiltersByTime(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		actor := makeUser(t, ctx, "auditor-time")
		writeTrail(t, ctx, actor.ID)
		tomorrow := time.Now().Add(24 * time.Hour)

		_, total, err := NewAuditTrail(testPool).List(ctx,
			audit.Filter{From: &tomorrow, Limit: 10}.Normalize())
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if total != 0 {
			t.Errorf("total = %d for a window that has not started, want 0", total)
		}
	})
}

func TestTheTotalCountsEveryMatchNotJustThePage(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		actor := makeUser(t, ctx, "auditor-total")
		writeTrail(t, ctx, actor.ID)

		found, total, err := NewAuditTrail(testPool).List(ctx,
			audit.Filter{Actor: actor.ID, Limit: 1}.Normalize())
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if len(found) != 1 || total != 2 {
			t.Errorf("page of %d with total %d, want 1 of 2", len(found), total)
		}
	})
}

func TestAPagePastTheEndStillCountsEveryMatch(t *testing.T) {
	// A total of zero would tell the panel the trail is empty.
	withTx(t, func(ctx context.Context) {
		actor := makeUser(t, ctx, "auditor-past-end")
		writeTrail(t, ctx, actor.ID)

		filter := audit.Filter{Actor: actor.ID, Limit: 1}.Normalize()
		filter.Offset = 5
		found, total, err := NewAuditTrail(testPool).List(ctx, filter)
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if len(found) != 0 || total != 2 {
			t.Errorf("page of %d with total %d, want 0 of 2", len(found), total)
		}
	})
}

func TestAFilterNoStoredEntryCanEqualFindsNothingRatherThanFailing(t *testing.T) {
	// A NUL byte or invalid UTF-8 would fail the statement.
	withTx(t, func(ctx context.Context) {
		actor := makeUser(t, ctx, "auditor-odd-filter")
		writeTrail(t, ctx, actor.ID)

		for _, f := range []audit.Filter{
			{Actor: actor.ID, Entity: "contest\x00"},
			{Actor: actor.ID, EntityID: "\xff"},
		} {
			found, total, err := NewAuditTrail(testPool).List(ctx, f.Normalize())
			if err != nil || len(found) != 0 || total != 0 {
				t.Errorf("List(%+v) = (%d, %d, %v), want nothing and no error", f, len(found), total, err)
			}
		}
	})
}

func TestTheTrailNamesWhatWasActedUpon(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "auditor-subject")
		contestID := makeContest(t, ctx, author.ID)
		if err := NewContests(testPool).ReplaceTranslations(ctx, contestID, []contests.Translation{
			{Lang: "en", Title: "Night in the archive"},
		}); err != nil {
			t.Fatalf("ReplaceTranslations() = %v", err)
		}
		if err := NewContests(testPool).ReplaceLanguages(ctx, contestID, []contests.ContestLanguage{
			{Code: "en", IsDefault: true},
		}); err != nil {
			t.Fatalf("ReplaceLanguages() = %v", err)
		}
		if err := NewAuditSink(testPool).Append(ctx, audit.Entry{
			ActorID: &author.ID, Action: "contest.answers_change",
			Entity: "contest", EntityID: contestID.String(),
		}); err != nil {
			t.Fatalf("Append() = %v", err)
		}

		found, _, err := NewAuditTrail(testPool).List(ctx,
			audit.Filter{Entity: "contest", EntityID: contestID.String(), Limit: 5}.Normalize())
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if len(found) != 1 {
			t.Fatalf("listed %d entries, want 1", len(found))
		}
		if found[0].EntityLabel != "Night in the archive" {
			t.Errorf("entity label = %q, want the contest's title", found[0].EntityLabel)
		}
	})
}

func TestTheTrailNamesAnAccountItWasAboutToo(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		actor := makeUser(t, ctx, "auditor-blocker")
		subject := makeUser(t, ctx, "s.popescu-blocked")
		if err := NewAuditSink(testPool).Append(ctx, audit.Entry{
			ActorID: &actor.ID, Action: "user.block",
			Entity: "user", EntityID: subject.ID.String(),
		}); err != nil {
			t.Fatalf("Append() = %v", err)
		}

		found, _, err := NewAuditTrail(testPool).List(ctx,
			audit.Filter{Entity: "user", EntityID: subject.ID.String(), Limit: 5}.Normalize())
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if len(found) != 1 || found[0].EntityLabel != "s.popescu-blocked" {
			t.Errorf("entity label = %q, want the subject's login", found[0].EntityLabel)
		}
	})
}

func TestSomethingSinceDeletedKeepsItsEntryWithoutAName(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		actor := makeUser(t, ctx, "auditor-gone")
		gone := uuid.New()
		if err := NewAuditSink(testPool).Append(ctx, audit.Entry{
			ActorID: &actor.ID, Action: "contest.delete",
			Entity: "contest", EntityID: gone.String(),
		}); err != nil {
			t.Fatalf("Append() = %v", err)
		}

		found, _, err := NewAuditTrail(testPool).List(ctx,
			audit.Filter{Entity: "contest", EntityID: gone.String(), Limit: 5}.Normalize())
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if len(found) != 1 {
			t.Fatalf("listed %d entries, want the entry to survive the deletion", len(found))
		}
		if found[0].EntityLabel != "" {
			t.Errorf("entity label = %q, want none for something that is gone", found[0].EntityLabel)
		}
		if found[0].EntityID != gone.String() {
			t.Errorf("entity id = %q, want it kept", found[0].EntityID)
		}
	})
}

func TestAppendManyWritesEveryEntryInOneStatement(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		actor := makeUser(t, ctx, "auditor-many")

		err := NewAuditSink(testPool).AppendMany(ctx, []audit.Entry{
			{ActorID: &actor.ID, Action: "user.block", Entity: "user", EntityID: "u-1", IP: "10.0.0.1",
				UserAgent: "Admin/1.0", Payload: map[string]any{"reason": "left"}},
			{Action: "contest.status_change", Entity: "contest", EntityID: "c-1"},
		})
		if err != nil {
			t.Fatalf("AppendMany() = %v", err)
		}

		found, total, err := NewAuditTrail(testPool).List(ctx, audit.Filter{Actor: actor.ID, Limit: 5}.Normalize())
		if err != nil {
			t.Fatalf("List() = %v", err)
		}
		if total != 1 || len(found) != 1 {
			t.Fatalf("found %d of %d entries for the actor, want 1 of 1", len(found), total)
		}
		if found[0].IP != "10.0.0.1" || found[0].UserAgent != "Admin/1.0" {
			t.Errorf("entry = %+v, want the ip and user agent kept", found[0])
		}
		if found[0].Payload["reason"] != "left" {
			t.Errorf("payload = %v, want it read back", found[0].Payload)
		}

		found, total, err = NewAuditTrail(testPool).List(ctx,
			audit.Filter{Entity: "contest", EntityID: "c-1", Limit: 5}.Normalize())
		if err != nil {
			t.Fatalf("List() for the system entry = %v", err)
		}
		if total != 1 || len(found) != 1 {
			t.Fatalf("found %d of %d entries for the contest, want 1 of 1", len(found), total)
		}
		if found[0].ActorID != nil {
			t.Errorf("actor = %v, want none for a system entry", found[0].ActorID)
		}
	})
}

func TestAppendManyDoesNothingForNoEntries(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		if err := NewAuditSink(testPool).AppendMany(ctx, nil); err != nil {
			t.Fatalf("AppendMany(nil) = %v", err)
		}
	})
}

func TestLatestStartBlockedFindsNothingForAContestWithNoEntries(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		problems, found, err := NewAuditTrail(testPool).LatestStartBlocked(ctx, uuid.New())
		if err != nil {
			t.Fatalf("LatestStartBlocked() = %v", err)
		}
		if found {
			t.Errorf("found = true with problems %v, want false for a contest with no audit history", problems)
		}
	})
}

func TestLatestStartBlockedReturnsTheNewestBlocksCodes(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contestID := uuid.New()
		sink := NewAuditSink(testPool)
		if err := sink.Append(ctx, audit.Entry{
			Action: "contest.start_blocked", Entity: "contest", EntityID: contestID.String(),
			Payload: map[string]any{"problems": []string{"no_story"}},
		}); err != nil {
			t.Fatalf("Append() first block = %v", err)
		}
		if err := sink.Append(ctx, audit.Entry{
			Action: "contest.start_blocked", Entity: "contest", EntityID: contestID.String(),
			Payload: map[string]any{"problems": []string{"no_questions"}},
		}); err != nil {
			t.Fatalf("Append() second block = %v", err)
		}

		problems, found, err := NewAuditTrail(testPool).LatestStartBlocked(ctx, contestID)
		if err != nil {
			t.Fatalf("LatestStartBlocked() = %v", err)
		}
		if !found {
			t.Fatal("found = false, want true — the contest has a start_blocked entry")
		}
		if len(problems) != 1 || problems[0] != "no_questions" {
			t.Errorf("problems = %v, want [no_questions] — the newest entry, not the first", problems)
		}
	})
}

// TestLatestStartBlockedFindsNothingOnceSomethingElseIsNewer: a newer entry
// of another kind means the next block is recorded afresh.
func TestLatestStartBlockedFindsNothingOnceSomethingElseIsNewer(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contestID := uuid.New()
		sink := NewAuditSink(testPool)
		if err := sink.Append(ctx, audit.Entry{
			Action: "contest.start_blocked", Entity: "contest", EntityID: contestID.String(),
			Payload: map[string]any{"problems": []string{"no_story"}},
		}); err != nil {
			t.Fatalf("Append() block = %v", err)
		}
		if err := sink.Append(ctx, audit.Entry{
			Action: "contest.status_change", Entity: "contest", EntityID: contestID.String(),
			Payload: map[string]any{"changes": map[string]any{"status": map[string]any{"from": "published", "to": "running"}}},
		}); err != nil {
			t.Fatalf("Append() status change = %v", err)
		}

		problems, found, err := NewAuditTrail(testPool).LatestStartBlocked(ctx, contestID)
		if err != nil {
			t.Fatalf("LatestStartBlocked() = %v", err)
		}
		if found {
			t.Errorf("found = true with problems %v, want false once the contest actually started", problems)
		}
	})
}

func TestAnEntityIdThatIsNotAnIdentifierDoesNotBreakTheQuery(t *testing.T) {
	// entity_id is unconstrained text; casting it in the join would fail the
	// whole page on one odd row.
	withTx(t, func(ctx context.Context) {
		actor := makeUser(t, ctx, "auditor-odd")
		if err := NewAuditSink(testPool).Append(ctx, audit.Entry{
			ActorID: &actor.ID, Action: "contest.update",
			Entity: "contest", EntityID: "not-a-uuid",
		}); err != nil {
			t.Fatalf("Append() = %v", err)
		}

		found, _, err := NewAuditTrail(testPool).List(ctx,
			audit.Filter{EntityID: "not-a-uuid", Limit: 5}.Normalize())
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if len(found) != 1 || found[0].EntityLabel != "" {
			t.Errorf("found %+v, want the entry with no label", found)
		}
	})
}
