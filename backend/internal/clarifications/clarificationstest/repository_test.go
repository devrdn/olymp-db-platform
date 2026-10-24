package clarificationstest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTheFakeHonoursTheRepositoryContract(t *testing.T) {
	RepositoryContract(t, func(t *testing.T, run func(context.Context, Target)) {
		repo := NewRepository()
		n := 0
		run(context.Background(), Target{
			Repo:       repo,
			NewContest: uuid.New,
			NewParticipant: func(contest uuid.UUID) Participant {
				n++
				p := Participant{Registration: uuid.New(), Login: "login-" + string(rune('a'+n)), FullName: "Name " + string(rune('A'+n))}
				repo.Register(contest, p.Registration, p.Login, p.FullName)
				return p
			},
			NewQuestion: func(contest uuid.UUID) uuid.UUID {
				id := uuid.New()
				repo.AddQuestion(contest, id)
				return id
			},
			NewHiddenQuestion: func(contest uuid.UUID) uuid.UUID {
				id := uuid.New()
				repo.AddHiddenQuestion(contest, id)
				return id
			},
			Close: repo.Close,
			NewStaff: func() (uuid.UUID, string) {
				n++
				id, login := uuid.New(), "staff-"+string(rune('a'+n))
				repo.AddAccount(id, login)
				return id, login
			},
			Now: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC),
		})
	})
}
