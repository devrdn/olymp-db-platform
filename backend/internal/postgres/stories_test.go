package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
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
			// Inside one transaction now() is its start time, which is what
			// updated_at is stamped with, so the clock a row is stamped with
			// is exactly the one read here — through the transaction, as the
			// insert reads it; the pool itself is another session.
			var now time.Time
			if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
				t.Fatalf("read the database clock: %v", err)
			}
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
