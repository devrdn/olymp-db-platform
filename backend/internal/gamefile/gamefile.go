// Package gamefile stores an organiser's uploaded SQL dump on the API host's
// disk while it is received in chunks, and reads bounded windows of its lines
// afterwards. It knows one local directory and nothing else: HTTP routes,
// database rows and template builds belong to its callers.
//
// Memory is bounded where the bytes arrive (CLAUDE.md rule 12): no upload is
// ever held in memory whole.
package gamefile

import "errors"

// Limits bounds what one Store will accept. All three are required; the
// caller decides the numbers, this package only enforces them.
type Limits struct {
	// MaxFileBytes bounds one upload's total size.
	MaxFileBytes int64
	// MaxDirBytes bounds everything the directory holds together.
	MaxDirBytes int64
	// MaxChunkBytes bounds one Append.
	MaxChunkBytes int64
}

// Summary is what Complete hands back once an upload is sealed.
type Summary struct {
	Bytes  int64
	SHA256 string // lowercase hex
	Lines  int64
}

// Window is one slice of a completed upload's lines.
type Window struct {
	// FromLine is the 1-based line number of Lines[0], or, when Lines is
	// empty, the clamped line the caller asked to start from.
	FromLine   int
	Lines      []string
	TotalLines int64
	// Truncated says the byte budget stopped the window before maxLines was
	// reached, possibly in the middle of one long line.
	Truncated bool
}

// Every refusal this package can hand to an HTTP layer (CLAUDE.md rule 1).
var (
	// ErrBadUploadID is an id that could not safely become a filename: empty,
	// too long, or carrying anything but [0-9a-fA-F-]. The id comes from a
	// request path, so this keeps a request from naming a file outside the
	// upload directory.
	ErrBadUploadID = errors.New("upload id is not valid")

	// ErrNotFound is an id with no Begin recorded, or one that was aborted.
	ErrNotFound = errors.New("upload not found")

	// ErrChunkOutOfOrder is an Append whose offset is past what has been
	// received.
	ErrChunkOutOfOrder = errors.New("chunk offset does not match what has been received")

	// ErrChunkTooLarge is one Append call carrying more than MaxChunkBytes.
	ErrChunkTooLarge = errors.New("chunk exceeds the maximum chunk size")

	// ErrFileTooLarge is an upload whose total would exceed MaxFileBytes.
	ErrFileTooLarge = errors.New("upload exceeds the maximum file size")

	// ErrStoreFull is a new upload arriving when the directory already
	// holds MaxDirBytes worth of other uploads.
	ErrStoreFull = errors.New("upload directory is full")

	// ErrLengthMismatch is Complete being told a declared length that does
	// not match what actually landed on disk.
	ErrLengthMismatch = errors.New("received bytes do not match the declared length")

	// ErrIncomplete is Window (or anything else that needs the line index)
	// called before Complete has built one.
	ErrIncomplete = errors.New("upload has not been completed")

	// ErrChunkIncomplete is a chunk whose body stopped before the store had
	// all of it (dropped connection, read deadline, transport cap). Append
	// truncates back to its starting offset, so the client resends the same
	// chunk. It wraps the cause, so the HTTP layer can still recognise its
	// own errors such as http.MaxBytesError.
	ErrChunkIncomplete = errors.New("the chunk body was not received in full")

	// ErrCorruptIndex is a line index whose header or marks fail a sanity
	// check: a value out of int64 range, or a mark past the declared data
	// length. The index is a file on disk that can be damaged or replaced, so
	// Window never seeks to an offset it has not checked.
	ErrCorruptIndex = errors.New("upload index file is corrupt")

	// ErrWindowUnreachable is a Window whose first line lies further past the
	// nearest index mark than maxWindowSkipBytes allows a call to read. Lines
	// have no length limit, so the walk from a mark is bounded instead
	// (CLAUDE.md rule 12).
	ErrWindowUnreachable = errors.New("the requested line is too far past the nearest index mark to reach")

	// ErrUploadSealed is an Append after Complete. Complete's checksum and
	// line index describe the file as it was, so a later write would make
	// them silently wrong.
	ErrUploadSealed = errors.New("upload has already been completed")
)
