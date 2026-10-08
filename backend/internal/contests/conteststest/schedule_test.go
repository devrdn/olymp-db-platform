package conteststest

import (
	"context"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

func TestScheduleHonoursTheRepositoryContract(t *testing.T) {
	ScheduleRepositoryContract(t, func(t *testing.T, run func(context.Context, ScheduleTarget)) {
		schedule := NewSchedule()
		run(context.Background(), ScheduleTarget{
			Repo:     schedule,
			Contests: schedule.Contests,
			Seed: func(status string, startsAt, endsAt *time.Time) uuid.UUID {
				return schedule.Contests.Put(contests.Contest{
					Status: status, StartsAt: startsAt, EndsAt: endsAt,
				}).ID
			},
			Now: schedule.Contests.Clock,
		})
	})
}
