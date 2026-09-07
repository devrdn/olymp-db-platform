package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/google/uuid"
)

func TestSchemaEndpointAnswersWithTheGamesShape(t *testing.T) {
	f := newParticipantFixture(t)
	contest := uuid.New()
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
	if f.access.schemaAsked != contest {
		t.Fatalf("asked about %s, want the contest named in the URL", f.access.schemaAsked)
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
		{"not a participant", queryproxy.ErrNotAParticipant, http.StatusForbidden, "not_a_participant"},
		{"the contest is not running", queryproxy.ErrContestNotRunning, http.StatusConflict, "contest_not_running"},
		{"the participant has finished", queryproxy.ErrFinished, http.StatusConflict, "contest_finished"},
		{"the address is not allowed", queryproxy.ErrAddressNotAllowed, http.StatusForbidden, "address_not_allowed"},
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
