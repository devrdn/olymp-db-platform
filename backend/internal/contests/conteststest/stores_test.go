package conteststest

import (
	"context"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/google/uuid"
)

// inUnitOfWork is a context carrying the same marker the fixture's unit of
// work puts on one, which is what the fakes that refuse to run outside a
// transaction look for.
func inUnitOfWork() context.Context {
	return context.WithValue(context.Background(), txKey{}, true)
}

func TestSubmissionsHonoursTheRepositoryContract(t *testing.T) {
	SubmissionRepositoryContract(t, func(t *testing.T, run func(context.Context, SubmissionTarget)) {
		repo := NewSubmissions()
		repo.Clock = func() time.Time { return FixtureNow }
		run(context.Background(), SubmissionTarget{
			Repo:           repo,
			RegistrationID: uuid.New(),
			QuestionID:     uuid.New(),
			Now:            repo.Clock,
		})
	})
}

func TestRegistrationsHonoursTheRepositoryContract(t *testing.T) {
	RegistrationRepositoryContract(t, func(t *testing.T, run func(context.Context, RegistrationTarget)) {
		type account struct{ login, fullName string }
		var (
			accounts = map[uuid.UUID]account{}
			admins   = map[uuid.UUID]bool{}
		)
		repo := NewRegistrations()
		repo.Clock = func() time.Time { return FixtureNow }
		repo.Accounts = func(_ context.Context, id uuid.UUID) (string, string) {
			return accounts[id].login, accounts[id].fullName
		}
		known := map[uuid.UUID]bool{}
		repo.ContestExists = func(id uuid.UUID) bool { return known[id] }
		repo.UserExists = func(id uuid.UUID) bool {
			_, ok := accounts[id]
			return ok
		}
		repo.Permissions = func(_ context.Context, id uuid.UUID) []string {
			if admins[id] {
				return []string{rbac.PermissionContestAdminAll}
			}
			return nil
		}
		run(context.Background(), RegistrationTarget{
			Repo: repo,
			NewUser: func(login, fullName string) uuid.UUID {
				id := uuid.New()
				accounts[id] = account{login, fullName}
				return id
			},
			NewContest: func() uuid.UUID {
				id := uuid.New()
				known[id] = true
				return id
			},
			GrantAdminAll: func(user uuid.UUID) { admins[user] = true },
			RecordWork:    repo.PutWork,
			Now:           repo.Clock,
		})
	})
}

func TestQuestionsHonoursTheRepositoryContract(t *testing.T) {
	QuestionRepositoryContract(t, func(t *testing.T, run func(context.Context, QuestionTarget)) {
		repo := NewQuestions()
		known := map[uuid.UUID]bool{}
		repo.ContestExists = func(id uuid.UUID) bool { return known[id] }
		run(inUnitOfWork(), QuestionTarget{
			Repo: repo, Visible: repo,
			NewContest: func() uuid.UUID {
				id := uuid.New()
				known[id] = true
				return id
			},
			Outside: context.Background(),
		})
	})
}

func TestContestsHonoursTheRepositoryContract(t *testing.T) {
	ContestRepositoryContract(t, func(t *testing.T, run func(context.Context, ContestTarget)) {
		var (
			repo          = NewContests()
			managers      = NewManagers()
			registrations = NewRegistrations()
		)
		repo.Clock = func() time.Time { return FixtureNow }
		repo.Rosters(managers, registrations)
		run(
			inUnitOfWork(),
			ContestTarget{
				Repo:    repo,
				NewUser: uuid.New,
				Appoint: func(contest, user uuid.UUID, role rbac.ContestRole) {
					if err := managers.Grant(context.Background(), contests.Manager{
						ContestID: contest, UserID: user, Role: role, GrantedBy: user,
					}); err != nil {
						t.Fatalf("Grant() = %v", err)
					}
				},
				Register: func(contest, user uuid.UUID) {
					if _, err := registrations.Add(context.Background(), contest, user); err != nil {
						t.Fatalf("Add() = %v", err)
					}
				},
				Outside: context.Background(),
				Now:     repo.Clock,
			})
	})
}

func TestManagersHonoursTheRepositoryContract(t *testing.T) {
	ManagerRepositoryContract(t, func(t *testing.T, run func(context.Context, ManagerTarget)) {
		type account struct{ login, fullName string }
		accounts := map[uuid.UUID]account{}
		repo := NewManagers()
		repo.Clock = func() time.Time { return FixtureNow }
		repo.Accounts = func(_ context.Context, id uuid.UUID) (string, string) {
			return accounts[id].login, accounts[id].fullName
		}
		run(context.Background(), ManagerTarget{
			Repo: repo,
			NewUser: func(login, fullName string) uuid.UUID {
				id := uuid.New()
				accounts[id] = account{login, fullName}
				return id
			},
			NewContest: uuid.New,
			Now:        repo.Clock,
		})
	})
}

func TestAttemptsHonoursTheStoreContract(t *testing.T) {
	AttemptStoreContract(t, func(t *testing.T, run func(context.Context, AttemptTarget)) {
		questions := NewQuestions()
		submissions := NewSubmissions()
		submissions.Clock = func() time.Time { return FixtureNow }
		run(inUnitOfWork(), AttemptTarget{
			Store:           NewAttempts(submissions),
			Questions:       questions,
			Submissions:     submissions,
			ContestID:       uuid.New(),
			RegistrationID:  uuid.New(),
			NewRegistration: uuid.New,
			Now:             submissions.Clock,
		})
	})
}

func TestSequentialProgressHonoursTheGateContract(t *testing.T) {
	SequentialGateContract(t, func(t *testing.T, run func(context.Context, SequenceTarget)) {
		questions := NewQuestions()
		submissions := NewSubmissions()
		submissions.Clock = func() time.Time { return FixtureNow }
		run(inUnitOfWork(), SequenceTarget{
			Gate:            NewSequentialProgress(questions, submissions),
			Questions:       questions,
			Submissions:     submissions,
			ContestID:       uuid.New(),
			RegistrationID:  uuid.New(),
			NewRegistration: uuid.New,
			NewContest:      uuid.New,
			Now:             submissions.Clock,
		})
	})
}

func TestStoriesHonoursTheRepositoryContract(t *testing.T) {
	StoryRepositoryContract(t, func(t *testing.T, run func(context.Context, StoryTarget)) {
		repo := NewStories()
		repo.Clock = func() time.Time { return FixtureNow }
		known := map[uuid.UUID]bool{}
		repo.ContestExists = func(id uuid.UUID) bool { return known[id] }
		run(context.Background(), StoryTarget{
			Repo: repo,
			Text: repo,
			NewContest: func() uuid.UUID {
				id := uuid.New()
				known[id] = true
				return id
			},
			Now: repo.Clock,
		})
	})
}

func TestLanguagesHonoursTheCatalogContract(t *testing.T) {
	LanguageCatalogContract(t, func(t *testing.T, run func(context.Context, LanguageTarget)) {
		catalog := NewLanguages()
		run(context.Background(), LanguageTarget{
			Catalog: catalog,
			Add: func(l contests.Language) {
				catalog.Available = append(catalog.Available, l)
			},
			Retire: func(code string) {
				for i := range catalog.Available {
					if catalog.Available[i].Code == code {
						catalog.Available[i].IsActive = false
					}
				}
			},
		})
	})
}

func TestPoliciesHonoursTheStoreContract(t *testing.T) {
	PolicyStoreContract(t, func(t *testing.T, run func(context.Context, PolicyTarget)) {
		store := NewPolicies()
		store.Clock = func() time.Time { return FixtureNow }
		known := map[uuid.UUID]bool{}
		store.ContestExists = func(id uuid.UUID) bool { return known[id] }
		run(context.Background(), PolicyTarget{
			Store: store,
			NewContest: func() uuid.UUID {
				id := uuid.New()
				known[id] = true
				return id
			},
			NewUser: uuid.New,
			Now:     store.Clock,
		})
	})
}
