package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
)

func TestStoryIsCreatedOnFirstSave(t *testing.T) {
	// A contest has no story row until one is written, so saving has to create
	// it; requiring a separate "create the story" step would be a state the
	// editor could get stuck in.
	withTx(t, func(ctx context.Context) {
		repo := NewStories(testPool)
		author := makeUser(t, ctx, "author-story")
		id := makeContest(t, ctx, author.ID)

		saved, err := repo.Save(ctx, id, map[string]string{"en": "A body in the stacks."})
		if err != nil {
			t.Fatalf("Save() = %v", err)
		}

		loaded, err := repo.ByContest(ctx, id)
		if err != nil {
			t.Fatalf("ByContest() = %v", err)
		}
		if loaded.ID != saved.ID {
			t.Errorf("story id = %v, want %v", loaded.ID, saved.ID)
		}
		if body, _ := loaded.Body("en"); body != "A body in the stacks." {
			t.Errorf("body = %q", body)
		}
	})
}

func TestSavingTheStoryAgainDropsTheLanguagesLeftOut(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewStories(testPool)
		author := makeUser(t, ctx, "author-story-replace")
		id := makeContest(t, ctx, author.ID)

		if _, err := repo.Save(ctx, id, map[string]string{
			"en": "A body in the stacks.",
			"ro": "Un cadavru între rafturi.",
		}); err != nil {
			t.Fatalf("Save() = %v", err)
		}
		if _, err := repo.Save(ctx, id, map[string]string{"en": "A body in the stacks."}); err != nil {
			t.Fatalf("Save() = %v", err)
		}

		loaded, _ := repo.ByContest(ctx, id)
		if _, ok := loaded.Body("ro"); ok {
			t.Error("the Romanian story is still there, want it replaced away")
		}
	})
}

func TestAContestWithNoStoryReportsNotFound(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-nostory")
		id := makeContest(t, ctx, author.ID)

		if _, err := NewStories(testPool).ByContest(ctx, id); !errors.Is(err, contests.ErrStoryNotFound) {
			t.Errorf("ByContest() = %v, want ErrStoryNotFound", err)
		}
	})
}

// BodyIn is the participant's read: one language's text, and the same
// ErrStoryNotFound for a contest with no story as for a story with nothing in
// that language.
func TestBodyInReadsOneLanguageOfTheStory(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewStories(testPool)
		author := makeUser(t, ctx, "author-story-bodyin")
		id := makeContest(t, ctx, author.ID)
		empty := makeContest(t, ctx, author.ID)

		if _, err := repo.Save(ctx, id, map[string]string{
			"en": "A body in the stacks.",
			"ro": "Un cadavru între rafturi.",
		}); err != nil {
			t.Fatalf("Save() = %v", err)
		}

		if body, err := repo.BodyIn(ctx, id, "ro"); err != nil || body != "Un cadavru între rafturi." {
			t.Errorf("BodyIn(ro) = %q, %v", body, err)
		}
		if _, err := repo.BodyIn(ctx, id, "ru"); !errors.Is(err, contests.ErrStoryNotFound) {
			t.Errorf("BodyIn(ru) = %v, want ErrStoryNotFound", err)
		}
		if _, err := repo.BodyIn(ctx, empty, "en"); !errors.Is(err, contests.ErrStoryNotFound) {
			t.Errorf("BodyIn(no story) = %v, want ErrStoryNotFound", err)
		}
	})
}
