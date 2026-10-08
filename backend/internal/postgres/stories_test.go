package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/google/uuid"
)

// What a single caller can observe of the story is the contract every
// contests.StoryRepository and contests.StoryText answers to, the in-memory
// one the service tests use included (conteststest.StoryRepositoryContract).
// What the schema itself enforces (a translation going with its story) is not
// part of it, and has no test here yet.
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
