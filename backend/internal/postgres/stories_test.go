package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/google/uuid"
)

func TestStoriesHonoursTheRepositoryContract(t *testing.T) {
	conteststest.StoryRepositoryContract(t, func(t *testing.T, run func(context.Context, conteststest.StoryTarget)) {
		withTx(t, func(ctx context.Context) {
			author := makeUser(t, ctx, "author-story")
			now := txNow(t, ctx)
			repo := NewStories(testPool)
			run(ctx, conteststest.StoryTarget{
				Repo:       repo,
				Text:       repo,
				NewContest: func() uuid.UUID { return makeContest(t, ctx, author.ID) },
				Now:        func() time.Time { return now },
			})
		})
	})
}
