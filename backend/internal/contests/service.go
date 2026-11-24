package contests

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
	"github.com/devrdn/olymp-db-platform/backend/internal/users"
	"github.com/google/uuid"
)

// ErrNotEditable reports a change the contest's current status forbids.
var ErrNotEditable = errors.New("contest can no longer be edited")

// Schedule moves refused on a running contest whose settings may otherwise
// still change; narrower than ErrNotEditable so the organiser is told why.
var (
	// ErrFreezeAlreadyReached refuses moving ends_at earlier once the
	// leaderboard has frozen; a later ends_at lengthens the freeze instead.
	ErrFreezeAlreadyReached = errors.New("the leaderboard has already frozen, so the end date can only move later")
	// ErrICPCStartLocked refuses a starts_at change on a running ICPC
	// contest, whose penalty minutes are counted from starts_at.
	ErrICPCStartLocked = errors.New("the start date cannot change while ICPC scoring is running")
)

// UserDirectory is the part of the account repository this package needs;
// contests neither create accounts nor change them.
type UserDirectory interface {
	ByID(ctx context.Context, id uuid.UUID) (users.User, error)
	ByLogin(ctx context.Context, login string) (users.User, error)
	// Search finds accounts by a substring of login, full name or email.
	// limit is already bounded by the caller. Only ID, Login and FullName
	// are populated.
	Search(ctx context.Context, query string, limit int) ([]users.User, error)
}

// AppointeeSessions retires the sessions of an account appointed to a
// contest's staff (internal/postgres.Users). Every method joins the
// transaction in ctx.
type AppointeeSessions interface {
	// Lock holds the account's row until the transaction ends.
	Lock(ctx context.Context, id uuid.UUID) error
	BumpSessionGeneration(ctx context.Context, id uuid.UUID) (int64, error)
	users.ContestManagers
}

// AccessCache is auth's short-lived copy of accounts (users.AccessCache).
type AccessCache interface {
	Forget(ctx context.Context, ids []uuid.UUID)
}

// ServiceConfig collects the storage a Service needs, as interfaces declared
// in this package so the rules are testable without a database.
type ServiceConfig struct {
	Contests      Repository
	Stories       StoryRepository
	SchemaNotes   SchemaNotesRepository
	Questions     QuestionRepository
	Managers      ManagerRepository
	Registrations RegistrationRepository
	Policies      PolicyStore
	Languages     LanguageCatalog
	Users         UserDirectory
	// Appointees is required: GrantManager retires the appointee's sessions.
	Appointees AppointeeSessions
	// Game reads the SQL a contest's game is built from, for ExportPackage.
	// Optional: without a game cluster, a package simply carries no game.
	Game GameSource
	// GameDatabases drops a deleted draft's databases from the game cluster.
	// Optional: without a game cluster there are none.
	GameDatabases GameDropper
	// Submissions records answers. Optional; only Submit uses it, and a
	// Service without one panics there.
	Submissions SubmissionRepository
	// Sequence answers whether a question may be answered yet in a
	// sequential contest (§6.1.1). Optional; only Submit uses it.
	Sequence SequentialGate
	// GameReadiness lets the publish gate refuse a contest whose game is not
	// built (§4.2). Required.
	GameReadiness scheduleGames
	// Covers lets the publish gate refuse an uploaded cover with nobody
	// credited (§10.1). Required: a Service without one would let an
	// organiser's publish or start through where the scheduler refuses.
	Covers     scheduleCovers
	Audit      *audit.Recorder
	UnitOfWork storage.UnitOfWork
	Now        func() time.Time
	// Gate admits answers and gives Submit its write deadline (§8).
	// Required, and the same *Gate every other consumer holds, so no route
	// has a grace of its own.
	Gate *Gate
	// Logger reports a reference answer whose regex does not compile.
	Logger *slog.Logger
	// Sleep is the backoff between attempt-number race retries; tests set a
	// no-op. Defaults to time.Sleep.
	Sleep func(time.Duration)
	// DefaultGraceMin is the installation's GAME_INSTANCE_GRACE_MIN, the
	// grace in force while a contest's Settings.GracePeriodMin is zero.
	// ExtendGrace compares against it because the reclaim sweep falls back
	// to the same value.
	DefaultGraceMin int
	// PoolTrigger wakes the game-pool tender after a roster change. Optional.
	PoolTrigger PoolTrigger
}

// Service holds the rules of authoring and running a contest.
type Service struct {
	contests        Repository
	stories         StoryRepository
	schemaNotes     SchemaNotesRepository
	questions       QuestionRepository
	managers        ManagerRepository
	registrations   RegistrationRepository
	policies        PolicyStore
	languages       LanguageCatalog
	users           UserDirectory
	appointees      AppointeeSessions
	access          AccessCache
	game            GameSource
	gameDatabases   GameDropper
	submissions     SubmissionRepository
	sequence        SequentialGate
	covers          scheduleCovers
	games           scheduleGames
	audit           *audit.Recorder
	uow             storage.UnitOfWork
	now             func() time.Time
	gate            *Gate
	log             *slog.Logger
	sleep           func(time.Duration)
	defaultGraceMin int
	poolTrigger     PoolTrigger
	trustedDomain   string
}

// NewService assembles the contest service. Panics without a Gate,
// GameReadiness, a covers store or Appointees.
func NewService(cfg ServiceConfig) *Service {
	if cfg.Gate == nil {
		panic("contests: NewService needs the participation gate")
	}
	if cfg.GameReadiness == nil {
		panic("contests: NewService needs to know whether a contest's game is built")
	}
	if cfg.Covers == nil {
		panic("contests: NewService needs the covers store the publish gate reads")
	}
	if cfg.Appointees == nil {
		panic("contests: NewService needs to retire the sessions of an account appointed to the staff")
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	sleep := cfg.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	return &Service{
		contests:        cfg.Contests,
		stories:         cfg.Stories,
		schemaNotes:     cfg.SchemaNotes,
		questions:       cfg.Questions,
		managers:        cfg.Managers,
		registrations:   cfg.Registrations,
		policies:        cfg.Policies,
		languages:       cfg.Languages,
		users:           cfg.Users,
		appointees:      cfg.Appointees,
		game:            cfg.Game,
		gameDatabases:   cfg.GameDatabases,
		submissions:     cfg.Submissions,
		sequence:        cfg.Sequence,
		covers:          cfg.Covers,
		games:           cfg.GameReadiness,
		audit:           cfg.Audit,
		uow:             cfg.UnitOfWork,
		now:             now,
		gate:            cfg.Gate,
		log:             log,
		sleep:           sleep,
		defaultGraceMin: cfg.DefaultGraceMin,
		poolTrigger:     cfg.PoolTrigger,
	}
}

// CreateCommand describes a new contest. Everything but the author is
// optional: an organizer starts from an empty draft and fills it in.
type CreateCommand struct {
	ActorID uuid.UUID
	// Patch is applied to DefaultContest.
	Patch        Patch
	Languages    []ContestLanguage
	Translations []Translation
}

// Create registers a contest and makes its author the owner.
func (s *Service) Create(ctx context.Context, cmd CreateCommand) (Contest, error) {
	c := cmd.Patch.ApplyTo(DefaultContest())
	c.CreatedBy = cmd.ActorID
	c.Languages = cmd.Languages
	if err := c.Validate(); err != nil {
		return Contest{}, err
	}
	if err := s.checkTrustedDomain(false, c.Settings.TrustedDomainOnly); err != nil {
		return Contest{}, err
	}
	if err := s.checkLanguages(ctx, cmd.Languages); err != nil {
		return Contest{}, err
	}
	if err := s.checkTranslationLanguages(ctx, cmd.Translations); err != nil {
		return Contest{}, err
	}

	// One transaction: a contest without an owner or a SQL policy must never
	// be observable.
	var created Contest
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		var err error
		if created, err = s.contests.Create(ctx, c); err != nil {
			return err
		}
		if len(cmd.Languages) > 0 {
			if err := s.contests.ReplaceLanguages(ctx, created.ID, cmd.Languages); err != nil {
				return err
			}
			created.Languages = cmd.Languages
		}
		if len(cmd.Translations) > 0 {
			if err := s.contests.ReplaceTranslations(ctx, created.ID, cmd.Translations); err != nil {
				return err
			}
			created.Translations = translationMap(cmd.Translations)
		}
		if err := s.managers.Grant(ctx, Manager{
			ContestID: created.ID,
			UserID:    cmd.ActorID,
			Role:      ownerRole,
			GrantedBy: cmd.ActorID,
			GrantedAt: s.now(),
		}); err != nil {
			return err
		}
		if err := s.policies.Save(ctx, DefaultSQLPolicy(created.ID)); err != nil {
			return err
		}
		// Deliberately the kind of contest made, not every setting the patch
		// carried (auditFields): a setting added to Patch is audited on update
		// only, unless it is added here too.
		return s.record(ctx, cmd.ActorID, audit.ActionContestCreate, created.ID, map[string]any{
			"enrollment":    created.Enrollment,
			"visibility":    created.Visibility,
			"question_mode": created.QuestionMode,
			"progression":   created.Progression,
			"scoring":       created.Scoring,
			"timing":        created.Timing,
		})
	})
	if err != nil {
		return Contest{}, err
	}
	return created, nil
}

// UpdateCommand changes a contest's own settings.
type UpdateCommand struct {
	ActorID   uuid.UUID
	ContestID uuid.UUID
	// ExpectedVersion is the Version the caller decided the change against;
	// a contest written since is refused with ErrContestChanged. Nil applies
	// the patch to whatever is stored.
	ExpectedVersion *int64
	// Patch is applied to the stored contest.
	Patch Patch
}

// Update changes a contest's settings.
func (s *Service) Update(ctx context.Context, cmd UpdateCommand) (Contest, error) {
	var written Contest
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		current, err := s.contests.ByID(ctx, cmd.ContestID)
		if err != nil {
			return err
		}
		if err := current.checkVersion(cmd.ExpectedVersion); err != nil {
			return err
		}
		updated, err := s.decideUpdate(ctx, current, cmd.Patch.ApplyTo(current))
		if err != nil {
			return err
		}
		if written, err = s.contests.Update(ctx, updated); err != nil {
			return err
		}
		changes := audit.Between(current.auditFields(), updated.auditFields())
		return s.record(ctx, cmd.ActorID, audit.ActionContestUpdate, updated.ID, changes.Payload())
	})
	if err != nil {
		return Contest{}, err
	}
	return written, nil
}

// decideUpdate applies every rule a change of current's settings to updated
// must pass, and returns what is to be written.
func (s *Service) decideUpdate(ctx context.Context, current, updated Contest) (Contest, error) {
	if !current.SettingsEditable() {
		return Contest{}, fmt.Errorf("%w: it is %s", ErrNotEditable, current.Status)
	}
	// A patch cannot say "no duration", and a fixed contest with one does not
	// validate, so switching back to fixed timing clears it.
	if updated.Timing == TimingFixed {
		updated.DurationMin = nil
	}

	// Before Validate: checkRunningChange may rewrite the schedule and the
	// freeze, and Validate must see what will be written.
	if err := checkRunningChange(current, &updated, s.now()); err != nil {
		return Contest{}, err
	}
	if err := updated.Validate(); err != nil {
		return Contest{}, err
	}
	if err := s.checkTrustedDomain(current.Settings.TrustedDomainOnly, updated.Settings.TrustedDomainOnly); err != nil {
		return Contest{}, err
	}
	if err := s.checkScoringKeepsBonus(ctx, current, updated); err != nil {
		return Contest{}, err
	}
	return updated, nil
}

// ExtendGrace lengthens how long a finished or archived contest's game
// databases survive before the reclaim sweep drops them (§2.4). It is one of
// two writes allowed on an ended contest, SetPrizePlaces the other, and not a
// second door into Update.
//
// graceMin must exceed the grace in force: the contest's own, or the
// installation default when it set none, since the sweep falls back to that
// default too. Comparing against the stored zero would let a small value cut
// the real default short.
//
// The write bumps updated_at, which the sweep measures from. That can only
// move the reclaim deadline later: now is after the old updated_at and the
// new grace is larger than the old one.
func (s *Service) ExtendGrace(ctx context.Context, actorID, contestID uuid.UUID, graceMin int) (Contest, error) {
	var written Contest
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		current, err := s.contests.ByID(ctx, contestID)
		if err != nil {
			return err
		}
		if !current.Ended() {
			return fmt.Errorf(
				"%w: the grace period is set through the ordinary settings while the contest is %s",
				ErrNotEditable, current.Status)
		}
		effective := current.Settings.GracePeriodMin
		if effective <= 0 {
			effective = s.defaultGraceMin
		}
		if graceMin <= effective {
			return fmt.Errorf(
				"%w: %d does not extend the current %d-minute grace period",
				ErrGraceNotLonger, graceMin, effective)
		}

		updated := current
		updated.Settings.GracePeriodMin = graceMin
		if err := updated.Validate(); err != nil {
			return err
		}
		if written, err = s.contests.Update(ctx, updated); err != nil {
			return err
		}
		changes := audit.NewChanges()
		changes.Set("grace_period_min", current.Settings.GracePeriodMin, graceMin)
		return s.record(ctx, actorID, audit.ActionContestUpdate, contestID, changes.Payload())
	})
	if err != nil {
		return Contest{}, err
	}
	return written, nil
}

// checkRunningChange refuses changes to a running contest's shape (question
// mode, clock, scoring), which participants are already answering under, and
// allows the window and network fixes that help them. updated is a pointer
// because a same-minute schedule resend is snapped back to the stored value,
// so a save never moves a deadline by the seconds the form cannot show.
func checkRunningChange(current Contest, updated *Contest, now time.Time) error {
	if current.Status != StatusRunning {
		return nil
	}
	// FreezeAt is recomputed from EndsAt on every read. Moving EndsAt alone
	// after the freeze would move FreezeAt into the future and unfreeze a
	// board that had already stopped. So once frozen, an extension moves
	// EndsAt by whole minutes and grows the freeze by the same minutes,
	// keeping FreezeAt in place; moving EndsAt earlier is refused.
	//
	// The form sends whole minutes, so a same-minute EndsAt is a resend and
	// is reset to the exact stored value.
	freezeLengthened := false
	if freezeAt, ok := current.FreezeAt(); ok && !now.Before(freezeAt) {
		switch {
		case sameMinute(current.EndsAt, updated.EndsAt):
			updated.EndsAt = current.EndsAt
		case updated.EndsAt != nil && updated.EndsAt.After(*current.EndsAt):
			minutes := int(updated.EndsAt.UTC().Truncate(time.Minute).
				Sub(current.EndsAt.UTC().Truncate(time.Minute)) / time.Minute)
			lengthened := *current.LeaderboardFreezeMin + minutes
			if !equalDuration(updated.LeaderboardFreezeMin, current.LeaderboardFreezeMin) &&
				!equalDuration(updated.LeaderboardFreezeMin, &lengthened) {
				return fmt.Errorf("%w: the leaderboard freeze cannot change while it runs", ErrNotEditable)
			}
			endsAt := current.EndsAt.Add(time.Duration(minutes) * time.Minute)
			updated.EndsAt = &endsAt
			updated.LeaderboardFreezeMin = &lengthened
			freezeLengthened = true
		default:
			return ErrFreezeAlreadyReached
		}
	}
	// ICPC penalty minutes are counted from starts_at when the table is read,
	// so moving it would rescore everybody. No other scoring reads starts_at.
	// Same-minute snap-back as for EndsAt.
	if current.Scoring == ScoringICPC {
		if !sameMinute(current.StartsAt, updated.StartsAt) {
			return ErrICPCStartLocked
		}
		updated.StartsAt = current.StartsAt
	}
	switch {
	case current.QuestionMode != updated.QuestionMode:
		return fmt.Errorf("%w: the question mode cannot change while it runs", ErrNotEditable)
	case current.Progression != updated.Progression:
		return fmt.Errorf("%w: the progression cannot change while it runs", ErrNotEditable)
	case current.Scoring != updated.Scoring:
		// The penalty is decided per submission when it is made (§6.1.1), so
		// earlier and later answers would disagree.
		return fmt.Errorf("%w: the scoring mode cannot change while it runs", ErrNotEditable)
	case current.Timing != updated.Timing:
		return fmt.Errorf("%w: the timing model cannot change while it runs", ErrNotEditable)
	case !equalDuration(current.DurationMin, updated.DurationMin):
		return fmt.Errorf("%w: the session length cannot change while it runs", ErrNotEditable)
	case !freezeLengthened && !equalDuration(current.LeaderboardFreezeMin, updated.LeaderboardFreezeMin):
		// Moving the freeze either reopens the live table or hides one
		// participants have seen. The names setting may still change.
		return fmt.Errorf("%w: the leaderboard freeze cannot change while it runs", ErrNotEditable)
	case current.ICPCPenaltyMin != updated.ICPCPenaltyMin:
		// Applied when the table is read, so changing it rescores everybody.
		return fmt.Errorf("%w: the ICPC penalty cannot change while it runs", ErrNotEditable)
	}
	return nil
}

// ByID returns one contest.
func (s *Service) ByID(ctx context.Context, id uuid.UUID) (Contest, error) {
	return s.contests.ByID(ctx, id)
}

// List returns a page of contests.
func (s *Service) List(ctx context.Context, f Filter) ([]Contest, int, error) {
	return s.contests.List(ctx, f.Normalize())
}

// SetLanguages sets the languages a contest is offered in.
func (s *Service) SetLanguages(ctx context.Context, actorID, contestID uuid.UUID, langs []ContestLanguage) error {
	return s.uow.Do(ctx, func(ctx context.Context) error {
		// A language added to a running contest would have nothing authored
		// in it.
		c, err := BeginContentEdit(ctx, s.contests, contestID)
		if err != nil {
			return err
		}
		if err := validateLanguages(langs); err != nil {
			return err
		}
		if err := s.checkLanguages(ctx, langs); err != nil {
			return err
		}
		if err := s.contests.ReplaceLanguages(ctx, contestID, langs); err != nil {
			return err
		}
		changes := audit.NewChanges()
		changes.Set("languages", languageCodes(c.Languages), languageCodes(langs))
		changes.Set("default_language", c.DefaultLanguage(), defaultLanguageOf(langs))
		return s.record(ctx, actorID, audit.ActionContestLanguages, contestID, changes.Payload())
	})
}

// SetTranslations replaces a contest's authored titles.
func (s *Service) SetTranslations(ctx context.Context, actorID, contestID uuid.UUID, translations []Translation) error {
	return s.uow.Do(ctx, func(ctx context.Context) error {
		if _, err := BeginContentEdit(ctx, s.contests, contestID); err != nil {
			return err
		}
		if err := s.checkTranslationLanguages(ctx, translations); err != nil {
			return err
		}
		// The trail records which languages were written, not the text: it
		// is not a version history (§9.2).
		written := make([]string, 0, len(translations))
		for _, t := range translations {
			written = append(written, t.Lang)
		}
		slices.Sort(written)

		if err := s.contests.ReplaceTranslations(ctx, contestID, translations); err != nil {
			return err
		}
		return s.record(ctx, actorID, audit.ActionContestTranslations, contestID, map[string]any{
			"languages": written,
		})
	})
}

// CheckPublish reports what stands between the contest and publication,
// without changing anything.
func (s *Service) CheckPublish(ctx context.Context, contestID uuid.UUID) error {
	c, err := s.contests.ByID(ctx, contestID)
	if err != nil {
		return err
	}
	return s.checkPublishable(ctx, c)
}

func (s *Service) checkPublishable(ctx context.Context, c Contest) error {
	return checkPublishable(ctx, s.stories, s.questions, s.registrations, s.games, s.covers, c)
}

// Transition moves a contest along its lifecycle.
func (s *Service) Transition(ctx context.Context, actorID, contestID uuid.UUID, status string) error {
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		c, err := s.contests.ByID(ctx, contestID)
		if err != nil {
			return err
		}
		if err := c.CanTransitionTo(status); err != nil {
			return err
		}
		// Checked on start too: content stays editable while published, so
		// the contest may have lost its story since.
		if status == StatusPublished || status == StatusRunning {
			if err := s.checkPublishable(ctx, c); err != nil {
				return err
			}
		}

		finished, err := s.contests.SetStatus(ctx, c, status)
		if err != nil {
			return err
		}
		changes := audit.NewChanges()
		changes.Set("status", c.Status, status)
		if err := s.record(ctx, actorID, audit.ActionContestStatusChange, contestID, changes.Payload()); err != nil {
			return err
		}
		return recordFinishes(ctx, s.audit, []FinishedContest{{ID: contestID, Users: finished}})
	})
	if err != nil {
		return err
	}

	// After commit, never inside the transaction.
	if s.poolTrigger != nil && (status == StatusPublished || status == StatusRunning) {
		s.poolTrigger.Trigger(contestID)
	}
	return nil
}

// GameDropper removes a contest's databases from the game cluster (CLAUDE.md
// Go layout rule 3); internal/provisioning implements it.
type GameDropper interface {
	// DropGame drops every database of the contest not yet dropped and marks
	// its row so. After an error some may remain, still recorded.
	DropGame(ctx context.Context, contestID uuid.UUID) error
}

// Delete removes a draft. A contest that was ever published is a record and
// can only be archived.
func (s *Service) Delete(ctx context.Context, actorID, contestID uuid.UUID) error {
	c, err := s.contests.ByID(ctx, contestID)
	if err != nil {
		return err
	}
	if c.Status != StatusDraft {
		return fmt.Errorf("%w: only a draft can be deleted, this one is %s", ErrNotEditable, c.Status)
	}

	// Before the rows, which are how anything finds a game database again, and
	// outside the unit of work: DROP DATABASE cannot run in a transaction.
	if s.gameDatabases != nil {
		if err := s.gameDatabases.DropGame(ctx, contestID); err != nil {
			return fmt.Errorf("drop the draft's game databases: %w", err)
		}
	}

	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.contests.Delete(ctx, contestID); err != nil {
			return err
		}
		return s.record(ctx, actorID, audit.ActionContestDelete, contestID, nil)
	})
}

// Policy returns the contest's SQL access policy.
func (s *Service) Policy(ctx context.Context, contestID uuid.UUID) (SQLPolicy, error) {
	return s.policies.ByContest(ctx, contestID)
}

// SetPolicy changes how much SQL power the contest hands out. Not once it is
// running: the template grants are built from this row and would stop
// matching what the validator enforces.
func (s *Service) SetPolicy(ctx context.Context, actorID, contestID uuid.UUID, p SQLPolicy) error {
	return s.uow.Do(ctx, func(ctx context.Context) error {
		if _, err := BeginContentEdit(ctx, s.contests, contestID); err != nil {
			return err
		}
		current, err := s.policies.ByContest(ctx, contestID)
		if err != nil {
			return err
		}

		p.ContestID = contestID
		if err := p.Validate(); err != nil {
			return err
		}
		actor := actorID
		p.UpdatedBy = &actor
		p.UpdatedAt = s.now()

		if err := s.policies.Save(ctx, p); err != nil {
			return err
		}
		changes := audit.Between(current.auditFields(), p.auditFields())
		return s.record(ctx, actorID, audit.ActionContestPolicyChange, contestID, changes.Payload())
	})
}

// checkLanguages rejects language codes the installation does not offer.
func (s *Service) checkLanguages(ctx context.Context, langs []ContestLanguage) error {
	if len(langs) == 0 {
		return nil
	}
	known, err := s.languages.Active(ctx)
	if err != nil {
		return fmt.Errorf("load languages: %w", err)
	}
	return checkLanguagesKnown(known, langs)
}

// checkTranslationLanguages checks the bounds of authored text, then applies
// the same language check to it. The language need not be one the contest
// declares yet; the publish gate checks the set is complete.
func (s *Service) checkTranslationLanguages(ctx context.Context, translations []Translation) error {
	langs := make([]ContestLanguage, 0, len(translations))
	for _, t := range translations {
		if err := t.Validate(); err != nil {
			return err
		}
		langs = append(langs, ContestLanguage{Code: t.Lang})
	}
	return s.checkLanguages(ctx, langs)
}

// record appends an audit entry for an action on a contest. Call it inside
// the action's unit of work: a failure rolls the action back, since an
// unaccounted privileged action is worse than a failed one.
func (s *Service) record(ctx context.Context, actorID uuid.UUID, action string, contestID uuid.UUID, payload map[string]any) error {
	var actor *uuid.UUID
	if actorID != uuid.Nil {
		actor = &actorID
	}
	return s.audit.Record(ctx, audit.Entry{
		ActorID:  actor,
		Action:   action,
		Entity:   "contest",
		EntityID: contestID.String(),
		Payload:  payload,
	})
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func equalDuration(a, b *int) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

// sameMinute compares two schedule times to the minute, the settings form's
// resolution, so resending a stored value that carries seconds is not a move.
func sameMinute(a, b *time.Time) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return a.UTC().Truncate(time.Minute).Equal(b.UTC().Truncate(time.Minute))
	}
}

func translationMap(translations []Translation) map[string]Translation {
	out := make(map[string]Translation, len(translations))
	for _, t := range translations {
		out[t.Lang] = t
	}
	return out
}

// ContentRows is the part of Repository BeginContentEdit uses, for every
// package whose edits are a contest's content (internal/provisioning's game).
type ContentRows interface {
	LockContest(ctx context.Context, id uuid.UUID) error
	ByID(ctx context.Context, id uuid.UUID) (Contest, error)
	MarkContentChanged(ctx context.Context, read Contest) (Contest, error)
}

// BeginContentEdit is the first step of every edit of a contest's content
// (its story, schema notes, questions, languages, titles, SQL policy and
// game), inside the edit's unit of work. It locks the row, so two edits of one
// contest take turns instead of refusing each other; refuses a contest whose
// content is frozen with ErrNotEditable; and moves the version, so a publish
// or start whose gate read the content before the edit is refused
// (docs/ARCHITECTURE.md section 7.3). It returns the row at its new version,
// for the edit's own decisions and any write it conditions on the row.
func BeginContentEdit(ctx context.Context, rows ContentRows, contestID uuid.UUID) (Contest, error) {
	if err := rows.LockContest(ctx, contestID); err != nil {
		return Contest{}, err
	}
	c, err := rows.ByID(ctx, contestID)
	if err != nil {
		return Contest{}, err
	}
	if !c.ContentEditable() {
		return Contest{}, fmt.Errorf("%w: it is %s", ErrNotEditable, c.Status)
	}
	return rows.MarkContentChanged(ctx, c)
}

// checkLanguageCodes rejects codes the installation does not offer.
func (s *Service) checkLanguageCodes(ctx context.Context, codes []string) error {
	langs := make([]ContestLanguage, 0, len(codes))
	for _, code := range codes {
		langs = append(langs, ContestLanguage{Code: code})
	}
	return s.checkLanguages(ctx, langs)
}

// langCodes lists the keys of a per-language map, in a stable order.
func langCodes(byLang map[string]string) []string {
	codes := make([]string, 0, len(byLang))
	for code := range byLang {
		codes = append(codes, code)
	}
	slices.Sort(codes)
	return codes
}

func languageCodes(langs []ContestLanguage) []string {
	codes := make([]string, 0, len(langs))
	for _, l := range langs {
		codes = append(codes, l.Code)
	}
	return codes
}

func defaultLanguageOf(langs []ContestLanguage) string {
	for _, l := range langs {
		if l.IsDefault {
			return l.Code
		}
	}
	return ""
}

// WithTrustedDomain sets the installation's email domain, the one a contest
// limited to it admits (TRUSTED_EMAIL_DOMAIN); empty when there is none. Call
// it while assembling the service, before it is used.
func (s *Service) WithTrustedDomain(domain string) *Service {
	s.trustedDomain = domain
	return s
}

// WithAccessCache is for the composition root, before the service is shared;
// without one nothing caches accounts.
func (s *Service) WithAccessCache(c AccessCache) *Service {
	s.access = c
	return s
}

// forget tells the access cache the accounts changed, once the change has
// committed: earlier, a request could cache the old row again. The caller's
// cancellation is ignored, so a hang-up cannot leave the old state cached.
func (s *Service) forget(ctx context.Context, ids ...uuid.UUID) {
	if s.access == nil || len(ids) == 0 {
		return
	}
	s.access.Forget(context.WithoutCancel(ctx), ids)
}

// checkTrustedDomain refuses turning the limit on where there is no domain to
// limit to. A contest that already has it is left alone, so it stays editable
// after the domain is unset.
func (s *Service) checkTrustedDomain(was, now bool) error {
	if now && !was && s.trustedDomain == "" {
		return ErrTrustedDomainUnset
	}
	return nil
}
