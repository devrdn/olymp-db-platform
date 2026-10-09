package provisioning

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/gamefile"
	"github.com/google/uuid"
)

// The table builder's data: one CSV file per table of a builder-sourced game,
// stored like an uploaded dump (gamefile id, bookkeeping row) but in its own
// directory and table. Completing a table's CSV never replaces the game; the
// file is loaded into a built database by Games.loadTableData.
//
// Row numbers count data rows only (the header is not row 1), are 1-based and
// never reused: DeleteTableRow tombstones a row number rather than removing
// its line, so the numbering an organiser sees never shifts.

// TableDataStatus is where one table's CSV file has got to.
type TableDataStatus string

const (
	// TableDataReceiving is a chunked upload still taking bytes.
	TableDataReceiving TableDataStatus = "receiving"
	// TableDataComplete is a fully validated file, the only status a build
	// loads rows from.
	TableDataComplete TableDataStatus = "complete"
	// TableDataAborted is a chunked upload cancelled before it completed.
	TableDataAborted TableDataStatus = "aborted"
)

// TableData is the bookkeeping row for one table's CSV file; the bytes live in
// internal/gamefile under ID.
type TableData struct {
	ID            uuid.UUID
	ContestID     uuid.UUID
	Table         string
	DeclaredBytes int64
	ReceivedBytes int64
	// Lines is the number of data rows, header excluded. Zero until Status is
	// TableDataComplete.
	Lines int64
	// DeletedRows are tombstoned row numbers, bounded at MaxTableDeletedRows.
	DeletedRows []int64
	Status      TableDataStatus
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ActiveRows is how many of the file's rows have not been deleted.
func (d TableData) ActiveRows() int64 { return d.Lines - int64(len(d.DeletedRows)) }

func (d TableData) deletedSet() map[int64]struct{} {
	set := make(map[int64]struct{}, len(d.DeletedRows))
	for _, row := range d.DeletedRows {
		set[row] = struct{}{}
	}
	return set
}

// Why a table's data could not be received, completed, read back or edited
// (CLAUDE.md rule 1).
var (
	// ErrTableDataDisabled means Games was never given WithTableData.
	ErrTableDataDisabled = errors.New("table data uploads are not configured on this installation")
	// ErrTableUnknown is a table name not in the contest's current definition,
	// compared as spelled.
	ErrTableUnknown = errors.New("the table is not part of the contest's current definition")
	// ErrTableDataInProgress is a second upload for a table that already has
	// one 'receiving', or two callers bootstrapping the same table at once.
	ErrTableDataInProgress = errors.New("this table already has an upload in progress")
	// ErrTableDataNotFound is an id, or a (contest, table) pair, with no
	// table-data row in the requested state.
	ErrTableDataNotFound = errors.New("no such table data upload")
	// ErrTableDataAlreadyComplete is an Append, Complete or Abort against an
	// upload no longer 'receiving'.
	ErrTableDataAlreadyComplete = errors.New("the table's data upload has already been completed or cancelled")
	// ErrTableDataChunkOutOfOrder mirrors gamefile.ErrChunkOutOfOrder.
	ErrTableDataChunkOutOfOrder = errors.New("the chunk does not continue where the upload left off")
	// ErrTableDataChunkTooLarge mirrors gamefile.ErrChunkTooLarge.
	ErrTableDataChunkTooLarge = errors.New("the chunk exceeds the maximum chunk size")
	// ErrTableDataChunkIncomplete mirrors gamefile.ErrChunkIncomplete.
	ErrTableDataChunkIncomplete = errors.New("the chunk body was not received in full")
	// ErrTableDataTooLarge mirrors gamefile.ErrFileTooLarge.
	ErrTableDataTooLarge = errors.New("the upload exceeds the maximum file size")
	// ErrTableDataStoreFull mirrors gamefile.ErrStoreFull.
	ErrTableDataStoreFull = errors.New("the table data directory is full")
	// ErrTableDataLengthMismatch mirrors gamefile.ErrLengthMismatch.
	ErrTableDataLengthMismatch = errors.New("the received bytes do not match the declared length")
	// ErrTableDataChanged is an AppendTableRow whose row did not land because
	// another form's row took the same offset (see AppendTableRow), or whose
	// table file was replaced under the call.
	ErrTableDataChanged = errors.New("the table's data changed while this row was being added")
	// ErrTableRowNotFound is a row number the file does not have.
	ErrTableRowNotFound = errors.New("no such row")
	// ErrTableRowAlreadyDeleted is a DeleteTableRow for a row already
	// tombstoned, kept distinct so a caller need not audit a second deletion.
	ErrTableRowAlreadyDeleted = errors.New("the row has already been deleted")
	// ErrTooManyDeletedRows is a delete past MaxTableDeletedRows, the
	// database CHECK surfaced as a sentinel.
	ErrTooManyDeletedRows = errors.New("too many rows have been deleted from this table")
)

// wrapTableFileErr maps gamefile's sentinels to this file's. It is separate
// from wrapGamefileErr so a table-data handler never has to recognise an
// upload's sentinels.
func wrapTableFileErr(err error) error {
	switch {
	case errors.Is(err, gamefile.ErrNotFound), errors.Is(err, gamefile.ErrBadUploadID):
		return ErrTableDataNotFound
	case errors.Is(err, gamefile.ErrUploadSealed):
		return ErrTableDataAlreadyComplete
	case errors.Is(err, gamefile.ErrFileTooLarge):
		return ErrTableDataTooLarge
	case errors.Is(err, gamefile.ErrStoreFull):
		return ErrTableDataStoreFull
	case errors.Is(err, gamefile.ErrChunkOutOfOrder):
		return ErrTableDataChunkOutOfOrder
	case errors.Is(err, gamefile.ErrChunkTooLarge):
		return ErrTableDataChunkTooLarge
	case errors.Is(err, gamefile.ErrChunkIncomplete):
		return fmt.Errorf("%w: %w", ErrTableDataChunkIncomplete, err)
	case errors.Is(err, gamefile.ErrLengthMismatch):
		return ErrTableDataLengthMismatch
	default:
		return fmt.Errorf("gamefile: %w", err)
	}
}

// WithTableData turns on per-table CSV storage. store must be a separate
// gamefile.Store from the one given to WithUploads, with its own directory, so
// neither orphan sweep mistakes the other's files for orphans.
func (g *Games) WithTableData(store *gamefile.Store, limits gamefile.Limits) *Games {
	g.tableFiles, g.tableLimits = store, limits
	return g
}

// TableDataLimits reports the table-data store's ceilings, which are
// independent of UploadLimits. internal/api reads them here rather than from a
// constant of its own (CLAUDE.md rule 11).
func (g *Games) TableDataLimits() (gamefile.Limits, bool) {
	return g.tableLimits, g.tableFiles != nil
}

// currentDefinitionTable returns the current definition's table named table,
// or ErrTableUnknown when the game is not builder-sourced or has no such
// table. Every table-data method that is not a pure id lookup calls it first.
func (g *Games) currentDefinitionTable(ctx context.Context, contestID uuid.UUID, table string) (TableDefinition, error) {
	tmpl, err := g.repo.Template(ctx, contestID)
	if err != nil {
		if errors.Is(err, ErrNoGame) {
			return TableDefinition{}, ErrTableUnknown
		}
		return TableDefinition{}, fmt.Errorf("read the contest's game: %w", err)
	}
	if tmpl.Source != SourceBuilder {
		return TableDefinition{}, ErrTableUnknown
	}
	for _, t := range tmpl.Definition.Tables {
		if t.Name == table {
			return t, nil
		}
	}
	return TableDefinition{}, ErrTableUnknown
}

// CurrentTableData returns the table's upload still 'receiving', so a reloaded
// page can resume it. An unknown table is ErrTableUnknown, not "nothing in
// progress".
func (g *Games) CurrentTableData(ctx context.Context, contestID uuid.UUID, table string) (TableData, error) {
	if g.tableFiles == nil {
		return TableData{}, ErrTableDataDisabled
	}
	if _, err := g.currentDefinitionTable(ctx, contestID, table); err != nil {
		return TableData{}, err
	}
	return g.repo.CurrentTableData(ctx, contestID, table)
}

// checkTableDataCompatibility refuses a definition that would change the
// structure of a table that already holds data (see ErrDefinitionTableLocked).
// SetDefinition calls it before anything is written.
func (g *Games) checkTableDataCompatibility(ctx context.Context, contestID uuid.UUID, next Definition) error {
	current, err := g.repo.Template(ctx, contestID)
	switch {
	case errors.Is(err, ErrNoGame):
		return nil // no game yet: no table of it can hold any data
	case err != nil:
		return fmt.Errorf("read the contest's current game: %w", err)
	}
	if current.Source != SourceBuilder {
		// A non-builder game should hold no table data (replaceGame discards
		// it). This loop is the backstop: leftover rows would otherwise let a
		// script save bypass the lock and load values validated against
		// another column type.
		for _, table := range next.Tables {
			data, err := g.repo.ReadyTableData(ctx, contestID, table.Name)
			switch {
			case errors.Is(err, ErrTableDataNotFound):
				continue
			case err != nil:
				return fmt.Errorf("read %s's own data: %w", table.Name, err)
			}
			if data.Lines > 0 {
				return fmt.Errorf("%w: table %q has %d row(s) of data left over from a game that is no longer the table builder's",
					ErrDefinitionTableLocked, table.Name, data.Lines)
			}
		}
		return nil
	}

	replacement := make(map[string]TableDefinition, len(next.Tables))
	for _, t := range next.Tables {
		replacement[t.Name] = t
	}

	for _, table := range current.Definition.Tables {
		data, err := g.repo.ReadyTableData(ctx, contestID, table.Name)
		switch {
		case errors.Is(err, ErrTableDataNotFound):
			continue
		case err != nil:
			return fmt.Errorf("read %s's own data: %w", table.Name, err)
		}
		if data.Lines == 0 {
			continue // a header alone does not lock the table
		}
		replacementTable, stillThere := replacement[table.Name]
		if !stillThere || !table.sameStructure(replacementTable) {
			return fmt.Errorf("%w: table %q has %d row(s) of data", ErrDefinitionTableLocked, table.Name, data.Lines)
		}
	}
	return nil
}

// BeginTableUpload reserves a new chunked CSV upload for one table of the
// contest's current definition. The file is reserved before the row is
// inserted, so a refused insert leaves at worst a rowless file for the orphan
// sweep.
func (g *Games) BeginTableUpload(ctx context.Context, contestID uuid.UUID, table string, declaredBytes int64) (TableData, error) {
	if g.tableFiles == nil {
		return TableData{}, ErrTableDataDisabled
	}
	if declaredBytes <= 0 || declaredBytes > g.tableLimits.MaxFileBytes {
		return TableData{}, ErrTableDataTooLarge
	}
	if _, err := g.currentDefinitionTable(ctx, contestID, table); err != nil {
		return TableData{}, err
	}

	if err := g.requireEditable(ctx, contestID); err != nil {
		return TableData{}, err
	}

	id := uuid.New()
	if err := g.tableFiles.Begin(id.String(), declaredBytes); err != nil {
		return TableData{}, wrapTableFileErr(err)
	}
	data, err := g.repo.BeginTableData(ctx, id, contestID, table, declaredBytes)
	if err != nil {
		// Store.Begin counted declaredBytes against the directory; release
		// it now rather than hold the budget until the janitor sweeps.
		_ = g.retireTableDataFile(id)
		if errors.Is(err, ErrTableDataInProgress) {
			return TableData{}, ErrTableDataInProgress
		}
		return TableData{}, fmt.Errorf("record the upload: %w", err)
	}
	return data, nil
}

// AppendTableChunk writes one chunk of a table-data upload already begun.
//
// After the chunk at offset 0 is written, the header is checked if it arrived
// in full, so a wrong file is refused after one chunk rather than the whole
// upload. CompleteTableUpload still checks it again.
func (g *Games) AppendTableChunk(ctx context.Context, contestID, id uuid.UUID, offset int64, r io.Reader) (int64, error) {
	if g.tableFiles == nil {
		return 0, ErrTableDataDisabled
	}
	data, err := g.tableDataByIDForContest(ctx, contestID, id)
	if err != nil {
		return 0, err
	}
	if data.Status != TableDataReceiving {
		return 0, ErrTableDataAlreadyComplete
	}

	received, err := g.tableFiles.Append(id.String(), offset, r)
	if err != nil {
		return received, wrapTableFileErr(err)
	}
	if received == data.ReceivedBytes {
		return received, nil // a retried chunk Append skipped
	}
	if err := g.repo.UpdateTableDataReceived(ctx, id, received); err != nil {
		return received, fmt.Errorf("record the upload's progress: %w", err)
	}

	// Runs at most once per upload: a resent first chunk took the retry
	// return above.
	if offset == 0 {
		if err := g.checkTableHeaderOnFirstChunk(ctx, contestID, id, data.Table); err != nil {
			return received, err
		}
	}
	return received, nil
}

// checkTableHeaderOnFirstChunk validates the header if its line has fully
// arrived, reading no further than the first newline; otherwise it says
// nothing and leaves the check to CompleteTableUpload.
func (g *Games) checkTableHeaderOnFirstChunk(ctx context.Context, contestID, id uuid.UUID, tableName string) error {
	table, err := g.currentDefinitionTable(ctx, contestID, tableName)
	if err != nil {
		return err
	}
	f, err := g.tableFiles.Open(id.String())
	if err != nil {
		return wrapTableFileErr(err)
	}
	defer func() { _ = f.Close() }()

	line, complete, err := firstLineIfComplete(f)
	if err != nil {
		return err
	}
	if !complete {
		return nil
	}
	fields, err := splitHeaderLine(line)
	if err != nil {
		return err
	}
	return validateHeader(fields, table)
}

func (g *Games) tableDataByIDForContest(ctx context.Context, contestID, id uuid.UUID) (TableData, error) {
	data, err := g.repo.TableDataByID(ctx, id)
	if err != nil {
		return TableData{}, err
	}
	if data.ContestID != contestID {
		// Another contest's upload reads as missing, so its existence does
		// not leak.
		return TableData{}, ErrTableDataNotFound
	}
	return data, nil
}

// CompleteTableUpload finalises a chunked CSV upload: checks the received
// length, then streams the file once, validating the header and then every
// row. This pass is authoritative; AppendTableChunk's header check is only an
// early refusal and may not have run.
func (g *Games) CompleteTableUpload(ctx context.Context, actorID, contestID, id uuid.UUID) (TableData, error) {
	if g.tableFiles == nil {
		return TableData{}, ErrTableDataDisabled
	}
	data, err := g.tableDataByIDForContest(ctx, contestID, id)
	if err != nil {
		return TableData{}, err
	}
	if data.Status != TableDataReceiving {
		return TableData{}, ErrTableDataAlreadyComplete
	}

	table, err := g.currentDefinitionTable(ctx, contestID, data.Table)
	if err != nil {
		return TableData{}, err
	}

	if err := g.requireEditable(ctx, contestID); err != nil {
		return TableData{}, err
	}

	if data.ReceivedBytes != data.DeclaredBytes {
		return TableData{}, ErrTableDataLengthMismatch
	}

	lines, err := g.validateTableFile(ctx, id.String(), table)
	if err != nil {
		return TableData{}, err
	}

	// The displaced file is retired inside the write and removed from disk
	// only after it commits.
	previous, err := g.tableDataToDisplace(ctx, contestID, data.Table, id)
	if err != nil {
		return TableData{}, err
	}

	completed, err := g.completeTableDataAndAudit(ctx, actorID, contestID, data.Table, id, data.ReceivedBytes, lines, previous)
	if err != nil {
		return TableData{}, err
	}

	if previous != nil {
		_ = g.retireTableDataFile(*previous)
	}
	return completed, nil
}

// completeTableDataAndAudit runs the write and its audit entry as one unit.
func (g *Games) completeTableDataAndAudit(
	ctx context.Context, actorID, contestID uuid.UUID, table string, id uuid.UUID, bytes, lines int64, previous *uuid.UUID,
) (TableData, error) {
	var completed TableData
	run := func(ctx context.Context) error {
		var err error
		completed, err = g.repo.CompleteTableData(ctx, contestID, table, id, bytes, lines, previous)
		if err != nil {
			return fmt.Errorf("store the table's data: %w", err)
		}
		if err := g.repo.MarkTableDataChanged(ctx, contestID); err != nil {
			return fmt.Errorf("mark the contest's data changed: %w", err)
		}
		return g.record(ctx, audit.Entry{
			ActorID: &actorID, Action: audit.ActionGameTableDataUpload,
			Entity: "contest", EntityID: contestID.String(),
			Payload: map[string]any{"table": table, "rows": lines, "bytes": bytes},
		})
	}
	err := g.atomically(ctx, run)
	return completed, err
}

// tableDataToDisplace returns the table's current 'complete' file, or nil when
// there is none or it is keeping.
func (g *Games) tableDataToDisplace(ctx context.Context, contestID uuid.UUID, table string, keeping uuid.UUID) (*uuid.UUID, error) {
	ready, err := g.repo.ReadyTableData(ctx, contestID, table)
	switch {
	case errors.Is(err, ErrTableDataNotFound):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("read the table's current data: %w", err)
	}
	if ready.ID == keeping {
		return nil, nil
	}
	return &ready.ID, nil
}

// validateTableFile streams the file, validating the header and then every
// row, and returns the number of data rows. It runs inside the request with
// no timeout middleware, so it stops when ctx is cancelled.
func (g *Games) validateTableFile(ctx context.Context, id string, table TableDefinition) (int64, error) {
	f, err := g.tableFiles.Open(id)
	if err != nil {
		return 0, wrapTableFileErr(err)
	}
	defer func() { _ = f.Close() }()

	scanner := newTableLineScanner(f)
	header, err := scanner.next()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return 0, fmt.Errorf("%w: the file is empty", ErrTableHeaderMismatch)
		}
		return 0, err
	}
	headerFields, err := splitHeaderLine(header)
	if err != nil {
		return 0, err
	}
	if err := validateHeader(headerFields, table); err != nil {
		return 0, err
	}

	var lines int64
	for {
		// Checked every thousand rows: a few milliseconds of wasted reading
		// at most.
		if lines%1000 == 0 {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
		}
		line, err := scanner.next()
		if errors.Is(err, io.EOF) {
			return lines, nil
		}
		if err != nil {
			return 0, err
		}
		lines++
		if lines > MaxTableDataRows {
			return 0, fmt.Errorf("%w: more than %d rows", ErrTableTooManyRows, MaxTableDataRows)
		}
		fields, err := splitCSVLine(line)
		if err != nil {
			return 0, fmt.Errorf("row %d: %w", lines, err)
		}
		if err := validateRow(fields, table, lines); err != nil {
			return 0, err
		}
	}
}

// AbortTableUpload cancels a chunked upload before it completed.
func (g *Games) AbortTableUpload(ctx context.Context, actorID, contestID, id uuid.UUID) (TableData, error) {
	if g.tableFiles == nil {
		return TableData{}, ErrTableDataDisabled
	}
	data, err := g.tableDataByIDForContest(ctx, contestID, id)
	if err != nil {
		return TableData{}, err
	}
	if data.Status != TableDataReceiving {
		return TableData{}, ErrTableDataAlreadyComplete
	}
	return g.abortTableData(ctx, &actorID, data)
}

// abortTableData aborts an upload; a nil actor records a system event (the
// janitor).
func (g *Games) abortTableData(ctx context.Context, actor *uuid.UUID, data TableData) (TableData, error) {
	mark := func(ctx context.Context) error {
		if err := g.repo.AbortTableData(ctx, data.ID); err != nil {
			return fmt.Errorf("mark the table upload aborted: %w", err)
		}
		return g.record(ctx, audit.Entry{
			ActorID: actor, Action: audit.ActionGameTableDataUploadAbort,
			Entity: "contest", EntityID: data.ContestID.String(),
			Payload: map[string]any{"table": data.Table, "bytes": data.ReceivedBytes},
		})
	}

	if err := g.retireTableDataFile(data.ID); err != nil {
		return TableData{}, fmt.Errorf("remove the table upload's file: %w", err)
	}
	err := g.atomically(ctx, mark)
	if err != nil {
		return TableData{}, err
	}
	data.Status = TableDataAborted
	return data, nil
}

func (g *Games) retireTableDataFile(id uuid.UUID) error {
	if err := g.tableFiles.Abort(id.String()); err != nil && !errors.Is(err, gamefile.ErrNotFound) {
		return wrapTableFileErr(err)
	}
	return nil
}

// AppendTableRow adds one row typed into a form to the table's file. values
// are the field text in column order. The first row creates the file; later
// rows are appended and only the new row is validated.
//
// Refused while a chunked upload is 'receiving' for the table: both would
// append at the same offset, and gamefile.Store.Append treats a second write
// at a covered offset as a retry, silently dropping one of them.
//
// Two forms racing are decided, not refused. The bytes are read back
// (rowLanded), so the loser gets ErrTableDataChanged rather than a 201 for a
// row that is not there. Length and count are stored as floors
// (AppendTableDataRow), so callers committing out of order cannot leave the
// bookkeeping describing the shorter file.
//
// The bytes are written before the row that counts them: an interruption then
// leaves one uncounted valid row, which tableFileState reconciles. The other
// order would count a row whose bytes never arrived.
func (g *Games) AppendTableRow(ctx context.Context, actorID, contestID uuid.UUID, tableName string, values []string) (TableData, error) {
	if g.tableFiles == nil {
		return TableData{}, ErrTableDataDisabled
	}
	table, err := g.currentDefinitionTable(ctx, contestID, tableName)
	if err != nil {
		return TableData{}, err
	}
	if len(values) != len(table.Columns) {
		return TableData{}, fmt.Errorf("%w: got %d value(s), the table has %d columns",
			ErrTableRowFieldCount, len(values), len(table.Columns))
	}
	fields := make([]csvField, len(values))
	for i, v := range values {
		fields[i] = csvField{Text: v, Null: v == ""}
	}
	if err := validateRow(fields, table, 0); err != nil {
		return TableData{}, err
	}

	if err := g.requireEditable(ctx, contestID); err != nil {
		return TableData{}, err
	}

	rowLine, err := formatCSVRow(values)
	if err != nil {
		return TableData{}, err
	}

	if _, err := g.repo.CurrentTableData(ctx, contestID, tableName); err == nil {
		return TableData{}, ErrTableDataInProgress
	} else if !errors.Is(err, ErrTableDataNotFound) {
		return TableData{}, fmt.Errorf("check for an upload in progress: %w", err)
	}

	ready, err := g.repo.ReadyTableData(ctx, contestID, tableName)
	switch {
	case errors.Is(err, ErrTableDataNotFound):
		return g.bootstrapTableRow(ctx, actorID, contestID, tableName, table, rowLine)
	case err != nil:
		return TableData{}, fmt.Errorf("read the table's current data: %w", err)
	}

	offset, lines, err := g.tableFileState(ready)
	if err != nil {
		return TableData{}, err
	}
	if lines >= MaxTableDataRows {
		// The same ceiling validateTableFile enforces for uploads (CLAUDE.md
		// rule 2).
		return TableData{}, fmt.Errorf("%w: the table already holds %d rows, the limit is %d",
			ErrTableTooManyRows, lines, MaxTableDataRows)
	}

	payload, err := g.rowPayload(ready.ID, offset, rowLine)
	if err != nil {
		return TableData{}, err
	}

	written, err := g.tableFiles.Append(ready.ID.String(), offset, strings.NewReader(payload))
	if err != nil {
		return TableData{}, wrapTableFileErr(err)
	}
	landed, err := g.rowLanded(ready.ID, offset, payload, written)
	if err != nil {
		return TableData{}, err
	}
	if !landed {
		// Another form's row took this offset; nothing of this row was written.
		return TableData{}, ErrTableDataChanged
	}
	newLines := lines + 1

	var updated TableData
	run := func(ctx context.Context) error {
		var err error
		updated, err = g.repo.AppendTableDataRow(ctx, ready.ID, written, newLines)
		if err != nil {
			return fmt.Errorf("record the appended row: %w", err)
		}
		if err := g.repo.MarkTableDataChanged(ctx, contestID); err != nil {
			return fmt.Errorf("mark the contest's data changed: %w", err)
		}
		return g.record(ctx, audit.Entry{
			ActorID: &actorID, Action: audit.ActionGameTableDataRowAdd,
			Entity: "contest", EntityID: contestID.String(),
			Payload: map[string]any{"table": tableName, "row": newLines},
		})
	}
	err = g.atomically(ctx, run)
	if err != nil {
		return TableData{}, err
	}
	return updated, nil
}

// rowLanded reports whether the bytes now at offset are this call's row.
// Append reports a write at a covered offset as a successful retry, and two
// rows often have the same length, so the bytes are read back and compared.
// Two byte-identical rows both report success and the file holds one; that is
// harmless.
func (g *Games) rowLanded(id uuid.UUID, offset int64, payload string, written int64) (bool, error) {
	if written != offset+int64(len(payload)) {
		return false, nil
	}
	f, err := g.tableFiles.Open(id.String())
	if err != nil {
		return false, wrapTableFileErr(err)
	}
	defer func() { _ = f.Close() }()

	got := make([]byte, len(payload))
	if _, err := f.ReadAt(got, offset); err != nil {
		return false, fmt.Errorf("read back the appended row: %w", err)
	}
	return string(got) == payload, nil
}

// tableFileState reports the offset for the next row and the data row count,
// as the file has them rather than the bookkeeping. Normally they agree and
// this is one stat. When the file is longer (a row landed but its count rolled
// back), the rows are recounted; appending at the stale offset would be
// swallowed as a retry and the table would accept no more rows.
func (g *Games) tableFileState(ready TableData) (offset, lines int64, err error) {
	offset, err = g.tableFiles.Received(ready.ID.String())
	if err != nil {
		return 0, 0, wrapTableFileErr(err)
	}
	if offset == ready.ReceivedBytes {
		return offset, ready.Lines, nil
	}
	lines, err = g.countTableDataRows(ready.ID.String())
	if err != nil {
		return 0, 0, err
	}
	return offset, lines, nil
}

// countTableDataRows counts every line but the header, tombstoned rows
// included so DeletedRows keeps naming the same rows. The rows were validated
// when written and are not validated again.
func (g *Games) countTableDataRows(id string) (int64, error) {
	f, err := g.tableFiles.Open(id)
	if err != nil {
		return 0, wrapTableFileErr(err)
	}
	defer func() { _ = f.Close() }()

	scanner := newTableLineScanner(f)
	var lines int64
	for i := 0; ; i++ {
		if _, err := scanner.next(); err != nil {
			if errors.Is(err, io.EOF) {
				return lines, nil
			}
			return 0, fmt.Errorf("count the table's rows: %w", err)
		}
		if i > 0 { // i == 0 is the header
			lines++
		}
	}
}

// rowPayload is the bytes one row is appended as: the row and its newline,
// preceded by a newline when the file does not end in one. Many exporters omit
// the final newline, and without this the new row would be glued onto the
// last one.
func (g *Games) rowPayload(id uuid.UUID, offset int64, rowLine string) (string, error) {
	if offset == 0 {
		return rowLine + "\n", nil
	}
	f, err := g.tableFiles.Open(id.String())
	if err != nil {
		return "", wrapTableFileErr(err)
	}
	defer func() { _ = f.Close() }()

	var last [1]byte
	if _, err := f.ReadAt(last[:], offset-1); err != nil {
		return "", fmt.Errorf("read the end of the table's data: %w", err)
	}
	if last[0] == '\n' {
		return rowLine + "\n", nil
	}
	return "\n" + rowLine + "\n", nil
}

// bootstrapTableRow creates a table's first data file: the header, then the
// one row the caller already validated.
func (g *Games) bootstrapTableRow(
	ctx context.Context, actorID, contestID uuid.UUID, tableName string, table TableDefinition, rowLine string,
) (TableData, error) {
	header, err := formatCSVRow(headerFields(table))
	if err != nil {
		return TableData{}, err
	}
	content := header + "\n" + rowLine + "\n"

	id := uuid.New()
	if err := g.tableFiles.Begin(id.String(), int64(len(content))); err != nil {
		return TableData{}, wrapTableFileErr(err)
	}
	if _, err := g.tableFiles.Append(id.String(), 0, strings.NewReader(content)); err != nil {
		_ = g.retireTableDataFile(id)
		return TableData{}, wrapTableFileErr(err)
	}

	var created TableData
	run := func(ctx context.Context) error {
		var err error
		created, err = g.repo.CreateReadyTableData(ctx, id, contestID, tableName, int64(len(content)), 1)
		if err != nil {
			return fmt.Errorf("store the table's first row: %w", err)
		}
		if err := g.repo.MarkTableDataChanged(ctx, contestID); err != nil {
			return fmt.Errorf("mark the contest's data changed: %w", err)
		}
		return g.record(ctx, audit.Entry{
			ActorID: &actorID, Action: audit.ActionGameTableDataRowAdd,
			Entity: "contest", EntityID: contestID.String(),
			Payload: map[string]any{"table": tableName, "row": int64(1)},
		})
	}
	err = g.atomically(ctx, run)
	if err != nil {
		// Most often another caller bootstrapped the same table first. The
		// file this call wrote is nobody's; remove it now.
		_ = g.retireTableDataFile(id)
		if errors.Is(err, ErrTableDataInProgress) {
			return TableData{}, ErrTableDataInProgress
		}
		return TableData{}, err
	}
	return created, nil
}

// TableRow is one data row of a window read: its stable row number and its
// field text in column order. A NULL field reads back as an empty string.
type TableRow struct {
	Row    int64
	Fields []string
}

// TableRowWindow is one page of a table's data rows, read for display.
type TableRowWindow struct {
	FromRow int64
	Rows    []TableRow
	// TotalRows is Lines, not ActiveRows, so row numbers stay stable and
	// deleted rows are simply absent from the page.
	TotalRows int64
	// Truncated says the byte budget stopped the window before maxRows.
	Truncated bool
}

// TableDataWindow reads up to maxRows non-tombstoned rows of a table's current
// data from fromRow, within maxBytes of row text except that the first row is
// always returned whole.
//
// gamefile's persisted line index cannot serve this file because it is sealed
// and a table's file keeps growing. The walk instead starts from the nearest
// tableRowIndex mark; without marks, paging through a whole file is quadratic
// in its size.
func (g *Games) TableDataWindow(ctx context.Context, contestID uuid.UUID, table string, fromRow int64, maxRows int, maxBytes int64) (TableRowWindow, error) {
	if g.tableFiles == nil {
		return TableRowWindow{}, ErrTableDataDisabled
	}
	if _, err := g.currentDefinitionTable(ctx, contestID, table); err != nil {
		return TableRowWindow{}, err
	}
	data, err := g.repo.ReadyTableData(ctx, contestID, table)
	if err != nil {
		if errors.Is(err, ErrTableDataNotFound) {
			return TableRowWindow{FromRow: fromRow}, nil // no file yet: an empty table
		}
		return TableRowWindow{}, fmt.Errorf("read the table's current data: %w", err)
	}
	if fromRow < 1 {
		fromRow = 1
	}
	if maxRows <= 0 || fromRow > data.Lines {
		return TableRowWindow{FromRow: fromRow, TotalRows: data.Lines}, nil
	}

	f, err := g.tableFiles.Open(data.ID.String())
	if err != nil {
		return TableRowWindow{}, wrapTableFileErr(err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return TableRowWindow{}, fmt.Errorf("read the table's data: %w", err)
	}
	marks := g.rowMarks.of(data.ID.String())
	scanner, row, err := openTableRowScan(f, info.Size(), marks, fromRow)
	if err != nil {
		return TableRowWindow{}, err
	}

	deleted := data.deletedSet()
	var remaining = maxBytes
	window := TableRowWindow{FromRow: fromRow, TotalRows: data.Lines}
	for {
		// fromRow has no ceiling and a cold read has no marks, so the walk
		// can be long; stop when the caller hangs up.
		if err := ctx.Err(); err != nil {
			return TableRowWindow{}, err
		}
		lineAt := scanner.offset
		line, err := scanner.next()
		if errors.Is(err, io.EOF) {
			return window, nil
		}
		if err != nil {
			return TableRowWindow{}, fmt.Errorf("read the table's data: %w", err)
		}
		// The line just read is data row row+1, which begins mark
		// row/interval when row is a multiple of the interval.
		if row%tableRowMarkInterval == 0 {
			marks.record(row/tableRowMarkInterval, lineAt, scanner.offset)
		}
		row++
		if row < fromRow {
			continue
		}
		if _, isDeleted := deleted[row]; isDeleted {
			continue
		}
		// A row over budget ends the window, except the page's first row,
		// which is always shown whole. Otherwise a row wider than the budget
		// yields an empty page whose "next" offset is fromRow again, and a
		// CSV row cannot be cut. A page may therefore exceed its budget.
		if int64(len(line)) > remaining && len(window.Rows) > 0 {
			window.Truncated = true
			return window, nil
		}
		remaining -= int64(len(line))

		fields, err := splitCSVLine(line)
		if err != nil {
			return TableRowWindow{}, fmt.Errorf("row %d: %w", row, err)
		}
		values := make([]string, len(fields))
		for i, f := range fields {
			values[i] = f.Text
		}
		window.Rows = append(window.Rows, TableRow{Row: row, Fields: values})
		if len(window.Rows) >= maxRows {
			return window, nil
		}
	}
}

// openTableRowScan positions f for a walk towards fromRow and reports how many
// data rows lie before that position: at the nearest usable mark, or else
// just past the header.
func openTableRowScan(f io.ReadSeeker, size int64, marks *tableRowMarks, fromRow int64) (*tableLineScanner, int64, error) {
	if offset, rowsBefore, ok := marks.nearest(fromRow, size); ok {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return nil, 0, fmt.Errorf("read the table's data: %w", err)
		}
		return newTableLineScannerAt(f, offset), rowsBefore, nil
	}

	scanner := newTableLineScanner(f)
	if _, err := scanner.next(); err != nil { // skip the header
		return nil, 0, fmt.Errorf("read the table's data: %w", err)
	}
	return scanner, 0, nil
}

// DeleteTableRow tombstones one row of a table's current data. The file is
// not rewritten: the row number goes into a bounded list
// (MaxTableDeletedRows) with one UPDATE, readers skip it, and an interruption
// leaves nothing partial.
func (g *Games) DeleteTableRow(ctx context.Context, actorID, contestID uuid.UUID, table string, row int64) error {
	if g.tableFiles == nil {
		return ErrTableDataDisabled
	}
	if _, err := g.currentDefinitionTable(ctx, contestID, table); err != nil {
		return err
	}
	data, err := g.repo.ReadyTableData(ctx, contestID, table)
	if err != nil {
		if errors.Is(err, ErrTableDataNotFound) {
			return ErrTableRowNotFound
		}
		return fmt.Errorf("read the table's current data: %w", err)
	}
	if row < 1 || row > data.Lines {
		return ErrTableRowNotFound
	}
	for _, d := range data.DeletedRows {
		if d == row {
			return ErrTableRowAlreadyDeleted
		}
	}

	if err := g.requireEditable(ctx, contestID); err != nil {
		return err
	}

	run := func(ctx context.Context) error {
		if err := g.repo.DeleteTableDataRow(ctx, data.ID, row); err != nil {
			return err
		}
		if err := g.repo.MarkTableDataChanged(ctx, contestID); err != nil {
			return fmt.Errorf("mark the contest's data changed: %w", err)
		}
		return g.record(ctx, audit.Entry{
			ActorID: &actorID, Action: audit.ActionGameTableDataRowDelete,
			Entity: "contest", EntityID: contestID.String(),
			Payload: map[string]any{"table": table, "row": row},
		})
	}
	return g.atomically(ctx, run)
}

// loadTableData loads each table's completed CSV into a freshly built
// database through COPY FROM STDIN (cluster.LoadTableData). A table with no
// completed file is left empty.
func (g *Games) loadTableData(ctx context.Context, contestID uuid.UUID, database string, definition Definition) error {
	if g.tableFiles == nil {
		return nil
	}
	for _, table := range definition.Tables {
		data, err := g.repo.ReadyTableData(ctx, contestID, table.Name)
		if errors.Is(err, ErrTableDataNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read %s's own data: %w", table.Name, err)
		}

		f, err := g.tableFiles.Open(data.ID.String())
		if err != nil {
			return fmt.Errorf("open %s's own data: %w", table.Name, wrapTableFileErr(err))
		}

		err = g.cluster.LoadTableData(ctx, database, table.Name, headerFields(table), newTableDataCopyReader(f, data.deletedSet()))
		closeErr := f.Close()
		if err != nil {
			return fmt.Errorf("load %s's own data: %w", table.Name, err)
		}
		if closeErr != nil {
			return fmt.Errorf("close %s's own data: %w", table.Name, closeErr)
		}
	}
	return nil
}

// tableDataCopyReader streams a table's CSV into COPY FROM STDIN without the
// header and tombstoned rows, one line at a time (CLAUDE.md rule 12).
type tableDataCopyReader struct {
	scanner *tableLineScanner
	deleted map[int64]struct{}
	row     int64
	started bool
	pending []byte
	err     error
}

func newTableDataCopyReader(r io.Reader, deleted map[int64]struct{}) *tableDataCopyReader {
	return &tableDataCopyReader{scanner: newTableLineScanner(r), deleted: deleted}
}

func (c *tableDataCopyReader) Read(p []byte) (int, error) {
	var n int
	for n < len(p) {
		if len(c.pending) > 0 {
			copied := copy(p[n:], c.pending)
			c.pending = c.pending[copied:]
			n += copied
			continue
		}
		if c.err != nil {
			break
		}
		c.advance()
	}
	if n > 0 {
		return n, nil
	}
	return 0, c.err
}

func (c *tableDataCopyReader) advance() {
	if !c.started {
		c.started = true
		if _, err := c.scanner.next(); err != nil { // skip the header
			c.err = err
			return
		}
	}
	for {
		line, err := c.scanner.next()
		if err != nil {
			c.err = err
			return
		}
		c.row++
		if _, deleted := c.deleted[c.row]; deleted {
			continue
		}
		c.pending = append(line, '\n')
		return
	}
}

// sweepAbandonedTableData aborts table-data uploads still 'receiving' past
// olderThan, in batches of abandonedUploadBatchLimit.
func (g *Games) sweepAbandonedTableData(ctx context.Context, olderThan time.Duration) (int, error) {
	abandoned, err := g.repo.AbandonedTableData(ctx, g.now().Add(-olderThan), abandonedUploadBatchLimit)
	if err != nil {
		return 0, fmt.Errorf("list abandoned table uploads: %w", err)
	}
	var count int
	var failures []error
	for _, data := range abandoned {
		if _, err := g.abortTableData(ctx, nil, data); err != nil {
			failures = append(failures, fmt.Errorf("abandon table upload %s: %w", data.ID, err))
			continue
		}
		count++
	}
	return count, errors.Join(failures...)
}

// sweepOrphanTableFiles removes table-data files no row uses. Files younger
// than orphanFileGrace are spared: BeginTableUpload and bootstrapTableRow
// create a file before the row naming it.
func (g *Games) sweepOrphanTableFiles(ctx context.Context) (int, error) {
	ids, err := g.tableFiles.UploadIDs(g.now().Add(-orphanFileGrace))
	if err != nil {
		return 0, fmt.Errorf("list the table data volume: %w", err)
	}

	var removed int
	var failures []error
	for _, idStr := range ids {
		id, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		inUse, err := g.repo.TableDataInUse(ctx, id)
		if err != nil {
			failures = append(failures, fmt.Errorf("check table upload %s: %w", id, err))
			continue
		}
		if inUse {
			continue
		}
		if err := g.retireTableDataFile(id); err != nil {
			failures = append(failures, fmt.Errorf("remove orphan table file %s: %w", id, err))
			continue
		}
		removed++
	}
	return removed, errors.Join(failures...)
}
