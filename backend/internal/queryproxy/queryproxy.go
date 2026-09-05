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

// People answers who is asking, and starts their clock.
type People interface {
	ByUser(ctx context.Context, contestID, userID uuid.UUID) (contests.Participant, error)
	// Start records now as the participant's first deliberate action, if they
	// have not already begun. The narrow slice of contests.RegistrationRepository
	// this façade needs — see contests.RegistrationRepository.Start for the
	// concurrency guarantee every implementation must provide.
	Start(ctx context.Context, registrationID uuid.UUID, now time.Time) (contests.Participant, error)
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
	// One instance serves two different keys: the authenticated caller
	// (cmd.UserID), checked first and before any lookup at all because it is
	// the only bounded thing Run has to key on before a registration is even
	// found; and the registration (participant.ID), checked once one exists,
	// against whatever that specific contest allows. Neither key's counters
	// interfere with the other's.
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
//
// Panics on a negative grace, the same as WithPerMinuteDefault does on a
// negative rate: config.Load never produces one (DEADLINE_GRACE is rejected
// there first), so a caller passing one is a bug in the wiring, not
// deployment input to fail closed on quietly.
func (s *Service) WithGrace(grace time.Duration) *Service {
	if grace < 0 {
		panic(fmt.Sprintf("queryproxy: negative grace %s", grace))
	}
	s.grace = grace
	return s
}

// WithPerMinuteDefault sets the rate a contest falls back to when its
// organiser left query_rate_limit_per_min at zero, and the ceiling
// effectiveRateLimit will not let any contest's own setting exceed — so this
// façade's own pre-check matches whatever QUERY_PER_MINUTE the Query Runner
// was actually deployed with rather than the architecture's own figure.
// Zero means the deployment's installation has no limit at all, the same
// meaning config.Runner.PerMinute gives the same variable; negative panics
// rather than being silently ignored, since Config.Load never produces one
// and a caller passing one is a bug this should not hide. WithGrace agrees:
// the same reasoning applies to a negative grace, and the two options must
// not disagree about what a bad value deserves.
func (s *Service) WithPerMinuteDefault(perMinute int) *Service {
	if perMinute < 0 {
		panic(fmt.Sprintf("queryproxy: negative per-minute default %d", perMinute))
	}
	s.perMinuteDefault = perMinute
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
// The order is cheapest first and most-revealing last, with two deliberate
// exceptions.
//
// The first runs before anything else and before any lookup at all: a rate
// check keyed by the authenticated caller (finding 3). cmd.ContestID is
// attacker-chosen and unbounded — any UUID at all, most naming no contest —
// so a caller who names a random one every time would pay nothing but a
// session read to reach ErrNotAParticipant, over and over, with no limiter in
// front of it: the participant lookup below (and, for somebody who is a
// participant but disqualified or finished, the contest lookup after it too)
// used to run on every one of those requests for free. cmd.UserID is the
// opposite of unbounded: one key per authenticated account, decided at
// sign-in and never supplied by the request, so a caller cannot mint a fresh
// one just by asking. It is checked against s.perMinuteDefault — the
// installation's own figure — because no contest has been looked up yet to
// ask for one of its own, and perMinuteDefault is already the ceiling
// effectiveRateLimit never lets any contest's own setting exceed, so nothing
// admitted here could have been refused by a contest-specific limit anyway
// (CLAUDE.md rules 5 and 13).
//
// The second is the existing registration-keyed check below: who is asking is
// a single row, the contest is another, and the rate check comes right after
// those two, keyed by the registration they named, ahead of every refusal
// downstream of it. A refused query still cost these two lookups, and used to
// cost them for free, over and over, with nothing beyond the check above
// counting the attempt: an individual participant hammering this endpoint
// after their own deadline passed, or before their contest opened, met no
// limiter of its own otherwise. Now every one of them is admitted or refused
// by the same limiter, keyed by their own registration this time so a
// contest's own tighter setting is what binds, and only a query that clears
// it is charged the work below.
//
// What still needs its own registration is checked next — is the contest
// running at all, and (the one formula every timing check in the system uses,
// §8) has this participant's own deadline passed — because both are already
// answerable from what the two lookups above returned and neither has to
// wait for anything else. The one exception is a not-yet-started individual
// participant, who has no deadline to compare against yet: for that one case
// this checks the contest's own window instead (finding 1) and leaves the
// deadline check itself for after the clock is actually started, further
// down.
//
// Only past that does the address restriction, the query's own length and the
// game's existence get checked, each cheaper than a database round trip and
// each placed so an oversized or misdirected query pays the same lookups and
// the same rate check a legitimate one does rather than dodging them for
// free. Only then is a database provisioned, which may create one. And only
// once every one of those has admitted the request does a not-yet-started
// individual participant's clock actually start (finding 2): starting it any
// earlier meant a query refused for its address, its length, an unprovisioned
// game, or a provisioning failure still cost that participant their own first
// minute, permanently, for a request that was never going to be answered
// anyway. A query the SQL validator itself rejects still starts the clock —
// validation happens downstream in the Query Runner, past everything this
// façade checks — which is left as is: the participant did act deliberately
// against the game, and by the time the validator speaks the request has
// already cleared every check that is this façade's to make.
func (s *Service) Run(ctx context.Context, cmd Command) (*queryrunner.Result, error) {
	if err := s.rate.Admit(cmd.UserID.String(), s.perMinuteDefault); err != nil {
		return nil, err
	}

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

	// The same registration a refusal is journalled against, so a participant
	// asking too fast — or hammering this endpoint while every answer is a
	// refusal — meets the same limit here as at the Query Runner, and meets
	// it before any of the refusals below rather than after. effectiveRateLimit
	// is what keeps this number honest against the installation's own: see its
	// doc for why a contest cannot ask for more than the Query Runner would
	// actually honour.
	limit := effectiveRateLimit(contest.Settings.QueryRateLimitPerMin, s.perMinuteDefault)
	if err = s.rate.Admit(participant.ID.String(), limit); err != nil {
		return nil, err
	}

	if contest.Status != contests.StatusRunning {
		return nil, ErrContestNotRunning
	}

	// Whether this call could be the deliberate action that starts an
	// individual participant's own clock (§8). Fixed timing never starts a
	// clock at all, and a participant who already has one does not get a
	// second — Start's own guard is "started_at IS NULL", but reading
	// StartedAt here first is what keeps every query after the first from
	// touching the registration row through anything but the read this
	// function already paid for.
	firstAction := contest.Timing == contests.TimingIndividual && participant.StartedAt == nil

	if firstAction {
		// Finding 1: contest.Status is a manual step in an organiser's own
		// workflow and says nothing about the wall clock — it can be moved to
		// "running" hours before starts_at. Deadline cannot be asked yet
		// either: it needs a StartedAt this participant does not have. So the
		// one thing there is to check before starting is the window itself,
		// and getting this refusal right here, before the address, length and
		// provisioning checks below, means a query outside the window never
		// pays for any of them either.
		if !contest.OpenForStart(s.now()) {
			return nil, ErrContestNotRunning
		}
	} else if s.deadlinePassed(contest, participant) {
		return nil, ErrContestNotRunning
	}

	if !contest.AllowsAddress(cmd.Address) {
		return nil, ErrAddressNotAllowed
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

	// Every other refusal has now had its say — the query is otherwise
	// admitted, and only now does a not-yet-started individual participant's
	// first deliberate action actually start their own clock (finding 2).
	// The window was already confirmed open above; nothing between then and
	// here can have moved it, short of the contest's own ends_at arriving
	// mid-request, which deadlinePassed below still catches.
	if firstAction {
		if participant, err = s.people.Start(ctx, participant.ID, s.now()); err != nil {
			return nil, fmt.Errorf("%w: start the participant's clock: %w", ErrUnavailable, err)
		}
		if s.deadlinePassed(contest, participant) {
			return nil, ErrContestNotRunning
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

// deadlinePassed reports whether contest is no longer open to participant, by
// the one formula every timing check in the system uses (contests.Deadline,
// §8): a fixed contest past its own ends_at, or an individual participant
// past their own started_at+duration_min, is refused here on the server's own
// clock even if contests.Status has not (yet, or ever, with a dead scheduler)
// caught up to "finished". ok is false for a state with no deadline to
// compare against at all — an individual participant who has not started, or
// an invariant Contest.Validate would have refused — and that is treated as
// passed rather than as no limit.
func (s *Service) deadlinePassed(contest contests.Contest, participant contests.Participant) bool {
	deadline, ok := contests.Deadline(contest, participant)
	return !ok || s.now().After(deadline.Add(s.grace))
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
