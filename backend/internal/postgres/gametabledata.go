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

// Bookkeeping for the table builder's per-table CSV files (game_table_data):
// gameuploads.go's shape at the grain of one table.

// GameTableData is the rows of the table builder's CSV files,
// provisioning.TableDataRows.
type GameTableData struct{ pool *pgxpool.Pool }

var _ provisioning.TableDataRows = (*GameTableData)(nil)

// NewGameTableData returns the table builder's rows over pool.
func NewGameTableData(pool *pgxpool.Pool) *GameTableData { return &GameTableData{pool: pool} }

func (r *GameTableData) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

const tableDataColumns = `id, contest_id, table_name, declared_bytes, received_bytes,
	coalesce(line_count, 0), coalesce(deleted_rows, '{}'), status, created_at, updated_at`

// tableDataCheckViolation is the SQLSTATE for a CHECK constraint, such as the
// bound on deleted_rows. Not named checkViolation: contests_test.go declares
// that identifier.
const tableDataCheckViolation = "23514"

// scanTableData scans tableDataColumns, then whatever columns follow them
// into more.
func scanTableData(row pgx.Row, more ...any) (provisioning.TableData, error) {
	var d provisioning.TableData
	var status string
	dest := append([]any{&d.ID, &d.ContestID, &d.Table, &d.DeclaredBytes, &d.ReceivedBytes,
		&d.Lines, &d.DeletedRows, &status, &d.CreatedAt, &d.UpdatedAt}, more...)
	err := row.Scan(dest...)
	d.Status = provisioning.UploadStatus(status)
	return d, err
}

// Begin records a new upload of table's data in 'receiving'. The caller
// generates id so it can name a reservation on disk before this row exists.
// One unfinished upload per table is enforced by the partial index
// game_table_data_one_receiving_idx, not by a read before the INSERT.
func (r *GameTableData) Begin(
	ctx context.Context, id, contestID uuid.UUID, table string, declaredBytes int64,
) (provisioning.TableData, error) {
	data, err := scanTableData(r.querier(ctx).QueryRow(ctx, `
		INSERT INTO game_table_data (id, contest_id, table_name, declared_bytes)
		VALUES ($1, $2, $3, $4)
		RETURNING `+tableDataColumns, id, contestID, table, declaredBytes))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return provisioning.TableData{}, provisioning.ErrUploadInProgress
		}
		return provisioning.TableData{}, fmt.Errorf("record the table upload: %w", err)
	}
	return data, nil
}

// ByID reads one table-data row by id, or ErrUploadNotFound.
func (r *GameTableData) ByID(ctx context.Context, id uuid.UUID) (provisioning.TableData, error) {
	data, err := scanTableData(r.querier(ctx).QueryRow(ctx,
		`SELECT `+tableDataColumns+` FROM game_table_data WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.TableData{}, provisioning.ErrUploadNotFound
	}
	if err != nil {
		return provisioning.TableData{}, fmt.Errorf("read the table upload: %w", err)
	}
	return data, nil
}

// WithOwner is ByID with the account that created the row's contest, in one
// statement: a chunk needs both and arrives hundreds of times per upload.
func (r *GameTableData) WithOwner(ctx context.Context, id uuid.UUID) (provisioning.TableData, uuid.UUID, error) {
	var owner uuid.UUID
	// The contest is joined under names of its own, so tableDataColumns stays
	// unambiguous, and joined rather than looked up, so a row without its
	// contest is no row.
	data, err := scanTableData(r.querier(ctx).QueryRow(ctx, `
		SELECT `+tableDataColumns+`, c.owner
		FROM game_table_data
		JOIN (SELECT id AS owned_contest, created_by AS owner FROM contests) c
		  ON c.owned_contest = game_table_data.contest_id
		WHERE game_table_data.id = $1`, id), &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.TableData{}, uuid.Nil, provisioning.ErrUploadNotFound
	}
	if err != nil {
		return provisioning.TableData{}, uuid.Nil, fmt.Errorf("read the table upload and its contest's owner: %w", err)
	}
	return data, owner, nil
}

// Current reads a table's one 'receiving' upload, or ErrUploadNotFound.
// game_table_data_one_receiving_idx guarantees at most one.
func (r *GameTableData) Current(ctx context.Context, contestID uuid.UUID, table string) (provisioning.TableData, error) {
	data, err := scanTableData(r.querier(ctx).QueryRow(ctx,
		`SELECT `+tableDataColumns+` FROM game_table_data
		 WHERE contest_id = $1 AND lower(table_name) = lower($2) AND status = 'receiving'`, contestID, table))
	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.TableData{}, provisioning.ErrUploadNotFound
	}
	if err != nil {
		return provisioning.TableData{}, fmt.Errorf("read the table's current upload: %w", err)
	}
	return data, nil
}

// Ready reads a table's one 'complete' file, or ErrUploadNotFound.
// game_table_data_one_complete_idx guarantees at most one.
func (r *GameTableData) Ready(ctx context.Context, contestID uuid.UUID, table string) (provisioning.TableData, error) {
	data, err := scanTableData(r.querier(ctx).QueryRow(ctx,
		`SELECT `+tableDataColumns+` FROM game_table_data
		 WHERE contest_id = $1 AND lower(table_name) = lower($2) AND status = 'complete'`, contestID, table))
	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.TableData{}, provisioning.ErrUploadNotFound
	}
	if err != nil {
		return provisioning.TableData{}, fmt.Errorf("read the table's current data: %w", err)
	}
	return data, nil
}

// UpdateReceived records how far a 'receiving' upload has got. It never
// lowers the length, and it does not touch a file that is no longer
// 'receiving' (ErrUploadAlreadyComplete): a chunk admitted before its upload
// completed can finish after, and a 'complete' file's length is what every
// reader and the next appended row go by.
func (r *GameTableData) UpdateReceived(ctx context.Context, id uuid.UUID, receivedBytes int64) error {
	tag, err := r.querier(ctx).Exec(ctx, `
		UPDATE game_table_data
		SET received_bytes = GREATEST(received_bytes, $2), updated_at = now()
		WHERE id = $1 AND status = 'receiving'`, id, receivedBytes)
	if err != nil {
		return fmt.Errorf("record the table upload's progress: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return provisioning.ErrUploadAlreadyComplete
	}
	return nil
}

// Complete marks id 'complete' with what the validation pass measured, and
// retires previous (the file this one displaces, if any).
func (r *GameTableData) Complete(
	ctx context.Context, contestID uuid.UUID, table string, id uuid.UUID, receivedBytes, lines int64, previous *uuid.UUID,
) (provisioning.TableData, error) {
	// Retire the displaced row first: in the other order both rows would be
	// 'complete' at once and game_table_data_one_complete_idx raises 23505.
	if previous != nil {
		if _, err := r.querier(ctx).Exec(ctx,
			`UPDATE game_table_data SET status = 'aborted', updated_at = now() WHERE id = $1`, *previous); err != nil {
			return provisioning.TableData{}, fmt.Errorf("retire the displaced table upload's row: %w", err)
		}
	}

	if _, err := r.querier(ctx).Exec(ctx, `
		UPDATE game_table_data
		SET status = 'complete', received_bytes = $2, line_count = $3, updated_at = now()
		WHERE id = $1 AND status = 'receiving'`, id, receivedBytes, lines); err != nil {
		return provisioning.TableData{}, fmt.Errorf("mark the table upload complete: %w", err)
	}

	return r.ByID(ctx, id)
}

// CreateReady records a table's first row as a file that starts already
// 'complete' (see provisioning.Games.AppendTableRow).
func (r *GameTableData) CreateReady(
	ctx context.Context, id, contestID uuid.UUID, table string, bytes, lines int64,
) (provisioning.TableData, error) {
	data, err := scanTableData(r.querier(ctx).QueryRow(ctx, `
		INSERT INTO game_table_data (id, contest_id, table_name, declared_bytes, received_bytes, line_count, status)
		VALUES ($1, $2, $3, $4, $4, $5, 'complete')
		RETURNING `+tableDataColumns, id, contestID, table, bytes, lines))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return provisioning.TableData{}, provisioning.ErrUploadInProgress
		}
		return provisioning.TableData{}, fmt.Errorf("record the table's first row: %w", err)
	}
	return data, nil
}

// AppendRow records one more row of rowBytes at the end of a
// 'complete' file, only while the file is still recorded as at bytes long,
// and so holds the row until the transaction ends.
//
// The condition decides two forms that read the same end. Whichever lock
// makes them take turns, the contest's or this row's, the second statement
// runs against the first one's committed length and matches nothing.
//
// No row matched also covers a row that is gone or no longer 'complete' (a
// game replacement discards table data); both are ErrTableDataChanged.
func (r *GameTableData) AppendRow(ctx context.Context, id uuid.UUID, at, rowBytes int64) (provisioning.TableData, error) {
	data, err := scanTableData(r.querier(ctx).QueryRow(ctx, `
		UPDATE game_table_data
		SET received_bytes = received_bytes + $3,
		    line_count     = coalesce(line_count, 0) + 1,
		    updated_at     = now()
		WHERE id = $1 AND status = 'complete' AND received_bytes = $2
		RETURNING `+tableDataColumns, id, at, rowBytes))
	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.TableData{}, provisioning.ErrTableDataChanged
	}
	if err != nil {
		return provisioning.TableData{}, fmt.Errorf("record the appended row: %w", err)
	}
	return data, nil
}

// Abort marks one table-data upload 'aborted'.
func (r *GameTableData) Abort(ctx context.Context, id uuid.UUID) error {
	if _, err := r.querier(ctx).Exec(ctx,
		`UPDATE game_table_data SET status = 'aborted', updated_at = now() WHERE id = $1`, id); err != nil {
		return fmt.Errorf("mark the table upload aborted: %w", err)
	}
	return nil
}

// DeleteRow tombstones one row of a 'complete' file in one atomic
// array_append whose WHERE refuses a row already tombstoned, so two
// concurrent deletes cannot both succeed. The CHECK on deleted_rows enforces
// the bound and is answered as ErrTooManyDeletedRows.
func (r *GameTableData) DeleteRow(ctx context.Context, id uuid.UUID, row int64) error {
	tag, err := r.querier(ctx).Exec(ctx, `
		UPDATE game_table_data
		SET deleted_rows = array_append(deleted_rows, $2::bigint), updated_at = now()
		WHERE id = $1 AND status = 'complete' AND NOT ($2::bigint = ANY(deleted_rows))`, id, row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == tableDataCheckViolation {
			return provisioning.ErrTooManyDeletedRows
		}
		return fmt.Errorf("delete row %d: %w", row, err)
	}
	if tag.RowsAffected() == 0 {
		// The caller checked the row against Lines, so it was already
		// tombstoned, possibly by a concurrent call.
		return provisioning.ErrTableRowAlreadyDeleted
	}
	return nil
}

// Discard retires every table-data row of one contest that anything
// still needs ('receiving' and 'complete') and returns their ids, so the
// caller can remove their bytes once its transaction has committed. Rows are
// marked 'aborted', not deleted: they stay as history, and InUse
// reads 'aborted' as unneeded.
func (r *GameTableData) Discard(ctx context.Context, contestID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		UPDATE game_table_data
		SET status = 'aborted', updated_at = now()
		WHERE contest_id = $1 AND status IN ('receiving', 'complete')
		RETURNING id`, contestID)
	if err != nil {
		return nil, fmt.Errorf("discard the contest's table data: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan a discarded table data row: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("discard the contest's table data: %w", err)
	}
	return ids, nil
}

// Abandoned lists up to limit table-data uploads still 'receiving'
// whose updated_at is older than cutoff, oldest first.
func (r *GameTableData) Abandoned(ctx context.Context, cutoff time.Time, limit int) ([]provisioning.TableData, error) {
	rows, err := r.querier(ctx).Query(ctx,
		`SELECT `+tableDataColumns+` FROM game_table_data
		 WHERE status = 'receiving' AND updated_at < $1
		 ORDER BY updated_at
		 LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("list abandoned table uploads: %w", err)
	}
	defer rows.Close()

	var out []provisioning.TableData
	for rows.Next() {
		d, err := scanTableData(rows)
		if err != nil {
			return nil, fmt.Errorf("scan an abandoned table upload: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list abandoned table uploads: %w", err)
	}
	return out, nil
}

// InUse reports whether anything still needs id's bytes on the volume: an
// upload still receiving, or a table's current 'complete' file.
func (r *GameTableData) InUse(ctx context.Context, id uuid.UUID) (bool, error) {
	var inUse bool
	if err := r.querier(ctx).QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM game_table_data
			WHERE id = $1 AND status IN ('receiving', 'complete')
		)`, id).Scan(&inUse); err != nil {
		return false, fmt.Errorf("check whether table upload %s is still in use: %w", id, err)
	}
	return inUse, nil
}
