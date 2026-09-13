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

// UserDirectory is the slice of the account repository this package needs:
// resolving the people it appoints and enrolls, and finding them by a typed
// search. It is deliberately three methods wide and no more — contests
// neither create accounts nor change them.
type UserDirectory interface {
	ByID(ctx context.Context, id uuid.UUID) (users.User, error)
	ByLogin(ctx context.Context, login string) (users.User, error)
	// Search resolves accounts by a substring of their login, full name or
	// email — the picker behind Service.SearchPeople. limit is already
	// bounded by the caller (see MaxDirectoryQueryLength and
	// DirectorySearchMaxLimit in directory.go); this method trusts it.
	// SearchPeople reads only ID, Login and FullName off the result — the
	// real implementation (internal/postgres/users.go) populates exactly
	// those and leaves everything else zero rather than running userColumns'
	// role and permission subqueries for a picker that throws them away.
	Search(ctx context.Context, query string, limit int) ([]users.User, error)
}

// ServiceConfig collects the storage a Service needs. Every field is an
// interface declared in this package, so the rules below are exercised in
// tests without a database.
type ServiceConfig struct {
	Contests      Repository
	Stories       StoryRepository
	Questions     QuestionRepository
	Managers      ManagerRepository
	Registrations RegistrationRepository
	Policies      PolicyStore
	Languages     LanguageCatalog
	Users         UserDirectory
	// Game reads the SQL one contest's game is built from, for
	// Service.ExportPackage and nothing else. Optional at the type level for
	// the same reason Submissions is: a deployment with no game cluster wires
	// none (internal/app builds the game half only when there is a cluster),
	// and a package from such an installation simply carries no game rather
	// than failing over a circuit the contest never had.
	Game GameSource
	// Submissions records participants' answers (submission.go). Optional at
	// the type level so every existing caller that has nothing to do with
	// answering questions keeps compiling unchanged; a Service assembled
	// without one panics the first time Submit is called, the same way a nil
	// map panics on write rather than silently discarding — Submit is the
	// only method that ever touches this field, so nothing else is affected
	// by leaving it unset.
	Submissions SubmissionRepository
	// Sequence answers whether a question may be answered yet in a sequential
	// contest (§6.1.1, sequence.go). Optional at the type level for the same
	// reason Submissions is: only Submit ever reads it, and only when a
	// contest's progression is actually sequential — a caller with nothing to
	// do with answering questions, or an installation that never turns this
	// on, need not supply one.
	Sequence   SequentialGate
	Audit      *audit.Recorder
	UnitOfWork storage.UnitOfWork
	// Now is the clock, injected so the enrollment deadline is testable.
	Now func() time.Time
	// Grace is the network-latency allowance Submit adds to a participant's
	// deadline before refusing an answer for arriving late (§8), matching
	// queryproxy's own default so the two paths share one grace, not one
	// apiece. Taken exactly as given, including zero: an installation that
	// sets DEADLINE_GRACE=0 means no grace, not "unset", and config.Load is
	// the one place that already resolves "unset" to five seconds before
	// this field is ever populated (internal/app/app.go passes
	// cfg.DeadlineGrace straight through) — a second default here would
	// override that deliberate choice right back to five seconds (finding
	// 2). A caller with nothing to do with answering questions, and so no
	// opinion on Grace, simply gets zero, which is harmless because Submit
	// is the only method that reads it.
	Grace time.Duration
	// Logger records the one thing Submit ever has to log rather than fail
	// on: a reference answer whose regex does not compile (submission.go's
	// own grade). Defaults to slog.Default() so a caller that never sets it
	// still gets that anomaly reported somewhere rather than a nil pointer.
	Logger *slog.Logger
	// Sleep is what Submit waits with between a lost attempt-number race and
	// its next retry (submission.go's own attemptBackoff). Defaults to
	// time.Sleep; a test that wants its retries instant sets this to a
	// no-op instead of waiting on the real clock for a scenario it is
	// forcing deterministically.
	Sleep func(time.Duration)
	// DefaultGraceMin is the installation's own default grace period
	// (config.GameInstanceGraceMin, GAME_INSTANCE_GRACE_MIN) — the grace that
	// actually governs a contest for as long as its own
	// Settings.GracePeriodMin reads zero (GracePeriodMin's own doc). Service.
	// ExtendGrace compares an organizer's requested value against this
	// number, not against the stored zero, when a contest never set an
	// explicit grace: internal/postgres/gameinstances.go's reclaimDeadline
	// already falls back to this same installation default for that same
	// contest, so comparing against anything else would let ExtendGrace
	// accept a value the sweep does not actually treat as an extension.
	// Zero (a caller with nothing to do with reclaim, same as Grace above)
	// means ExtendGrace requires nothing more than a positive value the
	// first time — harmless, since such a caller never constructs a Service
	// a contest's own game databases are reclaimed through.
	DefaultGraceMin int
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
	audit           *audit.Recorder
	uow             storage.UnitOfWork
	now             func() time.Time
	grace           time.Duration
	log             *slog.Logger
	sleep           func(time.Duration)
	defaultGraceMin int
}

// NewService assembles the contest service.
func NewService(cfg ServiceConfig) *Service {
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	// cfg.Grace is taken exactly as given, zero included — see its own doc
	// for why a second default here would be finding 2 all over again.
	grace := cfg.Grace
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
		audit:           cfg.Audit,
		uow:             cfg.UnitOfWork,
		now:             now,
		grace:           grace,
		log:             log,
		sleep:           sleep,
		defaultGraceMin: cfg.DefaultGraceMin,
	}
}

// CreateCommand describes a new contest. Everything but the author is
// optional: an organizer starts from an empty draft and fills it in.
type CreateCommand struct {
	ActorID      uuid.UUID
	Enrollment   string
	QuestionMode string
	// Progression decides the order questions may be answered in, empty
	// defaulting to ProgressionFree (§6.1.1).
	Progression string
	// Scoring decides how a result is derived from submissions, empty
	// defaulting to ScoringPoints (§6.1.1).
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

	// One transaction: a contest without an owner has nobody who may appoint
	// staff, and a contest without a policy has undefined SQL access. Neither
	// is a state the installation should ever be able to observe.
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
	// LeaderboardFreezeMin sets the freeze; ClearLeaderboardFreeze removes it.
	// Two fields because nil already means "leave it as it was", and "no
	// freeze" has to be sayable too.
	LeaderboardFreezeMin   *int
	ClearLeaderboardFreeze bool
	LeaderboardNames       string
	// ICPCPenaltyMin sets the ICPC per-attempt penalty, in minutes. Nil means
	// "leave it alone" — the field's own zero value (no penalty at all) is a
	// configuration an organizer can mean, so it cannot double as "unset".
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

	// The session length belongs to individual timing. Without this the switch
	// back is unreachable: a client has no way to send "no duration", and a
	// fixed contest that still carries one does not validate.
	if updated.Timing == TimingFixed {
		updated.DurationMin = nil
	}

	if err := updated.Validate(); err != nil {
		return Contest{}, err
	}
	if err := checkRunningChange(current, updated); err != nil {
		return Contest{}, err
	}

	// What may be recorded is declared once, on the type (Contest.auditFields).
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
// databases survive before the reclaim sweep drops them (§2.4).
//
// SettingsEditable is false for both statuses, and everything else about a
// finished contest stays exactly that immutable — this is the one narrow
// exception, not a second door into Update. Without it an organizer who
// discovers, after the contest already finished, that the reports are not
// done or a dispute is open has no recourse but an installation-wide
// environment variable and a restart, or hand-written SQL against a
// database whose only real safety net was the grace period itself.
//
// graceMin may only grow: refusing to shorten it here is what keeps this
// method reading as "buy more time" rather than a general settings edit that
// merely happens to be reachable once finished. The comparison is against
// the grace actually in force — current.Settings.GracePeriodMin when the
// contest set one explicitly, s.defaultGraceMin (the installation's own
// GAME_INSTANCE_GRACE_MIN, threaded through ServiceConfig.DefaultGraceMin)
// when it did not — never against the stored zero itself. Comparing against
// the raw zero used to accept any positive graceMin as a contest's "first
// explicit" grace, including one far below the installation default the
// reclaim sweep (internal/postgres/gameinstances.go's reclaimDeadline) was
// actually enforcing: ExtendGrace(…, 60) against a contest that never
// configured a grace read as an extension, was recorded as one (0 → 60), and
// in fact cut the real 24-hour default down to one hour, moving the reclaim
// deadline to minutes away. Comparing against the effective grace instead
// means a value that does not clear the installation default is refused
// exactly like a value that does not clear an explicit one.
//
// The Update call below still bumps updated_at (internal/postgres/
// contests.go), and that column is what reclaimDeadline measures from — but
// that is safe here specifically, and only here: ExtendGrace is the one
// write this package lets reach a finished or archived contest at all (the
// status guard just above; the ordinary Update refuses both statuses
// outright), so no unrelated settings edit can ever piggyback on this same
// timestamp bump. And because graceMin is now required to strictly exceed
// the effective grace that was already governing the deadline, resetting
// the anchor to "now" can only ever push that deadline later than it already
// was: now >= the contest's own updated_at, and the new grace is larger than
// the one the old deadline was computed from, so new_deadline = now +
// graceMin is later than old_deadline = old updated_at + effective grace
// however long ago the contest actually finished. The method never produces
// a deadline earlier than the one it replaces.
func (s *Service) ExtendGrace(ctx context.Context, actorID, contestID uuid.UUID, graceMin int) (Contest, error) {
	current, err := s.contests.ByID(ctx, contestID)
	if err != nil {
		return Contest{}, err
	}
	if current.Status != StatusFinished && current.Status != StatusArchived {
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

// checkRunningChange refuses the fields that must not move mid-flight.
//
// Extending a window or correcting a network range helps participants; the
// shape of the contest — how many questions it asks, how the clock works —
// is what they are already answering under.
func checkRunningChange(current, updated Contest) error {
	if current.Status != StatusRunning {
		return nil
	}
	switch {
	case current.QuestionMode != updated.QuestionMode:
		return fmt.Errorf("%w: the question mode cannot change while it runs", ErrNotEditable)
	case current.Progression != updated.Progression:
		// Same reasoning as question_mode: a participant already mid-sequence
		// has answered under one order or the other, and switching it under
		// them changes which question they are allowed to be looking at.
		return fmt.Errorf("%w: the progression cannot change while it runs", ErrNotEditable)
	case current.Scoring != updated.Scoring:
		// The penalty is applied or skipped per submission, at the moment of
		// answering (§6.1.1): moving this mid-run would make earlier answers
		// in the same contest disagree with later ones about whether the
		// penalty counted, for a reason no participant could see.
		return fmt.Errorf("%w: the scoring mode cannot change while it runs", ErrNotEditable)
	case current.Timing != updated.Timing:
		return fmt.Errorf("%w: the timing model cannot change while it runs", ErrNotEditable)
	case !equalDuration(current.DurationMin, updated.DurationMin):
		return fmt.Errorf("%w: the session length cannot change while it runs", ErrNotEditable)
	case !equalDuration(current.LeaderboardFreezeMin, updated.LeaderboardFreezeMin):
		// Moving the freeze mid-run either opens the live table for a moment
		// or hides a table participants have already seen. The label below
		// it is free to change: that is a choice about names, not results.
		return fmt.Errorf("%w: the leaderboard freeze cannot change while it runs", ErrNotEditable)
	case current.ICPCPenaltyMin != updated.ICPCPenaltyMin:
		// The ICPC penalty is not stored with any submission: it is applied
		// when the table is read, to every wrong attempt at once. Changing it
		// mid-run would retroactively rescore everybody's penalty time, and
		// could reorder a table participants have already seen.
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
	c, err := s.contests.ByID(ctx, contestID)
	if err != nil {
		return err
	}
	// Adding a language to a running contest would leave everything authored
	// in it empty for whoever picked it.
	if !c.ContentEditable() {
		return fmt.Errorf("%w: it is %s", ErrNotEditable, c.Status)
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
	c, err := s.contests.ByID(ctx, contestID)
	if err != nil {
		return err
	}
	if !c.ContentEditable() {
		return fmt.Errorf("%w: it is %s", ErrNotEditable, c.Status)
	}
	if err := s.checkTranslationLanguages(ctx, translations); err != nil {
		return err
	}

	// The languages, never the titles. Which languages were authored is the
	// part somebody asks about later; the text itself is content, and keeping
	// its previous copies here would make the trail a version history it
	// cannot serve as (§9.2).
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
// without changing anything. The constructor screen calls it to show the
// remaining work rather than making an organizer discover it by being refused.
func (s *Service) CheckPublish(ctx context.Context, contestID uuid.UUID) error {
	c, err := s.contests.ByID(ctx, contestID)
	if err != nil {
		return err
	}
	return s.checkPublishable(ctx, c)
}

// checkPublishable delegates to the package-level function schedule.go's
// Scheduler shares with this method — the same question, asked from
// Service's own wider StoryRepository and QuestionRepository.
func (s *Service) checkPublishable(ctx context.Context, c Contest) error {
	return checkPublishable(ctx, s.stories, s.questions, c)
}

// Transition moves a contest along its lifecycle.
//
// One entry point rather than Publish/Start/Finish/Archive: the rule about
// which step is legal lives in one table, and the gate hangs off the one step
// that needs it.
func (s *Service) Transition(ctx context.Context, actorID, contestID uuid.UUID, status string) error {
	c, err := s.contests.ByID(ctx, contestID)
	if err != nil {
		return err
	}
	if err := c.CanTransitionTo(status); err != nil {
		return err
	}
	// Both doors, not only the first. Content stays editable while published
	// — an organizer publishes to see the contest as participants will, and
	// may still fix a typo — so "publish, then remove the story, then start"
	// is a sequence the rules allow. The invariant has to hold at the moment
	// participants are actually let in.
	if status == StatusPublished || status == StatusRunning {
		if err := s.checkPublishable(ctx, c); err != nil {
			return err
		}
	}

	return s.uow.Do(ctx, func(ctx context.Context) error {
		// c.Status is what the transition rules and the publish gate above
		// were checked against; the write refuses if it is no longer true.
		if err := s.contests.SetStatus(ctx, contestID, c.Status, status); err != nil {
			return err
		}
		// One shape for every change, so the panel can render it without
		// knowing which action it is looking at.
		changes := audit.NewChanges()
		changes.Set("status", c.Status, status)
		return s.record(ctx, actorID, audit.ActionContestStatusChange, contestID, changes.Payload())
	})
}

// Delete removes a contest that never reached anybody.
//
// Only a draft: once it was published, people could see it, and what they saw
// is a record. Getting rid of one of those is archiving.
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

// SetPolicy changes how much SQL power the contest hands out.
//
// Not once it is running: participants would end up with different powers
// depending on when they connected, and the template grants — built from this
// row — would no longer match what the validator enforces.
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

	// How much power a participant gets, and what it was before: the whole
	// question after an incident.
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

// checkTranslationLanguages applies the same check to authored text.
//
// The language need not be one the contest declares: authoring a translation
// before deciding to offer it is a normal order of work, and the publish gate
// is what insists on the set being complete.
func (s *Service) checkTranslationLanguages(ctx context.Context, translations []Translation) error {
	langs := make([]ContestLanguage, 0, len(translations))
	for _, t := range translations {
		langs = append(langs, ContestLanguage{Code: t.Lang})
	}
	return s.checkLanguages(ctx, langs)
}

// record appends an audit entry for an action on a contest.
//
// A failure is returned rather than swallowed: these are privileged
// operations, and an action nobody can account for afterwards is worse than a
// failed one. Callers run these inside a unit of work, so the action rolls
// back with the entry.
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

// orDefaultInt is orDefault's pointer-typed counterpart, for a setting whose
// zero value (unlike an empty string) is a configuration an organizer can
// mean and so cannot itself stand for "not sent" — nil is what says that.
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

// cidrStrings renders a network list for the trail, where a printed prefix is
// what somebody reading it a year later can act on.
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
