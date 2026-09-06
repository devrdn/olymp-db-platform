package contests

import (
	"context"
	"errors"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
)

// ScheduleRepository is what the background scheduler needs from storage
// (docs/ARCHITECTURE.md §8): the advisory lock one tick competes for, the
// candidates a published→running move might touch, the write that actually
// moves one, and the bulk close a running→finished move needs.
//
// A narrow interface of its own rather than more methods on Repository:
// nothing else in this package ever calls any of these — only
// Scheduler.Advance does, and Repository already answers a different
// question ("what may be changed, by whom") for every one of its other
// callers.
type ScheduleRepository interface {
	// TryLock attempts, inside the ambient transaction, the advisory lock
	// every replica's tick competes for. It is released automatically when
	// that transaction ends — committed or rolled back — so it is never held
	// between ticks: a replica that dies mid-tick leaves nothing for the next
	// one to wait on, and costs it nothing but the tick it missed. false
	// means another replica already holds it this tick; the caller does no
	// further work and simply lets its own transaction end.
	TryLock(ctx context.Context) (bool, error)
	// DueToStart returns every published contest whose starts_at has
	// arrived, by the core database's own clock, together with everything
	// the publish gate (CheckPublishable) needs to re-check it — the same
	// projection ByID returns. Bounded to what this tick would actually
	// move: the query matches on status and starts_at, so a contest nowhere
	// near its window is never read, let alone gated (finding 1's own cost
	// concern — see Advance's doc).
	DueToStart(ctx context.Context) ([]Contest, error)
	// SetStatus moves one contest, exactly Repository's own method
	// (contests.go) and satisfied by the same implementation: the
	// check-then-write race it guards against — a concurrent write landing
	// between a caller's read and its write — is exactly the one that
	// matters here too, an organizer's own manual Transition racing this
	// tick for the same contest.
	SetStatus(ctx context.Context, id uuid.UUID, from, to string) error
	// AdvanceFinished moves every running contest whose ends_at has passed,
	// by the core database's own clock, to finished, and returns their ids.
	// A contest with no ends_at (individual timing needs none to publish,
	// see CheckPublishable) never matches, and stays running until an
	// organizer moves it by hand. No gate runs here: CheckPublishable is
	// what admits participants, and finishing only ever shuts a door that
	// was already open.
	AdvanceFinished(ctx context.Context) ([]uuid.UUID, error)
}

// scheduleStories and scheduleQuestions are the one-method slices of
// StoryRepository and QuestionRepository the scheduler needs to hold the
// same publish gate Service.Transition holds before starting a contest by
// hand (finding 1) — nothing else either interface offers (Save, Create,
// Reorder, ReplaceAnswers, ...) has any business running on a tick.
type scheduleStories interface {
	ByContest(ctx context.Context, contestID uuid.UUID) (Story, error)
}

type scheduleQuestions interface {
	List(ctx context.Context, contestID uuid.UUID) ([]Question, error)
}

// Scheduler moves contests along their published → running → finished
// lifecycle without an organizer asking (§8).
//
// Kept apart from Service on purpose: everything it needs is these storage
// reads, a unit of work and the audit trail, and nothing else Service carries
// (submissions, sequencing, the domain's own clock for enrollment deadlines)
// has anything to do with a background tick. Folding it into Service would
// mean every test and every wiring site that builds one — most of which
// never call Advance — would have to supply the pieces they do not use.
//
// One tick is one call to Advance: it competes for the lock, and either loses
// it (another replica has this tick, and does nothing further) or does the
// whole move-and-audit atomically before releasing it. The closing guarantee
// this whole feature exists to keep honest does not run through here: Submit
// (submission.go) checks Deadline against the core database's own clock in
// the same transaction as the answer it writes, so a late answer is refused
// even if Advance never runs again. What Advance owns is what the interface
// shows — the status a participant's screen and events channel report — and
// running late, whether by one tick or by however long a dead replica leaves
// the lock uncontested, never lets anybody in past their own window or keeps
// anybody answering past their own deadline; it only leaves the status saying
// "running" a little longer than the wall clock would have (§8's own words:
// "статус и SSE-события влияют только на UI").
type Scheduler struct {
	repo      ScheduleRepository
	stories   scheduleStories
	questions scheduleQuestions
	audit     *audit.Recorder
	uow       storage.UnitOfWork
}

// NewScheduler assembles the background scheduler.
func NewScheduler(repo ScheduleRepository, stories scheduleStories, questions scheduleQuestions, auditRecorder *audit.Recorder, uow storage.UnitOfWork) *Scheduler {
	return &Scheduler{repo: repo, stories: stories, questions: questions, audit: auditRecorder, uow: uow}
}

// checkPublishable is the body behind both Service.checkPublishable and
// Scheduler's own gate: read c's story and questions through whichever
// narrow stores the caller holds and hand all three to CheckPublishable
// together. A package-level function taking the two narrow interfaces rather
// than a method on either type, because it is the same question — "is c
// ready for participants" — asked by two callers holding different-shaped
// storage for it.
func checkPublishable(ctx context.Context, stories scheduleStories, questions scheduleQuestions, c Contest) error {
	story, err := stories.ByContest(ctx, c.ID)
	if err != nil && !errors.Is(err, ErrStoryNotFound) {
		return err
	}
	qs, err := questions.List(ctx, c.ID)
	if err != nil {
		return err
	}
	return CheckPublishable(c, story, qs)
}

// Advance runs one tick: try for the lock, and if it is won, move every
// contest whose window opened or closed, auditing exactly those, all inside
// the one transaction the lock lives in.
//
// A contest whose window opened is not moved on trust: Service.Transition
// runs CheckPublishable at the moment it lets status reach running, because
// content stays editable while published and "publish, then remove the
// story, then start" is a sequence the rules allow (Transition's own
// comment). The scheduler is now the other door into that same step, and it
// holds the same invariant — a contest whose story or questions vanished
// after publication is left published, not opened onto nothing, and the
// audit trail is where an organizer finds out why nothing happened at
// starts_at (finding 1).
//
// The gate runs once per contest DueToStart returned, never once per
// published contest in the installation: the query that returns them is
// already narrowed to status and starts_at, so a tick's cost is proportional
// to how many contests are actually due right now — ordinarily zero or one —
// not to how many exist.
//
// started and finished count the contests moved this tick — both zero, with
// a nil error, is the ordinary outcome of most ticks, whether because
// nothing's window has turned or because another replica already had this
// one.
func (s *Scheduler) Advance(ctx context.Context) (started, finished int, err error) {
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
			switch gateErr := checkPublishable(ctx, s.stories, s.questions, c); {
			case gateErr == nil:
				// Ready — fall through to the move below.
			case errors.Is(gateErr, ErrNotPublishable):
				entries = append(entries, startBlockedEntry(c.ID, gateErr))
				continue
			default:
				// Not a gate refusal but an infrastructure failure (the
				// story or question store is away): this is exactly what
				// aborts and surfaces rather than being read as "skipped"
				// (CLAUDE.md rule 8), since nothing here says the contest
				// is actually unpublishable.
				return fmt.Errorf("check publish gate for contest %s: %w", c.ID, gateErr)
			}

			if err := s.repo.SetStatus(ctx, c.ID, StatusPublished, StatusRunning); err != nil {
				// A concurrent manual Transition — or a delete — decided
				// this contest's fate between DueToStart's read and this
				// write; that caller's own audit entry already covers it,
				// so this tick simply moves on instead of treating somebody
				// else's race as its own failure.
				if errors.Is(err, ErrStatusChanged) || errors.Is(err, ErrNotFound) {
					continue
				}
				return fmt.Errorf("start contest %s: %w", c.ID, err)
			}
			started++
			entries = append(entries, scheduleEntry(c.ID, StatusPublished, StatusRunning))
		}

		finishedIDs, err := s.repo.AdvanceFinished(ctx)
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
	return started, finished, err
}

// scheduleEntry is one contest's automatic transition, in the same shape a
// manual one takes (Service.Transition) except for the actor: nil, because
// nobody asked for this one — exactly the case audit.Entry.ActorID's own doc
// names ("nil for system events such as a scheduled contest transition").
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

// startBlockedEntry records a contest whose window opened but which the
// publish gate refused (finding 1). There was no status change to describe
// — the contest is left exactly where it was — so the payload names the
// same machine-readable problem codes CheckPublish's own HTTP response uses
// (api/contests_handler.go's problemsOf), rather than a sentence: an
// organizer's screen is what turns a code into wording, in whatever language
// it speaks, the same as every other refusal this gate can report.
func startBlockedEntry(contestID uuid.UUID, gateErr error) audit.Entry {
	var notReady *NotPublishableError
	codes := []string{}
	if errors.As(gateErr, &notReady) {
		for _, p := range notReady.Problems {
			codes = append(codes, p.Code)
		}
	}
	return audit.Entry{
		Action:   audit.ActionContestStartBlocked,
		Entity:   "contest",
		EntityID: contestID.String(),
		Payload:  map[string]any{"problems": codes},
	}
}
