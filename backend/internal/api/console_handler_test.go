package api_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// The validator's vocabulary and the API's are two lists, and the interface
// chooses its sentence from the second. A refusal with no code of its own
// would reach a participant as whatever the interface says about an answer it
// cannot read — during a contest, about a query that may have been perfectly
// reasonable.
//
// This walks the first list rather than a copy of it, so adding a refusal
// without a code fails here instead of there.
func TestEveryRefusalHasACodeOfItsOwn(t *testing.T) {
	for _, code := range sqlpolicy.Codes() {
		t.Run(string(code), func(t *testing.T) {
			if !api.HasRefusalCode(code) {
				t.Fatalf("%q has no API code, so a participant meeting it is told nothing", code)
			}
		})
	}
}

// fakeConsole answers with whatever a test asked for, so the handler's own
// mapping from an error to a status and a code can be exercised without
// wiring the whole façade behind it.
type fakeConsole struct {
	result *queryrunner.Result
	err    error
}

func (c fakeConsole) Run(context.Context, queryproxy.Command) (*queryrunner.Result, error) {
	return c.result, c.err
}

// consoleFixture mounts the console endpoint behind a session for one
// authenticated participant.
type consoleFixture struct {
	router http.Handler
	cookie *http.Cookie
}

func newConsoleFixture(t *testing.T, console api.Console) *consoleFixture {
	t.Helper()

	userRepo := userstest.New()
	actor := userRepo.Add(users.User{Login: "participant", FullName: "Participant", Status: users.StatusActive})

	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })

	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: actor.ID, Login: actor.Login})
	if err != nil {
		t.Fatalf("session Create() returned error: %v", err)
	}

	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: userRepo,
		Authorizer: rbac.New(noRoles{}),
		Cookies:    auth.NewCookieWriter(false), Logger: log,
	})

	router := chi.NewRouter()
	api.NewConsoleHandler(console, mw, log).Mount(router)

	return &consoleFixture{
		router: router,
		cookie: &http.Cookie{Name: auth.SessionCookieName, Value: token},
	}
}

func (f *consoleFixture) run(sql string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost,
		"/contests/"+uuid.New().String()+"/query", strings.NewReader(`{"sql":`+strconvQuote(sql)+`}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// strconvQuote is a tiny JSON string literal, good enough for the plain SQL
// these tests send — a dedicated encoder would be answering a question this
// package's real JSON decoding already covers elsewhere.
func strconvQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// CLAUDE.md's security rule 1: every refusal the console can answer with
// needs a declared sentinel, a mapping in fail(), and a test asserting the
// 4xx. This is that test, for the length bound queryproxy now enforces
// before a query ever reaches the journal.
func TestAQueryOverTheLengthBoundIsA400(t *testing.T) {
	fixture := newConsoleFixture(t, fakeConsole{
		err: &sqlpolicy.Refusal{Code: sqlpolicy.CodeTooLong, Subject: "70000 bytes"},
	})

	rec := fixture.run("SELECT 1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "query_too_long" {
		t.Fatalf("code = %q, want %q", code, "query_too_long")
	}
}

// A syntax error carries a position so the console can point at the
// character rather than making a participant count them under a timer — the
// position pg_query's own C parser reported, not a value this handler
// invents.
func TestAParseErrorCarriesThePositionInTheText(t *testing.T) {
	fixture := newConsoleFixture(t, fakeConsole{
		err: &sqlpolicy.Refusal{Code: sqlpolicy.CodeParseError, Subject: `syntax error at or near "FRO"`, Position: 15},
	})

	rec := fixture.run("SELECT * FRO suspects")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	body := decode(t, rec)
	position, ok := body["position"].(float64)
	if !ok || int(position) != 15 {
		t.Fatalf("position = %v, want 15 (body: %s)", body["position"], rec.Body.String())
	}
}

// A refusal that names no position — every code but a parse error — must not
// invent one: zero is a real character offset (the first one), so a client
// distinguishing "no position" from "the first character" needs the key
// absent, not present and zero.
func TestARefusalWithNoPositionCarriesNoPositionField(t *testing.T) {
	fixture := newConsoleFixture(t, fakeConsole{
		err: &sqlpolicy.Refusal{Code: sqlpolicy.CodeTooLong, Subject: "70000 bytes"},
	})

	rec := fixture.run("SELECT 1")
	body := decode(t, rec)
	if _, present := body["position"]; present {
		t.Fatalf("a refusal with no position sent one anyway: %s", rec.Body.String())
	}
}

// A participant asking faster than the contest allows meets the same code
// whether the runner or this façade's own pre-check caught them — the two are
// the same limit, checked in two places, and the participant should not be
// able to tell which one refused.
func TestAQueryRefusedForItsRateIsA429(t *testing.T) {
	fixture := newConsoleFixture(t, fakeConsole{err: queryrunner.ErrTooManyQueries})

	rec := fixture.run("SELECT 1")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusTooManyRequests, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "query_too_often" {
		t.Fatalf("code = %q, want %q", code, "query_too_often")
	}
}

// CLAUDE.md's security rule 1 again, this time for the sentinel
// queryproxy.Service now returns for a deadline computed by contests.Deadline
// (docs/ARCHITECTURE.md §8) as much as for a contest whose status alone says
// it is not running — the façade does not distinguish the two to its caller,
// and this handler must still turn either into the same 409 a participant
// can act on.
func TestAParticipantPastTheirDeadlineGetsA409(t *testing.T) {
	fixture := newConsoleFixture(t, fakeConsole{err: queryproxy.ErrContestNotRunning})

	rec := fixture.run("SELECT 1")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "contest_not_running" {
		t.Fatalf("code = %q, want %q", code, "contest_not_running")
	}
}

// A game cluster at its configured disk budget has to reach the participant as
// its own sentence. It used to reach them as a 500 with "internal error": the
// refusal had no sentinel at all, because nothing on the participant's own path
// asked whether there was room before making them a database (CLAUDE.md rule
// 1). A 503 rather than a 500 because nothing is broken — an operator raising
// GAME_CLUSTER_MAX_BYTES or reclaiming a finished olympiad clears it — and
// rather than a 409 because it is a fact about the installation and not about
// the contest or the query.
func TestAFullGameClusterIsA503WithItsOwnCode(t *testing.T) {
	fixture := newConsoleFixture(t, fakeConsole{
		err: fmt.Errorf("%w: %w", queryproxy.ErrNoRoomForDatabase, provisioning.ErrClusterFull),
	})

	rec := fixture.run("SELECT 1")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "game_cluster_full" {
		t.Fatalf("code = %q, want %q", code, "game_cluster_full")
	}
}

// A journal that could not be opened is ours, not the participant's SQL being
// wrong, and the finding this closes is exactly a raw database error reaching
// the client as a 400 for it. It gets the same 500 the query service being
// unreachable gets, and none of the database's own words.
func TestAJournalFailureIsA500NotARawDatabaseError(t *testing.T) {
	underlying := errors.New(`ERROR: string is too long for tsvector (SQLSTATE 54001)`)
	fixture := newConsoleFixture(t, fakeConsole{
		err: fmt.Errorf("%w: %w", queryrunner.ErrJournalUnavailable, underlying),
	})

	rec := fixture.run("SELECT 1")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "tsvector") {
		t.Fatalf("the database's own words reached the client: %s", rec.Body.String())
	}
}

// CLAUDE.md's security rule 1 again, for the refusal that closes the console
// once nothing of the contest is still answerable. A 409 and a code of its
// own: the interface has to be able to say "there is nothing left to run a
// query for" rather than "you are not allowed here", and it chooses that
// sentence by the code alone.
func TestAConsoleWithNothingLeftToAnswerIsA409(t *testing.T) {
	fixture := newConsoleFixture(t, fakeConsole{err: queryproxy.ErrNothingLeftToAnswer})

	rec := fixture.run("SELECT 1")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "nothing_left_to_answer" {
		t.Fatalf("code = %q, want %q", code, "nothing_left_to_answer")
	}
}

// And it is not the same answer as a participant who has actually finished:
// finishing closes the whole play screen, this closes one panel of it, and a
// client that cannot tell them apart shows the wrong screen to one of them.
func TestNothingLeftToAnswerIsNotTheSameCodeAsHavingFinished(t *testing.T) {
	nothingLeft := errorCode(t, newConsoleFixture(t, fakeConsole{err: queryproxy.ErrNothingLeftToAnswer}).run("SELECT 1"))
	finished := errorCode(t, newConsoleFixture(t, fakeConsole{err: queryproxy.ErrFinished}).run("SELECT 1"))

	if nothingLeft == finished {
		t.Fatalf("both answered %q — the interface cannot tell a closed console from a closed contest", nothingLeft)
	}
}

// The console draws a column's type under its name and a meter under the
// editor, and neither exists unless this handler puts them in the body. The
// runner resolved both; this is the last boundary they have to cross
// (CLAUDE.md rule 11).
func TestTheAnswerCarriesTheColumnTypesAndTheDuration(t *testing.T) {
	fixture := newConsoleFixture(t, fakeConsole{result: &queryrunner.Result{
		Columns:     []string{"full_name", "at"},
		ColumnTypes: []string{"text", "timestamp with time zone"},
		Rows:        [][]any{{"Margot Feilhaber", "2024-11-09T00:14:22Z"}},
		Duration:    38 * time.Millisecond,
	}})

	rec := fixture.run("SELECT full_name, at FROM keycard_events")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	body := decode(t, rec)
	types, ok := body["column_types"].([]any)
	if !ok {
		t.Fatalf("column_types missing or not a list: %s", rec.Body.String())
	}
	want := []string{"text", "timestamp with time zone"}
	if len(types) != len(want) {
		t.Fatalf("column_types = %v, want %d of them", types, len(want))
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("column_types[%d] = %v, want %q", i, types[i], want[i])
		}
	}

	// Microseconds, because the meter rounds to milliseconds for display and a
	// duration already rounded here would show every quick query as "0 ms".
	micros, ok := body["duration_micros"].(float64)
	if !ok {
		t.Fatalf("duration_micros missing or not a number: %s", rec.Body.String())
	}
	if int64(micros) != (38 * time.Millisecond).Microseconds() {
		t.Fatalf("duration_micros = %v, want %d", micros, (38 * time.Millisecond).Microseconds())
	}
}

// The handler's own promise: never `null` for a list. A client that has to
// tell `null` from `[]` before it can draw a table is a client with a bug
// waiting, and the promise now covers three lists rather than two.
func TestAnEmptyAnswerCarriesEmptyListsAndNotNulls(t *testing.T) {
	// What a write with no RETURNING clause produces: a count, and nothing to
	// draw a table out of.
	fixture := newConsoleFixture(t, fakeConsole{result: &queryrunner.Result{RowsAffected: 3}})

	rec := fixture.run("UPDATE evidence SET note = 'seen' WHERE id < 4")
	body := decode(t, rec)

	for _, list := range []string{"columns", "column_types", "rows"} {
		value, present := body[list]
		if !present {
			t.Fatalf("%s is missing entirely: %s", list, rec.Body.String())
		}
		if value == nil {
			t.Fatalf("%s was sent as null: %s", list, rec.Body.String())
		}
		if _, isList := value.([]any); !isList {
			t.Fatalf("%s = %#v, want a list", list, value)
		}
	}
}

// The finding this closes, on the path it was found on: the Query Runner
// could not connect to the game cluster, the failure was classified as the
// database refusing the query, and the participant was handed the cluster's
// host, port, role name and their own database's internal name — under
// "check the fields you filled in", for a query that was fine.
//
// Asserted on the body, because the body is what left the building. A log
// line saying the right thing while the response says the wrong one is the
// defect, not the fix.
func TestAFailureOfOursNeverReachesTheParticipantsBody(t *testing.T) {
	// Not wrapped in any sentinel: this is the fallthrough, which is where an
	// error nobody anticipated lands.
	fixture := newConsoleFixture(t, fakeConsole{err: errors.New(
		"connecting to the game database: failed to connect to `user=game_reader " +
			"database=game_pool_ce678159661a1_57c5ba38100c`: 127.0.0.1:5433 (localhost): " +
			`failed SASL auth: FATAL: password authentication failed for user "game_reader" (SQLSTATE 28P01)`)})

	rec := fixture.run("select * from guests;")

	for _, secret := range []string{"game_reader", "game_pool_ce678159661a1", "5433", "password authentication"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("%q reached the participant: %s", secret, rec.Body.String())
		}
	}
	// And it is ours, not theirs: a 400 tells a client to stop retrying and a
	// participant to fix a query that was never wrong.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
}

// And the one thing that is safe to repeat still is. PostgreSQL naming the
// relation that does not exist is the most useful sentence anybody can send
// back, and it goes out because the error says it came from the database —
// not because the handler ran out of cases.
func TestTheDatabasesOwnWordsStillReachTheParticipant(t *testing.T) {
	fixture := newConsoleFixture(t, fakeConsole{
		err: &queryrunner.DatabaseError{Message: `ERROR: relation "guests" does not exist (SQLSTATE 42P01)`},
	})

	rec := fixture.run("select * from guests;")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "guests") {
		t.Fatalf("the database's own words were withheld: %s", rec.Body.String())
	}
}

// The words have to arrive where the console reads them. They used to travel
// only in `message` under `invalid_request`, so the console printed its
// sentence for a malformed form — "check the fields you filled in", with a
// support reference under it — and never showed the one line that said what
// to change. The console reads a query's specifics from `subject`, as it
// already does for every refusal the validator makes.
func TestADatabaseRefusalHasItsOwnCodeAndNamesTheReasonAsItsSubject(t *testing.T) {
	const reason = `ERROR: column "alibi" does not exist (SQLSTATE 42703)`
	fixture := newConsoleFixture(t, fakeConsole{err: &queryrunner.DatabaseError{Message: reason}})

	rec := fixture.run("select alibi from guests;")

	if code := errorCode(t, rec); code != "query_database_error" {
		t.Fatalf("code = %q, want query_database_error (body: %s)", code, rec.Body.String())
	}
	if subject, _ := decode(t, rec)["subject"].(string); subject != reason {
		t.Fatalf("subject = %q, want the database's own words %q", subject, reason)
	}
}
