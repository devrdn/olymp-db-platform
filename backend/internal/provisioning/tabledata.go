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

// This file is the table builder's own data: one CSV file per table of a
// builder-sourced game (Definition), on the same volume an uploaded dump
// lives on (upload.go) but in its own directory and its own bookkeeping
// table (migration 27) — game_table_data.go's own doc explains why a second
// table rather than a nullable column on game_uploads.
//
// A table's file is addressed exactly the way an upload's is: an id that
// names two files on disk (internal/gamefile's own convention) and one row
// that survives the request that started it. What differs is what "done"
// means. An uploaded dump becomes the contest's game the moment it
// completes; a table's CSV never does — it is data for one table of a
// definition that was already saved, and completing it only ever changes
// this file's own row. Loading it into a built database is
// Games.loadTableData, called from finishDefinitionBuild (template.go) once
// the schema exists.
//
// Row numbers, throughout this file, count data rows only (the header is
// not row 1) and are 1-based and never reused: DeleteTableRow tombstones a
// row number rather than removing the line it names, so the numbering a
// participant... no, an organiser... sees never shifts under them mid-review.

// TableDataStatus is where one table's CSV file has got to.
type TableDataStatus string

const (
	// TableDataReceiving is a chunked upload still taking bytes.
	TableDataReceiving TableDataStatus = "receiving"
	// TableDataComplete is a file whose header and every row have been
	// validated against the table's own columns — the one status a build
	// will ever load rows from.
	TableDataComplete TableDataStatus = "complete"
	// TableDataAborted is a chunked upload cancelled before it completed.
	TableDataAborted TableDataStatus = "aborted"
)

// TableData is one table's CSV file, as far as the database's own
// bookkeeping goes. Its bytes are never here — internal/gamefile holds
// those, addressed by this row's own ID, exactly the way Upload's are.
type TableData struct {
	ID            uuid.UUID
	ContestID     uuid.UUID
	Table         string
	DeclaredBytes int64
	ReceivedBytes int64
	// Lines is the number of data rows the file holds — the header does not
	// count. Valid once Status is TableDataComplete; zero until then.
	Lines int64
	// DeletedRows are the row numbers (1-based, data rows only) an organiser
	// has tombstoned. Bounded at MaxTableDeletedRows.
	DeletedRows []int64
	Status      TableDataStatus
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ActiveRows is how many of the file's rows have not been deleted — what an
// organiser's own screen shows as "N rows", rather than the raw line count a
// tombstone leaves unchanged.
func (d TableData) ActiveRows() int64 { return d.Lines - int64(len(d.DeletedRows)) }

// deletedSet is DeletedRows as a lookup a window read or a build's own load
// can test in O(1) per row rather than scanning the slice per line.
func (d TableData) deletedSet() map[int64]struct{} {
	set := make(map[int64]struct{}, len(d.DeletedRows))
	for _, row := range d.DeletedRows {
		set[row] = struct{}{}
	}
	return set
}

// Why a table's data could not be received, completed, read back or edited.
// Declared sentinels (CLAUDE.md rule 1), the same shape upload.go's own
// block takes for the dump it mirrors.
var (
	// ErrTableDataDisabled is every table-data method's answer on an
	// installation with no upload volume configured — Games was never given
	// WithTableData.
	ErrTableDataDisabled = errors.New("table data uploads are not configured on this installation")
	// ErrTableUnknown is a table name that does not appear, exactly as
	// spelled, in the contest's current definition.
	ErrTableUnknown = errors.New("the table is not part of the contest's current definition")
	// ErrTableDataInProgress is a second upload begun for a table that
	// already has one 'receiving', or a race between two callers bootstrapping
	// the same table's first row at once.
	ErrTableDataInProgress = errors.New("this table already has an upload in progress")
	// ErrTableDataNotFound is an id, or a (contest, table) pair, that names
	// no table-data row of the state being asked for.
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
	// ErrTableDataChanged is an AppendTableRow whose row is not the one that
	// ended up at the end of the file: another form's row landed between this
	// call reading the table's current data and writing its own, and
	// gamefile.Store.Append reported that as the success it reports for any
	// retry of an offset it already has. Only one row can be written at the
	// file's end, and the caller whose row was not has to be told so —
	// answering it 201 would drop that row silently (AppendTableRow's own
	// doc). Also what a row that is no longer a table's current file answers,
	// when a game was replaced under the call.
	ErrTableDataChanged = errors.New("the table's data changed while this row was being added")
	// ErrTableRowNotFound is a row number DeleteTableRow or a window read was
	// asked for that the file does not have.
	ErrTableRowNotFound = errors.New("no such row")
	// ErrTableRowAlreadyDeleted is a DeleteTableRow for a row already
	// tombstoned — repeating a delete is not an error a client needs telling
	// twice, but this lets a caller that cares (an audit entry that must not
	// claim a second deletion) tell the two apart.
	ErrTableRowAlreadyDeleted = errors.New("the row has already been deleted")
	// ErrTooManyDeletedRows is a delete past MaxTableDeletedRows — migration
	// 27's own CHECK, surfaced as a sentinel rather than a raw constraint
	// violation.
	ErrTooManyDeletedRows = errors.New("too many rows have been deleted from this table")
)

// wrapTableFileErr turns one of internal/gamefile's own sentinels into this
// package's, the same job upload.go's wrapGamefileErr does for a dump —
// mapped separately (not shared) because the two carry different words for
// the same underlying fact, and a handler's fail switch for a table's CSV
// must not have to recognise an upload's own sentinel to serve the right
// status.
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

// WithTableData turns on the table builder's own per-table CSV storage.
// store is a *second*, independent gamefile.Store — its own directory,
// never the one WithUploads was given — so that the orphan sweep for one
// kind of file can never mistake the other's id for a file nothing needs any
// more (gamefile.Store's own doc: "nothing about a directory is safe to
// share between two Stores"). Both directories live on the one volume the
// task's own brief asks for; they are simply not the same Store.
func (g *Games) WithTableData(store *gamefile.Store, limits gamefile.Limits) *Games {
	g.tableFiles, g.tableLimits = store, limits
	return g
}

// TableDataLimits reports the ceilings a chunked table-data upload must
// respect — UploadLimits' own doc, for the table builder's own store rather
// than the dump's. Two independent gamefile.Store values means two
// independent ceilings even on a deployment that happens to configure them
// identically today (WithTableData's own doc: "never the one WithUploads
// was given") — so this reads g.tableLimits, never g.limits, and internal/api
// reaches it through this method rather than a constant of its own
// (CLAUDE.md rule 11), exactly as it already does for UploadLimits.
func (g *Games) TableDataLimits() (gamefile.Limits, bool) {
	return g.tableLimits, g.tableFiles != nil
}

// currentDefinitionTable reads the contest's current definition and returns
// the TableDefinition named table, exactly as spelled — or ErrTableUnknown
// when the game is not builder-sourced, or names no such table. Every
// table-data method that is not a pure id lookup calls this first: an
// organiser cannot upload data for a table that does not (or no longer)
// exist, and Validate already refused any name that would not round-trip
// through an exact comparison.
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

// CurrentTableData lets a reloaded page find a table's own chunked CSV
// upload still 'receiving' and offer to resume it, rather than a second
// BeginTableUpload refusing with no way to explain why —
// Games.CurrentUpload's own doc (upload.go), for a table's own file instead
// of a whole dump.
//
// Symmetric with every other table-data method that is not a pure id lookup
// (currentDefinitionTable's own doc): the table must actually be part of
// the contest's current definition before this answers for it, so an
// unknown or since-removed table name is ErrTableUnknown rather than being
// folded into "nothing in progress".
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
// structure of a table that already holds data — ErrDefinitionTableLocked's
// own doc (definition.go) explains why the freeze covers a whole table
// (its name, every column, and the primary key) rather than only the one
// field an organiser happened to touch.
//
// Called from SetDefinition (template.go) right after Definition.Validate,
// before anything is written — the identical "before the click" ordering
// that check already follows for a definition whose shape alone is wrong.
func (g *Games) checkTableDataCompatibility(ctx context.Context, contestID uuid.UUID, next Definition) error {
	current, err := g.repo.Template(ctx, contestID)
	switch {
	case errors.Is(err, ErrNoGame):
		return nil // no game yet: no table of it can hold any data
	case err != nil:
		return fmt.Errorf("read the contest's current game: %w", err)
	}
	if current.Source != SourceBuilder {
		// A game that is not builder-sourced holds no table data: replaceGame
		// (template.go) discards it in the same transaction that stops the
		// game being the builder's, and migration 28 retired what predated
		// that rule. This used to return here on that reasoning alone — that
		// such a game "names no table any such row could belong to" — which
		// was true of the definition and false of the rows: nothing deleted
		// them, so saving any script at all was a way round the lock below,
		// and the redescribed table then loaded values validated against
		// another type. The loop is the backstop for that invariant rather
		// than a second copy of it: it costs one read per table of a
		// definition being saved over a game that is not the builder's, and
		// it refuses instead of silently loading data no check can vouch for.
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
			continue // a header with nothing under it: ErrDefinitionTableLocked's own doc on why this is not locked
		}
		replacementTable, stillThere := replacement[table.Name]
		if !stillThere || !table.sameStructure(replacementTable) {
			return fmt.Errorf("%w: table %q has %d row(s) of data", ErrDefinitionTableLocked, table.Name, data.Lines)
		}
	}
	return nil
}

// BeginTableUpload reserves a new chunked CSV upload for one table of the
// contest's current definition.
//
// GameEditable is checked here for the same reason BeginUpload checks it:
// starting to receive a file into a contest that is already running is disk
// and time nobody gets back. The reservation on disk happens before the
// database row (Store.Begin, then the INSERT), so a database refusal — most
// often ErrTableDataInProgress, from migration 27's own partial index —
// leaves at worst an empty file with no row, exactly what the orphan sweep
// exists to find.
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

	editable, err := g.author.GameEditable(ctx, contestID)
	if err != nil {
		return TableData{}, fmt.Errorf("check whether the game may be replaced: %w", err)
	}
	if !editable {
		return TableData{}, ErrGameNotEditable
	}

	id := uuid.New()
	if err := g.tableFiles.Begin(id.String(), declaredBytes); err != nil {
		return TableData{}, wrapTableFileErr(err)
	}
	data, err := g.repo.BeginTableData(ctx, id, contestID, table, declaredBytes)
	if err != nil {
		if errors.Is(err, ErrTableDataInProgress) {
			return TableData{}, ErrTableDataInProgress
		}
		return TableData{}, fmt.Errorf("record the upload: %w", err)
	}
	return data, nil
}

// AppendTableChunk writes one chunk of a table-data upload already begun —
// AppendChunk's own doc, for a table's file instead of a whole dump.
//
// The chunk that lands at offset 0 gets one extra check once it is written:
// whether the file's first line, if it has arrived in full, names the
// table's own columns. This is the brief's own requirement read for what it
// actually asks: a header that does not match is refused before the whole
// file is accepted, not before any byte of it is — the header is itself
// data, so "before the first byte" can only ever mean "after the first
// chunk", never literally before any bytes exist. An organiser who uploaded
// the wrong file this way finds out having spent one chunk's own transfer
// and wait, not the whole file's. CompleteTableUpload's own full pass
// (validateTableFile) still checks the header again when the upload
// finishes — this early check only ever adds an earlier chance to refuse,
// it never replaces that one.
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
		return received, nil // the idempotent-skip path (gamefile.Store.Append's own doc)
	}
	if err := g.repo.UpdateTableDataReceived(ctx, id, received); err != nil {
		return received, fmt.Errorf("record the upload's progress: %w", err)
	}

	// offset == 0 names exactly the chunk that just wrote the start of the
	// file. It is the only offset this branch can ever see for a given
	// upload: every later chunk's own offset is wherever the file's length
	// stood before it, which is never 0 again once a byte has landed — and
	// a resend of this same first chunk took the idempotent-skip return
	// above instead of reaching here, since gamefile.Store.Append reports
	// the file's unchanged length for a chunk it already has. So this runs
	// at most once per upload: never on a retry, never on any chunk after
	// the first.
	if offset == 0 {
		if err := g.checkTableHeaderOnFirstChunk(ctx, contestID, id, data.Table); err != nil {
			return received, err
		}
	}
	return received, nil
}

// checkTableHeaderOnFirstChunk is AppendTableChunk's own early half of the
// header check validateTableFile runs in full at CompleteTableUpload: it
// reads only as far as it takes to find the first line's own newline
// (firstLineIfComplete, tablecsv.go), never the whole chunk, and says
// nothing when that newline has not arrived yet — a header that does not
// fit inside the first chunk is not this call's business, only
// CompleteTableUpload's own full pass is.
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
		return nil // the header has not fully arrived in this chunk; nothing to check yet
	}
	fields, err := splitCSVLine(line)
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
		// Same reasoning as Games.currentContestUpload: a row that exists but
		// names another contest reads identically to one that does not exist,
		// so as not to leak that somebody else's upload is there.
		return TableData{}, ErrTableDataNotFound
	}
	return data, nil
}

// CompleteTableUpload finalises a chunked CSV upload: checks the received
// length against what was declared, then runs one streaming pass over the
// file (never the whole file at once — tableLineScanner's own bound) that
// checks the header before it reads a single data row, and every data row's
// field count and column types after that.
//
// The header is checked again here even though AppendTableChunk's own early
// check (its own doc) already looked at it once the first chunk landed —
// that early check is best-effort, not exhaustive: a header that did not fit
// inside the first chunk, or a file whose bytes reached the store some other
// way than AppendTableChunk (a test writing to it directly, say), reaches
// this pass having never been checked at all. This full pass is the one a
// build actually depends on; within it, the header is still checked before a
// single data row is read, so a mistaken upload does not spend the CPU (or
// the organiser's own wait) validating rows against columns the file was
// never really describing.
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

	editable, err := g.author.GameEditable(ctx, contestID)
	if err != nil {
		return TableData{}, fmt.Errorf("check whether the game may be replaced: %w", err)
	}
	if !editable {
		return TableData{}, ErrGameNotEditable
	}

	if data.ReceivedBytes != data.DeclaredBytes {
		return TableData{}, ErrTableDataLengthMismatch
	}

	lines, err := g.validateTableFile(id.String(), table)
	if err != nil {
		return TableData{}, err
	}

	// Whichever ready file this one displaces — this table's own, if it has
	// one. Retired inside the write below and removed from disk only once it
	// commits, the same deferred-removal order Games.replaceGame uses for a
	// displaced dump.
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

// completeTableDataAndAudit runs the write and its audit entry as one unit,
// the same shape replaceGame gives SetScript and CompleteUpload.
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
		if g.audit == nil {
			return nil
		}
		return g.audit.Record(ctx, audit.Entry{
			ActorID: &actorID, Action: audit.ActionGameTableDataUpload,
			Entity: "contest", EntityID: contestID.String(),
			Payload: map[string]any{"table": table, "rows": lines, "bytes": bytes},
		})
	}
	var err error
	if g.uow != nil {
		err = g.uow.Do(ctx, run)
	} else {
		err = run(ctx)
	}
	return completed, err
}

// tableDataToDisplace names the table's current 'complete' file, if it has
// one and it is not the id being completed — the file the new upload is
// about to replace.
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

// validateTableFile is the streaming pass CompleteTableUpload and the
// bootstrap half of AppendTableRow both run: header first, then every data
// row's field count and column types, never holding more of the file than
// tableLineScanner's own bound. It returns the number of data rows found.
func (g *Games) validateTableFile(id string, table TableDefinition) (int64, error) {
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
	headerFields, err := splitCSVLine(header)
	if err != nil {
		return 0, err
	}
	if err := validateHeader(headerFields, table); err != nil {
		return 0, err
	}

	var lines int64
	for {
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

// abortTableData is AbortTableUpload's own work, factored out so the janitor
// (sweepAbandonedTableData) can call it with a nil actor — a system event,
// the same convention abortUpload's own doc gives for the dump janitor.
func (g *Games) abortTableData(ctx context.Context, actor *uuid.UUID, data TableData) (TableData, error) {
	mark := func(ctx context.Context) error {
		if err := g.repo.AbortTableData(ctx, data.ID); err != nil {
			return fmt.Errorf("mark the table upload aborted: %w", err)
		}
		if g.audit == nil {
			return nil
		}
		return g.audit.Record(ctx, audit.Entry{
			ActorID: actor, Action: audit.ActionGameTableDataUploadAbort,
			Entity: "contest", EntityID: data.ContestID.String(),
			Payload: map[string]any{"table": data.Table, "bytes": data.ReceivedBytes},
		})
	}

	if err := g.retireTableDataFile(data.ID); err != nil {
		return TableData{}, fmt.Errorf("remove the table upload's file: %w", err)
	}
	var err error
	if g.uow != nil {
		err = g.uow.Do(ctx, mark)
	} else {
		err = mark(ctx)
	}
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

// AppendTableRow adds one row that came from a form — the organiser typing a
// suspect's row directly rather than uploading a file — to the same file a
// chunked upload's rows land in. values are the field text in the table's
// own column order.
//
// Refused while a chunked upload is 'receiving' for this table: both paths
// would otherwise compute the append offset from the same
// bookkeeping-reported length and race gamefile.Store.Append's own
// idempotent-retry logic, which treats a second write at an offset already
// covered as a no-op rather than as a second row — silently dropping it.
// Requiring the chunked upload to finish or be cancelled first is what keeps
// "the row from the form and the row from the batch land in the same file"
// (the brief's own words) true without that race.
//
// Two forms racing each other are a different matter, and are not refused —
// they are decided. Both read the same "the file is N bytes long" and both
// write there; gamefile.Store.Append answers the second one with the
// idempotent-retry success its own doc promises, having written nothing, so
// without a check that row is silently gone and the length recorded for it is
// one no file has, which makes every later append out of order for ever. Two
// things prevent that. The bytes just written are read back before anything
// is recorded (rowLanded), so a caller whose row is not the one at that
// offset is told ErrTableDataChanged rather than 201 for a row nobody will
// ever see. And the length and row count are recorded as floors rather than
// assignments (AppendTableDataRow), so the two callers reaching storage in
// the opposite order to the one they read in cannot leave the bookkeeping
// describing the shorter file: the second caller's own row has by then
// already been counted by the recount below, and its smaller pair is
// discarded rather than written.
//
// The bytes go first and the row that counts them second, which is the order
// whose failure is recoverable: an interruption (or a rolled-back
// transaction — a failed audit write is enough) leaves a file holding one
// more validated row than the bookkeeping counts, and the next call notices
// that and reconciles it (tableFileState). The other order would leave a row
// counted whose bytes never arrived, and no later call could tell what was
// meant to be there.
//
// The very first row for a table bootstraps its file: there is no upload to
// begin first, because a single validated row already satisfies everything
// CompleteTableUpload's own pass checks. Every row after that is appended to
// the existing file directly — O(1), no reseal, unlike CompleteTableUpload's
// own full pass, because this validates only the one new row rather than
// re-reading everything already accepted.
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

	editable, err := g.author.GameEditable(ctx, contestID)
	if err != nil {
		return TableData{}, fmt.Errorf("check whether the game may be replaced: %w", err)
	}
	if !editable {
		return TableData{}, ErrGameNotEditable
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
		// The same ceiling validateTableFile enforces for an uploaded file and
		// the same number this service publishes to its clients as max_rows —
		// a limit that holds on one of the two ways in is not a limit
		// (CLAUDE.md rule 2).
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
		// Another form's row is at this offset: gamefile.Store.Append treats a
		// write at an offset already covered as a retry of it and reports
		// success without writing a byte (its own doc). Nothing of this row
		// reached the file, so this caller is told so rather than being
		// answered 201 for a row nobody will ever see.
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
		if g.audit == nil {
			return nil
		}
		return g.audit.Record(ctx, audit.Entry{
			ActorID: &actorID, Action: audit.ActionGameTableDataRowAdd,
			Entity: "contest", EntityID: contestID.String(),
			Payload: map[string]any{"table": tableName, "row": newLines},
		})
	}
	if g.uow != nil {
		err = g.uow.Do(ctx, run)
	} else {
		err = run(ctx)
	}
	if err != nil {
		return TableData{}, err
	}
	return updated, nil
}

// rowLanded reports whether the bytes now at offset are this call's own row.
//
// gamefile.Store.Append answers a write at an offset it already has with the
// file's unchanged length and no error — the idempotent retry a resumed
// chunk upload depends on (its own doc), and exactly what a second form
// adding a row at the same moment gets. The length alone does not settle it,
// because two rows of the same table are often the same number of bytes, so
// the bytes themselves are read back and compared. One row's worth of them,
// never more: this reads what was just written and nothing else.
//
// Two callers writing byte-identical rows both read their own payload back
// and both believe they wrote, and the file holds that row once. Harmless,
// and the one case where the length alone would have been enough: the two
// asked for the same row, and the same row is what is there. What is not
// harmless — one caller's row overwritten by another's, or counted as if it
// were there — is exactly what comparing the bytes rules out.
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

// tableFileState reports the offset the next row goes at and how many data
// rows the file holds — the file's own answer to both, not the bookkeeping's
// copy of it.
//
// The two normally agree, and then this costs one stat. When they do not, the
// file is the one telling the truth: its bytes are what a build loads and
// what a window read pages through. The bytes always go first
// (AppendTableRow's own doc on the order), so the only way the two can
// disagree is a file holding one more already-validated row than the
// bookkeeping counts — a transaction that rolled back, or a process that died,
// after that row had landed. Appending at an offset taken from the stale side
// would write nowhere at all (gamefile.Store.Append reports the retry success
// its own doc promises for an offset already covered), so the disagreement is
// resolved here rather than carried into every later call as a table that
// takes no more rows.
//
// Recounting means one streaming pass over the file (never more of it than
// tableLineScanner's own bound), which is why it is done only on the path
// where the two disagree.
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

// countTableDataRows counts the file's data rows — every line but the header,
// tombstoned ones included, since DeletedRows names row numbers this count
// has to keep naming the same rows. Nothing is validated here: these rows
// were validated when they were written, and this is a recount, not a second
// opinion on their contents.
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

// rowPayload is the bytes one row is appended as: the row itself and the
// newline that ends it, preceded by one more newline when the file does not
// already end in one.
//
// A CSV whose last line has no trailing newline is what a good many
// exporters write, and validateTableFile accepts it — that last line is a
// whole row (tableLineScanner.next's own doc). Appending to such a file
// without this would glue the new row onto the end of the last one: two rows
// of three fields becoming one row of six, garbage in the organiser's own
// window and `extra data after last expected column` on the build that
// follows.
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

// bootstrapTableRow creates a table's very first data file: the header this
// package's own brief requires as the file's first line, followed by the one
// row already validated by the caller.
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
		if g.audit == nil {
			return nil
		}
		return g.audit.Record(ctx, audit.Entry{
			ActorID: &actorID, Action: audit.ActionGameTableDataRowAdd,
			Entity: "contest", EntityID: contestID.String(),
			Payload: map[string]any{"table": tableName, "row": int64(1)},
		})
	}
	if g.uow != nil {
		err = g.uow.Do(ctx, run)
	} else {
		err = run(ctx)
	}
	if err != nil {
		// The database refused to record a file already written — most often
		// the partial unique index, when a second caller bootstrapped the
		// same table first. The file this call wrote is nobody's; remove it
		// rather than leave it for the orphan sweep to find minutes later.
		_ = g.retireTableDataFile(id)
		if errors.Is(err, ErrTableDataInProgress) {
			return TableData{}, ErrTableDataInProgress
		}
		return TableData{}, err
	}
	return created, nil
}

// TableRow is one data row of a window read — its stable row number and its
// field text, in the table's own column order. A field that was NULL in the
// file (an unquoted empty field, csvField.Null's own doc) reads back as an
// empty string here: this package's console shows a row for review, not a
// value editor that must tell "empty" from "absent" apart.
type TableRow struct {
	Row    int64
	Fields []string
}

// TableRowWindow is one page of a table's data rows, read for display.
type TableRowWindow struct {
	FromRow int64
	Rows    []TableRow
	// TotalRows is the file's own row count — Lines, not ActiveRows — so
	// that "row 37 of 40" still means the 40 the file was completed with,
	// with the deleted ones simply missing from the page rather than
	// silently renumbering everything after them.
	TotalRows int64
	// Truncated says the byte budget stopped the window before maxRows was
	// reached, gamefile.Window's own Truncated field for the same reason.
	Truncated bool
}

// TableDataWindow reads up to maxRows surviving (not tombstoned) rows of a
// table's current data, starting at fromRow, never reading more than
// maxBytes of the file and never reading past what it returns — the
// console's own paginated look at a table's rows, the reason this does not
// reuse gamefile.Store.Window's own persisted index (this package's own
// doc on why: that index is sealed the moment it is built, and a table's
// file is written to again by every AppendTableRow after the first). A
// linear scan from the start of the file costs O(fromRow), not O(1); at the
// scale one table of an olympiad's own game is expected to hold, that is not
// a cost worth a second on-disk index to avoid.
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
			return TableRowWindow{FromRow: fromRow}, nil // no file yet: an empty table, not an error
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

	scanner := newTableLineScanner(f)
	if _, err := scanner.next(); err != nil { // the header; already validated, only skipped here
		return TableRowWindow{}, fmt.Errorf("read the table's data: %w", err)
	}

	deleted := data.deletedSet()
	var remaining = maxBytes
	window := TableRowWindow{FromRow: fromRow, TotalRows: data.Lines}
	var row int64
	for {
		line, err := scanner.next()
		if errors.Is(err, io.EOF) {
			return window, nil
		}
		if err != nil {
			return TableRowWindow{}, fmt.Errorf("read the table's data: %w", err)
		}
		row++
		if row < fromRow {
			continue
		}
		if _, isDeleted := deleted[row]; isDeleted {
			continue
		}
		// A row over budget stops the window here — except when it is the
		// page's own first row, which is let through anyway. Without this,
		// a table whose rows sit near MaxTableFieldBytes (columns wide
		// enough that one row alone can reach megabytes) answers every
		// window smaller than that with an empty page and truncated=true —
		// indistinguishable from "no more rows", and the "next" offset
		// (fromRow + rows shown) then computes right back to fromRow, since
		// zero rows were shown. gamefile.Store.Window (readWindowLines) never
		// does this to a dump's own line window: asked for more than its
		// budget allows, it still returns the one line it has, cut to the
		// budget, with Truncated set — a caller told the truth about a page
		// that cost more than it asked for, rather than one told nothing was
		// there. A CSV row can't be cut the same way (a sliced row would
		// parse as fields belonging to no real data), so instead of
		// shortening it, this lets the whole row through once, then stops:
		// a page can therefore go over its own byte budget, but it can never
		// show fewer than one row of data that exists.
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

// DeleteTableRow tombstones one row of a table's current data.
//
// Not a rewrite of the file. Removing a row from the middle of a file this
// platform expects to hold gigabytes of rows would mean reading and
// rewriting everything after it, on every click, for a file whose whole
// reason to live on disk rather than in the core database is that it can be
// exactly that large. A tombstone is a single append to a small bounded list
// (game_table_data.deleted_rows, MaxTableDeletedRows) instead: the delete
// costs one UPDATE regardless of the file's own size, and every reader
// (TableDataWindow, and Games.loadTableData at build time) simply skips the
// row number when it streams past it.
//
// What an interruption leaves behind: nothing partial. The tombstone is one
// UPDATE in the core database, and PostgreSQL's own transaction either
// applies it whole or not at all — there is no file write here to leave
// half-done, which a physical delete could not promise without its own
// journal.
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

	editable, err := g.author.GameEditable(ctx, contestID)
	if err != nil {
		return fmt.Errorf("check whether the game may be replaced: %w", err)
	}
	if !editable {
		return ErrGameNotEditable
	}

	run := func(ctx context.Context) error {
		if err := g.repo.DeleteTableDataRow(ctx, data.ID, row); err != nil {
			return err
		}
		if g.audit == nil {
			return nil
		}
		return g.audit.Record(ctx, audit.Entry{
			ActorID: &actorID, Action: audit.ActionGameTableDataRowDelete,
			Entity: "contest", EntityID: contestID.String(),
			Payload: map[string]any{"table": table, "row": row},
		})
	}
	if g.uow != nil {
		return g.uow.Do(ctx, run)
	}
	return run(ctx)
}

// loadTableData is finishDefinitionBuild's own seam (template.go): after
// BuildTemplate has created every table and before the build is marked
// ready, this loads each table's own completed CSV, if it has one, through
// cluster.LoadTableData — the same COPY ... FROM STDIN protocol path an
// uploaded dump's own data already runs through
// (gamedb.Provisioner.runScript), not a second, row-at-a-time way in.
//
// A table with no completed file is left empty, not refused — the brief's
// own words: an organiser may have described a table and not yet filled it.
func (g *Games) loadTableData(ctx context.Context, contestID uuid.UUID, database string, definition Definition) error {
	if g.tableFiles == nil {
		return nil
	}
	for _, table := range definition.Tables {
		data, err := g.repo.ReadyTableData(ctx, contestID, table.Name)
		if errors.Is(err, ErrTableDataNotFound) {
			continue // no file for this table yet: an empty table, not a refusal
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

// tableDataCopyReader streams a table's CSV file into a COPY ... FROM STDIN
// with its header line and its tombstoned rows removed — the one filtering
// this feature does at build time, so that LoadTableData (gamedb) never has
// to know what a header or a deleted row is and can simply hand bytes to
// PostgreSQL's own COPY protocol exactly as runScript's own copyDataReader
// does for an uploaded dump's COPY blocks.
//
// One line at a time, from tableLineScanner's own bound: never the whole
// file, whatever its own size — the same rule 12 reasoning gamedb's
// copyDataReader gives for a dump.
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
		if _, err := c.scanner.next(); err != nil { // the header, always skipped
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

// sweepAbandonedTableData aborts every table-data upload still 'receiving'
// past olderThan — SweepUploads' own janitor, at the finer grain of one
// table's file. Batched the same size as the dump janitor's own, for the
// same reason (abandonedUploadBatchLimit's own doc).
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

// sweepOrphanTableFiles removes every table-data file on the volume that
// nothing needs any more — sweepOrphanFiles' own doc, for
// TableDataRepository.TableDataInUse instead of UploadInUse. Its own age
// floor (orphanFileGrace) for the identical reason: BeginTableUpload and
// AppendTableRow's own bootstrap both reserve a file before the row that
// names it exists.
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
