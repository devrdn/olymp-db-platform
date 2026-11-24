package provisioning

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/gamefile"
	"github.com/google/uuid"
)

// The table builder's data: one CSV file per table of a builder-sourced game.
// A table's CSV is one kind of chunked upload (chunked.go), in its own
// directory and table. Completing a table's CSV never replaces the game; the
// file is loaded into a built database by Games.loadTableData.
//
// Row numbers count data rows only (the header is not row 1), are 1-based and
// never reused: DeleteTableRow tombstones a row number rather than removing
// its line, so the numbering an organiser sees never shifts.

// TableData is the bookkeeping row for one table's CSV file; the bytes live in
// internal/gamefile under ID.
type TableData struct {
	ID            uuid.UUID
	ContestID     uuid.UUID
	Table         string
	DeclaredBytes int64
	ReceivedBytes int64
	// Lines is the number of data rows, header excluded. Zero until Status is
	// UploadComplete.
	Lines int64
	// DeletedRows are tombstoned row numbers, bounded at MaxTableDeletedRows.
	DeletedRows []int64
	Status      UploadStatus
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

// Why a table's data could not be read back or edited (CLAUDE.md rule 1).
// Receiving it is refused with the chunked-upload sentinels (chunked.go).
var (
	// ErrTableUnknown is a table name not in the contest's current definition,
	// compared as spelled.
	ErrTableUnknown = errors.New("the table is not part of the contest's current definition")
	// ErrTableDataChanged is an AppendTableRow that read the table before
	// another form's row was recorded, or whose table file was replaced under
	// the call. Nothing of its row is kept.
	ErrTableDataChanged = errors.New("the table's data changed while this row was being added")
	// ErrTableDataDamaged is a table's file missing, or holding fewer bytes
	// than its record counts: the volume lost bytes that were stored. Nothing can be
	// read from the table or added to it until its data is uploaded again.
	ErrTableDataDamaged = errors.New("the table's stored data is missing or shorter than its record")
	// ErrTableRowNotFound is a row number the file does not have.
	ErrTableRowNotFound = errors.New("no such row")
	// ErrTableRowAlreadyDeleted is a DeleteTableRow for a row already
	// tombstoned, kept distinct so a caller need not audit a second deletion.
	ErrTableRowAlreadyDeleted = errors.New("the row has already been deleted")
	// ErrTooManyDeletedRows is a delete past MaxTableDeletedRows, the
	// database CHECK surfaced as a sentinel.
	ErrTooManyDeletedRows = errors.New("too many rows have been deleted from this table")
)

func (d TableData) progress() uploadProgress {
	return uploadProgress{ID: d.ID, ContestID: d.ContestID, ReceivedBytes: d.ReceivedBytes, Status: d.Status}
}

func (d TableData) aborted() TableData {
	d.Status = UploadAborted
	return d
}

func (d TableData) abortEntry(actor *uuid.UUID) audit.Entry {
	return audit.Entry{
		ActorID: actor, Action: audit.ActionGameTableDataUploadAbort,
		Entity: "contest", EntityID: d.ContestID.String(),
		Payload: map[string]any{"table": d.Table, "bytes": d.ReceivedBytes},
	}
}

// TableDataRows is UploadRows for table CSVs, with what only a table's data
// has: a file per table that stays the table's once complete, grows a typed
// row at a time and tombstones rows rather than rewriting them.
type TableDataRows interface {
	UploadRows[TableData]
	// WithOwner is ByID with the account that created the row's contest, in
	// one read: a chunk is charged to that owner (chunkBound).
	WithOwner(ctx context.Context, id uuid.UUID) (TableData, uuid.UUID, error)
	// Current reads a table's one 'receiving' upload, or ErrUploadNotFound.
	Current(ctx context.Context, contestID uuid.UUID, table string) (TableData, error)
	// Ready reads a table's one 'complete' file, or ErrUploadNotFound when
	// the table has none yet.
	Ready(ctx context.Context, contestID uuid.UUID, table string) (TableData, error)
	// Complete marks id 'complete' with what validation measured, and retires
	// previous (nil unless another upload for the table is displaced).
	Complete(ctx context.Context, contestID uuid.UUID, table string, id uuid.UUID, receivedBytes, lines int64, previous *uuid.UUID) (TableData, error)
	// CreateReady records a table's first row as a file that starts
	// 'complete'. A table that already has one is ErrUploadInProgress.
	CreateReady(ctx context.Context, id, contestID uuid.UUID, table string, bytes, lines int64) (TableData, error)
	// AppendRow records one more row of rowBytes at the end of a 'complete'
	// file, only while the file is still recorded as at bytes long.
	// Otherwise ErrTableDataChanged: another row was recorded first, or the
	// file is no longer the table's.
	AppendRow(ctx context.Context, id uuid.UUID, at, rowBytes int64) (TableData, error)
	// DeleteRow tombstones one row of a 'complete' file in one atomic update,
	// refusing with ErrTooManyDeletedRows past the stored bound.
	DeleteRow(ctx context.Context, id uuid.UUID, row int64) error
	// Discard retires every table-data row of a contest and returns the ids
	// whose files the caller removes after its transaction commits.
	//
	// Used when a game stops being builder-sourced. The rows are keyed by
	// table name, so left behind they would be loaded by a later definition
	// that reuses a name, against columns they were never validated for.
	Discard(ctx context.Context, contestID uuid.UUID) ([]uuid.UUID, error)
}

// WithTableData turns on per-table CSV storage on store (Uploads.use). store
// must be a separate gamefile.Store from the one given to WithUploads, with
// its own directory, so neither orphan sweep mistakes the other's files for
// orphans.
//
// store must buffer its chunks (gamefile.Limits.BufferedChunks) and it is a
// wiring mistake, answered with a panic, if it streams them: AppendTableRow
// writes to a table's file from inside a transaction holding the contest's
// row, and a streamed chunk holds that file's lock for as long as its client
// takes to send it.
func (g *Games) WithTableData(store *gamefile.Store, limits gamefile.Limits) *Games {
	if !store.BuffersChunks() {
		panic("provisioning: the table data store must buffer its chunks (gamefile.Limits.BufferedChunks)")
	}
	g.tables.use(store, limits)
	return g
}

// TableUploads begins, receives and aborts a table's CSV uploads;
// CompleteTableUpload is what makes one the table's data.
func (g *Games) TableUploads() *Uploads[TableData] { return g.tables }

// committedTableFile is the part of a table's file its row records
// (ReceivedBytes), which is all any reader may see. The file itself can run
// on: AppendTableRow writes a row inside the transaction that records it,
// after AppendTableDataRow and so holding the table's row, and a row whose
// transaction did not commit stays as a tail until the next one is written
// over it. A file shorter than its record, or gone while its row is still the
// table's, is ErrTableDataDamaged.
type committedTableFile struct {
	*io.SectionReader
	file io.Closer
}

func (c committedTableFile) Close() error { return c.file.Close() }

func (g *Games) openCommitted(ctx context.Context, data TableData) (committedTableFile, error) {
	f, err := g.tables.volume().Open(data.ID.String())
	if errors.Is(err, gamefile.ErrNotFound) && data.Status == UploadComplete {
		// Read again: an upload completed since data was read retires this
		// file, and that is a table that changed, not a damaged one.
		current, readErr := g.tableRows.ByID(ctx, data.ID)
		switch {
		case readErr != nil && !errors.Is(readErr, ErrUploadNotFound):
			return committedTableFile{}, fmt.Errorf("read the table's current data: %w", readErr)
		case readErr == nil && current.Status == UploadComplete:
			return committedTableFile{}, fmt.Errorf("%w: upload %s of table %q has no file",
				ErrTableDataDamaged, data.ID, data.Table)
		}
	}
	if err != nil {
		return committedTableFile{}, wrapGamefileErr(err)
	}
	info, err := f.Stat()
	if err == nil && info.Size() < data.ReceivedBytes {
		err = fmt.Errorf("%w: upload %s of table %q holds %d bytes of %d",
			ErrTableDataDamaged, data.ID, data.Table, info.Size(), data.ReceivedBytes)
	}
	if err != nil {
		_ = f.Close()
		return committedTableFile{}, fmt.Errorf("read the table's data: %w", err)
	}
	return committedTableFile{SectionReader: io.NewSectionReader(f, 0, data.ReceivedBytes), file: f}, nil
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
	if !g.tables.enabled() {
		return TableData{}, ErrUploadsDisabled
	}
	if _, err := g.currentDefinitionTable(ctx, contestID, table); err != nil {
		return TableData{}, err
	}
	return g.tableRows.Current(ctx, contestID, table)
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
			data, err := g.tableRows.Ready(ctx, contestID, table.Name)
			switch {
			case errors.Is(err, ErrUploadNotFound):
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
		data, err := g.tableRows.Ready(ctx, contestID, table.Name)
		switch {
		case errors.Is(err, ErrUploadNotFound):
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

// checkTableName is Uploads.checkName for a table's CSV: the table must be
// in the contest's current definition.
func (g *Games) checkTableName(ctx context.Context, contestID uuid.UUID, table string) error {
	_, err := g.currentDefinitionTable(ctx, contestID, table)
	return err
}

// checkTableHeaderOnFirstChunk is Uploads.firstChunk for a table's CSV. It
// validates the header if its line has fully arrived, reading no further than
// the first newline; otherwise it says nothing and leaves the check to
// CompleteTableUpload, which checks it again anyway.
func (g *Games) checkTableHeaderOnFirstChunk(ctx context.Context, data TableData) error {
	table, err := g.currentDefinitionTable(ctx, data.ContestID, data.Table)
	if err != nil {
		return err
	}
	f, err := g.tables.volume().Open(data.ID.String())
	if err != nil {
		return wrapGamefileErr(err)
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

// CompleteTableUpload finalises a chunked CSV upload: checks the received
// length, then streams the file once, validating the header and then every
// row. This pass is authoritative; the first chunk's header check is only an
// early refusal and may not have run.
func (g *Games) CompleteTableUpload(ctx context.Context, actorID, contestID, id uuid.UUID) (TableData, error) {
	data, err := g.tables.receiving(ctx, contestID, id)
	if err != nil {
		return TableData{}, err
	}

	table, err := g.currentDefinitionTable(ctx, contestID, data.Table)
	if err != nil {
		return TableData{}, err
	}

	if err := g.requireEditable(ctx, contestID); err != nil {
		return TableData{}, err
	}

	if data.ReceivedBytes != data.DeclaredBytes {
		return TableData{}, ErrUploadLengthMismatch
	}

	lines, err := g.validateTableFile(ctx, data, table)
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
		_ = g.tables.retire(*previous)
	}
	return completed, nil
}

// completeTableDataAndAudit runs the write and its audit entry as one unit.
func (g *Games) completeTableDataAndAudit(
	ctx context.Context, actorID, contestID uuid.UUID, table string, id uuid.UUID, bytes, lines int64, previous *uuid.UUID,
) (TableData, error) {
	var completed TableData
	run := func(ctx context.Context) error {
		if err := g.beginGameEdit(ctx, contestID); err != nil {
			return err
		}
		var err error
		completed, err = g.tableRows.Complete(ctx, contestID, table, id, bytes, lines, previous)
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
	ready, err := g.tableRows.Ready(ctx, contestID, table)
	switch {
	case errors.Is(err, ErrUploadNotFound):
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
func (g *Games) validateTableFile(ctx context.Context, data TableData, table TableDefinition) (int64, error) {
	f, err := g.openCommitted(ctx, data)
	if err != nil {
		return 0, err
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

// AppendTableRow adds one row typed into a form to the table's file. values
// are the field text in column order. The first row creates the file; later
// rows are appended and only the new row is validated.
//
// Refused while a chunked upload is 'receiving' for the table: completing it
// replaces the file this row would go into.
//
// Of two forms that read the same end of the table, one row is recorded and
// the other form is answered ErrTableDataChanged with nothing written, so the
// client adds its row again (committedTableFile).
func (g *Games) AppendTableRow(ctx context.Context, actorID, contestID uuid.UUID, tableName string, values []string) (TableData, error) {
	if !g.tables.enabled() {
		return TableData{}, ErrUploadsDisabled
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

	if _, err := g.tableRows.Current(ctx, contestID, tableName); err == nil {
		return TableData{}, ErrUploadInProgress
	} else if !errors.Is(err, ErrUploadNotFound) {
		return TableData{}, fmt.Errorf("check for an upload in progress: %w", err)
	}

	ready, err := g.tableRows.Ready(ctx, contestID, tableName)
	switch {
	case errors.Is(err, ErrUploadNotFound):
		return g.bootstrapTableRow(ctx, actorID, contestID, tableName, table, rowLine)
	case err != nil:
		return TableData{}, fmt.Errorf("read the table's current data: %w", err)
	}
	if ready.Lines >= MaxTableDataRows {
		// The same ceiling validateTableFile enforces for uploads (CLAUDE.md
		// rule 2).
		return TableData{}, fmt.Errorf("%w: the table already holds %d rows, the limit is %d",
			ErrTableTooManyRows, ready.Lines, MaxTableDataRows)
	}

	payload, err := g.rowPayload(ctx, ready, rowLine)
	if err != nil {
		return TableData{}, err
	}

	var updated TableData
	run := func(ctx context.Context) error {
		if err := g.beginGameEdit(ctx, contestID); err != nil {
			return err
		}
		var err error
		updated, err = g.tableRows.AppendRow(ctx, ready.ID, ready.ReceivedBytes, int64(len(payload)))
		if err != nil {
			return fmt.Errorf("record the appended row: %w", err)
		}
		if err := g.repo.MarkTableDataChanged(ctx, contestID); err != nil {
			return fmt.Errorf("mark the contest's data changed: %w", err)
		}
		if err := g.record(ctx, audit.Entry{
			ActorID: &actorID, Action: audit.ActionGameTableDataRowAdd,
			Entity: "contest", EntityID: contestID.String(),
			Payload: map[string]any{"table": tableName, "row": updated.Lines},
		}); err != nil {
			return err
		}
		// Last, so that only a failed commit leaves a tail.
		if _, err := g.tables.volume().ReplaceTail(ready.ID.String(), ready.ReceivedBytes, strings.NewReader(payload)); err != nil {
			if errors.Is(err, gamefile.ErrShorterThanRecorded) {
				return fmt.Errorf("%w: %w", ErrTableDataDamaged, err)
			}
			return wrapGamefileErr(err)
		}
		return nil
	}
	if err := g.atomically(ctx, run); err != nil {
		return TableData{}, err
	}
	return updated, nil
}

// rowPayload is the bytes one row is appended as: the row and its newline,
// preceded by a newline when the file does not end in one. Many exporters omit
// the final newline, and without this the new row would be glued onto the
// last one.
func (g *Games) rowPayload(ctx context.Context, data TableData, rowLine string) (string, error) {
	if data.ReceivedBytes == 0 {
		return rowLine + "\n", nil
	}
	f, err := g.openCommitted(ctx, data)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	var last [1]byte
	if _, err := f.ReadAt(last[:], data.ReceivedBytes-1); err != nil {
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
	if err := g.tables.volume().Begin(id.String(), int64(len(content))); err != nil {
		return TableData{}, wrapGamefileErr(err)
	}
	if _, err := g.tables.volume().Append(id.String(), 0, strings.NewReader(content)); err != nil {
		_ = g.tables.retire(id)
		return TableData{}, wrapGamefileErr(err)
	}

	var created TableData
	ranToCommit := false
	run := func(ctx context.Context) error {
		if err := g.beginGameEdit(ctx, contestID); err != nil {
			return err
		}
		var err error
		created, err = g.tableRows.CreateReady(ctx, id, contestID, tableName, int64(len(content)), 1)
		if err != nil {
			return fmt.Errorf("store the table's first row: %w", err)
		}
		if err := g.repo.MarkTableDataChanged(ctx, contestID); err != nil {
			return fmt.Errorf("mark the contest's data changed: %w", err)
		}
		if err := g.record(ctx, audit.Entry{
			ActorID: &actorID, Action: audit.ActionGameTableDataRowAdd,
			Entity: "contest", EntityID: contestID.String(),
			Payload: map[string]any{"table": tableName, "row": int64(1)},
		}); err != nil {
			return err
		}
		ranToCommit = true
		return nil
	}
	err = g.atomically(ctx, run)
	if err != nil {
		// Most often another caller bootstrapped the same table first, and
		// the file this call wrote is nobody's: remove it now. A failed
		// commit may have recorded the file, so it is left for the orphan
		// sweep to judge.
		if !ranToCommit {
			_ = g.tables.retire(id)
		}
		if errors.Is(err, ErrUploadInProgress) {
			return TableData{}, ErrUploadInProgress
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
	if !g.tables.enabled() {
		return TableRowWindow{}, ErrUploadsDisabled
	}
	if _, err := g.currentDefinitionTable(ctx, contestID, table); err != nil {
		return TableRowWindow{}, err
	}
	data, err := g.tableRows.Ready(ctx, contestID, table)
	if err != nil {
		if errors.Is(err, ErrUploadNotFound) {
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

	f, err := g.openCommitted(ctx, data)
	if err != nil {
		return TableRowWindow{}, err
	}
	defer func() { _ = f.Close() }()

	marks := g.rowMarks.of(data.ID.String())
	scanner, row, err := openTableRowScan(f, data.ReceivedBytes, marks, fromRow)
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
	if !g.tables.enabled() {
		return ErrUploadsDisabled
	}
	if _, err := g.currentDefinitionTable(ctx, contestID, table); err != nil {
		return err
	}
	data, err := g.tableRows.Ready(ctx, contestID, table)
	if err != nil {
		if errors.Is(err, ErrUploadNotFound) {
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
		if err := g.beginGameEdit(ctx, contestID); err != nil {
			return err
		}
		if err := g.tableRows.DeleteRow(ctx, data.ID, row); err != nil {
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
	if !g.tables.enabled() {
		return nil
	}
	for _, table := range definition.Tables {
		data, err := g.tableRows.Ready(ctx, contestID, table.Name)
		if errors.Is(err, ErrUploadNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read %s's own data: %w", table.Name, err)
		}

		f, err := g.openCommitted(ctx, data)
		if err != nil {
			return fmt.Errorf("open %s's own data: %w", table.Name, err)
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
		if bytes.Equal(line, copyEndOfData) {
			// CSV COPY still reads a bare `\.` line as the end of the data,
			// so a one-column text table holding that value would be cut
			// short in silence. Quoted, it is the value.
			line = []byte(`"\."`)
		}
		c.pending = append(line, '\n')
		return
	}
}

// copyEndOfData is the line COPY FROM STDIN takes as "no more rows", in CSV
// format as well as text.
var copyEndOfData = []byte(`\.`)
