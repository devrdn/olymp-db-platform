package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/google/uuid"
)

func TestSchemaNotesHonoursTheRepositoryContract(t *testing.T) {
	conteststest.SchemaNotesRepositoryContract(t, func(t *testing.T, run func(context.Context, conteststest.SchemaNotesTarget)) {
		withTx(t, func(ctx context.Context) {
			author := makeUser(t, ctx, "author-schema-notes")
			now := txNow(t, ctx)
			repo := NewSchemaNotes(testPool)
			run(ctx, conteststest.SchemaNotesTarget{
				Repo:       repo,
				Text:       repo,
				NewContest: func() uuid.UUID { return makeContest(t, ctx, author.ID) },
				Now:        func() time.Time { return now },
			})
		})
	})
}

// The column repeats the domain's bound for a row written past the service.
func TestASchemaNoteLongerThanTheBoundIsRefusedByTheTable(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-schema-notes-bound")
		contest := makeContest(t, ctx, author.ID)
		repo := NewSchemaNotes(testPool)

		if _, err := repo.Save(ctx, contest, map[string]string{"ro": strings.Repeat("ș", 100_000)}); err != nil {
			t.Fatalf("Save() at the bound = %v", err)
		}
		_, err := repo.Save(ctx, contest, map[string]string{"ro": strings.Repeat("ș", 100_001)})
		if err == nil || !strings.Contains(err.Error(), "contest_schema_notes_body_length") {
			t.Fatalf("Save() past the bound = %v, want the length constraint", err)
		}
	})
}
