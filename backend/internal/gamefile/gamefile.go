// Package gamefile stores an organiser's uploaded SQL dump on the API host's
// disk while it is being received, and gives a bounded way to look at it
// afterwards.
//
// It answers: where do the bytes of one chunked upload live, how does a
// retried chunk get deduplicated, and how does a console page show "lines
// 1,200,000-1,200,200" of a multi-gigabyte file without paging in the whole
// thing. It deliberately does not answer: which HTTP route accepts a chunk,
// which Postgres table remembers that an upload exists, or how the finished
// dump becomes a contest template. Those are the callers of this package, not
// part of it — this package knows nothing beyond one local directory.
//
// CLAUDE.md rule 12 is the constraint that shapes every function here: memory
// is bounded where the bytes arrive, not after they have already been
// allocated. Nothing in this package holds a whole upload in memory. Append
// copies through a fixed-size buffer. Complete builds the line index in one
// sequential pass with a fixed-size scan buffer. Window seeks to the nearest
// index mark and reads forward through a fixed-size buffer, bounded by the
// caller's own byte budget. A multi-gigabyte dump never becomes a []byte or an
// io.ReadAll call in this package, by construction rather than by discipline.
//
// Limits carries every size bound this package enforces. It invents none of
// its own — a default here would be a guess this package has no basis for,
// so the caller (which knows the deployment's disk and the contest's dump
// sizes) always supplies them.
//
// Concurrency: Store does not serialise calls that share an upload id. A
// chunked upload is inherently sequential — the browser holds one chunk in
// flight per upload and waits for the response before sending the next — so
// the natural caller (one HTTP request at a time per id) already provides
// that ordering. Two goroutines racing Append calls for the same id, or an
// Append racing an Abort for the same id, is a caller bug, not a case this
// package guards against.
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

// Window is one slice of a completed upload's lines, read from disk on
// demand rather than held anywhere between calls.
type Window struct {
	// FromLine is the 1-based line number of Lines[0] — or, when Lines is
	// empty, the (clamped) line number the caller asked to start from.
	FromLine   int
	Lines      []string
	TotalLines int64
	// Truncated says the byte budget stopped the window before maxLines was
	// reached — including in the middle of one very long line.
	Truncated bool
}

// Sentinels for every refusal this package can hand to an HTTP layer
// (CLAUDE.md rule 1). None of them is a bare errors.New at the call site —
// each is named here so a handler's fail switch can map it to a status code
// instead of collapsing every reason into "internal error".
var (
	// ErrBadUploadID is an id that could not safely become a filename: it
	// carries something other than [0-9a-fA-F-], is empty, or is
	// implausibly long. The id becomes part of a path on disk and arrives
	// from an HTTP request path, so this is the boundary that keeps a
	// request from ever naming a file outside the upload directory.
	ErrBadUploadID = errors.New("upload id is not valid")

	// ErrNotFound is an id nothing in the store currently knows: no Begin
	// was recorded for it, or it was Aborted.
	ErrNotFound = errors.New("upload not found")

	// ErrChunkOutOfOrder is an Append whose offset is past what has been
	// received — a gap the client has no business creating, since chunks
	// are supposed to be sent back to back.
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
)
