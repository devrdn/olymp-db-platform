package provisioning

import "sync"

// tableRowMarkInterval is how often a table's file records the byte offset of
// a row: every 500th data row, so a page anywhere in the file is a seek plus a
// walk of at most 499 rows rather than a walk from the beginning.
//
// A table's file is bounded at MaxTableDataRows rows, so this is at most 400
// offsets per file however large the file's bytes are — a few kilobytes beside
// a file the same package expects to hold megabytes.
const tableRowMarkInterval = 500

// maxTableRowMarkFiles bounds how many files' marks are kept at once. One
// entry per table an organiser is currently paging through, which is one; the
// ceiling exists because the key is a file id from a row an organiser writes,
// and a map fed from stored data with no ceiling is a leak rather than a
// cache. Past it the whole map is dropped rather than one entry chosen, for
// the reason templateSizes gives for the same choice: the cost of being wrong
// is one page read the slow way.
const maxTableRowMarkFiles = 64

// tableRowIndex is the marks kept for each table-data file, across requests.
//
// Why this is not gamefile's own persisted line index — the one an uploaded
// dump's preview pages through — is the thing TableDataWindow's own doc
// already says: that index is written once, when Complete seals the upload,
// and a table's file is written to again by every AppendTableRow after it.
// This is the other half of that answer rather than a contradiction of it:
// nothing is persisted, nothing is sealed, and a mark is only ever *added*.
//
// What makes that sound is that a table's file is append-only under its own
// id. AppendTableRow writes at the end; DeleteTableRow does not touch the file
// at all (it tombstones a row number in the core database); and a file that is
// replaced is replaced by an upload with a new id, the old one retired from
// disk. So a byte offset recorded for a row can never come to name a different
// row — it can only stop existing, along with the file, and then nothing asks
// for it again.
type tableRowIndex struct {
	mu      sync.Mutex
	byFile  map[string]*tableRowMarks
	entries int
}

// tableRowMarks is one file's offsets.
type tableRowMarks struct {
	mu sync.Mutex
	// offsets[i] is the byte offset at which data row i*tableRowMarkInterval+1
	// begins. offsets[0] is therefore the first byte after the header line, and
	// a file with no marks at all has not been read even that far.
	offsets []int64
	// size is how large the file was when the last mark was added. A file that
	// has since shrunk is not the file these offsets were taken from, and the
	// marks are dropped rather than trusted — append-only is an argument about
	// this package's own callers, and this is what makes it a check.
	size int64
}

// of returns the marks kept for a file, creating an empty set the first time.
func (x *tableRowIndex) of(id string) *tableRowMarks {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.byFile == nil || x.entries >= maxTableRowMarkFiles {
		x.byFile, x.entries = make(map[string]*tableRowMarks), 0
	}
	marks, ok := x.byFile[id]
	if !ok {
		marks = &tableRowMarks{}
		x.byFile[id] = marks
		x.entries++
	}
	return marks
}

// nearest is where a walk towards row may start: the byte offset of the
// closest recorded mark at or before it, and the number of data rows that lie
// before that offset. ok is false when there is no usable mark — the file has
// never been paged through, or it is not the file the marks were taken from —
// and the caller starts from the top, header and all.
//
// size is the file's current length, which is what makes the second of those
// checkable rather than assumed.
func (m *tableRowMarks) nearest(row, size int64) (offset, rowsBefore int64, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if size < m.size {
		m.offsets, m.size = nil, 0
	}
	if len(m.offsets) == 0 {
		return 0, 0, false
	}

	index := (row - 1) / tableRowMarkInterval
	if index >= int64(len(m.offsets)) {
		index = int64(len(m.offsets)) - 1
	}
	return m.offsets[index], index * tableRowMarkInterval, true
}

// record keeps the offset of the row that begins mark number index, if that is
// the next mark this file is missing. Anything else — a mark already held, or
// one past the end of what has been walked — is dropped: the offsets have to
// stay a dense run from the start of the file, because nearest indexes them by
// arithmetic rather than by search.
func (m *tableRowMarks) record(index, offset, size int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if index != int64(len(m.offsets)) {
		return
	}
	m.offsets = append(m.offsets, offset)
	m.size = size
}
