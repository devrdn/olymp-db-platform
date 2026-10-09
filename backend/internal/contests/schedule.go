package contests

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/google/uuid"
)

// ScheduleRepository is what the background scheduler needs from storage
// (docs/ARCHITECTURE.md §8).
type ScheduleRepository interface {
	// TryLock takes the advisory lock every replica's tick competes for,
	// inside the ambient transaction, so it is released when that ends and a
	// replica dying mid-tick blocks nobody. false means another replica has
	// this tick.
	TryLock(ctx context.Context) (bool, error)
	// DueToStart returns every published contest whose starts_at has arrived
	// by the database clock, in the projection ByID returns, so a tick's cost
	// scales with contests actually due.
	DueToStart(ctx context.Context) ([]Contest, error)
	// SetStatus is Repository.SetStatus: it refuses if a manual Transition
	// moved the contest first.
	SetStatus(ctx context.Context, id uuid.UUID, from, to string) error
	// AdvanceFinished moves to finished every running contest whose ends_at
	// plus grace has passed by the database clock, and returns their ids.
	// The grace is the gate's, so no request inside it is refused as
	// finished. A contest without ends_at never matches.
	AdvanceFinished(ctx context.Context, grace time.Duration) ([]uuid.UUID, error)
}

// scheduleStories, scheduleQuestions, scheduleRoster and scheduleCovers are
// the narrow stores the publish gate reads (CLAUDE.md Go layout rule 3).
type scheduleStories interface {
	ByContest(ctx context.Context, contestID uuid.UUID) (Story, error)
}

type scheduleQuestions interface {
	List(ctx context.Context, contestID uuid.UUID) ([]Question, error)
}

type scheduleRoster interface {
	RegisteredWithPermission(ctx context.Context, contestID uuid.UUID, permission string) ([]string, error)
}

type scheduleCovers interface {
	// Attribution returns the credit line of the contest's uploaded cover,
	// and false when it has none. A drawn cover needs no attribution.
	Attribution(ctx context.Context, contestID uuid.UUID) (string, bool, error)
}

// blockedContests reads the audit trail so a blocked start is recorded once
// per distinct refusal, not once per tick.
type blockedContests interface {
	// LatestStartBlocked returns the problem codes of contestID's newest
	// audit entry. found is false when there is no entry or the newest one
	// is not a start_blocked entry: something happened since, and a new
	// block deserves its own entry.
	LatestStartBlocked(ctx context.Context, contestID uuid.UUID) (problems []string, found bool, err error)
}

// Scheduler moves contests from published to running to finished without an
// organizer asking (§8). It is kept apart from Service, which carries much
// a background tick does not need.
//
// It owns only the status the UI shows. Submit checks the deadline against
// the database clock in the answer's own transaction, so a late or missed
// tick never lets anybody answer past their deadline (§8: "status and SSE
// events affect only the UI").
type Scheduler struct {
	repo      ScheduleRepository
	stories   scheduleStories
	questions scheduleQuestions
	roster    scheduleRoster
	blocked   blockedContests
	audit     *audit.Recorder
	uow       storage.UnitOfWork
	// gate supplies the grace AdvanceFinished uses, so the scheduler agrees
	// with every other consumer about when a window closes.
	gate *Gate
	// poolTrigger and covers are nil until their With methods.
	poolTrigger PoolTrigger
	covers      scheduleCovers
}

// NewScheduler assembles the background scheduler. Panics without a gate.
func NewScheduler(repo ScheduleRepository, stories scheduleStories, questions scheduleQuestions, roster scheduleRoster, blocked blockedContests, auditRecorder *audit.Recorder, uow storage.UnitOfWork, gate *Gate) *Scheduler {
	if gate == nil {
		panic("contests: NewScheduler needs the participation gate")
	}
	return &Scheduler{repo: repo, stories: stories, questions: questions, roster: roster, blocked: blocked, audit: auditRecorder, uow: uow, gate: gate}
}

// WithPoolTrigger sets the trigger Advance fires for every contest it moves
// to running. Optional.
func (s *Scheduler) WithPoolTrigger(trigger PoolTrigger) *Scheduler {
	s.poolTrigger = trigger
	return s
}

// WithCovers sets the cover store the publish gate reads. Optional; without
// it the gate never asks about covers.
func (s *Scheduler) WithCovers(store scheduleCovers) *Scheduler {
	s.covers = store
	return s
}

// checkPublishable is the publish gate, shared by Service and Scheduler, which
// hold different stores. It reports every problem at once, so an organizer
// fixes them in one pass.
func checkPublishable(ctx context.Context, stories scheduleStories, questions scheduleQuestions, roster scheduleRoster, contestCovers scheduleCovers, c Contest) error {
	story, err := stories.ByContest(ctx, c.ID)
	if err != nil && !errors.Is(err, ErrStoryNotFound) {
		return err
	}
	qs, err := questions.List(ctx, c.ID)
	if err != nil {
		return err
	}
	problems := publishProblems(c, story, qs)

	// See ProblemStaffRegistered.
	staff, err := roster.RegisteredWithPermission(ctx, c.ID, rbac.PermissionContestAdminAll)
	if err != nil {
		return err
	}
	for _, login := range staff {
		problems = append(problems, PublishProblem{Code: ProblemStaffRegistered, Detail: login})
	}

	// An uploaded cover with nobody credited (§10.1).
	if contestCovers != nil {
		attribution, uploaded, err := contestCovers.Attribution(ctx, c.ID)
		if err != nil {
			return err
		}
		if uploaded && strings.TrimSpace(attribution) == "" {
			problems = append(problems, PublishProblem{Code: ProblemCoverNeedsAttribution})
		}
	}

	if len(problems) > 0 {
		return &NotPublishableError{Problems: problems}
	}
	return nil
}

// Advance runs one tick: if it wins the lock, it moves every contest whose
// window opened or closed and audits those moves, in one transaction.
// started and finished count the moves; both zero is the usual tick.
//
// A due contest passes the same publish gate as a manual start, since
// content stays editable while published. A refused contest stays published
// and DueToStart returns it on every tick, so its start_blocked entry is
// written only when the refusal differs from the newest entry on file, not
// once per tick.
func (s *Scheduler) Advance(ctx context.Context) (started, finished int, err error) {
	// Triggered only after commit: a rolled-back move must not wake the pool.
	var startedContests []uuid.UUID

	err = s.uow.Do(ctx, func(ctx context.Context) error {
		ok, err := s.repo.TryLock(ctx)
		if err != nil {
			return fmt.Errorf("acquire the schedule lock: %w", err)
		}
		if !ok {
			return nil
		}

		due, err := s.repo.DueToStart(ctx)
		if err != nil {
			return fmt.Errorf("find contests due to start: %w", err)
		}

		var entries []audit.Entry
		for _, c := range due {
			switch gateErr := checkPublishable(ctx, s.stories, s.questions, s.roster, s.covers, c); {
			case gateErr == nil:
				// Ready: moved below.
			case errors.Is(gateErr, ErrNotPublishable):
				codes := problemCodesOf(gateErr)
				prev, found, err := s.blocked.LatestStartBlocked(ctx, c.ID)
				if err != nil {
					return fmt.Errorf("check prior block for contest %s: %w", c.ID, err)
				}
				if !found || !sameProblemCodes(prev, codes) {
					entries = append(entries, startBlockedEntry(c.ID, codes))
				}
				continue
			default:
				// An infrastructure failure aborts the tick rather than
				// reading as a refusal (CLAUDE.md rule 8).
				return fmt.Errorf("check publish gate for contest %s: %w", c.ID, gateErr)
			}

			if err := s.repo.SetStatus(ctx, c.ID, StatusPublished, StatusRunning); err != nil {
				// A concurrent Transition or delete got there first and
				// audited it.
				if errors.Is(err, ErrStatusChanged) || errors.Is(err, ErrNotFound) {
					continue
				}
				return fmt.Errorf("start contest %s: %w", c.ID, err)
			}
			started++
			startedContests = append(startedContests, c.ID)
			entries = append(entries, scheduleEntry(c.ID, StatusPublished, StatusRunning))
		}

		finishedIDs, err := s.repo.AdvanceFinished(ctx, s.gate.grace)
		if err != nil {
			return fmt.Errorf("advance contests to finished: %w", err)
		}
		finished = len(finishedIDs)
		for _, id := range finishedIDs {
			entries = append(entries, scheduleEntry(id, StatusRunning, StatusFinished))
		}

		if len(entries) == 0 {
			return nil
		}
		return s.audit.RecordMany(ctx, entries)
	})
	if err == nil && s.poolTrigger != nil {
		for _, id := range startedContests {
			s.poolTrigger.Trigger(id)
		}
	}
	return started, finished, err
}

// scheduleEntry records an automatic transition: a manual one's shape with a
// nil actor.
func scheduleEntry(contestID uuid.UUID, from, to string) audit.Entry {
	changes := audit.NewChanges()
	changes.Set("status", from, to)
	return audit.Entry{
		Action:   audit.ActionContestStatusChange,
		Entity:   "contest",
		EntityID: contestID.String(),
		Payload:  changes.Payload(),
	}
}

// startBlockedEntry records a due contest the publish gate refused, with the
// same problem codes CheckPublish returns over HTTP; the screen translates
// them.
func startBlockedEntry(contestID uuid.UUID, codes []string) audit.Entry {
	return audit.Entry{
		Action:   audit.ActionContestStartBlocked,
		Entity:   "contest",
		EntityID: contestID.String(),
		Payload:  map[string]any{"problems": codes},
	}
}

// problemCodesOf reads the problem codes out of a publish-gate refusal.
func problemCodesOf(gateErr error) []string {
	var notReady *NotPublishableError
	codes := []string{}
	if errors.As(gateErr, &notReady) {
		for _, p := range notReady.Problems {
			codes = append(codes, p.Code)
		}
	}
	return codes
}

// sameProblemCodes reports whether a and b name the same problems, in any
// order.
func sameProblemCodes(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}
