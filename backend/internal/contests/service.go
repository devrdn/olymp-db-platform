package contests

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/users"
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

// ServiceConfig collects the storage a Service needs, as interfaces declared
// in this package so the rules are testable without a database.
type ServiceConfig struct {
	Contests      Repository
	Stories       StoryRepository
	Questions     QuestionRepository
	Managers      ManagerRepository
	Registrations RegistrationRepository
	Policies      PolicyStore
	Languages     LanguageCatalog
	Users         UserDirectory
	// Game reads the SQL a contest's game is built from, for ExportPackage.
	// Optional: without a game cluster, a package simply carries no game.
	Game GameSource
	// Submissions records answers. Optional; only Submit uses it, and a
	// Service without one panics there.
	Submissions SubmissionRepository
	// Sequence answers whether a question may be answered yet in a
	// sequential contest (§6.1.1). Optional; only Submit uses it.
	Sequence SequentialGate
	// Covers lets the publish gate refuse an uploaded cover with nobody
	// credited (§10.1). Optional; without one the gate never asks.
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
	questions       QuestionRepository
	managers        ManagerRepository
	registrations   RegistrationRepository
	policies        PolicyStore
	languages       LanguageCatalog
	users           UserDirectory
	game            GameSource
	submissions     SubmissionRepository
	sequence        SequentialGate
	covers          scheduleCovers
	audit           *audit.Recorder
	uow             storage.UnitOfWork
	now             func() time.Time
	gate            *Gate
	log             *slog.Logger
	sleep           func(time.Duration)
	defaultGraceMin int
	poolTrigger     PoolTrigger
}

// NewService assembles the contest service. Panics without a Gate.
func NewService(cfg ServiceConfig) *Service {
	if cfg.Gate == nil {
		panic("contests: NewService needs the participation gate")
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
		questions:       cfg.Questions,
		managers:        cfg.Managers,
		registrations:   cfg.Registrations,
		policies:        cfg.Policies,
		languages:       cfg.Languages,
		users:           cfg.Users,
		game:            cfg.Game,
		submissions:     cfg.Submissions,
		sequence:        cfg.Sequence,
		covers:          cfg.Covers,
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
	ActorID      uuid.UUID
	Enrollment   string
	QuestionMode string
	// Progression empty defaults to ProgressionFree (§6.1.1).
	Progression string
	// Scoring empty defaults to ScoringPoints (§6.1.1).
	Scoring      string
	Timing       string
	DurationMin  *int
	StartsAt     *time.Time
	EndsAt       *time.Time
	AllowedCIDRs []netip.Prefix
	Settings     Settings
	Languages    []ContestLanguage
	Translations []Translation
	// LeaderboardFreezeMin is nil for no freeze; LeaderboardNames empty
	// defaults to LeaderboardNamesLogin.
	LeaderboardFreezeMin *int
	LeaderboardNames     string
	// ICPCPenaltyMin is nil for the default of DefaultICPCPenaltyMin minutes.
	ICPCPenaltyMin *int
}

// Create registers a contest and makes its author the owner.
func (s *Service) Create(ctx context.Context, cmd CreateCommand) (Contest, error) {
	c := Contest{
		Status:       StatusDraft,
		Enrollment:   orDefault(cmd.Enrollment, EnrollmentInviteOnly),
		QuestionMode: orDefault(cmd.QuestionMode, QuestionModeMulti),
		Progression:  orDefault(cmd.Progression, ProgressionFree),
		Scoring:      orDefault(cmd.Scoring, ScoringPoints),
		Timing:       orDefault(cmd.Timing, TimingFixed),
		DurationMin:  cmd.DurationMin,
		StartsAt:     cmd.StartsAt,
		EndsAt:       cmd.EndsAt,
		AllowedCIDRs: cmd.AllowedCIDRs,
		Settings:     cmd.Settings,
		Languages:    cmd.Languages,
		CreatedBy:    cmd.ActorID,

		LeaderboardFreezeMin: cmd.LeaderboardFreezeMin,
		LeaderboardNames:     orDefault(cmd.LeaderboardNames, LeaderboardNamesLogin),
		ICPCPenaltyMin:       orDefaultInt(cmd.ICPCPenaltyMin, DefaultICPCPenaltyMin),
	}
	if err := c.Validate(); err != nil {
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
		return s.record(ctx, cmd.ActorID, audit.ActionContestCreate, created.ID, map[string]any{
			"enrollment":    created.Enrollment,
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

// UpdateCommand changes a contest's own fields. A zero field means "leave it
// alone"; Settings is a pointer for the same reason, since its zero value is
// a meaningful configuration.
type UpdateCommand struct {
	ActorID      uuid.UUID
	ContestID    uuid.UUID
	Enrollment   string
	QuestionMode string
	Progression  string
	Scoring      string
	Timing       string
	DurationMin  *int
	StartsAt     *time.Time
	EndsAt       *time.Time
	// AllowedCIDRs replaces the network restriction. A non-nil empty slice
	// clears it; nil leaves it as it was.
	AllowedCIDRs []netip.Prefix
	Settings     *Settings
	// LeaderboardFreezeMin sets the freeze; ClearLeaderboardFreeze removes it,
	// since nil already means "leave it alone".
	LeaderboardFreezeMin   *int
	ClearLeaderboardFreeze bool
	LeaderboardNames       string
	// ICPCPenaltyMin is the per-attempt penalty in minutes; nil leaves it
	// alone, because zero (no penalty) is a real setting.
	ICPCPenaltyMin *int
}

// Update changes a contest's settings.
func (s *Service) Update(ctx context.Context, cmd UpdateCommand) (Contest, error) {
	current, err := s.contests.ByID(ctx, cmd.ContestID)
	if err != nil {
		return Contest{}, err
	}
	if !current.SettingsEditable() {
		return Contest{}, fmt.Errorf("%w: it is %s", ErrNotEditable, current.Status)
	}

	updated := current
	if cmd.Enrollment != "" {
		updated.Enrollment = cmd.Enrollment
	}
	if cmd.QuestionMode != "" {
		updated.QuestionMode = cmd.QuestionMode
	}
	if cmd.Progression != "" {
		updated.Progression = cmd.Progression
	}
	if cmd.Scoring != "" {
		updated.Scoring = cmd.Scoring
	}
	if cmd.Timing != "" {
		updated.Timing = cmd.Timing
	}
	if cmd.DurationMin != nil {
		updated.DurationMin = cmd.DurationMin
	}
	if cmd.StartsAt != nil {
		updated.StartsAt = cmd.StartsAt
	}
	if cmd.EndsAt != nil {
		updated.EndsAt = cmd.EndsAt
	}
	if cmd.AllowedCIDRs != nil {
		updated.AllowedCIDRs = cmd.AllowedCIDRs
	}
	if cmd.Settings != nil {
		updated.Settings = *cmd.Settings
	}
	switch {
	case cmd.ClearLeaderboardFreeze:
		updated.LeaderboardFreezeMin = nil
	case cmd.LeaderboardFreezeMin != nil:
		updated.LeaderboardFreezeMin = cmd.LeaderboardFreezeMin
	}
	if cmd.LeaderboardNames != "" {
		updated.LeaderboardNames = cmd.LeaderboardNames
	}
	if cmd.ICPCPenaltyMin != nil {
		updated.ICPCPenaltyMin = *cmd.ICPCPenaltyMin
	}

	// A client cannot send "no duration", and a fixed contest with one does
	// not validate, so switching back to fixed timing clears it here.
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

	changes := audit.Between(current.auditFields(), updated.auditFields())

	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.contests.Update(ctx, updated); err != nil {
			return err
		}
		return s.record(ctx, cmd.ActorID, audit.ActionContestUpdate, updated.ID, changes.Payload())
	})
	if err != nil {
		return Contest{}, err
	}
	return updated, nil
}

// ExtendGrace lengthens how long a finished or archived contest's game
// databases survive before the reclaim sweep drops them (§2.4). It is the
// one write allowed on an ended contest, not a second door into Update.
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
	current, err := s.contests.ByID(ctx, contestID)
	if err != nil {
		return Contest{}, err
	}
	if !current.Ended() {
		return Contest{}, fmt.Errorf(
			"%w: the grace period is set through the ordinary settings while the contest is %s",
			ErrNotEditable, current.Status)
	}
	effective := current.Settings.GracePeriodMin
	if effective <= 0 {
		effective = s.defaultGraceMin
	}
	if graceMin <= effective {
		return Contest{}, fmt.Errorf(
			"%w: %d does not extend the current %d-minute grace period",
			ErrInvalidContest, graceMin, effective)
	}

	updated := current
	updated.Settings.GracePeriodMin = graceMin
	if err := updated.Validate(); err != nil {
		return Contest{}, err
	}

	changes := audit.NewChanges()
	changes.Set("grace_period_min", current.Settings.GracePeriodMin, graceMin)

	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.contests.Update(ctx, updated); err != nil {
			return err
		}
		return s.record(ctx, actorID, audit.ActionContestUpdate, contestID, changes.Payload())
	})
	if err != nil {
		return Contest{}, err
	}
	return updated, nil
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
	// A language added to a running contest would have nothing authored in it.
	c, err := s.editableContest(ctx, contestID)
	if err != nil {
		return err
	}
	if err := validateLanguages(langs); err != nil {
		return err
	}
	if err := s.checkLanguages(ctx, langs); err != nil {
		return err
	}

	return s.uow.Do(ctx, func(ctx context.Context) error {
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
	if _, err := s.editableContest(ctx, contestID); err != nil {
		return err
	}
	if err := s.checkTranslationLanguages(ctx, translations); err != nil {
		return err
	}

	// The trail records which languages were written, not the text: it is
	// not a version history (§9.2).
	written := make([]string, 0, len(translations))
	for _, t := range translations {
		written = append(written, t.Lang)
	}
	slices.Sort(written)

	return s.uow.Do(ctx, func(ctx context.Context) error {
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
	return checkPublishable(ctx, s.stories, s.questions, s.registrations, s.covers, c)
}

// Transition moves a contest along its lifecycle.
func (s *Service) Transition(ctx context.Context, actorID, contestID uuid.UUID, status string) error {
	c, err := s.contests.ByID(ctx, contestID)
	if err != nil {
		return err
	}
	if err := c.CanTransitionTo(status); err != nil {
		return err
	}
	// Checked on start too: content stays editable while published, so the
	// contest may have lost its story since.
	if status == StatusPublished || status == StatusRunning {
		if err := s.checkPublishable(ctx, c); err != nil {
			return err
		}
	}

	err = s.uow.Do(ctx, func(ctx context.Context) error {
		// The write refuses if the status is no longer the one checked above.
		if err := s.contests.SetStatus(ctx, contestID, c.Status, status); err != nil {
			return err
		}
		changes := audit.NewChanges()
		changes.Set("status", c.Status, status)
		return s.record(ctx, actorID, audit.ActionContestStatusChange, contestID, changes.Payload())
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
	c, err := s.contests.ByID(ctx, contestID)
	if err != nil {
		return err
	}
	if !c.ContentEditable() {
		return fmt.Errorf("%w: the SQL policy cannot change once the contest is %s", ErrNotEditable, c.Status)
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

	changes := audit.Between(current.auditFields(), p.auditFields())

	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.policies.Save(ctx, p); err != nil {
			return err
		}
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

// checkTranslationLanguages applies the same check to authored text. The
// language need not be one the contest declares yet; the publish gate checks
// the set is complete.
func (s *Service) checkTranslationLanguages(ctx context.Context, translations []Translation) error {
	langs := make([]ContestLanguage, 0, len(translations))
	for _, t := range translations {
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

// orDefaultInt is orDefault for a setting whose zero is meaningful, so nil
// stands for "not sent".
func orDefaultInt(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
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

// editableContest loads a contest and refuses if its content is frozen.
func (s *Service) editableContest(ctx context.Context, contestID uuid.UUID) (Contest, error) {
	c, err := s.contests.ByID(ctx, contestID)
	if err != nil {
		return Contest{}, err
	}
	if !c.ContentEditable() {
		return Contest{}, fmt.Errorf("%w: it is %s", ErrNotEditable, c.Status)
	}
	return c, nil
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

// cidrStrings renders a network list for the audit trail.
func cidrStrings(prefixes []netip.Prefix) []string {
	out := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		out = append(out, prefix.String())
	}
	return out
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
