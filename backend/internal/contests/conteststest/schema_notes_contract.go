package conteststest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

// SchemaNotesTarget is a repository holding no table descriptions yet, and the
// means to create the contest they belong to.
type SchemaNotesTarget struct {
	Repo contests.SchemaNotesRepository
	// Text is the participant's read over the same rows as Repo.
	Text       contests.SchemaNotesText
	NewContest func() uuid.UUID
	// Now is the store's clock, which stamps UpdatedAt.
	Now func() time.Time
}

// SchemaNotesRepositoryContract is what every contests.SchemaNotesRepository
// and contests.SchemaNotesText must do; both the in-memory SchemaNotes and
// postgres.SchemaNotes run it. each prepares a fresh target for one case, calls
// run with it, and cleans up. Texts use "en", "ro" and "ru", which the real
// schema seeds.
func SchemaNotesRepositoryContract(t *testing.T, each func(t *testing.T, run func(context.Context, SchemaNotesTarget))) {
	save := func(t *testing.T, ctx context.Context, target SchemaNotesTarget, contest uuid.UUID, bodies map[string]string) contests.SchemaNotes {
		t.Helper()
		saved, err := target.Repo.Save(ctx, contest, bodies)
		if err != nil {
			t.Fatalf("Save() = %v", err)
		}
		return saved
	}
	load := func(t *testing.T, ctx context.Context, target SchemaNotesTarget, contest uuid.UUID) contests.SchemaNotes {
		t.Helper()
		notes, err := target.Repo.ByContest(ctx, contest)
		if err != nil {
			t.Fatalf("ByContest() = %v", err)
		}
		return notes
	}
	wantBodies := func(t *testing.T, got contests.SchemaNotes, want map[string]string, what string) {
		t.Helper()
		if got.Bodies == nil {
			t.Errorf("%s: bodies are nil, want an empty map at least", what)
		}
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
	first := func(t *testing.T, ctx context.Context, target SchemaNotesTarget, contest uuid.UUID, langs ...string) string {
		t.Helper()
		body, err := target.Text.FirstIn(ctx, contest, langs)
		if err != nil {
			t.Fatalf("FirstIn(%v) = %v", langs, err)
		}
		return body
	}

	const (
		english  = "`suspects` holds one row per person of interest."
		romanian = "`suspects` are câte un rând pentru fiecare suspect."
	)

	cases := []struct {
		name string
		body func(t *testing.T, ctx context.Context, target SchemaNotesTarget)
	}{
		{"a contest with no descriptions has none, which is not an error", func(t *testing.T, ctx context.Context, target SchemaNotesTarget) {
			notes := load(t, ctx, target, target.NewContest())

			wantBodies(t, notes, map[string]string{}, "loaded")
			if !notes.UpdatedAt.IsZero() {
				t.Errorf("UpdatedAt = %v, want zero when nothing was written", notes.UpdatedAt)
			}
		}},
		{"a save stores every language and returns what it stored", func(t *testing.T, ctx context.Context, target SchemaNotesTarget) {
			contest := target.NewContest()

			saved := save(t, ctx, target, contest, map[string]string{"en": english, "ro": romanian})

			wantBodies(t, saved, map[string]string{"en": english, "ro": romanian}, "saved")
			if want := target.Now(); !saved.UpdatedAt.Equal(want) {
				t.Errorf("saved UpdatedAt = %v, want the store's clock %v", saved.UpdatedAt, want)
			}
			loaded := load(t, ctx, target, contest)
			wantBodies(t, loaded, map[string]string{"en": english, "ro": romanian}, "loaded")
			if want := target.Now(); !loaded.UpdatedAt.Equal(want) {
				t.Errorf("loaded UpdatedAt = %v, want the store's clock %v", loaded.UpdatedAt, want)
			}
		}},
		{"saving again replaces the whole set of languages", func(t *testing.T, ctx context.Context, target SchemaNotesTarget) {
			contest := target.NewContest()
			save(t, ctx, target, contest, map[string]string{"en": english, "ro": romanian})

			second := save(t, ctx, target, contest, map[string]string{"en": "Rewritten.", "ru": "Переписано."})

			want := map[string]string{"en": "Rewritten.", "ru": "Переписано."}
			wantBodies(t, second, want, "returned by the second save")
			wantBodies(t, load(t, ctx, target, contest), want, "loaded after the second save")
		}},
		{"saving no languages removes every description", func(t *testing.T, ctx context.Context, target SchemaNotesTarget) {
			contest := target.NewContest()
			save(t, ctx, target, contest, map[string]string{"en": english})

			emptied := save(t, ctx, target, contest, map[string]string{})

			wantBodies(t, emptied, map[string]string{}, "returned by the empty save")
			wantBodies(t, load(t, ctx, target, contest), map[string]string{}, "loaded after the empty save")
			if body := first(t, ctx, target, contest, "en"); body != "" {
				t.Errorf("FirstIn(en) after emptying = %q, want nothing", body)
			}
		}},
		{"descriptions are a contest's own", func(t *testing.T, ctx context.Context, target SchemaNotesTarget) {
			one, other := target.NewContest(), target.NewContest()
			save(t, ctx, target, one, map[string]string{"en": english})
			save(t, ctx, target, other, map[string]string{"ro": romanian})

			wantBodies(t, load(t, ctx, target, one), map[string]string{"en": english}, "first contest")
			wantBodies(t, load(t, ctx, target, other), map[string]string{"ro": romanian}, "second contest")
			if body := first(t, ctx, target, other, "en"); body != "" {
				t.Errorf("FirstIn(en) of the other contest = %q, want nothing", body)
			}
		}},
		{"the text a caller holds is not the store's", func(t *testing.T, ctx context.Context, target SchemaNotesTarget) {
			contest := target.NewContest()
			bodies := map[string]string{"en": english}
			saved := save(t, ctx, target, contest, bodies)

			bodies["ro"] = romanian
			saved.Bodies["ru"] = "Не сохранено."
			load(t, ctx, target, contest).Bodies["en"] = "Scribbled over."

			wantBodies(t, load(t, ctx, target, contest), map[string]string{"en": english}, "loaded after the caller's writes")
		}},
		{"saving for a contest that is not there is reported", func(t *testing.T, ctx context.Context, target SchemaNotesTarget) {
			if _, err := target.Repo.Save(ctx, uuid.New(), map[string]string{"en": english}); !errors.Is(err, contests.ErrNotFound) {
				t.Errorf("Save() for an unknown contest error = %v, want ErrNotFound", err)
			}
		}},
		{"emptying the descriptions of a contest that is not there is reported", func(t *testing.T, ctx context.Context, target SchemaNotesTarget) {
			// No insert runs, so no foreign key can say the contest is gone.
			if _, err := target.Repo.Save(ctx, uuid.New(), map[string]string{}); !errors.Is(err, contests.ErrNotFound) {
				t.Errorf("Save() of no languages for an unknown contest error = %v, want ErrNotFound", err)
			}
		}},
		{"the participant's read takes the first language that has a text", func(t *testing.T, ctx context.Context, target SchemaNotesTarget) {
			contest := target.NewContest()
			save(t, ctx, target, contest, map[string]string{"en": english, "ro": romanian})

			if body := first(t, ctx, target, contest, "ro", "en"); body != romanian {
				t.Errorf("FirstIn(ro, en) = %q, want the Romanian text", body)
			}
			if body := first(t, ctx, target, contest, "en", "ro"); body != english {
				t.Errorf("FirstIn(en, ro) = %q, want the English text", body)
			}
			if body := first(t, ctx, target, contest, "ru", "en"); body != english {
				t.Errorf("FirstIn(ru, en) = %q, want the English text the Russian one falls back to", body)
			}
		}},
		{"the participant's read of languages with no text is empty", func(t *testing.T, ctx context.Context, target SchemaNotesTarget) {
			contest, empty := target.NewContest(), target.NewContest()
			save(t, ctx, target, contest, map[string]string{"en": english})

			if body := first(t, ctx, target, contest, "ru"); body != "" {
				t.Errorf("FirstIn(ru) = %q, want nothing", body)
			}
			if body := first(t, ctx, target, empty, "en", "ro"); body != "" {
				t.Errorf("FirstIn(en, ro) of a contest with none = %q, want nothing", body)
			}
			if body := first(t, ctx, target, contest); body != "" {
				t.Errorf("FirstIn() of no languages = %q, want nothing", body)
			}
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			each(t, func(ctx context.Context, target SchemaNotesTarget) {
				c.body(t, ctx, target)
			})
		})
	}
}
