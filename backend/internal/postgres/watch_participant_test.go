package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/google/uuid"
)

func TestWatchFindsAParticipantOnlyInItsOwnContest(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newWatchFixture(t, ctx)
		reg, user := f.participant("someone")
		other := newWatchFixture(t, ctx)
		watch := NewWatch(testPool)

		got, err := watch.Participant(ctx, f.contest, reg)
		if err != nil || got.Registration != reg || got.User != user || got.Contest != f.contest || got.Login == "" {
			t.Fatalf("Participant = %+v, %v", got, err)
		}
		for name, contest := range map[string]uuid.UUID{"another contest": other.contest, "no contest": uuid.New()} {
			if _, err := watch.Participant(ctx, contest, reg); !errors.Is(err, monitor.ErrParticipantNotFound) {
				t.Errorf("%s: %v, want ErrParticipantNotFound", name, err)
			}
		}
		if _, err := watch.Participant(ctx, f.contest, uuid.New()); !errors.Is(err, monitor.ErrParticipantNotFound) {
			t.Errorf("unknown registration: %v, want ErrParticipantNotFound", err)
		}
	})
}

func TestWatchQueriesPagesFiltersAndSearches(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newWatchFixture(t, ctx)
		reg, _ := f.participant("querier")
		neighbour, _ := f.participant("neighbour")
		long := "select " + strings.Repeat("x", queryrunner.MaxHistorySQLChars+10)
		f.query(reg, "SELECT * FROM suspects", "ok", "192.0.2.1", f.at(time.Minute))
		f.query(reg, "select 100% of it", "error", "192.0.2.1", f.at(2*time.Minute))
		f.query(reg, "drop table suspects", "rejected", "", f.at(3*time.Minute))
		f.query(reg, long, "ok", "2001:db8::1", f.at(4*time.Minute))
		f.query(neighbour, "select * from suspects", "ok", "", f.at(5*time.Minute))
		watch := NewWatch(testPool)
		read := func(q monitor.QueriesQuery) monitor.QueriesPage {
			t.Helper()
			q.Contest, q.Registration = f.contest, reg
			page, err := watch.Queries(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			return page
		}

		all := read(monitor.QueriesQuery{})
		if len(all.Items) != 4 || all.More {
			t.Fatalf("all: %d items, more %v", len(all.Items), all.More)
		}
		newest := all.Items[0]
		if newest.SQL != long || newest.SQLTruncated || newest.IP != "2001:db8::1" || newest.Status != "ok" ||
			newest.DurationMs == nil || *newest.DurationMs != 7 {
			t.Errorf("the newest query is not whole: %+v", newest.QueryData)
		}
		if all.Items[2].Error != "ERROR: boom (SQLSTATE 42703)" || all.Items[3].IP != "192.0.2.1" {
			t.Errorf("error or address missing: %+v / %+v", all.Items[2].QueryData, all.Items[3].QueryData)
		}

		// A failure of ours, not of their SQL, names the cluster; staff do
		// not see it.
		infra := f.query(neighbour, "select 6", "error", "", f.at(6*time.Minute))
		f.exec(`UPDATE query_log SET error_text = 'failed to connect to host=10.0.0.5 user=game_p1 database=game_c1' WHERE id = $1`, infra)
		neighbours, err := watch.Queries(ctx, monitor.QueriesQuery{Contest: f.contest, Registration: neighbour, Status: "error"})
		if err != nil || len(neighbours.Items) != 1 || neighbours.Items[0].Error != "" {
			t.Errorf("an infrastructure error reached the organiser: %+v, %v", neighbours.Items, err)
		}
		feed, err := watch.Feed(ctx, monitor.FeedQuery{Contest: f.contest, Registration: neighbour, Kinds: []string{monitor.FeedKindQuery}})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range feed.Items {
			if item.Data.(monitor.QueryData).Error != "" {
				t.Errorf("the feed shows an infrastructure error: %+v", item.Data)
			}
		}

		// Two by two, newest first, without a gap or a repeat.
		first := read(monitor.QueriesQuery{Limit: 2})
		cursor := first.Items[1].Cursor()
		second := read(monitor.QueriesQuery{Limit: 2, Before: &cursor})
		if !first.More || second.More || len(second.Items) != 2 ||
			second.Items[0].ID != all.Items[2].ID || second.Items[1].ID != all.Items[3].ID {
			t.Errorf("pages: %+v then %+v", first, second)
		}

		if got := read(monitor.QueriesQuery{Status: "rejected"}); len(got.Items) != 1 || got.Items[0].Status != "rejected" {
			t.Errorf("rejected only: %+v", got.Items)
		}
		if got := read(monitor.QueriesQuery{Search: "SUSPECTS"}); len(got.Items) != 2 {
			t.Errorf("search without case: %d items", len(got.Items))
		}
		// A percent sign is text, not a wildcard (CLAUDE.md rule 3).
		if got := read(monitor.QueriesQuery{Search: "100%"}); len(got.Items) != 1 {
			t.Errorf("search for a literal percent: %d items", len(got.Items))
		}
		if got := read(monitor.QueriesQuery{Search: "%"}); len(got.Items) != 1 {
			t.Errorf("a bare percent matched %d items, want the one containing it", len(got.Items))
		}
		if got := read(monitor.QueriesQuery{Search: `\`}); len(got.Items) != 0 {
			t.Errorf("a backslash matched %d items", len(got.Items))
		}
	})
}

func TestWatchAnswersCarryTheQueriesThatLedToThem(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newWatchFixture(t, ctx)
		second := f.makeQuestion(2)
		reg, _ := f.participant("answerer")
		neighbour, _ := f.participant("neighbour")

		f.query(reg, "select 1", "ok", "", f.at(1*time.Minute))
		f.query(reg, "select 2", "error", "", f.at(2*time.Minute))
		f.answer(reg, second, 1, false, f.at(3*time.Minute))
		f.query(neighbour, "select 99", "ok", "", f.at(4*time.Minute))
		f.answer(reg, f.question, 1, true, f.at(5*time.Minute)) // no query of theirs since the last answer
		f.query(reg, "select 3", "ok", "", f.at(6*time.Minute))
		f.query(reg, "select 4", "ok", "", f.at(7*time.Minute))
		f.answer(reg, second, 2, true, f.at(8*time.Minute))
		f.query(reg, "select after", "ok", "", f.at(9*time.Minute))

		answers, err := NewWatch(testPool).Answers(ctx, f.contest, reg, 10)
		if err != nil {
			t.Fatal(err)
		}
		if answers.Truncated || len(answers.Questions) != 2 {
			t.Fatalf("answers = %+v", answers)
		}
		q1, q2 := answers.Questions[0], answers.Questions[1]
		if q1.QuestionID != f.question || q2.QuestionID != second || len(q1.Attempts) != 1 || len(q2.Attempts) != 2 {
			t.Fatalf("grouping: %+v", answers.Questions)
		}
		sqls := func(a monitor.Attempt) []string {
			var out []string
			for _, q := range a.Queries {
				out = append(out, q.SQL)
			}
			return out
		}
		if got := sqls(q2.Attempts[0]); strings.Join(got, ",") != "select 1,select 2" {
			t.Errorf("first attempt ever: %v, want everything before it", got)
		}
		if got := sqls(q1.Attempts[0]); len(got) != 0 {
			t.Errorf("an answer after another with nothing between: %v", got)
		}
		if got := sqls(q2.Attempts[1]); strings.Join(got, ",") != "select 3,select 4" {
			t.Errorf("since the previous answer to any question: %v", got)
		}
		if !q2.Attempts[1].Correct || q2.Attempts[1].Value != "v2" || q2.Attempts[1].PointsAwarded != 10 {
			t.Errorf("attempt: %+v", q2.Attempts[1].AnswerData)
		}

		// The window is bounded and says how much it left out.
		bounded, err := NewWatch(testPool).Answers(ctx, f.contest, reg, 1)
		if err != nil {
			t.Fatal(err)
		}
		cut := bounded.Questions[1].Attempts[1]
		if len(cut.Queries) != 1 || cut.MoreQueries != 1 || cut.Queries[0].SQL != "select 3" {
			t.Errorf("bounded window: %v, %d more", sqls(cut), cut.MoreQueries)
		}
	})
}

func TestWatchWorkspaceListsRevisionsAndReadsOne(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newWatchFixture(t, ctx)
		reg, _ := f.participant("writer")
		stranger, _ := newWatchFixture(t, ctx).participant("stranger")
		store := NewWorkspace(testPool)
		if _, err := store.SaveNotes(ctx, reg, "my notes"); err != nil {
			t.Fatal(err)
		}
		tab, err := store.CreateTab(ctx, reg, 10, func([]string) string { return "Query 1" })
		if err != nil {
			t.Fatal(err)
		}
		f.exec(`UPDATE participant_sql_tabs SET body = 'select 1' WHERE id = $1`, tab.ID)
		monitorStore := NewMonitor(testPool)
		if err := monitorStore.RecordRevision(ctx, monitor.Revision{Registration: reg, Document: monitor.TabDocument(tab.ID),
			Title: "Query 1", Body: "select 1", At: f.at(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		watch := NewWatch(testPool)

		ws, err := watch.Workspace(ctx, reg)
		if err != nil {
			t.Fatal(err)
		}
		if ws.Notes.Body != "my notes" || ws.Notes.UpdatedAt == nil || len(ws.Tabs) != 1 || ws.Tabs[0].Body != "select 1" {
			t.Fatalf("workspace now: %+v", ws)
		}
		if len(ws.Revisions) != 2 || ws.Truncated {
			t.Fatalf("revisions: %+v", ws.Revisions)
		}
		newest := ws.Revisions[0]
		if newest.Document != tab.ID.String() || newest.Title != "Query 1" || newest.Size != len("select 1") {
			t.Errorf("newest revision: %+v", newest)
		}

		body, err := watch.Revision(ctx, reg, newest.ID)
		if err != nil || body.Body != "select 1" || body.ID != newest.ID {
			t.Errorf("revision = %+v, %v", body, err)
		}
		if _, err := watch.Revision(ctx, stranger, newest.ID); !errors.Is(err, monitor.ErrRevisionNotFound) {
			t.Errorf("another participant's revision: %v, want ErrRevisionNotFound", err)
		}

		// Looking creates nothing: an unopened workspace stays without tabs.
		empty, err := watch.Workspace(ctx, stranger)
		if err != nil || len(empty.Tabs) != 0 || empty.Notes.UpdatedAt != nil || len(empty.Revisions) != 0 {
			t.Errorf("an unopened workspace: %+v, %v", empty, err)
		}
	})
}
