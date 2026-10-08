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

// FixtureNow is the moment every fixture's clock reports, so tests that turn
// on a deadline can state times relative to something stable.
var FixtureNow = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

// FixtureDefaultGraceMin is the fixture's stand-in for the installation's own
// GAME_INSTANCE_GRACE_MIN (config.GameInstanceGraceMin) — the real default is
// 24 hours (internal/platform/config/config.go), and this mirrors that
// number so a test can exercise Service.ExtendGrace against a realistic
// installation default rather than the fixture's own arbitrary zero.
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
	// pool tender to wake for. Wired in by default so an ordinary test that
	// never mentions it still gets a real fake rather than a nil interface —
	// a test about the trigger itself asserts on f.PoolTrigger.Triggered, and
	// every other test simply never looks.
	PoolTrigger *PoolTrigger
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
	// Derived from the same question and submission stores above, not a
	// third store of its own — see SequentialProgress's own doc.
	f.Sequence = NewSequentialProgress(f.Questions, f.Submissions)
	// Participants carry the login the real repository joins in.
	f.Registrations.Accounts = func(ctx context.Context, id uuid.UUID) (string, string) {
		user, err := f.Users.ByID(ctx, id)
		if err != nil {
			return "", ""
		}
		return user.Login, user.FullName
	}
	// And what that account may do, which is what the publish gate reads to
	// find a participant who administers every contest.
	f.Registrations.Permissions = func(ctx context.Context, id uuid.UUID) []string {
		user, err := f.Users.ByID(ctx, id)
		if err != nil {
			return nil
		}
		return user.Permissions
	}
	// The submission store's own clock defaults to the same fixture.Now a
	// test already controls for the application clock — the honest default,
	// since the two only need to differ when a test is specifically
	// exercising the gap between them (§8's whole reason for asking the core
	// database's own clock rather than trusting the caller's).
	f.Submissions.Clock = func() time.Time { return f.Now }
	// A registration is stamped by the same clock, the way the table stamps
	// it with the database's own.
	f.Registrations.Clock = func() time.Time { return f.Now }
	// And a contest by the same one, the way its own table stamps it.
	f.Contests.Clock = func() time.Time { return f.Now }
	// The listing of a user's contests and of what a participant may see
	// reads who staffs and who is registered, which are these two stores.
	f.Contests.Rosters(f.Managers, f.Registrations)

	f.Service = contests.NewService(contests.ServiceConfig{
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
		// Quiet by default: a test exercising submission.go's own defensive
		// log line (a malformed reference answer) should not spray a fixed
		// test suite's output with it.
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		// A test that forces the attempt-race retry loop (ConflictsRemaining)
		// is exercising the loop's own logic, not the real clock — waiting
		// out attemptBackoff's real jitter on every one of those retries
		// would make the suite slower for nothing a fixture-backed test
		// could ever observe.
		Sleep: func(time.Duration) {},
		// See FixtureDefaultGraceMin's own doc.
		DefaultGraceMin: FixtureDefaultGraceMin,
		PoolTrigger:     f.PoolTrigger,
	})
	return f
}

// UnitOfWork runs the function directly, and marks the context while it does.
//
// It cannot roll back in-memory maps, and no test claims it does: what the
// fixture exercises is the rules, while the atomicity of the writes is a
// property of the real transaction runner and is tested there. What it can
// answer is which writes happened with a transaction open — the fact the
// trail's guarantee rests on, and the reason for the mark.
type UnitOfWork struct {
	// Calls counts the transactions opened, so a test can assert an operation
	// took exactly one rather than a transaction per statement.
	Calls int
	// Open is true only while Do is running fn — cleared again before Do
	// returns, success or failure. A fake that holds the same UnitOfWork (see
	// PoolTrigger) can check it to prove something happened after the
	// transaction ended rather than from inside it.
	Open bool
}

func (u *UnitOfWork) Do(ctx context.Context, fn func(context.Context) error) error {
	u.Calls++
	u.Open = true
	defer func() { u.Open = false }()
	return fn(context.WithValue(ctx, txKey{}, true))
}

// txKey marks a context running inside the fixture's unit of work. The real
// runner marks its context the same way, with the transaction itself, which is
// how a repository knows to write through it (storage.QuerierFrom).
type txKey struct{}

// inTx reports whether ctx is running inside the fixture's unit of work.
func inTx(ctx context.Context) bool {
	open, _ := ctx.Value(txKey{}).(bool)
	return open
}

// AddUser stores an account the contest service can resolve.
func (f *Fixture) AddUser(login string) users.User {
	return f.Users.Add(users.User{Login: login, FullName: login, Status: users.StatusActive})
}

// AddAdministrator stores an active account holding contest.admin_all: staff
// of every contest without being appointed to any of them.
//
// Through a role, because that is the only way an account has a permission —
// the repository derives Permissions from Roles, as the real one derives them
// from the role_permissions table.
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
