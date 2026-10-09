// Package covers owns the picture above a contest: what an organiser may
// upload, what is kept of it, under what name, and what a visitor with no
// session may read. It does not know where files live (filestore), who may
// edit a contest (internal/api), or how a cover-less contest looks (drawn in
// the browser).
//
// Uploads are re-encoded so nothing hidden in the source survives, files are
// named by the output's hash so they can be cached forever, and only the
// orphan sweep deletes a file.
package covers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Sentinels for every refusal this package makes (CLAUDE.md security rule 1).
var (
	// ErrNotFound is a contest with no cover, a contest a visitor may not
	// see, or a size this service does not store.
	ErrNotFound = errors.New("no cover for that contest")

	ErrTooLarge = errors.New("the upload is larger than a cover may be")

	// ErrImageKind is anything but the accepted raster formats, SVG included.
	ErrImageKind = errors.New("the file is not a picture this service accepts")

	// ErrImageTooLarge is a source past MaxSourcePixels or MaxSourceArea:
	// about the picture, where ErrTooLarge is about the file.
	ErrImageTooLarge = errors.New("the picture has more pixels on a side than a cover may have")

	// ErrAttributionRequired is an upload with no credit line.
	ErrAttributionRequired = errors.New("an uploaded cover needs a line saying whose picture it is")

	ErrAttributionTooLong = errors.New("the attribution is longer than the column will hold")
)

// MaxAttributionLen bounds the credit line (CLAUDE.md security rule 2); it is
// read back on every front page.
const MaxAttributionLen = 200

// Cover is one contest's uploaded picture as the database holds it: the hash,
// not the bytes.
type Cover struct {
	ContestID   uuid.UUID
	Hash        string
	Attribution string
	// Width and Height are the largest stored rendition's.
	Width, Height int
	UploadedAt    time.Time
	// UploadedBy is uuid.Nil once the account is gone (ON DELETE SET NULL).
	UploadedBy uuid.UUID
}

// Repository is the storage this package needs.
type Repository interface {
	// Save replaces whatever cover the contest had; one row per contest.
	Save(ctx context.Context, cover Cover) error
	// ByContest returns the cover of any contest, draft included, or
	// ErrNotFound.
	ByContest(ctx context.Context, contestID uuid.UUID) (Cover, error)
	// PublicByContest returns the cover only when a visitor without a
	// session may see the contest (the showcase's status selection), so a
	// draft's cover stays private.
	PublicByContest(ctx context.Context, contestID uuid.UUID) (Cover, error)
	// Delete removes the row; the files stay for the sweep.
	Delete(ctx context.Context, contestID uuid.UUID) error
}

// Files is the file store, narrowed to the two calls this package makes
// (CLAUDE.md layout rule 3). It has no Delete: nothing here removes a file.
type Files interface {
	Put(ctx context.Context, key string, contentType string, body []byte) error
	Get(ctx context.Context, key string) ([]byte, string, error)
}

type Service struct {
	covers Repository
	files  Files
}

func NewService(repo Repository, files Files) *Service {
	return &Service{covers: repo, files: files}
}

// Key names one rendition's file: the hash of the output and the width.
func Key(hash string, size int) string {
	return hash + "-" + strconv.Itoa(size) + ".jpg"
}

// Upload processes a picture, writes its renditions and records the cover.
//
// The credit line is checked before a byte is read, ahead of the expensive
// decode (CLAUDE.md rule 13). Files are written before the row: an
// interruption then leaves an orphan for the sweep rather than a row pointing
// at a missing file.
func (s *Service) Upload(ctx context.Context, contestID uuid.UUID, actorID uuid.UUID, src io.Reader, attribution string) (Cover, error) {
	// NUL and invalid UTF-8 cannot be stored or seen, so they are dropped.
	credit := strings.TrimSpace(strings.ReplaceAll(strings.ToValidUTF8(attribution, ""), "\x00", ""))
	switch {
	case credit == "":
		return Cover{}, ErrAttributionRequired
	case len([]rune(credit)) > MaxAttributionLen:
		return Cover{}, fmt.Errorf("%w: at most %d characters", ErrAttributionTooLong, MaxAttributionLen)
	}

	processed, err := Process(src)
	if err != nil {
		return Cover{}, err
	}

	for _, rendition := range processed.Renditions {
		if err := s.files.Put(ctx, Key(processed.Hash, rendition.Size), rendition.ContentType, rendition.Bytes); err != nil {
			return Cover{}, fmt.Errorf("store the %dpx cover of contest %s: %w", rendition.Size, contestID, err)
		}
	}

	cover := Cover{
		ContestID: contestID, Hash: processed.Hash, Attribution: credit,
		Width: processed.Width, Height: processed.Height,
		UploadedAt: time.Now().UTC(), UploadedBy: actorID,
	}
	if err := s.covers.Save(ctx, cover); err != nil {
		return Cover{}, fmt.Errorf("record the cover of contest %s: %w", contestID, err)
	}
	return cover, nil
}

// Remove deletes a contest's cover row; the files are left for the sweep.
func (s *Service) Remove(ctx context.Context, contestID uuid.UUID) error {
	if err := s.covers.Delete(ctx, contestID); err != nil {
		return fmt.Errorf("remove the cover of contest %s: %w", contestID, err)
	}
	return nil
}

func (s *Service) ByContest(ctx context.Context, contestID uuid.UUID) (Cover, error) {
	return s.covers.ByContest(ctx, contestID)
}

// Public is the cover of a contest a visitor with no session may see. No such
// contest, no cover and a draft are all ErrNotFound, so the public endpoint
// does not reveal that an unpublished contest exists.
func (s *Service) Public(ctx context.Context, contestID uuid.UUID) (Cover, error) {
	return s.covers.PublicByContest(ctx, contestID)
}

// Read returns one rendition's bytes and the type they are served as.
//
// hash comes from a stored row, never from the request, so a visitor cannot
// name a file; a size outside Sizes is ErrNotFound. A store failure is
// returned as is: a row naming a missing file means the volume lost something
// and deserves a 500, not a quiet 404.
func (s *Service) Read(ctx context.Context, hash string, size int) ([]byte, string, error) {
	if !slices.Contains(Sizes, size) {
		return nil, "", fmt.Errorf("%w: no cover is stored at %dpx", ErrNotFound, size)
	}
	if !validHash(hash) {
		return nil, "", fmt.Errorf("%w: %q is not a cover's name", ErrNotFound, hash)
	}

	body, contentType, err := s.files.Get(ctx, Key(hash, size))
	if err != nil {
		return nil, "", fmt.Errorf("read the %dpx cover %s: %w", size, hash, err)
	}
	return body, contentType, nil
}

const hashLen = 64

// validHash checks the shape of a name before it reaches the store, which
// validates keys independently; this only saves a syscall.
func validHash(hash string) bool {
	if len(hash) != hashLen {
		return false
	}
	for _, r := range hash {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
