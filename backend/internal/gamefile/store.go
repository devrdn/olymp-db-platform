package gamefile

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// copyBufferSize is the fixed buffer a chunk is copied to disk through,
// whatever the chunk's size (CLAUDE.md rule 12).
const copyBufferSize = 64 * 1024

// maxUploadIDLen bounds the id before it becomes part of a filename
// (CLAUDE.md rule 2). A UUID is 36 characters.
const maxUploadIDLen = 128

// dataSuffix and indexSuffix name the two files one upload occupies.
const (
	dataSuffix  = ".data"
	indexSuffix = ".idx"
)

// Store is a directory of in-progress and completed uploads on local disk.
// One Store owns one directory, and two Stores must not share a directory.
type Store struct {
	dir    string
	limits Limits
	locks  *idLocks

	// buffers holds the Limits.BufferedChunks buffers chunk bodies are read
	// into, each nil until first used and then kept, and is nil for a Store
	// that streams.
	buffers chan []byte

	// mu guards reserved: upload id to the total size Begin was told it would
	// reach (committedBytes).
	mu       sync.Mutex
	reserved map[string]int64

	// dirMu guards the cached directory size; a zero dirMeasuredAt means no
	// valid measurement (dirBytes).
	dirMu         sync.Mutex
	dirTotal      int64
	dirMeasuredAt time.Time
}

// dirBytesMaxAge is how long a measurement of the directory may stand before
// Append measures again.
//
// Walking the directory costs 3.3 ms at five hundred files, so it is not done
// per chunk. Every change this process
// makes updates or clears the figure; the age only bounds how long bytes left
// by an operator or a crash go unnoticed.
const dirBytesMaxAge = 30 * time.Second

// NewStore opens (creating if necessary) dir as an upload directory governed
// by limits. Its errors are configuration errors, not client refusals, so
// they are plain errors rather than sentinels.
func NewStore(dir string, limits Limits) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("gamefile: directory is required")
	}
	if limits.MaxFileBytes <= 0 || limits.MaxDirBytes <= 0 || limits.MaxChunkBytes <= 0 || limits.BufferedChunks < 0 {
		return nil, fmt.Errorf("gamefile: the byte limits must be positive and BufferedChunks not negative, got %+v", limits)
	}
	// The directory holds unpublished contest dumps on the host contestants
	// reach: no access for "other", read for the service group (gosec G301).
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("gamefile: create upload directory: %w", err)
	}
	s := &Store{dir: dir, limits: limits, locks: newIDLocks(), reserved: map[string]int64{}}
	if limits.BufferedChunks > 0 {
		s.buffers = make(chan []byte, limits.BufferedChunks)
		for range limits.BufferedChunks {
			s.buffers <- nil
		}
	}
	return s, nil
}

// validateUploadID is the gate every exported method sends id through before
// it touches a path. The set [0-9a-fA-F-] holds neither '/' nor '.', so an id
// from an HTTP path can never escape the directory.
func validateUploadID(id string) error {
	if id == "" || len(id) > maxUploadIDLen {
		return ErrBadUploadID
	}
	for _, r := range id {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		case r == '-':
		default:
			return ErrBadUploadID
		}
	}
	return nil
}

func (s *Store) dataPath(id string) string {
	return filepath.Join(s.dir, id+dataSuffix)
}

func (s *Store) indexPath(id string) string {
	return filepath.Join(s.dir, id+indexSuffix)
}

// Begin reserves an upload of declaredBytes under the caller's id; the
// organiser's filename is never used as a path.
//
// declaredBytes is checked against the space left, so an upload that cannot
// fit is refused before any chunk is sent rather than partway through.
//
// Begin for an id that already has data is a resume: it keeps the bytes,
// restores the reservation (the process may have restarted), and is never
// refused for space, since those bytes were admitted once.
func (s *Store) Begin(id string, declaredBytes int64) error {
	if err := validateUploadID(id); err != nil {
		return err
	}
	// Refused where the promise is made (CLAUDE.md rule 12).
	if declaredBytes < 0 || declaredBytes > s.limits.MaxFileBytes {
		return ErrFileTooLarge
	}

	path := s.dataPath(id)
	if _, err := os.Stat(path); err == nil {
		s.reserve(id, declaredBytes)
		return nil // resuming an upload already begun
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("gamefile: check existing upload: %w", err)
	}

	used, err := s.committedBytes()
	if err != nil {
		return fmt.Errorf("gamefile: measure directory usage: %w", err)
	}
	if used+declaredBytes > s.limits.MaxDirBytes {
		return ErrStoreFull
	}

	// path comes from a validated id, so it cannot leave s.dir (gosec G304).
	// 0o600: an unpublished contest dump needs no second reader (gosec G302).
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- see comment above
	if err != nil {
		if os.IsExist(err) {
			// A concurrent Begin for the same id won and already reserved it.
			return nil
		}
		return fmt.Errorf("gamefile: reserve upload: %w", err)
	}
	s.reserve(id, declaredBytes)
	if err := f.Close(); err != nil {
		return fmt.Errorf("gamefile: reserve upload: %w", err)
	}
	// A caller records the file's existence next, and a host crash must not
	// leave that record naming a file the directory never kept.
	return s.syncDir()
}

func (s *Store) syncDir() error {
	d, err := os.Open(s.dir)
	if err != nil {
		return fmt.Errorf("gamefile: open upload directory: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("gamefile: sync upload directory: %w", err)
	}
	return nil
}

// reserve and release record what an upload still owes the directory.
func (s *Store) reserve(id string, declaredBytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reserved[id] = declaredBytes
}

func (s *Store) release(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.reserved, id)
}

// committedBytes is what the directory holds plus what uploads begun in this
// process have promised and not yet sent; Begin admits a new upload against
// it, so several concurrent uploads cannot each pass on the same free space.
//
// Reservations live in memory only: after a restart only bytes on disk count
// until each upload resumes and re-reserves. Append is not checked against
// reservations, so an upload is never refused its own.
func (s *Store) committedBytes() (int64, error) {
	sizes, total, err := s.usage()
	if err != nil {
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for id, declared := range s.reserved {
		// What already arrived is in total; count only the rest.
		if outstanding := declared - sizes[id]; outstanding > 0 {
			total += outstanding
		}
	}
	return total, nil
}

// usedBytes walks the directory and sums every file's size: exact, but its
// cost grows with the number of files. Hot paths use dirBytes.
func (s *Store) usedBytes() (int64, error) {
	_, total, err := s.usage()
	return total, err
}

// dirBytes is the directory's size, from the cached figure when it is younger
// than dirBytesMaxAge and measured otherwise. Append never refuses on the
// cached figure alone.
func (s *Store) dirBytes() (int64, error) {
	s.dirMu.Lock()
	if !s.dirMeasuredAt.IsZero() && time.Since(s.dirMeasuredAt) < dirBytesMaxAge {
		total := s.dirTotal
		s.dirMu.Unlock()
		return total, nil
	}
	s.dirMu.Unlock()

	return s.usedBytes()
}

// dirBytesGrew adds bytes this process just wrote to the cached figure.
func (s *Store) dirBytesGrew(delta int64) {
	s.dirMu.Lock()
	defer s.dirMu.Unlock()
	if !s.dirMeasuredAt.IsZero() {
		s.dirTotal += delta
	}
}

// dirBytesChanged drops the cached figure after a change of unknown size.
func (s *Store) dirBytesChanged() {
	s.dirMu.Lock()
	defer s.dirMu.Unlock()
	s.dirMeasuredAt = time.Time{}
}

// usage walks the directory, returning each upload's data size by id and the
// total, and refreshes the cached figure.
func (s *Store) usage() (map[string]int64, int64, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	sizes := make(map[string]int64, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue // gone between ReadDir and Info; not this call's problem
		}
		if !info.Mode().IsRegular() {
			continue
		}
		total += info.Size()
		if id, ok := strings.CutSuffix(entry.Name(), dataSuffix); ok {
			sizes[id] = info.Size()
		}
	}

	s.dirMu.Lock()
	s.dirTotal, s.dirMeasuredAt = total, time.Now()
	s.dirMu.Unlock()

	return sizes, total, nil
}

// Append writes r at offset, which must equal the bytes received so far, and
// returns the new length. A chunk already on disk (offset below the length) is
// a no-op, so a client unsure whether its last chunk landed can resend it.
// Neither that, a wrong offset, a sealed upload nor a missing one reads r.
//
// Each call opens its own handle: an upload may span process restarts.
func (s *Store) Append(id string, offset int64, r io.Reader) (int64, error) {
	if err := validateUploadID(id); err != nil {
		return 0, err
	}
	if s.buffers == nil {
		unlock := s.locks.lock(id)
		defer unlock()
		return s.appendLocked(id, offset, r)
	}

	// Decided once without the body, so a chunk that will be refused costs no
	// buffer, and again under the lock the write takes.
	received, settled, err := s.admit(id, offset)
	if settled {
		return received, err
	}

	var buf []byte
	select {
	case buf = <-s.buffers:
	default:
		return received, ErrTooManyChunks
	}
	if buf == nil {
		// One byte past MaxChunkBytes is how a body over it shows.
		buf = make([]byte, s.limits.MaxChunkBytes+1)
	}
	defer func() { s.buffers <- buf }()
	n, readErr := io.ReadFull(r, buf)
	var body io.Reader = bytes.NewReader(buf[:n])
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		body = io.MultiReader(body, failing{readErr})
	}

	unlock := s.locks.lock(id)
	defer unlock()
	return s.appendLocked(id, offset, body)
}

// failing is a reader that ends a buffered body with the error its source
// stopped on.
type failing struct{ err error }

func (f failing) Read([]byte) (int, error) { return 0, f.err }

// admit takes id's lock for long enough to decide whether a chunk at offset
// can be written at all. settled means the answer is already final.
func (s *Store) admit(id string, offset int64) (received int64, settled bool, err error) {
	unlock := s.locks.lock(id)
	defer unlock()

	f, received, err := s.openUnsealed(id)
	if err != nil {
		return received, true, err
	}
	defer f.Close()

	switch {
	case offset < received:
		return received, true, nil // a retry of a chunk already on disk
	case offset > received:
		return received, true, ErrChunkOutOfOrder
	}
	return received, false, nil
}

// appendLocked is Append under id's lock.
func (s *Store) appendLocked(id string, offset int64, r io.Reader) (int64, error) {
	f, received, err := s.openUnsealed(id)
	if err != nil {
		return received, err
	}
	defer f.Close()

	switch {
	case offset < received:
		return received, nil // a retry of a chunk already on disk
	case offset > received:
		return received, ErrChunkOutOfOrder
	}
	return s.writeAt(f, offset, r)
}

// ReplaceTail cuts the file back to from and writes r there, returning the
// new length. It is for a caller that keeps the file's length in a record of
// its own and writes while holding that record: whatever lies beyond from was
// never recorded, so it is discarded rather than taken for a retry, as Append
// would take it. r is read under the file's lock, so it must already be in
// memory. A file shorter than from is ErrShorterThanRecorded.
func (s *Store) ReplaceTail(id string, from int64, r io.Reader) (int64, error) {
	if err := validateUploadID(id); err != nil {
		return 0, err
	}

	unlock := s.locks.lock(id)
	defer unlock()

	f, received, err := s.openUnsealed(id)
	if err != nil {
		return received, err
	}
	defer f.Close()

	switch {
	case from < 0 || from > received:
		return received, fmt.Errorf("%w: upload %s holds %d bytes of %d", ErrShorterThanRecorded, id, received, from)
	case from < received:
		if err := f.Truncate(from); err != nil {
			return received, fmt.Errorf("gamefile: discard unrecorded tail: %w", err)
		}
		s.dirBytesChanged()
	}
	return s.writeAt(f, from, r)
}

// openUnsealed opens id's data for writing and reports its length. The caller
// holds id's lock. A sealed upload is ErrUploadSealed, with the length.
func (s *Store) openUnsealed(id string) (*os.File, int64, error) {
	f, err := os.OpenFile(s.dataPath(id), os.O_RDWR, 0o600)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, fmt.Errorf("gamefile: open upload: %w", err)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, fmt.Errorf("gamefile: stat upload: %w", err)
	}
	received := info.Size()

	// The index is Complete's seal. Complete holds the same lock, so it has
	// either finished or not started.
	if _, err := os.Stat(s.indexPath(id)); err == nil {
		_ = f.Close()
		return nil, received, ErrUploadSealed
	} else if !os.IsNotExist(err) {
		_ = f.Close()
		return nil, received, fmt.Errorf("gamefile: check upload seal: %w", err)
	}
	return f, received, nil
}

// writeAt writes r at offset, the file's end, within the chunk, file and
// directory budgets, and returns the new length. The bytes are synced before
// it returns: the caller is about to record the length, and a host crash must
// not leave the file shorter than that record. A write that fails or is
// refused leaves the file ending at offset.
func (s *Store) writeAt(f *os.File, offset int64, r io.Reader) (int64, error) {
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, fmt.Errorf("gamefile: seek upload: %w", err)
	}

	// The reader is bounded by the tightest of the chunk, file and directory
	// budgets before any byte is written (CLAUDE.md rule 12).
	fileRemaining := s.limits.MaxFileBytes - offset

	writeCap := s.limits.MaxChunkBytes
	reason := ErrChunkTooLarge
	if fileRemaining <= writeCap {
		writeCap = fileRemaining
		reason = ErrFileTooLarge
	}

	// The cached figure may only say "plenty of room"; when the directory
	// budget could bound this chunk, it is measured fresh.
	used, err := s.dirBytes()
	if err != nil {
		return offset, fmt.Errorf("gamefile: measure directory usage: %w", err)
	}
	if s.limits.MaxDirBytes-used <= writeCap {
		if used, err = s.usedBytes(); err != nil {
			return offset, fmt.Errorf("gamefile: measure directory usage: %w", err)
		}
	}
	if dirRemaining := s.limits.MaxDirBytes - used; dirRemaining <= writeCap {
		writeCap = dirRemaining
		reason = ErrStoreFull
	}
	if writeCap < 0 {
		// Already over budget, e.g. after Limits were lowered.
		writeCap = 0
	}

	buf := make([]byte, copyBufferSize)
	source := &chunkSource{r: io.LimitReader(r, writeCap)}
	written, err := io.CopyBuffer(f, source, buf)
	if err != nil {
		_ = f.Truncate(offset)
		if source.err != nil {
			return offset, fmt.Errorf("%w: %w", ErrChunkIncomplete, source.err)
		}
		return offset, fmt.Errorf("gamefile: write chunk: %w", err)
	}

	if written == writeCap {
		// Stopped at the cap: probe one byte to learn whether the chunk was
		// over budget.
		var probe [1]byte
		n, probeErr := r.Read(probe[:])
		switch {
		case n > 0:
			_ = f.Truncate(offset)
			return offset, reason

		case probeErr != nil && !errors.Is(probeErr, io.EOF):
			// Not the same as EOF. The transport's body limit equals
			// MaxChunkBytes, so an oversized chunk usually surfaces here as
			// "request body too large"; treating it as EOF would accept a
			// truncated chunk.
			_ = f.Truncate(offset)
			return offset, fmt.Errorf("%w: %w", ErrChunkIncomplete, probeErr)
		}
	}

	if err := f.Sync(); err != nil {
		_ = f.Truncate(offset)
		return offset, fmt.Errorf("gamefile: sync chunk: %w", err)
	}

	s.dirBytesGrew(written)
	return offset + written, nil
}

// chunkSource records whether a failed copy failed on the caller's reader
// (the client can retry: ErrChunkIncomplete) or on our disk (an outage),
// which io.Copy reports identically (CLAUDE.md rule 8).
type chunkSource struct {
	r   io.Reader
	err error
}

func (s *chunkSource) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		s.err = err
	}
	return n, err
}

// Complete seals the upload: verifies the length, returns the checksum, and
// builds the line index.
//
// The checksum is computed in the same pass that builds the index, not across
// Append calls: the file must be read whole for the index anyway, and hash
// state carried between chunks would have to survive restarts and retries.
func (s *Store) Complete(id string, declaredBytes int64) (Summary, error) {
	if err := validateUploadID(id); err != nil {
		return Summary{}, err
	}

	// Held for the whole scan, so no Append lands before the seal appears.
	unlock := s.locks.lock(id)
	defer unlock()

	f, err := os.Open(s.dataPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return Summary{}, ErrNotFound
		}
		return Summary{}, fmt.Errorf("gamefile: open upload: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return Summary{}, fmt.Errorf("gamefile: stat upload: %w", err)
	}
	if info.Size() != declaredBytes {
		return Summary{}, fmt.Errorf("%w: received %d, declared %d", ErrLengthMismatch, info.Size(), declaredBytes)
	}

	built, sum, err := buildIndex(f)
	if err != nil {
		return Summary{}, fmt.Errorf("gamefile: index upload: %w", err)
	}
	if built.totalBytes != declaredBytes {
		// The file changed between the Stat and the scan.
		return Summary{}, fmt.Errorf("%w: received %d, declared %d", ErrLengthMismatch, built.totalBytes, declaredBytes)
	}

	if err := writeIndex(s.indexPath(id), built); err != nil {
		return Summary{}, fmt.Errorf("gamefile: save index: %w", err)
	}
	s.dirBytesChanged()
	s.release(id) // everything promised is now on disk

	return Summary{
		Bytes:  built.totalBytes,
		SHA256: hex.EncodeToString(sum[:]),
		Lines:  built.totalLines,
	}, nil
}

// Abort removes an upload's data and any index it had, and forgets it.
func (s *Store) Abort(id string) error {
	if err := validateUploadID(id); err != nil {
		return err
	}

	unlock := s.locks.lock(id)
	defer unlock()

	if _, err := os.Stat(s.dataPath(id)); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("gamefile: stat upload: %w", err)
	}

	if err := os.Remove(s.indexPath(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("gamefile: remove index: %w", err)
	}
	// Abort is the last chance to reclaim a killed Complete's temporaries.
	if err := clearIndexTemps(s.indexPath(id)); err != nil {
		return err
	}
	if err := os.Remove(s.dataPath(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("gamefile: remove upload: %w", err)
	}
	s.dirBytesChanged()
	s.release(id)
	return nil
}

// BuffersChunks reports whether Append reads a chunk into memory before
// taking its file's lock (Limits.BufferedChunks), which a caller that writes
// with ReplaceTail from inside a transaction requires.
func (s *Store) BuffersChunks() bool { return s.buffers != nil }

// UploadIDs lists the id of every upload the Store holds, in progress or
// completed, in no order, so a janitor can reconcile the volume without
// knowing the on-disk layout. Each upload appears once.
//
// Only uploads whose data was last written before modifiedBefore are listed:
// the caller creates a file and records it in two steps, and a fresh upload
// must not look orphaned in between. A zero Time lists nothing; pass a future
// time to list everything.
func (s *Store) UploadIDs(modifiedBefore time.Time) ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("gamefile: list uploads: %w", err)
	}

	var ids []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		id, ok := strings.CutSuffix(entry.Name(), dataSuffix)
		if !ok {
			continue // the index file, or anything else that is not one upload's data
		}
		if validateUploadID(id) != nil {
			continue // not a name this Store could have produced itself
		}
		info, err := entry.Info()
		if err != nil {
			continue // gone between ReadDir and Info
		}
		if !info.ModTime().Before(modifiedBefore) {
			continue // younger than the caller's cut-off
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// Open returns the upload's data file for streaming; the caller closes it.
func (s *Store) Open(id string) (*os.File, error) {
	if err := validateUploadID(id); err != nil {
		return nil, err
	}

	f, err := os.Open(s.dataPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("gamefile: open upload: %w", err)
	}
	return f, nil
}
