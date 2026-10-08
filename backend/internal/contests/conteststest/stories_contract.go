package conteststest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

// StoryTarget is what one case of the contract runs against: a repository
// holding no stories yet, and the means to create what a story hangs off. A
// real schema needs a contest to exist before a story can name it, so each
// implementation fills NewContest its own way: the in-memory store mints an
// identifier and remembers it, PostgreSQL inserts a row.
type StoryTarget struct {
	Repo contests.StoryRepository
	// Text is the participant's read over the same stories as Repo: what Repo
	// saved is what Text serves.
	Text contests.StoryText
	// NewContest creates a contest and returns its identifier.
	NewContest func() uuid.UUID
	// Now is what the store's clock reads when a story is written. A story's
	// UpdatedAt is that clock, so the contract can only state it in its terms.
	Now func() time.Time
}

// StoryRepositoryContract is what every contests.StoryRepository and
// contests.StoryText must do, run as subtests against one implementation. Both
// the in-memory Stories and postgres.Stories run it, so the store the service
// tests trust and the store production uses are held to the same answers: a
// rule the fake got wrong would otherwise pass every service test and fail
// only in a contest.
//
// each runs one case: it prepares a fresh target, calls run with it and the
// context to call the repository with, and cleans up afterwards. Only the
// behaviour a single caller can observe is here. The clock cannot be moved
// between two saves inside one case, so the contract states that a save
// stamps the story with the store's clock, and not that a later save moves it
// on.
//
// Texts use the language codes "en", "ro" and "ru", which the real schema
// seeds.
func StoryRepositoryContract(t *testing.T, each func(t *testing.T, run func(context.Context, StoryTarget))) {
	save := func(t *testing.T, ctx context.Context, target StoryTarget, contest uuid.UUID, bodies map[string]string) contests.Story {
		t.Helper()
		saved, err := target.Repo.Save(ctx, contest, bodies)
		if err != nil {
			t.Fatalf("Save() = %v", err)
		}
		return saved
	}
	load := func(t *testing.T, ctx context.Context, target StoryTarget, contest uuid.UUID) contests.Story {
		t.Helper()
		story, err := target.Repo.ByContest(ctx, contest)
		if err != nil {
			t.Fatalf("ByContest() = %v", err)
		}
		return story
	}
	// wantBodies fails the case unless the story's text is exactly want.
	wantBodies := func(t *testing.T, got contests.Story, want map[string]string, what string) {
		t.Helper()
		if len(got.Bodies) != len(want) {
			t.Errorf("%s: bodies = %v, want %v", what, got.Bodies, want)
			return
		}
		for lang, body := range want {
			if got.Bodies[lang] != body {
				t.Errorf("%s: body in %q = %q, want %q", what, lang, got.Bodies[lang], body)
			}
		}
	}
	notFound := func(t *testing.T, err error, what string) {
		t.Helper()
		if !errors.Is(err, contests.ErrStoryNotFound) {
			t.Errorf("%s error = %v, want ErrStoryNotFound", what, err)
		}
	}

	const (
		english  = "A body in the stacks."
		romanian = "Un cadavru între rafturi."
	)

	cases := []struct {
		name string
		body func(t *testing.T, ctx context.Context, target StoryTarget)
	}{
		{"a contest with no story reports not found", func(t *testing.T, ctx context.Context, target StoryTarget) {
			_, err := target.Repo.ByContest(ctx, target.NewContest())
			notFound(t, err, "ByContest()")
		}},
		{"the first save creates the story and returns it", func(t *testing.T, ctx context.Context, target StoryTarget) {
			contest := target.NewContest()

			saved := save(t, ctx, target, contest, map[string]string{"en": english, "ro": romanian})

			if saved.ID == uuid.Nil {
				t.Error("saved story has no id")
			}
			if saved.ContestID != contest {
				t.Errorf("saved contest = %v, want %v", saved.ContestID, contest)
			}
			wantBodies(t, saved, map[string]string{"en": english, "ro": romanian}, "saved")
			if want := target.Now(); !saved.UpdatedAt.Equal(want) {
				t.Errorf("saved UpdatedAt = %v, want the store's clock %v", saved.UpdatedAt, want)
			}

			loaded := load(t, ctx, target, contest)
			if loaded.ID != saved.ID {
				t.Errorf("loaded id = %v, want %v", loaded.ID, saved.ID)
			}
			if loaded.ContestID != contest {
				t.Errorf("loaded contest = %v, want %v", loaded.ContestID, contest)
			}
			wantBodies(t, loaded, map[string]string{"en": english, "ro": romanian}, "loaded")
			if want := target.Now(); !loaded.UpdatedAt.Equal(want) {
				t.Errorf("loaded UpdatedAt = %v, want the store's clock %v", loaded.UpdatedAt, want)
			}
		}},
		{"saving again replaces the whole set of languages and keeps the story", func(t *testing.T, ctx context.Context, target StoryTarget) {
			contest := target.NewContest()
			first := save(t, ctx, target, contest, map[string]string{"en": english, "ro": romanian})

			second := save(t, ctx, target, contest, map[string]string{"en": "The body was in the archive.", "ru": "Тело нашли в архиве."})

			want := map[string]string{"en": "The body was in the archive.", "ru": "Тело нашли в архиве."}
			if second.ID != first.ID {
				t.Errorf("story id after the second save = %v, want the first save's %v", second.ID, first.ID)
			}
			wantBodies(t, second, want, "returned by the second save")
			wantBodies(t, load(t, ctx, target, contest), want, "loaded after the second save")
		}},
		{"saving the same text again changes nothing observable", func(t *testing.T, ctx context.Context, target StoryTarget) {
			contest := target.NewContest()
			bodies := map[string]string{"en": english, "ro": romanian}
			first := save(t, ctx, target, contest, bodies)

			second := save(t, ctx, target, contest, bodies)

			if second.ID != first.ID {
				t.Errorf("story id after the repeat = %v, want %v", second.ID, first.ID)
			}
			wantBodies(t, second, bodies, "returned by the repeat")
			wantBodies(t, load(t, ctx, target, contest), bodies, "loaded after the repeat")
		}},
		{"saving no languages leaves a story with no text", func(t *testing.T, ctx context.Context, target StoryTarget) {
			contest := target.NewContest()
			first := save(t, ctx, target, contest, map[string]string{"en": english})

			emptied := save(t, ctx, target, contest, map[string]string{})

			if emptied.ID != first.ID {
				t.Errorf("story id after emptying = %v, want %v", emptied.ID, first.ID)
			}
			wantBodies(t, emptied, map[string]string{}, "returned by the empty save")
			wantBodies(t, load(t, ctx, target, contest), map[string]string{}, "loaded after the empty save")
		}},
		{"a story is a contest's own", func(t *testing.T, ctx context.Context, target StoryTarget) {
			first, second := target.NewContest(), target.NewContest()
			saved := save(t, ctx, target, first, map[string]string{"en": english})

			_, err := target.Repo.ByContest(ctx, second)
			notFound(t, err, "ByContest() of the other contest")

			other := save(t, ctx, target, second, map[string]string{"ro": romanian})
			if other.ID == saved.ID {
				t.Errorf("both contests got story id %v", saved.ID)
			}
			wantBodies(t, load(t, ctx, target, first), map[string]string{"en": english}, "first contest")
			wantBodies(t, load(t, ctx, target, second), map[string]string{"ro": romanian}, "second contest")
		}},
		{"delete removes the story and only that contest's", func(t *testing.T, ctx context.Context, target StoryTarget) {
			doomed, kept := target.NewContest(), target.NewContest()
			save(t, ctx, target, doomed, map[string]string{"en": english})
			save(t, ctx, target, kept, map[string]string{"en": english})

			if err := target.Repo.Delete(ctx, doomed); err != nil {
				t.Fatalf("Delete() = %v", err)
			}

			_, err := target.Repo.ByContest(ctx, doomed)
			notFound(t, err, "ByContest() after Delete")
			wantBodies(t, load(t, ctx, target, kept), map[string]string{"en": english}, "the other contest")
		}},
		{"deleting a story that is not there is not an error", func(t *testing.T, ctx context.Context, target StoryTarget) {
			contest := target.NewContest()

			if err := target.Repo.Delete(ctx, contest); err != nil {
				t.Errorf("Delete() of a contest with no story = %v, want nil", err)
			}
			if err := target.Repo.Delete(ctx, contest); err != nil {
				t.Errorf("second Delete() = %v, want nil", err)
			}
		}},
		{"a story saved after a delete is a new one", func(t *testing.T, ctx context.Context, target StoryTarget) {
			contest := target.NewContest()
			before := save(t, ctx, target, contest, map[string]string{"en": english, "ro": romanian})
			if err := target.Repo.Delete(ctx, contest); err != nil {
				t.Fatalf("Delete() = %v", err)
			}

			after := save(t, ctx, target, contest, map[string]string{"en": "The second draft."})

			if after.ID == before.ID {
				t.Errorf("story id after delete and save = %v, want a new one", after.ID)
			}
			wantBodies(t, load(t, ctx, target, contest), map[string]string{"en": "The second draft."}, "loaded")
		}},
		{"the text a caller holds is not the store's", func(t *testing.T, ctx context.Context, target StoryTarget) {
			contest := target.NewContest()
			bodies := map[string]string{"en": english}
			saved := save(t, ctx, target, contest, bodies)

			// Neither the map handed to Save nor the ones handed back may be
			// the store's own: writing to them later is not saving.
			bodies["ro"] = romanian
			saved.Bodies["ru"] = "Не сохранено."
			load(t, ctx, target, contest).Bodies["en"] = "Scribbled over."

			wantBodies(t, load(t, ctx, target, contest), map[string]string{"en": english}, "loaded after the caller's writes")
		}},
		{"saving the story of a contest that is not there is reported", func(t *testing.T, ctx context.Context, target StoryTarget) {
			// A contest deleted while its story was being edited: the author
			// is told the contest is gone, not that the store failed.
			if _, err := target.Repo.Save(ctx, uuid.New(), map[string]string{"en": english}); !errors.Is(err, contests.ErrNotFound) {
				t.Errorf("Save() for an unknown contest error = %v, want ErrNotFound", err)
			}
		}},
		{"the participant's read serves one language of the story", func(t *testing.T, ctx context.Context, target StoryTarget) {
			contest := target.NewContest()
			save(t, ctx, target, contest, map[string]string{"en": english, "ro": romanian})

			if body, err := target.Text.BodyIn(ctx, contest, "ro"); err != nil || body != romanian {
				t.Errorf("BodyIn(ro) = %q, %v, want %q", body, err, romanian)
			}
			if body, err := target.Text.BodyIn(ctx, contest, "en"); err != nil || body != english {
				t.Errorf("BodyIn(en) = %q, %v, want %q", body, err, english)
			}
		}},
		{"the participant's read of a missing language or story is not found", func(t *testing.T, ctx context.Context, target StoryTarget) {
			contest, empty := target.NewContest(), target.NewContest()
			save(t, ctx, target, contest, map[string]string{"en": english})

			_, err := target.Text.BodyIn(ctx, contest, "ru")
			notFound(t, err, "BodyIn() of a language the story lacks")
			_, err = target.Text.BodyIn(ctx, empty, "en")
			notFound(t, err, "BodyIn() of a contest with no story")
		}},
		{"the participant's read follows a replacement", func(t *testing.T, ctx context.Context, target StoryTarget) {
			contest := target.NewContest()
			save(t, ctx, target, contest, map[string]string{"en": english, "ro": romanian})
			save(t, ctx, target, contest, map[string]string{"en": "The body was in the archive."})

			if body, err := target.Text.BodyIn(ctx, contest, "en"); err != nil || body != "The body was in the archive." {
				t.Errorf("BodyIn(en) = %q, %v, want the replacement", body, err)
			}
			_, err := target.Text.BodyIn(ctx, contest, "ro")
			notFound(t, err, "BodyIn() of a language the replacement dropped")
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			each(t, func(ctx context.Context, target StoryTarget) {
				c.body(t, ctx, target)
			})
		})
	}
}
