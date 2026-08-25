package conteststest

import (
	"context"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/google/uuid"
)

// FixtureNow is the moment every fixture's clock reports, so tests that turn
// on a deadline can state times relative to something stable.
var FixtureNow = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

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
	Users         *userstest.Repository
	Audit         *Sink
	Now           time.Time
}

// NewFixture assembles a service over empty stores.
func NewFixture() *Fixture {
	f := &Fixture{
		Contests:      NewContests(),
		Stories:       NewStories(),
		Questions:     NewQuestions(),
		Managers:      NewManagers(),
		Registrations: NewRegistrations(),
		Policies:      NewPolicies(),
		Languages:     NewLanguages(),
		Users:         userstest.New(),
		Audit:         NewSink(),
		Now:           FixtureNow,
	}
	// Participants carry the login the real repository joins in.
	f.Registrations.Accounts = func(ctx context.Context, id uuid.UUID) (string, string) {
		user, err := f.Users.ByID(ctx, id)
		if err != nil {
			return "", ""
		}
		return user.Login, user.FullName
	}
	f.Service = contests.NewService(contests.ServiceConfig{
		Contests:      f.Contests,
		Stories:       f.Stories,
		Questions:     f.Questions,
		Managers:      f.Managers,
		Registrations: f.Registrations,
		Policies:      f.Policies,
		Languages:     f.Languages,
		Users:         f.Users,
		Audit:         audit.New(f.Audit),
		UnitOfWork:    unitOfWork{},
		Now:           func() time.Time { return f.Now },
	})
	return f
}

// unitOfWork runs the function directly.
//
// It cannot roll back in-memory maps, and no test claims it does: what the
// fixture exercises is the rules, while the atomicity of the writes is a
// property of the real transaction runner and is tested there.
type unitOfWork struct{}

func (unitOfWork) Do(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

// AddUser stores an account the contest service can resolve.
func (f *Fixture) AddUser(login string) users.User {
	return f.Users.Add(users.User{Login: login, FullName: login, Status: users.StatusActive})
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
