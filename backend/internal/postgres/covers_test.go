package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/covers"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
)

// aCover is a row with everything set, so a test only says what it changes.
func aCover(contest, actor uuid.UUID) covers.Cover {
	return covers.Cover{
		ContestID:   contest,
		Hash:        strings.Repeat("ab", 32),
		Attribution: "Photo: A. Organiser, CC BY 4.0",
		Width:       1600, Height: 900,
		UploadedAt: time.Now().UTC().Truncate(time.Microsecond),
		UploadedBy: actor,
	}
}

func TestACoverIsReadBackAsItWasStored(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewCovers(testPool)
		author := makeUser(t, ctx, "author-cover")
		contest := makeContest(t, ctx, author.ID)
		want := aCover(contest, author.ID)

		if err := repo.Save(ctx, want); err != nil {
			t.Fatalf("Save() = %v", err)
		}
		got, err := repo.ByContest(ctx, contest)
		if err != nil {
			t.Fatalf("ByContest() = %v", err)
		}

		if got.Hash != want.Hash || got.Attribution != want.Attribution {
			t.Errorf("read back %+v, want %+v", got, want)
		}
		if got.Width != 1600 || got.Height != 900 {
			t.Errorf("size = %dx%d, want 1600x900", got.Width, got.Height)
		}
		if got.UploadedBy != author.ID {
			t.Errorf("uploaded_by = %s, want %s", got.UploadedBy, author.ID)
		}
	})
}

func TestAContestWithNoCoverSaysSoInTheDomainsOwnWords(t *testing.T) {
	// Not pgx.ErrNoRows: the HTTP layer maps sentinels, and a driver error
	// reaching it is a 500 where a 404 was meant (CLAUDE.md, security rule 1).
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-cover-missing")
		contest := makeContest(t, ctx, author.ID)

		_, err := NewCovers(testPool).ByContest(ctx, contest)

		if !errors.Is(err, covers.ErrNotFound) {
			t.Errorf("ByContest() = %v, want covers.ErrNotFound", err)
		}
	})
}

func TestUploadingAgainReplacesTheRowRatherThanAddingOne(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewCovers(testPool)
		author := makeUser(t, ctx, "author-cover-replace")
		contest := makeContest(t, ctx, author.ID)

		first := aCover(contest, author.ID)
		if err := repo.Save(ctx, first); err != nil {
			t.Fatalf("Save() = %v", err)
		}
		second := first
		second.Hash = strings.Repeat("cd", 32)
		second.Attribution = "Photo: somebody else"
		if err := repo.Save(ctx, second); err != nil {
			t.Fatalf("Save() again = %v", err)
		}

		var rows int
		if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
			`SELECT count(*) FROM contest_covers WHERE contest_id = $1`, contest).Scan(&rows); err != nil {
			t.Fatalf("count the covers: %v", err)
		}
		if rows != 1 {
			t.Errorf("the contest has %d covers, want exactly one", rows)
		}
		got, err := repo.ByContest(ctx, contest)
		if err != nil {
			t.Fatalf("ByContest() = %v", err)
		}
		if got.Hash != second.Hash {
			t.Errorf("hash = %q, want the later upload's %q", got.Hash, second.Hash)
		}
	})
}

func TestADraftsCoverIsNotServedToAVisitor(t *testing.T) {
	// The file belongs to the olympiad, and the olympiad answers to the same
	// four statuses the public list selects. Guessing an address must not be
	// a way into a contest nobody has published.
	withTx(t, func(ctx context.Context) {
		repo := NewCovers(testPool)
		author := makeUser(t, ctx, "author-cover-draft")
		contest := makeContest(t, ctx, author.ID) // makeContest leaves it a draft
		if err := repo.Save(ctx, aCover(contest, author.ID)); err != nil {
			t.Fatalf("Save() = %v", err)
		}

		if _, err := repo.PublicByContest(ctx, contest); !errors.Is(err, covers.ErrNotFound) {
			t.Fatalf("PublicByContest() of a draft = %v, want covers.ErrNotFound", err)
		}

		exec(t, ctx, `UPDATE contests SET status = 'published' WHERE id = $1`, contest)
		if _, err := repo.PublicByContest(ctx, contest); err != nil {
			t.Errorf("PublicByContest() of a published contest = %v, want its cover", err)
		}
	})
}

func TestDeletingAContestTakesItsCoverWithIt(t *testing.T) {
	// The cascade, so that removing an olympiad never leaves a row pointing
	// at a contest that is gone. The files are the sweep's business.
	withTx(t, func(ctx context.Context) {
		repo := NewCovers(testPool)
		author := makeUser(t, ctx, "author-cover-cascade")
		contest := makeContest(t, ctx, author.ID)
		if err := repo.Save(ctx, aCover(contest, author.ID)); err != nil {
			t.Fatalf("Save() = %v", err)
		}

		exec(t, ctx, `DELETE FROM contests WHERE id = $1`, contest)

		if _, err := repo.ByContest(ctx, contest); !errors.Is(err, covers.ErrNotFound) {
			t.Errorf("ByContest() = %v, want the row gone with the contest", err)
		}
	})
}

func TestTheRecordOutlivesTheAccountThatUploadedIt(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewCovers(testPool)
		author := makeUser(t, ctx, "author-cover-owner")
		uploader := makeUser(t, ctx, "uploader-cover-owner")
		contest := makeContest(t, ctx, author.ID)
		if err := repo.Save(ctx, aCover(contest, uploader.ID)); err != nil {
			t.Fatalf("Save() = %v", err)
		}

		exec(t, ctx, `DELETE FROM users WHERE id = $1`, uploader.ID)

		got, err := repo.ByContest(ctx, contest)
		if err != nil {
			t.Fatalf("ByContest() = %v", err)
		}
		if got.UploadedBy != uuid.Nil {
			t.Errorf("uploaded_by = %s, want it cleared with the account", got.UploadedBy)
		}
		if got.Hash == "" {
			t.Error("the cover went with the account that uploaded it")
		}
	})
}

func TestRemovingACoverLeavesTheContestAlone(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewCovers(testPool)
		author := makeUser(t, ctx, "author-cover-remove")
		contest := makeContest(t, ctx, author.ID)
		if err := repo.Save(ctx, aCover(contest, author.ID)); err != nil {
			t.Fatalf("Save() = %v", err)
		}

		if err := repo.Delete(ctx, contest); err != nil {
			t.Fatalf("Delete() = %v", err)
		}
		// A second removal is what a second click is, not an error.
		if err := repo.Delete(ctx, contest); err != nil {
			t.Errorf("Delete() again = %v, want it to do nothing", err)
		}

		if _, err := repo.ByContest(ctx, contest); !errors.Is(err, covers.ErrNotFound) {
			t.Errorf("ByContest() = %v, want covers.ErrNotFound", err)
		}
		var contests int
		if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
			`SELECT count(*) FROM contests WHERE id = $1`, contest).Scan(&contests); err != nil {
			t.Fatalf("count the contests: %v", err)
		}
		if contests != 1 {
			t.Error("removing a cover removed the contest")
		}
	})
}

func TestTheColumnRefusesACreditLineTheDomainWouldHaveRefused(t *testing.T) {
	// The domain bounds the field (covers.MaxAttributionLen) and the column
	// says the same thing, so a writer that skips the service cannot park a
	// megabyte of prose in a row the front page reads.
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-cover-bound")
		contest := makeContest(t, ctx, author.ID)
		cover := aCover(contest, author.ID)
		cover.Attribution = strings.Repeat("x", covers.MaxAttributionLen+1)

		if err := NewCovers(testPool).Save(ctx, cover); err == nil {
			t.Error("Save() accepted a credit line past the bound")
		}
	})
}
