package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/covers"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
)

// Covers is the read and write side of contest_covers.
//
// The row only: the pictures themselves are files on a volume, and this
// repository never opens one. What it does own is the one question the file
// store cannot answer — whether the contest a file belongs to is one a
// visitor with no session may see.
type Covers struct{ pool *pgxpool.Pool }

var _ covers.Repository = (*Covers)(nil)

// NewCovers returns a repository over pool.
func NewCovers(pool *pgxpool.Pool) *Covers { return &Covers{pool: pool} }

func (r *Covers) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// coverColumns is the projection every read below shares, so a column added
// to the table is added to one list rather than to three statements that then
// disagree.
const coverColumns = `contest_id, hash, attribution, width, height, uploaded_at, COALESCE(uploaded_by, '00000000-0000-0000-0000-000000000000'::uuid)`

// Save replaces whatever cover the contest had.
//
// One statement and one row: re-uploading replaces the row, so nothing has to
// choose between two covers and no upload is left behind in the table. The
// files the old row named are left for the sweep.
func (r *Covers) Save(ctx context.Context, cover covers.Cover) error {
	_, err := r.querier(ctx).Exec(ctx, `
		INSERT INTO contest_covers
		    (contest_id, hash, attribution, width, height, uploaded_by, uploaded_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (contest_id) DO UPDATE
		SET hash        = EXCLUDED.hash,
		    attribution = EXCLUDED.attribution,
		    width       = EXCLUDED.width,
		    height      = EXCLUDED.height,
		    uploaded_by = EXCLUDED.uploaded_by,
		    uploaded_at = EXCLUDED.uploaded_at`,
		cover.ContestID, cover.Hash, cover.Attribution, cover.Width, cover.Height,
		nilUUID(cover.UploadedBy), cover.UploadedAt)
	if err != nil {
		return fmt.Errorf("save the cover of contest %s: %w", cover.ContestID, err)
	}
	return nil
}

// ByContest returns the cover of any contest, a draft's included: the
// organiser editing it has to see what they uploaded.
func (r *Covers) ByContest(ctx context.Context, contestID uuid.UUID) (covers.Cover, error) {
	return r.scanOne(ctx, `SELECT `+coverColumns+` FROM contest_covers WHERE contest_id = $1`, contestID)
}

// PublicByContest returns the cover only when the contest is one a visitor
// may see.
//
// The join is the check, not a second round trip: asking for the row and then
// asking whether the contest is published leaves a window in which the answer
// changes between the two, and puts the rule in a caller that can forget it.
// A draft's cover is as private as its questions — the file belongs to the
// olympiad, so it answers to the olympiad's own visibility rather than to
// whether somebody guessed a hash.
func (r *Covers) PublicByContest(ctx context.Context, contestID uuid.UUID) (covers.Cover, error) {
	return r.scanOne(ctx, `
		SELECT `+coverColumns+`
		FROM contest_covers cc
		JOIN contests c ON c.id = cc.contest_id
		WHERE cc.contest_id = $1 AND `+publicStatusFilter("c.status", 2), contestID, contests.PublicStatuses)
}

// Delete removes the row. A contest with no cover is not an error: removing
// one that is already gone is what a second click does.
func (r *Covers) Delete(ctx context.Context, contestID uuid.UUID) error {
	if _, err := r.querier(ctx).Exec(ctx, `DELETE FROM contest_covers WHERE contest_id = $1`, contestID); err != nil {
		return fmt.Errorf("remove the cover of contest %s: %w", contestID, err)
	}
	return nil
}

// scanOne reads the one row both reads above return, and turns its absence
// into the domain's own sentinel rather than into a driver error the HTTP
// layer would have to know about.
func (r *Covers) scanOne(ctx context.Context, sql string, contestID uuid.UUID, args ...any) (covers.Cover, error) {
	var cover covers.Cover
	err := r.querier(ctx).QueryRow(ctx, sql, append([]any{contestID}, args...)...).
		Scan(&cover.ContestID, &cover.Hash, &cover.Attribution,
			&cover.Width, &cover.Height, &cover.UploadedAt, &cover.UploadedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return covers.Cover{}, covers.ErrNotFound
	}
	if err != nil {
		return covers.Cover{}, fmt.Errorf("read the cover of contest %s: %w", contestID, err)
	}
	return cover, nil
}

// Attribution answers the publish gate's one question about a contest's
// picture: is there an uploaded cover, and whose is it?
//
// Two values rather than a Cover, because the gate needs neither the hash nor
// the size and a contest with no uploaded cover is not a failure — it wears a
// drawn one, whose author is us. Deliberately outside covers.Repository: the
// interface the domain's own service declares is what that service uses, and
// a method only the contests package calls belongs to the narrow interface
// that package declares for itself (CLAUDE.md, Go layout rule 3).
func (r *Covers) Attribution(ctx context.Context, contestID uuid.UUID) (string, bool, error) {
	var attribution string
	err := r.querier(ctx).QueryRow(ctx,
		`SELECT attribution FROM contest_covers WHERE contest_id = $1`, contestID).Scan(&attribution)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read the attribution of contest %s: %w", contestID, err)
	}
	return attribution, true, nil
}
