package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/google/uuid"
)

// fakeResets records what the route asked queryproxy for; the decisions are
// tested in internal/queryproxy.
type fakeResets struct {
	got []queryproxy.ResetCommand
	err error
}

func (r *fakeResets) Reset(_ context.Context, cmd queryproxy.ResetCommand) error {
	r.got = append(r.got, cmd)
	return r.err
}

func (f *participantFixture) reset(contestID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/contests/"+contestID+"/play/reset", nil)
	req.Header.Set("User-Agent", fixtureUserAgent)
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func TestAResetIsAskedForTheURLsContestAsTheSignedInParticipant(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()

	rec := f.reset(contestID.String())

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if len(f.resets.got) != 1 {
		t.Fatalf("%d resets asked for, want 1", len(f.resets.got))
	}
	got := f.resets.got[0]
	if got.ContestID != contestID || got.UserID != f.userID {
		t.Fatalf("asked for contest %v as %v, want %v as %v", got.ContestID, got.UserID, contestID, f.userID)
	}
	// httptest's peer; the gate checks it against the contest's networks.
	if got.Address != netip.MustParseAddr("192.0.2.1") {
		t.Fatalf("address = %v, want the caller's", got.Address)
	}
	if got.Session != monitor.SessionTag(f.cookie.Value) || got.UserAgent != fixtureUserAgent {
		t.Fatalf("session %q and browser %q, want the request's own", got.Session, got.UserAgent)
	}
}

func TestResetRefusalsBecomeTheDocumentedStatusAndCode(t *testing.T) {
	for _, given := range []struct {
		err        error
		status     int
		code       string
		retryAfter string
	}{
		{err: queryproxy.ErrResetReadOnly, status: http.StatusConflict, code: "reset_read_only"},
		{err: queryproxy.ErrResetTooOften, status: http.StatusTooManyRequests, code: "reset_too_often", retryAfter: "600"},
		{err: queryrunner.ErrTooManyQueries, status: http.StatusTooManyRequests, code: "query_too_often", retryAfter: "60"},
		{err: contests.ErrNotAParticipant, status: http.StatusForbidden, code: "not_a_participant"},
		{err: contests.ErrContestEnded, status: http.StatusConflict, code: "contest_ended"},
		{err: queryproxy.ErrNoGameYet, status: http.StatusConflict, code: "no_game_yet"},
		{err: queryproxy.ErrNothingLeftToAnswer, status: http.StatusConflict, code: "nothing_left_to_answer"},
		{err: fmt.Errorf("%w: waiting for the reset", queryrunner.ErrCanceled),
			status: http.StatusRequestTimeout, code: "query_cancelled"},
		{err: queryproxy.ErrNoRoomForDatabase, status: http.StatusServiceUnavailable, code: "game_cluster_full"},
		{err: fmt.Errorf("%w: copy the template: disk I/O", queryproxy.ErrUnavailable),
			status: http.StatusInternalServerError, code: "internal_error"},
	} {
		t.Run(given.code, func(t *testing.T) {
			f := newParticipantFixture(t)
			f.resets.err = given.err

			rec := f.reset(uuid.NewString())

			if rec.Code != given.status || errorCode(t, rec) != given.code {
				t.Fatalf("answered %d %q, want %d %q", rec.Code, errorCode(t, rec), given.status, given.code)
			}
			if got := rec.Header().Get("Retry-After"); got != given.retryAfter {
				t.Fatalf("Retry-After = %q, want %q", got, given.retryAfter)
			}
		})
	}
}

func TestAResetWithAnInvalidContestIDIsA400(t *testing.T) {
	f := newParticipantFixture(t)

	rec := f.reset("not-a-uuid")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if len(f.resets.got) != 0 {
		t.Fatal("an unreadable contest identifier reached the service")
	}
}

// A caller who left while their press waited is not an error of ours.
func TestAResetWhoseCallerLeftIsNotLogged(t *testing.T) {
	f := newParticipantFixture(t)
	f.resets.err = fmt.Errorf("%w: waiting for the reset", queryrunner.ErrCanceled)

	f.reset(uuid.NewString())

	if f.logs.loggedError("") {
		t.Fatal("a caller who left was logged as an error")
	}
}
