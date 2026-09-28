// Package covers owns the picture above a contest.
//
// It answers: what an organiser is allowed to upload, what is kept of it,
// under what name, and which of those bytes a visitor with no session may
// read. It deliberately does not answer: where the files physically live
// (internal/platform/filestore keeps one directory; this package knows only
// the port below), how a contest is authorised (internal/api asks the
// authoriser before any of this is called), or what a contest with no
// uploaded cover looks like — that one is drawn in the browser from the
// contest's own identifier and touches nothing here at all.
//
// The design decisions behind it are recorded in
// docs/ARCHITECTURE.md §9.7.
// Three of them shape every line here:
//
//   - Whatever arrives is re-encoded rather than stored. What we serve is a
//     file this process wrote, so an EXIF tag with the author's home
//     coordinates, a second document appended after the end of the picture
//     and a polyglot that is also valid HTML all cease to exist rather than
//     being searched for. See Process.
//   - The file's name is the hash of the output. A cover's address changes
//     exactly when its picture does, which is what makes serving it with a
//     year of immutable caching correct rather than merely fast.
//   - A file is never deleted by the request that stops referring to it.
//     Removing a cover removes the row; the files are collected by the same
//     sweep that already collects orphaned game databases. A file system that
//     refuses a delete must not be able to fail an organiser's edit.
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

// Sentinels for every refusal this package makes (CLAUDE.md, security rule
// 1). Each one exists so that internal/api's fail switch can name it and
// answer with a code the interface can translate: a bare errors.New here
// would reach a client as "internal error" for its own typo.
var (
	// ErrNotFound is a contest with no cover, a contest a visitor may not
	// see, or a size this service does not store.
	ErrNotFound = errors.New("no cover for that contest")

	// ErrTooLarge is a source body past MaxUploadBytes.
	ErrTooLarge = errors.New("the upload is larger than a cover may be")

	// ErrImageKind is anything that is not one of the raster formats this
	// package decodes — an SVG above all, whatever is inside it.
	ErrImageKind = errors.New("the file is not a picture this service accepts")

	// ErrImageTooLarge is a source declaring more than MaxSourcePixels on a
	// side. Separate from ErrTooLarge because the two say different things to
	// the person uploading: one is about the file, the other about the
	// picture, and a photograph can easily be one without being the other.
	ErrImageTooLarge = errors.New("the picture has more pixels on a side than a cover may have")

	// ErrAttributionRequired is an upload with no credit line. Design spec
	// §10.1: an uploaded picture belongs to somebody, and a contest wearing
	// one without saying whose it is does not publish.
	ErrAttributionRequired = errors.New("an uploaded cover needs a line saying whose picture it is")

	// ErrAttributionTooLong is a credit line past MaxAttributionLen.
	ErrAttributionTooLong = errors.New("the attribution is longer than the column will hold")
)

// MaxAttributionLen bounds the credit line (CLAUDE.md, security rule 2).
//
// The column is unbounded text and the request is bounded at eight
// mebibytes, so without this a credit line could be a megabyte of prose that
// is then read back on every front page. Two hundred characters holds a
// photographer, a source and a licence with room to spare.
const MaxAttributionLen = 200

// Cover is one contest's uploaded picture, as the database holds it.
//
// It carries the hash rather than the bytes: a page listing eight contests
// needs to know which of them have a cover and at what address, and pulling
// eight photographs across to find out would make the front page the
// heaviest read in the installation.
type Cover struct {
	ContestID   uuid.UUID
	Hash        string
	Attribution string
	// Width and Height are the largest rendition's, which is what was
	// actually stored rather than what was uploaded.
	Width, Height int
	UploadedAt    time.Time
	// UploadedBy is uuid.Nil once the account is gone: the record outlives
	// it, which is what the column's ON DELETE SET NULL promises.
	UploadedBy uuid.UUID
}

// Repository is the storage this package needs, declared here and
// implemented by internal/postgres (CLAUDE.md, Go layout rule 3).
type Repository interface {
	// Save replaces whatever cover the contest had. One row per contest, so
	// a re-upload leaves no second row for anything to choose between.
	Save(ctx context.Context, cover Cover) error
	// ByContest returns the cover of any contest, draft included: the
	// organiser editing it has to see what they uploaded. ErrNotFound when
	// there is none.
	ByContest(ctx context.Context, contestID uuid.UUID) (Cover, error)
	// PublicByContest returns the cover only when the contest itself is one
	// a visitor without a session may see — the same selection of statuses
	// the public list makes (internal/postgres.Showcase.Recent). A draft's
	// cover is as private as its questions: the file belongs to the
	// olympiad, so it answers to the olympiad's own visibility rather than
	// to whether somebody guessed a hash.
	PublicByContest(ctx context.Context, contestID uuid.UUID) (Cover, error)
	// Delete removes the row. The files stay for the sweep.
	Delete(ctx context.Context, contestID uuid.UUID) error
}

// Files is the file store, narrowed to the two calls this package makes.
//
// Declared here rather than imported, so the domain names what it needs and
// internal/platform/filestore satisfies it structurally without either
// package importing the other (CLAUDE.md, Go layout rules 3 and 7). Delete is
// deliberately absent: nothing in this package removes a file, and an
// interface carrying a method its only consumer never calls is an invitation
// to call it.
type Files interface {
	Put(ctx context.Context, key string, contentType string, body []byte) error
	Get(ctx context.Context, key string) ([]byte, string, error)
}

// Service is what an organiser's upload and a visitor's read both go through.
type Service struct {
	covers Repository
	files  Files
}

// NewService returns the service over its two stores.
func NewService(repo Repository, files Files) *Service {
	return &Service{covers: repo, files: files}
}

// Key names one rendition's file: the hash of the output and the width.
//
// Both halves matter. The hash makes the address change with the picture,
// which is what a year of immutable caching rests on; the width keeps the two
// renditions of one upload apart, since they are the same cover and differ
// only in how much of it there is.
func Key(hash string, size int) string {
	return hash + "-" + strconv.Itoa(size) + ".jpg"
}

// Upload processes a picture, writes its renditions and records the cover.
//
// The credit line is checked first, before a single byte is read: refusing
// after the decode would spend the resampling of a photograph on a request
// that was always going to be refused (CLAUDE.md, security rule 13 applied to
// the expensive step rather than to the rate limit alone).
//
// The files are written before the row, and that order is deliberate. A row
// pointing at a file that was never written is a broken cover on the front
// page; a file no row points at is one more thing for the sweep, which
// already exists. Of the two ways this can be interrupted, only one is
// visible to a visitor.
func (s *Service) Upload(ctx context.Context, contestID uuid.UUID, actorID uuid.UUID, src io.Reader, attribution string) (Cover, error) {
	credit := strings.TrimSpace(attribution)
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

// Remove forgets a contest's cover.
//
// The row only. The files are left for the sweep that already collects
// orphaned game databases (design spec §4): a file system that refuses a
// delete would otherwise be able to fail an organiser's edit, and the two
// failures have nothing to do with each other.
func (s *Service) Remove(ctx context.Context, contestID uuid.UUID) error {
	if err := s.covers.Delete(ctx, contestID); err != nil {
		return fmt.Errorf("remove the cover of contest %s: %w", contestID, err)
	}
	return nil
}

// ByContest is the cover as the organiser editing that contest sees it.
func (s *Service) ByContest(ctx context.Context, contestID uuid.UUID) (Cover, error) {
	return s.covers.ByContest(ctx, contestID)
}

// Public is the cover of a contest a visitor with no session may see, or
// ErrNotFound when there is no such contest, no such cover, or the contest is
// still a draft.
//
// One answer for all three on purpose: telling a stranger apart "this
// olympiad exists but is not published yet" from "this olympiad does not
// exist" is a fact about an unpublished contest, and this endpoint is open to
// the whole internet.
func (s *Service) Public(ctx context.Context, contestID uuid.UUID) (Cover, error) {
	return s.covers.PublicByContest(ctx, contestID)
}

// Read returns one rendition's bytes and the type they are served as.
//
// hash comes from a row this service wrote, never from the request: the
// public route reads the contest's cover first and asks for that hash, so a
// visitor cannot name a file. size does come from the request, and anything
// outside Sizes is ErrNotFound rather than a refusal of its own — a width we
// do not store is a file that does not exist, which is exactly what it is.
//
// A store failure is returned as it is, not folded into ErrNotFound. A row
// naming a file the volume does not hold is not a visitor's mistake: the
// sweep only removes files nothing refers to, so this means the volume lost
// something, and that is a line in the log and a 500 rather than a quiet 404
// that nobody investigates.
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

// hashLen is the length of a SHA-256 sum in hexadecimal.
const hashLen = 64

// validHash checks the shape of a name before it reaches the store.
//
// The store checks its own keys and refuses anything that could name a second
// directory — that is its guarantee and it does not depend on this one. This
// is the cheaper check in front of it: a row with a hash that cannot be one
// is a row this service never wrote, and asking the file system about it
// would be one syscall spent on an answer already known.
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
