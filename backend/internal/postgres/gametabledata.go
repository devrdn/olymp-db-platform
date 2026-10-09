package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Bookkeeping for the table builder's per-table CSV files (game_table_data):
// gameuploads.go's shape at the grain of one table.

const tableDataColumns = `id, contest_id, table_name, declared_bytes, received_bytes,
	coalesce(line_count, 0), coalesce(deleted_rows, '{}'), status, created_at, updated_at`

// tableDataCheckViolation is the SQLSTATE for a CHECK constraint, such as the
// bound on deleted_rows. Not named checkViolation: contests_test.go declares
// that identifier.
const tableDataCheckViolation = "23514"

func scanTableData(row pgx.Row) (provisioning.TableData, error) {
	var d provisioning.TableData
	var status string
	err := row.Scan(&d.ID, &d.ContestID, &d.Table, &d.DeclaredBytes, &d.ReceivedBytes,
		&d.Lines, &d.DeletedRows, &status, &d.CreatedAt, &d.UpdatedAt)
	d.Status = provisioning.TableDataStatus(status)
	return d, err
}

// BeginTableData records a new table-data upload in 'receiving'. The caller
// generates id so it can name a reservation on disk before this row exists.
// One unfinished upload per table is enforced by the partial index
// game_table_data_one_receiving_idx, not by a read before the INSERT.
func (r *GameInstances) BeginTableData(
	ctx context.Context, id, contestID uuid.UUID, table string, declaredBytes int64,
) (provisioning.TableData, error) {
	data, err := scanTableData(r.querier(ctx).QueryRow(ctx, `
		INSERT INTO game_table_data (id, contest_id, table_name, declared_bytes)
		VALUES ($1, $2, $3, $4)
		RETURNING `+tableDataColumns, id, contestID, table, declaredBytes))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return provisioning.TableData{}, provisioning.ErrTableDataInProgress
		}
		return provisioning.TableData{}, fmt.Errorf("record the table upload: %w", err)
	}
	return data, nil
}

// TableDataByID reads one table-data row by id, or ErrTableDataNotFound.
func (r *GameInstances) TableDataByID(ctx context.Context, id uuid.UUID) (provisioning.TableData, error) {
	data, err := scanTableData(r.querier(ctx).QueryRow(ctx,
		`SELECT `+tableDataColumns+` FROM game_table_data WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.TableData{}, provisioning.ErrTableDataNotFound
	}
	if err != nil {
		return provisioning.TableData{}, fmt.Errorf("read the table upload: %w", err)
	}
	return data, nil
}

// CurrentTableData reads a table's one 'receiving' upload, or
// ErrTableDataNotFound. game_table_data_one_receiving_idx guarantees at most
// one.
func (r *GameInstances) CurrentTableData(ctx context.Context, contestID uuid.UUID, table string) (provisioning.TableData, error) {
	data, err := scanTableData(r.querier(ctx).QueryRow(ctx,
		`SELECT `+tableDataColumns+` FROM game_table_data
		 WHERE contest_id = $1 AND lower(table_name) = lower($2) AND status = 'receiving'`, contestID, table))
	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.TableData{}, provisioning.ErrTableDataNotFound
	}
	if err != nil {
		return provisioning.TableData{}, fmt.Errorf("read the table's current upload: %w", err)
	}
	return data, nil
}

// ReadyTableData reads a table's one 'complete' file, or
// ErrTableDataNotFound. game_table_data_one_complete_idx guarantees at most
// one.
func (r *GameInstances) ReadyTableData(ctx context.Context, contestID uuid.UUID, table string) (provisioning.TableData, error) {
	data, err := scanTableData(r.querier(ctx).QueryRow(ctx,
		`SELECT `+tableDataColumns+` FROM game_table_data
		 WHERE contest_id = $1 AND lower(table_name) = lower($2) AND status = 'complete'`, contestID, table))
	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.TableData{}, provisioning.ErrTableDataNotFound
	}
	if err != nil {
		return provisioning.TableData{}, fmt.Errorf("read the table's current data: %w", err)
	}
	return data, nil
}

// UpdateTableDataReceived records how many bytes Store.Append actually wrote.
func (r *GameInstances) UpdateTableDataReceived(ctx context.Context, id uuid.UUID, receivedBytes int64) error {
	if _, err := r.querier(ctx).Exec(ctx,
		`UPDATE game_table_data SET received_bytes = $2, updated_at = now() WHERE id = $1`,
		id, receivedBytes); err != nil {
		return fmt.Errorf("record the table upload's progress: %w", err)
	}
	return nil
}

// CompleteTableData marks id 'complete' with what the validation pass
// measured, and retires previous (the file this one displaces, if any).
func (r *GameInstances) CompleteTableData(
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

	return r.TableDataByID(ctx, id)
}

// CreateReadyTableData records a table's first row as a file that starts
// already 'complete' (see provisioning.Games.AppendTableRow).
func (r *GameInstances) CreateReadyTableData(
	ctx context.Context, id, contestID uuid.UUID, table string, bytes, lines int64,
) (provisioning.TableData, error) {
	data, err := scanTableData(r.querier(ctx).QueryRow(ctx, `
		INSERT INTO game_table_data (id, contest_id, table_name, declared_bytes, received_bytes, line_count, status)
		VALUES ($1, $2, $3, $4, $4, $5, 'complete')
		RETURNING `+tableDataColumns, id, contestID, table, bytes, lines))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return provisioning.TableData{}, provisioning.ErrTableDataInProgress
		}
		return provisioning.TableData{}, fmt.Errorf("record the table's first row: %w", err)
	}
	return data, nil
}

// MarkTableDataChanged records that this contest's built game no longer holds
// the data its tables do. A contest with no game row is not an error: the
// build that writes it later loads all the data.
//
// clock_timestamp(), not now(): now() is transaction start, so a mark whose
// transaction opened before a build claimed the template could carry a time
// no later than the claim, and FinishBuild's `data_changed_at <= claimedAt`
// would clear it. clock_timestamp() is always after ClaimBuild's commit.
func (r *GameInstances) MarkTableDataChanged(ctx context.Context, contestID uuid.UUID) error {
	if _, err := r.querier(ctx).Exec(ctx,
		`UPDATE game_templates SET data_changed_at = clock_timestamp() WHERE contest_id = $1`, contestID); err != nil {
		return fmt.Errorf("mark the contest's table data changed: %w", err)
	}
	return nil
}

// AppendTableDataRow records one more row appended to an already-'complete'
// file, writing its byte length and row count in one statement.
//
// GREATEST keeps both figures moving forward: with two concurrent appends to
// one table, the last UPDATE to run need not be the one that read last, and
// it must not overwrite a newer pair with a smaller one. coalesce covers a
// null line_count.
//
// No row matched means the row is gone or no longer 'complete' (a concurrent
// game replacement discards table data), answered as ErrTableDataChanged.
func (r *GameInstances) AppendTableDataRow(ctx context.Context, id uuid.UUID, receivedBytes, lines int64) (provisioning.TableData, error) {
	data, err := scanTableData(r.querier(ctx).QueryRow(ctx, `
		UPDATE game_table_data
		SET received_bytes = GREATEST(received_bytes, $2),
		    line_count     = GREATEST(coalesce(line_count, 0), $3),
		    updated_at     = now()
		WHERE id = $1 AND status = 'complete'
		RETURNING `+tableDataColumns, id, receivedBytes, lines))
	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.TableData{}, provisioning.ErrTableDataChanged
	}
	if err != nil {
		return provisioning.TableData{}, fmt.Errorf("record the appended row: %w", err)
	}
	return data, nil
}

// AbortTableData marks one table-data upload 'aborted'.
func (r *GameInstances) AbortTableData(ctx context.Context, id uuid.UUID) error {
	if _, err := r.querier(ctx).Exec(ctx,
		`UPDATE game_table_data SET status = 'aborted', updated_at = now() WHERE id = $1`, id); err != nil {
		return fmt.Errorf("mark the table upload aborted: %w", err)
	}
	return nil
}

// DeleteTableDataRow tombstones one row of a 'complete' file in one atomic
// array_append whose WHERE refuses a row already tombstoned, so two
// concurrent deletes cannot both succeed. The CHECK on deleted_rows enforces
// the bound and is answered as ErrTooManyDeletedRows.
func (r *GameInstances) DeleteTableDataRow(ctx context.Context, id uuid.UUID, row int64) error {
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

// DiscardTableData retires every table-data row of one contest that anything
// still needs ('receiving' and 'complete') and returns their ids, so the
// caller can remove their bytes once its transaction has committed. Rows are
// marked 'aborted', not deleted: they stay as history, and TableDataInUse
// reads 'aborted' as unneeded.
func (r *GameInstances) DiscardTableData(ctx context.Context, contestID uuid.UUID) ([]uuid.UUID, error) {
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

// AbandonedTableData lists up to limit table-data uploads still 'receiving'
// whose updated_at is older than cutoff, oldest first.
func (r *GameInstances) AbandonedTableData(ctx context.Context, cutoff time.Time, limit int) ([]provisioning.TableData, error) {
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

// TableDataInUse reports whether anything still needs id's bytes on the
// volume: an upload still receiving, or a table's current 'complete' file.
func (r *GameInstances) TableDataInUse(ctx context.Context, id uuid.UUID) (bool, error) {
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
