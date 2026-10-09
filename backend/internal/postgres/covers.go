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

// Covers is the read and write side of contest_covers. The pictures are files
// on a volume that this repository never opens; it answers whether a cover's
// contest is visible to an anonymous visitor.
type Covers struct{ pool *pgxpool.Pool }

var _ covers.Repository = (*Covers)(nil)

// NewCovers returns a repository over pool.
func NewCovers(pool *pgxpool.Pool) *Covers { return &Covers{pool: pool} }

func (r *Covers) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

const coverColumns = `contest_id, hash, attribution, width, height, uploaded_at, COALESCE(uploaded_by, '00000000-0000-0000-0000-000000000000'::uuid)`

// Save replaces whatever cover the contest had; a contest has at most one
// row. Files the old row named are left for the sweep.
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
// may see. The visibility check is a join in the same statement, so it cannot
// race a status change or be forgotten by a caller.
func (r *Covers) PublicByContest(ctx context.Context, contestID uuid.UUID) (covers.Cover, error) {
	return r.scanOne(ctx, `
		SELECT `+coverColumns+`
		FROM contest_covers cc
		JOIN contests c ON c.id = cc.contest_id
		WHERE cc.contest_id = $1 AND `+publicStatusFilter("c.status", 2), contestID, contests.PublicStatuses)
}

// Delete removes the row. A contest with no cover is not an error.
func (r *Covers) Delete(ctx context.Context, contestID uuid.UUID) error {
	if _, err := r.querier(ctx).Exec(ctx, `DELETE FROM contest_covers WHERE contest_id = $1`, contestID); err != nil {
		return fmt.Errorf("remove the cover of contest %s: %w", contestID, err)
	}
	return nil
}

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

// ReferencedHashes is every cover hash any contest still uses, once each.
// Files are named by content hash and shared between contests, so the orphan
// sweep may remove only a hash no row names. It scans the whole table, run by
// an operator, never on a request path.
//
// Not part of covers.Repository: the sweep declares its own narrow interface
// (CLAUDE.md, Go layout rule 3).
func (r *Covers) ReferencedHashes(ctx context.Context) ([]string, error) {
	rows, err := r.querier(ctx).Query(ctx, `SELECT DISTINCT hash FROM contest_covers`)
	if err != nil {
		return nil, fmt.Errorf("read the referenced cover hashes: %w", err)
	}
	defer rows.Close()

	var hashes []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, fmt.Errorf("read the referenced cover hashes: %w", err)
		}
		hashes = append(hashes, hash)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the referenced cover hashes: %w", err)
	}
	return hashes, nil
}

// Attribution reports a contest's uploaded cover attribution and whether one
// exists, for the publish gate. No cover is not an error: the contest uses a
// drawn one. Not part of covers.Repository: the contests package declares its
// own narrow interface (CLAUDE.md, Go layout rule 3).
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
