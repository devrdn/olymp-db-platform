package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/devrdn/olymp-db-platform/backend/internal/archive"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
)

var _ archive.Repository = (*Archive)(nil)

// archiveWhere is the archive's selection, shared by the page, its count and
// ByID. The first two conditions are written exactly as contests_archive_idx's
// predicate so the planner can match it; $1 is the escaped title search.
//
// The title search needs no index of its own: it filters the rows the archive
// index already delivers in order, and reaches each one's titles through the
// translations' primary key.
const archiveWhere = `
		WHERE c.visibility = 'open' AND c.status IN ('finished', 'archived')
		  AND ($1 = '' OR EXISTS (
		      SELECT 1 FROM contest_translations t
		      WHERE t.contest_id = c.id AND t.title ILIKE '%' || $1 || '%'))`

// archiveColumns are the columns before the descriptions, archiveColumnsTail
// those after them. A page shows no description, so the list selects an empty
// object in their place and only ByID aggregates every translation's text.
const archiveColumns = `
		SELECT c.id, c.status, c.visibility, c.starts_at, c.ends_at, c.archive_tasks,
		       (SELECT count(*) FROM registrations r
		        WHERE r.contest_id = c.id AND r.status <> 'disqualified'),
		       COALESCE((SELECT json_object_agg(ct.lang, ct.title)
		                 FROM contest_translations ct WHERE ct.contest_id = c.id), '{}'::json),`

const archiveDescriptions = `
		       COALESCE((SELECT json_object_agg(ct.lang, COALESCE(ct.description, ''))
		                 FROM contest_translations ct WHERE ct.contest_id = c.id), '{}'::json),`

const archiveColumnsTail = `
		       COALESCE((SELECT json_agg(cl.lang ORDER BY cl.lang)
		                 FROM contest_languages cl WHERE cl.contest_id = c.id), '[]'::json),
		       COALESCE((SELECT cl.lang FROM contest_languages cl
		                 WHERE cl.contest_id = c.id AND cl.is_default
		                 ORDER BY cl.lang LIMIT 1), '')`

// archiveListSQL takes the escaped search, the limit and the offset. The id
// breaks ties so the pages do not overlap.
const archiveListSQL = archiveColumns + ` '{}'::json,` + archiveColumnsTail + `, COUNT(*) OVER() AS total
		FROM contests c` + archiveWhere + `
		ORDER BY c.ends_at DESC NULLS LAST, c.id DESC
		LIMIT $2 OFFSET $3`

// Archive reads the contest archive: open contests that have ended.
type Archive struct {
	pool *pgxpool.Pool
}

// NewArchive returns the archive's read side over pool.
func NewArchive(pool *pgxpool.Pool) *Archive { return &Archive{pool: pool} }

func (r *Archive) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// List returns a page of the archive and the total matching the filter. The
// total rides on the page's rows; only a page past the end, which has no rows,
// counts separately, so it still reports the total.
func (r *Archive) List(ctx context.Context, f archive.Filter) (archive.Page, error) {
	query := escapeLike(f.Query)
	rows, err := r.querier(ctx).Query(ctx, archiveListSQL, query, f.Limit, f.Offset)
	if err != nil {
		return archive.Page{}, fmt.Errorf("list the archive: %w", err)
	}
	defer rows.Close()

	var page archive.Page
	for rows.Next() {
		c, err := scanArchived(rows, &page.Total)
		if err != nil {
			return archive.Page{}, err
		}
		page.Items = append(page.Items, c)
	}
	if err := rows.Err(); err != nil {
		return archive.Page{}, fmt.Errorf("list the archive: %w", err)
	}
	if len(page.Items) == 0 && f.Offset > 0 {
		if err := r.querier(ctx).QueryRow(ctx,
			`SELECT COUNT(*) FROM contests c`+archiveWhere, query).Scan(&page.Total); err != nil {
			return archive.Page{}, fmt.Errorf("count the archive: %w", err)
		}
	}
	return page, nil
}

// ByID returns one archived contest, or archive.ErrNotFound when it is closed,
// unfinished or absent.
func (r *Archive) ByID(ctx context.Context, id uuid.UUID) (archive.Contest, error) {
	rows, err := r.querier(ctx).Query(ctx,
		archiveColumns+archiveDescriptions+archiveColumnsTail+`
		FROM contests c`+archiveWhere+` AND c.id = $2`, "", id)
	if err != nil {
		return archive.Contest{}, fmt.Errorf("read archived contest %s: %w", id, err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return archive.Contest{}, fmt.Errorf("read archived contest %s: %w", id, err)
		}
		return archive.Contest{}, archive.ErrNotFound
	}
	c, err := scanArchived(rows, nil)
	if err != nil {
		return archive.Contest{}, err
	}
	return c, nil
}

// scanArchived reads one row; total is the trailing window count the list adds
// and ByID does not.
func scanArchived(rows pgx.Rows, total *int) (archive.Contest, error) {
	var (
		c                           archive.Contest
		titles, descriptions, langs []byte
	)
	dest := []any{&c.ID, &c.Status, &c.Visibility, &c.StartsAt, &c.EndsAt, &c.ArchiveTasks, &c.Participants,
		&titles, &descriptions, &langs, &c.DefaultLanguage}
	if total != nil {
		dest = append(dest, total)
	}
	if err := rows.Scan(dest...); err != nil {
		return archive.Contest{}, fmt.Errorf("scan an archived contest: %w", err)
	}
	for _, d := range []struct {
		raw []byte
		to  any
	}{{titles, &c.Titles}, {descriptions, &c.Descriptions}, {langs, &c.Languages}} {
		if err := json.Unmarshal(d.raw, d.to); err != nil {
			return archive.Contest{}, fmt.Errorf("decode archived contest %s: %w", c.ID, err)
		}
	}
	return c, nil
}
