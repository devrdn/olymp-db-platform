package contests_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/google/uuid"
)

func TestSchemaNotesAreStoredInEveryLanguageTheyWereWrittenIn(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	saved, err := f.Service.SetSchemaNotes(context.Background(), uuid.New(), c.ID, map[string]string{
		"en": "`suspects`: one row per person of interest.",
		"ro": "`suspects`: câte un rând pentru fiecare suspect.",
	})
	if err != nil {
		t.Fatalf("SetSchemaNotes() = %v", err)
	}
	if saved.Bodies["ro"] != "`suspects`: câte un rând pentru fiecare suspect." {
		t.Errorf("saved = %v, want the Romanian text", saved.Bodies)
	}

	notes, err := f.Service.SchemaNotes(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("SchemaNotes() = %v", err)
	}
	if len(notes.Bodies) != 2 || notes.Bodies["en"] != "`suspects`: one row per person of interest." {
		t.Errorf("SchemaNotes() = %v, want both languages", notes.Bodies)
	}
}

// Optional content: a contest with none answers with none, not a refusal.
func TestAContestWithoutSchemaNotesHasNone(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	notes, err := f.Service.SchemaNotes(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("SchemaNotes() = %v", err)
	}
	if len(notes.Bodies) != 0 {
		t.Errorf("SchemaNotes() = %v, want none", notes.Bodies)
	}
}

func TestSchemaNotesOfAContestThatIsNotThereAreNotFound(t *testing.T) {
	f := conteststest.NewFixture()

	if _, err := f.Service.SchemaNotes(context.Background(), uuid.New()); !errors.Is(err, contests.ErrNotFound) {
		t.Errorf("SchemaNotes() = %v, want ErrNotFound", err)
	}
	if _, err := f.Service.SetSchemaNotes(context.Background(), uuid.New(), uuid.New(), map[string]string{"en": "x"}); !errors.Is(err, contests.ErrNotFound) {
		t.Errorf("SetSchemaNotes() = %v, want ErrNotFound", err)
	}
}

// A blank document is no document: the participant's tab appears only for a
// language with something to read, so whitespace must not count as written.
func TestABlankSchemaNoteIsNotStored(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	saved, err := f.Service.SetSchemaNotes(context.Background(), uuid.New(), c.ID, map[string]string{
		"en": "Every table is described here.",
		"ro": "  \n\t ",
		"ru": "",
	})
	if err != nil {
		t.Fatalf("SetSchemaNotes() = %v", err)
	}
	if len(saved.Bodies) != 1 || saved.Bodies["en"] == "" {
		t.Errorf("saved = %v, want English alone", saved.Bodies)
	}
	if !f.Audit.Recorded(audit.ActionContestSchemaNotesChange) {
		t.Fatalf("audit entries = %v, want a %s", f.Audit.Actions(), audit.ActionContestSchemaNotesChange)
	}
	if langs := f.Audit.Entries[len(f.Audit.Entries)-1].Payload["languages"]; !slices.Equal(langs.([]string), []string{"en"}) {
		t.Errorf("audited languages = %v, want only the one written", langs)
	}
}

func TestSchemaNotesRefuseALanguageTheInstallationDoesNotOffer(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	_, err := f.Service.SetSchemaNotes(context.Background(), uuid.New(), c.ID, map[string]string{
		"rus": "Таблицы.",
	})

	if !errors.Is(err, contests.ErrUnknownLanguage) {
		t.Errorf("SetSchemaNotes() = %v, want ErrUnknownLanguage", err)
	}
}

// Frozen with the rest of the content at the start: participants are reading
// it.
func TestSchemaNotesCannotChangeOnceTheContestHasStarted(t *testing.T) {
	for _, status := range []string{contests.StatusRunning, contests.StatusFinished, contests.StatusArchived} {
		t.Run(status, func(t *testing.T) {
			f := conteststest.NewFixture()
			c := f.SeedContest(status)

			_, err := f.Service.SetSchemaNotes(context.Background(), uuid.New(), c.ID, map[string]string{
				"en": "A different schema entirely.",
			})

			if !errors.Is(err, contests.ErrNotEditable) {
				t.Errorf("SetSchemaNotes() = %v, want ErrNotEditable", err)
			}
			if f.Audit.Recorded(audit.ActionContestSchemaNotesChange) {
				t.Error("a refused change was audited")
			}
		})
	}
}

func TestSchemaNotesStayEditableWhilePublished(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)

	if _, err := f.Service.SetSchemaNotes(context.Background(), uuid.New(), c.ID, map[string]string{"en": "Late fix."}); err != nil {
		t.Errorf("SetSchemaNotes() = %v, want nil before the start", err)
	}
}

func TestChangingTheSchemaNotesIsAuditedWithItsLanguages(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	actor := uuid.New()

	if _, err := f.Service.SetSchemaNotes(context.Background(), actor, c.ID,
		map[string]string{"ro": "Tabelele.", "en": "The tables."}); err != nil {
		t.Fatalf("SetSchemaNotes() = %v", err)
	}

	entry := f.Audit.Entries[len(f.Audit.Entries)-1]
	if entry.Action != audit.ActionContestSchemaNotesChange {
		t.Fatalf("last audit action = %q, want %q", entry.Action, audit.ActionContestSchemaNotesChange)
	}
	if entry.ActorID == nil || *entry.ActorID != actor {
		t.Errorf("audited actor = %v, want %v", entry.ActorID, actor)
	}
	if langs := entry.Payload["languages"]; !slices.Equal(langs.([]string), []string{"en", "ro"}) {
		t.Errorf("audited languages = %v, want [en ro], never the text", langs)
	}
}

// CLAUDE.md rule 2, counted in characters so every language gets the same room.
func TestSchemaNotesAreBoundedPerLanguage(t *testing.T) {
	cases := []struct {
		name  string
		runes int
		ok    bool
	}{
		{"at the bound", 100_000, true},
		{"past the bound", 100_001, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := conteststest.NewFixture()
			c := f.SeedContest(contests.StatusDraft)

			_, err := f.Service.SetSchemaNotes(context.Background(), uuid.New(), c.ID, map[string]string{
				"en": "The tables.",
				"ro": strings.Repeat("ș", tc.runes),
			})
			if tc.ok && err != nil {
				t.Errorf("SetSchemaNotes() = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, contests.ErrInvalidContest) {
				t.Errorf("SetSchemaNotes() = %v, want ErrInvalidContest", err)
			}
		})
	}
}
