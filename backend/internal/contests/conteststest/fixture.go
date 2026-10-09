package conteststest

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/google/uuid"
)

// FixtureNow is what every fixture's clock reports.
var FixtureNow = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

// FixtureDefaultGraceMin mirrors the installation default of
// GAME_INSTANCE_GRACE_MIN (24 hours), so Service.ExtendGrace runs against a
// realistic value.
const FixtureDefaultGraceMin = 24 * 60

// Fixture is a contest service wired to in-memory storage, with each store
// exposed so a test can arrange the state it needs and inspect what happened.
type Fixture struct {
	Service       *contests.Service
	Contests      *Contests
	Stories       *Stories
	Questions     *Questions
	Managers      *Managers
	Registrations *Registrations
	Policies      *Policies
	Languages     *Languages
	Games         *Games
	Covers        *Covers
	Submissions   *Submissions
	Sequence      *SequentialProgress
	Users         *userstest.Repository
	Audit         *Sink
	UnitOfWork    *UnitOfWork
	Now           time.Time
	// PoolTrigger records every contest Enroll or AddParticipants asked the
	// pool tender to wake for.
	PoolTrigger *PoolTrigger
	// Gate is the gate Service was built with (zero grace unless WithGrace),
	// exposed so another consumer can share it, as internal/app does.
	Gate *contests.Gate

	// config lets WithGrace rebuild Service over the same stores.
	config contests.ServiceConfig
}

// NewFixture assembles a service over empty stores.
func NewFixture() *Fixture {
	uow := &UnitOfWork{}
	f := &Fixture{
		Contests:      NewContests(),
		Stories:       NewStories(),
		Questions:     NewQuestions(),
		Managers:      NewManagers(),
		Registrations: NewRegistrations(),
		Policies:      NewPolicies(),
		Languages:     NewLanguages(),
		Games:         NewGames(),
		Covers:        NewCovers(),
		Submissions:   NewSubmissions(),
		Users:         userstest.New(),
		Audit:         NewSink(),
		UnitOfWork:    uow,
		Now:           FixtureNow,
		PoolTrigger:   NewPoolTrigger(uow),
	}
	f.Sequence = NewSequentialProgress(f.Questions, f.Submissions)
	// Participants and staff carry the login the real repositories join in.
	accounts := func(ctx context.Context, id uuid.UUID) (string, string) {
		user, err := f.Users.ByID(ctx, id)
		if err != nil {
			return "", ""
		}
		return user.Login, user.FullName
	}
	f.Registrations.Accounts = accounts
	f.Managers.Accounts = accounts
	// The contest foreign keys are enforced. Registrations.UserExists stays
	// nil: service paths resolve the account first, and the leaderboard and
	// profile rigs register accounts they never create.
	f.Questions.ContestExists = f.Contests.Exists
	f.Stories.ContestExists = f.Contests.Exists
	f.Policies.ContestExists = f.Contests.Exists
	f.Registrations.ContestExists = f.Contests.Exists
	// The publish gate reads these to find a participant who administers
	// every contest.
	f.Registrations.Permissions = func(ctx context.Context, id uuid.UUID) []string {
		user, err := f.Users.ByID(ctx, id)
		if err != nil {
			return nil
		}
		return user.Permissions
	}
	// The stores' clocks stand in for the database's now() and share the
	// application clock; a test about the gap between them sets its own.
	f.Submissions.Clock = func() time.Time { return f.Now }
	f.Registrations.Clock = func() time.Time { return f.Now }
	f.Managers.Clock = func() time.Time { return f.Now }
	f.Contests.Clock = func() time.Time { return f.Now }
	f.Stories.Clock = func() time.Time { return f.Now }
	f.Policies.Clock = func() time.Time { return f.Now }
	f.Contests.Rosters(f.Managers, f.Registrations)

	f.Gate = contests.NewGate(0)
	f.config = contests.ServiceConfig{
		Contests:      f.Contests,
		Stories:       f.Stories,
		Questions:     f.Questions,
		Managers:      f.Managers,
		Registrations: f.Registrations,
		Policies:      f.Policies,
		Languages:     f.Languages,
		Game:          f.Games,
		Covers:        f.Covers,
		Submissions:   f.Submissions,
		Sequence:      f.Sequence,
		Users:         f.Users,
		Audit:         audit.New(f.Audit),
		UnitOfWork:    f.UnitOfWork,
		Now:           func() time.Time { return f.Now },
		Gate:          f.Gate,
		// Quiet, so defensive log lines do not flood test output.
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		// Forced retries (ConflictsRemaining) need no real backoff.
		Sleep:           func(time.Duration) {},
		DefaultGraceMin: FixtureDefaultGraceMin,
		PoolTrigger:     f.PoolTrigger,
	}
	f.Service = contests.NewService(f.config)
	return f
}

// WithGrace rebuilds Service and Gate with grace as the deadline allowance.
// Every other fixture has none: an instant past a deadline is past it.
func (f *Fixture) WithGrace(grace time.Duration) *Fixture {
	f.Gate = contests.NewGate(grace)
	f.config.Gate = f.Gate
	f.Service = contests.NewService(f.config)
	return f
}

// UnitOfWork runs fn directly and marks the context as inside a transaction.
// It cannot roll back the maps; atomicity is tested against the real runner.
// The mark lets the fakes tell which writes happened with a transaction open.
type UnitOfWork struct {
	// Calls counts transactions opened, so a test can assert an operation
	// took exactly one.
	Calls int
	// Open is true only while Do runs fn, so a fake sharing this UnitOfWork
	// (PoolTrigger) can prove it acted after the transaction ended.
	Open bool
}

func (u *UnitOfWork) Do(ctx context.Context, fn func(context.Context) error) error {
	u.Calls++
	u.Open = true
	defer func() { u.Open = false }()
	return fn(context.WithValue(ctx, txKey{}, true))
}

// txKey marks a context inside the fixture's unit of work, as the real runner
// marks its context with the transaction (storage.QuerierFrom).
type txKey struct{}

func inTx(ctx context.Context) bool {
	open, _ := ctx.Value(txKey{}).(bool)
	return open
}

// AddUser stores an account the contest service can resolve.
func (f *Fixture) AddUser(login string) users.User {
	return f.Users.Add(users.User{Login: login, FullName: login, Status: users.StatusActive})
}

// AddAdministrator stores an active account holding contest.admin_all, staff
// of every contest without an appointment. It goes through a role because
// Permissions derive from Roles, as role_permissions does in the real schema.
func (f *Fixture) AddAdministrator(login string) users.User {
	const role = "administrator"
	f.Users.GrantRole(role, rbac.PermissionContestAdminAll)
	return f.Users.Add(users.User{
		Login:    login,
		FullName: login,
		Status:   users.StatusActive,
		Roles:    []string{role},
	})
}

// SeedContest stores a contest in the given status, with a schedule and one
// language, so tests about status rules do not each have to build one.
func (f *Fixture) SeedContest(status string) contests.Contest {
	start := FixtureNow.Add(-time.Hour)
	end := FixtureNow.Add(2 * time.Hour)

	return f.Contests.Put(contests.Contest{
		Status:       status,
		Enrollment:   contests.EnrollmentInviteOnly,
		QuestionMode: contests.QuestionModeMulti,
		Progression:  contests.ProgressionFree,
		Scoring:      contests.ScoringPoints,
		Timing:       contests.TimingFixed,
		StartsAt:     &start,
		EndsAt:       &end,
		Languages:    []contests.ContestLanguage{{Code: "en", IsDefault: true}},
		Translations: map[string]contests.Translation{
			"en": {Lang: "en", Title: "The Library Murder"},
		},
		CreatedBy: uuid.New(),
	})
}

// SeedPublishableContest stores a draft that satisfies the publish gate: one
// language, a title, a story and one question with a reference answer.
func (f *Fixture) SeedPublishableContest() contests.Contest {
	c := f.SeedContest(contests.StatusDraft)

	if _, err := f.Stories.Save(context.Background(), c.ID, map[string]string{
		"en": "A body in the stacks.",
	}); err != nil {
		panic(err)
	}
	f.Questions.Put(contests.Question{
		ContestID: c.ID,
		Ord:       1,
		Kind:      contests.KindFinal,
		Points:    10,
		IsVisible: true,
		Texts:     map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
		Answers:   []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the butler"}},
	})
	return c
}
