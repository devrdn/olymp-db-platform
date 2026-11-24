package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
	"github.com/devrdn/olymp-db-platform/backend/internal/provisioning"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Bookkeeping for an uploaded dump (game_uploads), and how it becomes a
// contest's game once complete.

// GameUploads is the rows of uploaded dumps, provisioning.DumpRows.
type GameUploads struct{ pool *pgxpool.Pool }

var _ provisioning.DumpRows = (*GameUploads)(nil)

// NewGameUploads returns the dumps' rows over pool.
func NewGameUploads(pool *pgxpool.Pool) *GameUploads { return &GameUploads{pool: pool} }

func (r *GameUploads) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

const uploadColumns = `id, contest_id, filename, declared_bytes, received_bytes,
	coalesce(sha256, ''), coalesce(line_count, 0), status, created_at, updated_at`

func scanUpload(row pgx.Row) (provisioning.Upload, error) {
	var u provisioning.Upload
	var status string
	err := row.Scan(&u.ID, &u.ContestID, &u.Filename, &u.DeclaredBytes, &u.ReceivedBytes,
		&u.SHA256, &u.Lines, &status, &u.CreatedAt, &u.UpdatedAt)
	u.Status = provisioning.UploadStatus(status)
	return u, err
}

// Begin records a new upload of the file filename in 'receiving'. The caller
// generates id so it can name a reservation on disk before this row exists.
//
// One upload in progress per contest is enforced by
// game_uploads_one_receiving_idx, not by a SELECT before the INSERT, which
// would race; its violation comes back as ErrUploadInProgress.
func (r *GameUploads) Begin(
	ctx context.Context, id, contestID uuid.UUID, filename string, declaredBytes int64,
) (provisioning.Upload, error) {
	upload, err := scanUpload(r.querier(ctx).QueryRow(ctx, `
		INSERT INTO game_uploads (id, contest_id, filename, declared_bytes)
		VALUES ($1, $2, $3, $4)
		RETURNING `+uploadColumns, id, contestID, filename, declaredBytes))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return provisioning.Upload{}, provisioning.ErrUploadInProgress
		}
		return provisioning.Upload{}, fmt.Errorf("record the upload: %w", err)
	}
	return upload, nil
}

// ByID reads one upload by id, or ErrUploadNotFound.
func (r *GameUploads) ByID(ctx context.Context, id uuid.UUID) (provisioning.Upload, error) {
	upload, err := scanUpload(r.querier(ctx).QueryRow(ctx,
		`SELECT `+uploadColumns+` FROM game_uploads WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.Upload{}, provisioning.ErrUploadNotFound
	}
	if err != nil {
		return provisioning.Upload{}, fmt.Errorf("read the upload: %w", err)
	}
	return upload, nil
}

// Current reads a contest's one 'receiving' upload, or ErrUploadNotFound.
// game_uploads_one_receiving_idx guarantees at most one.
func (r *GameUploads) Current(ctx context.Context, contestID uuid.UUID) (provisioning.Upload, error) {
	upload, err := scanUpload(r.querier(ctx).QueryRow(ctx,
		`SELECT `+uploadColumns+` FROM game_uploads WHERE contest_id = $1 AND status = 'receiving'`, contestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.Upload{}, provisioning.ErrUploadNotFound
	}
	if err != nil {
		return provisioning.Upload{}, fmt.Errorf("read the contest's current upload: %w", err)
	}
	return upload, nil
}

// UpdateReceived records how far a 'receiving' upload has got, so a resumed
// browser can be told where to continue from. It never lowers the length, and
// an upload no longer 'receiving' is ErrUploadAlreadyComplete and left alone:
// a chunk's write can arrive after a later chunk's or after completion.
func (r *GameUploads) UpdateReceived(ctx context.Context, id uuid.UUID, receivedBytes int64) error {
	tag, err := r.querier(ctx).Exec(ctx, `
		UPDATE game_uploads
		SET received_bytes = GREATEST(received_bytes, $2), updated_at = now()
		WHERE id = $1 AND status = 'receiving'`, id, receivedBytes)
	if err != nil {
		return fmt.Errorf("record the upload's progress: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return provisioning.ErrUploadAlreadyComplete
	}
	return nil
}

// CompleteUpload marks the dump id 'complete' with what Store.Complete
// measured, retires previous (marked 'aborted', its file removed by the
// caller after commit), and replaces the contest's game through upsertGame.
// It is GameInstances's, not GameUploads's, because it replaces the game.
//
// Neither UPDATE checks RowsAffected: the caller already validated the row,
// and a race between that read and this write is rare enough to leave to the
// next organiser action or the janitor.
func (r *GameInstances) CompleteUpload(
	ctx context.Context, contestID, id uuid.UUID, database string, summary provisioning.UploadSummary, previous *uuid.UUID,
) (provisioning.Template, error) {
	if _, err := r.querier(ctx).Exec(ctx, `
		UPDATE game_uploads
		SET status = 'complete', received_bytes = $2, sha256 = $3, line_count = $4, updated_at = now()
		WHERE id = $1 AND status = 'receiving'`,
		id, summary.Bytes, summary.SHA256, summary.Lines); err != nil {
		return provisioning.Template{}, fmt.Errorf("mark the upload complete: %w", err)
	}

	if previous != nil {
		if _, err := r.querier(ctx).Exec(ctx,
			`UPDATE game_uploads SET status = 'aborted', updated_at = now() WHERE id = $1`, *previous); err != nil {
			return provisioning.Template{}, fmt.Errorf("retire the displaced upload's row: %w", err)
		}
	}

	return r.upsertGame(ctx, contestID, database, "", string(provisioning.SourceFile), &id, nil)
}

// Abort marks one upload 'aborted'. It never touches game_templates.
func (r *GameUploads) Abort(ctx context.Context, id uuid.UUID) error {
	if _, err := r.querier(ctx).Exec(ctx,
		`UPDATE game_uploads SET status = 'aborted', updated_at = now() WHERE id = $1`, id); err != nil {
		return fmt.Errorf("mark the upload aborted: %w", err)
	}
	return nil
}

// Abandoned lists up to limit uploads still 'receiving' whose updated_at is
// older than cutoff, oldest first, for the janitor. Served by
// game_uploads_receiving_updated_idx (CLAUDE.md rule 7).
func (r *GameUploads) Abandoned(ctx context.Context, cutoff time.Time, limit int) ([]provisioning.Upload, error) {
	rows, err := r.querier(ctx).Query(ctx,
		`SELECT `+uploadColumns+` FROM game_uploads
		 WHERE status = 'receiving' AND updated_at < $1
		 ORDER BY updated_at
		 LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("list abandoned uploads: %w", err)
	}
	defer rows.Close()

	var out []provisioning.Upload
	for rows.Next() {
		u, err := scanUpload(rows)
		if err != nil {
			return nil, fmt.Errorf("scan an abandoned upload: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list abandoned uploads: %w", err)
	}
	return out, nil
}

// InUse reports whether anything still needs id's bytes on the volume:
// an upload still taking chunks, or one a contest's game is built from. A
// displaced upload is not in use, so the janitor's orphan sweep removes it.
//
// game_templates is searched by upload_id without an index: the sweep starts
// from a file and has no contest to scope by, and the table holds one row per
// contest, so the scan stays small.
func (r *GameUploads) InUse(ctx context.Context, id uuid.UUID) (bool, error) {
	var inUse bool
	if err := r.querier(ctx).QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM game_uploads u
			WHERE u.id = $1
			  AND (u.status = 'receiving'
			       OR EXISTS(SELECT 1 FROM game_templates t WHERE t.upload_id = u.id))
		)`, id).Scan(&inUse); err != nil {
		return false, fmt.Errorf("check whether upload %s is still in use: %w", id, err)
	}
	return inUse, nil
}
