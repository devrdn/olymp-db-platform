// Package queryproxy takes a participant's SQL and answers with rows.
//
// It decides whether and where a query runs: who is asking, whether the
// participation gate (contests.Gate.StandingOf) admits them now, and which
// database is theirs. What the SQL may do, how long it runs and how much it
// returns belong to the Query Runner. The database is always looked up from
// the registration; a request never names one.
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

// Why a query was not run, before it was looked at. The gate's refusals are
// contests' sentinels and are passed through as the gate gives them.
var (
	// ErrNothingLeftToAnswer means every question of the contest is either
	// answered correctly or out of attempts, so the console stops taking
	// queries. It is not contests.ErrParticipantFinished and writes no status:
	// finishing closes the whole play screen, while this closes only the
	// console and lifts itself when an organiser raises max_attempts or
	// reveals a question.
	ErrNothingLeftToAnswer = errors.New("no question of this contest is still answerable")
	// ErrNoGameYet is a contest whose game database was never built.
	ErrNoGameYet = errors.New("the contest has no game database yet")
	// ErrNoRoomForDatabase is the game cluster out of its configured disk
	// budget. Kept apart from ErrUnavailable because an operator can act on it
	// (raise GAME_CLUSTER_MAX_BYTES, reclaim a contest, add a volume).
	ErrNoRoomForDatabase = errors.New("the game cluster has no room for this participant's database")
	// ErrUnavailable is this service failing, as opposed to the query being
	// refused. It must not reach the participant as "your request was bad".
	ErrUnavailable = errors.New("the query could not be answered")
	// ErrDatabaseDeclined replaces the database's own error in a contest that
	// hides its schema: "relation x does not exist" would let a participant
	// rebuild the hidden catalogue by guessing names.
	ErrDatabaseDeclined = errors.New("the database refused the query")
)

// Errors lists every sentinel this package declares, so internal/api can
// answer each one (CLAUDE.md rule 1). The gate's, the Query Runner's and
// sqlpolicy's errors belong to their own lists.
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
	// have not already begun. See contests.RegistrationRepository.Start for
	// the concurrency guarantee.
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
// runner.
type LookupResult struct {
	Participant contests.Participant
	Contest     contests.Contest
	// GameErr is provisioning.ErrNoGame for a contest with no ready template.
	// Run checks it only after the rate limit and the gate.
	Game    provisioning.Contest
	GameErr error
	// InstanceErr is provisioning.ErrNoInstance for a registration with no
	// database yet. Both go to Databases.EnsureFrom as they are, so a current
	// copy costs no extra read.
	Instance    provisioning.Instance
	InstanceErr error
}

// Lookup answers who is asking and what about, in one round trip per request.
type Lookup interface {
	ForRun(ctx context.Context, contestID, userID uuid.UUID) (LookupResult, error)
	// ForAccess is the participant and the contest, for read endpoints that
	// need neither the game nor a database.
	ForAccess(ctx context.Context, contestID, userID uuid.UUID) (contests.Participant, contests.Contest, error)
}

// Databases hands out the participant's own copy, and says how large it may
// grow. Only defaultLookup calls Instance.
type Databases interface {
	Instance(ctx context.Context, registration uuid.UUID) (provisioning.Instance, error)
	EnsureFrom(ctx context.Context, contest provisioning.Contest, registration uuid.UUID, existing provisioning.Instance, err error) (string, error)
	Quota(ctx context.Context, contest provisioning.Contest) (int64, error)
}

// Answerable answers whether a registration still has a question it could
// answer. "Answerable" must match what contests.Reader shows on the question
// list (visible, not answered correctly, attempts left), or the console closes
// on somebody with work still to do.
type Answerable interface {
	AnswerableLeft(ctx context.Context, contestID, registrationID uuid.UUID) (bool, error)
}

// Executor runs the query and journals it.
type Executor interface {
	Run(ctx context.Context, req queryrunner.Request, origin queryrunner.Origin) (*queryrunner.Result, error)
}

// Command is one participant asking one question. It carries no database:
// the database is always looked up from the registration.
type Command struct {
	ContestID uuid.UUID
	UserID    uuid.UUID
	SQL       string
	// Address is resolved by the HTTP layer. It is checked against the
	// contest's network restriction and written into the journal row.
	Address netip.Addr
	// RequestID ties the journal row to the request in the technical logs.
	RequestID uuid.UUID
	// Session is monitor.SessionTag, never the token.
	Session   string
	UserAgent string
}

// Watcher hears of every admitted request, to detect a changed address or a
// second session (monitor.Tracker). It records and never refuses.
type Watcher interface {
	Observe(ctx context.Context, visit monitor.Visit)
}

// Service answers queries.
type Service struct {
	people    People
	databases Databases
	runner    Executor
	// rate is a second instance of the Query Runner's limiter, so a query can
	// be refused before the journal writes it. It holds two key spaces: the
	// user ID, checked before any lookup, and the registration ID, checked
	// against the contest's own limit.
	rate *queryrunner.RateLimiter
	// perMinuteDefault is the rate for a contest left at zero and the ceiling
	// for any contest's own setting. Zero means no limit.
	perMinuteDefault int
	now              func() time.Time
	// gate carries the installation's latency grace past a deadline. The grace
	// is never shown to a participant; anything that renders a deadline must
	// use contests.Deadline.
	gate *contests.Gate
	// schemas is nil until WithSchemas; see Schema.
	schemas Schemas
	// answerable is nil until WithAnswerable, and nil fails open.
	answerable Answerable
	lookup     Lookup
	// watcher is nil until WithWatcher; nil watches nothing.
	watcher Watcher
}

// New assembles the façade around gate. It panics on a nil gate: no grace
// assumed here is sure to match the rest of the installation's.
func New(people People, contestStore Contests, games Games, databases Databases, runner Executor, gate *contests.Gate) *Service {
	if gate == nil {
		panic("queryproxy: New needs the participation gate")
	}
	return &Service{
		people: people, databases: databases, runner: runner,
		lookup:           defaultLookup{people: people, contests: contestStore, games: games, databases: databases},
		rate:             queryrunner.NewRateLimiter(0, time.Minute),
		perMinuteDefault: queryrunner.DefaultLimits().PerMinute,
		now:              func() time.Time { return time.Now().UTC() },
		gate:             gate,
	}
}

// defaultLookup is New's Lookup: separate calls in admission order. A
// deployment replaces it with single queries (WithLookup).
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

// WithClock overrides the clock the gate is asked with, for tests.
func (s *Service) WithClock(now func() time.Time) *Service {
	if now != nil {
		s.now = now
	}
	return s
}

// WithPerMinuteDefault sets the installation's rate, so this pre-check matches
// the QUERY_PER_MINUTE the Query Runner runs with. Zero means no limit, as in
// config.Runner.PerMinute. A negative value panics, as it does in
// contests.NewGate: Config.Load never produces one.
func (s *Service) WithPerMinuteDefault(perMinute int) *Service {
	if perMinute < 0 {
		panic(fmt.Sprintf("queryproxy: negative per-minute default %d", perMinute))
	}
	s.perMinuteDefault = perMinute
	return s
}

// WithAnswerable supplies the reader behind ErrNothingLeftToAnswer.
//
// Without it the query runs: this check fails open, unlike WithSchemas.
// Failing closed would refuse every query of every contest, mid-contest, and
// look the same as a contest that is over.
func (s *Service) WithAnswerable(answerable Answerable) *Service {
	s.answerable = answerable
	return s
}

// WithWatcher reports every query admission lets through to watcher, before
// the length or content checks, so a refused query still counts as the
// registration being used from that address.
func (s *Service) WithWatcher(watcher Watcher) *Service {
	s.watcher = watcher
	return s
}

// WithLookup replaces New's three-call default with a single query.
func (s *Service) WithLookup(lookup Lookup) *Service {
	s.lookup = lookup
	return s
}

// effectiveRateLimit resolves a contest's rate against the installation's.
// Zero means no limit on either side. A contest may set a stricter rate, never
// a looser one: a higher rate would be refused by the Query Runner anyway,
// after this façade had already paid for the journal write.
func effectiveRateLimit(contestLimit, installationLimit int) int {
	if contestLimit <= 0 {
		return installationLimit
	}
	if installationLimit > 0 && contestLimit > installationLimit {
		return installationLimit
	}
	return contestLimit
}

// Run answers one query, or says why it will not. Checks run cheapest first,
// except the two rate checks, which come before anything they protect
// (CLAUDE.md rules 5 and 13):
//
//  1. Rate by user ID, before any lookup. ContestID is caller-chosen and
//     unbounded; the user ID is one key per account. The installation's rate
//     is used because no contest is known yet, and it is the ceiling of every
//     contest's rate.
//  2. One lookup, then rate by registration against the contest's own limit,
//     so every refusal below is counted.
//  3. The gate, from values in hand, then the query length.
//  4. Anything left to answer (one round trip). Before provisioning, so a
//     participant who cannot score cannot make the service build a database.
//  5. The game exists, then the database is provisioned.
//  6. Only then does an individual participant's clock start, so a refused
//     request never costs them time. A query the SQL validator later rejects
//     still starts it: the participant did act against the game.
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

	limit := effectiveRateLimit(contest.Settings.QueryRateLimitPerMin, s.perMinuteDefault)
	if err = s.rate.Admit(participant.ID.String(), limit); err != nil {
		return nil, err
	}

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
	// Only a read-write database can grow, and the quota costs a template-size
	// lookup on the game cluster, so read-only contests skip it.
	var quota int64
	if game.Policy.Mode == sqlpolicy.ModeReadWrite {
		if quota, err = s.databases.Quota(ctx, game); err != nil {
			return nil, fmt.Errorf("%w: work out the size limit: %w", ErrUnavailable, err)
		}
	}

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

	// Refusals and runner outcomes go back unwrapped: each carries a code the
	// interface translates. Only the database's own words are withheld, and
	// only where the contest hides its schema.
	if err != nil && !game.Policy.AllowCatalog && speaksForTheDatabase(err) {
		return nil, ErrDatabaseDeclined
	}
	return result, err
}

// provisionFailure maps an EnsureFrom error for both Run and Schema, so a
// participant is told the same thing about the cluster from either.
func provisionFailure(err error) error {
	if errors.Is(err, provisioning.ErrClusterFull) {
		return fmt.Errorf("%w: %w", ErrNoRoomForDatabase, err)
	}
	return fmt.Errorf("%w: provide the participant's database: %w", ErrUnavailable, err)
}

// classifyParticipant maps a missing registration to contests.ErrNotAParticipant,
// the same refusal the gate gives a disqualified one, so probing a contest
// reveals nothing about who is on it. Any other error is ErrUnavailable.
func classifyParticipant(participant contests.Participant, err error, wrap string) (contests.Participant, error) {
	switch {
	case errors.Is(err, contests.ErrParticipantNotFound):
		return contests.Participant{}, contests.ErrNotAParticipant
	case err != nil:
		return contests.Participant{}, fmt.Errorf("%w: %s: %w", ErrUnavailable, wrap, err)
	}
	return participant, nil
}

// admit asks the gate whether participant may act in contest now, from addr.
// It never starts a clock.
func (s *Service) admit(contest contests.Contest, participant contests.Participant, addr netip.Addr) error {
	return s.gate.StandingOf(contest, participant, s.now(), addr).Refusal()
}

// Access resolves who is asking and confirms they may currently interact with
// contestID, for the read endpoints (story, questions). It is the same gate
// Run asks, without Run's game lookup or provisioning. It starts no clock (see
// StartOnRead) and charges no rate; AdmitRead is the separate rate check for
// these reads.
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
// the contest's content (story, question list or schema) and returns the
// participant as it now stands.
//
// Otherwise a participant could read everything, prepare offline, and spend
// their duration only on typing. Callers invoke it only after a successful
// read: a refused read showed nothing and must cost nothing. The events
// channel and leaderboards never call it. The gate is asked here, not trusted
// from an earlier Access, and asked again after Start (see startClock).
func (s *Service) StartOnRead(ctx context.Context, contest contests.Contest, participant contests.Participant, addr netip.Addr) (contests.Participant, error) {
	return s.startClock(ctx, contest, participant, addr)
}

// startClock checks the gate, starts the clock, and checks again: the start
// may be another request's, and time may already be up. A participant whose
// clock is not pending costs nothing.
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
	// Gate first: a registration disqualified or finished meanwhile comes back
	// still pending, and the gate's refusal says why.
	if err := s.admit(contest, started, addr); err != nil {
		return contests.Participant{}, err
	}
	// Still pending yet admitted means the store broke its contract. Fail
	// closed: the gate would otherwise admit a participant with no deadline.
	if contests.ClockPending(contest, started) {
		return contests.Participant{}, fmt.Errorf("%w: starting the participant's clock left it unstarted", ErrUnavailable)
	}
	return started, nil
}

// resolve is the lookup Access and AccessForEvents share, classified the same
// way Run classifies it.
func (s *Service) resolve(ctx context.Context, contestID, userID uuid.UUID) (contests.Participant, contests.Contest, error) {
	participant, contest, err := s.lookup.ForAccess(ctx, contestID, userID)
	if participant, err = classifyParticipant(participant, err, "look up the participant and the contest"); err != nil {
		return contests.Participant{}, contests.Contest{}, err
	}
	return participant, contest, nil
}

// AccessForEvents confirms the caller may hold the events channel open for
// contestID now (the gate's MayWait): everything Access admits, plus a
// published contest that has not started, so the participant sees it begin.
// It widens only what the channel may wait for, never what a participant may
// do.
//
// The Standing is returned either way so the channel can tell "finished" from
// a plain close (Standing.Over). A failed lookup returns the zero Standing,
// which is over for nobody.
func (s *Service) AccessForEvents(ctx context.Context, contestID, userID uuid.UUID, addr netip.Addr) (contests.Participant, contests.Contest, contests.Standing, error) {
	participant, contest, err := s.resolve(ctx, contestID, userID)
	if err != nil {
		return contests.Participant{}, contests.Contest{}, contests.Standing{}, err
	}

	// MayWait is false only where MayAct is too, so Refusal names why.
	standing := s.gate.StandingOf(contest, participant, s.now(), addr)
	if !standing.MayWait() {
		return contests.Participant{}, contests.Contest{}, standing, standing.Refusal()
	}
	return participant, contest, standing, nil
}

// AdmitRead applies Run's pre-lookup rate check to the read endpoints: keyed
// by user ID (bounded, CLAUDE.md rule 5), against the installation's rate. It
// shares Run's limiter and keys, so queries and reads spend one budget.
func (s *Service) AdmitRead(userID uuid.UUID) error {
	return s.rate.Admit(userID.String(), s.perMinuteDefault)
}

// speaksForTheDatabase reports an error carrying PostgreSQL's own words about
// the query (queryrunner.DatabaseError). It asks the error what it is rather
// than excluding known errors of ours, so an unknown failure, such as the
// runner losing the cluster, is never passed off as the database refusing.
func speaksForTheDatabase(err error) bool {
	var database *queryrunner.DatabaseError
	return errors.As(err, &database)
}
