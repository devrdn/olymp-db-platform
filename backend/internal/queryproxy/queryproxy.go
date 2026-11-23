// Package queryproxy takes a participant's SQL and answers with rows.
//
// It decides whether and where a query runs: it admits the asker through
// participation.Admitter with the console's own query rate, then finds which
// database is theirs. Who may act is the participation gate's decision; what
// the SQL may do, how long it runs and how much it returns belong to the Query
// Runner. The database is always looked up from the registration; a request
// never names one.
package queryproxy

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/participation"
	"github.com/devrdn/olymp-db-platform/backend/internal/provisioning"
	"github.com/devrdn/olymp-db-platform/backend/internal/queryrunner"
	"github.com/devrdn/olymp-db-platform/backend/internal/sqlpolicy"
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
// sqlpolicy's errors belong to their own lists, and admission's to
// participation's.
func Errors() []error {
	return []error{
		ErrNothingLeftToAnswer, ErrNoGameYet, ErrNoRoomForDatabase, ErrUnavailable,
		ErrDatabaseDeclined, ErrSchemaHidden, ErrResetReadOnly, ErrResetTooOften,
	}
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

// Lookup answers who is asking, what about, and on which game and database,
// in one round trip per query. The contest carries no Translations, as
// participation.Lookup's does not.
type Lookup interface {
	ForRun(ctx context.Context, contestID, userID uuid.UUID) (LookupResult, error)
}

// Databases hands out the participant's own copy, says how large it may
// grow, and remakes it on request.
type Databases interface {
	EnsureFrom(ctx context.Context, contest provisioning.Contest, registration uuid.UUID, existing provisioning.Instance, err error) (string, error)
	Quota(ctx context.Context, contest provisioning.Contest) (int64, error)
	// Reset copies the template over the registration's database and records
	// actor as who asked; provisioning.ErrNoInstance when it has none.
	Reset(ctx context.Context, actor uuid.UUID, contest provisioning.Contest, registration uuid.UUID) error
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
	// ContestID is the identifier as the caller wrote it, parsed only after
	// the rate is charged (participation.Request.Contest).
	ContestID string
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

// Service answers queries.
type Service struct {
	admitter  *participation.Admitter
	lookup    Lookup
	databases Databases
	runner    Executor
	// rate is a second instance of the Query Runner's limiter, so a query can
	// be refused before the journal writes it. It holds two key spaces: the
	// user ID, checked before any lookup, and the registration ID, checked
	// against the contest's own limit (queryBudget).
	rate *queryrunner.RateLimiter
	// resets is keyed by registration ID, so its keys are bounded by the
	// rosters (CLAUDE.md rule 5).
	resets *queryrunner.RateLimiter
	// perMinuteDefault is the rate for a contest left at zero and the ceiling
	// for any contest's own setting. Zero means no limit.
	perMinuteDefault int
	// schemas is nil until WithSchemas; see Schema.
	schemas Schemas
	// answerable is nil until WithAnswerable, and nil fails open.
	answerable Answerable
}

// New assembles the façade. Admission, the gate and the start are admitter's;
// lookup reads the game and the database in the same round trip as admission.
// It panics without either.
func New(admitter *participation.Admitter, lookup Lookup, databases Databases, runner Executor) *Service {
	if admitter == nil || lookup == nil {
		panic("queryproxy: New needs the participant admitter and the console's lookup")
	}
	return &Service{
		admitter: admitter, lookup: lookup, databases: databases, runner: runner,
		rate:             queryrunner.NewRateLimiter(0, time.Minute),
		resets:           queryrunner.NewRateLimiter(ResetsPerWindow, ResetWindow),
		perMinuteDefault: queryrunner.DefaultLimits().PerMinute,
	}
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

// queryBudget is the console's query rate as admission charges it: the
// account at the installation's rate before any lookup, since no contest is
// known yet and that rate is the ceiling of every contest's, then the
// registration at the contest's own, so every refusal after the lookup counts
// (CLAUDE.md rules 5 and 13).
type queryBudget struct{ s *Service }

func (b queryBudget) Charge(_ context.Context, account uuid.UUID) error {
	return b.s.rate.Admit(account.String(), b.s.perMinuteDefault)
}

func (b queryBudget) ChargeRegistration(_ context.Context, participant contests.Participant, contest contests.Contest) error {
	return b.s.rate.Admit(participant.ID.String(), effectiveRateLimit(contest.Settings.QueryRateLimitPerMin, b.s.perMinuteDefault))
}

// runLookup is Lookup.ForRun as admission's lookup, keeping the game and the
// database it found so the console reads its row once.
type runLookup struct {
	lookup Lookup
	found  LookupResult
}

func (l *runLookup) ForAccess(ctx context.Context, contestID, userID uuid.UUID) (contests.Participant, contests.Contest, error) {
	found, err := l.lookup.ForRun(ctx, contestID, userID)
	l.found = found
	return found.Participant, found.Contest, err
}

// admit admits the asker through participation, spending budget, and keeps
// the game and the database the same round trip found.
func (s *Service) admit(ctx context.Context, budget participation.Budget, contestID string, userID uuid.UUID,
	addr netip.Addr, session, userAgent string) (*participation.Admission, LookupResult, error) {
	found := &runLookup{lookup: s.lookup}
	admission, err := s.admitter.Admit(ctx, budget, participation.Request{
		Contest: contestID, Account: userID, Address: addr, Session: session, UserAgent: userAgent,
	}, participation.LookedUpBy(found))
	if err != nil {
		return nil, LookupResult{}, err
	}
	return admission, found.found, nil
}

// Run answers one query, or says why it will not. Checks run cheapest first,
// after admission (queryBudget, the gate and the visit), so a query refused
// below still counts as the registration being used from that address:
//
//  1. The query length.
//  2. Anything left to answer (one round trip). Before provisioning, so a
//     participant who cannot score cannot make the service build a database.
//  3. The game exists, then the database is provisioned.
//  4. Only then is the participant's start recorded, which under individual
//     timing starts their clock, so a refused request never costs them time.
//     A query the SQL validator later rejects still records it: the
//     participant did act against the game.
func (s *Service) Run(ctx context.Context, cmd Command) (*queryrunner.Result, error) {
	admission, lookup, err := s.admit(ctx, queryBudget{s}, cmd.ContestID, cmd.UserID, cmd.Address, cmd.Session, cmd.UserAgent)
	if err != nil {
		return nil, err
	}
	participant := admission.Participant

	if len(cmd.SQL) > sqlpolicy.MaxQueryBytes {
		return nil, &sqlpolicy.Refusal{Code: sqlpolicy.CodeTooLong, Subject: fmt.Sprintf("%d bytes", len(cmd.SQL))}
	}

	if s.answerable != nil {
		left, err := s.answerable.AnswerableLeft(ctx, admission.Contest.ID, participant.ID)
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

	if err := admission.Start(ctx); err != nil {
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

// speaksForTheDatabase reports an error carrying PostgreSQL's own words about
// the query (queryrunner.DatabaseError). It asks the error what it is rather
// than excluding known errors of ours, so an unknown failure, such as the
// runner losing the cluster, is never passed off as the database refusing.
func speaksForTheDatabase(err error) bool {
	var database *queryrunner.DatabaseError
	return errors.As(err, &database)
}
