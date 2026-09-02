// Package queryproxy takes a participant's SQL and answers with rows.
//
// It is the façade section 2.2 names: the thin thing between the interface and
// the Query Runner. Everything it does is decide *whether* and *where* — who is
// asking, whether the contest will take a question from them now, and which
// database is theirs. What the SQL is allowed to do, how long it may run and
// how much it may return are the Query Runner's, and this package does not
// second-guess any of them.
//
// The one rule worth stating on its own: the database is looked up from the
// registration on every request and never arrives in one. A participant names
// their query and nothing else — section 5, point 1.
package queryproxy

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

// Why a query was not run, before it was ever looked at.
//
// Separate errors because each is a different sentence to the person asking,
// and because only one of them means they did something wrong.
var (
	// ErrNotAParticipant covers never having registered and having been
	// disqualified alike: both mean this contest will not take a question from
	// this person, and distinguishing them to the caller would tell somebody
	// probing a contest whether an account is on its roster.
	ErrNotAParticipant = errors.New("not a participant of this contest")
	// ErrContestNotRunning is a contest that has not started or has finished.
	ErrContestNotRunning = errors.New("the contest is not running")
	// ErrFinished is a participant who has already finished. Their answers
	// are in; the console closing with them is the point of finishing.
	ErrFinished = errors.New("the participant has finished")
	// ErrAddressNotAllowed is a query from outside the network the contest is
	// held on. Checked on every query and not only at enrolment: a restriction
	// that is applied once is a restriction somebody walks out of the room
	// with.
	ErrAddressNotAllowed = errors.New("the address is not allowed")
	// ErrNoGameYet is a contest whose game database was never built. Nobody's
	// fault, and not a fact about the query.
	ErrNoGameYet = errors.New("the contest has no game database yet")
	// ErrUnavailable is this service failing, as opposed to the query being
	// refused. Marked apart because the two need different answers: a refusal
	// is about the query and belongs to the participant, while a database that
	// cannot be reached is ours and must not arrive as "your request was bad"
	// with a connection string attached.
	ErrUnavailable = errors.New("the query could not be answered")
	// ErrDatabaseDeclined is the database refusing a query in a contest that
	// hides its schema.
	//
	// PostgreSQL says exactly which relation does not exist, which is the most
	// useful sentence there is — and, in a contest that closed the catalogues
	// so that the schema has to be discovered some other way, it is an oracle:
	// guess a name, read the answer, and the list the closed catalogue was
	// hiding is reconstructed. So in that contest and only in that one, the
	// database's own words are kept back.
	ErrDatabaseDeclined = errors.New("the database refused the query")
)

// People answers who is asking.
type People interface {
	ByUser(ctx context.Context, contestID, userID uuid.UUID) (contests.Participant, error)
}

// Contests answers what they are asking about.
type Contests interface {
	ByID(ctx context.Context, id uuid.UUID) (contests.Contest, error)
}

// Games answers which template a contest plays on, and under what policy.
type Games interface {
	Game(ctx context.Context, contestID uuid.UUID) (provisioning.Contest, error)
}

// Databases hands out the participant's own copy, and says how large it may
// grow.
type Databases interface {
	Ensure(ctx context.Context, contest provisioning.Contest, registration uuid.UUID) (string, error)
	Quota(ctx context.Context, contest provisioning.Contest) (int64, error)
}

// Executor runs the query and journals it. In a deployment that is a client of
// the Query Runner service wrapped in the query log; in a test it is neither.
type Executor interface {
	Run(ctx context.Context, req queryrunner.Request, requestID uuid.UUID) (*queryrunner.Result, error)
}

// Command is one participant asking one question.
//
// There is no database in it, and that absence is the point.
type Command struct {
	ContestID uuid.UUID
	UserID    uuid.UUID
	SQL       string
	// Address is where the query came from, resolved by the HTTP layer. The
	// contest may be held on one network, and a participant who enrolled in
	// the lab must not be able to carry on from home.
	Address netip.Addr
	// RequestID ties the journal row to the same request in the technical
	// logs, which is what makes "it failed at 14:02" answerable.
	RequestID uuid.UUID
}

// Service answers queries.
type Service struct {
	people    People
	contests  Contests
	games     Games
	databases Databases
	runner    Executor
}

// New assembles the façade.
func New(people People, contests Contests, games Games, databases Databases, runner Executor) *Service {
	return &Service{people: people, contests: contests, games: games, databases: databases, runner: runner}
}

// Run answers one query, or says why it will not.
//
// The order is cheapest first and most-revealing last. Who is asking is a
// single row; whether the contest is running is another; only then is a
// database provisioned, which may create one, and only then does anything
// reach the Query Runner. A query from somebody who is not a participant must
// not cost a CREATE DATABASE.
func (s *Service) Run(ctx context.Context, cmd Command) (*queryrunner.Result, error) {
	participant, err := s.people.ByUser(ctx, cmd.ContestID, cmd.UserID)
	switch {
	case errors.Is(err, contests.ErrParticipantNotFound):
		return nil, ErrNotAParticipant
	case err != nil:
		return nil, fmt.Errorf("%w: look up the participant: %w", ErrUnavailable, err)
	case participant.Status == contests.RegistrationDisqualified:
		return nil, ErrNotAParticipant
	case participant.Status == contests.RegistrationFinished:
		return nil, ErrFinished
	}

	contest, err := s.contests.ByID(ctx, cmd.ContestID)
	if err != nil {
		return nil, fmt.Errorf("%w: look up the contest: %w", ErrUnavailable, err)
	}
	if contest.Status != contests.StatusRunning {
		return nil, ErrContestNotRunning
	}
	if !contest.AllowsAddress(cmd.Address) {
		return nil, ErrAddressNotAllowed
	}

	game, err := s.games.Game(ctx, cmd.ContestID)
	switch {
	case errors.Is(err, provisioning.ErrNoGame):
		return nil, ErrNoGameYet
	case err != nil:
		return nil, fmt.Errorf("%w: look up the contest's game: %w", ErrUnavailable, err)
	}

	database, err := s.databases.Ensure(ctx, game, participant.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: provide the participant's database: %w", ErrUnavailable, err)
	}
	// Only where writing is permitted. A read-only contest cannot grow its
	// database, so the quota is a number nothing will compare against — and
	// working it out means asking the game cluster how large the template is,
	// on every query, for every participant.
	var quota int64
	if game.Policy.Mode == sqlpolicy.ModeReadWrite {
		if quota, err = s.databases.Quota(ctx, game); err != nil {
			return nil, fmt.Errorf("%w: work out the size limit: %w", ErrUnavailable, err)
		}
	}

	result, err := s.runner.Run(ctx, queryrunner.Request{
		Registration:   participant.ID,
		Database:       database,
		SQL:            cmd.SQL,
		Policy:         game.Policy,
		DiskQuotaBytes: quota,
	}, cmd.RequestID)

	// A refusal and the runner's own outcomes go back untouched: each carries
	// a code the interface turns into a sentence in the participant's own
	// language, and wrapping one would leave that with nothing to read. What
	// is held back is the database speaking for itself, and only where the
	// contest asked for the schema to be hidden.
	if err != nil && !game.Policy.AllowCatalog && speaksForTheDatabase(err) {
		return nil, ErrDatabaseDeclined
	}
	return result, err
}

// speaksForTheDatabase reports an error that carries PostgreSQL's own words
// rather than one of ours.
//
// Written as "none of the answers we produce" rather than as a list of driver
// errors: a new sentinel of ours is something this must keep passing through,
// and a new shape of database error is something it must keep catching. Only
// one of those two lists can be kept complete by hand, so the check is against
// ours.
func speaksForTheDatabase(err error) bool {
	var refusal *sqlpolicy.Refusal
	if errors.As(err, &refusal) {
		return false
	}
	for _, ours := range queryrunner.Outcomes() {
		if errors.Is(err, ours) {
			return false
		}
	}
	return true
}
