package gamefile

import (
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

// copyBufferSize is the fixed window Append copies a chunk through. Its size
// is independent of the chunk's or the file's size — that independence is
// the whole point of rule 12: a caller who sends a four-gigabyte chunk moves
// this many bytes through the process at a time, never more.
const copyBufferSize = 64 * 1024

// maxUploadIDLen bounds the id before it becomes half a filename. A UUID is
// 36 characters; this leaves headroom without leaving the bound unstated
// (CLAUDE.md rule 2 — every string that reaches storage has an explicit
// length).
const maxUploadIDLen = 128

// dataSuffix and indexSuffix name the two files one upload occupies. Neither
// is ever derived from anything but the id — see validateUploadID.
const (
	dataSuffix  = ".data"
	indexSuffix = ".idx"
)

// Store is a directory of in-progress and completed uploads on local disk.
// One Store owns one directory; nothing about it is safe to share between
// two directories, and nothing about a directory is safe to share between
// two Stores that disagree on Limits.
type Store struct {
	dir    string
	limits Limits
	// locks serialises Append, Complete and Abort per upload id. See
	// idLocks in lock.go for why this exists and why it is per-id rather
	// than one mutex for the whole Store.
	locks *idLocks

	// mu guards reserved, which maps an upload id to the total size Begin
	// was told it would reach. See committedBytes for what it buys and
	// what it deliberately does not.
	mu       sync.Mutex
	reserved map[string]int64
}

// NewStore opens (creating if necessary) dir as an upload directory governed
// by limits. This is a constructor-time configuration error, not a domain
// refusal that reaches a client, so it returns a plain error rather than a
// sentinel (CLAUDE.md rule 1 is about errors a service hands to the HTTP
// layer; nothing here does).
func NewStore(dir string, limits Limits) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("gamefile: directory is required")
	}
	if limits.MaxFileBytes <= 0 || limits.MaxDirBytes <= 0 || limits.MaxChunkBytes <= 0 {
		return nil, fmt.Errorf("gamefile: limits must all be positive, got %+v", limits)
	}
	// 0o750, not the 0o755 os.MkdirAll's own default-shaped call would
	// suggest: this directory holds an organiser's raw contest dump before
	// anyone has had a chance to look at it, on the same host that serves
	// the API to every contestant. "Other" gets nothing; "group" keeps read
	// and traversal so an operator in the deployment's own service group can
	// inspect the volume without needing to become the API's own user
	// (gosec G301 wants 0o750 or stricter, which is also the bound this
	// deployment's other MkdirAll calls already use — see cmd/apicontract).
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("gamefile: create upload directory: %w", err)
	}
	return &Store{dir: dir, limits: limits, locks: newIDLocks(), reserved: map[string]int64{}}, nil
}

// validateUploadID is the one gate every exported method sends id through
// before it touches a path. The allowed set is exactly [0-9a-fA-F-]: not
// because ids happen to be UUIDs today, but because that set contains
// neither '/' nor '.', which is what makes "id becomes half a filename"
// safe regardless of what a caller passes through from an HTTP path
// segment. ".." is rejected by the same charset check, not a special case.
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

// Begin reserves an upload of declaredBytes. id is the caller's (a UUID
// string); the store never invents one, and never uses the human's filename
// as a path.
//
// declaredBytes is what the whole upload will come to, and it is checked
// against the space actually left rather than only against "is there any
// space at all". The difference is what the organiser finds out and when: at
// 15 GiB used of 16, the old check let a three-gigabyte upload start
// happily, cut it off at whatever byte the directory budget ran out on, and
// answered ErrStoreFull somewhere around the 129th chunk — after however
// many minutes an office uplink takes to send the gigabyte that did fit, and
// with those bytes already on disk waiting for the janitor. Refusing the
// promise costs one comparison and is the same answer, given before anything
// was sent.
//
// Calling Begin again for an id that already has data on disk is not an
// error: it is how a resumed upload re-announces itself after a dropped
// connection, and it must not truncate what was already received. A resume
// re-states the reservation (the process may have restarted since the first
// Begin) but is never refused for space — the bytes it is coming back for
// were already admitted once, and refusing them now would only strand what
// is on disk.
func (s *Store) Begin(id string, declaredBytes int64) error {
	if err := validateUploadID(id); err != nil {
		return err
	}
	// The same bound Append applies per chunk, applied to the promise: a
	// declared size past MaxFileBytes cannot be honoured, so it is refused
	// where it is made rather than at whichever chunk crosses the line
	// (CLAUDE.md rule 12).
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

	// path is dataPath(id): id has already passed validateUploadID's
	// charset gate above ([0-9a-fA-F-], neither '/' nor '.'), so this join
	// can never escape s.dir or name anything but one file directly inside
	// it — there is no path segment here for a caller to control (gosec
	// G304 flags any variable reaching OpenFile; this one cannot vary
	// outside the id's own validated charset).
	//
	// 0o600, not 0o644: the same contest dump this file will hold is what
	// store.go's NewStore doc already treats as unpublished until the
	// contest starts, so "other" (and here, "group" too — nothing about the
	// deployment needs a second reader of an in-progress upload) get
	// nothing (gosec G302).
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- see comment above
	if err != nil {
		if os.IsExist(err) {
			// Lost a race with another Begin for the same id — which already
			// reserved it, with the same declared size, so there is nothing
			// to add.
			return nil
		}
		return fmt.Errorf("gamefile: reserve upload: %w", err)
	}
	s.reserve(id, declaredBytes)
	return f.Close()
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

// committedBytes is what the directory holds plus what the uploads begun in
// this process have promised and not yet sent — the number Begin admits a new
// upload against.
//
// Without it the directory budget is only ever spent by bytes that already
// arrived, so three organisers each announcing four gibibytes onto a volume
// with ten free all pass Begin and all three fail somewhere in the middle:
// the check is real for one caller at a time and empty for several. Counting
// the promise is what makes the refusal arrive before the upload does.
//
// Two things it deliberately is not. It is not durable: the map lives in this
// process, so a restart forgets what was outstanding and the guarantee falls
// back to what it was before — the actual bytes on disk, which are still
// counted, plus a reservation restored by the first resume of each upload
// (Begin's own doc). Making it durable means a third file per upload, or the
// core database — and the core database is on the other side of this
// package's boundary by design. And it is not applied to Append: a chunk is
// admitted against what the disk holds now, exactly as before, because an
// upload must never be refused its own reservation.
func (s *Store) committedBytes() (int64, error) {
	sizes, total, err := s.usage()
	if err != nil {
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for id, declared := range s.reserved {
		// Only what has not arrived yet: the rest is already in total, and
		// counting it twice would refuse an upload the volume can hold.
		if outstanding := declared - sizes[id]; outstanding > 0 {
			total += outstanding
		}
	}
	return total, nil
}

// usedBytes sums the size of every file this Store's directory currently
// holds. This walks directory metadata, not file content — its cost is
// proportional to the number of uploads in flight, never to their size, so
// it does not reopen the rule 12 question Append and Complete answer.
func (s *Store) usedBytes() (int64, error) {
	_, total, err := s.usage()
	return total, err
}

// usage is the same walk, also reporting how large each upload's data file
// is by id — what committedBytes needs to tell an outstanding reservation
// from bytes that have already landed.
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
	return sizes, total, nil
}

// Append writes at exactly offset. Returns the new length.
//
// Every call opens and closes its own file handle rather than keeping one in
// a map on Store: a chunked upload is expected to span more than one
// process lifetime (a redeploy between chunks must not lose progress), so
// there is nothing for an in-memory handle to usefully outlive. The per-id
// lock taken below is a different thing — held only for this one call, not
// kept across calls — so it does not reopen that question.
func (s *Store) Append(id string, offset int64, r io.Reader) (int64, error) {
	if err := validateUploadID(id); err != nil {
		return 0, err
	}

	// Two goroutines can legitimately be in this method for the same id at
	// once — a retried chunk racing the attempt that is still in flight —
	// and without this, both would Stat the same length, Seek to the same
	// offset, and overwrite each other's bytes. This is what makes "Append
	// writes exactly at the end" a Store invariant instead of a hope about
	// callers (see idLocks in lock.go).
	unlock := s.locks.lock(id)
	defer unlock()

	// 0o600 for the same reason Begin creates the file that way: this is the
	// same not-yet-public contest dump, opened again to append the next
	// chunk (gosec G302). id is validated above, same as Begin's own
	// OpenFile — no #nosec needed here, gosec only flagged the permission
	// bits on this call, not the path.
	f, err := os.OpenFile(s.dataPath(id), os.O_RDWR, 0o600)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("gamefile: open upload: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("gamefile: stat upload: %w", err)
	}
	received := info.Size()

	// Complete seals the upload by writing its index; an Append that lands
	// after that would silently make the checksum and line count Complete
	// already handed back describe bytes that no longer exist. Complete
	// takes the same per-id lock as this method, so there is no window
	// where Complete is mid-write and this check could pass just before the
	// index appears — either Complete has already finished, or it has not
	// started.
	if _, err := os.Stat(s.indexPath(id)); err == nil {
		return received, ErrUploadSealed
	} else if !os.IsNotExist(err) {
		return received, fmt.Errorf("gamefile: check upload seal: %w", err)
	}

	switch {
	case offset < received:
		// A retry of a chunk already on disk. Idempotency here is what
		// makes resuming after a dropped connection possible at all: the
		// client cannot tell whether its last chunk was written before the
		// connection died, so it will send it again, and that must be a
		// no-op rather than a corruption.
		return received, nil
	case offset > received:
		return received, ErrChunkOutOfOrder
	}

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return received, fmt.Errorf("gamefile: seek upload: %w", err)
	}

	// Bound the reader by whichever budget is tightest, decided before a
	// single byte of this chunk reaches the file: the per-call chunk cap,
	// what remains of this upload's own MaxFileBytes, and what remains of
	// the whole directory's MaxDirBytes. This is rule 12's point applied to
	// three limits instead of one — a limit checked after the bytes already
	// landed on disk (write, then compare, then Truncate) is a limit
	// applied after the allocation it exists to prevent, not before it.
	//
	// fileRemaining cannot be negative: every prior Append on this id kept
	// received within MaxFileBytes, or refused before writing.
	fileRemaining := s.limits.MaxFileBytes - received

	// The directory holds a handful of files, not gigabytes of them, so
	// walking it once per Append call (not per copy-buffer, and not more
	// often than the file-size check next to it) is cheap — see usedBytes.
	used, err := s.usedBytes()
	if err != nil {
		return received, fmt.Errorf("gamefile: measure directory usage: %w", err)
	}
	dirRemaining := s.limits.MaxDirBytes - used

	writeCap := s.limits.MaxChunkBytes
	reason := ErrChunkTooLarge
	if fileRemaining <= writeCap {
		writeCap = fileRemaining
		reason = ErrFileTooLarge
	}
	if dirRemaining <= writeCap {
		writeCap = dirRemaining
		reason = ErrStoreFull
	}
	if writeCap < 0 {
		// Only possible if the directory (or this file) was already over
		// budget before this call — e.g. a smaller Limits was applied to an
		// existing directory. Accept nothing rather than turn that into a
		// negative LimitReader.
		writeCap = 0
	}

	buf := make([]byte, copyBufferSize)
	source := &chunkSource{r: io.LimitReader(r, writeCap)}
	written, err := io.CopyBuffer(f, source, buf)
	if err != nil {
		_ = f.Truncate(offset)
		if source.err != nil {
			// The caller's own body stopped arriving. Named, because the
			// client can act on it — see ErrChunkIncomplete.
			return offset, fmt.Errorf("%w: %w", ErrChunkIncomplete, source.err)
		}
		return offset, fmt.Errorf("gamefile: write chunk: %w", err)
	}

	if written == writeCap {
		// The limited reader stopped at exactly the tightest cap; find out
		// whether the caller's reader had more to give, without reading any
		// of it into memory — let alone onto disk — beyond this one byte.
		// This is what lets the right sentinel be reported (chunk, file, or
		// directory, whichever was tightest) without ever holding, or
		// writing, an over-budget chunk anywhere.
		var probe [1]byte
		n, probeErr := r.Read(probe[:])
		switch {
		case n > 0:
			_ = f.Truncate(offset)
			return offset, reason

		case probeErr != nil && !errors.Is(probeErr, io.EOF):
			// The reader could not answer the question, which is not the
			// same answer as "there was nothing more" — and discarding this
			// error is how a refused chunk became a success. The deployment
			// makes it the ordinary case rather than an exotic one: the
			// transport's ceiling and MaxChunkBytes are deliberately the
			// same number, so a caller sending one byte too many is stopped
			// by the socket at exactly the byte this cap stopped at, the
			// probe reads (0, "request body too large"), and reading that as
			// EOF answered 200 OK to a request that had been refused —
			// keeping what fitted and silently dropping the rest.
			_ = f.Truncate(offset)
			return offset, fmt.Errorf("%w: %w", ErrChunkIncomplete, probeErr)
		}
	}

	return offset + written, nil
}

// chunkSource remembers whether the failure that ended a copy came from the
// caller's reader or from this process's own disk. io.Copy reports the two
// identically, and they are not the same fact (CLAUDE.md rule 8): an
// interrupted body is the client's to retry and is named as such, while a
// write that failed is an outage of ours and must not be dressed up as
// something the browser can fix by sending the chunk again.
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
// The checksum is computed here, in the single sequential pass Complete
// already has to make to build the line index — not incrementally across
// Append calls. Carrying a sha256.Hash's state between chunks is possible
// (hash.Hash implements encoding.BinaryMarshaler) but it means persisting
// that state to disk after every Append, restoring it correctly on the
// offset<received idempotent-skip path, and getting all of that right across
// a process restart mid-upload — real complexity bought for a saving that
// does not exist, because Complete has to read every byte anyway to build
// the index. One sequential local-disk read of a file that was just written
// to the same disk is the cheap operation here; a second read of the whole
// upload never happens.
func (s *Store) Complete(id string, declaredBytes int64) (Summary, error) {
	if err := validateUploadID(id); err != nil {
		return Summary{}, err
	}

	// Same lock Append takes: holding it for the whole scan means an Append
	// racing this call either finished before Complete started, or has not
	// started yet by the time the index (Complete's seal, checked by
	// Append) appears on disk.
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
		// buildIndex read the file independently of the Stat above; a
		// mismatch here means the file changed underneath us mid-read,
		// which is a caller bug (nothing else writes to this path) but not
		// one this package should paper over with a stale-looking summary.
		return Summary{}, fmt.Errorf("%w: received %d, declared %d", ErrLengthMismatch, built.totalBytes, declaredBytes)
	}

	if err := writeIndex(s.indexPath(id), built); err != nil {
		return Summary{}, fmt.Errorf("gamefile: save index: %w", err)
	}

	// Everything this upload promised is now on disk and counted there, so
	// the reservation has nothing left to hold (committedBytes' own doc).
	s.release(id)

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

	// Same lock Append and Complete take, so an Abort cannot remove the
	// data file out from under either while they are mid-call.
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
	// And whatever a Complete killed mid-write left beside it: nothing else
	// on this volume can ever name those again (clearIndexTemps' own doc), so
	// retiring the upload is the last chance to reclaim their bytes.
	if err := clearIndexTemps(s.indexPath(id)); err != nil {
		return err
	}
	if err := os.Remove(s.dataPath(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("gamefile: remove upload: %w", err)
	}
	// The bytes are gone and so is the promise of the ones that never came —
	// this is what keeps an abandoned upload the janitor cleared from holding
	// its share of the directory budget until the process restarts.
	s.release(id)
	return nil
}

// Received reports how many bytes of id have landed on disk so far.
func (s *Store) Received(id string) (int64, error) {
	if err := validateUploadID(id); err != nil {
		return 0, err
	}

	info, err := os.Stat(s.dataPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("gamefile: stat upload: %w", err)
	}
	return info.Size(), nil
}

// UploadIDs lists the id of every upload this Store currently holds —
// in-progress or completed, in no particular order. It exists so a caller
// that needs to reconcile the volume against its own bookkeeping (the
// provisioning package's orphan-file janitor is the one today) asks the
// Store rather than walking the directory itself: the on-disk layout — one
// data file plus, once sealed, one side index — is this package's own
// convention, not something a caller should reverse-engineer by pattern
// matching file names (see dataSuffix/indexSuffix above).
//
// The result names uploads, never paths or filenames — a caller gets the
// same id it would pass to Received, Open or Abort, nothing that leaks how
// this Store lays out its directory. Each upload appears exactly once
// regardless of how many files on disk belong to it: only the data file
// (the one file that exists for every upload, in progress or sealed) is
// counted, so a completed upload's index file is never mistaken for a
// second upload.
//
// modifiedBefore is a floor on the age of what is listed: an upload whose
// data file was written at or after it is left out. Reconciling a volume
// against somebody else's bookkeeping is inherently a race — the caller
// creates the file and records it in two steps, whichever order it picks —
// and a listing with no age at all hands the janitor the reservation of an
// upload whose row is at that instant still being inserted. The cut-off is
// what makes that window a matter of time rather than of luck.
//
// It is a floor and nothing else, so there is no "list everything" value: a
// zero Time lists nothing at all, since no file's modification time is before
// it. A caller (a test, in practice) that wants every id passes a cut-off in
// the future.
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
			// Gone between ReadDir and Info. Not this call's problem, and the
			// same answer usedBytes gives: nothing to list and nothing to
			// report — a caller told about an id whose file has already
			// disappeared would only be sent to remove it again.
			continue
		}
		if !info.ModTime().Before(modifiedBefore) {
			continue // younger than the caller's cut-off
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// Open returns the upload's data file for the build step to stream from —
// it is the caller's to Close, and this package never reads it as a whole
// itself.
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
