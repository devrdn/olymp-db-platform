// Package participation admits a participant's request to a contest. One call,
// Admitter.Admit, spends the request's budget, finds who is asking, asks the
// participation gate (contests.Gate.StandingOf) and tells the monitor of the
// visit, in that order; the Admission it hands back records the participant's
// start only when the caller says the content is ready.
//
// It does not decide who may act (the gate does), keep any budget but the play
// screen's reads (each resource keeps its own and hands it in), or know HTTP.
package participation

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/monitor"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
	"github.com/google/uuid"
)

var (
	// ErrUnavailable is this service failing to admit, as opposed to the gate
	// refusing. It must not reach the participant as "you may not".
	ErrUnavailable = errors.New("the participant could not be admitted")
	// ErrInvalidContest is a contest identifier that is not one. It is found
	// after the budget is charged, so a malformed one still counts.
	ErrInvalidContest = errors.New("contest identifier is not valid")
)

// Errors lists every sentinel this package declares, so internal/api can
// answer each one (CLAUDE.md rule 1). The gate's refusals belong to contests.
func Errors() []error {
	return []error{ErrUnavailable, ErrInvalidContest, ErrTooManyReads}
}

// Lookup finds who is asking and what about, in one round trip.
//
// The contest carries no Translations: no participant path shows a title or
// description, and every language's description would otherwise ride on each
// call. A path that needs one reads the contest by ID.
type Lookup interface {
	// ForAccess answers contests.ErrParticipantNotFound for an account with no
	// registration in the contest, or no such contest.
	ForAccess(ctx context.Context, contestID, account uuid.UUID) (contests.Participant, contests.Contest, error)
}

// Watcher hears of every admitted request, to detect a changed address or a
// second session (monitor.Tracker). It records, never refuses, and bounds its
// own time.
type Watcher interface {
	Observe(ctx context.Context, visit monitor.Visit)
}

// Request is one participant request, as the HTTP layer resolved it.
type Request struct {
	// Contest is the identifier as the caller named it, parsed only once the
	// budget is charged (CLAUDE.md rule 13).
	Contest string
	Account uuid.UUID
	// Address is the exact client address, checked against the contest's
	// network restriction and recorded in the visit.
	Address netip.Addr
	// Session is monitor.SessionTag, never the token.
	Session   string
	UserAgent string
}

// Config is what an Admitter is built from.
type Config struct {
	Lookup Lookup
	// People records a participant's start (contests.StartParticipant).
	People contests.Starter
	// Gate carries the installation's one deadline grace; the composition root
	// hands every consumer the same one.
	Gate *contests.Gate
	// Audit records a start inside the transaction UnitOfWork opens.
	Audit      *audit.Recorder
	UnitOfWork storage.UnitOfWork
	// Watcher is nil to watch nothing.
	Watcher Watcher
	// Now is nil for the wall clock in UTC.
	Now func() time.Time
}

// Admitter admits participant requests. It holds no state of its own between
// requests.
type Admitter struct {
	lookup  Lookup
	people  contests.Starter
	gate    *contests.Gate
	audit   *audit.Recorder
	uow     storage.UnitOfWork
	watcher Watcher
	now     func() time.Time
}

// New assembles an Admitter. It panics without a lookup, a store to start
// participants in, the gate, or the audit trail and its unit of work: no
// default for any of them is safe, and a start is never recorded unaudited.
func New(cfg Config) *Admitter {
	if cfg.Lookup == nil || cfg.People == nil {
		panic("participation: New needs a lookup and a store to start participants in")
	}
	if cfg.Gate == nil {
		panic("participation: New needs the participation gate")
	}
	if cfg.Audit == nil || cfg.UnitOfWork == nil {
		panic("participation: New needs the audit recorder and its unit of work")
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Admitter{
		lookup: cfg.Lookup, people: cfg.People, gate: cfg.Gate,
		audit: cfg.Audit, uow: cfg.UnitOfWork, watcher: cfg.Watcher, now: now,
	}
}

// Option changes how one request is admitted.
type Option func(*options)

type options struct {
	mayWait    bool
	lookup     Lookup
	unobserved bool
}

// MayWait admits the request while the gate's MayWait holds rather than
// MayAct: also a published contest that has not started, so the events channel
// can wait across the start. It widens what a request may wait for, never what
// it may do: Start still asks MayAct.
func MayWait() Option {
	return func(o *options) { o.mayWait = true }
}

// WithoutVisit admits the request without telling the Watcher of it. The events
// channel takes it, being one long connection rather than a stream of requests
// that the monitor does not observe (docs/ARCHITECTURE.md §9.4); the signals
// route takes it and calls Admission.Visit once its body is read.
func WithoutVisit() Option {
	return func(o *options) { o.unobserved = true }
}

// LookedUpBy finds the participant and contest with lookup for this request
// instead of the Admitter's own, for a caller that reads more of the same row
// in the same round trip (the console's game and database).
func LookedUpBy(lookup Lookup) Option {
	return func(o *options) { o.lookup = lookup }
}

// Admit admits req, or says why not. In order:
//
//  1. budget is charged to the account, before the contest identifier is
//     even parsed (CLAUDE.md rule 13): the identifier is the caller's choice,
//     the account one bounded key (rule 5), and a refused request, a
//     malformed one included, still spends it.
//  2. One lookup. Nobody on the roster is contests.ErrNotAParticipant, the
//     gate's own answer to a disqualified registration, so a probe learns
//     nothing about who is on it.
//  3. A RegistrationBudget is charged to the registration found, before the
//     gate, so a request the gate refuses still counts.
//  4. The gate, from the values in hand. A refusal is a *Refused.
//  5. The visit (Admission.Visit), for an admitted request only, unless
//     WithoutVisit.
//
// It starts nobody's clock: that is Admission.Start, once the content is
// ready.
func (a *Admitter) Admit(ctx context.Context, budget Budget, req Request, opts ...Option) (*Admission, error) {
	if budget == nil {
		return nil, fmt.Errorf("%w: the request names no budget", ErrUnavailable)
	}
	if err := budget.Charge(ctx, req.Account); err != nil {
		return nil, err
	}
	contestID, err := uuid.Parse(req.Contest)
	if err != nil {
		return nil, ErrInvalidContest
	}

	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.lookup == nil {
		o.lookup = a.lookup
	}
	participant, contest, err := a.find(ctx, o.lookup, contestID, req.Account)
	if err != nil {
		return nil, err
	}
	if registration, ok := budget.(RegistrationBudget); ok {
		if err := registration.ChargeRegistration(ctx, participant, contest); err != nil {
			return nil, err
		}
	}
	admission := &Admission{Participant: participant, Contest: contest, admitter: a, request: req, contestID: contestID, options: o}
	if err := admission.decide(); err != nil {
		return nil, err
	}
	if !o.unobserved {
		admission.Visit(ctx)
	}
	return admission, nil
}

func (a *Admitter) find(ctx context.Context, lookup Lookup, contestID, account uuid.UUID) (contests.Participant, contests.Contest, error) {
	participant, contest, err := lookup.ForAccess(ctx, contestID, account)
	if err != nil {
		return contests.Participant{}, contests.Contest{}, LookupError(err)
	}
	return participant, contest, nil
}

// LookupError is how admission reads a failed lookup of a participant, for any
// caller that looks one up again: no registration is contests.ErrNotAParticipant,
// the gate's answer to a disqualified one, so a probe learns nothing about the
// roster, and anything else is ErrUnavailable.
func LookupError(err error) error {
	if errors.Is(err, contests.ErrParticipantNotFound) {
		return contests.ErrNotAParticipant
	}
	return fmt.Errorf("%w: look up the participant and the contest: %w", ErrUnavailable, err)
}

// Refused is the gate refusing a request. It unwraps to the gate's sentinel
// (Standing.Refusal), which callers answer; Standing says whether the contest
// is over for the participant, which only the events channel reads.
type Refused struct {
	Standing contests.Standing
}

func (r *Refused) Error() string { return r.Standing.Refusal().Error() }
func (r *Refused) Unwrap() error { return r.Standing.Refusal() }

// Admission is one admitted request: who, in which contest, as admission found
// them.
type Admission struct {
	Participant contests.Participant
	Contest     contests.Contest

	admitter  *Admitter
	request   Request
	contestID uuid.UUID
	options   options
}

// Visit tells the Watcher of this admitted request. Admit calls it unless
// WithoutVisit; a route that refuses a malformed body after admission takes
// WithoutVisit and calls it once the body is read, so a refused body is no
// visit.
func (adm *Admission) Visit(ctx context.Context) {
	if adm.admitter.watcher == nil {
		return
	}
	adm.admitter.watcher.Observe(ctx, monitor.Visit{
		Contest: adm.Contest.ID, Registration: adm.Participant.ID,
		Address: adm.request.Address, Session: adm.request.Session, UserAgent: adm.request.UserAgent,
	})
}

// decide asks the gate of the admission's values at this instant.
func (adm *Admission) decide() error {
	standing := adm.admitter.gate.StandingOf(adm.Contest, adm.Participant, adm.admitter.now(), adm.request.Address)
	admitted := standing.MayAct()
	if adm.options.mayWait {
		admitted = standing.MayWait()
	}
	if !admitted {
		return &Refused{Standing: standing}
	}
	return nil
}

// Recheck admits the same request again, for a channel held open past the
// request that opened it: a fresh lookup and the gate under the same options.
// It charges no budget, since the opening paid for it, and records no visit.
func (adm *Admission) Recheck(ctx context.Context) (*Admission, error) {
	participant, contest, err := adm.admitter.find(ctx, adm.options.lookup, adm.contestID, adm.request.Account)
	if err != nil {
		return nil, err
	}
	again := &Admission{Participant: participant, Contest: contest, admitter: adm.admitter,
		request: adm.request, contestID: adm.contestID, options: adm.options}
	if err := again.decide(); err != nil {
		return nil, err
	}
	return again, nil
}

// Start records the participant's start, which under individual timing starts
// their clock. Call it once the content is ready and before it is sent: a
// refused request starts nothing, and nothing is sent to a participant whose
// start could not be recorded. Otherwise an individual participant could read
// everything, prepare offline, and spend their time only on typing.
//
// The gate is asked of MayAct before the start, not trusted from Admit, and
// again of the registration the store hands back: that may be another
// request's earlier start whose time is already up, or a registration
// disqualified in between, which the store does not move. A participant
// already started costs nothing. On success Participant is the started one.
func (adm *Admission) Start(ctx context.Context) error {
	if !adm.Participant.AwaitsStart() {
		return nil
	}
	a := adm.admitter
	if err := a.mayAct(adm.Contest, adm.Participant, adm.request.Address); err != nil {
		return err
	}
	started, err := contests.StartParticipant(ctx, a.uow, a.audit, a.people, adm.Participant.ID, a.now())
	if err != nil {
		return fmt.Errorf("%w: record the participant's start: %w", ErrUnavailable, err)
	}
	if err := a.mayAct(adm.Contest, started, adm.request.Address); err != nil {
		return err
	}
	// Pending yet admitted means the store broke its contract; fail closed,
	// or the gate would admit a participant with no deadline. Under fixed
	// timing the deadline never depends on the start.
	if contests.ClockPending(adm.Contest, started) {
		return fmt.Errorf("%w: recording the participant's start left their clock pending", ErrUnavailable)
	}
	adm.Participant = started
	return nil
}

func (a *Admitter) mayAct(contest contests.Contest, participant contests.Participant, addr netip.Addr) error {
	standing := a.gate.StandingOf(contest, participant, a.now(), addr)
	if !standing.MayAct() {
		return &Refused{Standing: standing}
	}
	return nil
}
