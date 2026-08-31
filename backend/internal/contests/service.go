package contests

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
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
// resolving the people it appoints and enrolls. It is deliberately two
// methods wide — contests neither create accounts nor change them.
type UserDirectory interface {
	ByID(ctx context.Context, id uuid.UUID) (users.User, error)
	ByLogin(ctx context.Context, login string) (users.User, error)
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
	Audit         *audit.Recorder
	UnitOfWork    storage.UnitOfWork
	// Now is the clock, injected so the enrollment deadline is testable.
	Now func() time.Time
}

// Service holds the rules of authoring and running a contest.
type Service struct {
	contests      Repository
	stories       StoryRepository
	questions     QuestionRepository
	managers      ManagerRepository
	registrations RegistrationRepository
	policies      PolicyStore
	languages     LanguageCatalog
	users         UserDirectory
	audit         *audit.Recorder
	uow           storage.UnitOfWork
	now           func() time.Time
}

// NewService assembles the contest service.
func NewService(cfg ServiceConfig) *Service {
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		contests:      cfg.Contests,
		stories:       cfg.Stories,
		questions:     cfg.Questions,
		managers:      cfg.Managers,
		registrations: cfg.Registrations,
		policies:      cfg.Policies,
		languages:     cfg.Languages,
		users:         cfg.Users,
		audit:         cfg.Audit,
		uow:           cfg.UnitOfWork,
		now:           now,
	}
}

// CreateCommand describes a new contest. Everything but the author is
// optional: an organizer starts from an empty draft and fills it in.
type CreateCommand struct {
	ActorID      uuid.UUID
	Enrollment   string
	QuestionMode string
	Timing       string
	DurationMin  *int
	StartsAt     *time.Time
	EndsAt       *time.Time
	AllowedCIDRs []netip.Prefix
	Settings     Settings
	Languages    []ContestLanguage
	Translations []Translation
}

// Create registers a contest and makes its author the owner.
func (s *Service) Create(ctx context.Context, cmd CreateCommand) (Contest, error) {
	c := Contest{
		Status:       StatusDraft,
		Enrollment:   orDefault(cmd.Enrollment, EnrollmentInviteOnly),
		QuestionMode: orDefault(cmd.QuestionMode, QuestionModeMulti),
		Timing:       orDefault(cmd.Timing, TimingFixed),
		DurationMin:  cmd.DurationMin,
		StartsAt:     cmd.StartsAt,
		EndsAt:       cmd.EndsAt,
		AllowedCIDRs: cmd.AllowedCIDRs,
		Settings:     cmd.Settings,
		Languages:    cmd.Languages,
		CreatedBy:    cmd.ActorID,
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
	Timing       string
	DurationMin  *int
	StartsAt     *time.Time
	EndsAt       *time.Time
	// AllowedCIDRs replaces the network restriction. A non-nil empty slice
	// clears it; nil leaves it as it was.
	AllowedCIDRs []netip.Prefix
	Settings     *Settings
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
	case current.Timing != updated.Timing:
		return fmt.Errorf("%w: the timing model cannot change while it runs", ErrNotEditable)
	case !equalDuration(current.DurationMin, updated.DurationMin):
		return fmt.Errorf("%w: the session length cannot change while it runs", ErrNotEditable)
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

func (s *Service) checkPublishable(ctx context.Context, c Contest) error {
	story, err := s.stories.ByContest(ctx, c.ID)
	if err != nil && !errors.Is(err, ErrStoryNotFound) {
		return err
	}
	questions, err := s.questions.List(ctx, c.ID)
	if err != nil {
		return err
	}
	return CheckPublishable(c, story, questions)
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
