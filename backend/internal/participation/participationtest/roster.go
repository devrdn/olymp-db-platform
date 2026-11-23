// Package participationtest is an in-memory roster for tests of anything
// admitted through participation: the contest and participant a lookup finds,
// and the starts it records. It does not decide anything: the real
// participation.Admitter and contests.Gate do, over what a test stages here.
package participationtest

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/participation"
	"github.com/google/uuid"
)

// Roster is participation.Lookup and the contests.Starter admission starts
// participants in (participation.Config.People), over one staged contest. A participant staged with Stage is found by every account; one
// staged with StageFor only by that account. Any other contest or account is
// contests.ErrParticipantNotFound, as postgres.Registrations answers it.
type Roster struct {
	mu          sync.Mutex
	contest     contests.Contest
	participant *contests.Participant
	byAccount   map[uuid.UUID]contests.Participant
	err         error
	startErr    error
	delay       time.Duration
	lookups     int
	askedAbout  uuid.UUID
	started     []uuid.UUID
}

func NewRoster() *Roster {
	return &Roster{byAccount: map[uuid.UUID]contests.Participant{}}
}

// Stage makes contest the one on the roster and participant who every account
// finds there.
func (r *Roster) Stage(contest contests.Contest, participant contests.Participant) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.contest, r.participant = contest, &participant
}

// StageFor makes account find participant in the staged contest.
func (r *Roster) StageFor(account uuid.UUID, participant contests.Participant) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byAccount[account] = participant
}

// SetContest replaces the staged contest, as an organiser or the scheduler
// moving it would.
func (r *Roster) SetContest(contest contests.Contest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.contest = contest
}

// SetParticipant replaces the participant every account finds.
func (r *Roster) SetParticipant(participant contests.Participant) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.participant = &participant
}

func (r *Roster) Contest() contests.Contest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.contest
}

// Participant is who every account finds, as it now stands.
func (r *Roster) Participant() contests.Participant {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.participant == nil {
		return contests.Participant{}
	}
	return *r.participant
}

// Fail makes every lookup answer err; nil finds the staged state again.
func (r *Roster) Fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

// FailStart makes every start's write answer err.
func (r *Roster) FailStart(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startErr = err
}

// Delay holds every lookup for d, standing for a slow database round trip.
func (r *Roster) Delay(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.delay = d
}

// Lookups is how many lookups ran, so a test can tell a refusal made before
// the lookup from one made after it.
func (r *Roster) Lookups() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lookups
}

// AskedAbout is the contest the last lookup was asked about.
func (r *Roster) AskedAbout() uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.askedAbout
}

// Started lists the registrations whose start this roster recorded, in order.
func (r *Roster) Started() []uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uuid.UUID(nil), r.started...)
}

// OutsideEveryTestNetwork is a network no test request comes from:
// httptest.NewRequest's 192.0.2.1 and every address a test names lie outside
// TEST-NET-2.
var OutsideEveryTestNetwork = netip.MustParsePrefix("198.51.100.0/24")

// Refuse stages the state the gate refuses with err for every account,
// keeping the staged contest's and participants' identifiers. It panics on an
// error that is not one of the gate's refusals.
func (r *Roster) Refuse(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	participant := contests.Participant{}
	if r.participant != nil {
		participant = *r.participant
	}
	contest := Running(r.contest.ID)
	contest.Languages = r.contest.Languages
	status := ""
	switch {
	case errors.Is(err, contests.ErrNotAParticipant):
		status = contests.RegistrationDisqualified
	case errors.Is(err, contests.ErrParticipantFinished):
		status = contests.RegistrationFinished
	case errors.Is(err, contests.ErrContestEnded):
		contest.Status = contests.StatusFinished
	case errors.Is(err, contests.ErrContestNotRunning):
		contest.Status = contests.StatusDraft
	case errors.Is(err, contests.ErrDeadlinePassed):
		ended := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		contest.EndsAt = &ended
	case errors.Is(err, contests.ErrAddressNotAllowed):
		contest.AllowedCIDRs = []netip.Prefix{OutsideEveryTestNetwork}
	default:
		panic(fmt.Sprintf("participationtest: %v is not a refusal of the gate", err))
	}
	if status != "" {
		participant.Status = status
		for account, p := range r.byAccount {
			p.Status = status
			r.byAccount[account] = p
		}
	}
	r.contest = contest
	if r.participant != nil {
		r.participant = &participant
	}
}

// Running is a running contest under fixed timing whose window stays open for
// a day from now.
func Running(id uuid.UUID) contests.Contest {
	ends := time.Now().Add(24 * time.Hour)
	return contests.Contest{ID: id, Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &ends}
}

func (r *Roster) ForAccess(ctx context.Context, contestID, account uuid.UUID) (contests.Participant, contests.Contest, error) {
	r.mu.Lock()
	r.lookups++
	r.askedAbout = contestID
	delay := r.delay
	r.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return contests.Participant{}, contests.Contest{}, ctx.Err()
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return contests.Participant{}, contests.Contest{}, r.err
	}
	if contestID != r.contest.ID {
		return contests.Participant{}, contests.Contest{}, contests.ErrParticipantNotFound
	}
	if p, ok := r.byAccount[account]; ok {
		return p, r.contest, nil
	}
	if r.participant == nil {
		return contests.Participant{}, contests.Contest{}, contests.ErrParticipantNotFound
	}
	return *r.participant, r.contest, nil
}

// Start mirrors postgres.Registrations.Start: it sets StartedAt and the active
// status together, once, and reports recording it only the first time.
func (r *Roster) Start(_ context.Context, registrationID uuid.UUID, now time.Time) (contests.Participant, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.startErr != nil {
		return contests.Participant{}, false, r.startErr
	}
	start := func(p contests.Participant) (contests.Participant, bool) {
		if p.StartedAt != nil || p.Status == contests.RegistrationDisqualified || p.Status == contests.RegistrationFinished {
			return p, false
		}
		p.StartedAt, p.Status = &now, contests.RegistrationActive
		r.started = append(r.started, p.ID)
		return p, true
	}
	if r.participant != nil && r.participant.ID == registrationID {
		started, recorded := start(*r.participant)
		r.participant = &started
		return started, recorded, nil
	}
	for account, p := range r.byAccount {
		if p.ID == registrationID {
			started, recorded := start(p)
			r.byAccount[account] = started
			return started, recorded, nil
		}
	}
	return contests.Participant{}, false, contests.ErrParticipantNotFound
}

// NewAdmitter is the real participation.Admitter over roster, with a gate of
// no grace and an audit trail that keeps nothing. watcher may be nil, and so
// may now, which is the wall clock.
func NewAdmitter(roster *Roster, watcher participation.Watcher, now func() time.Time) *participation.Admitter {
	return participation.New(participation.Config{
		Lookup: roster, People: roster, Gate: contests.NewGate(0),
		Audit: audit.New(Discard{}), UnitOfWork: Discard{},
		Watcher: watcher, Now: now,
	})
}

// Discard is an audit sink that keeps nothing and a unit of work with no
// transaction, for a test that does not read the trail.
type Discard struct{}

func (Discard) Do(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }
func (Discard) Append(context.Context, audit.Entry) error                    { return nil }
func (Discard) AppendMany(context.Context, []audit.Entry) error              { return nil }
