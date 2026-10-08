package conteststest

import (
	"context"
	"testing"
	"time"

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
