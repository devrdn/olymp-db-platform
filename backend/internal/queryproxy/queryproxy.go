// Package queryproxy takes a participant's SQL and answers with rows.
//
// It is the façade section 2.2 names: the thin thing between the interface and
// the Query Runner. Everything it does is decide *whether* and *where* — who is
// asking, whether the contest will take a question from them now, and which
// database is theirs. What the SQL is allowed to do, how long it may run and
// how much it may return are the Query Runner's, and this package does not
// second-guess any of them.
//
// The middle question has one answer for the whole service, and it is not
// written here: this package looks the registration up and asks the
// participation gate, contests.StandingOf, the same rule every other
// participant-facing path asks. What it keeps of its own is in front of the
// gate — the rate limits — and behind it: starting an individual clock, and
// telling the monitor who was seen.
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
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

// Why a query was not run, before it was ever looked at.
//
// Separate errors because each is a different sentence to the person asking,
// and because only one of them means they did something wrong.
//
// Whether this participant may act in this contest at all is not among them:
// that is the participation gate's (contests.StandingOf), and its refusals —
// not a participant, the contest not running, finished, time up, an address
// the contest is not held on — are contests' sentinels, handed over as the
// gate gives them.
var (
	// ErrNothingLeftToAnswer is a participant for whom no question of the
	// contest is still answerable: every one of them is either answered
	// correctly or out of attempts. The console exists to help somebody
	// arrive at an answer, and running a query cannot lead to one any more,
	// so it stops taking queries — a participant otherwise keeps an
	// execution slot, a database and a journal writer for a contest they can
	// no longer score a point in.
	//
	// Deliberately not contests.ErrParticipantFinished, and deliberately not
	// a status write either. Finishing is a fact about the registration and
	// it closes the whole play screen: the gate refuses a finished
	// registration for Access as well, so marking somebody finished here
	// would take away the story, the question list, the results and the
	// timer along with the console. This is only
	// about the console, only about right now, and it reverses itself the
	// moment the contest gives them something to answer again — an organiser
	// raising max_attempts, or making a hidden question visible.
	ErrNothingLeftToAnswer = errors.New("no question of this contest is still answerable")
	// ErrNoGameYet is a contest whose game database was never built. Nobody's
	// fault, and not a fact about the query.
	ErrNoGameYet = errors.New("the contest has no game database yet")
	// ErrNoRoomForDatabase is the game cluster having no room left within its
	// configured budget for this participant's own copy of the contest.
	//
	// Marked apart from ErrUnavailable for the reason provisioning.
	// ErrClusterFull is a sentinel at all: this is a deployment that has run
	// out of the disk it said it had, which is a fact somebody can act on
	// (raise GAME_CLUSTER_MAX_BYTES, reclaim a finished contest, add a
	// volume), not this service failing. Reported as "internal error" it is
	// indistinguishable from an outage, and the participant is told nothing
	// true.
	ErrNoRoomForDatabase = errors.New("the game cluster has no room for this participant's database")
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

// Errors lists every sentinel this package declares: the refusals a
// participant meets, and ErrUnavailable, which is ours. internal/api answers
// each from a table of its own, and a test there walks this list so that none
// can reach a client as "internal error"; a test here reads the package's
// source so that none can be declared and left off it.
//
// Not everything this package returns is here. The participation gate's
// refusals are contests' (contests.Errors), a *sqlpolicy.Refusal Run builds
// itself, and the Query Runner's and its transport's errors it passes
// through, belong to those packages and are answered from their own lists.
func Errors() []error {
	return []error{
		ErrNothingLeftToAnswer, ErrNoGameYet, ErrNoRoomForDatabase, ErrUnavailable,
		ErrDatabaseDeclined, ErrSchemaHidden,
	}
}

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

// LookupResult is everything Run needs about one query before it reaches the
// runner: who is asking, what they are asking about, its game, and the
// participant's own copy of it — the four rows People.ByUser, Contests.ByID,
// Games.Game and Databases.Instance each once read separately.
type LookupResult struct {
	Participant contests.Participant
	Contest     contests.Contest
	// Game and GameErr are checked at the same point in Run's admission order
	// a standalone Games.Game call always was: GameErr is
	// provisioning.ErrNoGame for a contest with no ready template, nil once
	// Game is populated.
	Game    provisioning.Contest
	GameErr error
	// Instance and InstanceErr are the participant's own database as
	// Databases.Instance would answer it — InstanceErr is
	// provisioning.ErrNoInstance for a registration that has none yet — and
	// go to Databases.EnsureFrom as they are, so the common case, a current
	// copy, costs no read of its own.
	Instance    provisioning.Instance
	InstanceErr error
}

// Lookup answers who is asking and what about, one round trip per request.
// See defaultLookup for what New wires by default and postgres.Registrations
// for the real single queries a deployment replaces it with (WithLookup).
type Lookup interface {
	// ForRun is everything Run and Schema need (LookupResult).
	ForRun(ctx context.Context, contestID, userID uuid.UUID) (LookupResult, error)
	// ForAccess is the participant and the contest, for the read endpoints
	// that need neither the game nor a database (Access, AccessForEvents).
	ForAccess(ctx context.Context, contestID, userID uuid.UUID) (contests.Participant, contests.Contest, error)
}

// Databases hands out the participant's own copy, and says how large it may
// grow. Instance is asked only by New's defaultLookup: the deployment's
// Lookup reads the same row in its own statement.
type Databases interface {
	Instance(ctx context.Context, registration uuid.UUID) (provisioning.Instance, error)
	EnsureFrom(ctx context.Context, contest provisioning.Contest, registration uuid.UUID, existing provisioning.Instance, err error) (string, error)
	Quota(ctx context.Context, contest provisioning.Contest) (int64, error)
}

// Answerable answers the one question the console needs before it will take
// another query: is there still a question in this contest this registration
// could get an answer out of?
//
// Narrow on purpose (CLAUDE.md rule 3). postgres.Answerable implements it in
// one statement, and "still answerable" there is the same definition
// contests.Reader already shows the participant on their own question list —
// visible, not answered correctly, attempts not spent. The two must not
// drift: a list that says a question can still be answered while this says
// otherwise takes the console away from somebody with work still to do.
type Answerable interface {
	AnswerableLeft(ctx context.Context, contestID, registrationID uuid.UUID) (bool, error)
}

// Executor runs the query and journals it. In a deployment that is a client of
// the Query Runner service wrapped in the query log; in a test it is neither.
type Executor interface {
	Run(ctx context.Context, req queryrunner.Request, origin queryrunner.Origin) (*queryrunner.Result, error)
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
	// the lab must not be able to carry on from home. It is also written into
	// the query's journal row, so an organiser sees where each query came
	// from.
	Address netip.Addr
	// RequestID ties the journal row to the same request in the technical
	// logs, which is what makes "it failed at 14:02" answerable.
	RequestID uuid.UUID
	// Session names the caller's session (monitor.SessionTag, never the
	// token) and UserAgent their browser, for the Watcher: a second session
	// using the registration is reported with the browser it came from.
	Session   string
	UserAgent string
}

// Watcher hears of every request admission lets through, to detect a
// registration's address changing or a second session using it
// (monitor.Tracker). It never refuses: what it detects is recorded, not
// enforced, and a watcher that cannot keep up gives up on its own within a
// bounded time.
type Watcher interface {
	Observe(ctx context.Context, visit monitor.Visit)
}

// Service answers queries.
type Service struct {
	people    People
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
	// now is the instant every admission here asks the participation gate
	// about. A field rather than a bare time.Now() call so a test can hold
	// "now" still next to a deadline it names explicitly, instead of racing
	// the wall clock.
	now func() time.Time
	// grace is the network-latency allowance an already-working participant
	// is given past their deadline (§8), handed to the gate
	// (contests.StandingOf), which is the one place it is added. It is never
	// part of what a participant is shown — nothing here renders a deadline,
	// and the day something does, it must call contests.Deadline without this.
	grace time.Duration
	// schemas answers what a contest's game looks like, for the console's
	// schema panel. Set by WithSchemas and nil until then — see Schema for
	// why a build that never wired it refuses rather than panicking.
	schemas Schemas
	// answerable answers whether this participant still has a question to
	// work towards. Set by WithAnswerable and nil until then — see that
	// option for why a build that never wired it runs the query anyway.
	answerable Answerable
	// lookup answers everything LookupResult carries. See defaultLookup for
	// what New sets it to, and WithLookup for replacing it.
	lookup Lookup
	// watcher hears of every admitted query. Set by WithWatcher; nil watches
	// nothing.
	watcher Watcher
}

// defaultGrace is the network-latency allowance a deployment gets unless
// WithGrace says otherwise — the "about 5 seconds" docs/ARCHITECTURE.md §8
// names.
const defaultGrace = 5 * time.Second

// New assembles the façade.
func New(people People, contests Contests, games Games, databases Databases, runner Executor) *Service {
	return &Service{
		people: people, databases: databases, runner: runner,
		lookup:           defaultLookup{people: people, contests: contests, games: games, databases: databases},
		rate:             queryrunner.NewRateLimiter(0, time.Minute),
		perMinuteDefault: queryrunner.DefaultLimits().PerMinute,
		now:              func() time.Time { return time.Now().UTC() },
		grace:            defaultGrace,
	}
}

// defaultLookup is New's own Lookup: the separate calls Run made before this
// existed, still made the same way and in the same order, behind the one
// interface Run now depends on exclusively. WithLookup replaces it with real
// single queries.
type defaultLookup struct {
	people    People
	contests  Contests
	games     Games
	databases Databases
}

func (d defaultLookup) ForRun(ctx context.Context, contestID, userID uuid.UUID) (LookupResult, error) {
	participant, contest, err := d.ForAccess(ctx, contestID, userID)
	if err != nil {
		return LookupResult{}, err
	}
	game, gameErr := d.games.Game(ctx, contestID)
	instance, instanceErr := d.databases.Instance(ctx, participant.ID)
	return LookupResult{
		Participant: participant, Contest: contest,
		Game: game, GameErr: gameErr,
		Instance: instance, InstanceErr: instanceErr,
	}, nil
}

func (d defaultLookup) ForAccess(ctx context.Context, contestID, userID uuid.UUID) (contests.Participant, contests.Contest, error) {
	participant, err := d.people.ByUser(ctx, contestID, userID)
	if err != nil {
		return contests.Participant{}, contests.Contest{}, err
	}
	contest, err := d.contests.ByID(ctx, contestID)
	if err != nil {
		return contests.Participant{}, contests.Contest{}, err
	}
	return participant, contest, nil
}

// WithClock overrides the wall clock every admission here asks the gate
// about. A deployment never calls this and gets time.Now().UTC(); tests use
// it to place "now" precisely relative to a deadline instead of racing it.
func (s *Service) WithClock(now func() time.Time) *Service {
	if now != nil {
		s.now = now
	}
	return s
}

// WithGrace overrides the network-latency allowance New defaults to five
// seconds, so a deployment's own configuration decides what "just in time"
// means here the same way it does for the submission path — both hand it to
// the gate, which adds it on top of the one contests.Deadline formula, rather
// than keeping a grace of their own.
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

// WithAnswerable supplies the reader behind Run's "is anything still
// answerable" check (ErrNothingLeftToAnswer).
//
// An option rather than a constructor argument, and one that fails *open*:
// a build that never wired it runs the query, exactly as this façade did
// before the check existed. That is the opposite direction from WithSchemas,
// and deliberately so. A missing schema reader costs a participant one panel
// they can play without; a missing reader here, failing closed, would refuse
// every query from every participant of every contest, and it would do it
// mid-olympiad with no way for anybody to tell it apart from a contest that
// really is over. The console staying open for somebody with nothing left to
// answer is the state this whole check exists to improve on — it is not a
// state anything breaks in.
func (s *Service) WithAnswerable(answerable Answerable) *Service {
	s.answerable = answerable
	return s
}

// WithWatcher reports every query admission lets through to watcher. One
// call per query, after admission and before anything else is spent on it,
// so a query refused for its length or its content still counts as the
// registration being used from where it was used.
func (s *Service) WithWatcher(watcher Watcher) *Service {
	s.watcher = watcher
	return s
}

// WithLookup replaces New's own three-call default with a single query,
// removing two of the core round trips Run otherwise pays on every request.
func (s *Service) WithLookup(lookup Lookup) *Service {
	s.lookup = lookup
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
// session read to reach contests.ErrNotAParticipant, over and over, with no limiter in
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
// The second is the registration-keyed check right after the one lookup
// (s.lookup.ForRun) that answers who is asking, the contest and its game
// together, ahead of every refusal downstream of it. A refused query still
// cost that lookup, and used to cost it for free, over and over, with nothing
// beyond the check above counting the attempt: an individual participant
// hammering this endpoint after their own deadline passed, or before their
// contest opened, met no limiter of its own otherwise. Now every one of them
// is admitted or refused by the same limiter, keyed by their own registration
// this time so a contest's own tighter setting is what binds, and only a query
// that clears it is charged the work below.
//
// Then, from values already in hand: may this participant act in this
// contest now, from this address — the participation gate
// (contests.StandingOf), the same one every participant-facing read asks —
// and is the query within its length. A not-yet-started individual
// participant is admitted inside the contest's own window (finding 1), and
// asked again once their clock has started, further down.
//
// Next is the one check that costs a round trip of its own: has this
// participant anything left to answer at all (ErrNothingLeftToAnswer)? After
// the checks above because those are comparisons of values already in hand
// and this is a query; before the game is looked at and before provisioning
// because a participant who can no longer score a point must not be able to
// make this service create them a database, nor ask the game cluster for a
// template, by asking for one. After the rate check for the same reason every
// other refusal is (finding 3): a refused query still costs this read, so it
// still counts. It is skipped entirely when nothing was wired to answer it —
// see WithAnswerable for why a missing wire lets the query through rather than
// refusing it.
//
// Then whether the contest has a game at all (ErrNoGameYet). The game was read
// by the same combined lookup, but its answer is not looked at until here, so
// an unprovisioned game is reported at the same point in the order a separate
// lookup of it used to be.
//
// Only then is a database provisioned, which may create one. And only
// once every one of those has admitted the request does a not-yet-started
// individual participant's clock actually start (finding 2), through the same
// seam a first read starts it (StartOnRead): starting it any
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

	lookup, lookupErr := s.lookup.ForRun(ctx, cmd.ContestID, cmd.UserID)
	participant, err := classifyParticipant(lookup.Participant, lookupErr, "look up the participant and the contest")
	if err != nil {
		return nil, err
	}
	contest := lookup.Contest

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

	// May this participant act in this contest right now, from where they
	// are — the same gate the participant-facing read endpoints ask before
	// showing the story or the questions (see Access). Run keeps its own rate
	// limiting around it rather than folding it in, because a refused query
	// still has to count against the caller's rate (finding 3, see the doc
	// above), and a read of the story never costs a rate check at all.
	if err := s.admit(contest, participant, cmd.Address); err != nil {
		return nil, err
	}
	if s.watcher != nil {
		s.watcher.Observe(ctx, monitor.Visit{
			Contest: contest.ID, Registration: participant.ID,
			Address: cmd.Address, Session: cmd.Session, UserAgent: cmd.UserAgent,
		})
	}

	if len(cmd.SQL) > sqlpolicy.MaxQueryBytes {
		return nil, &sqlpolicy.Refusal{Code: sqlpolicy.CodeTooLong, Subject: fmt.Sprintf("%d bytes", len(cmd.SQL))}
	}

	// Nothing left to work towards closes the console and nothing else (see
	// ErrNothingLeftToAnswer). nil means this build wired no reader, which
	// fails open: the query runs, as it did before this check existed.
	if s.answerable != nil {
		left, err := s.answerable.AnswerableLeft(ctx, cmd.ContestID, participant.ID)
		if err != nil {
			return nil, fmt.Errorf("%w: work out what is still answerable: %w", ErrUnavailable, err)
		}
		if !left {
			return nil, ErrNothingLeftToAnswer
		}
	}

	switch {
	case errors.Is(lookup.GameErr, provisioning.ErrNoGame):
		return nil, ErrNoGameYet
	case lookup.GameErr != nil:
		return nil, fmt.Errorf("%w: look up the contest's game: %w", ErrUnavailable, lookup.GameErr)
	}
	game := lookup.Game

	database, err := s.databases.EnsureFrom(ctx, game, participant.ID, lookup.Instance, lookup.InstanceErr)
	if err != nil {
		return nil, provisionFailure(err)
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
	// first deliberate action actually start their own clock (finding 2). A
	// participant who has one already, or who never will under fixed timing,
	// costs no write: startClock reads StartedAt from the participant this
	// function already paid for. The gate is asked again around the start,
	// for the contest's own ends_at arriving mid-request and for a start
	// another request made first.
	if participant, err = s.startClock(ctx, contest, participant, cmd.Address); err != nil {
		return nil, err
	}

	result, err := s.runner.Run(ctx, queryrunner.Request{
		Registration:   participant.ID,
		Database:       database,
		SQL:            cmd.SQL,
		Policy:         game.Policy,
		DiskQuotaBytes: quota,
	}, queryrunner.Origin{RequestID: cmd.RequestID, Address: cmd.Address})

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

// provisionFailure is what both callers of Databases.EnsureFrom turn its error
// into: the cluster having no room becomes this façade's own sentinel, and
// everything else stays what it was, an outage of ours wearing ErrUnavailable.
//
// One function rather than the same switch written twice, because Run and
// Schema call Ensure for the same reason and a participant must not be told two
// different things about one cluster depending on which of the two they
// happened to hit first.
func provisionFailure(err error) error {
	if errors.Is(err, provisioning.ErrClusterFull) {
		return fmt.Errorf("%w: %w", ErrNoRoomForDatabase, err)
	}
	return fmt.Errorf("%w: provide the participant's database: %w", ErrUnavailable, err)
}

// classifyParticipant turns a raw participant and its lookup error into the
// one answer every caller of s.lookup — Run, Schema and resolve — promises:
// no registration at all is contests.ErrNotAParticipant, the same refusal the
// gate gives a disqualified registration, so probing a contest for who is on
// it learns nothing; anything else is ours (ErrUnavailable, wrapped with wrap
// so each caller can name what it was trying to do). What the registration's
// own status means is the gate's to say, not this function's.
func classifyParticipant(participant contests.Participant, err error, wrap string) (contests.Participant, error) {
	switch {
	case errors.Is(err, contests.ErrParticipantNotFound):
		return contests.Participant{}, contests.ErrNotAParticipant
	case err != nil:
		return contests.Participant{}, fmt.Errorf("%w: %s: %w", ErrUnavailable, wrap, err)
	}
	return participant, nil
}

// admit asks the participation gate (contests.StandingOf) whether participant
// may act in contest now, from addr, with this service's clock and grace: nil,
// or the gate's one refusal. It never starts an individual participant's
// clock — Run does that once every other check downstream has had its say
// (§8, finding 2), and StartOnRead once a read of the contest's content has
// succeeded — so a caller that only wants to know "is this still open to me"
// can ask without the side effect of asking.
func (s *Service) admit(contest contests.Contest, participant contests.Participant, addr netip.Addr) error {
	return contests.StandingOf(contest, participant, s.now(), s.grace, addr).Refusal()
}

// Access resolves who is asking and confirms they may currently interact with
// contestID, for a caller that only wants to look — the participant-facing
// story and questions endpoints, not the SQL console. It starts no clock
// itself; a reader of the contest's content follows a successful read with
// StartOnRead.
//
// This is deliberately the same admission Run requires before it will take a
// query — the participation gate, contests.StandingOf — and nothing more: no
// rate limit of its own, no game lookup, no database provisioning, because
// reading the story costs none of what running a query against the
// participant's own database costs. It is exposed here rather than
// reimplemented beside the read endpoints because "may this student see this
// contest" answered twice, even slightly differently, is exactly the shape of
// bug this project keeps finding.
//
// "No rate limit of its own" is not "no rate limit at all": the lookup here
// is still a cost a caller can spend for free unless something charges for
// it, exactly the reasoning Run's own doc gives for checking a rate before
// any lookup. AdmitRead is that charge, kept a separate method rather than
// folded into this one.
func (s *Service) Access(ctx context.Context, contestID, userID uuid.UUID, addr netip.Addr) (contests.Participant, contests.Contest, error) {
	participant, contest, err := s.resolve(ctx, contestID, userID)
	if err != nil {
		return contests.Participant{}, contests.Contest{}, err
	}

	if err := s.admit(contest, participant, addr); err != nil {
		return contests.Participant{}, contests.Contest{}, err
	}
	return participant, contest, nil
}

// StartOnRead starts an individual participant's clock on their first read of
// the contest's content — the story, the question list or the schema — and
// hands back the participant as it now stands.
//
// Under individual timing those reads are the contest itself. If only a query
// or an answer started the clock, a participant could read every question and
// the whole schema for as long as the window stays open, prepare offline, and
// spend their duration only on typing; ICPC penalty minutes, counted from
// started_at, would shrink by the same preparation. So the first read is the
// first deliberate action, and it goes through the one seam Run and
// contests.Service.Submit use (People.Start, which sets started_at at most
// once however many reads race to it).
//
// A separate method rather than part of Access, and called by the reader only
// once its content has been read successfully: Access also admits the answer
// endpoint and the query log, which start the clock on their own terms or not
// at all, and a read refused for any reason — rate, address, a missing story,
// a hidden schema — showed the participant nothing and must cost them nothing.
// The events channel (AccessForEvents) and the leaderboards never call it:
// watching the clock or the standings is not reading the contest.
//
// Nothing is written for fixed timing or for a participant already started.
// Otherwise the gate is asked here, from addr, rather than trusted from the
// caller's earlier Access, so this method alone never starts a clock the gate
// would refuse — outside the contest's own window, at or after ends_at, or
// from a network the contest is not held on — and asked again of the
// participant Start hands back, whose start may be one another request made
// first and whose time may already be up.
func (s *Service) StartOnRead(ctx context.Context, contest contests.Contest, participant contests.Participant, addr netip.Addr) (contests.Participant, error) {
	return s.startClock(ctx, contest, participant, addr)
}

// startClock is StartOnRead's rule, shared with Run's first query: check,
// start, check again. A participant whose clock is not pending is handed back
// as is, at no cost; one the gate admits with their clock still pending after
// Start is refused, failing closed.
func (s *Service) startClock(ctx context.Context, contest contests.Contest, participant contests.Participant, addr netip.Addr) (contests.Participant, error) {
	if !contests.ClockPending(contest, participant) {
		return participant, nil
	}
	if err := s.admit(contest, participant, addr); err != nil {
		return contests.Participant{}, err
	}
	started, err := s.people.Start(ctx, participant.ID, s.now())
	if err != nil {
		return contests.Participant{}, fmt.Errorf("%w: start the participant's clock: %w", ErrUnavailable, err)
	}
	// The gate first: a registration Start does not move — disqualified or
	// finished since the check above — comes back with its clock still
	// pending, and the participant is told what the gate says of who they
	// now are.
	if err := s.admit(contest, started, addr); err != nil {
		return contests.Participant{}, err
	}
	// A clock still pending that the gate admits is a Start that reported
	// success without starting anything: a store that broke its own contract.
	// The gate would let such a participant start forever, with no deadline
	// running, so it is refused here, as ours.
	if contests.ClockPending(contest, started) {
		return contests.Participant{}, fmt.Errorf("%w: starting the participant's clock left it unstarted", ErrUnavailable)
	}
	return started, nil
}

// resolve is the lookup both Access and AccessForEvents need before either
// asks the gate about the result: who is asking, and the contest they are
// asking about, in one round trip (Lookup.ForAccess) — every
// participant-facing read pays it, autosaves and signals included.
// Classified by the same classifyParticipant Run uses, so the two cannot
// disagree about "who is this".
func (s *Service) resolve(ctx context.Context, contestID, userID uuid.UUID) (contests.Participant, contests.Contest, error) {
	participant, contest, err := s.lookup.ForAccess(ctx, contestID, userID)
	if participant, err = classifyParticipant(participant, err, "look up the participant and the contest"); err != nil {
		return contests.Participant{}, contests.Contest{}, err
	}
	return participant, contest, nil
}

// AccessForEvents resolves who is asking and confirms they may hold the
// events channel open for contestID right now (§8, finding 4): the gate's
// MayWait, which is everything Access admits and one thing more — a published
// contest that has not started, so a participant enrolled before starts_at
// can observe the published → running transition on this channel rather than
// poll for it.
//
// Nothing else is admitted that Access would refuse: a waiting participant
// must still hold a registration that is neither disqualified nor finished,
// and call from an address the contest's own network restriction allows.
// Reading the story, the questions, or answering a question still goes
// through Access unchanged — this widens only what the channel may be held
// open for, never what a participant connected to it may do.
//
// The Standing the gate decided on is handed back with the answer, refused or
// not, because the channel has one more question than "may they wait": when it
// closes, is it over for them (Standing.Over), so it can say the contest
// finished rather than merely close. A lookup that failed decided nothing and
// hands back the zero Standing, which is over for nobody.
func (s *Service) AccessForEvents(ctx context.Context, contestID, userID uuid.UUID, addr netip.Addr) (contests.Participant, contests.Contest, contests.Standing, error) {
	participant, contest, err := s.resolve(ctx, contestID, userID)
	if err != nil {
		return contests.Participant{}, contests.Contest{}, contests.Standing{}, err
	}

	// MayWait is false only where MayAct is too, so Refusal names why.
	standing := contests.StandingOf(contest, participant, s.now(), s.grace, addr)
	if !standing.MayWait() {
		return contests.Participant{}, contests.Contest{}, standing, standing.Refusal()
	}
	return participant, contest, standing, nil
}

// AdmitRead applies, to the participant-facing read endpoints, the same
// pre-lookup rate check Run applies to itself (see Run's own doc): keyed by
// the authenticated caller's userID, against the installation's own
// perMinuteDefault, before Access ever runs its lookup.
//
// The key is bounded the same way Run's first check is bounded: one per
// authenticated account, assigned at sign-in and never supplied by the
// request, so a caller cannot mint a fresh counter just by asking (CLAUDE.md
// rule 5) — unlike cmd.ContestID, which is attacker-chosen and would let a
// caller who names a fresh random contest every time build an unbounded
// number of counters instead of spending down its own one.
//
// This shares s.rate — the same instance and the same key namespace Run's
// first check uses — rather than a second limiter of its own: a participant
// who alternates between running queries and polling the story or the
// question list spends one account-wide budget either way, not two that add
// together into double the intended rate.
func (s *Service) AdmitRead(userID uuid.UUID) error {
	return s.rate.Admit(userID.String(), s.perMinuteDefault)
}

// speaksForTheDatabase reports an error that carries PostgreSQL's own words
// about this query rather than one of ours.
//
// It asks the error what it is. This used to be written the other way round —
// "none of the answers we produce, therefore the database's" — on the
// reasoning that only the list of ours can be kept complete by hand. The
// reasoning was sound and the default was not: a runner that could not reach
// the game cluster is on neither list, so in the one contest that withholds
// the database's words its own outage was reported to the participant as the
// database refusing their query. The runner now names the database's words
// where they arrive (queryrunner.DatabaseError, set by the client in
// rpc.errorFor), so the question can be asked directly, and everything else —
// including a failure this package has never heard of — passes through as
// what it is.
func speaksForTheDatabase(err error) bool {
	var database *queryrunner.DatabaseError
	return errors.As(err, &database)
}
