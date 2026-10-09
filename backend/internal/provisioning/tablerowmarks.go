package provisioning

import "sync"

// tableRowMarkInterval is how often the byte offset of a data row is recorded,
// so any page is a seek plus a walk of at most 499 rows. MaxTableDataRows
// bounds this to 400 offsets per file.
const tableRowMarkInterval = 500

// maxTableRowMarkFiles bounds how many files' marks are kept, since the keys
// come from stored data. Past it the whole map is dropped; the cost is one
// slow page read.
const maxTableRowMarkFiles = 64

// tableRowIndex holds the row marks for each table-data file, in memory and
// across requests. Marks are only added, which is sound because a table's file
// is append-only under its id: rows are appended, deletes are tombstones in
// the database, and a replaced file gets a new id. A recorded offset can
// therefore never come to name a different row.
type tableRowIndex struct {
	mu      sync.Mutex
	byFile  map[string]*tableRowMarks
	entries int
}

// tableRowMarks is one file's offsets.
type tableRowMarks struct {
	mu sync.Mutex
	// offsets[i] is the byte offset at which data row i*tableRowMarkInterval+1
	// begins; offsets[0] is the first byte after the header.
	offsets []int64
	// size is the file's length when the last mark was added. If the file has
	// since shrunk, the marks are dropped: this checks the append-only
	// assumption rather than trusting it.
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

// nearest returns the offset of the closest mark at or before row and the
// number of data rows before it. ok is false when there is no usable mark,
// including when size (the file's current length) shows the file shrank.
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

// record keeps the offset for mark number index only if it is the next one
// missing. The offsets must stay dense because nearest indexes them by
// arithmetic.
func (m *tableRowMarks) record(index, offset, size int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if index != int64(len(m.offsets)) {
		return
	}
	m.offsets = append(m.offsets, offset)
	m.size = size
}
