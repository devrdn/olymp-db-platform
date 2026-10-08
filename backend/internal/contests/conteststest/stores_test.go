package conteststest

import (
	"context"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/google/uuid"
)

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
			NewContest:    uuid.New,
			GrantAdminAll: func(user uuid.UUID) { admins[user] = true },
			RecordWork:    repo.PutWork,
			Now:           repo.Clock,
		})
	})
}

func TestQuestionsHonoursTheRepositoryContract(t *testing.T) {
	QuestionRepositoryContract(t, func(t *testing.T, run func(context.Context, QuestionTarget)) {
		repo := NewQuestions()
		run(context.Background(), QuestionTarget{Repo: repo, Visible: repo, NewContest: uuid.New})
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
			// The same marker the fixture's unit of work puts on a context.
			context.WithValue(context.Background(), txKey{}, true),
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
