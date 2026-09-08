package gamefile

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("gamefile: create upload directory: %w", err)
	}
	return &Store{dir: dir, limits: limits}, nil
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

// Begin reserves an upload. id is the caller's (a UUID string); the store
// never invents one, and never uses the human's filename as a path.
//
// Calling Begin again for an id that already has data on disk is not an
// error: it is how a resumed upload re-announces itself after a dropped
// connection, and it must not truncate what was already received. Only a
// genuinely new id is checked against MaxDirBytes — a resume adds no new
// reservation, so it is not what that limit is protecting against.
func (s *Store) Begin(id string) error {
	if err := validateUploadID(id); err != nil {
		return err
	}

	path := s.dataPath(id)
	if _, err := os.Stat(path); err == nil {
		return nil // resuming an upload already begun
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("gamefile: check existing upload: %w", err)
	}

	used, err := s.usedBytes()
	if err != nil {
		return fmt.Errorf("gamefile: measure directory usage: %w", err)
	}
	if used >= s.limits.MaxDirBytes {
		return ErrStoreFull
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil // lost a race with another Begin for the same id
		}
		return fmt.Errorf("gamefile: reserve upload: %w", err)
	}
	return f.Close()
}

// usedBytes sums the size of every file this Store's directory currently
// holds. This walks directory metadata, not file content — its cost is
// proportional to the number of uploads in flight, never to their size, so
// it does not reopen the rule 12 question Append and Complete answer.
func (s *Store) usedBytes() (int64, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue // gone between ReadDir and Info; not this call's problem
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
	}
	return total, nil
}

// Append writes at exactly offset. Returns the new length.
//
// Every call opens and closes its own file handle rather than keeping one in
// a map on Store: a chunked upload is expected to span more than one
// process lifetime (a redeploy between chunks must not lose progress), so
// there is nothing for an in-memory handle to usefully outlive.
func (s *Store) Append(id string, offset int64, r io.Reader) (int64, error) {
	if err := validateUploadID(id); err != nil {
		return 0, err
	}

	f, err := os.OpenFile(s.dataPath(id), os.O_RDWR, 0o644)
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

	buf := make([]byte, copyBufferSize)
	limited := io.LimitReader(r, s.limits.MaxChunkBytes)
	written, err := io.CopyBuffer(f, limited, buf)
	if err != nil {
		_ = f.Truncate(offset)
		return offset, fmt.Errorf("gamefile: write chunk: %w", err)
	}

	if written == s.limits.MaxChunkBytes {
		// The limited reader stopped at exactly the cap; find out whether
		// the caller's reader had more to give without reading any of it
		// into memory beyond this one byte. This is the check that lets
		// ErrChunkTooLarge be reported without ever holding an oversized
		// chunk anywhere.
		var probe [1]byte
		n, _ := r.Read(probe[:])
		if n > 0 {
			_ = f.Truncate(offset)
			return offset, ErrChunkTooLarge
		}
	}

	newLength := offset + written
	if newLength > s.limits.MaxFileBytes {
		_ = f.Truncate(offset)
		return offset, ErrFileTooLarge
	}

	return newLength, nil
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

	if _, err := os.Stat(s.dataPath(id)); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("gamefile: stat upload: %w", err)
	}

	if err := os.Remove(s.indexPath(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("gamefile: remove index: %w", err)
	}
	if err := os.Remove(s.dataPath(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("gamefile: remove upload: %w", err)
	}
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
