package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/google/uuid"
)

func TestSchemaEndpointAnswersWithTheGamesShape(t *testing.T) {
	f := newParticipantFixture(t)
	contest := f.playContest(t)
	f.access.schema = provisioning.Schema{Tables: []provisioning.Table{
		{Name: "guests", Columns: []provisioning.Column{
			{Name: "id", Type: "uuid"},
			{Name: "room_id", Type: "uuid", Nullable: true, References: "rooms"},
		}},
	}}

	rec := f.get("/contests/" + contest.String() + "/play/schema")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	// Access is asked about the contest the URL names, and Schema is handed
	// the pair Access admitted.
	if f.access.gotContestID != contest {
		t.Fatalf("Access asked about %s, want the contest named in the URL", f.access.gotContestID)
	}
	if f.access.schemaAsked != contest || f.access.schemaFor != f.access.participant.ID {
		t.Fatalf("Schema handed %s/%s, want the admitted %s/%s",
			f.access.schemaAsked, f.access.schemaFor, contest, f.access.participant.ID)
	}

	var body struct {
		Tables []struct {
			Name    string `json:"name"`
			Columns []struct {
				Name       string `json:"name"`
				Type       string `json:"type"`
				Nullable   bool   `json:"nullable"`
				References string `json:"references"`
			} `json:"columns"`
		} `json:"tables"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v — %s", err, rec.Body)
	}
	if len(body.Tables) != 1 || body.Tables[0].Name != "guests" {
		t.Fatalf("body carries %+v", body.Tables)
	}
	// The two fields the panel actually draws with, and the two most likely
	// to be dropped by an `omitempty` somewhere on the way out.
	if !body.Tables[0].Columns[1].Nullable {
		t.Error("nullability did not reach the client")
	}
	if body.Tables[0].Columns[1].References != "rooms" {
		t.Error("the foreign key did not reach the client")
	}
}

// A client that has to tell `null` from `[]` before it can draw a tree is a
// client with a bug waiting.
func TestSchemaEndpointNeverAnswersWithANullList(t *testing.T) {
	f := newParticipantFixture(t)

	rec := f.get("/contests/" + uuid.NewString() + "/play/schema")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["tables"] == nil {
		t.Fatalf("tables came back null: %s", rec.Body)
	}
}

// CLAUDE.md rule 1: every refusal a service hands the HTTP layer is a
// sentinel the `fail` switch can name, so the client is told what happened
// rather than "internal error".
func TestSchemaEndpointNamesEveryRefusal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"the contest hides its schema", queryproxy.ErrSchemaHidden, http.StatusForbidden, "schema_hidden"},
		{"the game was never built", queryproxy.ErrNoGameYet, http.StatusConflict, "no_game_yet"},
		{"the game cluster is full", queryproxy.ErrNoRoomForDatabase, http.StatusServiceUnavailable, "game_cluster_full"},
		{"not a participant", contests.ErrNotAParticipant, http.StatusForbidden, "not_a_participant"},
		{"the contest is not running", contests.ErrContestNotRunning, http.StatusConflict, "contest_not_running"},
		{"the contest has ended", contests.ErrContestEnded, http.StatusConflict, "contest_ended"},
		{"the participant has finished", contests.ErrParticipantFinished, http.StatusConflict, "contest_finished"},
		{"the participant's time is up", contests.ErrDeadlinePassed, http.StatusConflict, "deadline_passed"},
		{"the address is not allowed", contests.ErrAddressNotAllowed, http.StatusForbidden, "address_not_allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newParticipantFixture(t)
			f.access.schemaErr = tc.err

			rec := f.get("/contests/" + uuid.NewString() + "/play/schema")
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v — %s", err, rec.Body)
			}
			if body.Error.Code != tc.code {
				t.Fatalf("code %q, want %q", body.Error.Code, tc.code)
			}
		})
	}
}

// The schema panel is a /play read like the story: admitted by the same
// Access, and observed like every other admitted request, so the tracker of
// address changes and parallel sessions hears of a registration that only
// ever looks at the schema.
func TestASchemaReadIsObserved(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := f.playContest(t)

	if rec := f.get("/contests/" + contestID.String() + "/play/schema"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	visits := f.watcher.seen()
	if len(visits) != 1 {
		t.Fatalf("the request was observed %d times, want once", len(visits))
	}
	if got := visits[0]; got.Contest != contestID || got.Registration != f.access.participant.ID {
		t.Fatalf("visit filed under %v/%v, want %v/%v", got.Contest, got.Registration, contestID, f.access.participant.ID)
	}
}

// Admission refuses before the schema is asked for anything: the game is not
// looked up, no database is ensured, and nothing is observed. The refusal is
// the gate's own, answered from the shared table.
func TestARefusedAdmissionNeverReachesTheSchema(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"not a participant", contests.ErrNotAParticipant, http.StatusForbidden, "not_a_participant"},
		{"the contest is not running", contests.ErrContestNotRunning, http.StatusConflict, "contest_not_running"},
		{"the contest has ended", contests.ErrContestEnded, http.StatusConflict, "contest_ended"},
		{"the participant has finished", contests.ErrParticipantFinished, http.StatusConflict, "contest_finished"},
		{"the participant's time is up", contests.ErrDeadlinePassed, http.StatusConflict, "deadline_passed"},
		{"the address is not allowed", contests.ErrAddressNotAllowed, http.StatusForbidden, "address_not_allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newParticipantFixture(t)
			contestID := f.playContest(t)
			f.access.err = tc.err

			rec := f.get("/contests/" + contestID.String() + "/play/schema")
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v — %s", err, rec.Body)
			}
			if body.Error.Code != tc.code {
				t.Fatalf("code %q, want %q", body.Error.Code, tc.code)
			}
			if f.access.schemaAsked != uuid.Nil {
				t.Fatal("the schema was asked for after admission refused the request")
			}
			if visits := f.watcher.seen(); len(visits) != 0 {
				t.Fatalf("a refused request was observed: %+v", visits)
			}
		})
	}
}

// The rate budget is spent before the contest is even looked up, the same
// order every other read on this handler uses.
func TestSchemaEndpointIsRateLimitedBeforeItLooksAnythingUp(t *testing.T) {
	f := newParticipantFixture(t)
	f.access.admitReadErr = queryrunner.ErrTooManyQueries

	rec := f.get("/contests/" + uuid.NewString() + "/play/schema")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429: %s", rec.Code, rec.Body)
	}
	if f.access.schemaAsked != uuid.Nil {
		t.Fatal("the schema was read despite the read budget refusing the request")
	}
}

func TestSchemaEndpointRefusesAContestIdentifierThatIsNotAUUID(t *testing.T) {
	f := newParticipantFixture(t)

	rec := f.get("/contests/not-a-uuid/play/schema")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body)
	}
	if f.access.schemaAsked != uuid.Nil {
		t.Fatal("looked the schema up for an identifier that never parsed")
	}
}

// The journal is written before anything sanitises anything, so reading the
// column back was a way round both of the console's guards at once.
func TestTheQueryLogNeverHandsBackAFailureOfOurs(t *testing.T) {
	f := newParticipantFixture(t)
	f.history.items = []queryrunner.HistoryEntry{
		{
			SQL:    "select * from guests;",
			Status: queryrunner.StatusError,
			Error: "connecting to the game database: failed to connect to `user=game_reader " +
				"database=game_pool_ce678159661a1_57c5ba38100c`: 127.0.0.1:5433 (localhost): " +
				`failed SASL auth: FATAL: password authentication failed for user "game_reader"`,
			ExecutedAt: time.Now().UTC(),
		},
	}

	rec := f.get("/contests/" + uuid.NewString() + "/play/log?limit=10&offset=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	for _, secret := range []string{"game_reader", "game_pool_ce678159661a1", "5433", "password authentication"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("%q reached the participant through the query log: %s", secret, rec.Body)
		}
	}
	// The fact still travels: the status is what the panel renders.
	if !strings.Contains(rec.Body.String(), `"status":"error"`) {
		t.Fatalf("the row lost its status as well: %s", rec.Body)
	}
}

// The same reading, in a contest that hides its schema: the console
// deliberately withholds PostgreSQL's own words so a participant cannot
// enumerate a closed catalogue one guess at a time. Reading them back out of
// the log restored exactly that oracle.
func TestTheQueryLogIsNotAnOracleForAHiddenSchema(t *testing.T) {
	f := newParticipantFixture(t)
	f.history.items = []queryrunner.HistoryEntry{
		{
			SQL:        "select * from suspects;",
			Status:     queryrunner.StatusError,
			Error:      `ERROR: relation "suspects" does not exist (SQLSTATE 42P01)`,
			ExecutedAt: time.Now().UTC(),
		},
	}

	rec := f.get("/contests/" + uuid.NewString() + "/play/log?limit=10&offset=0")
	if strings.Contains(rec.Body.String(), "does not exist") || strings.Contains(rec.Body.String(), "suspects\"") {
		t.Fatalf("the database's own words reached the participant: %s", rec.Body)
	}
}

// And the one text that is theirs still arrives. A refusal from the SQL
// validator is about the query they typed, and "syntax error at or near" is
// the most useful thing they can be told.
func TestTheQueryLogStillShowsARefusalOfTheirOwnQuery(t *testing.T) {
	f := newParticipantFixture(t)
	f.history.items = []queryrunner.HistoryEntry{
		{
			SQL:        "fksdf;",
			Status:     queryrunner.StatusRejected,
			Error:      `parse_error: syntax error at or near "fksdf"`,
			ExecutedAt: time.Now().UTC(),
		},
	}

	rec := f.get("/contests/" + uuid.NewString() + "/play/log?limit=10&offset=0")
	if !strings.Contains(rec.Body.String(), "syntax error at or near") {
		t.Fatalf("the participant's own refusal was withheld too: %s", rec.Body)
	}
}
