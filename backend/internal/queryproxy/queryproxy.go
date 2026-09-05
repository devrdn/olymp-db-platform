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
	"time"

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
	// rate is a second instance of the Query Runner's own sliding-window
	// limiter, kept here for the one thing the runner cannot do: refuse a
	// query before the journal writes it. The two processes cannot share one
	// instance, but they share the one implementation (queryrunner.RateLimiter).
	rate *queryrunner.RateLimiter
	// perMinuteDefault is the rate a contest gets when its organiser left
	// query_rate_limit_per_min at zero, and — since effectiveRateLimit treats
	// it as the installation's own ceiling — the most any contest's own
	// setting is allowed to ask for. New supplies the architecture's own
	// figure (section 5); WithPerMinuteDefault lets the deployment's actual
	// configuration override it. Zero means the installation itself has no
	// limit, the same convention config.Runner.PerMinute uses.
	perMinuteDefault int
	// now is the clock Run compares a participant's deadline against. A field
	// rather than a bare time.Now() call so a test can hold "now" still next
	// to a deadline it names explicitly, instead of racing the wall clock.
	now func() time.Time
	// grace is the network-latency allowance added to a deadline before Run
	// refuses a query for arriving too late (§8). It is never subtracted from
	// what a participant is shown — nothing here renders a deadline, and the
	// day something does, it must call contests.Deadline without this.
	grace time.Duration
}

// defaultGrace is the network-latency allowance a deployment gets unless
// WithGrace says otherwise — the "about 5 seconds" docs/ARCHITECTURE.md §8
// names.
const defaultGrace = 5 * time.Second

// New assembles the façade.
func New(people People, contests Contests, games Games, databases Databases, runner Executor) *Service {
	return &Service{
		people: people, contests: contests, games: games, databases: databases, runner: runner,
		rate:             queryrunner.NewRateLimiter(0, time.Minute),
		perMinuteDefault: queryrunner.DefaultLimits().PerMinute,
		now:              func() time.Time { return time.Now().UTC() },
		grace:            defaultGrace,
	}
}

// WithClock overrides the wall clock Run compares a participant's deadline
// against. A deployment never calls this and gets time.Now().UTC(); tests use
// it to place "now" precisely relative to a deadline instead of racing it.
func (s *Service) WithClock(now func() time.Time) *Service {
	if now != nil {
		s.now = now
	}
	return s
}

// WithGrace overrides the network-latency allowance New defaults to five
// seconds, so a deployment's own configuration decides what "just in time"
// means here the same way it will for the submission path and SSE — all
// three add this on top of the one contests.Deadline formula rather than
// keeping a grace of their own.
func (s *Service) WithGrace(grace time.Duration) *Service {
	if grace >= 0 {
		s.grace = grace
	}
	return s
}

// WithPerMinuteDefault sets the rate a contest falls back to when its
// organiser left query_rate_limit_per_min at zero, and the ceiling
// effectiveRateLimit will not let any contest's own setting exceed — so this
// façade's own pre-check matches whatever QUERY_PER_MINUTE the Query Runner
// was actually deployed with rather than the architecture's own figure.
// Zero means the deployment's installation has no limit at all, the same
// meaning config.Runner.PerMinute gives the same variable; negative is
// refused rather than silently ignored, since Config.Load never produces one
// and a caller passing one is a bug this should not hide.
func (s *Service) WithPerMinuteDefault(perMinute int) *Service {
	if perMinute >= 0 {
		s.perMinuteDefault = perMinute
	}
	return s
}

// effectiveRateLimit resolves a contest's own rate against the installation's,
// so that the number an organiser sets describes what will actually happen.
//
// Zero means "no limit" on both sides, by the same convention
// perMinuteDefault documents: a contest left at zero defers to the
// installation, and an installation left at zero has no ceiling for anything
// to defer to, so a contest's explicit number is never clamped against it. An
// installation that does state one is a genuine ceiling — an organiser could
// previously ask for more than the Query Runner would ever honour, and every
// query above the installation's own rate still paid this façade's journal
// write before the Runner refused it for the same reason a second time
// (the finding this closes). Below the ceiling, or with no ceiling to be
// below, the contest's own number is what is enforced — a stricter contest
// setting than the installation's already worked correctly and stays
// unchanged.
func effectiveRateLimit(contestLimit, installationLimit int) int {
	if contestLimit <= 0 {
		return installationLimit
	}
	if installationLimit > 0 && contestLimit > installationLimit {
		return installationLimit
	}
	return contestLimit
}

// Run answers one query, or says why it will not.
//
// The order is cheapest first and most-revealing last, with one deliberate
// exception. Who is asking is a single row; whether the contest is running —
// which now means both its status and the participant's own deadline, per
// contests.Deadline — and holds this address is decided from another; the
// rate check comes next,
// keyed by the registration those two rows named — and only then is the
// query's own length checked, even though it costs nothing but a comparison
// and could be tested first. Checking it first used to mean an oversized
// query never called the rate check at all: refused for free, over and over,
// against no budget (CLAUDE.md rule 13 — a refused query still counts against
// the rate, because refusing it still cost something). The two lookups above
// are the same ones an ordinary query pays regardless, so an oversized one
// now costs exactly what a legitimate one does, plus the one comparison that
// refuses it — instead of nothing. Only then is a database provisioned, which
// may create one, and only then does anything reach the Query Runner. A query
// from somebody who is not a participant must not cost a CREATE DATABASE.
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
	// The one formula every timing check in the system uses (§8), and the
	// guarantee that closing does not depend on the scheduler: a fixed
	// contest past its own ends_at, or an individual participant past their
	// own started_at+duration_min, is refused here on the server's own clock
	// even if contests.Status has not (yet, or ever, with a dead scheduler)
	// caught up to "finished". ok is false for a state with no deadline to
	// compare against — an individual participant who has not started, or an
	// invariant Contest.Validate would have refused — and that is refused the
	// same way rather than treated as no limit at all.
	deadline, ok := contests.Deadline(contest, participant)
	if !ok || s.now().After(deadline.Add(s.grace)) {
		return nil, ErrContestNotRunning
	}
	if !contest.AllowsAddress(cmd.Address) {
		return nil, ErrAddressNotAllowed
	}

	// The same registration a refusal is journalled against, so a participant
	// asking too fast meets the same limit here as at the Query Runner — and
	// meets it before a row is written rather than after, which is the only
	// difference between the two checks. effectiveRateLimit is what keeps this
	// number honest against the installation's own: see its doc for why a
	// contest cannot ask for more than the Query Runner would actually honour.
	limit := effectiveRateLimit(contest.Settings.QueryRateLimitPerMin, s.perMinuteDefault)
	if err = s.rate.Admit(participant.ID.String(), limit); err != nil {
		return nil, err
	}

	if len(cmd.SQL) > sqlpolicy.MaxQueryBytes {
		return nil, &sqlpolicy.Refusal{Code: sqlpolicy.CodeTooLong, Subject: fmt.Sprintf("%d bytes", len(cmd.SQL))}
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
	// A failure to journal the query is ours, not the database refusing the
	// participant's SQL — it never got that far — and it is not among
	// Outcomes() because it never crosses to the Query Runner at all.
	if errors.Is(err, queryrunner.ErrJournalUnavailable) {
		return false
	}
	for _, ours := range queryrunner.Outcomes() {
		if errors.Is(err, ours) {
			return false
		}
	}
	return true
}
